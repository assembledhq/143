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

type PullRequestStreams struct {
	client *Client
	logger zerolog.Logger
}

type PullRequestSubscription struct {
	C <-chan models.PullRequestUpdatedEvent

	ch     chan models.PullRequestUpdatedEvent
	cancel context.CancelFunc
	pubsub *redis.PubSub

	mu          sync.Mutex
	closeReason string
}

func (s *PullRequestSubscription) setCloseReason(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeReason = reason
}

func NewPullRequestStreams(client *Client, logger zerolog.Logger) *PullRequestStreams {
	if client == nil {
		return nil
	}
	return &PullRequestStreams{
		client: client,
		logger: logger,
	}
}

func (s *PullRequestStreams) Available() bool {
	return s != nil && s.client != nil && s.client.Available()
}

func (s *PullRequestStreams) PublishUpdated(ctx context.Context, orgID uuid.UUID, event models.PullRequestUpdatedEvent) error {
	if s == nil || s.client == nil {
		return nil
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal pull request update event: %w", err)
	}

	if err := s.client.doCommand(ctx, "publish", func() error {
		return s.client.raw().Publish(ctx, pullRequestStreamChannel(orgID), payload).Err()
	}); err != nil {
		return fmt.Errorf("publish pull request update event: %w", err)
	}
	return nil
}

func (s *PullRequestStreams) Subscribe(orgID uuid.UUID) (*PullRequestSubscription, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("redis unavailable")
	}
	if !s.client.Available() {
		return nil, errors.New("redis unavailable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	pubsub := s.client.raw().Subscribe(ctx, pullRequestStreamChannel(orgID))
	if _, err := pubsub.Receive(ctx); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, fmt.Errorf("subscribe pull request stream: %w", err)
	}

	sub := &PullRequestSubscription{
		ch:     make(chan models.PullRequestUpdatedEvent, 32),
		C:      nil,
		cancel: cancel,
		pubsub: pubsub,
	}
	sub.C = sub.ch

	go func() {
		defer close(sub.ch)
		defer cancel()

		for msg := range pubsub.Channel() {
			var event models.PullRequestUpdatedEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				s.logger.Warn().Err(err).Str("channel", msg.Channel).Msg("failed to decode pull request update event")
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

func (s *PullRequestSubscription) Close() {
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

func (s *PullRequestSubscription) CloseReason() string {
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

func pullRequestStreamChannel(orgID uuid.UUID) string {
	return fmt.Sprintf("143:stream:{org:%s}:pull_requests", orgID.String())
}
