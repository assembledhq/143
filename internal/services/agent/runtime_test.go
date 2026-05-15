package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
)

type runtimeTestSessionStore struct {
	countRunning               int
	extensionGrants            int
	countRunningErr            error
	beginErr                   error
	beginCalls                 int
	recordRuntimeProgressCalls int
	recordRuntimeProgressErr   error
	lastProgressType           models.RuntimeProgressType
	lastProgressStrength       models.RuntimeProgressStrength
	lastProgressObservedAt     time.Time
	grantConfigured            bool
	grantAllowed               bool
	grantErr                   error
	lastGrantLockToken         uuid.UUID
	lastGrantExpectedSoft      time.Time
	lastGrantNewSoft           time.Time
	lastGrantHard              time.Time
	lastGrantExtensionSeconds  int
}

func (s *runtimeTestSessionStore) UpdateStatus(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateResult(context.Context, uuid.UUID, uuid.UUID, string, *models.SessionResult) error {
	return nil
}

func (s *runtimeTestSessionStore) CountRunningByOrg(context.Context, uuid.UUID) (int, error) {
	return s.countRunning, s.countRunningErr
}

func (s *runtimeTestSessionStore) UpdateTurnComplete(context.Context, uuid.UUID, uuid.UUID, int, *models.SessionResult, string, string) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateSnapshotInfo(context.Context, uuid.UUID, uuid.UUID, string, string) error {
	return nil
}

func (s *runtimeTestSessionStore) BeginRuntime(context.Context, uuid.UUID, uuid.UUID, models.CheckpointCapability, time.Time, time.Time, time.Time) error {
	s.beginCalls++
	return s.beginErr
}

func (s *runtimeTestSessionStore) RequestCancel(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (s *runtimeTestSessionStore) ConsumeCancelRequest(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func (s *runtimeTestSessionStore) RecordRuntimeProgress(_ context.Context, _ uuid.UUID, _ uuid.UUID, progressType models.RuntimeProgressType, strength models.RuntimeProgressStrength, observedAt time.Time) error {
	s.recordRuntimeProgressCalls++
	s.lastProgressType = progressType
	s.lastProgressStrength = strength
	s.lastProgressObservedAt = observedAt
	return s.recordRuntimeProgressErr
}

func (s *runtimeTestSessionStore) GrantRuntimeExtension(_ context.Context, _ uuid.UUID, _ uuid.UUID, lockToken uuid.UUID, expectedSoftDeadline, newSoftDeadline, hardDeadline time.Time, extensionSeconds int) (bool, error) {
	s.extensionGrants++
	s.lastGrantLockToken = lockToken
	s.lastGrantExpectedSoft = expectedSoftDeadline
	s.lastGrantNewSoft = newSoftDeadline
	s.lastGrantHard = hardDeadline
	s.lastGrantExtensionSeconds = extensionSeconds
	if s.grantErr != nil {
		return false, s.grantErr
	}
	if s.grantConfigured {
		return s.grantAllowed, nil
	}
	return true, nil
}

func (s *runtimeTestSessionStore) PublishCheckpoint(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string, models.CheckpointKind, models.CheckpointCapability, int64, time.Time, *string, models.RuntimeStopReason) (bool, error) {
	return true, nil
}

func (s *runtimeTestSessionStore) UpdateRecoveryState(context.Context, uuid.UUID, uuid.UUID, models.RecoveryState, *time.Time, *time.Time, bool) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateSandboxState(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateWorkingBranch(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateBaseCommitSHA(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (s *runtimeTestSessionStore) SetGitIdentity(context.Context, uuid.UUID, uuid.UUID, string, *uuid.UUID) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateFailure(context.Context, uuid.UUID, uuid.UUID, string, string, []string, bool) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateTitle(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateRevisionContext(context.Context, uuid.UUID, uuid.UUID, []byte) error {
	return nil
}

func (s *runtimeTestSessionStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	return models.Session{}, nil
}

func (s *runtimeTestSessionStore) AcquireTurnHold(context.Context, uuid.UUID, uuid.UUID, string) (string, error) {
	return "", nil
}

func (s *runtimeTestSessionStore) SetWorkerNodeIDForContainer(context.Context, uuid.UUID, uuid.UUID, string, string) error {
	return nil
}

func (s *runtimeTestSessionStore) ReleaseTurnHold(context.Context, uuid.UUID, uuid.UUID) (bool, string, error) {
	return true, "", nil
}

func (s *runtimeTestSessionStore) FinalizeContainerDestroy(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	return true, nil
}

func (s *runtimeTestSessionStore) ClearContainerID(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	return true, nil
}

func (s *runtimeTestSessionStore) ContainerHoldState(context.Context, uuid.UUID, uuid.UUID, string) (bool, bool, error) {
	return true, false, nil
}

type runtimeTestJobStore struct{}

func (s *runtimeTestJobStore) Enqueue(context.Context, uuid.UUID, string, string, any, int, *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (s *runtimeTestJobStore) EnqueueWithTarget(context.Context, uuid.UUID, string, string, any, int, *string, *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (s *runtimeTestJobStore) OldestPendingSessionJobAge(context.Context) (time.Duration, bool, error) {
	return 0, false, nil
}

type runtimeTestJobBacklogStore struct {
	age time.Duration
	ok  bool
	err error
}

func (s *runtimeTestJobBacklogStore) Enqueue(context.Context, uuid.UUID, string, string, any, int, *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (s *runtimeTestJobBacklogStore) EnqueueWithTarget(context.Context, uuid.UUID, string, string, any, int, *string, *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (s *runtimeTestJobBacklogStore) OldestPendingSessionJobAge(context.Context) (time.Duration, bool, error) {
	return s.age, s.ok, s.err
}

type runtimeTestOrgStore struct {
	org models.Organization
	err error
}

func (s *runtimeTestOrgStore) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	if s.err != nil {
		return models.Organization{}, s.err
	}
	return s.org, nil
}

func TestRuntimeController_ExtendsHealthyRunAfterStrongProgress(t *testing.T) {
	t.Parallel()

	sessionStore := &runtimeTestSessionStore{}
	controller := newRuntimeController(
		runtimeConfig{
			SoftBudget:             time.Second,
			NoProgressTimeout:      10 * time.Second,
			GracefulShutdownWindow: time.Second,
			ExtensionIncrement:     2 * time.Second,
			MaxAutomaticExtension:  2 * time.Second,
			AbsoluteRuntimeCeiling: 5 * time.Second,
			QueueAgeThreshold:      time.Minute,
		},
		sessionStore,
		&runtimeTestJobStore{},
		nil,
		zerolog.Nop(),
		uuid.New(),
		uuid.New(),
		3,
		nil,
		newRuntimeProgressTracker(time.Now()),
	)

	startedAt := time.Now().UTC()
	require.NoError(t, controller.Begin(context.Background(), startedAt, models.CheckpointCapabilityFullResume), "Begin should seed the runtime deadlines")

	controller.tracker.Record(models.RuntimeProgressTypeToolResult, models.RuntimeProgressStrengthStrong, startedAt.Add(900*time.Millisecond))
	controller.tick(context.Background(), startedAt.Add(1100*time.Millisecond))

	require.Equal(t, 1, sessionStore.extensionGrants, "strong recent progress should grant a runtime extension after the soft budget expires")
}

func TestRuntimeController_ExtendsAtConfiguredConcurrencyLimit(t *testing.T) {
	t.Parallel()

	sessionStore := &runtimeTestSessionStore{countRunning: 3}
	controller := newRuntimeController(
		runtimeConfig{
			SoftBudget:             time.Second,
			NoProgressTimeout:      10 * time.Second,
			GracefulShutdownWindow: time.Second,
			ExtensionIncrement:     2 * time.Second,
			MaxAutomaticExtension:  2 * time.Second,
			AbsoluteRuntimeCeiling: 5 * time.Second,
			QueueAgeThreshold:      time.Minute,
		},
		sessionStore,
		&runtimeTestJobStore{},
		nil,
		zerolog.Nop(),
		uuid.New(),
		uuid.New(),
		3,
		nil,
		newRuntimeProgressTracker(time.Now()),
	)

	startedAt := time.Now().UTC()
	require.NoError(t, controller.Begin(context.Background(), startedAt, models.CheckpointCapabilityFullResume), "Begin should seed the runtime deadlines")

	controller.tracker.Record(models.RuntimeProgressTypeToolResult, models.RuntimeProgressStrengthStrong, startedAt.Add(900*time.Millisecond))
	controller.tick(context.Background(), startedAt.Add(1100*time.Millisecond))

	require.Equal(t, 1, sessionStore.extensionGrants, "hitting the org concurrency cap should not block an in-flight session from receiving a bounded extension")
}

func TestResolveRuntimeConfig_UsesDefaultsAndOrgOverrides(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	defaultCfg := (&Orchestrator{}).resolveRuntimeConfig(context.Background(), orgID)

	t.Run("defaults without org store", func(t *testing.T) {
		t.Parallel()

		cfg := (&Orchestrator{}).resolveRuntimeConfig(context.Background(), orgID)
		require.Equal(t, defaultCfg, cfg, "resolveRuntimeConfig should return defaults when no org store is configured")
	})

	t.Run("defaults on org lookup error", func(t *testing.T) {
		t.Parallel()

		orch := &Orchestrator{orgs: &runtimeTestOrgStore{err: errors.New("lookup failed")}}
		cfg := orch.resolveRuntimeConfig(context.Background(), orgID)
		require.Equal(t, defaultCfg, cfg, "resolveRuntimeConfig should fall back to defaults when org lookup fails")
	})

	t.Run("defaults on invalid settings JSON", func(t *testing.T) {
		t.Parallel()

		orch := &Orchestrator{
			orgs: &runtimeTestOrgStore{org: models.Organization{ID: orgID, Settings: json.RawMessage(`{invalid`)}}}
		cfg := orch.resolveRuntimeConfig(context.Background(), orgID)
		require.Equal(t, defaultCfg, cfg, "resolveRuntimeConfig should fall back to defaults when settings cannot be parsed")
	})

	t.Run("uses org overrides", func(t *testing.T) {
		t.Parallel()

		settings := json.RawMessage(`{
			"max_session_duration_seconds": 1200,
			"runtime_budgets": {
				"no_progress_timeout_seconds": 90,
				"graceful_shutdown_window_seconds": 45,
				"checkpoint_finalization_window_seconds": 30,
				"automatic_extension_seconds": 120,
				"max_automatic_extension_seconds": 300,
				"absolute_runtime_ceiling_seconds": 1500
			}
		}`)
		orch := &Orchestrator{
			orgs: &runtimeTestOrgStore{org: models.Organization{ID: orgID, Settings: settings}},
		}

		cfg := orch.resolveRuntimeConfig(context.Background(), orgID)
		require.Equal(t, 20*time.Minute, cfg.SoftBudget, "resolveRuntimeConfig should use the org soft budget override")
		require.Equal(t, 90*time.Second, cfg.NoProgressTimeout, "resolveRuntimeConfig should use the org no-progress timeout override")
		require.Equal(t, 45*time.Second, cfg.GracefulShutdownWindow, "resolveRuntimeConfig should use the org graceful shutdown override")
		require.Equal(t, 30*time.Second, cfg.CheckpointFinalizeWindow, "resolveRuntimeConfig should use the org checkpoint finalization override")
		require.Equal(t, 2*time.Minute, cfg.ExtensionIncrement, "resolveRuntimeConfig should use the org extension increment override")
		require.Equal(t, 5*time.Minute, cfg.MaxAutomaticExtension, "resolveRuntimeConfig should use the org max automatic extension override")
		require.Equal(t, 25*time.Minute, cfg.AbsoluteRuntimeCeiling, "resolveRuntimeConfig should use the org absolute ceiling override")
	})
}

func TestRuntimeHelpers_MapCheckpointCapabilitiesAndStopReasons(t *testing.T) {
	t.Parallel()

	require.Equal(t, models.CheckpointCapabilityFullResume, checkpointCapabilityForAgent(models.AgentTypeCodex), "Codex agents should support full resume checkpoints")
	require.Equal(t, models.CheckpointCapabilityFullResume, checkpointCapabilityForAgent(models.AgentTypeClaudeCode), "Claude Code agents should support full resume checkpoints")
	require.Equal(t, models.CheckpointCapabilityFullResume, checkpointCapabilityForAgent(models.AgentTypeGeminiCLI), "Gemini agents should support full resume checkpoints")
	require.Equal(t, models.CheckpointCapabilityFilesystemOnly, checkpointCapabilityForAgent(models.AgentTypeAmp), "Amp agents should only support filesystem checkpoints")
	require.Equal(t, models.CheckpointCapabilityFilesystemOnly, checkpointCapabilityForAgent(models.AgentTypePi), "Pi agents should only support filesystem checkpoints")
	require.Equal(t, models.CheckpointCapabilityNoDurable, checkpointCapabilityForAgent(models.AgentTypePMAgent), "non-checkpointed agents should default to no durable checkpoint support")

	require.Equal(t, models.RuntimeStopReasonUserCancel, stopReasonToRuntime(StopReasonUserCancel), "user cancels should map to the user-cancel runtime reason")
	require.Equal(t, models.RuntimeStopReasonSoftBudget, stopReasonToRuntime(StopReasonSoftBudget), "soft budget stops should map to the soft-budget runtime reason")
	require.Equal(t, models.RuntimeStopReasonNoProgress, stopReasonToRuntime(StopReasonNoProgress), "no-progress stops should map to the no-progress runtime reason")
	require.Equal(t, models.RuntimeStopReasonAbsoluteCeiling, stopReasonToRuntime(StopReasonAbsoluteCeiling), "absolute-ceiling stops should map to the absolute-ceiling runtime reason")
	require.Equal(t, models.RuntimeStopReasonNone, stopReasonToRuntime(StopReason("unknown")), "unknown stop reasons should map to the empty runtime reason")
}

func TestRuntimeProgressTracker_RecordAndPersist(t *testing.T) {
	t.Parallel()

	tracker := &runtimeProgressTracker{}
	require.False(t, tracker.ShouldPersist(), "ShouldPersist should ignore an empty tracker with no observed progress")

	tracker.Record(models.RuntimeProgressTypeToolResult, models.RuntimeProgressStrengthStrong, time.Time{})
	lastProgressAt, lastStrongAt, progressType, strength := tracker.Snapshot()
	require.False(t, lastProgressAt.IsZero(), "Record should synthesize the observation timestamp when none is supplied")
	require.Equal(t, models.RuntimeProgressTypeToolResult, progressType, "Record should store the latest progress type")
	require.Equal(t, models.RuntimeProgressStrengthStrong, strength, "Record should store the latest progress strength")
	require.Equal(t, lastProgressAt, lastStrongAt, "strong progress should update the strong-progress watermark")
	require.True(t, tracker.ShouldPersist(), "ShouldPersist should persist newly observed progress once")
	require.False(t, tracker.ShouldPersist(), "ShouldPersist should not re-persist the same progress snapshot twice")
}

func TestRuntimeProgressFromLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		entry        LogEntry
		expectedType models.RuntimeProgressType
		expected     models.RuntimeProgressStrength
		wantOK       bool
	}{
		{name: "tool use", entry: LogEntry{Level: "tool_use"}, expectedType: models.RuntimeProgressTypeToolUse, expected: models.RuntimeProgressStrengthWeak, wantOK: true},
		{name: "completed command execution", entry: LogEntry{Level: "tool_use", Metadata: map[string]any{"tool": "command_execution", "status": "completed"}}, expectedType: models.RuntimeProgressTypeToolResult, expected: models.RuntimeProgressStrengthStrong, wantOK: true},
		{name: "failed command execution", entry: LogEntry{Level: "tool_use", Metadata: map[string]any{"tool": "command_execution", "status": "failed"}}, expectedType: models.RuntimeProgressTypeToolResult, expected: models.RuntimeProgressStrengthStrong, wantOK: true},
		{name: "question", entry: LogEntry{Level: "question"}, expectedType: models.RuntimeProgressTypeQuestionBlocked, expected: models.RuntimeProgressStrengthStrong, wantOK: true},
		{name: "tool result output", entry: LogEntry{Level: "output", Metadata: map[string]any{"type": "tool_result"}}, expectedType: models.RuntimeProgressTypeToolResult, expected: models.RuntimeProgressStrengthStrong, wantOK: true},
		{name: "assistant output", entry: LogEntry{Level: "output"}, expectedType: models.RuntimeProgressTypeAssistantOutput, expected: models.RuntimeProgressStrengthWeak, wantOK: true},
		{name: "debug", entry: LogEntry{Level: "debug"}, expectedType: models.RuntimeProgressTypeAssistantReason, expected: models.RuntimeProgressStrengthWeak, wantOK: true},
		{name: "debug item started command execution", entry: LogEntry{Level: "debug", Message: `{"type":"item.started","item":{"type":"command_execution"}}`}, expectedType: models.RuntimeProgressTypeToolUse, expected: models.RuntimeProgressStrengthWeak, wantOK: true},
		{name: "unknown", entry: LogEntry{Level: "trace"}, expectedType: models.RuntimeProgressTypeNone, expected: models.RuntimeProgressStrengthNone, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			progressType, strength, ok := runtimeProgressFromLog(tt.entry)
			require.Equal(t, tt.expectedType, progressType, "runtimeProgressFromLog should map the progress type correctly")
			require.Equal(t, tt.expected, strength, "runtimeProgressFromLog should map the progress strength correctly")
			require.Equal(t, tt.wantOK, ok, "runtimeProgressFromLog should report whether the log entry counts as progress")
		})
	}
}

func TestRuntimeController_BeginCapturesLockToken(t *testing.T) {
	t.Parallel()

	sessionStore := &runtimeTestSessionStore{}
	controller := newRuntimeController(
		runtimeConfig{
			SoftBudget:             time.Second,
			NoProgressTimeout:      10 * time.Second,
			GracefulShutdownWindow: time.Second,
			ExtensionIncrement:     2 * time.Second,
			MaxAutomaticExtension:  2 * time.Second,
			AbsoluteRuntimeCeiling: 5 * time.Second,
			QueueAgeThreshold:      time.Minute,
		},
		sessionStore,
		&runtimeTestJobStore{},
		nil,
		zerolog.Nop(),
		uuid.New(),
		uuid.New(),
		3,
		nil,
		newRuntimeProgressTracker(time.Now()),
	)

	startedAt := time.Now().UTC()
	lockToken := uuid.New()
	ctx := jobctx.WithLockToken(context.Background(), lockToken)
	require.NoError(t, controller.Begin(ctx, startedAt, models.CheckpointCapabilityFullResume), "Begin should initialize runtime state")

	require.Equal(t, lockToken, controller.lockToken, "Begin should capture the worker lock token from the job context")
}

func TestRuntimeController_ShouldExtend_GatesOnQueuePressureAndProgress(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	tests := []struct {
		name        string
		isDraining  bool
		running     int
		runningErr  error
		queueAge    time.Duration
		queueOK     bool
		queueErr    error
		lastStrong  time.Time
		expectAllow bool
	}{
		{name: "rejects while draining", isDraining: true, lastStrong: now, expectAllow: false},
		{name: "rejects above concurrency limit", running: 4, lastStrong: now, expectAllow: false},
		{name: "ignores concurrency count errors", runningErr: errors.New("count failed"), lastStrong: now, expectAllow: true},
		{name: "rejects old queue backlog", queueAge: 3 * time.Minute, queueOK: true, lastStrong: now, expectAllow: false},
		{name: "ignores queue age errors", queueErr: errors.New("queue failed"), lastStrong: now, expectAllow: true},
		{name: "rejects without strong progress", lastStrong: time.Time{}, expectAllow: false},
		{name: "rejects stale strong progress", lastStrong: now.Add(-3 * time.Second), expectAllow: false},
		{name: "allows recent strong progress", lastStrong: now.Add(-time.Second), expectAllow: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sessionStore := &runtimeTestSessionStore{countRunning: tt.running, countRunningErr: tt.runningErr}
			backlogStore := &runtimeTestJobBacklogStore{age: tt.queueAge, ok: tt.queueOK, err: tt.queueErr}
			controller := newRuntimeController(
				runtimeConfig{
					ExtensionIncrement:     2 * time.Second,
					AbsoluteRuntimeCeiling: 10 * time.Second,
					QueueAgeThreshold:      2 * time.Minute,
				},
				sessionStore,
				backlogStore,
				nil,
				zerolog.Nop(),
				uuid.New(),
				uuid.New(),
				3,
				func() bool { return tt.isDraining },
				newRuntimeProgressTracker(now),
			)

			require.Equal(t, tt.expectAllow, controller.shouldExtend(context.Background(), now, tt.lastStrong), "shouldExtend should enforce queue-pressure and progress gates")
		})
	}
}

func TestRuntimeController_TryExtendHandlesStaleDeadlinesAndGrantOutcomes(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()

	t.Run("returns true when another worker already extended", func(t *testing.T) {
		t.Parallel()

		store := &runtimeTestSessionStore{}
		controller := newRuntimeController(
			runtimeConfig{SoftBudget: time.Second, ExtensionIncrement: 2 * time.Second, AbsoluteRuntimeCeiling: 10 * time.Second},
			store,
			&runtimeTestJobStore{},
			nil,
			zerolog.Nop(),
			orgID,
			sessionID,
			3,
			nil,
			newRuntimeProgressTracker(now),
		)
		controller.startedAt = now.Add(-2 * time.Second)
		controller.softDeadline = now.Add(2 * time.Second)
		controller.hardDeadline = now.Add(10 * time.Second)

		require.True(t, controller.tryExtend(context.Background(), now), "tryExtend should treat stale expected deadlines as already-handled success")
		require.Equal(t, 0, store.extensionGrants, "tryExtend should not write when the deadline already changed")
	})

	t.Run("stops extending at the configured cap", func(t *testing.T) {
		t.Parallel()

		store := &runtimeTestSessionStore{}
		controller := newRuntimeController(
			runtimeConfig{
				SoftBudget:             2 * time.Second,
				ExtensionIncrement:     2 * time.Second,
				MaxAutomaticExtension:  2 * time.Second,
				AbsoluteRuntimeCeiling: 30 * time.Second,
			},
			store,
			&runtimeTestJobStore{},
			nil,
			zerolog.Nop(),
			orgID,
			sessionID,
			3,
			nil,
			newRuntimeProgressTracker(now),
		)
		controller.startedAt = now.Add(-4 * time.Second)
		controller.softDeadline = controller.startedAt.Add(4 * time.Second)
		controller.hardDeadline = now.Add(30 * time.Second)

		require.False(t, controller.tryExtend(context.Background(), controller.softDeadline), "tryExtend should stop once the max automatic extension has been consumed")
	})

	t.Run("returns false on grant errors", func(t *testing.T) {
		t.Parallel()

		store := &runtimeTestSessionStore{grantErr: errors.New("update failed")}
		controller := newRuntimeController(
			runtimeConfig{SoftBudget: time.Second, ExtensionIncrement: 2 * time.Second, AbsoluteRuntimeCeiling: 10 * time.Second},
			store,
			&runtimeTestJobStore{},
			nil,
			zerolog.Nop(),
			orgID,
			sessionID,
			3,
			nil,
			newRuntimeProgressTracker(now),
		)
		controller.startedAt = now.Add(-time.Second)
		controller.softDeadline = now
		controller.hardDeadline = now.Add(10 * time.Second)

		require.False(t, controller.tryExtend(context.Background(), now), "tryExtend should fail closed when the session store write errors")
	})

	t.Run("returns false when grant is denied", func(t *testing.T) {
		t.Parallel()

		store := &runtimeTestSessionStore{grantConfigured: true, grantAllowed: false}
		controller := newRuntimeController(
			runtimeConfig{SoftBudget: time.Second, ExtensionIncrement: 2 * time.Second, AbsoluteRuntimeCeiling: 10 * time.Second},
			store,
			&runtimeTestJobStore{},
			nil,
			zerolog.Nop(),
			orgID,
			sessionID,
			3,
			nil,
			newRuntimeProgressTracker(now),
		)
		controller.startedAt = now.Add(-time.Second)
		controller.softDeadline = now
		controller.hardDeadline = now.Add(10 * time.Second)

		require.False(t, controller.tryExtend(context.Background(), now), "tryExtend should fail closed when another writer wins the compare-and-swap")
	})

	t.Run("grants extension and carries the lock token", func(t *testing.T) {
		t.Parallel()

		store := &runtimeTestSessionStore{}
		controller := newRuntimeController(
			runtimeConfig{SoftBudget: time.Second, ExtensionIncrement: 2 * time.Second, AbsoluteRuntimeCeiling: 10 * time.Second},
			store,
			&runtimeTestJobStore{},
			nil,
			zerolog.Nop(),
			orgID,
			sessionID,
			3,
			nil,
			newRuntimeProgressTracker(now),
		)
		controller.startedAt = now.Add(-time.Second)
		controller.softDeadline = now
		controller.hardDeadline = now.Add(10 * time.Second)
		controller.lockToken = uuid.New()

		require.True(t, controller.tryExtend(context.Background(), now), "tryExtend should extend when the session store grants the compare-and-swap")
		require.Equal(t, controller.lockToken, store.lastGrantLockToken, "tryExtend should pass the worker lock token through to the store")
		require.Equal(t, 2, store.lastGrantExtensionSeconds, "tryExtend should persist the granted extension in seconds")
		require.True(t, controller.softDeadline.After(now), "tryExtend should advance the in-memory soft deadline after a successful grant")
	})
}

func TestRuntimeController_TickPersistsProgressAndRequestsStops(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()

	t.Run("persists fresh progress", func(t *testing.T) {
		t.Parallel()

		store := &runtimeTestSessionStore{recordRuntimeProgressErr: errors.New("write failed")}
		controller := newRuntimeController(
			runtimeConfig{
				SoftBudget:             10 * time.Second,
				NoProgressTimeout:      time.Minute,
				ExtensionIncrement:     2 * time.Second,
				AbsoluteRuntimeCeiling: time.Hour,
			},
			store,
			&runtimeTestJobStore{},
			nil,
			zerolog.Nop(),
			uuid.New(),
			uuid.New(),
			3,
			nil,
			newRuntimeProgressTracker(now),
		)
		controller.softDeadline = now.Add(5 * time.Second)
		controller.hardDeadline = now.Add(time.Minute)
		controller.tracker.Record(models.RuntimeProgressTypeToolResult, models.RuntimeProgressStrengthStrong, now.Add(time.Second))

		controller.tick(context.Background(), now)
		require.Equal(t, 1, store.recordRuntimeProgressCalls, "tick should attempt to persist fresh runtime progress once")
		require.Equal(t, models.RuntimeProgressTypeToolResult, store.lastProgressType, "tick should persist the observed progress type")
	})

	t.Run("requests no-progress stop", func(t *testing.T) {
		t.Parallel()

		controller := newRuntimeController(
			runtimeConfig{
				SoftBudget:             10 * time.Second,
				NoProgressTimeout:      2 * time.Second,
				ExtensionIncrement:     time.Second,
				AbsoluteRuntimeCeiling: time.Hour,
			},
			&runtimeTestSessionStore{},
			&runtimeTestJobStore{},
			nil,
			zerolog.Nop(),
			uuid.New(),
			uuid.New(),
			3,
			nil,
			newRuntimeProgressTracker(now.Add(-5*time.Second)),
		)
		controller.softDeadline = now.Add(5 * time.Second)
		controller.hardDeadline = now.Add(time.Minute)

		controller.tick(context.Background(), now)
		require.Equal(t, StopReasonNoProgress, controller.stopRequested, "tick should request a no-progress stop after the configured idle timeout")
	})

	t.Run("does not request no-progress stop while tool is active", func(t *testing.T) {
		t.Parallel()

		controller := newRuntimeController(
			runtimeConfig{
				SoftBudget:             10 * time.Second,
				NoProgressTimeout:      2 * time.Second,
				ExtensionIncrement:     time.Second,
				AbsoluteRuntimeCeiling: time.Hour,
			},
			&runtimeTestSessionStore{},
			&runtimeTestJobStore{},
			nil,
			zerolog.Nop(),
			uuid.New(),
			uuid.New(),
			3,
			nil,
			newRuntimeProgressTracker(now.Add(-5*time.Second)),
		)
		controller.softDeadline = now.Add(5 * time.Second)
		controller.hardDeadline = now.Add(time.Minute)
		controller.tracker.Record(models.RuntimeProgressTypeToolUse, models.RuntimeProgressStrengthWeak, now.Add(-5*time.Second))

		controller.tick(context.Background(), now)
		require.Equal(t, StopReasonNone, controller.stopRequested, "tick should not request a no-progress stop while a tool command is still active")
	})

	t.Run("requests absolute-ceiling stop", func(t *testing.T) {
		t.Parallel()

		controller := newRuntimeController(
			runtimeConfig{
				SoftBudget:             10 * time.Second,
				NoProgressTimeout:      time.Hour,
				ExtensionIncrement:     time.Second,
				AbsoluteRuntimeCeiling: 3 * time.Second,
			},
			&runtimeTestSessionStore{},
			&runtimeTestJobStore{},
			nil,
			zerolog.Nop(),
			uuid.New(),
			uuid.New(),
			3,
			nil,
			newRuntimeProgressTracker(now),
		)
		controller.softDeadline = now.Add(5 * time.Second)
		controller.hardDeadline = now

		controller.tick(context.Background(), now)
		require.Equal(t, StopReasonAbsoluteCeiling, controller.stopRequested, "tick should request an absolute-ceiling stop when the hard deadline is reached")
	})

	t.Run("requests soft-budget stop when extension is not allowed", func(t *testing.T) {
		t.Parallel()

		controller := newRuntimeController(
			runtimeConfig{
				SoftBudget:             time.Second,
				NoProgressTimeout:      time.Hour,
				ExtensionIncrement:     2 * time.Second,
				AbsoluteRuntimeCeiling: 10 * time.Second,
				QueueAgeThreshold:      time.Minute,
			},
			&runtimeTestSessionStore{},
			&runtimeTestJobStore{},
			nil,
			zerolog.Nop(),
			uuid.New(),
			uuid.New(),
			3,
			nil,
			newRuntimeProgressTracker(now.Add(-5*time.Second)),
		)
		controller.softDeadline = now
		controller.hardDeadline = now.Add(10 * time.Second)

		controller.tick(context.Background(), now)
		require.Equal(t, StopReasonSoftBudget, controller.stopRequested, "tick should request a soft-budget stop when no bounded extension is available")
	})
}
