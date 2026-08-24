package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

type jsonStreamSubscription struct {
	cancel context.CancelFunc
	pubsub *redis.PubSub

	mu          sync.Mutex
	closeReason string
}

func subscribeJSONStream[T any](client *Client, logger zerolog.Logger, channel, subscribeError, decodeError string) (chan T, *jsonStreamSubscription, error) {
	if client == nil || !client.Available() {
		return nil, nil, errors.New("redis unavailable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	pubsub := client.raw().Subscribe(ctx, channel)
	if _, err := pubsub.Receive(ctx); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, nil, fmt.Errorf("%s: %w", subscribeError, err)
	}

	state := &jsonStreamSubscription{cancel: cancel, pubsub: pubsub}
	ch := make(chan T, 32)
	go func() {
		defer close(ch)
		defer cancel()

		for msg := range pubsub.Channel() {
			var event T
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				logger.Warn().Err(err).Str("channel", msg.Channel).Msg(decodeError)
				continue
			}

			select {
			case ch <- event:
			case <-ctx.Done():
				state.setCloseReason(ctx.Err().Error())
				return
			}
		}

		if err := ctx.Err(); err != nil {
			state.setCloseReason(err.Error())
			return
		}
		state.setCloseReason("subscription closed")
	}()

	return ch, state, nil
}

func (s *jsonStreamSubscription) setCloseReason(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeReason = reason
}

func (s *jsonStreamSubscription) close() {
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

func (s *jsonStreamSubscription) reason() string {
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
