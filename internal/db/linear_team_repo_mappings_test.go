package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLinearTeamRepoMappingStore_UpsertScopesRepositoryToOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("(?s)INSERT INTO linear_team_repo_mappings.*SELECT.*FROM repositories.*r.id = @repository_id.*r.org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "linear_team_id", "linear_project_id",
			"repository_id", "default_branch", "priority",
			"created_at", "updated_at",
		}).AddRow(
			rowID, orgID, "team_1", nil,
			repoID, "main", 0,
			now, now,
		))

	store := NewLinearTeamRepoMappingStore(mock)
	mapping, err := store.Upsert(context.Background(), orgID, UpsertInput{
		OrgID:         orgID,
		LinearTeamID:  "team_1",
		RepositoryID:  repoID,
		DefaultBranch: "main",
	})
	require.NoError(t, err, "upsert should succeed for a repository in the same org")
	require.Equal(t, repoID, mapping.RepositoryID, "mapping should point at the requested repository")
	require.NoError(t, mock.ExpectationsWereMet(), "upsert SQL should validate repository ownership inside the insert")
}

func TestLinearTeamRepoMappingStore_UpsertRejectsDisconnectedRepository(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()

	mock.ExpectQuery("(?s)INSERT INTO linear_team_repo_mappings.*SELECT.*FROM repositories.*r.id = @repository_id.*r.org_id = @org_id.*r.status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "linear_team_id", "linear_project_id",
			"repository_id", "default_branch", "priority",
			"created_at", "updated_at",
		}))

	store := NewLinearTeamRepoMappingStore(mock)
	_, err = store.Upsert(context.Background(), orgID, UpsertInput{
		OrgID:        orgID,
		LinearTeamID: "team_1",
		RepositoryID: repoID,
	})
	require.ErrorIs(t, err, ErrLinearTeamRepoMappingNotFound, "upsert should reject repositories that are not active")
	require.NoError(t, mock.ExpectationsWereMet(), "upsert SQL should filter the selected repository to active rows")
}
