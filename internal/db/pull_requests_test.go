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
		"health_version", "merge_when_ready_state", "merge_when_ready_requested_by", "merge_when_ready_requested_at",
		"merge_when_ready_head_sha", "merge_when_ready_health_version", "merge_when_ready_error",
		"merge_when_ready_updated_at", "feedback_monitoring", "feedback_bot_epoch", "feedback_bot_cycles_in_epoch", "merged_at", "created_at", "updated_at",
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
					models.PullRequestMergeStateUnknown, false, 0, false, nil, int64(0),
					models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil, models.PRFeedbackMonitoringInherit, int64(0), 0, nil, now, now),
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
		"health_version", "merge_when_ready_state", "merge_when_ready_requested_by", "merge_when_ready_requested_at",
		"merge_when_ready_head_sha", "merge_when_ready_health_version", "merge_when_ready_error",
		"merge_when_ready_updated_at", "feedback_monitoring", "feedback_bot_epoch", "feedback_bot_cycles_in_epoch", "merged_at", "created_at", "updated_at",
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
					models.PullRequestMergeStateUnknown, false, 0, false, nil, int64(0),
					models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil, models.PRFeedbackMonitoringInherit, int64(0), 0, nil, now, now),
		)

	pr, err := store.GetByOrgRepoAndNumber(context.Background(), orgID, "org/repo", 42)
	require.NoError(t, err, "GetByOrgRepoAndNumber should find the org-scoped pull request")
	require.Equal(t, prID, pr.ID, "GetByOrgRepoAndNumber should return the matching pull request")
	require.Equal(t, orgID, pr.OrgID, "GetByOrgRepoAndNumber should preserve the owning org")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStore_ListOpenByOrgRepoAndHeadSHA(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	store := NewPullRequestStore(mock)

	cols := []string{
		"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
		"title", "body", "status", "review_status", "authored_by", "ci_status", "head_sha", "head_ref", "base_sha",
		"merge_state", "has_conflicts", "failing_test_count", "needs_agent_action", "github_state_synced_at",
		"health_version", "merge_when_ready_state", "merge_when_ready_requested_by", "merge_when_ready_requested_at",
		"merge_when_ready_head_sha", "merge_when_ready_health_version", "merge_when_ready_error",
		"merge_when_ready_updated_at", "feedback_monitoring", "feedback_bot_epoch", "feedback_bot_cycles_in_epoch", "merged_at", "created_at", "updated_at",
	}

	prID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	now := time.Now()
	headSHA := "head-sha"

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id = .+ AND github_repo = .+ AND head_sha = .+ AND status = 'open'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "org/repo", "head_sha": headSHA}).
		WillReturnRows(
			pgxmock.NewRows(cols).
				AddRow(prID, &sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "org/repo",
					"Fix bug", ptrStr("Description"), "open", "pending", "user1", "", &headSHA, nil, nil,
					models.PullRequestMergeStateUnknown, false, 0, false, nil, int64(0),
					models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil, models.PRFeedbackMonitoringInherit, int64(0), 0, nil, now, now),
		)

	prs, err := store.ListOpenByOrgRepoAndHeadSHA(context.Background(), orgID, "org/repo", headSHA)
	require.NoError(t, err, "ListOpenByOrgRepoAndHeadSHA should find open PRs for the org/repo/head SHA")
	require.Len(t, prs, 1, "ListOpenByOrgRepoAndHeadSHA should return the matching PR")
	require.Equal(t, prID, prs[0].ID, "ListOpenByOrgRepoAndHeadSHA should preserve the PR id")
	require.Equal(t, orgID, prs[0].OrgID, "ListOpenByOrgRepoAndHeadSHA should preserve the owning org")
	require.Equal(t, &headSHA, prs[0].HeadSHA, "ListOpenByOrgRepoAndHeadSHA should return the matching head SHA")
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
		"health_version", "merge_when_ready_state", "merge_when_ready_requested_by", "merge_when_ready_requested_at",
		"merge_when_ready_head_sha", "merge_when_ready_health_version", "merge_when_ready_error",
		"merge_when_ready_updated_at", "feedback_monitoring", "feedback_bot_epoch", "feedback_bot_cycles_in_epoch", "merged_at", "created_at", "updated_at",
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
					models.PullRequestMergeStateUnknown, false, 0, false, nil, int64(0),
					models.PullRequestMergeWhenReadyStateOff, nil, nil, "", nil, "", nil, models.PRFeedbackMonitoringInherit, int64(0), 0, nil, now, now),
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

func TestPullRequestStore_UpdateGitHubSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	store := NewPullRequestStore(mock)

	orgID := uuid.New()
	prID := uuid.New()
	body := "Fresh description"
	headSHA := "new-head"
	headRef := "feature/code-review"
	baseSHA := "new-base"

	mock.ExpectExec("UPDATE pull_requests[\\s\\S]*github_pr_url = @github_pr_url[\\s\\S]*head_sha = @head_sha[\\s\\S]*health_version = CASE WHEN head_sha IS DISTINCT FROM @head_sha THEN 0").
		WithArgs(pgx.NamedArgs{
			"id":            prID,
			"org_id":        orgID,
			"github_pr_url": "https://github.com/org/repo/pull/42",
			"title":         "Fresh title",
			"body":          &body,
			"head_sha":      &headSHA,
			"head_ref":      &headRef,
			"base_sha":      &baseSHA,
			"merge_state":   models.PullRequestMergeStateUnknown,
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateGitHubSnapshot(context.Background(), orgID, prID, PullRequestGitHubSnapshot{
		GitHubPRURL: "https://github.com/org/repo/pull/42",
		Title:       "Fresh title",
		Body:        &body,
		HeadSHA:     &headSHA,
		HeadRef:     &headRef,
		BaseSHA:     &baseSHA,
	})
	require.NoError(t, err, "UpdateGitHubSnapshot should persist the latest GitHub PR mirror fields")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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

func TestPullRequestStore_QueueMergeWhenReady_RequeuesCancelledState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()
	version := int64(12)

	mock.ExpectQuery("UPDATE pull_requests[\\s\\S]*merge_when_ready_state = @state[\\s\\S]*merge_when_ready_error = ''[\\s\\S]*WHERE id = @id AND org_id = @org_id[\\s\\S]*RETURNING").
		WithArgs(pgx.NamedArgs{
			"id":             prID,
			"org_id":         orgID,
			"requested_by":   userID,
			"head_sha":       "head-new",
			"health_version": version,
			"state":          models.PullRequestMergeWhenReadyStateQueued,
		}).
		WillReturnRows(pgxmock.NewRows([]string{
			"merge_when_ready_state",
			"merge_when_ready_requested_by",
			"merge_when_ready_requested_at",
			"merge_when_ready_head_sha",
			"merge_when_ready_health_version",
			"merge_when_ready_error",
		}).AddRow(models.PullRequestMergeWhenReadyStateQueued, &userID, &now, "head-new", &version, ""))

	status, err := store.QueueMergeWhenReady(context.Background(), orgID, prID, userID, "head-new", version)
	require.NoError(t, err, "QueueMergeWhenReady should allow re-queueing after cancellation")
	require.Equal(t, models.PullRequestMergeWhenReadyStateQueued, status.State, "QueueMergeWhenReady should return the queued state")
	require.Equal(t, userID, *status.RequestedByUserID, "QueueMergeWhenReady should replace the requesting user")
	require.Equal(t, "head-new", status.RequestedHeadSHA, "QueueMergeWhenReady should replace the requested head SHA")
	require.Empty(t, status.LastError, "QueueMergeWhenReady should clear prior cancellation or failure errors")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStore_CancelMergeWhenReady(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()
	version := int64(12)

	mock.ExpectQuery("UPDATE pull_requests[\\s\\S]*merge_when_ready_state = @state[\\s\\S]*WHERE id = @id AND org_id = @org_id[\\s\\S]*merge_when_ready_state IN \\('queued', 'failed', 'cancelled'\\)[\\s\\S]*RETURNING").
		WithArgs(pgx.NamedArgs{
			"id":     prID,
			"org_id": orgID,
			"state":  models.PullRequestMergeWhenReadyStateCancelled,
		}).
		WillReturnRows(pgxmock.NewRows([]string{
			"merge_when_ready_state",
			"merge_when_ready_requested_by",
			"merge_when_ready_requested_at",
			"merge_when_ready_head_sha",
			"merge_when_ready_health_version",
			"merge_when_ready_error",
		}).AddRow(models.PullRequestMergeWhenReadyStateCancelled, &userID, &now, "head", &version, ""))

	status, err := store.CancelMergeWhenReady(context.Background(), orgID, prID)
	require.NoError(t, err, "CancelMergeWhenReady should mark queued intent cancelled")
	require.Equal(t, models.PullRequestMergeWhenReadyStateCancelled, status.State, "CancelMergeWhenReady should return the cancelled state")
	require.Equal(t, "head", status.RequestedHeadSHA, "CancelMergeWhenReady should preserve the cancelled request context")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStore_ClaimMergeWhenReadyForProcessing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rowsAffected int64
		expected     bool
	}{
		{name: "claims queued intent", rowsAffected: 1, expected: true},
		{name: "does not claim missing or fresh intent", rowsAffected: 0, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should initialize")
			defer mock.Close()

			store := NewPullRequestStore(mock)
			orgID := uuid.New()
			prID := uuid.New()
			staleBefore := time.Now().Add(-15 * time.Minute).UTC()

			mock.ExpectExec("UPDATE pull_requests[\\s\\S]*merge_when_ready_state = @state[\\s\\S]*merge_when_ready_state = 'queued'[\\s\\S]*merge_when_ready_state = 'merging'[\\s\\S]*merge_when_ready_updated_at < @stale_before").
				WithArgs(pgx.NamedArgs{
					"id":           prID,
					"org_id":       orgID,
					"state":        models.PullRequestMergeWhenReadyStateMerging,
					"stale_before": staleBefore,
				}).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rowsAffected))

			claimed, err := store.ClaimMergeWhenReadyForProcessing(context.Background(), orgID, prID, staleBefore)
			require.NoError(t, err, "ClaimMergeWhenReadyForProcessing should not return database errors on successful exec")
			require.Equal(t, tt.expected, claimed, "ClaimMergeWhenReadyForProcessing should report whether it claimed the intent")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPullRequestStore_ReleaseMergeWhenReadyClaim(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()

	mock.ExpectExec("UPDATE pull_requests[\\s\\S]*merge_when_ready_state = @state[\\s\\S]*merge_when_ready_error = ''[\\s\\S]*merge_when_ready_state = 'merging'").
		WithArgs(pgx.NamedArgs{
			"id":     prID,
			"org_id": orgID,
			"state":  models.PullRequestMergeWhenReadyStateQueued,
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.ReleaseMergeWhenReadyClaim(context.Background(), orgID, prID)
	require.NoError(t, err, "ReleaseMergeWhenReadyClaim should return a claimed intent to queued without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
