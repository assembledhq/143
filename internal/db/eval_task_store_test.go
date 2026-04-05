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

var evalTaskTestColumns = []string{
	"id", "org_id", "repo_id", "name", "description",
	"base_commit_sha", "solution_commit_sha", "solution_diff",
	"issue_description", "issue_context",
	"server_deploy_sha", "pm_document_set_pin_id", "org_settings_version_id",
	"memory_snapshot", "sandbox_image_digest", "context_overrides",
	"scoring_criteria", "pass_threshold",
	"source", "source_pr_number", "complexity", "tags",
	"created_by", "created_at", "updated_at", "archived_at",
}

func newEvalTaskRow(taskID, orgID, repoID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		taskID, orgID, repoID, "Auth token refresh regression", "Tests auth token refresh",
		"abc123", nil, nil,
		"Fix the auth token refresh bug", json.RawMessage(`{}`),
		nil, nil, nil,
		nil, nil, json.RawMessage(`{}`),
		json.RawMessage(`[{"name":"tests_pass","grader_type":"code_check","weight":1.0}]`), 0.7,
		"manual", nil, "moderate", []string{"auth"},
		nil, now, now, nil,
	}
}

func TestEvalTaskStore_Create(t *testing.T) {
	t.Parallel()

	taskID := uuid.New()
	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO eval_tasks").
		WithArgs(anyArgs(22)...).
		WillReturnRows(pgxmock.NewRows(evalTaskTestColumns).AddRow(newEvalTaskRow(taskID, orgID, repoID, now)...))

	store := NewEvalTaskStore(mock)
	task := &models.EvalTask{
		OrgID:            orgID,
		RepoID:           repoID,
		Name:             "Auth token refresh regression",
		Description:      "Tests auth token refresh",
		BaseCommitSHA:    "abc123",
		IssueDescription: "Fix the auth token refresh bug",
		IssueContext:     json.RawMessage(`{}`),
		ContextOverrides: json.RawMessage(`{}`),
		ScoringCriteria:  json.RawMessage(`[{"name":"tests_pass","grader_type":"code_check","weight":1.0}]`),
		PassThreshold:    0.7,
		Source:           models.EvalTaskSourceManual,
		Complexity:       models.EvalComplexityModerate,
		Tags:             []string{"auth"},
	}

	err = store.Create(context.Background(), task)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, taskID, task.ID, "Create should set the task ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalTaskStore_GetByID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	taskID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns task when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(evalTaskTestColumns).AddRow(newEvalTaskRow(taskID, orgID, repoID, now)...))
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(evalTaskTestColumns))
			},
			expectErr: true,
		},
		{
			name: "returns error on db failure",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection closed"))
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

			tt.setupMock(mock)

			store := NewEvalTaskStore(mock)
			task, err := store.GetByID(context.Background(), orgID, taskID)

			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error")
			} else {
				require.NoError(t, err, "GetByID should not return an error")
				require.Equal(t, taskID, task.ID, "should return the correct task")
				require.Equal(t, orgID, task.OrgID, "should belong to the correct org")
			}

			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestEvalTaskStore_ListByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	otherOrgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	taskID1 := uuid.New()
	taskID2 := uuid.New()

	tests := []struct {
		name        string
		orgID       uuid.UUID
		filters     models.EvalTaskListFilters
		setupMock   func(mock pgxmock.PgxPoolIface)
		expectCount int
		expectErr   bool
	}{
		{
			name:    "returns tasks for org",
			orgID:   orgID,
			filters: models.EvalTaskListFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(evalTaskTestColumns).
						AddRow(newEvalTaskRow(taskID1, orgID, repoID, now)...).
						AddRow(newEvalTaskRow(taskID2, orgID, repoID, now)...))
			},
			expectCount: 2,
		},
		{
			name:    "returns empty for other org",
			orgID:   otherOrgID,
			filters: models.EvalTaskListFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE").
					WithArgs(anyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(evalTaskTestColumns))
			},
			expectCount: 0,
		},
		{
			name:  "filters by source",
			orgID: orgID,
			filters: models.EvalTaskListFilters{
				Source: ptrTo(models.EvalTaskSourcePRBootstrap),
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE").
					WithArgs(anyArgs(3)...).
					WillReturnRows(pgxmock.NewRows(evalTaskTestColumns))
			},
			expectCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			tt.setupMock(mock)

			store := NewEvalTaskStore(mock)
			tasks, err := store.ListByOrg(context.Background(), tt.orgID, tt.filters)

			if tt.expectErr {
				require.Error(t, err, "ListByOrg should return an error")
			} else {
				require.NoError(t, err, "ListByOrg should not return an error")
				require.Len(t, tasks, tt.expectCount, "should return expected number of tasks")
			}

			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestEvalTaskStore_Archive(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	taskID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectExec("UPDATE eval_tasks SET archived_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalTaskStore(mock)
	err = store.Archive(context.Background(), orgID, taskID)
	require.NoError(t, err, "Archive should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func ptrTo[T any](v T) *T {
	return &v
}
