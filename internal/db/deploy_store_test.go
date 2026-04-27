package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var deployColumns = []string{
	"id", "pull_request_id", "org_id", "environment", "deployed_at", "commit_sha", "created_at",
}

func newDeployRow(id, prID, orgID uuid.UUID, now time.Time) []any {
	sha := "abc123"
	return []any{
		id, prID, orgID, "production", now, &sha, now,
	}
}

func TestDeployStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewDeployStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	sha := "abc123"

	d := &models.Deploy{
		PullRequestID: uuid.New(),
		OrgID:         uuid.New(),
		Environment:   "production",
		CommitSHA:     &sha,
	}

	mock.ExpectQuery("INSERT INTO deploys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "deployed_at", "created_at"}).
				AddRow(generatedID, now, now),
		)

	err = store.Create(context.Background(), d)
	require.NoError(t, err, "should create deploy without error")
	require.Equal(t, generatedID, d.ID, "should set the generated ID on the deploy")
	require.Equal(t, now, d.DeployedAt, "should set the deployed_at timestamp on the deploy")
	require.Equal(t, now, d.CreatedAt, "should set the created_at timestamp on the deploy")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestDeployStore_Create_ConflictIsNoOp(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewDeployStore(mock)
	sha := "abc123"

	d := &models.Deploy{
		PullRequestID: uuid.New(),
		OrgID:         uuid.New(),
		Environment:   "production",
		CommitSHA:     &sha,
	}

	mock.ExpectQuery("INSERT INTO deploys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "deployed_at", "created_at"}))

	err = store.Create(context.Background(), d)
	require.NoError(t, err, "ON CONFLICT DO NOTHING should produce a successful no-op even when no row is returned")
	require.Equal(t, uuid.Nil, d.ID, "deploy fields should remain unset when the insert is skipped by ON CONFLICT")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestDeployStore_GetByPullRequestID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewDeployStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	prID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM deploys WHERE pull_request_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(deployColumns).
				AddRow(newDeployRow(id, prID, orgID, now)...),
		)

	d, err := store.GetByPullRequestID(context.Background(), orgID, prID)
	require.NoError(t, err, "should retrieve deploy by pull request ID without error")
	require.Equal(t, id, d.ID, "should return the correct deploy ID")
	require.Equal(t, prID, d.PullRequestID, "should return the correct pull request ID")
	require.Equal(t, orgID, d.OrgID, "should return the correct org ID")
	require.Equal(t, "production", d.Environment, "should return the correct environment")
	require.NotNil(t, d.CommitSHA, "should return a non-nil commit SHA")
	require.Equal(t, "abc123", *d.CommitSHA, "should return the correct commit SHA value")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestDeployStore_GetByPullRequestID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewDeployStore(mock)

	mock.ExpectQuery("SELECT .+ FROM deploys WHERE pull_request_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(deployColumns))

	_, err = store.GetByPullRequestID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "should return an error when deploy is not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
