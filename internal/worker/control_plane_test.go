package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRenewLeaseLoopBoundsAttemptsAndKnownExpiry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                         string
		interval, timeout, remaining time.Duration
		blocked                      bool
		lateSuccess                  bool
		wantCalls                    bool
	}{
		{name: "hung renewal is cut off at actual expiry", interval: 5 * time.Millisecond, timeout: time.Second, remaining: 50 * time.Millisecond, blocked: true, wantCalls: true},
		{name: "expiry does not wait for next tick", interval: time.Second, timeout: time.Second, remaining: 30 * time.Millisecond},
		{name: "failed short attempt does not extend expiry", interval: 20 * time.Millisecond, timeout: 5 * time.Millisecond, remaining: 35 * time.Millisecond, blocked: true, wantCalls: true},
		{name: "success after attempt deadline does not extend expiry", interval: 20 * time.Millisecond, timeout: 5 * time.Millisecond, remaining: 35 * time.Millisecond, blocked: true, lateSuccess: true, wantCalls: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jobID, token := uuid.New(), uuid.New()
			expiry := time.Now().Add(tt.remaining)
			var calls atomic.Int32
			var invalidFence atomic.Bool
			renewer := &renewLeaseStoreStub{renewLeaseFn: func(ctx context.Context, gotJob, gotToken uuid.UUID, _ time.Duration) (*models.Job, bool, error) {
				calls.Add(1)
				deadline, ok := ctx.Deadline()
				if gotJob != jobID || gotToken != token || !ok || deadline.After(expiry) {
					invalidFence.Store(true)
				}
				if tt.blocked {
					<-ctx.Done()
					if tt.lateSuccess {
						lateExpiry := expiry.Add(time.Hour)
						return &models.Job{ID: jobID, LockToken: &token, LeaseExpiresAt: &lateExpiry}, true, nil
					}
					return nil, false, ctx.Err()
				}
				return nil, false, errors.New("unavailable")
			}}
			w := New(nil, zerolog.Nop(), "test-worker", WithLeaseRenewer(renewer))
			w.renewInterval, w.renewTimeout = tt.interval, tt.timeout
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var lost atomic.Bool
			done := make(chan struct{})
			go w.renewLeaseLoop(ctx, jobID, token, expiry, &lost, cancel, done)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("known expiry must cancel a blocked renewal and handler")
			}
			require.True(t, lost.Load(), "actual expiry must fence the handler")
			require.Equal(t, tt.wantCalls, calls.Load() > 0, "renewal must run only when a tick precedes expiry")
			require.False(t, invalidFence.Load(), "renewal must retain the exact job token and end by known expiry")
			require.ErrorIs(t, ctx.Err(), context.Canceled, "lost ownership must cancel handler work")
		})
	}
}

func TestRenewLeaseLoopFencesWhileCancellationUnwinds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		cancelParent bool
	}{
		{name: "confirmed expiry fences before cancellation finishes"},
		{name: "normal completion remains distinct from lease loss", cancelParent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entered, unwinding, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseRenewal := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseRenewal()
			jobID, token := uuid.New(), uuid.New()
			expiry := time.Now().Add(150 * time.Millisecond)
			renewer := &renewLeaseStoreStub{renewLeaseFn: func(ctx context.Context, _, _ uuid.UUID, _ time.Duration) (*models.Job, bool, error) {
				close(entered)
				<-ctx.Done()
				close(unwinding)
				<-release // Model bounded protocol cancellation still in progress.
				lateExpiry := expiry.Add(time.Hour)
				return &models.Job{ID: jobID, LockToken: &token, LeaseExpiresAt: &lateExpiry}, true, nil
			}}
			w := New(nil, zerolog.Nop(), "test-worker", WithLeaseRenewer(renewer))
			w.renewInterval, w.renewTimeout = time.Millisecond, 50*time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var lost atomic.Bool
			var handlerCancels atomic.Int32
			handlerCanceled := make(chan struct{})
			done := make(chan struct{})
			go w.renewLeaseLoop(ctx, jobID, token, expiry, &lost, func() { handlerCancels.Add(1); close(handlerCanceled); cancel() }, done)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("renewal must enter before testing cancellation unwind")
			}
			if tt.cancelParent {
				cancel()
			}
			select {
			case <-unwinding:
			case <-time.After(time.Second):
				t.Fatal("renewal deadline or normal completion must start cancellation")
			}
			if !tt.cancelParent {
				select {
				case <-handlerCanceled:
				case <-time.After(time.Second):
					t.Fatal("known expiry must fence the handler before the blocked renewal returns")
				}
			}
			select {
			case <-done:
				t.Fatal("renewal loop must join cancellation rather than detach the database call")
			default:
			}
			require.Equal(t, !tt.cancelParent, lost.Load(), "only confirmed lease expiry may classify ownership as lost during cancellation unwind")
			wantCancels := int32(1)
			if tt.cancelParent {
				wantCancels = 0
			}
			require.Equal(t, wantCancels, handlerCancels.Load(), "normal completion must not invoke the lease-loss callback")
			releaseRenewal()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("completed protocol cancellation must join promptly")
			}
		})
	}
}

func TestRenewLeaseLoopCancellationDoesNotDeclareOwnershipLost(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	renewer := &renewLeaseStoreStub{renewLeaseFn: func(ctx context.Context, _, _ uuid.UUID, _ time.Duration) (*models.Job, bool, error) {
		close(entered)
		<-ctx.Done()
		return nil, false, ctx.Err()
	}}
	w := New(nil, zerolog.Nop(), "test-worker", WithLeaseRenewer(renewer))
	w.renewInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var lost atomic.Bool
	var handlerCancels atomic.Int32
	done := make(chan struct{})
	go w.renewLeaseLoop(ctx, uuid.New(), uuid.New(), time.Now().Add(time.Minute), &lost, func() { handlerCancels.Add(1) }, done)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("renewal should start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler completion or handoff must unblock renewal")
	}
	require.False(t, lost.Load(), "handler cancellation is not proof of lost ownership")
	require.Equal(t, int32(0), handlerCancels.Load(), "the renewal loop must not reclassify normal completion as lease loss")
}

func TestRenewLeaseLoopRecoversAfterBoundedFailure(t *testing.T) {
	t.Parallel()
	jobID, token := uuid.New(), uuid.New()
	initialExpiry := time.Now().Add(100 * time.Millisecond)
	extendedExpiry := initialExpiry.Add(time.Second)
	var calls atomic.Int32
	confirmed := make(chan struct{})
	renewer := &renewLeaseStoreStub{renewLeaseFn: func(ctx context.Context, _, _ uuid.UUID, _ time.Duration) (*models.Job, bool, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return nil, false, ctx.Err()
		}
		select {
		case <-confirmed:
		default:
			close(confirmed)
		}
		return &models.Job{ID: jobID, LockToken: &token, LeaseExpiresAt: &extendedExpiry}, true, nil
	}}
	w := New(nil, zerolog.Nop(), "test-worker", WithLeaseRenewer(renewer))
	w.renewInterval, w.renewTimeout = 5*time.Millisecond, 5*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var lost atomic.Bool
	done := make(chan struct{})
	go w.renewLeaseLoop(ctx, jobID, token, initialExpiry, &lost, cancel, done)
	select {
	case <-confirmed:
	case <-time.After(time.Second):
		t.Fatal("later renewal must recover after the bounded failed call")
	}
	select {
	case <-done:
		t.Fatal("confirmed renewal must keep the handler alive past its original expiry")
	case <-time.After(time.Until(initialExpiry) + 20*time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation should join renewal promptly")
	}
	require.False(t, lost.Load(), "only confirmed expiry may extend ownership, and successful renewal must survive the old deadline")
	require.GreaterOrEqual(t, calls.Load(), int32(2), "a bounded failure must allow a later renewal")
}

type controlPlaneClaimStore struct {
	jobLeaseStore
	job            *models.Job
	terminalWrites atomic.Int32
}

func (s *controlPlaneClaimStore) ClaimNextRunnable(context.Context, string, string, uuid.UUID, time.Duration) (*models.Job, error) {
	return s.job, nil
}
func (s *controlPlaneClaimStore) MarkSucceededWithLease(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	s.terminalWrites.Add(1)
	return true, nil
}

func TestExpiredHungRenewalSkipsTerminalWrite(t *testing.T) {
	t.Parallel()
	token := uuid.New()
	expiry := time.Now().Add(40 * time.Millisecond)
	jobs := &controlPlaneClaimStore{job: &models.Job{ID: uuid.New(), OrgID: uuid.New(), JobType: "test", LockToken: &token, LeaseExpiresAt: &expiry}}
	renewer := &renewLeaseStoreStub{renewLeaseFn: func(ctx context.Context, _, _ uuid.UUID, _ time.Duration) (*models.Job, bool, error) {
		<-ctx.Done()
		return nil, false, ctx.Err()
	}}
	w := New(nil, zerolog.Nop(), "test-worker", WithLeaseRenewer(renewer))
	w.jobs, w.renewInterval = jobs, time.Millisecond
	w.Register("test", func(ctx context.Context, _ string, _ json.RawMessage) error { <-ctx.Done(); return nil })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w.poll(ctx)
	require.NoError(t, ctx.Err(), "known lease expiry must stop the handler before the outer test deadline")
	require.Equal(t, int32(0), jobs.terminalWrites.Load(), "handler completion after lease loss must not issue a terminal write")
}
