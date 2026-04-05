package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var projectTaskTestColumns = []string{
	"id", "project_id", "org_id", "title", "description", "approach", "reasoning",
	"sort_order", "depends_on", "batch_number", "status", "complexity", "confidence",
	"session_id", "issue_id", "branch_name", "pr_url", "outcome_notes",
	"retry_count", "max_retries", "created_at", "updated_at", "completed_at",
}

func newProjectTaskRow(taskID, projectID, orgID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		taskID, projectID, orgID, "Task One", nil, nil, nil,
		1, []uuid.UUID{}, 1, "pending", nil, nil,
		nil, nil, nil, nil, nil,
		0, 3, now, now, nil,
	}
}

func TestProjectTaskStore_Create(t *testing.T) {
	t.Parallel()

	taskID := uuid.New()
	projectID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	// Create has 16 named args
	mock.ExpectQuery("INSERT INTO project_tasks").
		WithArgs(anyArgs(16)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(taskID, now, now))
	mock.ExpectExec("DELETE FROM project_task_dependencies").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	store := NewProjectTaskStore(mock)
	task := &models.ProjectTask{
		ProjectID:   projectID,
		OrgID:       orgID,
		Title:       "Task One",
		Status:      models.ProjectTaskStatusPending,
		SortOrder:   1,
		BatchNumber: 1,
		MaxRetries:  3,
	}

	err = store.Create(context.Background(), task)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, taskID, task.ID, "Create should set the task ID")
	require.WithinDuration(t, now, task.CreatedAt, time.Second, "Create should set created_at")
	require.WithinDuration(t, now, task.UpdatedAt, time.Second, "Create should set updated_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectTaskStore_GetByID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	taskID := uuid.New()
	projectID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns task when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(projectTaskTestColumns).AddRow(newProjectTaskRow(taskID, projectID, orgID, now)...))
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(projectTaskTestColumns))
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

			store := NewProjectTaskStore(mock)
			tt.setupMock(mock)

			task, err := store.GetByID(context.Background(), orgID, taskID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, taskID, task.ID, "should return the correct task ID")
			require.Equal(t, orgID, task.OrgID, "should return the correct org ID")
			require.Equal(t, "Task One", task.Title, "should return the correct task title")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestProjectTaskStore_ListByProject(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		filters   ProjectTaskFilters
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name:    "returns tasks for project",
			filters: ProjectTaskFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE project_id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(projectTaskTestColumns).
							AddRow(newProjectTaskRow(uuid.New(), projectID, orgID, now)...).
							AddRow(newProjectTaskRow(uuid.New(), projectID, orgID, now)...),
					)
			},
			expected: 2,
		},
		{
			name:    "returns filtered tasks by status",
			filters: ProjectTaskFilters{Status: "pending"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE project_id .+ AND org_id .+ AND status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(projectTaskTestColumns).
							AddRow(newProjectTaskRow(uuid.New(), projectID, orgID, now)...),
					)
			},
			expected: 1,
		},
		{
			name:    "returns empty when no tasks exist",
			filters: ProjectTaskFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE project_id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(projectTaskTestColumns))
			},
			expected: 0,
		},
		{
			name:    "returns error on database failure",
			filters: ProjectTaskFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE project_id .+ AND org_id").
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

			store := NewProjectTaskStore(mock)
			tt.setupMock(mock)

			tasks, err := store.ListByProject(context.Background(), orgID, projectID, tt.filters)
			if tt.expectErr {
				require.Error(t, err, "ListByProject should return an error")
				return
			}
			require.NoError(t, err, "ListByProject should not return an error")
			require.Len(t, tasks, tt.expected, "should return expected number of tasks")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestProjectTaskStore_Delete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewProjectTaskStore(mock)

	// Delete has 2 named args: id, org_id
	mock.ExpectExec("DELETE FROM project_tasks WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.Delete(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err, "Delete should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectTaskStore_CountByProjectAndStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name: "returns count for status",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(5))
			},
			expected: 5,
		},
		{
			name: "returns zero when no matching tasks",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
			},
			expected: 0,
		},
		{
			name: "returns error on db failure",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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

			store := NewProjectTaskStore(mock)
			tt.setupMock(mock)

			count, err := store.CountByProjectAndStatus(context.Background(), uuid.New(), uuid.New(), "completed")
			if tt.expectErr {
				require.Error(t, err, "CountByProjectAndStatus should return an error")
				return
			}
			require.NoError(t, err, "CountByProjectAndStatus should not return an error")
			require.Equal(t, tt.expected, count, "should return expected count")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestProjectTaskStore_GetMaxBatchNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name: "returns max batch number",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				maxBatch := 3
				mock.ExpectQuery("SELECT max\\(batch_number\\)").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(&maxBatch))
			},
			expected: 3,
		},
		{
			name: "returns zero when no tasks exist",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT max\\(batch_number\\)").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(nil))
			},
			expected: 0,
		},
		{
			name: "returns error on db failure",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT max\\(batch_number\\)").
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

			store := NewProjectTaskStore(mock)
			tt.setupMock(mock)

			maxBatch, err := store.GetMaxBatchNumber(context.Background(), uuid.New(), uuid.New())
			if tt.expectErr {
				require.Error(t, err, "GetMaxBatchNumber should return an error")
				return
			}
			require.NoError(t, err, "GetMaxBatchNumber should not return an error")
			require.Equal(t, tt.expected, maxBatch, "should return expected max batch number")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestProjectTaskStore_Update(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewProjectTaskStore(mock)

	// Update has 18 named args
	mock.ExpectExec("UPDATE project_tasks SET").
		WithArgs(anyArgs(18)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("DELETE FROM project_task_dependencies").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	task := &models.ProjectTask{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		ProjectID: uuid.New(),
		Title:     "Updated Task",
		Status:    models.ProjectTaskStatusCompleted,
		SortOrder: 2,
	}

	err = store.Update(context.Background(), task)
	require.NoError(t, err, "Update should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
