package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// CodeReviewStreams fans org-scoped code review lifecycle events out over Redis
// pub/sub so the code reviews page can refresh live instead of polling or
// relying on a manual refresh button. Mirrors PullRequestStreams.
type CodeReviewStreams struct {
	client *Client
	logger zerolog.Logger
}

type CodeReviewSubscription struct {
	C <-chan models.CodeReviewUpdatedEvent

	ch     chan models.CodeReviewUpdatedEvent
	cancel context.CancelFunc
	pubsub *redis.PubSub

	mu          sync.Mutex
	closeReason string
}

func (s *CodeReviewSubscription) setCloseReason(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeReason = reason
}

func NewCodeReviewStreams(client *Client, logger zerolog.Logger) *CodeReviewStreams {
	if client == nil {
		return nil
	}
	return &CodeReviewStreams{
		client: client,
		logger: logger,
	}
}

func (s *CodeReviewStreams) Available() bool {
	return s != nil && s.client != nil && s.client.Available()
}

func (s *CodeReviewStreams) PublishUpdated(ctx context.Context, orgID uuid.UUID, event models.CodeReviewUpdatedEvent) error {
	if s == nil || s.client == nil {
		return nil
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal code review update event: %w", err)
	}

	if err := s.client.doCommand(ctx, "publish", func() error {
		return s.client.raw().Publish(ctx, codeReviewStreamChannel(orgID), payload).Err()
	}); err != nil {
		return fmt.Errorf("publish code review update event: %w", err)
	}
	return nil
}

func (s *CodeReviewStreams) Subscribe(orgID uuid.UUID) (*CodeReviewSubscription, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("redis unavailable")
	}
	if !s.client.Available() {
		return nil, errors.New("redis unavailable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	pubsub := s.client.raw().Subscribe(ctx, codeReviewStreamChannel(orgID))
	if _, err := pubsub.Receive(ctx); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, fmt.Errorf("subscribe code review stream: %w", err)
	}

	sub := &CodeReviewSubscription{
		ch:     make(chan models.CodeReviewUpdatedEvent, 32),
		C:      nil,
		cancel: cancel,
		pubsub: pubsub,
	}
	sub.C = sub.ch

	go func() {
		defer close(sub.ch)
		defer cancel()

		for msg := range pubsub.Channel() {
			var event models.CodeReviewUpdatedEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				s.logger.Warn().Err(err).Str("channel", msg.Channel).Msg("failed to decode code review update event")
				continue
			}

			select {
			case sub.ch <- event:
			case <-ctx.Done():
				sub.setCloseReason(ctx.Err().Error())
				return
			}
		}

		if err := ctx.Err(); err != nil {
			sub.setCloseReason(err.Error())
			return
		}
		sub.setCloseReason("subscription closed")
	}()

	return sub, nil
}

func (s *CodeReviewSubscription) Close() {
	if s == nil {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.pubsub != nil {
		_ = s.pubsub.Close()
	}
}

func (s *CodeReviewSubscription) CloseReason() string {
	if s == nil {
		return "subscription closed"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeReason == "" {
		return "subscription closed"
	}
	return s.closeReason
}

func codeReviewStreamChannel(orgID uuid.UUID) string {
	return fmt.Sprintf("143:stream:{org:%s}:code_reviews", orgID.String())
}
