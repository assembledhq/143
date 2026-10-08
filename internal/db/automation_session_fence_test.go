package db

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestAutomationRunStore_ClaimPendingForPerRunInTxFencesSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                string
		status                              models.AutomationRunStatus
		noAttempts, superseded, lookupError bool
		expected                            bool
	}{
		{name: "first dispatch", status: models.AutomationRunStatusPending, noAttempts: true, expected: true},
		{name: "current predecessor", status: models.AutomationRunStatusPending, expected: true},
		{name: "selection used older attempt snapshot", status: models.AutomationRunStatusPending, superseded: true},
		{name: "another worker already running", status: models.AutomationRunStatusRunning},
		{name: "ownership lookup fails closed", status: models.AutomationRunStatusPending, lookupError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated mock pool")
			defer mock.Close()
			orgID, runID, previousID := uuid.New(), uuid.New(), uuid.New()
			if tt.noAttempts {
				previousID = uuid.Nil
			}
			mock.ExpectBegin()
			tx, err := mock.Begin(context.Background())
			require.NoError(t, err, "begin dispatch transaction")
			mock.ExpectQuery(`SELECT status FROM automation_runs WHERE id = @id AND org_id = @org_id FOR UPDATE`).WithArgs(runID, orgID).WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow(tt.status))
			if tt.status == models.AutomationRunStatusPending {
				expectation := mock.ExpectQuery(`SELECT sessions.id[\s\S]+WHERE sal.automation_run_id = @run_id AND sal.org_id = @org_id[\s\S]+ORDER BY sessions.created_at DESC, sessions.id DESC LIMIT 1`).WithArgs(runID, orgID)
				if tt.lookupError {
					expectation.WillReturnError(errors.New("read failed"))
				} else {
					rows := pgxmock.NewRows([]string{"id"})
					if !tt.noAttempts {
						currentID := previousID
						if tt.superseded {
							currentID = uuid.New()
						}
						rows.AddRow(currentID)
					}
					expectation.WillReturnRows(rows)
				}
			}
			if tt.expected {
				mock.ExpectExec(`UPDATE automation_runs[\s\S]+WHERE id = @id AND org_id = @org_id[\s\S]+AND status = 'pending'`).WithArgs(runID, orgID).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			got, err := NewAutomationRunStore(mock).ClaimPendingForPerRunInTx(context.Background(), tx, orgID, runID, previousID)
			if tt.lookupError {
				require.Error(t, err, "ownership lookup failures must prevent dispatch")
			} else {
				require.NoError(t, err, "claim should check the committed predecessor under its run lock")
			}
			require.Equal(t, tt.expected, got, "only a selection based on the current predecessor may claim the run")
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback(context.Background()), "claim remains rollbackable until the session and link are created")
			require.NoError(t, mock.ExpectationsWereMet(), "tenant-scoped row lock and fresh ownership read must precede the claim")
		})
	}
}
