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

type PullRequestStreams struct {
	client *Client
	logger zerolog.Logger
}

type PullRequestSubscription struct {
	C <-chan models.PullRequestUpdatedEvent
	*jsonStreamSubscription
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
	ch, state, err := subscribeJSONStream[models.PullRequestUpdatedEvent](s.client, s.logger, pullRequestStreamChannel(orgID), "subscribe pull request stream", "failed to decode pull request update event")
	if err != nil {
		return nil, err
	}
	return &PullRequestSubscription{C: ch, jsonStreamSubscription: state}, nil
}

func (s *PullRequestSubscription) Close() {
	if s == nil {
		return
	}
	s.jsonStreamSubscription.close()
}

func (s *PullRequestSubscription) CloseReason() string {
	if s == nil {
		return "subscription closed"
	}
	return s.jsonStreamSubscription.reason()
}

func pullRequestStreamChannel(orgID uuid.UUID) string {
	return fmt.Sprintf("143:stream:{org:%s}:pull_requests", orgID.String())
}
