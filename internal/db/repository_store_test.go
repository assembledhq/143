package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

func TestRepositoryStore_ListByOrg_DefaultFiltersDisconnected(t *testing.T) {
	// Default filter path: the SQL picked up by pgxmock must restrict to
	// status = 'active'. Regression guard against a refactor that drops the
	// WHERE clause and silently surfaces disconnected repos in pickers.
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	orgID := uuid.New()

	mock.ExpectQuery(`status = 'active'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoColumns))

	repos, err := store.ListByOrg(context.Background(), orgID, RepositoryFilters{})
	require.NoError(t, err, "ListByOrg should not return an error with default filters")
	require.Empty(t, repos, "no repos expected")
	require.NoError(t, mock.ExpectationsWereMet(), "default filter must emit the status = 'active' predicate")
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

func TestRepositoryStore_SetStatus_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)

	mock.ExpectQuery("UPDATE repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)

	_, err = store.SetStatus(context.Background(), uuid.New(), uuid.New(), models.RepositoryStatusActive)
	require.Error(t, err, "SetStatus should propagate query errors")
	require.Contains(t, err.Error(), "update repository status", "error should be wrapped with context")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_UpsertFromGitHub_OmitsStatusFromSetClause(t *testing.T) {
	// Regression guard: a GitHub sync must never silently re-activate a
	// user-disconnected repo. UpsertFromGitHub deliberately omits `status`
	// from the ON CONFLICT ... DO UPDATE SET clause; if it ever gets added,
	// this test catches it at the source level.
	//
	// The check parses the SET clause's column names rather than substring-
	// matching the word "status", so reformatting the SQL (lowercasing
	// keywords, reordering columns, changing whitespace) won't trip it.
	t.Parallel()

	src, err := os.ReadFile("repositories.go")
	require.NoError(t, err, "reading repositories.go source should succeed")

	setClause := extractUpsertFromGitHubSetClause(t, string(src))
	cols := setClauseColumns(setClause)
	require.NotContains(t, cols, "status",
		"UpsertFromGitHub's DO UPDATE SET clause must not include status — would silently revive user-disconnected repos on sync (SET columns: %v)", cols)
}

// extractUpsertFromGitHubSetClause returns the contents of the DO UPDATE SET
// clause from the UpsertFromGitHub function in repositories.go. Case-insensitive
// keyword matching keeps the test robust to SQL reformatting.
func extractUpsertFromGitHubSetClause(t *testing.T, src string) string {
	t.Helper()
	fnIdx := strings.Index(src, "UpsertFromGitHub")
	require.NotEqual(t, -1, fnIdx, "UpsertFromGitHub must exist in repositories.go")
	tail := src[fnIdx:]

	// (?is) = case-insensitive + dotall so we can match across newlines.
	re := regexp.MustCompile(`(?is)do\s+update\s+set\s+(.*?)\s+returning\b`)
	matches := re.FindStringSubmatch(tail)
	require.Len(t, matches, 2, "expected DO UPDATE SET ... RETURNING block in UpsertFromGitHub")
	return matches[1]
}

// setClauseColumns returns the lowercased LHS column names from a SET clause's
// `col = expr, col = expr` list. Splits at the top level only, so commas inside
// function calls (e.g., COALESCE(a, b)) don't fool the parser.
func setClauseColumns(clause string) []string {
	var (
		cols  []string
		depth int
		buf   strings.Builder
	)
	flush := func() {
		assignment := strings.TrimSpace(buf.String())
		buf.Reset()
		if assignment == "" {
			return
		}
		if eq := strings.Index(assignment, "="); eq >= 0 {
			cols = append(cols, strings.ToLower(strings.TrimSpace(assignment[:eq])))
		}
	}
	for _, ch := range clause {
		switch {
		case ch == '(':
			depth++
			buf.WriteRune(ch)
		case ch == ')':
			depth--
			buf.WriteRune(ch)
		case ch == ',' && depth == 0:
			flush()
		default:
			buf.WriteRune(ch)
		}
	}
	flush()
	return cols
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

func TestRepositoryStore_GetByFullNameAnyStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    string
		queryErr  error
		expectErr bool
	}{
		{
			name:   "returns disconnected repository when found",
			status: "disconnected",
		},
		{
			name:      "returns error when query fails",
			queryErr:  context.Canceled,
			expectErr: true,
		},
		{
			name:      "returns error when repository not found",
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

			if tt.queryErr != nil {
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = @org_id AND full_name = @full_name").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(tt.queryErr)
			} else if tt.expectErr {
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = @org_id AND full_name = @full_name").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(repoColumns))
			} else {
				row := newRepoRow(repoID, orgID, integrationID, now)
				row[11] = tt.status
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = @org_id AND full_name = @full_name").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(repoColumns).
							AddRow(row...),
					)
			}

			repo, err := store.GetByFullNameAnyStatus(context.Background(), orgID, "org/repo")
			if tt.expectErr {
				require.Error(t, err, "GetByFullNameAnyStatus should return an error")
				require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
				return
			}
			require.NoError(t, err, "GetByFullNameAnyStatus should not return an error")
			require.Equal(t, repoID, repo.ID, "should return the correct repository ID")
			require.Equal(t, tt.status, repo.Status, "should preserve the repository status")
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

func TestRepositoryStore_GetActiveOwnerByGitHubID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	repoID := uuid.New()
	orgID := uuid.New()

	mock.ExpectQuery("JOIN organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"repository_id", "org_id", "org_name", "github_id", "full_name", "status"}).
				AddRow(repoID, orgID, "Assembled", int64(67890), "assembledhq/143", "active"),
		)

	owner, err := store.GetActiveOwnerByGitHubID(context.Background(), 67890)
	require.NoError(t, err, "GetActiveOwnerByGitHubID should return the active owner")
	require.Equal(t, repoID, owner.RepositoryID, "active owner should include repository id")
	require.Equal(t, orgID, owner.OrgID, "active owner should include owning org id")
	require.Equal(t, "Assembled", owner.OrgName, "active owner should include owning org name")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_ClaimFromGitHub_ReactivatesDisconnectedRepo(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	now := time.Now()
	repoID := uuid.New()
	repo := &models.Repository{
		OrgID:          uuid.New(),
		IntegrationID:  uuid.New(),
		GitHubID:       67890,
		FullName:       "assembledhq/143",
		DefaultBranch:  "main",
		CloneURL:       "https://github.com/assembledhq/143.git",
		InstallationID: 12345,
		Settings:       json.RawMessage(`{}`),
	}

	mock.ExpectQuery("INSERT INTO repositories").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(repoID, now, now))

	err = store.ClaimFromGitHub(context.Background(), repo)
	require.NoError(t, err, "ClaimFromGitHub should activate the repository for the org")
	require.Equal(t, repoID, repo.ID, "ClaimFromGitHub should scan the repository id")
	require.Equal(t, string(models.RepositoryStatusActive), repo.Status, "ClaimFromGitHub should mark the repo active")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRepositoryStore_ClaimFromGitHub_MapsActiveOwnerUniqueViolation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	repo := &models.Repository{
		OrgID:          uuid.New(),
		IntegrationID:  uuid.New(),
		GitHubID:       67890,
		FullName:       "assembledhq/143",
		DefaultBranch:  "main",
		CloneURL:       "https://github.com/assembledhq/143.git",
		InstallationID: 12345,
		Settings:       json.RawMessage(`{}`),
	}

	mock.ExpectQuery("INSERT INTO repositories").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "idx_repositories_active_github_id"})

	err = store.ClaimFromGitHub(context.Background(), repo)
	require.ErrorIs(t, err, ErrActiveGitHubRepositoryOwnershipConflict, "ClaimFromGitHub should map the active GitHub owner unique index to a typed conflict")
	require.True(t, errors.As(err, new(*pgconn.PgError)), "ClaimFromGitHub should retain the underlying postgres error for diagnostics")
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

	mock.ExpectQuery(`s\.repository_id AS resolved_repository_id`).
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

	mock.ExpectQuery(`s\.repository_id AS resolved_repository_id`).
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

func TestRepositoryStore_GetAnyInstallationIDByOrg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expected  int64
		expectErr bool
	}{
		{
			name: "returns installation id for active repo",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT installation_id FROM repositories").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"installation_id"}).AddRow(int64(12345)))
			},
			expected: 12345,
		},
		{
			name: "returns error when no active repo installation exists",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT installation_id FROM repositories").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
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
			tt.setupMock(mock, orgID)

			installationID, err := store.GetAnyInstallationIDByOrg(context.Background(), orgID)
			if tt.expectErr {
				require.Error(t, err, "GetAnyInstallationIDByOrg should return an error when no installation id is available")
			} else {
				require.NoError(t, err, "GetAnyInstallationIDByOrg should not return an error")
				require.Equal(t, tt.expected, installationID, "GetAnyInstallationIDByOrg should return the expected installation id")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestRepositoryStore_DisconnectByIntegration(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewRepositoryStore(mock)
	orgID := uuid.New()
	integrationID := uuid.New()

	mock.ExpectExec("UPDATE repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	err = store.DisconnectByIntegration(context.Background(), orgID, integrationID)
	require.NoError(t, err, "DisconnectByIntegration should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
