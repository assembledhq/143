package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
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

func TestRepositoryStore_ListByOrg_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
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

	repos, err := store.ListByOrg(context.Background(), orgID)
	require.NoError(t, err)
	assert.Len(t, repos, 2)
	assert.Equal(t, repoID1, repos[0].ID)
	assert.Equal(t, repoID2, repos[1].ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryStore_GetByID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewRepositoryStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repoColumns).
				AddRow(newRepoRow(repoID, orgID, integrationID, now)...),
		)

	repo, err := store.GetByID(context.Background(), orgID, repoID)
	require.NoError(t, err)
	assert.Equal(t, repoID, repo.ID)
	assert.Equal(t, "org/repo", repo.FullName)
	assert.Equal(t, "active", repo.Status)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryStore_GetByID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewRepositoryStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoColumns))

	_, err = store.GetByID(context.Background(), orgID, repoID)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryStore_Create_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Equal(t, generatedID, repo.ID)
	assert.Equal(t, now, repo.CreatedAt)
	assert.Equal(t, now, repo.UpdatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryStore_Update_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Equal(t, now, repo.UpdatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryStore_Delete_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewRepositoryStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()

	mock.ExpectExec("DELETE FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.Delete(context.Background(), orgID, repoID)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryStore_UpsertFromGitHub_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Equal(t, generatedID, repo.ID)
	assert.Equal(t, now, repo.CreatedAt)
	assert.Equal(t, now, repo.UpdatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryStore_DisconnectByInstallationID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewRepositoryStore(mock)

	mock.ExpectExec("UPDATE repositories").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	err = store.DisconnectByInstallationID(context.Background(), int64(99))
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
