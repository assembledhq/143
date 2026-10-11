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

// EvalBatchStreams publishes lightweight "something changed" signals for eval
// batch progress over Redis pub/sub. The channel is keyed by batch ID so a
// detail-page subscriber only wakes for events that actually concern its
// batch — orgs with many in-flight batches don't fan out cross-batch noise to
// every viewer. Events deliberately carry only the fields needed to identify
// the change; clients fetch the full EvalBatchDetail via the existing GET
// handler when they receive an event, which both keeps the payload bounded
// for large batches and side-steps Redis pub/sub's at-most-once / unordered
// delivery (the canonical state always lives in Postgres).
type EvalBatchStreams struct {
	client *Client
	logger zerolog.Logger
}

type EvalBatchSubscription struct {
	C     <-chan models.EvalBatchUpdatedEvent
	state *evalSubscriptionState[models.EvalBatchUpdatedEvent]
}

func NewEvalBatchStreams(client *Client, logger zerolog.Logger) *EvalBatchStreams {
	if client == nil {
		return nil
	}
	return &EvalBatchStreams{client: client, logger: logger}
}

func (s *EvalBatchStreams) Available() bool {
	return s != nil && s.client != nil && s.client.Available()
}

// PublishUpdated fans out an event keyed on the batch ID embedded in the
// event itself. Callers must populate event.BatchID before invoking.
func (s *EvalBatchStreams) PublishUpdated(ctx context.Context, event models.EvalBatchUpdatedEvent) error {
	if s == nil || s.client == nil {
		return nil
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal eval batch update event: %w", err)
	}

	if err := s.client.doCommand(ctx, "publish", func() error {
		return s.client.raw().Publish(ctx, evalBatchStreamChannel(event.BatchID), payload).Err()
	}); err != nil {
		return fmt.Errorf("publish eval batch update event: %w", err)
	}
	return nil
}

// Subscribe attaches to the per-batch channel. Subscribers see only events
// for the requested batch ID — there is no server-side filter to maintain
// in callers, and unrelated batches in the same org cause no fanout.
func (s *EvalBatchStreams) Subscribe(batchID uuid.UUID) (*EvalBatchSubscription, error) {
	if s == nil {
		return nil, errors.New("redis unavailable")
	}
	state, err := subscribeToEvalStream[models.EvalBatchUpdatedEvent](s.client, s.logger, evalBatchStreamChannel(batchID), "eval batch")
	if err != nil {
		return nil, err
	}
	return &EvalBatchSubscription{C: state.ch, state: state}, nil
}

func (s *EvalBatchSubscription) Close() {
	if s == nil || s.state == nil {
		return
	}
	s.state.close()
}

func (s *EvalBatchSubscription) CloseReason() string {
	if s == nil || s.state == nil {
		return "subscription closed"
	}
	return s.state.reason()
}

// evalBatchStreamChannel scopes the pub/sub channel by batch ID so subscribers
// only see events for the batch they're watching. Per-batch keys (instead of
// per-org) cap fanout at one PUBLISH per batch transition rather than O(orgs ×
// in-flight batches), which matters at scale even though Redis comfortably
// handles 100k+ channels.
func evalBatchStreamChannel(batchID uuid.UUID) string {
	return fmt.Sprintf("143:stream:{batch:%s}:eval_batches", batchID.String())
}

// EvalBootstrapStreams mirrors EvalBatchStreams for bootstrap (PR-history scan)
// runs and is similarly keyed per-bootstrap-run. Bootstrap is rarer than batch
// activity but the same scoping rationale applies — subscribers only care
// about the run they're watching.
type EvalBootstrapStreams struct {
	client *Client
	logger zerolog.Logger
}

type EvalBootstrapSubscription struct {
	C     <-chan models.EvalBootstrapUpdatedEvent
	state *evalSubscriptionState[models.EvalBootstrapUpdatedEvent]
}

func NewEvalBootstrapStreams(client *Client, logger zerolog.Logger) *EvalBootstrapStreams {
	if client == nil {
		return nil
	}
	return &EvalBootstrapStreams{client: client, logger: logger}
}

func (s *EvalBootstrapStreams) Available() bool {
	return s != nil && s.client != nil && s.client.Available()
}

// PublishUpdated fans out an event keyed on event.BootstrapRunID.
func (s *EvalBootstrapStreams) PublishUpdated(ctx context.Context, event models.EvalBootstrapUpdatedEvent) error {
	if s == nil || s.client == nil {
		return nil
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal eval bootstrap update event: %w", err)
	}

	if err := s.client.doCommand(ctx, "publish", func() error {
		return s.client.raw().Publish(ctx, evalBootstrapStreamChannel(event.BootstrapRunID), payload).Err()
	}); err != nil {
		return fmt.Errorf("publish eval bootstrap update event: %w", err)
	}
	return nil
}

func (s *EvalBootstrapStreams) Subscribe(runID uuid.UUID) (*EvalBootstrapSubscription, error) {
	if s == nil {
		return nil, errors.New("redis unavailable")
	}
	state, err := subscribeToEvalStream[models.EvalBootstrapUpdatedEvent](s.client, s.logger, evalBootstrapStreamChannel(runID), "eval bootstrap")
	if err != nil {
		return nil, err
	}
	return &EvalBootstrapSubscription{C: state.ch, state: state}, nil
}

func (s *EvalBootstrapSubscription) Close() {
	if s == nil || s.state == nil {
		return
	}
	s.state.close()
}

func (s *EvalBootstrapSubscription) CloseReason() string {
	if s == nil || s.state == nil {
		return "subscription closed"
	}
	return s.state.reason()
}

type evalSubscriptionState[T any] struct {
	ch     chan T
	cancel context.CancelFunc
	pubsub *redis.PubSub

	mu          sync.Mutex
	closeReason string
}

func subscribeToEvalStream[T any](client *Client, logger zerolog.Logger, channel, eventName string) (*evalSubscriptionState[T], error) {
	if client == nil || !client.Available() {
		return nil, errors.New("redis unavailable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	pubsub := client.raw().Subscribe(ctx, channel)
	if _, err := pubsub.Receive(ctx); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, fmt.Errorf("subscribe %s stream: %w", eventName, err)
	}

	state := &evalSubscriptionState[T]{
		ch:     make(chan T, 32),
		cancel: cancel,
		pubsub: pubsub,
	}
	go state.forward(ctx, logger, eventName)
	return state, nil
}

func (s *evalSubscriptionState[T]) forward(ctx context.Context, logger zerolog.Logger, eventName string) {
	defer close(s.ch)
	defer s.cancel()

	for msg := range s.pubsub.Channel() {
		var event T
		if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
			logger.Warn().Err(err).Str("channel", msg.Channel).Msg("failed to decode " + eventName + " update event")
			continue
		}

		select {
		case s.ch <- event:
		case <-ctx.Done():
			s.setCloseReason(ctx.Err().Error())
			return
		}
	}

	if err := ctx.Err(); err != nil {
		s.setCloseReason(err.Error())
		return
	}
	s.setCloseReason("subscription closed")
}

func (s *evalSubscriptionState[T]) setCloseReason(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeReason = reason
}

func (s *evalSubscriptionState[T]) close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.pubsub != nil {
		_ = s.pubsub.Close()
	}
}

func (s *evalSubscriptionState[T]) reason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeReason == "" {
		return "subscription closed"
	}
	return s.closeReason
}

func evalBootstrapStreamChannel(runID uuid.UUID) string {
	return fmt.Sprintf("143:stream:{bootstrap:%s}:eval_bootstraps", runID.String())
}
