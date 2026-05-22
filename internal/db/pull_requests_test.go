package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
		"title", "body", "status", "review_status", "authored_by", "ci_status", "head_sha", "head_ref", "base_sha",
		"merge_state", "has_conflicts", "failing_test_count", "needs_agent_action", "github_state_synced_at",
		"health_version", "merged_at", "created_at", "updated_at",
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
					"Fix bug", ptrStr("Description"), "open", "pending", "user1", "", nil, nil, nil,
					models.PullRequestMergeStateUnknown, false, 0, false, nil, int64(0), nil, now, now),
		)

	pr, err := store.GetByRepoAndNumber(context.Background(), "org/repo", 42)
	require.NoError(t, err)
	require.Equal(t, prID, pr.ID)
	require.Equal(t, 42, pr.GitHubPRNumber)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_GetByOrgRepoAndNumber(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	store := NewPullRequestStore(mock)

	cols := []string{
		"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
		"title", "body", "status", "review_status", "authored_by", "ci_status", "head_sha", "head_ref", "base_sha",
		"merge_state", "has_conflicts", "failing_test_count", "needs_agent_action", "github_state_synced_at",
		"health_version", "merged_at", "created_at", "updated_at",
	}

	prID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(cols).
				AddRow(prID, &sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "org/repo",
					"Fix bug", ptrStr("Description"), "open", "pending", "user1", "", nil, nil, nil,
					models.PullRequestMergeStateUnknown, false, 0, false, nil, int64(0), nil, now, now),
		)

	pr, err := store.GetByOrgRepoAndNumber(context.Background(), orgID, "org/repo", 42)
	require.NoError(t, err, "GetByOrgRepoAndNumber should find the org-scoped pull request")
	require.Equal(t, prID, pr.ID, "GetByOrgRepoAndNumber should return the matching pull request")
	require.Equal(t, orgID, pr.OrgID, "GetByOrgRepoAndNumber should preserve the owning org")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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
		"title", "body", "status", "review_status", "authored_by", "ci_status", "head_sha", "head_ref", "base_sha",
		"merge_state", "has_conflicts", "failing_test_count", "needs_agent_action", "github_state_synced_at",
		"health_version", "merged_at", "created_at", "updated_at",
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
					"Fix bug", ptrStr("body"), "open", "pending", "app", "success", nil, nil, nil,
					models.PullRequestMergeStateUnknown, false, 0, false, nil, int64(0), nil, now, now),
		)

	result, err := store.BatchGetBySessionIDs(context.Background(), orgID, []uuid.UUID{sessionID})
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, prID, result[sessionID].ID)
	require.Equal(t, models.PullRequestCIStatusSuccess, result[sessionID].CIStatus)
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

func TestPullRequestStore_UpdateTitle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)

	orgID := uuid.New()
	prID := uuid.New()

	mock.ExpectExec("UPDATE pull_requests SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateTitle(context.Background(), orgID, prID, "Updated PR title")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPullRequestStore_UpdateHeadSHA(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)

	orgID := uuid.New()
	prID := uuid.New()

	mock.ExpectExec("UPDATE pull_requests[\\s\\S]*SET head_sha = @head_sha[\\s\\S]*github_state_synced_at = NULL[\\s\\S]*health_version = 0[\\s\\S]*failing_test_count = 0").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateHeadSHA(context.Background(), orgID, prID, "abc123def456")
	require.NoError(t, err, "UpdateHeadSHA should persist the new commit SHA and mark cached health stale")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// Load-bearing regression: pr_health_service.go's currentMatchesHead
// short-circuit treats `pr.HealthVersion != 0` as proof that UpdateHeadSHA
// has not run since the cached health summary was written. If a future writer
// stops resetting health_version to 0 here, that short-circuit will silently
// surface stale "Resolve conflicts" / "Fix tests" banners after a fresh push.
// Pin the literal `health_version = 0` substring so the test fails loudly if
// the SET clause changes.
//
// Bumping the column to a non-zero default? Update pr_health_service.go's
// currentMatchesHead at the same time so the SHA comparison becomes
// unconditional, then update this regression to match.
func TestPullRequestStore_UpdateHeadSHA_ResetsHealthVersionToZero(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)

	orgID := uuid.New()
	prID := uuid.New()

	// Anchor the literal SET fragment so accidental edits like
	// `health_version = pr.health_version` or `health_version = @hv` fail
	// the regex match instead of silently sliding past it.
	mock.ExpectExec(`SET\s+head_sha = @head_sha,\s+github_state_synced_at = NULL,\s+health_version = 0,`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateHeadSHA(context.Background(), orgID, prID, "abc123def456")
	require.NoError(t, err, "UpdateHeadSHA should reset health_version to 0 on every push to invalidate the cached health summary")
	require.NoError(t, mock.ExpectationsWereMet(), "the SET clause must literally write health_version = 0 — see currentMatchesHead in pr_health_service.go")
}

// Drift detection: if the PR row was deleted between the push and the
// follow-up UpdateHeadSHA, surface pgx.ErrNoRows so the worker dead-letters
// and we can investigate instead of silently leaving GitHub with a new
// commit no DB row tracks.
func TestPullRequestStore_UpdateHeadSHA_NoRowReturnsErrNoRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPullRequestStore(mock)

	orgID := uuid.New()
	prID := uuid.New()

	mock.ExpectExec("UPDATE pull_requests[\\s\\S]*SET head_sha = @head_sha").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = store.UpdateHeadSHA(context.Background(), orgID, prID, "abc123def456")
	require.ErrorIs(t, err, pgx.ErrNoRows, "UpdateHeadSHA should report drift when no PR row matched")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
