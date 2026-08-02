package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestRepositoryStore_DisconnectByGitHubID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewRepositoryStore(mock)

	mock.ExpectExec("UPDATE repositories SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.DisconnectByGitHubID(context.Background(), 12345, 67890)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRepositoryStore_MergeSettings(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create a database mock")
	t.Cleanup(mock.Close)

	orgID, repoID, integrationID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT[\\s\\S]+FROM repositories[\\s\\S]+FOR UPDATE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private",
			"language", "description", "clone_url", "installation_id", "status", "last_synced_at",
			"context_quality", "settings", "created_at", "updated_at",
		}).AddRow(
			repoID, orgID, integrationID, int64(143), "assembledhq/143", "main", true,
			nil, nil, "https://github.com/assembledhq/143.git", int64(99), "active", nil,
			nil, json.RawMessage(`{"preview":{"enabled":true},"pr_handoff_mode":"pre_publish"}`), now, now,
		))
	updatedAt := now.Add(time.Second)
	mock.ExpectQuery("UPDATE repositories").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"updated_at"}).AddRow(updatedAt))
	mock.ExpectCommit()

	repo, err := NewRepositoryStore(mock).MergeSettings(
		context.Background(), orgID, repoID, json.RawMessage(`{"pr_handoff_mode":"draft_first"}`),
	)
	require.NoError(t, err, "repository settings merge should succeed")
	require.JSONEq(t, `{"preview":{"enabled":true},"pr_handoff_mode":"draft_first"}`, string(repo.Settings), "repository settings merge should preserve unrelated settings")
	require.Equal(t, updatedAt, repo.UpdatedAt, "repository settings merge should return the persisted update time")
	require.NoError(t, mock.ExpectationsWereMet(), "repository settings merge should use one tenant-scoped transaction")
}
