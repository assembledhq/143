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

var projectCycleTestColumns = []string{
	"id", "project_id", "org_id", "pm_plan_id", "cycle_number", "analysis", "decisions",
	"progress_pct", "tasks_completed_this_cycle", "tasks_failed_this_cycle",
	"tasks_created_this_cycle", "created_at",
}

func newProjectCycleRow(cycleID, projectID, orgID uuid.UUID, now time.Time) []interface{} {
	progressPct := 50
	return []interface{}{
		cycleID, projectID, orgID, nil, 1, "Cycle analysis", json.RawMessage(`["decision1"]`),
		&progressPct, 2, 1,
		3, now,
	}
}

func TestProjectCycleStore_Create(t *testing.T) {
	t.Parallel()

	cycleID := uuid.New()
	projectID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	// Create has 10 named args
	mock.ExpectQuery("INSERT INTO project_cycles").
		WithArgs(anyArgs(10)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(cycleID, now))

	store := NewProjectCycleStore(mock)
	cycle := &models.ProjectCycle{
		ProjectID:               projectID,
		OrgID:                   orgID,
		CycleNumber:             1,
		Analysis:                "Cycle analysis",
		Decisions:               json.RawMessage(`["decision1"]`),
		TasksCompletedThisCycle: 2,
		TasksFailedThisCycle:    1,
		TasksCreatedThisCycle:   3,
	}

	err = store.Create(context.Background(), cycle)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, cycleID, cycle.ID, "Create should set the cycle ID")
	require.WithinDuration(t, now, cycle.CreatedAt, time.Second, "Create should set created_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectCycleStore_ListByProject(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		limit     int
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name:  "returns cycles for project",
			limit: 20,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_cycles WHERE project_id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(projectCycleTestColumns).
							AddRow(newProjectCycleRow(uuid.New(), projectID, orgID, now)...).
							AddRow(newProjectCycleRow(uuid.New(), projectID, orgID, now)...),
					)
			},
			expected: 2,
		},
		{
			name:  "returns empty when no cycles exist",
			limit: 20,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_cycles WHERE project_id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(projectCycleTestColumns))
			},
			expected: 0,
		},
		{
			name:  "returns error on database failure",
			limit: 20,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_cycles WHERE project_id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
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

			store := NewProjectCycleStore(mock)
			tt.setupMock(mock)

			cycles, err := store.ListByProject(context.Background(), orgID, projectID, tt.limit)
			if tt.expectErr {
				require.Error(t, err, "ListByProject should return an error")
				return
			}
			require.NoError(t, err, "ListByProject should not return an error")
			require.Len(t, cycles, tt.expected, "should return expected number of cycles")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestProjectCycleStore_GetByID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	cycleID := uuid.New()
	projectID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns cycle when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_cycles WHERE id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(projectCycleTestColumns).AddRow(newProjectCycleRow(cycleID, projectID, orgID, now)...))
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_cycles WHERE id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(projectCycleTestColumns))
			},
			expectErr: true,
		},
		{
			name: "returns error on db failure",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_cycles WHERE id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
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

			store := NewProjectCycleStore(mock)
			tt.setupMock(mock)

			cycle, err := store.GetByID(context.Background(), orgID, cycleID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, cycleID, cycle.ID, "should return the correct cycle ID")
			require.Equal(t, orgID, cycle.OrgID, "should return the correct org ID")
			require.Equal(t, "Cycle analysis", cycle.Analysis, "should return the correct analysis")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
