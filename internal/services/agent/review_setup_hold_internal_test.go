package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type reviewSetupHoldCall struct {
	org, session, holder, token uuid.UUID
	kind                        models.SessionSandboxHolderKind
}

type reviewSetupHoldTestStore struct {
	SessionSandboxHolderStore
	mu                          sync.Mutex
	created                     []db.CreateSessionSandboxHolderParams
	released                    []reviewSetupHoldCall
	heartbeats                  []reviewSetupHoldCall
	createErr                   error
	heartbeatErr                error
	heartbeatOK                 bool
	releaseCtxErr               error
	releaseWait                 bool
	createDeadline              time.Time
	heartbeatStarted            chan struct{}
	heartbeatWait               chan struct{}
	heartbeatIgnoreCancellation bool
}

func (s *reviewSetupHoldTestStore) CreateActive(ctx context.Context, _ uuid.UUID, params db.CreateSessionSandboxHolderParams) (models.SessionSandboxHolder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, params)
	s.createDeadline, _ = ctx.Deadline()
	return models.SessionSandboxHolder{}, s.createErr
}

func (s *reviewSetupHoldTestStore) HeartbeatWithLease(ctx context.Context, org, session uuid.UUID, kind models.SessionSandboxHolderKind, holder, token uuid.UUID, _ time.Duration) (bool, error) {
	s.mu.Lock()
	s.heartbeats = append(s.heartbeats, reviewSetupHoldCall{org, session, holder, token, kind})
	s.mu.Unlock()
	if s.heartbeatStarted != nil {
		close(s.heartbeatStarted)
		if s.heartbeatIgnoreCancellation {
			<-s.heartbeatWait
		} else {
			<-ctx.Done()
		}
		return false, ctx.Err()
	}
	return s.heartbeatOK, s.heartbeatErr
}

func (s *reviewSetupHoldTestStore) ReleaseWithLease(ctx context.Context, org, session uuid.UUID, kind models.SessionSandboxHolderKind, holder, token uuid.UUID) (bool, error) {
	s.mu.Lock()
	s.released = append(s.released, reviewSetupHoldCall{org, session, holder, token, kind})
	s.releaseCtxErr = ctx.Err()
	s.mu.Unlock()
	if s.releaseWait {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return true, nil
}

type reviewSetupHoldTestSessions struct {
	SessionStore
	calls    int
	owned    bool
	err      error
	resets   int
	deadline time.Time
}

func (s *reviewSetupHoldTestSessions) AcquireExistingTurnHold(ctx context.Context, _ uuid.UUID, _ uuid.UUID, _ string) (bool, error) {
	s.calls++
	s.deadline, _ = ctx.Deadline()
	return s.owned, s.err
}

func (s *reviewSetupHoldTestSessions) ResetAfterLostReuse(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	s.resets++
	return true, nil
}

func newReviewSetupHoldTestFixture() (*Orchestrator, *models.Session, *Sandbox, *reviewSetupHoldTestStore, *reviewSetupHoldTestSessions) {
	holders := &reviewSetupHoldTestStore{heartbeatOK: true}
	sessions := &reviewSetupHoldTestSessions{owned: true}
	orchestrator := &Orchestrator{sessions: sessions, sandboxHolders: holders, nodeID: "synthetic-worker", logger: zerolog.Nop()}
	session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: models.SessionOriginCodeReview}
	return orchestrator, session, &Sandbox{ID: "shared-review-workspace"}, holders, sessions
}

func TestReviewSetupHoldHeartbeatAndReleaseFences(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		heartbeatOK  bool
		heartbeatErr error
		callerCancel bool
		releaseWait  bool
	}{
		{name: "normal release", heartbeatOK: true},
		{name: "lost heartbeat"},
		{name: "heartbeat error", heartbeatErr: errors.New("holder database unavailable")},
		{name: "caller cancellation", heartbeatOK: true, callerCancel: true},
		{name: "bounded release", heartbeatOK: true, releaseWait: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				o, session, sandbox, holders, sessions := newReviewSetupHoldTestFixture()
				holders.heartbeatOK, holders.heartbeatErr, holders.releaseWait = test.heartbeatOK, test.heartbeatErr, test.releaseWait
				ctx, cancel := context.WithCancelCause(withCodeReviewPreparationBudget(context.Background()))
				defer cancel(nil)
				hold, err := o.acquireReviewSetupHold(ctx, session, sandbox, cancel)
				require.NoError(t, err, "the setup holder should be acquired before preparing inputs")
				require.Equal(t, 1, sessions.calls, "setup must strictly fence the existing container after creating its pin")
				require.Equal(t, time.Now().Add(5*time.Second), holders.createDeadline, "holder creation must use the bounded acquisition write deadline")
				require.Equal(t, holders.createDeadline, sessions.deadline, "strict container fencing must share the same acquisition deadline")
				require.Len(t, holders.created, 1, "one setup holder must be created")
				created := holders.created[0]
				require.Equal(t, models.SessionSandboxHolderKindOperator, created.HolderKind, "preparation must reuse the existing durable operator holder kind")
				require.Equal(t, session.ID, created.SessionID, "holder must remain scoped to the review session")
				require.Equal(t, sandbox.ID, created.ContainerID, "holder must pin the actual shared container")
				require.NotEqual(t, uuid.Nil, created.HolderID, "each setup holder must have a unique identity")
				require.NotEqual(t, uuid.Nil, created.LeaseToken, "each setup holder must have an exact lease token")
				require.Equal(t, 60*time.Second, created.LeaseDuration, "setup pin must have a bounded durable lease")
				time.Sleep(20 * time.Second)
				synctest.Wait()
				if !test.heartbeatOK || test.heartbeatErr != nil {
					require.ErrorIs(t, context.Cause(ctx), ErrWorkerDrainCause, "a failed setup heartbeat must stop preparation immediately")
				} else {
					require.NoError(t, ctx.Err(), "a live heartbeat must preserve preparation")
				}
				if test.callerCancel {
					cancel(ErrUserCancelCause)
				}
				started := time.Now()
				hold.Release()
				hold.Release()
				require.Len(t, holders.released, 1, "deferred and handoff releases must release the same setup pin only once")
				want := reviewSetupHoldCall{session.OrgID, session.ID, created.HolderID, created.LeaseToken, created.HolderKind}
				require.Equal(t, want, holders.released[0], "release must preserve the exact organization, session, holder identity and token")
				require.Equal(t, []reviewSetupHoldCall{want}, holders.heartbeats, "heartbeat must renew only the owned setup pin")
				require.NoError(t, holders.releaseCtxErr, "release must use a detached context after caller or holder cancellation")
				if test.releaseWait {
					require.Equal(t, 5*time.Second, time.Since(started), "blocked release must stop at the bounded database write deadline")
				}
				if test.heartbeatOK && test.heartbeatErr == nil && !test.callerCancel {
					require.NoError(t, ctx.Err(), "normal setup pin release must never cancel the reviewer run")
				}
			})
		})
	}
}

func TestReviewSetupHoldAbsoluteDeadlineStopsPreparation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		o, session, sandbox, holders, _ := newReviewSetupHoldTestFixture()
		started := time.Now()
		deadline := started.Add(time.Minute)
		ctx, cancel := context.WithCancelCause(WithCodeReviewWorkspacePreparationDeadline(context.Background(), deadline))
		defer cancel(nil)
		hold, err := o.acquireReviewSetupHold(ctx, session, sandbox, cancel)
		require.NoError(t, err, "a live preparation budget must acquire a setup pin")
		time.Sleep(2*time.Minute + 10*time.Second)
		synctest.Wait()
		require.ErrorIs(t, context.Cause(ctx), context.DeadlineExceeded, "setup ownership must terminate after the absolute budget plus verification and cleanup allowance")
		hold.Release()
		require.Len(t, holders.released, 1, "an expired pin must still release its exact durable holder")
	})
}

func TestReviewSetupHoldCancellationAndHandoffOrdering(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		cause        error
		releaseFirst bool
	}{
		{name: "late deadline after handoff", cause: context.DeadlineExceeded, releaseFirst: true},
		{name: "late lease loss after handoff", cause: ErrWorkerDrainCause, releaseFirst: true},
		{name: "deadline before handoff", cause: context.DeadlineExceeded},
		{name: "lease loss before handoff", cause: ErrWorkerDrainCause},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			done := make(chan struct{})
			close(done)
			releases := 0
			hold := &reviewSetupHold{stop: func() {}, done: done, release: func() { releases++ }}
			if tt.releaseFirst {
				hold.Release()
			}
			hold.cancelWhileActive(cancel, tt.cause)
			hold.Release()
			if tt.releaseFirst {
				require.NoError(t, context.Cause(ctx), "an observed setup failure arriving after handoff must not cancel the live runtime")
			} else {
				require.ErrorIs(t, context.Cause(ctx), tt.cause, "a failure while the setup lease is active must retain its cancellation fence")
			}
			require.Equal(t, 1, releases, "handoff and deferred cleanup must release the exact setup holder once")
		})
	}
}

func TestReviewSetupHoldFailureAndGenericSession(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		generic   bool
		createErr error
		fenceErr  error
		fenced    bool
	}{
		{name: "generic session unchanged", generic: true},
		{name: "holder creation fails", createErr: errors.New("database unavailable")},
		{name: "late container destruction", fenced: true},
		{name: "container fence unavailable", fenceErr: errors.New("container row unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			o, session, sandbox, holders, sessions := newReviewSetupHoldTestFixture()
			holders.createErr, sessions.err, sessions.owned = test.createErr, test.fenceErr, !test.fenced
			if test.generic {
				session.Origin = models.SessionOriginManual
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			hold, err := o.acquireReviewSetupHold(ctx, session, sandbox, cancel)
			require.Nil(t, hold, "ineligible or failed acquisition must not return a setup pin")
			if test.generic {
				require.NoError(t, err, "generic sessions must retain their existing ownership path")
				require.Empty(t, holders.created, "generic sessions must not create review setup holders")
				return
			}
			if test.fenced {
				require.ErrorIs(t, err, ErrStaleSandboxIDCleared, "a failed strict container fence must stop preparation for durable-state retry")
			} else {
				require.ErrorIs(t, err, ErrSandboxWorkspaceNotReady, "temporary holder writes must preserve the workspace retry")
			}
			require.NoError(t, ctx.Err(), "acquisition failure must not cancel an unrelated run context")
			if test.createErr != nil {
				require.Zero(t, sessions.calls, "a failed creation must not acquire a shared container hold")
				require.Len(t, holders.released, 1, "an uncertain creation error must attempt fenced cleanup of its own token")
				require.Equal(t, holders.created[0].LeaseToken, holders.released[0].token, "uncertain creation cleanup must never release another setup lease")
			} else {
				require.Len(t, holders.created, 1, "strict fencing follows a successfully created setup holder")
				require.Len(t, holders.released, 1, "a failed strict fence must release its newly created pin once")
				require.Equal(t, holders.created[0].LeaseToken, holders.released[0].token, "late destruction cleanup must preserve the exact setup lease token")
			}
			if test.fenced {
				require.Equal(t, 1, sessions.resets, "late container loss must reopen durable session state for retry")
			}
		})
	}
}

func TestReviewSetupHoldReleaseJoinsInflightHeartbeatWithoutCancellingRun(t *testing.T) {
	t.Parallel()
	for _, ignoreCancellation := range []bool{false, true} {
		t.Run(map[bool]string{false: "database honors cancellation", true: "bounded join with stalled database"}[ignoreCancellation], func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				o, session, sandbox, holders, _ := newReviewSetupHoldTestFixture()
				holders.heartbeatStarted, holders.heartbeatWait = make(chan struct{}), make(chan struct{})
				holders.heartbeatIgnoreCancellation = ignoreCancellation
				ctx, cancel := context.WithCancelCause(withCodeReviewPreparationBudget(context.Background()))
				defer cancel(nil)
				hold, err := o.acquireReviewSetupHold(ctx, session, sandbox, cancel)
				require.NoError(t, err, "setup must acquire a live pin before its heartbeat begins")
				time.Sleep(20 * time.Second)
				<-holders.heartbeatStarted
				started := time.Now()
				hold.Release()
				require.NoError(t, ctx.Err(), "intentional handoff cancellation during an in-flight heartbeat must not cancel the reviewer")
				require.Len(t, holders.released, 1, "handoff must release its exact setup holder even if a database call stalls")
				if ignoreCancellation {
					require.Equal(t, 5*time.Second, time.Since(started), "a stalled heartbeat must not block handoff beyond the bounded join allowance")
					close(holders.heartbeatWait)
					synctest.Wait()
					require.NoError(t, ctx.Err(), "a late heartbeat result after handoff must not cancel the reviewer")
				}
				hold.Release()
				require.Len(t, holders.released, 1, "deferred cleanup after handoff must not release the pin twice")
			})
		})
	}
}
