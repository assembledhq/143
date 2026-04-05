package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var pmPlanColumns = []string{
	"id", "org_id", "repository_id", "status", "analysis", "tasks", "clusters", "skipped_issues",
	"issues_reviewed", "in_flight_runs_checked", "past_outcomes_reviewed",
	"recent_prs_checked", "past_decisions_reviewed", "commits_analyzed",
	"product_context_snapshot", "token_usage", "triggered_by",
	"created_at", "completed_at",
}

func newPMPlanRow(planID, orgID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		planID,
		orgID,
		nil, // repository_id
		"executing",
		"analysis text",
		json.RawMessage(`[]`),
		json.RawMessage(`[]`),
		json.RawMessage(`[]`),
		5,
		3,  // in_flight_runs_checked
		8,  // past_outcomes_reviewed
		1,  // recent_prs_checked
		12, // past_decisions_reviewed
		20, // commits_analyzed
		json.RawMessage(`{"direction":"focus"}`),
		json.RawMessage(`{"input_tokens":100}`),
		"cron",
		now,
		nil,
	}
}

func TestPMPlanStore_Create(t *testing.T) {
	t.Parallel()

	planID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO pm_plans").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(planID, now))

	store := NewPMPlanStore(mock)
	plan := &models.PMPlan{
		OrgID:                  orgID,
		Status:                 models.PMPlanStatusExecuting,
		Analysis:               "analysis text",
		Tasks:                  json.RawMessage(`[]`),
		Clusters:               json.RawMessage(`[]`),
		SkippedIssues:          json.RawMessage(`[]`),
		IssuesReviewed:         5,
		ProductContextSnapshot: json.RawMessage(`{"direction":"focus"}`),
		TokenUsage:             json.RawMessage(`{"input_tokens":100}`),
		TriggeredBy:            models.PMTriggerCron,
	}

	err = store.Create(context.Background(), plan)
	require.NoError(t, err, "Create should succeed")
	require.Equal(t, planID, plan.ID, "Create should set plan ID")
	require.WithinDuration(t, now, plan.CreatedAt, time.Second, "Create should set created_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPMPlanStore_GetByID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	planID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns plan",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(pmPlanColumns).AddRow(newPMPlanRow(planID, orgID, now)...))
			},
		},
		{
			name: "returns error on failure",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("boom"))
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

			store := NewPMPlanStore(mock)
			tt.setupMock(mock)

			_, err = store.GetByID(context.Background(), orgID, planID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error")
				return
			}
			require.NoError(t, err, "GetByID should succeed")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPMPlanStore_ListByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now()
	cursorPlan := models.PMPlan{ID: uuid.New(), CreatedAt: now.Add(-time.Hour)}
	cursor := FormatPMPlanCursor(cursorPlan)

	tests := []struct {
		name      string
		limit     int
		cursor    string
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
	}{
		{
			name:  "returns plans",
			limit: 2,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(pmPlanColumns).
							AddRow(newPMPlanRow(uuid.New(), orgID, now)...).
							AddRow(newPMPlanRow(uuid.New(), orgID, now)...),
					)
			},
			expected: 2,
		},
		{
			name:  "returns empty",
			limit: 2,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM pm_plans WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(pmPlanColumns))
			},
			expected: 0,
		},
		{
			name:   "applies cursor filtering",
			limit:  2,
			cursor: cursor,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("created_at <").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(pmPlanColumns).
							AddRow(newPMPlanRow(uuid.New(), orgID, now)...),
					)
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewPMPlanStore(mock)
			tt.setupMock(mock)

			plans, err := store.ListByOrg(context.Background(), orgID, PMPlanFilters{Limit: tt.limit, Cursor: tt.cursor})
			require.NoError(t, err, "ListByOrg should succeed")
			require.Len(t, plans, tt.expected, "should return expected number of plans")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPMPlanStore_Update(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	planID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectExec("UPDATE pm_plans").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewPMPlanStore(mock)
	plan := &models.PMPlan{
		ID:             planID,
		OrgID:          orgID,
		Status:         "completed",
		Analysis:       "updated analysis",
		Tasks:          json.RawMessage(`[]`),
		Clusters:       json.RawMessage(`[]`),
		SkippedIssues:  json.RawMessage(`[]`),
		IssuesReviewed: 3,
		TokenUsage:     json.RawMessage(`{"output_tokens":50}`),
		TriggeredBy:    "manual",
	}

	err = store.Update(context.Background(), plan)
	require.NoError(t, err, "Update should succeed")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
