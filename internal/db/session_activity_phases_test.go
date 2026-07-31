package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

var activityPhaseTestColumns = []string{
	"id", "org_id", "session_id", "thread_id", "turn_number", "phase_number",
	"status", "boundary_reason", "started_at", "completed_at", "runtime_id",
	"trigger_kind", "trigger_batch_id", "trigger_sequence_start", "trigger_sequence_end",
	"created_at", "updated_at",
}

var inboxBatchTestColumns = []string{
	"id", "org_id", "session_id", "thread_id", "runtime_id", "sequence_start",
	"sequence_end", "status", "acknowledged_at", "started_at", "abandoned_at",
	"created_at", "updated_at",
}

func TestSessionActivityPhaseStoreStartInboxPhaseMarksEntriesApplied(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
	runtimeID, leaseToken, batchID, phaseID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sequenceStart, sequenceEnd := int64(4), int64(5)
	requestedAt := time.Date(2026, 7, 26, 10, 0, 0, 789, time.UTC)
	startedAt := requestedAt.Truncate(time.Microsecond)
	createdAt := startedAt
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM session_threads").
		WithArgs(orgID, sessionID, threadID).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(threadID))
	mock.ExpectQuery("UPDATE thread_inbox_delivery_batches").
		WithArgs(orgID, batchID, sessionID, threadID, startedAt).
		WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns).AddRow(
			batchID, orgID, sessionID, threadID, runtimeID, sequenceStart, sequenceEnd,
			models.InboxDeliveryBatchStarted, startedAt.Add(-time.Second), &startedAt, nil,
			createdAt, startedAt,
		))
	mock.ExpectExec("UPDATE thread_inbox_entries").
		WithArgs(orgID, sessionID, threadID, runtimeID, int64(4), int64(5), startedAt).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectQuery("(?s)SELECT id FROM thread_runtimes.+lease_expires_at > \\$6.+FOR UPDATE").
		WithArgs(orgID, sessionID, threadID, runtimeID, leaseToken, startedAt).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(runtimeID))
	mock.ExpectQuery("INSERT INTO session_activity_phases").
		WithArgs(
			orgID, sessionID, threadID, 2, startedAt, &runtimeID,
			models.ActivityPhaseTriggerInboxBatch, &batchID, pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(activityPhaseTestColumns).AddRow(
			phaseID, orgID, sessionID, threadID, 2, 1,
			models.ActivityPhaseStatusRunning, nil, startedAt, nil, &runtimeID,
			models.ActivityPhaseTriggerInboxBatch, &batchID, &sequenceStart, &sequenceEnd,
			createdAt, createdAt,
		))
	mock.ExpectCommit()

	phase, err := NewSessionActivityPhaseStore(mock).StartPhase(
		context.Background(), orgID, sessionID, threadID, 2, nil, &leaseToken,
		models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInboxBatch, BatchID: &batchID},
		requestedAt,
	)
	require.NoError(t, err, "StartPhase should atomically apply an acknowledged inbox batch")
	require.Equal(t, startedAt, phase.StartedAt, "StartPhase should persist the normalized execution start time")
	require.NoError(t, mock.ExpectationsWereMet(), "StartPhase should mark every batch entry applied before committing")
}

func TestSessionActivityPhaseStoreStartPhaseRejectsStaleRuntimeLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID, runtimeID, leaseToken := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	startedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM session_threads").
		WithArgs(orgID, sessionID, threadID).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(threadID))
	mock.ExpectQuery("(?s)SELECT id FROM thread_runtimes.+lease_expires_at > \\$6.+FOR UPDATE").
		WithArgs(orgID, sessionID, threadID, runtimeID, leaseToken, startedAt).
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	_, err = NewSessionActivityPhaseStore(mock).StartPhase(
		context.Background(), orgID, sessionID, threadID, 1, &runtimeID, &leaseToken,
		models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, startedAt,
	)
	require.Error(t, err, "StartPhase should reject a runtime without a current matching lease")
	require.NoError(t, mock.ExpectationsWereMet(), "StartPhase should validate runtime lease ownership before inserting")
}

func TestSessionActivityPhaseStoreStartRuntimePhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		trigger models.ActivityPhaseTriggerKind
	}{
		{name: "initial execution", trigger: models.ActivityPhaseTriggerInitial},
		{name: "recovery execution", trigger: models.ActivityPhaseTriggerRecovery},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "activity phase store test should create a database mock")
			t.Cleanup(mock.Close)

			orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
			runtimeID, leaseToken, phaseID := uuid.New(), uuid.New(), uuid.New()
			startedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT id FROM session_threads").
				WithArgs(orgID, sessionID, threadID).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(threadID))
			mock.ExpectQuery("(?s)SELECT id FROM thread_runtimes.+lease_expires_at > \\$6.+FOR UPDATE").
				WithArgs(orgID, sessionID, threadID, runtimeID, leaseToken, startedAt).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(runtimeID))
			mock.ExpectQuery("INSERT INTO session_activity_phases").
				WithArgs(orgID, sessionID, threadID, 1, startedAt, &runtimeID, tt.trigger, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(activityPhaseTestColumns).AddRow(
					phaseID, orgID, sessionID, threadID, 1, 1,
					models.ActivityPhaseStatusRunning, nil, startedAt, nil, &runtimeID,
					tt.trigger, nil, nil, nil, startedAt, startedAt,
				))
			mock.ExpectCommit()

			phase, err := NewSessionActivityPhaseStore(mock).StartPhase(
				context.Background(), orgID, sessionID, threadID, 1, &runtimeID, &leaseToken,
				models.ActivityPhaseTrigger{Kind: tt.trigger}, startedAt,
			)
			require.NoError(t, err, "StartPhase should start execution with a valid current runtime lease")
			require.Equal(t, tt.trigger, phase.TriggerKind, "StartPhase should preserve the exact lifecycle trigger")
			require.NoError(t, mock.ExpectationsWereMet(), "StartPhase should atomically validate the runtime and insert the phase")
		})
	}
}

func TestSessionActivityPhaseStoreAcknowledgeInboxBatchReplaysAfterRuntimeLoss(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID, runtimeID, leaseToken, batchID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	acknowledgedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	startedAt := acknowledgedAt.Add(time.Second)
	mock.ExpectQuery("SELECT .* FROM thread_inbox_delivery_batches").
		WithArgs(orgID, sessionID, threadID, runtimeID, int64(5)).
		WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns).AddRow(
			batchID, orgID, sessionID, threadID, runtimeID, int64(4), int64(5),
			models.InboxDeliveryBatchStarted, acknowledgedAt, &startedAt, nil, acknowledgedAt, startedAt,
		))

	batch, phaseTransitioned, err := NewSessionActivityPhaseStore(mock).AcknowledgeInboxBatchWithTransition(
		context.Background(), orgID, sessionID, threadID, runtimeID, leaseToken, nil, 5, acknowledgedAt.Add(time.Minute),
	)
	require.NoError(t, err, "AcknowledgeInboxBatch should replay durable success after the runtime lease is gone")
	require.False(t, phaseTransitioned, "replayed acknowledgment should not report another phase transition")
	require.Equal(t, batchID, batch.ID, "AcknowledgeInboxBatch replay should return the exact durable batch")
	require.NoError(t, mock.ExpectationsWereMet(), "AcknowledgeInboxBatch replay should not require a live runtime transaction")
}

func TestSessionActivityPhaseStoreAcknowledgeInboxBatchCommitsBoundaryAtomically(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
	runtimeID, leaseToken, phaseID, batchID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	acknowledgedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT .* FROM thread_inbox_delivery_batches").
		WithArgs(orgID, sessionID, threadID, runtimeID, int64(5)).
		WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT last_acked_sequence").
		WithArgs(orgID, sessionID, threadID, runtimeID, leaseToken, acknowledgedAt).
		WillReturnRows(pgxmock.NewRows([]string{"last_acked_sequence"}).AddRow(int64(3)))
	mock.ExpectQuery("SELECT count").
		WithArgs(orgID, sessionID, threadID, runtimeID, int64(4), int64(5)).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectExec("UPDATE session_activity_phases").
		WithArgs(orgID, phaseID, acknowledgedAt, sessionID, threadID, runtimeID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO thread_inbox_delivery_batches").
		WithArgs(orgID, sessionID, threadID, runtimeID, int64(4), int64(5), acknowledgedAt).
		WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns).AddRow(
			batchID, orgID, sessionID, threadID, runtimeID, int64(4), int64(5),
			models.InboxDeliveryBatchAcknowledged, acknowledgedAt, nil, nil, acknowledgedAt, acknowledgedAt,
		))
	mock.ExpectExec("UPDATE thread_inbox_entries").
		WithArgs(orgID, sessionID, threadID, runtimeID, int64(4), int64(5), acknowledgedAt).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectExec("UPDATE thread_runtimes").
		WithArgs(orgID, sessionID, threadID, runtimeID, leaseToken, int64(5), acknowledgedAt, int64(3)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	batch, phaseTransitioned, err := NewSessionActivityPhaseStore(mock).AcknowledgeInboxBatchWithTransition(
		context.Background(), orgID, sessionID, threadID, runtimeID, leaseToken, &phaseID, 5, acknowledgedAt,
	)
	require.NoError(t, err, "AcknowledgeInboxBatch should atomically close steering, persist its batch, and advance the watermark")
	require.True(t, phaseTransitioned, "fresh steering acknowledgment should report its durable phase transition")
	require.Equal(t, batchID, batch.ID, "AcknowledgeInboxBatch should return the exact acknowledged delivery batch")
	require.NoError(t, mock.ExpectationsWereMet(), "AcknowledgeInboxBatch should commit every platform-owned boundary write together")
}

func TestSessionActivityPhaseStoreAcknowledgeInboxBatchRejectsDiscontinuousRange(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID, runtimeID, leaseToken := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	acknowledgedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT .* FROM thread_inbox_delivery_batches").
		WithArgs(orgID, sessionID, threadID, runtimeID, int64(4)).
		WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT last_acked_sequence").
		WithArgs(orgID, sessionID, threadID, runtimeID, leaseToken, acknowledgedAt).
		WillReturnRows(pgxmock.NewRows([]string{"last_acked_sequence"}).AddRow(int64(5)))
	mock.ExpectRollback()

	_, err = NewSessionActivityPhaseStore(mock).AcknowledgeInboxBatch(
		context.Background(), orgID, sessionID, threadID, runtimeID, leaseToken, nil, 4, acknowledgedAt,
	)
	require.ErrorIs(t, err, ErrInboxBatchConflict, "AcknowledgeInboxBatch should reject a range behind the durable watermark")
	require.NoError(t, mock.ExpectationsWereMet(), "AcknowledgeInboxBatch should roll back discontinuous acknowledgments")
}

func TestSessionActivityPhaseStoreAcknowledgeInboxBatchRejectsExpiredLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
	runtimeID, leaseToken := uuid.New(), uuid.New()
	acknowledgedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT .* FROM thread_inbox_delivery_batches").
		WithArgs(orgID, sessionID, threadID, runtimeID, int64(5)).
		WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("(?s)SELECT last_acked_sequence.+lease_expires_at > \\$6.+FOR UPDATE").
		WithArgs(orgID, sessionID, threadID, runtimeID, leaseToken, acknowledgedAt).
		WillReturnRows(pgxmock.NewRows([]string{"last_acked_sequence"}))
	mock.ExpectRollback()

	_, err = NewSessionActivityPhaseStore(mock).AcknowledgeInboxBatch(
		context.Background(), orgID, sessionID, threadID, runtimeID, leaseToken, nil, 5, acknowledgedAt,
	)

	require.Error(t, err, "AcknowledgeInboxBatch should reject an expired runtime lease")
	require.NoError(t, mock.ExpectationsWereMet(), "expired acknowledgment should not mutate phases, messages, batches, or the runtime watermark")
}

func TestSessionActivityPhaseStoreAbandonInboxBatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID, runtimeID, batchID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	acknowledgedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	abandonedAt := acknowledgedAt.Add(time.Minute)
	mock.ExpectQuery("UPDATE thread_inbox_delivery_batches").
		WithArgs(orgID, batchID, abandonedAt).
		WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns).AddRow(
			batchID, orgID, sessionID, threadID, runtimeID, int64(4), int64(5),
			models.InboxDeliveryBatchAbandoned, acknowledgedAt, nil, &abandonedAt, acknowledgedAt, abandonedAt,
		))

	batch, err := NewSessionActivityPhaseStore(mock).AbandonInboxBatch(context.Background(), orgID, batchID, abandonedAt)
	require.NoError(t, err, "AbandonInboxBatch should terminally abandon an acknowledged batch")
	require.Equal(t, models.InboxDeliveryBatchAbandoned, batch.Status, "AbandonInboxBatch should return the exact terminal batch state")
	require.NoError(t, mock.ExpectationsWereMet(), "AbandonInboxBatch should scope the transition by organization and batch")
}

func TestSessionActivityPhaseStoreAbandonInboxBatchRejectsTerminalBatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, batchID := uuid.New(), uuid.New()
	abandonedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery("UPDATE thread_inbox_delivery_batches").
		WithArgs(orgID, batchID, abandonedAt).
		WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns))

	_, err = NewSessionActivityPhaseStore(mock).AbandonInboxBatch(context.Background(), orgID, batchID, abandonedAt)
	require.True(t, errors.Is(err, ErrInboxBatchConflict), "AbandonInboxBatch should reject a second terminal transition")
	require.NoError(t, mock.ExpectationsWereMet(), "AbandonInboxBatch should preserve immutable terminal batches")
}

func TestSessionActivityPhaseStoreCompletePhase(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID, phaseID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	startedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(time.Minute)
	reason := models.ActivityPhaseBoundaryFinalResponse
	expected := models.SessionActivityPhase{
		ID: phaseID, OrgID: orgID, SessionID: sessionID, ThreadID: threadID,
		TurnNumber: 1, PhaseNumber: 1, Status: models.ActivityPhaseStatusCompleted,
		BoundaryReason: reason, StartedAt: startedAt, CompletedAt: &completedAt,
		TriggerKind: models.ActivityPhaseTriggerInitial, CreatedAt: startedAt, UpdatedAt: completedAt,
	}
	mock.ExpectQuery("UPDATE session_activity_phases").
		WithArgs(orgID, phaseID, models.ActivityPhaseStatusCompleted, reason, completedAt).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "thread_id", "turn_number", "phase_number",
			"status", "boundary_reason", "started_at", "completed_at", "runtime_id",
			"trigger_kind", "trigger_batch_id", "trigger_sequence_start", "trigger_sequence_end",
			"created_at", "updated_at",
		}).AddRow(
			phaseID, orgID, sessionID, threadID, 1, 1,
			models.ActivityPhaseStatusCompleted, reason, startedAt, &completedAt, nil,
			models.ActivityPhaseTriggerInitial, nil, nil, nil, startedAt, completedAt,
		))

	actual, transitioned, err := NewSessionActivityPhaseStore(mock).CompletePhaseWithTransition(
		context.Background(), orgID, phaseID, models.ActivityPhaseStatusCompleted, reason, completedAt,
	)
	require.NoError(t, err, "CompletePhase should terminally transition a running phase")
	require.True(t, transitioned, "fresh completion should report its durable terminal transition")
	require.Equal(t, expected, actual, "CompletePhase should return the exact persisted terminal phase")
	require.NoError(t, mock.ExpectationsWereMet(), "CompletePhase should scope its update by org and phase")
}

func TestSessionActivityPhaseStoreCompletePhaseIdempotentAtPostgresPrecision(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID, phaseID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	startedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	requestedAt := startedAt.Add(time.Minute + 789*time.Nanosecond)
	persistedAt := requestedAt.Truncate(time.Microsecond)
	reason := models.ActivityPhaseBoundaryFinalResponse
	columns := []string{
		"id", "org_id", "session_id", "thread_id", "turn_number", "phase_number",
		"status", "boundary_reason", "started_at", "completed_at", "runtime_id",
		"trigger_kind", "trigger_batch_id", "trigger_sequence_start", "trigger_sequence_end",
		"created_at", "updated_at",
	}
	row := func() *pgxmock.Rows {
		return pgxmock.NewRows(columns).AddRow(
			phaseID, orgID, sessionID, threadID, 1, 1,
			models.ActivityPhaseStatusCompleted, reason, startedAt, &persistedAt, nil,
			models.ActivityPhaseTriggerInitial, nil, nil, nil, startedAt, persistedAt,
		)
	}
	mock.ExpectQuery("UPDATE session_activity_phases").
		WithArgs(orgID, phaseID, models.ActivityPhaseStatusCompleted, reason, persistedAt).
		WillReturnRows(pgxmock.NewRows(columns))
	mock.ExpectQuery("SELECT .* FROM session_activity_phases").
		WithArgs(orgID, phaseID).
		WillReturnRows(row())

	actual, transitioned, err := NewSessionActivityPhaseStore(mock).CompletePhaseWithTransition(
		context.Background(), orgID, phaseID, models.ActivityPhaseStatusCompleted, reason, requestedAt,
	)
	require.NoError(t, err, "CompletePhase should accept an exact retry after PostgreSQL timestamp normalization")
	require.False(t, transitioned, "idempotent completion retry should not report another terminal transition")
	require.Equal(t, persistedAt, *actual.CompletedAt, "CompletePhase should return the normalized persisted completion time")
	require.NoError(t, mock.ExpectationsWereMet(), "CompletePhase should compare retries at PostgreSQL timestamp precision")
}

func TestSessionActivityPhaseStoreCompletePhaseRejectsConflictingRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID, phaseID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	startedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(time.Minute)
	persistedReason := models.ActivityPhaseBoundaryFinalResponse
	requestedReason := models.ActivityPhaseBoundaryHumanInput
	mock.ExpectQuery("UPDATE session_activity_phases").
		WithArgs(orgID, phaseID, models.ActivityPhaseStatusCompleted, requestedReason, completedAt).
		WillReturnRows(pgxmock.NewRows(activityPhaseTestColumns))
	mock.ExpectQuery("SELECT .* FROM session_activity_phases").
		WithArgs(orgID, phaseID).
		WillReturnRows(pgxmock.NewRows(activityPhaseTestColumns).AddRow(
			phaseID, orgID, sessionID, threadID, 1, 1,
			models.ActivityPhaseStatusCompleted, persistedReason, startedAt, &completedAt, nil,
			models.ActivityPhaseTriggerInitial, nil, nil, nil, startedAt, completedAt,
		))

	_, err = NewSessionActivityPhaseStore(mock).CompletePhase(
		context.Background(), orgID, phaseID, models.ActivityPhaseStatusCompleted, requestedReason, completedAt,
	)
	require.ErrorIs(t, err, ErrActivityPhaseConflict, "CompletePhase should reject a retry whose terminal values differ")
	require.NoError(t, mock.ExpectationsWereMet(), "CompletePhase should compare the exact persisted terminal values")
}

func TestSessionActivityPhaseStoreListStrandedRunningExcludesActiveRuntimeLessPhases(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID := uuid.New()
	expiredBefore := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)FROM session_activity_phases p.+JOIN session_threads t ON t\.org_id = p\.org_id AND t\.session_id = p\.session_id AND t\.id = p\.thread_id.+WHERE p\.org_id = \$1 AND p\.status = 'running'.+\(p\.runtime_id IS NULL AND p\.started_at < \$2 AND t\.status NOT IN \('pending', 'running'\)\).+OR \(p\.runtime_id IS NOT NULL AND \(.+r\.id IS NULL`).
		WithArgs(orgID, expiredBefore, 100).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "thread_id", "turn_number", "phase_number",
			"status", "boundary_reason", "started_at", "completed_at", "runtime_id",
			"trigger_kind", "trigger_batch_id", "trigger_sequence_start", "trigger_sequence_end",
			"created_at", "updated_at",
		}))

	phases, err := NewSessionActivityPhaseStore(mock).ListStrandedRunning(
		context.Background(), orgID, expiredBefore, 100,
	)
	require.NoError(t, err, "ListStrandedRunning should query expired leases and inactive runtime-less phases")
	require.Equal(t, []models.SessionActivityPhase{}, phases, "ListStrandedRunning should return the exact database result")
	require.NoError(t, mock.ExpectationsWereMet(), "ListStrandedRunning should exclude active runtime-less phases before applying the bounded limit")
}

func TestSessionActivityPhaseStoreReconcileStrandedPhaseRequiresInactiveThreadForRuntimeLessPhase(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, phaseID := uuid.New(), uuid.New()
	threadID := uuid.New()
	expiredBefore := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	completedAt := expiredBefore.Add(time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT thread_id, runtime_id").
		WithArgs(orgID, phaseID).
		WillReturnRows(pgxmock.NewRows([]string{"thread_id", "runtime_id"}).
			AddRow(threadID, nil))
	mock.ExpectQuery("SELECT status FROM session_threads").
		WithArgs(orgID, threadID).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow(models.ThreadStatusRunning))
	mock.ExpectQuery("SELECT thread_id, runtime_id, started_at").
		WithArgs(orgID, phaseID).
		WillReturnRows(pgxmock.NewRows([]string{"thread_id", "runtime_id", "started_at"}).
			AddRow(threadID, nil, expiredBefore.Add(-time.Minute)))
	mock.ExpectCommit()

	reconciled, err := NewSessionActivityPhaseStore(mock).ReconcileStrandedPhase(
		context.Background(), orgID, phaseID, expiredBefore, completedAt,
	)
	require.NoError(t, err, "ReconcileStrandedPhase should require durable inactive-thread evidence for runtime-less phases")
	require.False(t, reconciled, "ReconcileStrandedPhase should preserve a phase that does not qualify")
	require.NoError(t, mock.ExpectationsWereMet(), "ReconcileStrandedPhase should preserve active runtime-less phases")
}

func TestSessionActivityPhaseStoreReconcileStrandedRuntimeLessPhaseAfterThreadStops(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, phaseID, threadID := uuid.New(), uuid.New(), uuid.New()
	cutoff := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	completedAt := cutoff.Add(time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT thread_id, runtime_id").
		WithArgs(orgID, phaseID).
		WillReturnRows(pgxmock.NewRows([]string{"thread_id", "runtime_id"}).
			AddRow(threadID, nil))
	mock.ExpectQuery("SELECT status FROM session_threads").
		WithArgs(orgID, threadID).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow(models.ThreadStatusIdle))
	mock.ExpectQuery("SELECT thread_id, runtime_id, started_at").
		WithArgs(orgID, phaseID).
		WillReturnRows(pgxmock.NewRows([]string{"thread_id", "runtime_id", "started_at"}).
			AddRow(threadID, nil, cutoff.Add(-time.Minute)))
	mock.ExpectExec("UPDATE session_activity_phases").
		WithArgs(orgID, phaseID, completedAt).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	reconciled, err := NewSessionActivityPhaseStore(mock).ReconcileStrandedPhase(
		context.Background(), orgID, phaseID, cutoff, completedAt,
	)
	require.NoError(t, err, "ReconcileStrandedPhase should close an old runtime-less phase after its thread stops")
	require.True(t, reconciled, "ReconcileStrandedPhase should report the durable terminal transition")
	require.NoError(t, mock.ExpectationsWereMet(), "runtime-less reconciliation should lock both phase and thread before transition")
}

func TestSessionActivityPhaseStoreReconcileStrandedPhasePreservesRenewedRuntime(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, phaseID, threadID, runtimeID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	cutoff := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	completedAt := cutoff.Add(time.Minute)
	renewedUntil := cutoff.Add(time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT thread_id, runtime_id").
		WithArgs(orgID, phaseID).
		WillReturnRows(pgxmock.NewRows([]string{"thread_id", "runtime_id"}).
			AddRow(threadID, runtimeID))
	mock.ExpectQuery("SELECT status, lease_expires_at FROM thread_runtimes").
		WithArgs(orgID, runtimeID).
		WillReturnRows(pgxmock.NewRows([]string{"status", "lease_expires_at"}).
			AddRow(models.ThreadRuntimeStatusLive, renewedUntil))
	mock.ExpectQuery("SELECT thread_id, runtime_id, started_at").
		WithArgs(orgID, phaseID).
		WillReturnRows(pgxmock.NewRows([]string{"thread_id", "runtime_id", "started_at"}).
			AddRow(threadID, runtimeID, cutoff.Add(-time.Minute)))
	mock.ExpectCommit()

	reconciled, err := NewSessionActivityPhaseStore(mock).ReconcileStrandedPhase(
		context.Background(), orgID, phaseID, cutoff, completedAt,
	)
	require.NoError(t, err, "ReconcileStrandedPhase should recheck the locked runtime lease")
	require.False(t, reconciled, "ReconcileStrandedPhase should preserve a phase whose runtime recovered")
	require.NoError(t, mock.ExpectationsWereMet(), "runtime-backed reconciliation should lock the runtime against heartbeat races")
}

func TestSessionActivityPhaseStoreReconcileAbandonedInboxBatchesAcrossOrgs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "activity phase store test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, sessionID, threadID, runtimeID, batchID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	acknowledgedAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	cutoff := acknowledgedAt.Add(time.Minute)
	abandonedAt := cutoff.Add(time.Minute)
	expected := models.ThreadInboxDeliveryBatch{
		ID: batchID, OrgID: orgID, SessionID: sessionID, ThreadID: threadID, RuntimeID: runtimeID,
		SequenceStart: 4, SequenceEnd: 5, Status: models.InboxDeliveryBatchAbandoned,
		AcknowledgedAt: acknowledgedAt, AbandonedAt: &abandonedAt, CreatedAt: acknowledgedAt, UpdatedAt: abandonedAt,
	}
	mock.ExpectQuery(`(?s)WITH candidates AS.+b\.status = 'acknowledged'.+r\.status IN \('lost', 'closed', 'failed'\).+FOR UPDATE OF b, r SKIP LOCKED.+UPDATE thread_inbox_delivery_batches`).
		WithArgs(cutoff, 100, abandonedAt).
		WillReturnRows(pgxmock.NewRows(inboxBatchTestColumns).AddRow(
			batchID, orgID, sessionID, threadID, runtimeID, int64(4), int64(5),
			models.InboxDeliveryBatchAbandoned, acknowledgedAt, nil, &abandonedAt, acknowledgedAt, abandonedAt,
		))

	batches, err := NewSessionActivityPhaseStore(mock).ReconcileAbandonedInboxBatchesAcrossOrgs(
		context.Background(), cutoff, abandonedAt, 100,
	)
	require.NoError(t, err, "batch reconciliation should abandon acknowledged work whose runtime cannot resume")
	require.Equal(t, []models.ThreadInboxDeliveryBatch{expected}, batches, "batch reconciliation should return the exact durable abandoned batches")
	require.NoError(t, mock.ExpectationsWereMet(), "batch reconciliation should be bounded and lock candidates against recovery races")
}

func TestValidateTerminalActivityPhase(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name    string
		status  models.ActivityPhaseStatus
		reason  models.ActivityPhaseBoundaryReason
		at      time.Time
		wantErr bool
	}{
		{name: "completed final response", status: models.ActivityPhaseStatusCompleted, reason: models.ActivityPhaseBoundaryFinalResponse, at: now},
		{name: "completed human input", status: models.ActivityPhaseStatusCompleted, reason: models.ActivityPhaseBoundaryHumanInput, at: now},
		{name: "completed approval", status: models.ActivityPhaseStatusCompleted, reason: models.ActivityPhaseBoundaryApproval, at: now},
		{name: "completed plan approval", status: models.ActivityPhaseStatusCompleted, reason: models.ActivityPhaseBoundaryPlanApproval, at: now},
		{name: "completed steering", status: models.ActivityPhaseStatusCompleted, reason: models.ActivityPhaseBoundarySteered, at: now},
		{name: "failed error", status: models.ActivityPhaseStatusFailed, reason: models.ActivityPhaseBoundaryError, at: now},
		{name: "cancelled stop", status: models.ActivityPhaseStatusCancelled, reason: models.ActivityPhaseBoundaryStopped, at: now},
		{name: "cancelled cancellation", status: models.ActivityPhaseStatusCancelled, reason: models.ActivityPhaseBoundaryCancelled, at: now},
		{name: "interrupted maintenance", status: models.ActivityPhaseStatusInterrupted, reason: models.ActivityPhaseBoundaryMaintenance, at: now},
		{name: "interrupted runtime loss", status: models.ActivityPhaseStatusInterrupted, reason: models.ActivityPhaseBoundaryRuntimeLost, at: now},
		{name: "interrupted capacity", status: models.ActivityPhaseStatusInterrupted, reason: models.ActivityPhaseBoundaryCapacitySuspended, at: now},
		{name: "interrupted explicit", status: models.ActivityPhaseStatusInterrupted, reason: models.ActivityPhaseBoundaryInterrupted, at: now},
		{name: "running cannot complete", status: models.ActivityPhaseStatusRunning, reason: models.ActivityPhaseBoundaryFinalResponse, at: now, wantErr: true},
		{name: "mismatched status reason", status: models.ActivityPhaseStatusFailed, reason: models.ActivityPhaseBoundaryCancelled, at: now, wantErr: true},
		{name: "missing completion time", status: models.ActivityPhaseStatusCompleted, reason: models.ActivityPhaseBoundaryFinalResponse, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateTerminalActivityPhase(tt.status, tt.reason, tt.at)
			if tt.wantErr {
				require.Error(t, err, "invalid terminal lifecycle transition should be rejected")
				return
			}
			require.NoError(t, err, "valid terminal lifecycle transition should be accepted")
		})
	}
}
