package db

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var repoColumns = []string{
	"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
	"private", "language", "description", "clone_url", "installation_id", "status",
	"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
}

func newRepoRow(id, orgID, integrationID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		id, orgID, integrationID, int64(12345), "org/repo", "main",
		false, nil, nil, "https://github.com/org/repo.git", int64(99),
		"active", nil, nil, json.RawMessage(`{}`), now, now,
	}
}

func TestRepositoryStore_ListByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	orgID := uuid.New()
	repoID1 := uuid.New()
	repoID2 := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repoColumns).
				AddRow(newRepoRow(repoID1, orgID, integrationID, now)...).
				AddRow(newRepoRow(repoID2, orgID, integrationID, now)...),
		)

	repos, err := store.ListByOrg(context.Background(), orgID, RepositoryFilters{IncludeDisconnected: true})
	require.NoError(t, err, "ListByOrg should not return an error")
	require.Len(t, repos, 2, "should return both repositories for the org")
	require.Equal(t, repoID1, repos[0].ID, "should return the first repository ID")
	require.Equal(t, repoID2, repos[1].ID, "should return the second repository ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_GetByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, repoID, integrationID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns repository when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, repoID, integrationID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(repoColumns).
							AddRow(newRepoRow(repoID, orgID, integrationID, now)...),
					)
			},
		},
		{
			name: "returns error when repository not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, repoID, integrationID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(repoColumns))
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

			store := NewRepositoryStore(mock)
			orgID := uuid.New()
			repoID := uuid.New()
			integrationID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, repoID, integrationID, now)

			repo, err := store.GetByID(context.Background(), orgID, repoID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error when repository is not found")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, repoID, repo.ID, "should return the correct repository ID")
			require.Equal(t, "org/repo", repo.FullName, "should return the correct repository full name")
			require.Equal(t, "active", repo.Status, "should return the correct repository status")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestRepositoryStore_SetStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	row := newRepoRow(repoID, orgID, integrationID, now)
	// Override the status column (index 11) to reflect the new value.
	row[11] = "disconnected"

	mock.ExpectQuery("UPDATE repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoColumns).AddRow(row...))

	repo, err := store.SetStatus(context.Background(), orgID, repoID, models.RepositoryStatusDisconnected)
	require.NoError(t, err, "SetStatus should not error on happy path")
	require.Equal(t, "disconnected", repo.Status, "should return the refreshed row with new status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_UpsertFromGitHub_OmitsStatusFromSetClause(t *testing.T) {
	// Regression guard: a GitHub sync must never silently re-activate a
	// user-disconnected repo. UpsertFromGitHub deliberately omits `status`
	// from the ON CONFLICT ... DO UPDATE SET clause; if it ever gets added,
	// this test catches it at the source level.
	t.Parallel()

	src, err := os.ReadFile("repositories.go")
	require.NoError(t, err, "reading repositories.go source should succeed")

	i := strings.Index(string(src), "UpsertFromGitHub")
	require.NotEqual(t, -1, i, "UpsertFromGitHub must exist in repositories.go")

	// Scan the SET clause only — an INSERT line referencing `status` is fine.
	tail := string(src)[i:]
	setStart := strings.Index(tail, "DO UPDATE")
	returnEnd := strings.Index(tail, "RETURNING")
	require.NotEqual(t, -1, setStart, "expected DO UPDATE clause in UpsertFromGitHub")
	require.NotEqual(t, -1, returnEnd, "expected RETURNING clause in UpsertFromGitHub")
	setClause := tail[setStart:returnEnd]

	require.NotContains(t, setClause, "status",
		"UpsertFromGitHub's DO UPDATE SET clause must not touch status — would silently revive user-disconnected repos on sync")
}

func TestRepositoryStore_GetByFullName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, repoID, integrationID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns repository when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, repoID, integrationID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = @org_id AND full_name = @full_name").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(repoColumns).
							AddRow(newRepoRow(repoID, orgID, integrationID, now)...),
					)
			},
		},
		{
			name: "returns error when repository not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, repoID, integrationID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = @org_id AND full_name = @full_name").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(repoColumns))
			},
			expectErr: true,
		},
		{
			name: "returns error when query fails",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, repoID, integrationID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = @org_id AND full_name = @full_name").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(context.DeadlineExceeded)
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewRepositoryStore(mock)
			orgID := uuid.New()
			repoID := uuid.New()
			integrationID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, repoID, integrationID, now)

			repo, err := store.GetByFullName(context.Background(), orgID, "org/repo")
			if tt.expectErr {
				require.Error(t, err, "GetByFullName should return an error")
				return
			}
			require.NoError(t, err, "GetByFullName should not return an error")
			require.Equal(t, repoID, repo.ID, "should return the correct repository ID")
			require.Equal(t, orgID, repo.OrgID, "should return the correct org ID")
			require.Equal(t, "org/repo", repo.FullName, "should return the correct repository full name")
			require.Equal(t, "active", repo.Status, "should return the correct repository status")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestRepositoryStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	repo := &models.Repository{
		OrgID:          uuid.New(),
		IntegrationID:  uuid.New(),
		GitHubID:       12345,
		FullName:       "org/new-repo",
		DefaultBranch:  "main",
		Private:        false,
		CloneURL:       "https://github.com/org/new-repo.git",
		InstallationID: 99,
		Status:         "active",
		Settings:       json.RawMessage(`{}`),
	}

	mock.ExpectQuery("INSERT INTO repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(generatedID, now, now),
		)

	err = store.Create(context.Background(), repo)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, generatedID, repo.ID, "should set the generated ID on the repository")
	require.Equal(t, now, repo.CreatedAt, "should set the created_at timestamp on the repository")
	require.Equal(t, now, repo.UpdatedAt, "should set the updated_at timestamp on the repository")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_Update(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	now := time.Now()

	repo := &models.Repository{
		ID:       uuid.New(),
		OrgID:    uuid.New(),
		Status:   "inactive",
		Settings: json.RawMessage(`{"auto_fix":true}`),
	}

	mock.ExpectQuery("UPDATE repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"updated_at"}).
				AddRow(now),
		)

	err = store.Update(context.Background(), repo)
	require.NoError(t, err, "Update should not return an error")
	require.Equal(t, now, repo.UpdatedAt, "should set the updated_at timestamp on the repository")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_Delete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()

	mock.ExpectExec("DELETE FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.Delete(context.Background(), orgID, repoID)
	require.NoError(t, err, "Delete should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_UpsertFromGitHub(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	repo := &models.Repository{
		OrgID:          uuid.New(),
		IntegrationID:  uuid.New(),
		GitHubID:       67890,
		FullName:       "org/upsert-repo",
		DefaultBranch:  "main",
		Private:        true,
		CloneURL:       "https://github.com/org/upsert-repo.git",
		InstallationID: 42,
		Status:         "active",
		Settings:       json.RawMessage(`{}`),
	}

	mock.ExpectQuery("INSERT INTO repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(generatedID, now, now),
		)

	err = store.UpsertFromGitHub(context.Background(), repo)
	require.NoError(t, err, "UpsertFromGitHub should not return an error")
	require.Equal(t, generatedID, repo.ID, "should set the generated ID on the repository")
	require.Equal(t, now, repo.CreatedAt, "should set the created_at timestamp on the repository")
	require.Equal(t, now, repo.UpdatedAt, "should set the updated_at timestamp on the repository")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_GetSummary(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()
	latestStatus := "running"

	summaryColumns := []string{
		"repository_id", "full_name", "active_session_count",
		"latest_session_status", "active_project_count",
	}

	mock.ExpectQuery("SELECT .+ FROM repositories r").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(summaryColumns).
				AddRow(repoID, "org/repo", 3, &latestStatus, 1),
		)

	summaries, err := store.GetSummary(context.Background(), orgID)
	require.NoError(t, err, "GetSummary should not return an error")
	require.Len(t, summaries, 1, "should return one summary")
	require.Equal(t, repoID, summaries[0].RepositoryID, "should return the correct repository ID")
	require.Equal(t, "org/repo", summaries[0].FullName, "should return the correct full name")
	require.Equal(t, 3, summaries[0].ActiveSessionCount, "should return the correct active session count")
	require.NotNil(t, summaries[0].LatestSessionStatus, "latest session status should not be nil")
	require.Equal(t, "running", *summaries[0].LatestSessionStatus, "should return the correct latest session status")
	require.Equal(t, 1, summaries[0].ActiveProjectCount, "should return the correct active project count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_GetSummary_Empty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	orgID := uuid.New()

	summaryColumns := []string{
		"repository_id", "full_name", "active_session_count",
		"latest_session_status", "active_project_count",
	}

	mock.ExpectQuery("SELECT .+ FROM repositories r").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(summaryColumns))

	summaries, err := store.GetSummary(context.Background(), orgID)
	require.NoError(t, err, "GetSummary should not return an error for empty result")
	require.Empty(t, summaries, "should return empty summaries")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_DisconnectByInstallationID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)

	mock.ExpectExec("UPDATE repositories").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	err = store.DisconnectByInstallationID(context.Background(), int64(99))
	require.NoError(t, err, "DisconnectByInstallationID should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
