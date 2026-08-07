//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// These tests share and truncate one integration database, so they must run
// serially rather than with t.Parallel.

func TestIntegration_ActivityPhaseWritesRejectStaleRuntimeLease(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	session := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusRunning, CurrentTurn: 1})
	thread := seedActivityPhaseThread(t, pool, orgID, session.ID)

	runtimeStore := db.NewThreadRuntimeStore(pool)
	phaseStore := db.NewSessionActivityPhaseStore(pool)
	logStore := db.NewSessionLogStore(pool)

	oldLease := uuid.New()
	oldRuntime := createActivityPhaseRuntime(t, runtimeStore, orgID, session.ID, thread.ID, oldLease)
	oldPhase, err := phaseStore.StartPhase(
		ctx, orgID, session.ID, thread.ID, 1, &oldRuntime.ID, &oldLease,
		models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, time.Now().UTC(),
	)
	require.NoError(t, err, "old runtime should open its activity phase")

	oldGuard := &models.ActivityPhaseWriteGuard{RuntimeID: oldRuntime.ID, LeaseToken: oldLease}
	activeLog := activityPhaseLog(orgID, session.ID, thread.ID, oldPhase.ID, oldGuard, "old runtime active")
	require.NoError(t, logStore.Create(ctx, &activeLog), "current runtime lease should append to its running phase")

	terminal, err := runtimeStore.MarkTerminalWithLease(
		ctx, orgID, oldRuntime.ID, oldLease, models.ThreadRuntimeStatusLost, "integration replacement", "",
	)
	require.NoError(t, err, "old runtime should be marked lost")
	require.True(t, terminal, "old runtime terminal transition should use its exact lease")
	_, err = phaseStore.CompletePhase(
		ctx, orgID, oldPhase.ID, models.ActivityPhaseStatusInterrupted,
		models.ActivityPhaseBoundaryRuntimeLost, time.Now().UTC(),
	)
	require.NoError(t, err, "lost runtime phase should close before replacement starts")

	lateOldLog := activityPhaseLog(orgID, session.ID, thread.ID, oldPhase.ID, oldGuard, "late old runtime output")
	err = logStore.Create(ctx, &lateOldLog)
	require.ErrorContains(t, err, "active runtime lease", "terminal phase should reject late output from its former worker")

	newLease := uuid.New()
	newRuntime := createActivityPhaseRuntime(t, runtimeStore, orgID, session.ID, thread.ID, newLease)
	newPhase, err := phaseStore.StartPhase(
		ctx, orgID, session.ID, thread.ID, 1, &newRuntime.ID, &newLease,
		models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerRecovery}, time.Now().UTC(),
	)
	require.NoError(t, err, "replacement runtime should open a recovery phase")

	staleReplacementLog := activityPhaseLog(orgID, session.ID, thread.ID, newPhase.ID, oldGuard, "stale worker targets replacement")
	err = logStore.Create(ctx, &staleReplacementLog)
	require.ErrorContains(t, err, "active runtime lease", "old runtime lease should not mutate its replacement phase")

	newGuard := &models.ActivityPhaseWriteGuard{RuntimeID: newRuntime.ID, LeaseToken: newLease}
	replacementLog := activityPhaseLog(orgID, session.ID, thread.ID, newPhase.ID, newGuard, "replacement runtime active")
	require.NoError(t, logStore.Create(ctx, &replacementLog), "replacement runtime should append with its own lease")
}

func TestIntegration_TranscriptWindowUsesCoherentPhaseEntryAndStatusSnapshot(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	session := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusRunning, CurrentTurn: 1})
	thread := seedActivityPhaseThread(t, pool, orgID, session.ID)

	phaseStore := db.NewSessionActivityPhaseStore(pool)
	phase, err := phaseStore.StartPhase(
		ctx, orgID, session.ID, thread.ID, 1, nil, nil,
		models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, time.Now().UTC(),
	)
	require.NoError(t, err, "runtime-less phase should start for the snapshot fixture")

	snapshot, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	require.NoError(t, err, "repeatable-read transcript snapshot should begin")
	t.Cleanup(func() {
		require.NoError(t, snapshot.Rollback(ctx), "snapshot should remain open until the test completes")
	})

	snapshotStore := db.NewSessionTranscriptStore(snapshot)
	before, err := snapshotStore.ListThreadWindow(ctx, orgID, thread.ID, db.SessionTranscriptWindowOptions{
		Include: db.DefaultTranscriptInclude(),
	})
	require.NoError(t, err, "initial snapshot window should load")
	require.Equal(t, models.ThreadStatusRunning, before.ThreadStatus, "snapshot should begin with the running thread status")
	require.Empty(t, before.Rows, "snapshot should begin before the assistant boundary message commits")
	require.Len(t, before.Phases[1], 1, "snapshot fixture should contain exactly one running phase")
	require.Equal(t, models.ActivityPhaseStatusRunning, before.Phases[1][0].Status, "snapshot should begin with the running phase")

	message := &models.SessionMessage{
		SessionID: session.ID, OrgID: orgID, ThreadID: &thread.ID, TurnNumber: 1,
		Role: models.MessageRoleAssistant, Content: "completed response", ActivityPhaseID: &phase.ID,
	}
	_, err = phaseStore.CreateAssistantMessageAndCompletePhase(
		ctx, orgID, phase.ID, message, models.ActivityPhaseBoundaryFinalResponse, time.Now().UTC(),
	)
	require.NoError(t, err, "assistant boundary should atomically commit its message and terminal phase")
	require.NoError(t, db.NewSessionThreadStore(pool).UpdateStatus(ctx, orgID, thread.ID, models.ThreadStatusCompleted), "thread should transition after its final boundary")

	during, err := snapshotStore.ListThreadWindow(ctx, orgID, thread.ID, db.SessionTranscriptWindowOptions{
		Include: db.DefaultTranscriptInclude(),
	})
	require.NoError(t, err, "existing snapshot should remain readable after the concurrent boundary")
	require.Equal(t, models.ThreadStatusRunning, during.ThreadStatus, "existing snapshot should retain its original thread status")
	require.Empty(t, during.Rows, "existing snapshot should not mix in the newly committed message")
	require.Len(t, during.Phases[1], 1, "existing snapshot should retain exactly its original phase")
	require.Equal(t, models.ActivityPhaseStatusRunning, during.Phases[1][0].Status, "existing snapshot should not mix in the newly terminal phase")

	after, err := db.NewSessionTranscriptStore(pool).ListThreadWindow(ctx, orgID, thread.ID, db.SessionTranscriptWindowOptions{
		Include: db.DefaultTranscriptInclude(),
	})
	require.NoError(t, err, "fresh snapshot should load the committed boundary")
	require.Equal(t, models.ThreadStatusCompleted, after.ThreadStatus, "fresh snapshot should include the committed thread status")
	require.Len(t, after.Rows, 1, "fresh snapshot should contain exactly the committed assistant message")
	require.NotNil(t, after.Rows[0].Message, "fresh snapshot row should be the committed assistant message")
	require.Equal(t, "completed response", after.Rows[0].Message.Content, "fresh snapshot should include the committed assistant message")
	require.Len(t, after.Phases[1], 1, "fresh snapshot should contain exactly the terminal phase")
	require.Equal(t, models.ActivityPhaseStatusCompleted, after.Phases[1][0].Status, "fresh snapshot should include the terminal phase")
}

func seedActivityPhaseThread(t *testing.T, pool *pgxpool.Pool, orgID, sessionID uuid.UUID) models.SessionThread {
	t.Helper()
	thread := models.SessionThread{
		SessionID: sessionID,
		OrgID:     orgID,
		AgentType: models.AgentTypeCodex,
		Label:     "Activity phase integration thread",
		Status:    models.ThreadStatusRunning,
	}
	require.NoError(t, db.NewSessionThreadStore(pool).Create(context.Background(), &thread, models.MaxRunningThreadsPerSession), "activity phase fixture thread should be created")
	return thread
}

func createActivityPhaseRuntime(
	t *testing.T,
	store *db.ThreadRuntimeStore,
	orgID, sessionID, threadID, leaseToken uuid.UUID,
) models.ThreadRuntime {
	t.Helper()
	runtime, err := store.CreateStarting(context.Background(), orgID, db.CreateThreadRuntimeParams{
		SessionID:       sessionID,
		ThreadID:        threadID,
		SandboxID:       uuid.New(),
		ContainerID:     "activity-phase-integration",
		RuntimeHandleID: uuid.NewString(),
		AgentType:       models.AgentTypeCodex,
		OwnerNodeID:     "activity-phase-integration",
		LeaseToken:      leaseToken,
		LeaseDuration:   time.Minute,
	})
	require.NoError(t, err, "activity phase fixture runtime should be created")
	return runtime
}

func activityPhaseLog(
	orgID, sessionID, threadID, phaseID uuid.UUID,
	guard *models.ActivityPhaseWriteGuard,
	message string,
) models.SessionLog {
	return models.SessionLog{
		SessionID: sessionID, OrgID: orgID, ThreadID: &threadID,
		TurnNumber: 1, Level: models.SessionLogLevelInfo, Message: message,
		ActivityPhaseID: &phaseID, ActivityPhaseWriteGuard: guard,
	}
}
