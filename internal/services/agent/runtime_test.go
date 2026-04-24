package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type runtimeTestSessionStore struct {
	countRunning    int
	extensionGrants int
}

func (s *runtimeTestSessionStore) UpdateStatus(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateResult(context.Context, uuid.UUID, uuid.UUID, string, *models.SessionResult) error {
	return nil
}

func (s *runtimeTestSessionStore) CountRunningByOrg(context.Context, uuid.UUID) (int, error) {
	return s.countRunning, nil
}

func (s *runtimeTestSessionStore) UpdateTurnComplete(context.Context, uuid.UUID, uuid.UUID, int, *models.SessionResult, string, string) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateSnapshotInfo(context.Context, uuid.UUID, uuid.UUID, string, string) error {
	return nil
}

func (s *runtimeTestSessionStore) BeginRuntime(context.Context, uuid.UUID, uuid.UUID, models.CheckpointCapability, time.Time, time.Time, time.Time) error {
	return nil
}

func (s *runtimeTestSessionStore) RecordRuntimeProgress(context.Context, uuid.UUID, uuid.UUID, models.RuntimeProgressType, models.RuntimeProgressStrength, time.Time) error {
	return nil
}

func (s *runtimeTestSessionStore) GrantRuntimeExtension(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time, time.Time, time.Time, int) (bool, error) {
	s.extensionGrants++
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

func (s *runtimeTestSessionStore) UpdateFailure(context.Context, uuid.UUID, uuid.UUID, string, string, []string, bool) error {
	return nil
}

func (s *runtimeTestSessionStore) UpdateTitle(context.Context, uuid.UUID, uuid.UUID, string) error {
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

type runtimeTestJobStore struct{}

func (s *runtimeTestJobStore) Enqueue(context.Context, uuid.UUID, string, string, any, int, *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (s *runtimeTestJobStore) OldestPendingSessionJobAge(context.Context) (time.Duration, bool, error) {
	return 0, false, nil
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
