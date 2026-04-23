package cache

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestJobNotifier_NilBehaviors(t *testing.T) {
	t.Parallel()

	var notifier *JobNotifier
	require.NoError(t, notifier.Publish(context.Background()), "nil notifier publish should be a no-op")
	require.NotPanics(t, func() {
		notifier.Start(context.Background(), func() {})
	}, "nil notifier start should be a no-op")
	require.Nil(t, NewJobNotifier(nil, zerolog.Nop()), "constructor should return nil without a client")
}

func TestJobNotifier_PublishAndStartDelivers(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	notifier := NewJobNotifier(client, zerolog.Nop())
	require.NotNil(t, notifier, "constructor should build a notifier when Redis is configured")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	delivered := make(chan struct{}, 1)
	notifier.Start(ctx, func() {
		select {
		case delivered <- struct{}{}:
		default:
		}
	})

	require.Eventually(t, func() bool {
		if err := notifier.Publish(context.Background()); err != nil {
			return false
		}
		select {
		case <-delivered:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond, "publisher should deliver a wake-up once the subscriber is connected")
}

func TestJobNotifier_StartGuardsAndCanceledRun(t *testing.T) {
	t.Parallel()

	client, _ := testRedisClient(t)
	notifier := NewJobNotifier(client, zerolog.Nop())

	require.NotPanics(t, func() {
		notifier.Start(context.Background(), nil)
	}, "Start should ignore nil callbacks")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NotPanics(t, func() {
		notifier.Start(ctx, func() {})
	}, "Start should tolerate already-canceled contexts")
}
