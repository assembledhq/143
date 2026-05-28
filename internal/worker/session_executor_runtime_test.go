package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type executorRuntimeExecutorStoreStub struct {
	executor       models.SessionExecutor
	getErr         error
	markRunningOK  bool
	terminalStatus models.SessionExecutorStatus
	terminalToken  uuid.UUID
	terminalCalls  int
	drainingCalls  int
}

func (s *executorRuntimeExecutorStoreStub) GetByID(context.Context, uuid.UUID) (models.SessionExecutor, error) {
	if s.getErr != nil {
		return models.SessionExecutor{}, s.getErr
	}
	return s.executor, nil
}

func (s *executorRuntimeExecutorStoreStub) MarkRunningWithLease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Duration) (bool, error) {
	return s.markRunningOK, nil
}

func (s *executorRuntimeExecutorStoreStub) HeartbeatWithLease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Duration) (bool, error) {
	return true, nil
}

func (s *executorRuntimeExecutorStoreStub) MarkDrainingWithLease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
	s.drainingCalls++
	return true, nil
}

func (s *executorRuntimeExecutorStoreStub) MarkTerminalWithLease(_ context.Context, _ uuid.UUID, _ uuid.UUID, lockToken uuid.UUID, status models.SessionExecutorStatus, _ *int, _ string) (bool, error) {
	s.terminalCalls++
	s.terminalStatus = status
	s.terminalToken = lockToken
	return true, nil
}

type executorRuntimeJobStoreStub struct {
	job               *models.Job
	active            bool
	activeSequence    []bool
	getErr            error
	sawCanceledCtx    bool
	succeededToken    uuid.UUID
	succeededCalls    int
	failedCalls       int
	retryCalls        int
	targetRetryCalls  int
	targetRetryNodeID *string
	deadLetterCalls   int
	renewCalls        int
}

func (s *executorRuntimeJobStoreStub) GetRunningForSessionExecutor(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (*models.Job, bool, error) {
	if len(s.activeSequence) > 0 {
		active := s.activeSequence[0]
		s.activeSequence = s.activeSequence[1:]
		return s.job, active, s.getErr
	}
	return s.job, s.active, s.getErr
}

func (s *executorRuntimeJobStoreStub) RenewLease(context.Context, uuid.UUID, uuid.UUID, time.Duration) (*models.Job, bool, error) {
	s.renewCalls++
	return &models.Job{LeaseExpiresAt: ptr(time.Now().Add(time.Minute))}, true, nil
}

func (s *executorRuntimeJobStoreStub) RenewLeaseForSessionExecutor(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, time.Duration) (*models.Job, bool, error) {
	s.renewCalls++
	return &models.Job{LeaseExpiresAt: ptr(time.Now().Add(time.Minute))}, true, nil
}

func (s *executorRuntimeJobStoreStub) MarkSucceededWithLease(ctx context.Context, _ uuid.UUID, lockToken uuid.UUID) (bool, error) {
	s.succeededCalls++
	s.succeededToken = lockToken
	if ctx.Err() != nil {
		s.sawCanceledCtx = true
	}
	return true, nil
}

func (s *executorRuntimeJobStoreStub) MarkFailedWithLease(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	s.failedCalls++
	return true, nil
}

func (s *executorRuntimeJobStoreStub) RetryWithLease(context.Context, uuid.UUID, uuid.UUID, string, time.Time) (bool, error) {
	s.retryCalls++
	return true, nil
}

func (s *executorRuntimeJobStoreStub) RetryWithoutConsumingAttemptWithLease(context.Context, uuid.UUID, uuid.UUID, string, time.Time) (bool, error) {
	s.retryCalls++
	return true, nil
}

func (s *executorRuntimeJobStoreStub) RetryWithLeaseAndTarget(context.Context, uuid.UUID, uuid.UUID, string, time.Time, *string) (bool, error) {
	s.targetRetryCalls++
	return true, nil
}

func (s *executorRuntimeJobStoreStub) RetryWithoutConsumingAttemptWithLeaseAndTarget(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ string, _ time.Time, targetNodeID *string) (bool, error) {
	s.targetRetryCalls++
	s.targetRetryNodeID = targetNodeID
	return true, nil
}

func (s *executorRuntimeJobStoreStub) DeadLetterWithLease(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	s.deadLetterCalls++
	return true, nil
}

func ptr[T any](v T) *T {
	return &v
}

func TestSessionExecutorRuntime_RefusesStaleLockToken(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	runtime := &SessionExecutorRuntime{
		Executors: &executorRuntimeExecutorStoreStub{
			executor: models.SessionExecutor{
				ID:        executorID,
				OrgID:     orgID,
				SessionID: sessionID,
				JobID:     jobID,
				JobType:   "run_agent",
				LockToken: lockToken,
				Status:    models.SessionExecutorStatusStarting,
			},
			markRunningOK: true,
		},
		Jobs: &executorRuntimeJobStoreStub{active: false},
		Handlers: map[string]JobHandler{
			"run_agent": func(context.Context, string, json.RawMessage) error {
				t.Fatal("handler must not run when boot validation fails")
				return nil
			},
		},
		Logger:                 zerolog.Nop(),
		BootValidationTimeout:  time.Millisecond,
		BootValidationInterval: time.Millisecond,
	}

	err := runtime.Run(context.Background(), executorID)
	require.ErrorIs(t, err, ErrExecutorInvalidHandoff, "runtime should refuse stale or missing fenced job ownership")
}

func TestSessionExecutorRuntime_BootValidationTimeoutMarksExecutorFailed(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	executors := &executorRuntimeExecutorStoreStub{
		executor: models.SessionExecutor{
			ID:        executorID,
			OrgID:     orgID,
			SessionID: sessionID,
			JobID:     jobID,
			JobType:   "run_agent",
			LockToken: lockToken,
			Status:    models.SessionExecutorStatusStarting,
		},
		markRunningOK: true,
	}
	runtime := &SessionExecutorRuntime{
		Executors:              executors,
		Jobs:                   &executorRuntimeJobStoreStub{active: false},
		Logger:                 zerolog.Nop(),
		BootValidationTimeout:  time.Millisecond,
		BootValidationInterval: time.Millisecond,
	}

	err := runtime.Run(context.Background(), executorID)
	require.ErrorIs(t, err, ErrExecutorInvalidHandoff, "runtime should return invalid handoff after boot validation timeout")
	require.Equal(t, 1, executors.terminalCalls, "runtime should mark a timed-out boot executor terminal")
	require.Equal(t, models.SessionExecutorStatusFailed, executors.terminalStatus, "runtime should mark boot validation timeout as failed")
}

func TestSessionExecutorRuntime_WaitsForDispatcherHandoffAtBoot(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	jobs := &executorRuntimeJobStoreStub{
		activeSequence: []bool{false, true},
		job: &models.Job{
			ID:          jobID,
			OrgID:       orgID,
			JobType:     "run_agent",
			Payload:     json.RawMessage(`{}`),
			Status:      "running",
			Attempts:    1,
			MaxAttempts: 3,
			LockToken:   &lockToken,
			CreatedAt:   time.Now(),
		},
	}
	handlerCalls := 0
	runtime := &SessionExecutorRuntime{
		Executors: &executorRuntimeExecutorStoreStub{
			executor: models.SessionExecutor{
				ID:        executorID,
				OrgID:     orgID,
				SessionID: sessionID,
				JobID:     jobID,
				JobType:   "run_agent",
				LockToken: lockToken,
				Status:    models.SessionExecutorStatusStarting,
			},
			markRunningOK: true,
		},
		Jobs: jobs,
		Handlers: map[string]JobHandler{
			"run_agent": func(context.Context, string, json.RawMessage) error {
				handlerCalls++
				return nil
			},
		},
		Logger:                 zerolog.Nop(),
		BootValidationTimeout:  100 * time.Millisecond,
		BootValidationInterval: time.Millisecond,
	}

	err := runtime.Run(context.Background(), executorID)
	require.NoError(t, err, "runtime should wait for the worker to complete executor handoff")
	require.Equal(t, 1, handlerCalls, "runtime should run the handler once the fenced handoff becomes visible")
	require.Empty(t, jobs.activeSequence, "runtime should retry boot validation after the first missing ownership read")
}

func TestSessionExecutorRuntime_InjectsDeadTargetNodeContext(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	deadTargetNodeID := "worker-old-generation"
	executorNodeID := "worker-new-generation"
	jobs := &executorRuntimeJobStoreStub{
		active: true,
		job: &models.Job{
			ID:           jobID,
			OrgID:        orgID,
			JobType:      "continue_session",
			Payload:      json.RawMessage(`{}`),
			Status:       "running",
			Attempts:     1,
			MaxAttempts:  3,
			LockToken:    &lockToken,
			TargetNodeID: &deadTargetNodeID,
			CreatedAt:    time.Now(),
		},
	}
	runtime := &SessionExecutorRuntime{
		Executors: &executorRuntimeExecutorStoreStub{
			executor: models.SessionExecutor{
				ID:         executorID,
				OrgID:      orgID,
				SessionID:  sessionID,
				JobID:      jobID,
				JobType:    "continue_session",
				HostNodeID: executorNodeID,
				LockToken:  lockToken,
				Status:     models.SessionExecutorStatusStarting,
			},
			markRunningOK: true,
		},
		Jobs: jobs,
		Handlers: map[string]JobHandler{
			"continue_session": func(ctx context.Context, _ string, _ json.RawMessage) error {
				nodeID, ok := jobctx.DeadTargetNodeFromContext(ctx)
				require.True(t, ok, "executor runtime should tell handlers when the job's target node was bypassed as dead")
				require.Equal(t, deadTargetNodeID, nodeID, "executor runtime should preserve the dead target node id")
				return nil
			},
		},
		Logger: zerolog.Nop(),
	}

	err := runtime.Run(context.Background(), executorID)
	require.NoError(t, err, "runtime should complete when the handler succeeds")
	require.Equal(t, 1, jobs.succeededCalls, "runtime should mark the job succeeded")
}

func TestSessionExecutorRuntime_SuccessMarksJobAndExecutorTerminalWithLockToken(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	payload := json.RawMessage(`{"session_id":"` + sessionID.String() + `","org_id":"` + orgID.String() + `"}`)
	executors := &executorRuntimeExecutorStoreStub{
		executor: models.SessionExecutor{
			ID:        executorID,
			OrgID:     orgID,
			SessionID: sessionID,
			JobID:     jobID,
			JobType:   "run_agent",
			LockToken: lockToken,
			Status:    models.SessionExecutorStatusStarting,
		},
		markRunningOK: true,
	}
	jobs := &executorRuntimeJobStoreStub{
		active: true,
		job: &models.Job{
			ID:          jobID,
			OrgID:       orgID,
			JobType:     "run_agent",
			Payload:     payload,
			Status:      "running",
			Attempts:    1,
			MaxAttempts: 3,
			LockToken:   &lockToken,
			CreatedAt:   time.Now(),
		},
	}
	handlerCalls := 0
	runtime := &SessionExecutorRuntime{
		Executors: executors,
		Jobs:      jobs,
		Handlers: map[string]JobHandler{
			"run_agent": func(ctx context.Context, jobType string, got json.RawMessage) error {
				handlerCalls++
				gotJobID, ok := jobctx.JobIDFromContext(ctx)
				require.True(t, ok, "runtime should put the job id in handler context")
				require.Equal(t, jobID, gotJobID, "runtime should preserve job id in context")
				gotToken, ok := jobctx.LockTokenFromContext(ctx)
				require.True(t, ok, "runtime should put the lock token in handler context")
				require.Equal(t, lockToken, gotToken, "runtime should preserve lock token in context")
				ownerKind, ok := jobctx.OwnerKindFromContext(ctx)
				require.True(t, ok, "runtime should put the owner kind in handler context")
				require.Equal(t, string(models.JobOwnerKindSessionExecutor), ownerKind, "runtime should identify executor-owned handlers")
				require.Equal(t, payload, got, "runtime should pass the claimed job payload")
				return nil
			},
		},
		Logger: zerolog.Nop(),
	}

	err := runtime.Run(context.Background(), executorID)
	require.NoError(t, err, "runtime should complete successful handler execution")
	require.Equal(t, 1, handlerCalls, "runtime should run the job handler once")
	require.Equal(t, 1, jobs.succeededCalls, "runtime should mark the job succeeded")
	require.Equal(t, lockToken, jobs.succeededToken, "runtime should fence job success by lock token")
	require.Equal(t, 1, executors.terminalCalls, "runtime should mark the executor terminal")
	require.Equal(t, models.SessionExecutorStatusCompleted, executors.terminalStatus, "runtime should mark successful executors completed")
	require.Equal(t, lockToken, executors.terminalToken, "runtime should fence executor terminal write by lock token")
}

func TestSessionExecutorRuntime_RetryableErrorRequeuesJob(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	jobs := &executorRuntimeJobStoreStub{
		active: true,
		job: &models.Job{
			ID:          jobID,
			OrgID:       orgID,
			JobType:     "continue_session",
			Payload:     json.RawMessage(`{}`),
			Status:      "running",
			Attempts:    1,
			MaxAttempts: 3,
			LockToken:   &lockToken,
			CreatedAt:   time.Now(),
		},
	}
	runtime := &SessionExecutorRuntime{
		Executors: &executorRuntimeExecutorStoreStub{
			executor: models.SessionExecutor{
				ID:        executorID,
				OrgID:     orgID,
				SessionID: sessionID,
				JobID:     jobID,
				JobType:   "continue_session",
				LockToken: lockToken,
				Status:    models.SessionExecutorStatusStarting,
			},
			markRunningOK: true,
		},
		Jobs: jobs,
		Handlers: map[string]JobHandler{
			"continue_session": func(context.Context, string, json.RawMessage) error {
				return &RetryableError{Err: errors.New("capacity full"), BypassMaxRetryDuration: true}
			},
		},
		Logger: zerolog.Nop(),
	}

	err := runtime.Run(context.Background(), executorID)
	require.NoError(t, err, "runtime should treat successful retry scheduling as handled")
	require.Equal(t, 1, jobs.retryCalls, "runtime should requeue retryable job errors")
	require.Equal(t, 0, jobs.succeededCalls, "runtime should not mark retryable jobs succeeded")
}

func TestSessionExecutorRuntime_RetryableErrorClearsTargetNode(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	jobs := &executorRuntimeJobStoreStub{
		active: true,
		job: &models.Job{
			ID:          jobID,
			OrgID:       orgID,
			JobType:     "continue_session",
			Payload:     json.RawMessage(`{}`),
			Status:      "running",
			Attempts:    1,
			MaxAttempts: 3,
			LockToken:   &lockToken,
			CreatedAt:   time.Now(),
		},
	}
	runtime := &SessionExecutorRuntime{
		Executors: &executorRuntimeExecutorStoreStub{
			executor: models.SessionExecutor{
				ID:        executorID,
				OrgID:     orgID,
				SessionID: sessionID,
				JobID:     jobID,
				JobType:   "continue_session",
				LockToken: lockToken,
				Status:    models.SessionExecutorStatusStarting,
			},
			markRunningOK: true,
		},
		Jobs: jobs,
		Handlers: map[string]JobHandler{
			"continue_session": func(context.Context, string, json.RawMessage) error {
				return &RetryableError{Err: errors.New("capacity full"), ClearTargetNodeID: true}
			},
		},
		Logger: zerolog.Nop(),
	}

	err := runtime.Run(context.Background(), executorID)
	require.NoError(t, err, "runtime should treat successful retry scheduling as handled")
	require.Equal(t, 1, jobs.targetRetryCalls, "runtime should use the targeted retry path so target_node_id is rewritten")
	require.Nil(t, jobs.targetRetryNodeID, "runtime should pass nil through the targeted retry path to clear target_node_id")
	require.Equal(t, 0, jobs.retryCalls, "runtime should not use the non-target retry path when clearing target_node_id")
}

func TestSessionExecutorRuntime_RetryableErrorMarksExecutorRequeued(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	executors := &executorRuntimeExecutorStoreStub{
		executor: models.SessionExecutor{
			ID:        executorID,
			OrgID:     orgID,
			SessionID: sessionID,
			JobID:     jobID,
			JobType:   "continue_session",
			LockToken: lockToken,
			Status:    models.SessionExecutorStatusStarting,
		},
		markRunningOK: true,
	}
	runtime := &SessionExecutorRuntime{
		Executors: executors,
		Jobs: &executorRuntimeJobStoreStub{
			active: true,
			job: &models.Job{
				ID:          jobID,
				OrgID:       orgID,
				JobType:     "continue_session",
				Payload:     json.RawMessage(`{}`),
				Status:      "running",
				Attempts:    1,
				MaxAttempts: 3,
				LockToken:   &lockToken,
				CreatedAt:   time.Now(),
			},
		},
		Handlers: map[string]JobHandler{
			"continue_session": func(context.Context, string, json.RawMessage) error {
				return &RetryableError{Err: errors.New("capacity full"), BypassMaxRetryDuration: true}
			},
		},
		Logger: zerolog.Nop(),
	}

	err := runtime.Run(context.Background(), executorID)
	require.NoError(t, err, "runtime should treat successful retry scheduling as handled")
	require.Equal(t, models.SessionExecutorStatusRequeued, executors.terminalStatus, "retrying attempts should not be marked completed")
}

func TestSessionExecutorRuntime_UsesDetachedContextForTerminalJobWrites(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	jobs := &executorRuntimeJobStoreStub{
		active: true,
		job: &models.Job{
			ID:          jobID,
			OrgID:       orgID,
			JobType:     "run_agent",
			Payload:     json.RawMessage(`{}`),
			Status:      "running",
			Attempts:    1,
			MaxAttempts: 3,
			LockToken:   &lockToken,
			CreatedAt:   time.Now(),
		},
	}
	runtime := &SessionExecutorRuntime{
		Executors: &executorRuntimeExecutorStoreStub{
			executor: models.SessionExecutor{
				ID:        executorID,
				OrgID:     orgID,
				SessionID: sessionID,
				JobID:     jobID,
				JobType:   "run_agent",
				LockToken: lockToken,
				Status:    models.SessionExecutorStatusStarting,
			},
			markRunningOK: true,
		},
		Jobs: jobs,
		Handlers: map[string]JobHandler{
			"run_agent": func(context.Context, string, json.RawMessage) error {
				return nil
			},
		},
		Logger: zerolog.Nop(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runtime.Run(ctx, executorID)
	require.NoError(t, err, "runtime should still complete terminal writes after parent cancellation")
	require.Equal(t, 1, jobs.succeededCalls, "runtime should attempt the success write")
	require.False(t, jobs.sawCanceledCtx, "runtime should detach final job writes from executor parent cancellation")
}

func TestSessionExecutorRuntime_DrainRequestsTypedSystemStopAndRequeues(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	handlerStarted := make(chan struct{})
	orch := &orchestratorServiceStub{stopSessionResult: true}
	executors := &executorRuntimeExecutorStoreStub{
		executor: models.SessionExecutor{
			ID:        executorID,
			OrgID:     orgID,
			SessionID: sessionID,
			JobID:     jobID,
			JobType:   "run_agent",
			LockToken: lockToken,
			Status:    models.SessionExecutorStatusStarting,
		},
		markRunningOK: true,
	}
	jobs := &executorRuntimeJobStoreStub{
		active: true,
		job: &models.Job{
			ID:          jobID,
			OrgID:       orgID,
			JobType:     "run_agent",
			Payload:     json.RawMessage(`{}`),
			Status:      "running",
			Attempts:    1,
			MaxAttempts: 3,
			LockToken:   &lockToken,
			CreatedAt:   time.Now(),
		},
	}
	runtime := &SessionExecutorRuntime{
		Executors: executors,
		Jobs:      jobs,
		Services:  &Services{Orchestrator: orch},
		Handlers: map[string]JobHandler{
			"run_agent": func(ctx context.Context, _ string, _ json.RawMessage) error {
				close(handlerStarted)
				<-ctx.Done()
				return ctx.Err()
			},
		},
		Logger: zerolog.Nop(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runtime.Run(ctx, executorID)
	}()
	<-handlerStarted
	cancel()

	err := <-done
	require.NoError(t, err, "runtime should persist the drain retry decision successfully")
	require.Equal(t, 0, orch.cancelSessionCalls, "drain watcher must not route worker drain through the user-cancel API")
	require.Equal(t, 1, orch.stopSessionCalls, "drain watcher should request a typed system stop")
	require.Equal(t, sessionID, orch.stopSessionID, "drain watcher should stop the executor session")
	require.Equal(t, agent.StopReasonWorkerDrain, orch.stopReason, "drain watcher should preserve worker-drain as the stop reason")
	require.Equal(t, 1, jobs.retryCalls, "drained sessions should retry the original turn instead of closing it as succeeded")
	require.Equal(t, 0, jobs.succeededCalls, "drained sessions should not close the accepted job as succeeded")
	require.Equal(t, models.SessionExecutorStatusRequeued, executors.terminalStatus, "drained executors should finish as requeued")
}

func TestSessionExecutorRuntime_DrainPreservesHandlerRetryableError(t *testing.T) {
	t.Parallel()

	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	handlerStarted := make(chan struct{})
	orch := &orchestratorServiceStub{stopSessionResult: true}
	executors := &executorRuntimeExecutorStoreStub{
		executor: models.SessionExecutor{
			ID:        executorID,
			OrgID:     orgID,
			SessionID: sessionID,
			JobID:     jobID,
			JobType:   "run_agent",
			LockToken: lockToken,
			Status:    models.SessionExecutorStatusStarting,
		},
		markRunningOK: true,
	}
	jobs := &executorRuntimeJobStoreStub{
		active: true,
		job: &models.Job{
			ID:          jobID,
			OrgID:       orgID,
			JobType:     "run_agent",
			Payload:     json.RawMessage(`{}`),
			Status:      "running",
			Attempts:    1,
			MaxAttempts: 1,
			LockToken:   &lockToken,
			CreatedAt:   time.Now().Add(-24 * time.Hour),
		},
	}
	runtime := &SessionExecutorRuntime{
		Executors: executors,
		Jobs:      jobs,
		Services:  &Services{Orchestrator: orch},
		Handlers: map[string]JobHandler{
			"run_agent": func(ctx context.Context, _ string, _ json.RawMessage) error {
				close(handlerStarted)
				<-ctx.Done()
				return &RetryableError{
					Err:                    fmt.Errorf("%w: %w", agent.ErrSessionInterrupted, ctx.Err()),
					BypassMaxRetryDuration: true,
				}
			},
		},
		Logger: zerolog.Nop(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runtime.Run(ctx, executorID)
	}()
	<-handlerStarted
	cancel()

	err := <-done
	require.NoError(t, err, "runtime should persist the preserved retryable drain decision")
	require.Equal(t, 1, jobs.retryCalls, "existing retryable interruption should still requeue on the final attempt")
	require.Equal(t, 0, jobs.deadLetterCalls, "existing retryable interruption should not be flattened into a final-attempt dead letter")
	require.Equal(t, models.SessionExecutorStatusRequeued, executors.terminalStatus, "existing retryable interruption should preserve requeued executor status")
}
