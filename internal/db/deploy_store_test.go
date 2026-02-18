package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
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
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Equal(t, generatedID, d.ID)
	assert.Equal(t, now, d.DeployedAt)
	assert.Equal(t, now, d.CreatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDeployStore_GetByPullRequestID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Equal(t, id, d.ID)
	assert.Equal(t, prID, d.PullRequestID)
	assert.Equal(t, "production", d.Environment)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDeployStore_GetByPullRequestID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewDeployStore(mock)

	mock.ExpectQuery("SELECT .+ FROM deploys WHERE pull_request_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(deployColumns))

	_, err = store.GetByPullRequestID(context.Background(), uuid.New(), uuid.New())
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
