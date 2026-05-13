package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

var threadInboxTestColumns = []string{
	"id", "org_id", "session_id", "thread_id", "sequence_no", "message_id",
	"entry_type", "delivery_state", "accepted_at", "delivered_at", "acked_at",
	"owner_node_id", "delivery_attempts", "last_error",
}

func TestThreadInboxStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewThreadInboxStore(mock)
	now := time.Now().UTC()
	entryID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()

	mock.ExpectQuery(`INSERT INTO thread_inbox_entries`).
		WithArgs(anyArgs(5)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "sequence_no", "delivery_state", "accepted_at"}).
				AddRow(entryID, int64(7), string(models.ThreadInboxDeliveryStatePending), now),
		)

	entry := &models.ThreadInboxEntry{
		OrgID:     orgID,
		SessionID: sessionID,
		ThreadID:  threadID,
		MessageID: 42,
		EntryType: models.ThreadInboxEntryTypeUserMessage,
	}
	err = store.Create(context.Background(), entry)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, entryID, entry.ID, "Create should populate the inbox entry ID")
	require.Equal(t, int64(7), entry.SequenceNo, "Create should assign the per-thread sequence number")
	require.Equal(t, models.ThreadInboxDeliveryStatePending, entry.DeliveryState, "Create should default new entries to pending delivery")
	require.Equal(t, now, entry.AcceptedAt, "Create should populate accepted_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadInboxStore_ListPendingAfter(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	threadID := uuid.New()
	sessionID := uuid.New()
	entryID := uuid.New()
	now := time.Now().UTC()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns pending and delivered entries after the cursor",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`SELECT .+ FROM thread_inbox_entries`).
					WithArgs(anyArgs(4)...).
					WillReturnRows(
						pgxmock.NewRows(threadInboxTestColumns).
							AddRow(
								entryID, orgID, sessionID, threadID, int64(8), int64(44),
								string(models.ThreadInboxEntryTypeUserMessage), string(models.ThreadInboxDeliveryStatePending),
								now, nil, nil, nil, 0, nil,
							),
					)
			},
		},
		{
			name: "wraps query errors",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(`SELECT .+ FROM thread_inbox_entries`).
					WithArgs(anyArgs(4)...).
					WillReturnError(fmt.Errorf("db unavailable"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewThreadInboxStore(mock)
			tt.setupMock(mock)

			entries, err := store.ListPendingAfter(context.Background(), orgID, threadID, 7, 50)
			if tt.expectErr {
				require.Error(t, err, "ListPendingAfter should return an error")
				return
			}
			require.NoError(t, err, "ListPendingAfter should not return an error")
			require.Equal(t, []models.ThreadInboxEntry{
				{
					ID:            entryID,
					OrgID:         orgID,
					SessionID:     sessionID,
					ThreadID:      threadID,
					SequenceNo:    8,
					MessageID:     44,
					EntryType:     models.ThreadInboxEntryTypeUserMessage,
					DeliveryState: models.ThreadInboxDeliveryStatePending,
					AcceptedAt:    now,
				},
			}, entries, "ListPendingAfter should return the expected inbox entries")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestThreadInboxStore_MarkDeliveredAndAcked(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewThreadInboxStore(mock)
	orgID := uuid.New()
	threadID := uuid.New()

	mock.ExpectExec(`UPDATE thread_inbox_entries\s+SET delivery_state = 'delivered'`).
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE thread_inbox_entries\s+SET delivery_state = 'acked'`).
		WithArgs(anyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	err = store.MarkDelivered(context.Background(), orgID, threadID, 9, "worker-a")
	require.NoError(t, err, "MarkDelivered should not return an error")

	err = store.MarkAckedThrough(context.Background(), orgID, threadID, 9, "worker-a")
	require.NoError(t, err, "MarkAckedThrough should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
