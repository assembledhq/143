package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
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
	*jsonStreamSubscription
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
	ch, state, err := subscribeJSONStream[models.CodeReviewUpdatedEvent](s.client, s.logger, codeReviewStreamChannel(orgID), "subscribe code review stream", "failed to decode code review update event")
	if err != nil {
		return nil, err
	}
	return &CodeReviewSubscription{C: ch, jsonStreamSubscription: state}, nil
}

func (s *CodeReviewSubscription) Close() {
	if s == nil {
		return
	}
	s.jsonStreamSubscription.close()
}

func (s *CodeReviewSubscription) CloseReason() string {
	if s == nil {
		return "subscription closed"
	}
	return s.jsonStreamSubscription.reason()
}

func codeReviewStreamChannel(orgID uuid.UUID) string {
	return fmt.Sprintf("143:stream:{org:%s}:code_reviews", orgID.String())
}
