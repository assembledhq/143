package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func retainedThreadCreationRows(row *models.SessionThread) *pgxmock.Rows {
	rows := pgxmock.NewRows(sessionThreadTestColumns)
	if row == nil {
		return rows
	}
	values := newSessionThreadRow(row.ID, row.SessionID, row.OrgID, row.Label, row.CreatedAt)
	for i, column := range sessionThreadTestColumns {
		switch column {
		case "agent_type":
			values[i] = row.AgentType
		case "model_override":
			values[i] = row.ModelOverride
		case "status":
			values[i] = row.Status
		case "current_turn":
			values[i] = row.CurrentTurn
		case "created_by_source":
			values[i] = row.CreatedBySource
		case "archived_at":
			values[i] = row.ArchivedAt
		}
	}
	return rows.AddRow(values...)
}

func TestSessionThreadStoreCreateWithID(t *testing.T) {
	t.Parallel()
	storageErr := errors.New("synthetic creation acknowledgement unavailable")
	tests := []struct {
		name                                   string
		existing, conflict, capacity, inserted bool
		storageErr, expectedErr                error
	}{
		{name: "existing progress before capacity", existing: true},
		{name: "existing different identity", existing: true, conflict: true, expectedErr: ErrThreadCreationIdentityConflict},
		{name: "fresh exact identity", capacity: true, inserted: true},
		{name: "concurrent same identity", capacity: true},
		{name: "concurrent identity after capacity filled", capacity: false},
		{name: "invisible collision", capacity: true, conflict: true, expectedErr: ErrThreadCreationIdentityConflict},
		{name: "fresh capacity refusal", conflict: true, expectedErr: ErrThreadLimitReached},
		{name: "uncertain insertion returns original input unchanged", capacity: true, storageErr: storageErr, expectedErr: storageErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "each retained creation case needs isolated database expectations")
			defer mock.Close()
			original := models.SessionThread{ID: uuid.New(), OrgID: uuid.New(), SessionID: uuid.New(), AgentType: models.AgentTypeClaudeCode,
				Label: "retained startup", Status: models.ThreadStatusIdle}
			stored := original
			stored.Status, stored.CurrentTurn = models.ThreadStatusRunning, 7
			stored.CreatedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			stored.CreatedBySource = models.ThreadCreatedBySourceUser
			stored.ExecutionMode, stored.FilesystemMode = models.ThreadExecutionModeWork, models.ThreadFilesystemModeReadWrite
			stored.RecoveryEventHistory = []byte("[]")
			read := func(row *models.SessionThread) {
				mock.ExpectQuery(`(?s)^SELECT .* FROM session_threads WHERE org_id=\$1 AND id=\$2$`).
					WithArgs(original.OrgID, original.ID).WillReturnRows(retainedThreadCreationRows(row))
			}
			expected := stored
			if tt.existing {
				if tt.conflict {
					stored.SessionID = uuid.New()
				}
				read(&stored)
			} else {
				read(nil)
				query := mock.ExpectQuery(`(?s)^WITH capacity AS MATERIALIZED .*INSERT INTO session_threads.*ON CONFLICT\(id\) DO NOTHING RETURNING id,created_at.*`).
					WithArgs(original.OrgID, original.SessionID, original.ID, original.AgentType, original.ModelOverride, original.ReasoningEffort,
						original.Label, original.Instructions, original.FileScope, original.Status, models.ThreadCreatedBySourceUser, original.CreatedByThreadID,
						models.ThreadExecutionModeWork, 2, models.ThreadFilesystemModeReadWrite)
				if tt.storageErr != nil {
					query.WillReturnError(tt.storageErr)
				} else if tt.inserted {
					query.WillReturnRows(pgxmock.NewRows([]string{"available", "id", "created_at"}).AddRow(true, &original.ID, &stored.CreatedAt))
					expected = original
					expected.CreatedAt, expected.CreatedBySource = stored.CreatedAt, stored.CreatedBySource
					expected.ExecutionMode, expected.FilesystemMode = stored.ExecutionMode, stored.FilesystemMode
				} else {
					query.WillReturnRows(pgxmock.NewRows([]string{"available", "id", "created_at"}).AddRow(tt.capacity, nil, nil))
					if tt.conflict {
						read(nil)
					} else {
						read(&stored)
					}
				}
			}
			actual := original
			err = NewSessionThreadStore(mock).CreateWithID(context.Background(), original.OrgID, &actual, 2)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr, "failed or unacknowledged creation cannot imply successful adoption")
				require.Equal(t, original, actual, "every error must preserve the original preminted input without provisional defaults or timestamps")
			} else {
				require.NoError(t, err, "exact retained creation or same-ID adoption must succeed without a replacement identity")
				require.Equal(t, expected, actual, "same-ID adoption must return the complete existing state and never reset its turn")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "retained creation must preserve tenant predicates, use a later adoption read and own no transaction or effects")
		})
	}
}
