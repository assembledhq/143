package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// prColumns matches the SELECT columns from pull_requests queries.
var handlerPRColumns = []string{
	"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
	"title", "body", "status", "review_status", "merged_at", "created_at", "updated_at",
}

// sessionColumns matches the SELECT columns from sessions queries (must match sessionSelectColumns in session_store.go).
var sessionColumns = []string{
	"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "pm_approach", "pm_reasoning", "project_task_id",
	"model_override",
	"created_at",
}

// newMockPool creates a pgxmock pool and returns it with a cleanup.
func newMockPool(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	t.Cleanup(func() { mock.Close() })
	return mock
}

func TestNewPRService(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	svc := NewPRService(nil, nil, nil, nil, nil, nil, nil, nil, logger)
	require.NotNil(t, svc, "NewPRService should return a non-nil service")
	require.Equal(t, defaultGitHubAPI, svc.baseURL, "NewPRService should set the default GitHub API base URL")
	require.NotNil(t, svc.httpClient, "NewPRService should initialize an HTTP client")
}

func TestSetBaseURL(t *testing.T) {
	t.Parallel()

	svc := &PRService{}
	svc.SetBaseURL("https://custom.api.example.com")
	require.Equal(t, "https://custom.api.example.com", svc.baseURL, "SetBaseURL should update the base URL")
}

func TestSetReviewCommentStore(t *testing.T) {
	t.Parallel()

	svc := &PRService{}
	require.Nil(t, svc.reviewComments, "reviewComments should be nil initially")

	mockPool := newMockPool(t)
	store := db.NewReviewCommentStore(mockPool)
	svc.SetReviewCommentStore(store)
	require.NotNil(t, svc.reviewComments, "SetReviewCommentStore should set the review comment store")
}

func TestHandlePullRequestEvent_MergedFlow(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	sessionMock := newMockPool(t)
	issueMock := newMockPool(t)
	deployMock := newMockPool(t)
	jobMock := newMockPool(t)

	prStore := db.NewPullRequestStore(prMock)
	sessionStore := db.NewSessionStore(sessionMock)
	issueStore := db.NewIssueStore(issueMock)
	deployStore := db.NewDeployStore(deployMock)
	jobStore := db.NewJobStore(jobMock)

	svc := &PRService{
		pullRequests: prStore,
		sessions:    sessionStore,
		issues:       issueStore,
		deploys:      deployStore,
		jobs:         jobStore,
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(prID, sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "testorg/testrepo",
					"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now),
		)

	// Mock: UpdateStatus to merged.
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Mock: GetByID for session.
	sessionMock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).
				AddRow(sessionID, issueID, orgID, "claude-code", "completed", "full", "standard",
					nil, nil, nil, nil,
					nil, nil, nil, nil,
					nil, nil, nil, nil,
					nil, nil, nil, nil, nil,
					nil, nil, nil, nil,
					nil, // model_override
					now),
		)

	// Mock: UpdateStatus for issue.
	issueMock.ExpectExec("UPDATE issues SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Mock: Create deploy.
	deployMock.ExpectQuery("INSERT INTO deploys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "deployed_at", "created_at"}).
				AddRow(uuid.New(), now, now),
		)

	// Mock: Enqueue job.
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()),
		)

	event := PullRequestEvent{
		Action: "closed",
		Number: 42,
	}
	event.PR.Merged = true
	event.PR.Head.SHA = "abc123commit"
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestEvent should not return an error for a merged PR")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR store expectations should be met")
	require.NoError(t, sessionMock.ExpectationsWereMet(), "all session store expectations should be met")
	require.NoError(t, issueMock.ExpectationsWereMet(), "all issue store expectations should be met")
	require.NoError(t, deployMock.ExpectationsWereMet(), "all deploy store expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "all job store expectations should be met")
}

func TestHandlePullRequestEvent_ClosedWithoutMergeFlow(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests: prStore,
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(prID, sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "testorg/testrepo",
					"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now),
		)

	// Mock: UpdateStatus to closed.
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	event := PullRequestEvent{
		Action: "closed",
		Number: 42,
	}
	event.PR.Merged = false
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestEvent should not return an error for a closed-without-merge PR")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR store expectations should be met")
}

func TestHandlePullRequestEvent_NonClosedAction(t *testing.T) {
	t.Parallel()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests: prStore,
		logger:       zerolog.Nop(),
	}

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(prID, sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "testorg/testrepo",
					"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now),
		)

	event := PullRequestEvent{
		Action: "opened",
		Number: 42,
	}
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestEvent should ignore non-closed actions")
}

func TestHandlePullRequestEvent_UnknownPR(t *testing.T) {
	t.Parallel()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests: prStore,
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns an error (not found).
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("no rows in result set"))

	event := PullRequestEvent{
		Action: "closed",
		Number: 999,
	}
	event.Repository.FullName = "unknown/repo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestEvent should silently ignore unknown PRs")
}

func TestHandlePullRequestReviewEvent_ApprovedFlow(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests: prStore,
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(prID, sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "testorg/testrepo",
					"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now),
		)

	// Mock: UpdateReviewStatus.
	prMock.ExpectExec("UPDATE pull_requests SET review_status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	event := PullRequestReviewEvent{
		Action: "submitted",
	}
	event.Review.State = "approved"
	event.Review.User.Login = "reviewer1"
	event.PullRequest.Number = 42
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestReviewEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestReviewEvent should not return an error for approved review")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR store expectations should be met")
}

func TestHandlePullRequestReviewEvent_ChangesRequestedWithBody(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	reviewMock := newMockPool(t)
	jobMock := newMockPool(t)

	prStore := db.NewPullRequestStore(prMock)
	reviewStore := db.NewReviewCommentStore(reviewMock)
	jobStore := db.NewJobStore(jobMock)

	svc := &PRService{
		pullRequests:   prStore,
		reviewComments: reviewStore,
		jobs:           jobStore,
		logger:         zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(prID, sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "testorg/testrepo",
					"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now),
		)

	// Mock: UpdateReviewStatus.
	prMock.ExpectExec("UPDATE pull_requests SET review_status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Mock: Create review comment.
	commentID := uuid.New()
	reviewMock.ExpectQuery("INSERT INTO review_comments").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(commentID, now),
		)

	// Mock: Enqueue job for processing review comment.
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()),
		)

	event := PullRequestReviewEvent{
		Action: "submitted",
	}
	event.Review.ID = 12345
	event.Review.State = "changes_requested"
	event.Review.Body = "Please fix the error handling"
	event.Review.User.Login = "reviewer1"
	event.PullRequest.Number = 42
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestReviewEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestReviewEvent should not return an error for changes_requested review")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR store expectations should be met")
	require.NoError(t, reviewMock.ExpectationsWereMet(), "all review comment store expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "all job store expectations should be met")
}

func TestHandlePullRequestReviewEvent_ChangesRequestedNoReviewStore(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests:   prStore,
		reviewComments: nil, // no review comment store
		logger:         zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(prID, sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "testorg/testrepo",
					"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now),
		)

	// Mock: UpdateReviewStatus.
	prMock.ExpectExec("UPDATE pull_requests SET review_status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	event := PullRequestReviewEvent{
		Action: "submitted",
	}
	event.Review.State = "changes_requested"
	event.Review.Body = "Please fix this"
	event.PullRequest.Number = 42
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestReviewEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestReviewEvent should not error when reviewComments store is nil")
}

func TestHandlePullRequestReviewEvent_NonSubmittedAction(t *testing.T) {
	t.Parallel()

	svc := &PRService{logger: zerolog.Nop()}

	event := PullRequestReviewEvent{
		Action: "edited",
	}

	err := svc.HandlePullRequestReviewEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestReviewEvent should ignore non-submitted actions")
}

func TestHandlePullRequestReviewEvent_UnknownReviewState(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests: prStore,
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(prID, sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "testorg/testrepo",
					"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now),
		)

	event := PullRequestReviewEvent{
		Action: "submitted",
	}
	event.Review.State = "commented"
	event.PullRequest.Number = 42
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestReviewEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestReviewEvent should return nil for unknown review states")
}

func TestHandlePullRequestReviewEvent_UnknownPR(t *testing.T) {
	t.Parallel()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests: prStore,
		logger:       zerolog.Nop(),
	}

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("no rows in result set"))

	event := PullRequestReviewEvent{
		Action: "submitted",
	}
	event.Review.State = "approved"
	event.PullRequest.Number = 999
	event.Repository.FullName = "unknown/repo"

	err := svc.HandlePullRequestReviewEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestReviewEvent should silently ignore unknown PRs")
}

func TestHandlePullRequestReviewCommentEvent_Created(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	reviewMock := newMockPool(t)
	jobMock := newMockPool(t)

	prStore := db.NewPullRequestStore(prMock)
	reviewStore := db.NewReviewCommentStore(reviewMock)
	jobStore := db.NewJobStore(jobMock)

	svc := &PRService{
		pullRequests:   prStore,
		reviewComments: reviewStore,
		jobs:           jobStore,
		logger:         zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(prID, sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "testorg/testrepo",
					"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now),
		)

	// Mock: Create review comment.
	commentID := uuid.New()
	reviewMock.ExpectQuery("INSERT INTO review_comments").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(commentID, now),
		)

	// Mock: Enqueue job.
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()),
		)

	position := 10
	event := PullRequestReviewCommentEvent{
		Action: "created",
	}
	event.Comment.ID = 67890
	event.Comment.Body = "This variable should be renamed"
	event.Comment.Path = "internal/main.go"
	event.Comment.Position = &position
	event.Comment.User.Login = "reviewer1"
	event.PullRequest.Number = 42
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestReviewCommentEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestReviewCommentEvent should not return an error for a created comment")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR store expectations should be met")
	require.NoError(t, reviewMock.ExpectationsWereMet(), "all review comment store expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "all job store expectations should be met")
}

func TestHandlePullRequestReviewCommentEvent_NonCreatedAction(t *testing.T) {
	t.Parallel()

	svc := &PRService{logger: zerolog.Nop()}

	event := PullRequestReviewCommentEvent{
		Action: "edited",
	}

	err := svc.HandlePullRequestReviewCommentEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestReviewCommentEvent should ignore non-created actions")
}

func TestHandlePullRequestReviewCommentEvent_UnknownPR(t *testing.T) {
	t.Parallel()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests: prStore,
		logger:       zerolog.Nop(),
	}

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("no rows in result set"))

	event := PullRequestReviewCommentEvent{
		Action: "created",
	}
	event.PullRequest.Number = 999
	event.Repository.FullName = "unknown/repo"

	err := svc.HandlePullRequestReviewCommentEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestReviewCommentEvent should silently ignore unknown PRs")
}

func TestHandlePullRequestReviewCommentEvent_NilReviewCommentStore(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests:   prStore,
		reviewComments: nil,
		logger:         zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(prID, sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "testorg/testrepo",
					"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now),
		)

	event := PullRequestReviewCommentEvent{
		Action: "created",
	}
	event.Comment.ID = 67890
	event.Comment.Body = "Some comment"
	event.PullRequest.Number = 42
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestReviewCommentEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestReviewCommentEvent should return nil when reviewComments store is nil")
}

func TestParseDiff_DeletedFileNotCapturedWithoutPath(t *testing.T) {
	t.Parallel()

	// The parser only sets currentPath from "+++ b/" lines. For deleted files
	// that have "+++ /dev/null", no path is captured, so the file is not included.
	diff := `diff --git a/old.go b/old.go
deleted file mode 100644
--- a/old.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package old
-
-func OldFunc() {}`

	result := parseDiff(diff)
	require.Empty(t, result, "parser does not capture deleted files with +++ /dev/null (no path set)")
}

func TestParseDiff_MixedAddAndRemoveLines(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,4 +1,4 @@
 package main

-import "old"
+import "new"

 func main() {}`

	result := parseDiff(diff)
	require.Len(t, result, 1, "parsed diff should have 1 file")
	require.Contains(t, result[0].Content, "import \"new\"", "content should include added lines")
	require.NotContains(t, result[0].Content, "import \"old\"", "content should not include removed lines")
}

func TestParseDiff_ContextLines(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,5 +1,6 @@
 package main

 import "fmt"
+import "os"

 func main() {}`

	result := parseDiff(diff)
	require.Len(t, result, 1, "parsed diff should have 1 file")
	require.Equal(t, "main.go", result[0].Path, "file path should match")
	require.Contains(t, result[0].Content, "import \"os\"", "content should include added line")
	require.Contains(t, result[0].Content, "package main", "content should include context lines")
	require.Contains(t, result[0].Content, "import \"fmt\"", "content should include unchanged context lines")
}

func TestParseDiff_EmptyDiff(t *testing.T) {
	t.Parallel()

	result := parseDiff("")
	require.Empty(t, result, "empty diff should produce empty result")
}

func TestGetPRHead(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/testorg/testrepo/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(map[string]any{
			"head": map[string]string{
				"sha": "headsha123",
				"ref": "143/fix/branch",
			},
		})
		require.NoError(t, err, "mock server should encode getPRHead response")
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	sha, branch, err := svc.getPRHead(context.Background(), "test-token", "testorg", "testrepo", 42)
	require.NoError(t, err, "getPRHead should not return an error")
	require.Equal(t, "headsha123", sha, "getPRHead should return the correct SHA")
	require.Equal(t, "143/fix/branch", branch, "getPRHead should return the correct branch name")
}

func TestPostComment(t *testing.T) {
	t.Parallel()

	var capturedBody map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/testorg/testrepo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		require.NoError(t, err, "mock server should decode comment body")
		err = json.NewEncoder(w).Encode(map[string]any{"id": 1})
		require.NoError(t, err, "mock server should encode response")
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	svc.postComment(context.Background(), "test-token", "testorg", "testrepo", 42, "Test comment body")
	require.Equal(t, "Test comment body", capturedBody["body"], "postComment should send the correct body")
}

func TestUpdateRef(t *testing.T) {
	t.Parallel()

	var capturedMethod string
	var capturedPath string
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /repos/testorg/testrepo/git/refs/heads/my-branch", func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		err := json.NewEncoder(w).Encode(map[string]string{"ref": "refs/heads/my-branch"})
		require.NoError(t, err, "mock server should encode updateRef response")
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	err := svc.updateRef(context.Background(), "test-token", "testorg", "testrepo", "refs/heads/my-branch", "newsha123")
	require.NoError(t, err, "updateRef should not return an error")
	require.Equal(t, "PATCH", capturedMethod, "updateRef should use PATCH method")
	require.Contains(t, capturedPath, "refs/heads/my-branch", "updateRef should call the correct path")
}

func TestDoGitHubRequest_WithBody(t *testing.T) {
	t.Parallel()

	var capturedContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedContentType = r.Header.Get("Content-Type")
		_, err := w.Write([]byte(`{"result":"ok"}`))
		require.NoError(t, err, "test server should write response")
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	body, err := svc.doGitHubRequest(context.Background(), "my-token", http.MethodPost, "/test", map[string]string{"key": "value"})
	require.NoError(t, err, "doGitHubRequest should not return an error for valid POST request")
	require.Equal(t, "application/json", capturedContentType, "doGitHubRequest should set Content-Type for POST requests with body")
	require.Contains(t, string(body), "ok", "doGitHubRequest should return response body")
}

func TestFormatPRBody_WithValidationStore(t *testing.T) {
	t.Parallel()

	validationMock := newMockPool(t)
	validationStore := db.NewValidationStore(validationMock)

	logger := zerolog.Nop()
	svc := &PRService{
		validations: validationStore,
		logger:      logger,
	}

	runID := uuid.New()
	orgID := uuid.New()
	summary := "Fixed the null pointer"
	run := &models.Session{
		ID:            runID,
		OrgID:         orgID,
		AgentType:     "claude-code",
		ResultSummary: &summary,
	}
	issue := &models.Issue{
		Source:                "sentry",
		Severity:              "high",
		AffectedCustomerCount: 5,
		OccurrenceCount:       20,
	}

	// Mock: GetBySessionID returns a validation.
	validationColumns := []string{
		"id", "session_id", "org_id", "status",
		"direction_check", "correctness_check", "quality_check", "security_scan",
		"regression_test_check", "coverage_delta", "ci_check", "details",
		"started_at", "completed_at", "created_at",
	}
	now := time.Now()
	validationMock.ExpectQuery("SELECT .+ FROM validations WHERE session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(validationColumns).
				AddRow(uuid.New(), runID, orgID, "completed",
					"pass", "pass", "pass", "pass",
					"pass", json.RawMessage(`{}`), "pass", json.RawMessage(`{}`),
					&now, &now, now),
		)

	body := svc.formatPRBody(context.Background(), run, issue)
	require.Contains(t, body, "## Validation", "PR body should contain Validation section when validation exists")
	require.Contains(t, body, "Direction alignment", "PR body should contain direction check row")
	require.Contains(t, body, "Correctness", "PR body should contain correctness check row")
	require.Contains(t, body, "Security scan", "PR body should contain security scan row")
}

func TestHandlePullRequestEvent_MergedWithUpdateStatusError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests: prStore,
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(prID, sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "testorg/testrepo",
					"Fix bug", (*string)(nil), "open", "pending", (*time.Time)(nil), now, now),
		)

	// Mock: UpdateStatus fails.
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("db connection lost"))

	event := PullRequestEvent{
		Action: "closed",
		Number: 42,
	}
	event.PR.Merged = true
	event.PR.Head.SHA = "abc123"
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.Error(t, err, "HandlePullRequestEvent should return an error when UpdateStatus fails for merged PR")
	require.Contains(t, err.Error(), "update PR status to merged", "error should describe the failed operation")
}

// issueColumns for mock issue queries.
var handlerIssueColumns = []string{
	"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
	"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
	"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
	"created_at", "updated_at",
}

// repoColumns for mock repository queries.
var handlerRepoColumns = []string{
	"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
	"private", "language", "description", "clone_url", "installation_id", "status",
	"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
}

func TestCreatePR_Success(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main

+import "fmt"
 func main() {}`

	diffStr := diff

	run := &models.Session{
		ID:        runID,
		OrgID:     orgID,
		IssueID:   issueID,
		AgentType: "claude-code",
		Diff:      &diffStr,
	}

	// Set up mock GitHub API server.
	baseSHA := "basesha123"
	blobSHA := "blobsha456"
	treeSHA := "treesha789"
	commitSHA := "commitsha012"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/testorg/testrepo/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]string{"sha": baseSHA},
		})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/git/refs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"ref": "refs/heads/143/fix/test"})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": blobSHA})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/git/trees", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": treeSHA})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/git/commits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": commitSHA})
		require.NoError(t, err)
	})
	mux.HandleFunc("PATCH /repos/testorg/testrepo/git/", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(map[string]string{"ref": "updated"})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/pulls", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]any{
			"number":   55,
			"html_url": "https://github.com/testorg/testrepo/pull/55",
		})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/issues/55/labels", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode([]map[string]string{{"name": "143-generated"}})
		require.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// Set up DB mocks.
	issueMock := newMockPool(t)
	repoMock := newMockPool(t)
	prMock := newMockPool(t)
	sessionMock := newMockPool(t)

	issueStore := db.NewIssueStore(issueMock)
	repoStore := db.NewRepositoryStore(repoMock)
	prStore := db.NewPullRequestStore(prMock)
	sessionStore := db.NewSessionStore(sessionMock)

	// Mock: issue GetByID.
	issueMock.ExpectQuery("SELECT .+ FROM issues WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerIssueColumns).
				AddRow(issueID, orgID, "ENG-123", "linear", nil, &repoID,
					"Fix null pointer", nil, json.RawMessage(`{}`), "open", now, now,
					5, 2, "high", []string{"bug"}, "fp-1",
					now, now),
		)

	// Mock: repo GetByID.
	repoMock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerRepoColumns).
				AddRow(repoID, orgID, integrationID, int64(12345), "testorg/testrepo", "main",
					false, nil, nil, "https://github.com/testorg/testrepo.git", int64(99),
					"active", nil, nil, json.RawMessage(`{}`), now, now),
		)

	// Mock: PR create.
	prMock.ExpectQuery("INSERT INTO pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(uuid.New(), now, now),
		)

	// Mock: session UpdateStatus.
	sessionMock.ExpectExec("UPDATE sessions SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Mock: issue UpdateStatus.
	issueMock.ExpectExec("UPDATE issues SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Create a mock token provider with cached token.
	tokenSvc := &Service{
		cache: make(map[int64]*cachedToken),
	}
	tokenSvc.cache[99] = &cachedToken{
		Token:     "test-installation-token",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		pullRequests:  prStore,
		sessions:     sessionStore,
		issues:        issueStore,
		repos:         repoStore,
		logger:        zerolog.Nop(),
		baseURL:       server.URL,
		httpClient:    server.Client(),
	}

	pr, err := svc.CreatePR(context.Background(), run)
	require.NoError(t, err, "CreatePR should not return an error for a valid run")
	require.NotNil(t, pr, "CreatePR should return a non-nil pull request")
	require.Equal(t, 55, pr.GitHubPRNumber, "PR number should match the mock server response")
	require.Equal(t, "testorg/testrepo", pr.GitHubRepo, "PR repo should match")
	require.Equal(t, "open", pr.Status, "PR status should be open")
}

func TestCreatePR_NoDiff(t *testing.T) {
	t.Parallel()

	svc := &PRService{logger: zerolog.Nop()}

	// Nil diff.
	run := &models.Session{ID: uuid.New(), Diff: nil}
	_, err := svc.CreatePR(context.Background(), run)
	require.Error(t, err, "CreatePR should return an error when diff is nil")
	require.Contains(t, err.Error(), "no diff", "error should mention no diff")

	// Empty diff.
	empty := ""
	run2 := &models.Session{ID: uuid.New(), Diff: &empty}
	_, err = svc.CreatePR(context.Background(), run2)
	require.Error(t, err, "CreatePR should return an error when diff is empty")
	require.Contains(t, err.Error(), "no diff", "error should mention no diff")
}

func TestCreatePR_NoRepositoryOnIssue(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	issueMock := newMockPool(t)
	issueStore := db.NewIssueStore(issueMock)

	svc := &PRService{
		issues: issueStore,
		logger: zerolog.Nop(),
	}

	// Mock: issue GetByID returns issue without repository.
	issueMock.ExpectQuery("SELECT .+ FROM issues WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerIssueColumns).
				AddRow(issueID, orgID, "ENG-123", "linear", nil, nil, // nil repository_id
					"Fix null pointer", nil, json.RawMessage(`{}`), "open", now, now,
					5, 2, "high", []string{}, "fp-1",
					now, now),
		)

	diff := "diff --git a/main.go b/main.go\n+++ b/main.go\n+package main\n"
	run := &models.Session{
		ID:      uuid.New(),
		OrgID:   orgID,
		IssueID: issueID,
		Diff:    &diff,
	}

	_, err := svc.CreatePR(context.Background(), run)
	require.Error(t, err, "CreatePR should return an error when issue has no repository")
	require.Contains(t, err.Error(), "no repository", "error should mention no repository")
}

func TestPushRevision_Success(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	prID := uuid.New()
	issueID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	diff := `diff --git a/handler.go b/handler.go
--- a/handler.go
+++ b/handler.go
@@ -1,3 +1,4 @@
 package handler
+import "errors"
 func Handle() {}`

	diffStr := diff
	resultSummary := "Fixed error handling per review feedback"

	run := &models.Session{
		ID:            runID,
		OrgID:         orgID,
		IssueID:       issueID,
		AgentType:     "claude-code",
		Diff:          &diffStr,
		ResultSummary: &resultSummary,
	}

	pr := &models.PullRequest{
		ID:             prID,
		OrgID:          orgID,
		SessionID:     uuid.New(),
		GitHubPRNumber: 42,
		GitHubRepo:     "testorg/testrepo",
	}

	// Set up mock GitHub API server.
	headSHA := "headsha123"
	blobSHA := "blobsha456"
	treeSHA := "treesha789"
	commitSHA := "commitsha012"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/testorg/testrepo/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(map[string]any{
			"head": map[string]string{"sha": headSHA, "ref": "143/fix/my-branch"},
		})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": blobSHA})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/git/trees", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": treeSHA})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/git/commits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": commitSHA})
		require.NoError(t, err)
	})
	mux.HandleFunc("PATCH /repos/testorg/testrepo/git/", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(map[string]string{"ref": "updated"})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(map[string]any{"id": 1})
		require.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// Set up DB mocks.
	issueMock := newMockPool(t)
	repoMock := newMockPool(t)

	issueStore := db.NewIssueStore(issueMock)
	repoStore := db.NewRepositoryStore(repoMock)

	// Mock: issue GetByID.
	issueMock.ExpectQuery("SELECT .+ FROM issues WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerIssueColumns).
				AddRow(issueID, orgID, "ENG-123", "linear", nil, &repoID,
					"Fix null pointer", nil, json.RawMessage(`{}`), "open", now, now,
					5, 2, "high", []string{"bug"}, "fp-1",
					now, now),
		)

	// Mock: repo GetByID.
	repoMock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerRepoColumns).
				AddRow(repoID, orgID, integrationID, int64(12345), "testorg/testrepo", "main",
					false, nil, nil, "https://github.com/testorg/testrepo.git", int64(99),
					"active", nil, nil, json.RawMessage(`{}`), now, now),
		)

	// Create a mock token provider with cached token.
	tokenSvc := &Service{
		cache: make(map[int64]*cachedToken),
	}
	tokenSvc.cache[99] = &cachedToken{
		Token:     "test-installation-token",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		issues:        issueStore,
		repos:         repoStore,
		logger:        zerolog.Nop(),
		baseURL:       server.URL,
		httpClient:    server.Client(),
	}

	err := svc.PushRevision(context.Background(), pr, run)
	require.NoError(t, err, "PushRevision should not return an error for a valid revision")
}

func TestPushRevision_NoDiff(t *testing.T) {
	t.Parallel()

	svc := &PRService{logger: zerolog.Nop()}

	run := &models.Session{ID: uuid.New(), Diff: nil}
	pr := &models.PullRequest{}
	err := svc.PushRevision(context.Background(), pr, run)
	require.Error(t, err, "PushRevision should return an error when diff is nil")
	require.Contains(t, err.Error(), "no diff", "error should mention no diff")
}

func TestPushRevision_WithParentSessionID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	parentID := uuid.New()
	prID := uuid.New()
	issueID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1 +1 @@
+package main`

	diffStr := diff

	run := &models.Session{
		ID:              runID,
		OrgID:           orgID,
		IssueID:         issueID,
		AgentType:       "claude-code",
		Diff:            &diffStr,
		ParentSessionID: &parentID,
	}

	pr := &models.PullRequest{
		ID:             prID,
		OrgID:          orgID,
		GitHubPRNumber: 42,
		GitHubRepo:     "testorg/testrepo",
	}

	// Set up mock GitHub API server.
	var capturedCommitMsg string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/testorg/testrepo/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(map[string]any{
			"head": map[string]string{"sha": "headsha", "ref": "143/fix/branch"},
		})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": "blobsha"})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/git/trees", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]string{"sha": "treesha"})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/git/commits", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err)
		capturedCommitMsg, _ = body["message"].(string)
		w.WriteHeader(http.StatusCreated)
		err = json.NewEncoder(w).Encode(map[string]string{"sha": "commitsha"})
		require.NoError(t, err)
	})
	mux.HandleFunc("PATCH /repos/testorg/testrepo/git/", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(map[string]string{"ref": "updated"})
		require.NoError(t, err)
	})
	mux.HandleFunc("POST /repos/testorg/testrepo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(map[string]any{"id": 1})
		require.NoError(t, err)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	issueMock := newMockPool(t)
	repoMock := newMockPool(t)

	issueMock.ExpectQuery("SELECT .+ FROM issues WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerIssueColumns).
				AddRow(issueID, orgID, "ENG-123", "linear", nil, &repoID,
					"Fix null pointer", nil, json.RawMessage(`{}`), "open", now, now,
					5, 2, "high", []string{}, "fp-1",
					now, now),
		)
	repoMock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerRepoColumns).
				AddRow(repoID, orgID, integrationID, int64(12345), "testorg/testrepo", "main",
					false, nil, nil, "https://github.com/testorg/testrepo.git", int64(99),
					"active", nil, nil, json.RawMessage(`{}`), now, now),
		)

	tokenSvc := &Service{cache: make(map[int64]*cachedToken)}
	tokenSvc.cache[99] = &cachedToken{
		Token:     "test-token",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		issues:        db.NewIssueStore(issueMock),
		repos:         db.NewRepositoryStore(repoMock),
		logger:        zerolog.Nop(),
		baseURL:       server.URL,
		httpClient:    server.Client(),
	}

	err := svc.PushRevision(context.Background(), pr, run)
	require.NoError(t, err, "PushRevision should not return an error with parent session ID")
	require.Contains(t, capturedCommitMsg, parentID.String(), "commit message should reference parent session ID")
}
