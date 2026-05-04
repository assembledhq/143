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

var projectTestColumns = []string{
	"id", "org_id", "repository_id", "title", "goal", "scope", "completion_criteria",
	"status", "priority", "execution_mode", "max_concurrent", "auto_merge", "base_branch",
	"current_phase", "lessons_learned", "approach_history",
	"total_tasks", "completed_tasks", "failed_tasks",
	"proposed_by_pm", "source_issue_ids", "proposal_reasoning", "similar_projects",
	"agent_type", "model_override",
	"created_by", "deleted_at", "created_at", "updated_at", "completed_at", "archived_at",
}

func newProjectRow(projectID, orgID, repoID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		projectID, orgID, &repoID, "Test Project", "Build a thing", nil, nil,
		"draft", 50, "sequential", 2, false, "main",
		nil, json.RawMessage(`[]`), json.RawMessage(`[]`),
		0, 0, 0,
		false, []uuid.UUID{}, nil, json.RawMessage(`[]`),
		nil, nil,
		nil, (*time.Time)(nil), now, now, nil, nil,
	}
}

func anyArgs(n int) []interface{} {
	args := make([]interface{}, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func TestProjectStore_Create(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	// Create wraps insert + join-table writes in a transaction.
	mock.ExpectBegin()
	// 22 named args: projects INSERT columns minus the schedule fields that
	// moved to the automations table in Phase 3 (schedule_enabled,
	// schedule_interval, schedule_unit, next_run_at).
	mock.ExpectQuery("INSERT INTO projects").
		WithArgs(anyArgs(22)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(projectID, now, now))
	mock.ExpectCommit()

	store := NewProjectStore(mock)
	project := &models.Project{
		OrgID:         orgID,
		RepositoryID:  &repoID,
		Title:         "Test Project",
		Goal:          "Build a thing",
		Status:        models.ProjectStatusDraft,
		Priority:      50,
		ExecutionMode: models.ProjectExecModeSequential,
		MaxConcurrent: 2,
		BaseBranch:    "main",
	}

	err = store.Create(context.Background(), project)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, projectID, project.ID, "Create should set the project ID")
	require.WithinDuration(t, now, project.CreatedAt, time.Second, "Create should set created_at")
	require.WithinDuration(t, now, project.UpdatedAt, time.Second, "Create should set updated_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectStore_GetByID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns project when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(projectTestColumns).AddRow(newProjectRow(projectID, orgID, repoID, now)...))
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(projectTestColumns))
			},
			expectErr: true,
		},
		{
			name: "returns error on db failure",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
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

			store := NewProjectStore(mock)
			tt.setupMock(mock)

			project, err := store.GetByID(context.Background(), orgID, projectID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, projectID, project.ID, "should return the correct project ID")
			require.Equal(t, orgID, project.OrgID, "should return the correct org ID")
			require.Equal(t, "Test Project", project.Title, "should return the correct project title")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestProjectStore_ListByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		filters   ProjectFilters
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name:    "returns projects for org",
			filters: ProjectFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(projectTestColumns).
							AddRow(newProjectRow(uuid.New(), orgID, repoID, now)...).
							AddRow(newProjectRow(uuid.New(), orgID, repoID, now)...),
					)
			},
			expected: 2,
		},
		{
			name:    "returns filtered projects by status",
			filters: ProjectFilters{Status: "active"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id .+ AND status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(projectTestColumns).
							AddRow(newProjectRow(uuid.New(), orgID, repoID, now)...),
					)
			},
			expected: 1,
		},
		{
			name:    "excludes archived projects by default",
			filters: ProjectFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id .+ archived_at IS NULL").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(projectTestColumns).
							AddRow(newProjectRow(uuid.New(), orgID, repoID, now)...),
					)
			},
			expected: 1,
		},
		{
			name:    "returns only archived projects when requested",
			filters: ProjectFilters{OnlyArchived: true},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id .+ archived_at IS NOT NULL").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(projectTestColumns).
							AddRow(newProjectRow(uuid.New(), orgID, repoID, now)...),
					)
			},
			expected: 1,
		},
		{
			name:    "returns empty when no projects exist",
			filters: ProjectFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(projectTestColumns))
			},
			expected: 0,
		},
		{
			name:    "returns error on database failure",
			filters: ProjectFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
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

			store := NewProjectStore(mock)
			tt.setupMock(mock)

			projects, err := store.ListByOrg(context.Background(), orgID, tt.filters)
			if tt.expectErr {
				require.Error(t, err, "ListByOrg should return an error")
				return
			}
			require.NoError(t, err, "ListByOrg should not return an error")
			require.Len(t, projects, tt.expected, "should return expected number of projects")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestProjectStore_Archive(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "archives project successfully",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE projects SET archived_at = now\\(\\), updated_at = now\\(\\) WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "returns error when project is already archived",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE projects SET archived_at = now\\(\\), updated_at = now\\(\\) WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 0))
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

			store := NewProjectStore(mock)
			tt.setupMock(mock)

			err = store.Archive(context.Background(), orgID, projectID)
			if tt.expectErr {
				require.Error(t, err, "Archive should return an error")
				return
			}
			require.NoError(t, err, "Archive should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestProjectStore_Unarchive(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "unarchives project successfully",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE projects SET archived_at = NULL, updated_at = now\\(\\) WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NOT NULL").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "returns error when project is not archived",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE projects SET archived_at = NULL, updated_at = now\\(\\) WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NOT NULL").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 0))
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

			store := NewProjectStore(mock)
			tt.setupMock(mock)

			err = store.Unarchive(context.Background(), orgID, projectID)
			if tt.expectErr {
				require.Error(t, err, "Unarchive should return an error")
				return
			}
			require.NoError(t, err, "Unarchive should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestProjectStore_ListByOrg_CursorUsesSortKeyPredicate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	cursorID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id .+ priority > .+ created_at < .+ id <").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectTestColumns))

	store := NewProjectStore(mock)
	projects, err := store.ListByOrg(context.Background(), orgID, ProjectFilters{
		Cursor: cursorID.String(),
		Limit:  10,
	})
	require.NoError(t, err, "ListByOrg should not return an error for cursor pagination")
	require.Len(t, projects, 0, "ListByOrg should return no projects when none match")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectStore_Update(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewProjectStore(mock)
	orgID := uuid.New()
	projectID := uuid.New()

	// 20 named args: the projects UPDATE SET list after Phase 3 removed
	// the four per-project schedule columns.
	mock.ExpectExec("UPDATE projects SET").
		WithArgs(anyArgs(20)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	project := &models.Project{
		ID:            projectID,
		OrgID:         orgID,
		Title:         "Updated Project",
		Goal:          "New goal",
		Status:        models.ProjectStatusActive,
		Priority:      10,
		ExecutionMode: models.ProjectExecModeParallel,
		MaxConcurrent: 4,
		BaseBranch:    "develop",
	}

	err = store.Update(context.Background(), project)
	require.NoError(t, err, "Update should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
	}{
		{name: "update to active", status: "active"},
		{name: "update to completed", status: "completed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewProjectStore(mock)

			// UpdateStatus has 3 named args: id, org_id, status
			mock.ExpectExec("UPDATE projects SET status").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			err = store.UpdateStatus(context.Background(), uuid.New(), uuid.New(), tt.status)
			require.NoError(t, err, "UpdateStatus should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestProjectStore_ListByOrg_WithRepositoryID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id .+ AND repository_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectTestColumns).
				AddRow(newProjectRow(uuid.New(), orgID, repoID, now)...),
		)

	store := NewProjectStore(mock)
	projects, err := store.ListByOrg(context.Background(), orgID, ProjectFilters{
		RepositoryID: repoID,
	})
	require.NoError(t, err, "ListByOrg with RepositoryID should not return an error")
	require.Len(t, projects, 1, "should return one project")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectStore_ListByOrg_WithSearch(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id .+ AND \\(title ILIKE .+ OR goal ILIKE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectTestColumns).
				AddRow(newProjectRow(uuid.New(), orgID, repoID, now)...),
		)

	store := NewProjectStore(mock)
	projects, err := store.ListByOrg(context.Background(), orgID, ProjectFilters{
		Search: "build",
	})
	require.NoError(t, err, "ListByOrg with Search should not return an error")
	require.Len(t, projects, 1, "should return one project")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectStore_ListByOrg_WithCreatedBy(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id .+ AND created_by").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectTestColumns).
				AddRow(newProjectRow(uuid.New(), orgID, repoID, now)...),
		)

	store := NewProjectStore(mock)
	projects, err := store.ListByOrg(context.Background(), orgID, ProjectFilters{
		CreatedBy: userID,
	})
	require.NoError(t, err, "ListByOrg with CreatedBy should not return an error")
	require.Len(t, projects, 1, "should return one project")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectStore_ListByOrg_WithCreatedByIDs(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	userID1 := uuid.New()
	userID2 := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery(`SELECT .+ FROM projects WHERE org_id .+ AND created_by = ANY`).
		WithArgs(pgxmock.AnyArg(), []uuid.UUID{userID1, userID2}).
		WillReturnRows(
			pgxmock.NewRows(projectTestColumns).
				AddRow(newProjectRow(uuid.New(), orgID, repoID, now)...),
		)

	store := NewProjectStore(mock)
	projects, err := store.ListByOrg(context.Background(), orgID, ProjectFilters{
		CreatedByIDs: []uuid.UUID{userID1, userID2},
	})
	require.NoError(t, err, "ListByOrg with CreatedByIDs should not return an error")
	require.Len(t, projects, 1, "should return one project")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectStore_SoftDelete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewProjectStore(mock)

	mock.ExpectExec("UPDATE projects SET deleted_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.SoftDelete(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err, "SoftDelete should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectStore_SoftDelete_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewProjectStore(mock)

	mock.ExpectExec("UPDATE projects SET deleted_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = store.SoftDelete(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "SoftDelete should return an error when project not found")
	require.Contains(t, err.Error(), "not found or already deleted")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectStore_Count(t *testing.T) {
	t.Parallel()

	t.Run("count by status", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		store := NewProjectStore(mock)

		mock.ExpectQuery("SELECT count").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

		count, err := store.Count(context.Background(), uuid.New(), ProjectFilters{Status: "active"})
		require.NoError(t, err)
		require.Equal(t, 3, count)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("count by status and proposed_by_pm", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		store := NewProjectStore(mock)

		mock.ExpectQuery("SELECT count").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

		pmTrue := true
		count, err := store.Count(context.Background(), uuid.New(), ProjectFilters{Status: "draft", ProposedByPM: &pmTrue})
		require.NoError(t, err)
		require.Equal(t, 2, count)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("count by status, repo, and proposed_by_pm", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		store := NewProjectStore(mock)

		mock.ExpectQuery("SELECT count").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

		pmTrue := true
		count, err := store.Count(context.Background(), uuid.New(), ProjectFilters{Status: "draft", RepositoryID: uuid.New(), ProposedByPM: &pmTrue})
		require.NoError(t, err)
		require.Equal(t, 1, count)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestProjectStore_ListByOrgRepoStatuses(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewProjectStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectTestColumns).
				AddRow(newProjectRow(uuid.New(), orgID, repoID, now)...),
		)

	projects, err := store.ListByOrgRepoStatuses(context.Background(), orgID, repoID, []string{"active"})
	require.NoError(t, err, "ListByOrgRepoStatuses should not return an error")
	require.Len(t, projects, 1, "should return one project")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectStore_UpdateProgress(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewProjectStore(mock)

	// UpdateProgress has 2 named args: project_id, org_id
	mock.ExpectExec("UPDATE projects SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateProgress(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err, "UpdateProgress should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
