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

var automationRunResultColumnNames = []string{
	"run_id", "org_id", "attempt", "attempt_lock_token", "thread_id", "turn_number", "outcome",
	"review_complete", "checkpoint_key", "checkpoint_published", "checkpoint_head_sha", "native_context",
	"dependency_fingerprint", "agent_session_id", "recorded_at",
}

func automationRunResultRow(r models.AutomationRunResult) []any {
	return []any{
		r.RunID, r.OrgID, r.Attempt, r.AttemptLockToken, r.ThreadID, r.TurnNumber, r.Outcome,
		r.ReviewComplete, r.CheckpointKey, r.CheckpointPublished, r.CheckpointHeadSHA, r.NativeContext,
		r.DependencyFingerprint, r.AgentSessionID, r.RecordedAt,
	}
}

func newTestAutomationRunResult(orgID uuid.UUID) models.AutomationRunResult {
	key := "snapshots/abc"
	head := "0123456789abcdef0123456789abcdef01234567"
	return models.AutomationRunResult{
		RunID:               uuid.New(),
		OrgID:               orgID,
		Attempt:             1,
		AttemptLockToken:    uuid.New(),
		ThreadID:            uuid.New(),
		TurnNumber:          3,
		Outcome:             models.AutomationRunResultTurnCompleted,
		ReviewComplete:      true,
		CheckpointKey:       &key,
		CheckpointPublished: true,
		CheckpointHeadSHA:   &head,
		NativeContext:       true,
		RecordedAt:          time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC),
	}
}

func TestAutomationRunResultStore_Write(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(r *models.AutomationRunResult)
		setupMock func(mock pgxmock.PgxPoolIface, r models.AutomationRunResult)
		wantOK    bool
		wantErr   bool
	}{
		{
			name:   "fenced insert returns the stored marker",
			mutate: func(*models.AutomationRunResult) {},
			setupMock: func(mock pgxmock.PgxPoolIface, r models.AutomationRunResult) {
				mock.ExpectQuery("INSERT INTO automation_run_results").
					WithArgs(anyArgs(15)...).
					WillReturnRows(pgxmock.NewRows(automationRunResultColumnNames).AddRow(automationRunResultRow(r)...))
			},
			wantOK: true,
		},
		{
			name:   "fence rejection returns false without error",
			mutate: func(*models.AutomationRunResult) {},
			setupMock: func(mock pgxmock.PgxPoolIface, _ models.AutomationRunResult) {
				mock.ExpectQuery("INSERT INTO automation_run_results").
					WithArgs(anyArgs(15)...).
					WillReturnRows(pgxmock.NewRows(automationRunResultColumnNames))
			},
		},
		{
			name:   "review_complete is forced false for a failed turn",
			mutate: func(r *models.AutomationRunResult) { r.Outcome = models.AutomationRunResultAgentFailed },
			setupMock: func(mock pgxmock.PgxPoolIface, r models.AutomationRunResult) {
				r.ReviewComplete = false
				mock.ExpectQuery("INSERT INTO automation_run_results").
					WithArgs(anyArgs(15)...).
					WillReturnRows(pgxmock.NewRows(automationRunResultColumnNames).AddRow(automationRunResultRow(r)...))
			},
			wantOK: true,
		},
		{
			name:      "zero attempt is rejected before any query",
			mutate:    func(r *models.AutomationRunResult) { r.Attempt = 0 },
			setupMock: func(pgxmock.PgxPoolIface, models.AutomationRunResult) {},
			wantErr:   true,
		},
		{
			name:      "missing lock token is rejected before any query",
			mutate:    func(r *models.AutomationRunResult) { r.AttemptLockToken = uuid.Nil },
			setupMock: func(pgxmock.PgxPoolIface, models.AutomationRunResult) {},
			wantErr:   true,
		},
		{
			name:      "unknown outcome is rejected before any query",
			mutate:    func(r *models.AutomationRunResult) { r.Outcome = "superseded" },
			setupMock: func(pgxmock.PgxPoolIface, models.AutomationRunResult) {},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()

			orgID := uuid.New()
			result := newTestAutomationRunResult(orgID)
			tt.mutate(&result)
			tt.setupMock(mock, result)

			store := NewAutomationRunResultStore(mock)
			ok, err := store.Write(context.Background(), nil, orgID, uuid.New(), &result)
			if tt.wantErr {
				require.Error(t, err, "invalid marker should be rejected")
			} else {
				require.NoError(t, err, "write should not error")
				require.Equal(t, tt.wantOK, ok, "write should report whether the fence accepted it")
				if tt.wantOK && result.Outcome != models.AutomationRunResultTurnCompleted {
					require.False(t, result.ReviewComplete, "review_complete should be false unless the turn completed")
				}
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAutomationRunResultStore_GetByRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, r models.AutomationRunResult)
		wantErr   error
	}{
		{
			name: "returns the marker",
			setupMock: func(mock pgxmock.PgxPoolIface, r models.AutomationRunResult) {
				mock.ExpectQuery("SELECT .+ FROM automation_run_results").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(automationRunResultColumnNames).AddRow(automationRunResultRow(r)...))
			},
		},
		{
			name: "missing marker maps to the sentinel",
			setupMock: func(mock pgxmock.PgxPoolIface, _ models.AutomationRunResult) {
				mock.ExpectQuery("SELECT .+ FROM automation_run_results").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(automationRunResultColumnNames))
			},
			wantErr: ErrAutomationRunResultNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()

			result := newTestAutomationRunResult(uuid.New())
			tt.setupMock(mock, result)

			store := NewAutomationRunResultStore(mock)
			got, err := store.GetByRun(context.Background(), result.OrgID, result.RunID)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "missing marker should map to the sentinel error")
			} else {
				require.NoError(t, err, "lookup should succeed")
				require.Equal(t, result, got, "marker should round-trip through the scanner")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
