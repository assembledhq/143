package db

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestJobStoreNonterminalLookupPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status models.JobStatus
		valid  bool
		zeroID bool
	}{
		{name: "pending", status: models.JobStatusPending, valid: true},
		{name: "running", status: models.JobStatusRunning, valid: true},
		{name: "parked without a lease", status: models.JobStatusAwaitingStartControl, valid: true},
		{name: "terminal cannot be returned as an active owner", status: models.JobStatusSucceeded},
		{name: "unsupported cannot be returned as an active owner", status: models.JobStatus("future")},
		{name: "a supported status cannot bless a zero consumer identity", status: models.JobStatusPending, zeroID: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "create an isolated exact-SQL policy mock")
			defer mock.Close()
			orgID, jobID := uuid.New(), uuid.New()
			if tt.zeroID {
				jobID = uuid.Nil
			}
			mock.ExpectQuery(`SELECT id, status FROM jobs WHERE org_id = @org_id AND queue = @queue AND dedupe_key = @dedupe_key AND status IN ('pending', 'running', 'awaiting_start_control')`).
				WithArgs(orgID, "agent", "synthetic-key").
				WillReturnRows(pgxmock.NewRows([]string{"id", "status"}).AddRow(jobID, tt.status))
			actual, err := NewJobStore(mock).GetActiveByDedupeKey(context.Background(), orgID, "agent", "synthetic-key")
			expected := ActiveJobRef{}
			if tt.valid {
				require.NoError(t, err, "exact nonterminal ownership includes parked jobs without broadening execution eligibility")
				expected = ActiveJobRef{ID: jobID, Status: tt.status}
			} else {
				require.Error(t, err, "invalid active observations must expose no partial consumer")
			}
			require.Equal(t, expected, actual, "lookup must return the exact scoped owner or zero on inconsistent state")
			require.NoError(t, mock.ExpectationsWereMet(), "the active lookup must keep its explicit tenant, queue and key predicates")
		})
	}
}
