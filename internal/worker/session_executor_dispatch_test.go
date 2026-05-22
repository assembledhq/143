package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
)

type executorCreatorStub struct {
	clearCalls     int
	clearOrgID     uuid.UUID
	clearSessionID uuid.UUID
	clearJobID     uuid.UUID
	calls          int
	orgID          uuid.UUID
	params         models.CreateSessionExecutorParams
	id             uuid.UUID
	err            error
	terminalCalls  int
	terminalStatus models.SessionExecutorStatus
	terminalError  string
}

func (s *executorCreatorStub) ClearPreHandoffReservation(ctx context.Context, orgID, sessionID, jobID uuid.UUID) (int64, error) {
	s.clearCalls++
	s.clearOrgID = orgID
	s.clearSessionID = sessionID
	s.clearJobID = jobID
	return 0, nil
}

func (s *executorCreatorStub) CreateStarting(ctx context.Context, orgID uuid.UUID, params models.CreateSessionExecutorParams) (uuid.UUID, error) {
	s.calls++
	s.orgID = orgID
	s.params = params
	if s.err != nil {
		return uuid.Nil, s.err
	}
	if s.id == uuid.Nil {
		s.id = uuid.New()
	}
	return s.id, nil
}

func (s *executorCreatorStub) MarkTerminalWithLease(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, status models.SessionExecutorStatus, _ *int, lastError string) (bool, error) {
	s.terminalCalls++
	s.terminalStatus = status
	s.terminalError = lastError
	return true, nil
}

type jobHandoffStoreStub struct {
	calls      int
	orgID      uuid.UUID
	jobID      uuid.UUID
	lockToken  uuid.UUID
	executorID uuid.UUID
	ok         bool
	err        error
}

func (s *jobHandoffStoreStub) HandoffToSessionExecutorWithLease(ctx context.Context, orgID, jobID, lockToken, executorID uuid.UUID) (bool, error) {
	s.calls++
	s.orgID = orgID
	s.jobID = jobID
	s.lockToken = lockToken
	s.executorID = executorID
	if s.err != nil {
		return false, s.err
	}
	return s.ok, nil
}

type executorLauncherStub struct {
	calls        int
	cleanupCalls int
	spec         ExecutorLaunchSpec
	cleanupSpec  ExecutorLaunchSpec
	err          error
	cleanupErr   error
}

func (s *executorLauncherStub) Launch(ctx context.Context, spec ExecutorLaunchSpec) error {
	s.calls++
	s.spec = spec
	return s.err
}

func (s *executorLauncherStub) Cleanup(ctx context.Context, spec ExecutorLaunchSpec) error {
	s.cleanupCalls++
	s.cleanupSpec = spec
	return s.cleanupErr
}

func TestDurableSessionExecutorDispatcher_DispatchPreservesLockToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	executorID := uuid.New()
	ctx := jobctx.WithJobID(context.Background(), jobID)
	ctx = jobctx.WithLockToken(ctx, lockToken)

	executors := &executorCreatorStub{id: executorID}
	jobs := &jobHandoffStoreStub{ok: true}
	launcher := &executorLauncherStub{}
	dispatcher := &DurableSessionExecutorDispatcher{
		Executors: executors,
		Jobs:      jobs,
		Launcher:  launcher,
		NodeID:    "worker-a",
		Image:     "ghcr.io/assembledhq/143-server:test",
		BuildSHA:  "build-sha",
	}

	got, err := dispatcher.Dispatch(ctx, "run_agent", models.Session{ID: sessionID, OrgID: orgID}, &threadID)
	require.NoError(t, err, "Dispatch should create, launch, and hand off a session executor")
	require.Equal(t, executorID, got, "Dispatch should return the created executor id")
	require.Equal(t, 1, executors.clearCalls, "Dispatch should clear stale pre-handoff reservations before creating a new executor")
	require.Equal(t, orgID, executors.clearOrgID, "pre-handoff cleanup should be org-scoped")
	require.Equal(t, sessionID, executors.clearSessionID, "pre-handoff cleanup should target the current session")
	require.Equal(t, jobID, executors.clearJobID, "pre-handoff cleanup should only target reservations for the current job")
	require.Equal(t, 1, executors.calls, "Dispatch should create one executor row")
	require.Equal(t, orgID, executors.orgID, "executor row should be org-scoped")
	require.Equal(t, lockToken, executors.params.LockToken, "executor row should preserve the worker lock token")
	require.Equal(t, 1, launcher.calls, "Dispatch should launch exactly one executor")
	require.Equal(t, lockToken, launcher.spec.LockToken, "launch spec should preserve the worker lock token")
	require.Equal(t, 1, jobs.calls, "Dispatch should hand off the job exactly once")
	require.Equal(t, orgID, jobs.orgID, "handoff should be org-scoped")
	require.Equal(t, jobID, jobs.jobID, "handoff should target the running job")
	require.Equal(t, lockToken, jobs.lockToken, "handoff should preserve the existing fencing token")
	require.Equal(t, executorID, jobs.executorID, "handoff should assign ownership to the created executor")
}

func TestDurableSessionExecutorDispatcher_CleansUpWhenLaunchFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	executorID := uuid.New()
	ctx := jobctx.WithJobID(context.Background(), jobID)
	ctx = jobctx.WithLockToken(ctx, lockToken)

	executors := &executorCreatorStub{id: executorID}
	launcher := &executorLauncherStub{err: errors.New("docker unavailable")}
	dispatcher := &DurableSessionExecutorDispatcher{
		Executors: executors,
		Jobs:      &jobHandoffStoreStub{ok: true},
		Launcher:  launcher,
		NodeID:    "worker-a",
	}

	_, err := dispatcher.Dispatch(ctx, "run_agent", models.Session{ID: sessionID, OrgID: orgID}, nil)
	require.Error(t, err, "Dispatch should return launch errors")
	require.Equal(t, 1, executors.terminalCalls, "Dispatch should mark the executor terminal when launch fails")
	require.Equal(t, models.SessionExecutorStatusFailed, executors.terminalStatus, "launch failures should mark the reserved executor failed")
	require.Contains(t, executors.terminalError, "launch session executor", "terminal error should describe the launch failure")
	require.Equal(t, 0, launcher.cleanupCalls, "cleanup should not run when no container was launched")
}

func TestDurableSessionExecutorDispatcher_CleansUpLaunchedContainerWhenHandoffFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	executorID := uuid.New()
	ctx := jobctx.WithJobID(context.Background(), jobID)
	ctx = jobctx.WithLockToken(ctx, lockToken)

	executors := &executorCreatorStub{id: executorID}
	launcher := &executorLauncherStub{}
	dispatcher := &DurableSessionExecutorDispatcher{
		Executors: executors,
		Jobs:      &jobHandoffStoreStub{ok: false},
		Launcher:  launcher,
		NodeID:    "worker-a",
	}

	_, err := dispatcher.Dispatch(ctx, "run_agent", models.Session{ID: sessionID, OrgID: orgID}, nil)
	require.Error(t, err, "Dispatch should return handoff fencing failures")
	require.Equal(t, 1, launcher.cleanupCalls, "Dispatch should stop the launched container after failed handoff")
	require.Equal(t, executorID, launcher.cleanupSpec.ExecutorID, "cleanup should target the launched executor")
	require.Equal(t, 1, executors.terminalCalls, "Dispatch should mark the executor terminal when handoff fails")
	require.Equal(t, models.SessionExecutorStatusFailed, executors.terminalStatus, "handoff failures should mark the reserved executor failed")
	require.Contains(t, executors.terminalError, "job handoff", "terminal error should describe the handoff failure")
}

func TestDurableSessionExecutorDispatcher_RequiresFencingContext(t *testing.T) {
	t.Parallel()

	dispatcher := &DurableSessionExecutorDispatcher{
		Executors: &executorCreatorStub{},
		Jobs:      &jobHandoffStoreStub{ok: true},
		Launcher:  &executorLauncherStub{},
	}

	_, err := dispatcher.Dispatch(context.Background(), "run_agent", models.Session{ID: uuid.New(), OrgID: uuid.New()}, nil)
	require.Error(t, err, "Dispatch should refuse to run without job id and lock token context")
}

func TestMaybeDispatchSessionExecutor_RequiresDispatcherWhenConfigured(t *testing.T) {
	t.Parallel()

	session := models.Session{ID: uuid.New(), OrgID: uuid.New()}
	services := &Services{RequireSessionExecutorDispatcher: true}

	err := maybeDispatchSessionExecutor(context.Background(), services, "run_agent", session, nil)

	require.Error(t, err, "production-style services should reject inline execution when no dispatcher is wired")
	require.Contains(t, err.Error(), "session executor dispatcher is required", "error should identify the missing dispatcher")
}

func TestMaybeDispatchSessionExecutor_AllowsInlineWhenNotRequired(t *testing.T) {
	t.Parallel()

	session := models.Session{ID: uuid.New(), OrgID: uuid.New()}
	services := &Services{}

	err := maybeDispatchSessionExecutor(context.Background(), services, "run_agent", session, nil)

	require.NoError(t, err, "local/dev services should keep the explicit inline fallback")
}
