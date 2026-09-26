package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionStore_TerminalCleanupProjection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		withAfter bool
		empty     bool
		scanError bool
	}{
		{name: "first page returns only identifiers"},
		{name: "later page includes timestamp and ID keyset", withAfter: true},
		{name: "end of sweep is empty", withAfter: true, empty: true},
		{name: "row errors are propagated", scanError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "create isolated cleanup query mock")
			defer mock.Close()
			before := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
			completed := before.Add(-time.Hour)
			id := uuid.MustParse("00000000-0000-0000-0000-000000000002")
			var after *models.SessionStreamCleanupCursor
			query := `SELECT id, completed_at FROM sessions
				WHERE status IN ('completed', 'failed', 'cancelled', 'pr_created', 'skipped')
				AND completed_at IS NOT NULL AND completed_at < @before`
			args := []any{before}
			if tt.withAfter {
				after = &models.SessionStreamCleanupCursor{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), CompletedAt: completed}
				query += ` AND (completed_at, id) > (@after_completed_at, @after_id)`
				args = append(args, after.CompletedAt, after.ID)
			}
			query += ` ORDER BY completed_at ASC, id ASC LIMIT @limit`
			args = append(args, 500)
			rows := pgxmock.NewRows([]string{"id", "completed_at"})
			expected := []models.SessionStreamCleanupCursor{}
			if !tt.empty {
				if tt.scanError {
					rows.AddRow(id, completed).RowError(0, context.DeadlineExceeded)
				} else {
					rows.AddRow(id, completed)
					expected = []models.SessionStreamCleanupCursor{{ID: id, CompletedAt: completed}}
				}
			}
			mock.ExpectQuery(query).WithArgs(args...).WillReturnRows(rows)
			got, err := NewSessionStore(mock).ListTerminalEndedBefore(context.Background(), before, after, 500)
			if tt.scanError {
				require.Error(t, err, "invalid cleanup rows must fail instead of advancing the cursor")
			} else {
				require.NoError(t, err, "stream cleanup must query identifiers without loading session payloads")
				require.Equal(t, expected, got, "cleanup should return exactly the matching identifiers")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "cleanup must use the exact minimal ordered projection")
		})
	}
}
