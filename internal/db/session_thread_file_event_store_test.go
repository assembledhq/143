package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestSessionThreadFileEventStore_AppendBatchEmptyIsNoop(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionThreadFileEventStore(mock)
	require.NoError(t, store.AppendBatch(context.Background(), uuid.New(), nil))
	require.NoError(t, mock.ExpectationsWereMet(), "no SQL should run for an empty batch")
}

func TestSessionThreadFileEventStore_AppendBatchSplitsAtChunkBoundary(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionThreadFileEventStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	// Build appendBatchChunkSize+1 events. The store must split into two
	// statements so neither approaches Postgres's 65535-parameter cap. The
	// concrete numbers also pin the chunk size constant — a regression that
	// drops chunking would issue one statement and fail the second expectation.
	total := appendBatchChunkSize + 1
	events := make([]models.SessionThreadFileEvent, total)
	for i := range events {
		events[i] = models.SessionThreadFileEvent{
			OrgID:     orgID,
			SessionID: sessionID,
			ThreadID:  &threadID,
			Turn:      1,
			Path:      fmt.Sprintf("file-%d.go", i),
			EventType: models.FileEventTypeModified,
		}
	}

	const colsPerRow = 8
	mock.ExpectExec("INSERT INTO session_thread_file_events").
		WithArgs(anyArgs(appendBatchChunkSize * colsPerRow)...).
		WillReturnResult(pgxmock.NewResult("INSERT", appendBatchChunkSize))
	mock.ExpectExec("INSERT INTO session_thread_file_events").
		WithArgs(anyArgs(1 * colsPerRow)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	require.NoError(t, store.AppendBatch(context.Background(), orgID, events))
	require.NoError(t, mock.ExpectationsWereMet(), "AppendBatch should split at the chunk boundary")
}

func TestSessionThreadFileEventStore_AppendBatchRejectsCrossOrgRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionThreadFileEventStore(mock)
	orgA := uuid.New()
	orgB := uuid.New()

	events := []models.SessionThreadFileEvent{
		{OrgID: orgA, Path: "a.go", EventType: models.FileEventTypeModified},
		{OrgID: orgB, Path: "b.go", EventType: models.FileEventTypeModified},
	}
	err = store.AppendBatch(context.Background(), orgA, events)
	require.Error(t, err, "mismatched orgs in one batch should be rejected")
	require.NoError(t, mock.ExpectationsWereMet(), "no SQL should run when org check fails")
}

func TestSessionThreadFileEventStore_AppendBatchDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionThreadFileEventStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	// Caller passes events with OrgID = uuid.Nil; the store fills it from the
	// argument when persisting but must not write back into the caller's slice.
	events := []models.SessionThreadFileEvent{
		{OrgID: uuid.Nil, SessionID: sessionID, ThreadID: &threadID, Turn: 1, Path: "x.go", EventType: models.FileEventTypeModified},
	}

	mock.ExpectExec("INSERT INTO session_thread_file_events").
		WithArgs(anyArgs(8)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	require.NoError(t, store.AppendBatch(context.Background(), orgID, events))
	require.Equal(t, uuid.Nil, events[0].OrgID, "AppendBatch must not mutate the caller's slice")
}
