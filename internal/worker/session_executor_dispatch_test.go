package worker

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
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
	containerCalls int
	containerID    string
	containerOK    bool
	containerErr   error
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

func (s *executorCreatorStub) RecordContainerIDWithLease(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, containerID string) (bool, error) {
	s.containerCalls++
	s.containerID = containerID
	if s.containerErr != nil {
		return false, s.containerErr
	}
	return s.containerOK, nil
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

func (s *executorLauncherStub) Launch(ctx context.Context, spec ExecutorLaunchSpec) (ExecutorLaunchResult, error) {
	s.calls++
	s.spec = spec
	if s.err != nil {
		return ExecutorLaunchResult{}, s.err
	}
	return ExecutorLaunchResult{ContainerID: "container-" + spec.ExecutorID.String()}, nil
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

	executors := &executorCreatorStub{id: executorID, containerOK: true}
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
	require.Equal(t, 1, executors.containerCalls, "Dispatch should persist the launched container id before handoff")
	require.Equal(t, "container-"+executorID.String(), executors.containerID, "Dispatch should persist the Docker container id for forensic lookup")
	require.Equal(t, 1, jobs.calls, "Dispatch should hand off the job exactly once")
	require.Equal(t, orgID, jobs.orgID, "handoff should be org-scoped")
	require.Equal(t, jobID, jobs.jobID, "handoff should target the running job")
	require.Equal(t, lockToken, jobs.lockToken, "handoff should preserve the existing fencing token")
	require.Equal(t, executorID, jobs.executorID, "handoff should assign ownership to the created executor")
}

func TestDurableSessionExecutorDispatcher_DispatchLogsHandoffLifecycle(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	executorID := uuid.New()
	ctx := jobctx.WithJobID(context.Background(), jobID)
	ctx = jobctx.WithLockToken(ctx, lockToken)

	var logs bytes.Buffer
	dispatcher := &DurableSessionExecutorDispatcher{
		Executors: &executorCreatorStub{id: executorID, containerOK: true},
		Jobs:      &jobHandoffStoreStub{ok: true},
		Launcher:  &executorLauncherStub{},
		NodeID:    "worker-a",
		Image:     "ghcr.io/assembledhq/143-server:test",
		BuildSHA:  "build-sha",
		Logger:    zerolog.New(&logs),
	}

	got, err := dispatcher.Dispatch(ctx, "run_agent", models.Session{ID: sessionID, OrgID: orgID}, &threadID)

	require.NoError(t, err, "Dispatch should complete successfully before inspecting logs")
	require.Equal(t, executorID, got, "Dispatch should return the created executor id")
	require.Contains(t, logs.String(), "session executor dispatch starting", "dispatch logs should include the start breadcrumb")
	require.Contains(t, logs.String(), "session executor row created", "dispatch logs should include executor row creation")
	require.Contains(t, logs.String(), "session executor job handoff completed", "dispatch logs should include handoff completion")
	require.Contains(t, logs.String(), executorID.String(), "dispatch logs should include the executor id for correlation")
	require.Contains(t, logs.String(), jobID.String(), "dispatch logs should include the job id for correlation")
	require.Contains(t, logs.String(), `"container_id":"container-`+executorID.String()+`"`, "dispatch logs should preserve the legacy executor container field")
	require.Contains(t, logs.String(), `"executor_container_id":"container-`+executorID.String()+`"`, "dispatch logs should identify the executor container explicitly")
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

	executors := &executorCreatorStub{id: executorID, containerOK: true}
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

	executors := &executorCreatorStub{id: executorID, containerOK: true}
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

	err := maybeDispatchSessionExecutor(context.Background(), nil, services, "run_agent", session, nil)

	require.Error(t, err, "production-style services should reject inline execution when no dispatcher is wired")
	require.Contains(t, err.Error(), "session executor dispatcher is required", "error should identify the missing dispatcher")
}

func TestMaybeDispatchSessionExecutor_AllowsInlineWhenNotRequired(t *testing.T) {
	t.Parallel()

	session := models.Session{ID: uuid.New(), OrgID: uuid.New()}
	services := &Services{}

	err := maybeDispatchSessionExecutor(context.Background(), nil, services, "run_agent", session, nil)

	require.NoError(t, err, "local/dev services should keep the explicit inline fallback")
}

func TestMaybeDispatchSessionExecutor_CodeReviewPlacementRequiresStores(t *testing.T) {
	t.Parallel()
	dispatcher := &fakeSessionExecutorDispatcher{}
	session := models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: models.SessionOriginCodeReview}
	services := &Services{SessionExecutorDispatcher: dispatcher}
	err := maybeDispatchSessionExecutor(context.Background(), nil, services, "run_agent", session, nil)
	require.Error(t, err, "missing placement stores should prevent a code review executor launch")
	require.ErrorContains(t, err, "code review executor placement stores are required", "code review placement should require its stores")
	require.Equal(t, 0, dispatcher.calls, "missing placement stores should not dispatch an executor")
}

func TestCodeReviewExecutorPlacement(t *testing.T) {
	t.Parallel()
	owner := "owner"
	container := "sandbox"
	other := "other"
	tests := []struct {
		name             string
		session          models.Session
		currentNode      string
		ownerHealthy     bool
		deadTarget       string
		localKnown       bool
		localAvailable   bool
		capacityNode     *string
		wantTarget       *string
		wantClear        bool
		wantDelay        time.Duration
		wantSelectCalled bool
		wantLocalCalled  bool
		wantBypass       bool
		wantWindow       bool
	}{
		{name: "live workspace on this node", session: models.Session{ContainerID: &container, WorkerNodeID: &owner}, currentNode: owner, ownerHealthy: true},
		{name: "live workspace on sibling", session: models.Session{ContainerID: &container, WorkerNodeID: &owner}, currentNode: other, ownerHealthy: true, wantTarget: &owner, wantBypass: true},
		{name: "dead owner needs a recovery claim", session: models.Session{ContainerID: &container, WorkerNodeID: &owner}, currentNode: other, wantTarget: &owner, wantBypass: true},
		{name: "dead owner claim performs runtime cleanup", session: models.Session{ContainerID: &container, WorkerNodeID: &owner}, currentNode: other, deadTarget: owner},
		{name: "container without owner is not redirected", session: models.Session{ContainerID: &container}, currentNode: owner},
		{name: "cold workspace stays local when capacity exists", currentNode: owner, localKnown: true, localAvailable: true, wantLocalCalled: true},
		{name: "cold workspace redirects only when local full", currentNode: owner, localKnown: true, capacityNode: &other, wantTarget: &other, wantDelay: 5 * time.Second, wantSelectCalled: true, wantLocalCalled: true, wantWindow: true},
		{name: "fleet saturation waits without launching", currentNode: owner, localKnown: true, wantClear: true, wantDelay: 10 * time.Second, wantSelectCalled: true, wantLocalCalled: true, wantWindow: true},
		{name: "unknown metadata falls back to runtime admission", currentNode: owner, capacityNode: &other, wantLocalCalled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			if tt.deadTarget != "" {
				ctx = jobctx.WithDeadTargetNode(ctx, tt.deadTarget)
			}
			selectCalled := false
			localCalled := false
			err := codeReviewExecutorPlacement(ctx, tt.session, tt.currentNode,
				func(context.Context, string) (bool, error) { return tt.ownerHealthy, nil },
				func(_ context.Context, nodeID string) (bool, bool, error) {
					localCalled = true
					require.Equal(t, tt.currentNode, nodeID, "local capacity should be checked on the claiming node")
					return tt.localKnown, tt.localAvailable, nil
				},
				func(_ context.Context, excludedNodeID string) (*string, error) {
					selectCalled = true
					require.Equal(t, tt.currentNode, excludedNodeID, "alternate selection should exclude the claiming node")
					return tt.capacityNode, nil
				})
			require.Equal(t, tt.wantSelectCalled, selectCalled, "placement should consult capacity only when no live owner can be used")
			require.Equal(t, tt.wantLocalCalled, localCalled, "placement should check the claiming node before considering alternates")
			if tt.wantTarget == nil && !tt.wantClear {
				require.NoError(t, err, "local placement should allow dispatch")
				return
			}
			var retry *RetryableError
			require.ErrorAs(t, err, &retry, "remote or saturated placement should defer executor launch")
			require.Equal(t, tt.wantTarget, retry.TargetNodeID, "placement should select the expected target")
			require.Equal(t, tt.wantClear, retry.ClearTargetNodeID, "placement should clear a stale target only during fleet saturation")
			require.Equal(t, tt.wantDelay, *retry.RetryAfter, "placement should use the expected retry delay")
			require.Equal(t, tt.wantBypass, retry.BypassMaxRetryDuration, "only ownership redirects should bypass the retry window")
			require.Equal(t, tt.wantWindow, retry.MaxRetryDuration != nil, "capacity waits should start a bounded retry window")
		})
	}
}
