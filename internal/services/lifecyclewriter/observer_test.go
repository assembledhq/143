package lifecyclewriter

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type observation struct {
	id       uuid.UUID
	role     models.LifecycleWriterRole
	build    string
	deadline bool
}

type observationStoreFunc func(context.Context, uuid.UUID, models.LifecycleWriterRole, string) error

func (f observationStoreFunc) Observe(ctx context.Context, id uuid.UUID, role models.LifecycleWriterRole, build string) error {
	return f(ctx, id, role, build)
}

func receiveObservation(t *testing.T, calls <-chan observation) observation {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("the passive observer should attempt a bounded observation without blocking startup")
		return observation{}
	}
}

func TestSessionExecutorObserverIdentityAndRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, build string
	}{
		{name: "actual build", build: "synthetic-build"},
		{name: "honest development build", build: "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := make(chan observation, 2)
			ticks := make(chan time.Time)
			var attempts int
			var logs bytes.Buffer
			var stopped atomic.Bool
			store := observationStoreFunc(func(ctx context.Context, id uuid.UUID, role models.LifecycleWriterRole, build string) error {
				deadline, ok := ctx.Deadline()
				calls <- observation{id: id, role: role, build: build, deadline: ok && time.Until(deadline) <= observationTimeout}
				attempts++
				if attempts == 1 {
					return errors.New("synthetic observation failure")
				}
				return nil
			})
			stop := startSessionExecutor(store, tt.build, zerolog.New(&logs), ticks, func() { stopped.Store(true) }, observationTimeout)
			t.Cleanup(stop)
			first := receiveObservation(t, calls)
			require.NotEqual(t, uuid.Nil, first.id, "each process must have an independent nonzero instance identity")
			expected := observation{id: first.id, role: models.LifecycleWriterRoleSessionExecutor, build: tt.build, deadline: true}
			require.Equal(t, expected, first, "initial observation must use the actual build, fixed role and bounded attempt")
			ticks <- time.Now()
			require.Equal(t, expected, receiveObservation(t, calls), "retry must preserve immutable process identity and metadata")
			stop()
			require.True(t, stopped.Load(), "joining must stop the interval source before returning")
			require.Contains(t, logs.String(), `"level":"warn"`, "failed telemetry must remain observable without failing execution")
			require.Contains(t, logs.String(), "next interval will retry", "failure logs must not claim successful registration")
		})
	}
}

func TestSessionExecutorObserverRestartIdentity(t *testing.T) {
	t.Parallel()
	calls := make(chan observation, 2)
	store := observationStoreFunc(func(_ context.Context, id uuid.UUID, role models.LifecycleWriterRole, build string) error {
		calls <- observation{id: id, role: role, build: build}
		return nil
	})
	firstStop := StartSessionExecutor(store, "synthetic-build", zerolog.Nop())
	t.Cleanup(firstStop)
	first := receiveObservation(t, calls)
	secondStop := StartSessionExecutor(store, "synthetic-build", zerolog.Nop())
	t.Cleanup(secondStop)
	second := receiveObservation(t, calls)
	firstStop()
	secondStop()
	require.NotEqual(t, first.id, second.id, "concurrent processes or restarts must never reuse a node or executor allocation identity")
	require.Equal(t, observation{id: second.id, role: first.role, build: first.build}, second, "independent process identity must not change truthful role and build metadata")
}

func TestSessionExecutorObserverBlockedAttemptAndJoin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		timeout time.Duration
		cancel  bool
	}{
		{name: "attempt deadline permits later retry", timeout: 20 * time.Millisecond},
		{name: "stop cancels and joins in flight attempt", timeout: time.Minute, cancel: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ticks := make(chan time.Time, 1)
			started := make(chan observation, 2)
			ended := make(chan error, 2)
			var active, maxActive atomic.Int32
			store := observationStoreFunc(func(ctx context.Context, id uuid.UUID, role models.LifecycleWriterRole, build string) error {
				current := active.Add(1)
				if current > maxActive.Load() {
					maxActive.Store(current)
				}
				started <- observation{id: id, role: role, build: build}
				<-ctx.Done()
				active.Add(-1)
				ended <- ctx.Err()
				return ctx.Err()
			})
			stop := startSessionExecutor(store, "synthetic-build", zerolog.Nop(), ticks, func() {}, tt.timeout)
			t.Cleanup(stop)
			first := receiveObservation(t, started)
			if tt.cancel {
				stop()
				require.ErrorIs(t, <-ended, context.Canceled, "stop must cancel a blocked attempt and wait for it to exit")
			} else {
				ticks <- time.Now()
				select {
				case err := <-ended:
					require.ErrorIs(t, err, context.DeadlineExceeded, "blocked database attempts must expire without failing the process")
				case <-time.After(2 * time.Second):
					t.Fatal("a cooperative database attempt must respect its deadline")
				}
				require.Equal(t, first, receiveObservation(t, started), "a later observation must preserve identity after a timeout")
				stop()
			}
			require.Equal(t, int32(1), maxActive.Load(), "slow attempts must never accumulate concurrent telemetry writes")
			require.Equal(t, int32(0), active.Load(), "joining must leave no process observation in flight")
		})
	}
}
