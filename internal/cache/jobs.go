package cache

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

const jobsNotifyChannel = "143:jobs:notify"

type JobNotifier struct {
	client *Client
	logger zerolog.Logger
}

func NewJobNotifier(client *Client, logger zerolog.Logger) *JobNotifier {
	if client == nil {
		return nil
	}
	return &JobNotifier{client: client, logger: logger}
}

func (n *JobNotifier) Publish(ctx context.Context) error {
	if n == nil || n.client == nil {
		return nil
	}
	return n.client.doCommand(ctx, "publish", func() error {
		return n.client.raw().Publish(ctx, jobsNotifyChannel, "wake").Err()
	})
}

func (n *JobNotifier) Start(ctx context.Context, onNotify func()) {
	if n == nil || n.client == nil || onNotify == nil {
		return
	}
	go n.run(ctx, onNotify)
}

func (n *JobNotifier) run(ctx context.Context, onNotify func()) {
	for {
		if ctx.Err() != nil {
			return
		}

		pubsub := n.client.raw().Subscribe(ctx, jobsNotifyChannel)
		if _, err := pubsub.Receive(ctx); err != nil {
			n.logger.Warn().Err(err).Msg("Redis job subscription failed")
			_ = pubsub.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				_ = pubsub.Close()
				return
			case _, ok := <-ch:
				if !ok {
					_ = pubsub.Close()
					time.Sleep(2 * time.Second)
					goto reconnect
				}
				onNotify()
			}
		}
	reconnect:
	}
}
