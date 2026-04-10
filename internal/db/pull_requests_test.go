package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func ptrStr(s string) *string { return &s }

func TestPullRequestStore_GetByRepoAndNumber(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)

	cols := []string{
		"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
		"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
	}

	prID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(cols).
				AddRow(prID, &sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "org/repo",
					"Fix bug", ptrStr("Description"), "open", "pending", "user1", "", nil, now, now),
		)

	pr, err := store.GetByRepoAndNumber(context.Background(), "org/repo", 42)
	require.NoError(t, err)
	require.Equal(t, prID, pr.ID)
	require.Equal(t, 42, pr.GitHubPRNumber)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_UpdateCIStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)

	orgID := uuid.New()
	prID := uuid.New()

	mock.ExpectExec("UPDATE pull_requests SET ci_status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateCIStatus(context.Background(), orgID, prID, "success")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_BatchGetBySessionIDs_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)

	cols := []string{
		"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
		"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
	}

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(cols).
				AddRow(prID, &sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "org/repo",
					"Fix bug", ptrStr("body"), "open", "pending", "app", "success", nil, now, now),
		)

	result, err := store.BatchGetBySessionIDs(context.Background(), orgID, []uuid.UUID{sessionID})
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, prID, result[sessionID].ID)
	require.Equal(t, "success", result[sessionID].CIStatus)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_BatchGetBySessionIDs_Empty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)

	result, err := store.BatchGetBySessionIDs(context.Background(), uuid.New(), []uuid.UUID{})
	require.NoError(t, err)
	require.Nil(t, result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_UpdateReviewStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)

	orgID := uuid.New()
	prID := uuid.New()

	mock.ExpectExec("UPDATE pull_requests SET review_status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateReviewStatus(context.Background(), orgID, prID, "approved")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
