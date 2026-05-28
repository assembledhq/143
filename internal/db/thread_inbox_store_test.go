package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var threadInboxEntryTestColumns = []string{
	"id", "org_id", "session_id", "thread_id", "sequence_no", "message_id", "client_message_id",
	"entry_type", "payload", "delivery_state", "delivery_attempts",
	"last_error", "owner_node_id", "runtime_id", "accepted_at",
	"delivered_at", "acked_at", "created_at", "updated_at",
}

func TestThreadInboxStore_AppendForMessage(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()
	now := time.Now().UTC()
	payload := json.RawMessage(`{"message_id":42}`)

	mock.ExpectQuery("WITH locked_thread AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(threadInboxEntryTestColumns).AddRow(
			entryID, orgID, sessionID, threadID, int64(3), int64(42), "message:42",
			models.ThreadInboxEntryTypeUserMessage, payload,
			models.ThreadInboxDeliveryStatePending, 0,
			nil, nil, nil, now, nil, nil, now, now,
		))

	store := NewThreadInboxStore(mock)
	entry, err := store.AppendForMessage(context.Background(), orgID, AppendThreadInboxEntryParams{
		SessionID: sessionID,
		ThreadID:  threadID,
		MessageID: 42,
		EntryType: models.ThreadInboxEntryTypeUserMessage,
		Payload:   payload,
	})

	require.NoError(t, err, "AppendForMessage should not return an error")
	require.Equal(t, entryID, entry.ID, "AppendForMessage should return the inserted inbox entry")
	require.Equal(t, int64(3), entry.SequenceNo, "AppendForMessage should return the assigned sequence")
	require.Equal(t, models.ThreadInboxDeliveryStatePending, entry.DeliveryState, "AppendForMessage should create pending entries")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_MarkDeliveredForEntry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	threadID := uuid.New()
	runtimeID := uuid.New()
	ownerNodeID := "worker-1"

	entryID := uuid.New()

	mock.ExpectExec("UPDATE thread_inbox_entries").
		WithArgs(orgID, threadID, runtimeID, ownerNodeID, entryID, int64(4)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewThreadInboxStore(mock)
	updated, err := store.MarkDeliveredForEntry(context.Background(), orgID, threadID, runtimeID, ownerNodeID, entryID, 4)

	require.NoError(t, err, "MarkDeliveredForEntry should not return an error")
	require.Equal(t, int64(1), updated, "MarkDeliveredForEntry should return updated row count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_ListDeliverableAfter(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()
	now := time.Now().UTC()
	payload := json.RawMessage(`{"content":"continue"}`)

	mock.ExpectQuery("SELECT .* FROM thread_inbox_entries").
		WithArgs(orgID, threadID, int64(2), 25).
		WillReturnRows(pgxmock.NewRows(threadInboxEntryTestColumns).AddRow(
			entryID, orgID, sessionID, threadID, int64(3), int64(42), nil,
			models.ThreadInboxEntryTypeUserMessage, payload,
			models.ThreadInboxDeliveryStatePending, 0,
			nil, nil, nil, now, nil, nil, now, now,
		))

	store := NewThreadInboxStore(mock)
	entries, err := store.ListDeliverableAfter(context.Background(), orgID, threadID, 2, 25)

	require.NoError(t, err, "ListDeliverableAfter should not return an error")
	require.Equal(t, []int64{3}, []int64{entries[0].SequenceNo}, "ListDeliverableAfter should return entries after the cursor in order")
	require.Equal(t, payload, entries[0].Payload, "ListDeliverableAfter should preserve payload bytes")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_ListDeliverableAfterIncludesRetriedPendingBeforeCursor(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()
	now := time.Now().UTC()
	payload := json.RawMessage(`{"content":"retry older input"}`)

	mock.ExpectQuery("sequence_no > \\$3\\s+OR delivery_state = 'pending'").
		WithArgs(orgID, threadID, int64(5), 25).
		WillReturnRows(pgxmock.NewRows(threadInboxEntryTestColumns).AddRow(
			entryID, orgID, sessionID, threadID, int64(3), int64(42), nil,
			models.ThreadInboxEntryTypeUserMessage, payload,
			models.ThreadInboxDeliveryStatePending, 0,
			nil, nil, nil, now, nil, nil, now, now,
		))

	store := NewThreadInboxStore(mock)
	entries, err := store.ListDeliverableAfter(context.Background(), orgID, threadID, 5, 25)

	require.NoError(t, err, "ListDeliverableAfter should not return an error")
	require.Equal(t, []int64{3}, []int64{entries[0].SequenceNo}, "ListDeliverableAfter should include explicitly retried pending entries before the runtime cursor")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_MarkDeadLetter(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()
	now := time.Now().UTC()
	reason := "invalid inbox payload"

	mock.ExpectQuery("UPDATE thread_inbox_entries").
		WithArgs(orgID, threadID, entryID, reason).
		WillReturnRows(pgxmock.NewRows(threadInboxEntryTestColumns).AddRow(
			entryID, orgID, sessionID, threadID, int64(4), int64(42), nil,
			models.ThreadInboxEntryTypeUserMessage, json.RawMessage(`{"message_id":42}`),
			models.ThreadInboxDeliveryStateDeadLetter, 1,
			reason, nil, nil, now, nil, nil, now, now,
		))

	store := NewThreadInboxStore(mock)
	entry, err := store.MarkDeadLetter(context.Background(), orgID, threadID, entryID, reason)

	require.NoError(t, err, "MarkDeadLetter should not return an error")
	require.Equal(t, models.ThreadInboxDeliveryStateDeadLetter, entry.DeliveryState, "MarkDeadLetter should mark the entry as dead-lettered")
	require.Equal(t, &reason, entry.LastError, "MarkDeadLetter should persist the recoverable reason")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_ListRecoverableByThread(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()
	now := time.Now().UTC()
	reason := "runtime lease expired after live delivery before ack"

	mock.ExpectQuery("SELECT .* FROM thread_inbox_entries").
		WithArgs(orgID, threadID, 10).
		WillReturnRows(pgxmock.NewRows(threadInboxEntryTestColumns).AddRow(
			entryID, orgID, sessionID, threadID, int64(7), int64(99), nil,
			models.ThreadInboxEntryTypeUserMessage, json.RawMessage(`{"content":"retry me"}`),
			models.ThreadInboxDeliveryStateUnknownDelivery, 2,
			reason, "worker-1", nil, now, now, nil, now, now,
		))

	store := NewThreadInboxStore(mock)
	entries, err := store.ListRecoverableByThread(context.Background(), orgID, threadID, 10)

	require.NoError(t, err, "ListRecoverableByThread should not return an error")
	require.Len(t, entries, 1, "ListRecoverableByThread should return recoverable entries")
	require.Equal(t, models.ThreadInboxDeliveryStateUnknownDelivery, entries[0].DeliveryState, "ListRecoverableByThread should include unknown-delivery entries")
	require.Equal(t, &reason, entries[0].LastError, "ListRecoverableByThread should preserve the recovery reason")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_MarkDeliveryFailed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()
	reason := "stdin write failed"

	mock.ExpectQuery("UPDATE thread_inbox_entries").
		WithArgs(orgID, threadID, runtimeID, entryID, reason, 5).
		WillReturnRows(pgxmock.NewRows(threadInboxEntryTestColumns).AddRow(
			entryID, orgID, sessionID, threadID, int64(7), int64(99), nil,
			models.ThreadInboxEntryTypeUserMessage, json.RawMessage(`{"content":"retry me"}`),
			models.ThreadInboxDeliveryStatePending, 1,
			reason, nil, nil, now, nil, nil, now, now,
		))

	store := NewThreadInboxStore(mock)
	entry, err := store.MarkDeliveryFailed(context.Background(), orgID, threadID, runtimeID, entryID, reason, 5)

	require.NoError(t, err, "MarkDeliveryFailed should not return an error")
	require.Equal(t, models.ThreadInboxDeliveryStatePending, entry.DeliveryState, "MarkDeliveryFailed should keep retryable failures pending")
	require.Equal(t, 1, entry.DeliveryAttempts, "MarkDeliveryFailed should increment attempts")
	require.Equal(t, &reason, entry.LastError, "MarkDeliveryFailed should persist the write error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_RetryRecoverable(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("UPDATE thread_inbox_entries").
		WithArgs(orgID, threadID, entryID, false).
		WillReturnRows(pgxmock.NewRows(threadInboxEntryTestColumns).AddRow(
			entryID, orgID, sessionID, threadID, int64(7), int64(99), nil,
			models.ThreadInboxEntryTypeUserMessage, json.RawMessage(`{"content":"retry me"}`),
			models.ThreadInboxDeliveryStatePending, 0,
			nil, nil, nil, now, nil, nil, now, now,
		))

	store := NewThreadInboxStore(mock)
	entry, err := store.RetryRecoverable(context.Background(), orgID, threadID, entryID, false)

	require.NoError(t, err, "RetryRecoverable should not return an error")
	require.Equal(t, models.ThreadInboxDeliveryStatePending, entry.DeliveryState, "RetryRecoverable should return the entry to pending delivery")
	require.Zero(t, entry.DeliveryAttempts, "RetryRecoverable should clear failed delivery attempts")
	require.Nil(t, entry.LastError, "RetryRecoverable should clear the recovery error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_RetryRecoverableRequiresExplicitUnknownReplay(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	threadID := uuid.New()
	entryID := uuid.New()

	mock.ExpectQuery("delivery_state = 'dead_letter'").
		WithArgs(orgID, threadID, entryID, false).
		WillReturnError(pgx.ErrNoRows)

	store := NewThreadInboxStore(mock)
	_, err = store.RetryRecoverable(context.Background(), orgID, threadID, entryID, false)

	require.ErrorIs(t, err, pgx.ErrNoRows, "RetryRecoverable should not replay unknown-delivery entries without explicit consent")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_MarkAckedForSeedMessages(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	threadID := uuid.New()
	runtimeID := uuid.New()

	mock.ExpectExec("UPDATE thread_inbox_entries").
		WithArgs(orgID, threadID, runtimeID, []int64{11, 12}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	store := NewThreadInboxStore(mock)
	updated, err := store.MarkAckedForSeedMessages(context.Background(), orgID, threadID, runtimeID, []int64{11, 12})

	require.NoError(t, err, "MarkAckedForSeedMessages should not return an error")
	require.Equal(t, int64(2), updated, "MarkAckedForSeedMessages should return updated row count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_CountPendingByThread(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	threadID := uuid.New()

	mock.ExpectQuery("SELECT count").
		WithArgs(orgID, threadID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(5))

	store := NewThreadInboxStore(mock)
	count, err := store.CountPendingByThread(context.Background(), orgID, threadID)

	require.NoError(t, err, "CountPendingByThread should not return an error")
	require.Equal(t, 5, count, "CountPendingByThread should return pending entries")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_ListDeliverySummariesBySession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	lastError := "runtime lease expired before ack"
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT .* FROM thread_inbox_entries").
		WithArgs(orgID, sessionID).
		WillReturnRows(pgxmock.NewRows(threadInboxSummaryTestColumns()).AddRow(
			threadID, 2, 1, 3, 1, 4, 1, int64(11), now, now.Add(time.Second), now.Add(2*time.Second), lastError,
		))

	store := NewThreadInboxStore(mock)
	summaries, err := store.ListDeliverySummariesBySession(context.Background(), orgID, sessionID)

	require.NoError(t, err, "ListDeliverySummariesBySession should not return an error")
	require.Equal(t, models.ThreadInboxSummaryStateDeadLetter, summaries[threadID].State, "dead-letter entries should dominate the summarized state")
	require.Equal(t, 2, summaries[threadID].PendingCount, "summary should include pending count")
	require.Equal(t, 1, summaries[threadID].DeliveringCount, "summary should include delivering count")
	require.Equal(t, 3, summaries[threadID].DeliveredCount, "summary should include delivered count")
	require.Equal(t, 1, summaries[threadID].UnknownDeliveryCount, "summary should include unknown-delivery count")
	require.Equal(t, 4, summaries[threadID].AckedCount, "summary should include acked count")
	require.Equal(t, 1, summaries[threadID].DeadLetterCount, "summary should include dead-letter count")
	require.Equal(t, int64(11), summaries[threadID].LastSequenceNo, "summary should include the latest sequence")
	require.Equal(t, &lastError, summaries[threadID].LastError, "summary should include the latest error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_GetDeliverySummaryByThread(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	threadID := uuid.New()

	mock.ExpectQuery("SELECT .* FROM thread_inbox_entries").
		WithArgs(orgID, threadID).
		WillReturnRows(pgxmock.NewRows(threadInboxSummaryTestColumns()).AddRow(
			threadID, 0, 0, 0, 0, 3, 0, int64(3), nil, nil, time.Now().UTC(), nil,
		))

	store := NewThreadInboxStore(mock)
	summary, err := store.GetDeliverySummaryByThread(context.Background(), orgID, threadID)

	require.NoError(t, err, "GetDeliverySummaryByThread should not return an error")
	require.Equal(t, threadID, summary.ThreadID, "summary should be scoped to the requested thread")
	require.Equal(t, models.ThreadInboxSummaryStateAcked, summary.State, "acked entries should summarize as acked")
	require.Equal(t, 3, summary.AckedCount, "summary should include acked count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func threadInboxSummaryTestColumns() []string {
	return []string{
		"thread_id", "pending_count", "delivering_count", "delivered_count",
		"unknown_delivery_count", "acked_count", "dead_letter_count", "last_sequence_no",
		"last_accepted_at", "last_delivered_at", "last_acked_at", "last_error",
	}
}
