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

var prColumns = []string{
	"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
	"title", "body", "status", "review_status", "authored_by", "ci_status", "head_sha", "head_ref", "base_sha",
	"merge_state", "has_conflicts", "failing_test_count", "needs_agent_action", "github_state_synced_at",
	"health_version", "merge_when_ready_state", "merge_when_ready_requested_by", "merge_when_ready_requested_at",
	"merge_when_ready_head_sha", "merge_when_ready_health_version", "merge_when_ready_error",
	"merge_when_ready_updated_at", "feedback_monitoring", "feedback_bot_epoch", "feedback_bot_cycles_in_epoch",
	"merged_at", "created_at", "updated_at",
}

func newPRRow(id, sessionID, orgID uuid.UUID, now time.Time) []any {
	return []any{
		id, &sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "org/repo",
		"Fix bug", (*string)(nil), "open", "pending", "app", "", (*string)(nil), (*string)(nil), (*string)(nil),
		models.PullRequestMergeStateUnknown, false, 0, false, (*time.Time)(nil), int64(0),
		models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil),
		models.PRFeedbackMonitoringInherit, int64(0), 0,
		(*time.Time)(nil), now, now,
	}
}

func TestPullRequestStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	sid := uuid.New()
	pr := &models.PullRequest{
		SessionID:      &sid,
		OrgID:          uuid.New(),
		GitHubPRNumber: 42,
		GitHubPRURL:    "https://github.com/org/repo/pull/42",
		GitHubRepo:     "org/repo",
		Title:          "Fix bug",
		Status:         "open",
		ReviewStatus:   "pending",
	}

	mock.ExpectQuery(`(?s)INSERT INTO pull_requests .*changeset_id.*SELECT id FROM session_changesets`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(generatedID, now, now),
		)

	err = store.Create(context.Background(), pr)
	require.NoError(t, err, "should create pull request without error")
	require.Equal(t, generatedID, pr.ID, "should set the generated ID on the pull request")
	require.Equal(t, now, pr.CreatedAt, "should set the created_at timestamp on the pull request")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStoreCreateUsesExplicitChangeset(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	now := time.Now()
	sessionID := uuid.New()
	changesetID := uuid.New()
	pr := &models.PullRequest{
		SessionID: &sessionID, ChangesetID: &changesetID, OrgID: uuid.New(), GitHubPRNumber: 43,
		GitHubPRURL: "https://github.com/org/repo/pull/43", GitHubRepo: "org/repo", Title: "Second PR",
		Status: models.PullRequestStatusOpen, ReviewStatus: models.PullRequestReviewStatusPending,
	}
	mock.ExpectQuery(`INSERT INTO pull_requests .*changeset_id`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))

	err = NewPullRequestStore(mock).Create(context.Background(), pr)
	require.NoError(t, err, "Create should persist an explicitly targeted changeset")
	require.Equal(t, &changesetID, pr.ChangesetID, "Create should preserve explicit changeset identity")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStoreGetByChangesetIDScopesByOrgAndSession(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	changesetID := uuid.New()
	prID := uuid.New()
	now := time.Now()
	columns := append([]string{}, prColumns[:2]...)
	columns = append(columns, "changeset_id")
	columns = append(columns, prColumns[2:]...)
	row := newPRRow(prID, sessionID, orgID, now)
	row = append(row[:2], append([]any{&changesetID}, row[2:]...)...)

	mock.ExpectQuery(`SELECT .+ FROM pull_requests WHERE org_id = .+ AND session_id = .+ AND changeset_id =`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(columns).AddRow(row...))

	pr, err := NewPullRequestStore(mock).GetByChangesetID(context.Background(), orgID, sessionID, changesetID)
	require.NoError(t, err, "GetByChangesetID should return the tenant- and session-scoped PR")
	require.Equal(t, prID, pr.ID, "GetByChangesetID should return the expected PR")
	require.Equal(t, &changesetID, pr.ChangesetID, "GetByChangesetID should hydrate changeset identity")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStore_GetByID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prColumns).
				AddRow(newPRRow(id, sessionID, orgID, now)...),
		)

	pr, err := store.GetByID(context.Background(), orgID, id)
	require.NoError(t, err, "should retrieve pull request by ID without error")
	require.Equal(t, id, pr.ID, "should return the correct pull request ID")
	require.Equal(t, &sessionID, pr.SessionID, "should return the correct agent run ID")
	require.Equal(t, orgID, pr.OrgID, "should return the correct org ID")
	require.Equal(t, 42, pr.GitHubPRNumber, "should return the correct GitHub PR number")
	require.Equal(t, "https://github.com/org/repo/pull/42", pr.GitHubPRURL, "should return the correct GitHub PR URL")
	require.Equal(t, "org/repo", pr.GitHubRepo, "should return the correct GitHub repo")
	require.Equal(t, "Fix bug", pr.Title, "should return the correct title")
	require.Equal(t, models.PullRequestStatusOpen, pr.Status, "should return the correct status")
	require.Equal(t, models.PullRequestReviewStatusPending, pr.ReviewStatus, "should return the correct review status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPullRequestStore(mock)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prColumns))

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "should return an error when pull request is not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStore_GetBySessionID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE session_id.+changeset_id IS NULL.+OR changeset_id =.+ORDER BY").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prColumns).
				AddRow(newPRRow(id, sessionID, orgID, now)...),
		)

	pr, err := store.GetBySessionID(context.Background(), orgID, sessionID)
	require.NoError(t, err, "should retrieve pull request by agent run ID without error")
	require.Equal(t, id, pr.ID, "should return the correct pull request ID")
	require.Equal(t, &sessionID, pr.SessionID, "should return the correct agent run ID")
	require.Equal(t, orgID, pr.OrgID, "should return the correct org ID")
	require.Equal(t, 42, pr.GitHubPRNumber, "should return the correct GitHub PR number")
	require.Equal(t, models.PullRequestStatusOpen, pr.Status, "should return the correct status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStore_BatchGetPrimaryBySessionIDsAllowsRollingLegacyRows(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	defer mock.Close()

	orgID, sessionID, prID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	mock.ExpectQuery("SELECT DISTINCT ON .+ FROM pull_requests.+changeset_id IS NULL.+OR changeset_id =.+ORDER BY session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prColumns).AddRow(newPRRow(prID, sessionID, orgID, now)...))

	prs, err := NewPullRequestStore(mock).BatchGetPrimaryBySessionIDs(context.Background(), orgID, []uuid.UUID{sessionID})
	require.NoError(t, err, "batch primary lookup should accept an unattached PR from a rolling legacy writer")
	require.Equal(t, prID, prs[sessionID].ID, "batch primary lookup should retain one-PR compatibility during rollout")
	require.NoError(t, mock.ExpectationsWereMet(), "query should exclude child changesets while retaining legacy candidates")
}

func TestPullRequestStore_UpdateStatus_Closed(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE pull_requests SET status .+ updated_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, id, "closed")
	require.NoError(t, err, "should update pull request status to closed without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStore_UpdateStatus_Merged(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE pull_requests SET status .+ merged_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, id, "merged")
	require.NoError(t, err, "should update pull request status to merged without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStore_ListByOrg_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prColumns).
				AddRow(newPRRow(id1, sessionID, orgID, now)...).
				AddRow(newPRRow(id2, sessionID, orgID, now)...),
		)

	prs, err := store.ListByOrg(context.Background(), orgID, PullRequestFilters{})
	require.NoError(t, err, "should list pull requests by org without error")
	require.Len(t, prs, 2, "should return both pull requests for the org")
	require.Equal(t, id1, prs[0].ID, "first pull request should have the correct ID")
	require.Equal(t, id2, prs[1].ID, "second pull request should have the correct ID")
	require.Equal(t, &sessionID, prs[0].SessionID, "first pull request should have the correct agent run ID")
	require.Equal(t, orgID, prs[0].OrgID, "first pull request should have the correct org ID")
	require.Equal(t, 42, prs[0].GitHubPRNumber, "first pull request should have the correct GitHub PR number")
	require.Equal(t, models.PullRequestStatusOpen, prs[0].Status, "first pull request should have the correct status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPullRequestStore_ListByOrg_WithStatusFilter(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id .+ AND status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(prColumns).
				AddRow(newPRRow(id, sessionID, orgID, now)...),
		)

	prs, err := store.ListByOrg(context.Background(), orgID, PullRequestFilters{Status: "open"})
	require.NoError(t, err, "should list pull requests filtered by status without error")
	require.Len(t, prs, 1, "should return only the pull request matching the status filter")
	require.Equal(t, id, prs[0].ID, "filtered pull request should have the correct ID")
	require.Equal(t, models.PullRequestStatusOpen, prs[0].Status, "filtered pull request should have the correct status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
