package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	automationevents "github.com/assembledhq/143/internal/services/automations"
)

// handlerPRColumns matches the SELECT columns from PullRequestStore.GetByRepoAndNumber
// (internal/db/pull_requests.go). pgxmock requires column names to match the query,
// but order need not match the struct because pgx.RowToStructByName maps by name.
// If the store query changes its SELECT list, update this slice to match.
var handlerPRColumns = []string{
	"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
	"title", "body", "status", "review_status", "authored_by", "ci_status", "head_sha", "head_ref", "base_sha",
	"merge_state", "has_conflicts", "failing_test_count", "needs_agent_action", "github_state_synced_at",
	"health_version", "merge_when_ready_state", "merge_when_ready_requested_by", "merge_when_ready_requested_at",
	"merge_when_ready_head_sha", "merge_when_ready_health_version", "merge_when_ready_error",
	"merge_when_ready_updated_at", "merged_at", "created_at", "updated_at",
}

var handlerPreviewTargetColumns = []string{
	"id", "org_id", "repository_id", "branch", "commit_sha", "preview_config_name",
	"resolved_config_digest", "source_type", "source_id", "source_url",
	"created_by_user_id", "request_id", "preview_group_id", "created_at",
}

var handlerPreviewGroupColumns = []string{
	"id", "org_id", "repository_id", "group_kind", "branch", "preview_config_name",
	"pull_request_number", "source_type", "source_id", "source_url", "current_target_id",
	"latest_commit_sha", "current_status", "pinned", "created_by_user_id", "created_at", "last_activity_at",
}

var handlerPreviewLinkColumns = []string{
	"id", "org_id", "preview_target_id", "link_type", "slug", "repository_id",
	"pr_number", "created_at", "updated_at",
}

var handlerPreviewInstanceColumns = []string{
	"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
	"provider", "worker_node_id", "preview_handle", "primary_service", "port",
	"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
	"last_path", "memory_limit_mb", "cpu_limit_millis", "disk_limit_mb", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
	"source_workspace_revision", "source_workspace_revision_updated_at", "runtime_workspace_revision", "runtime_workspace_revision_updated_at", "runtime_workspace_revision_source", "unavailable_reason", "preview_holding_container",
}

// sessionColumns matches the SELECT columns from SessionStore queries
// (internal/db/session_store.go — sessionSelectColumns). pgx maps by name so
// column order is not critical, but the set of names must match the query.
// If session_store.go changes its SELECT list, update this slice to match.
var sessionColumns = []string{
	"id", "primary_issue_id", "org_id", "origin", "interaction_mode", "validation_policy", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier",
	"container_id", "worker_node_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning", "project_task_id",
	"model_override", "reasoning_effort", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "workspace_generation", "snapshot_key", "pending_snapshot_key", "pending_snapshot_set_at",
	"runtime_soft_deadline_at", "runtime_hard_deadline_at", "runtime_last_progress_at", "runtime_last_progress_type", "runtime_last_progress_strength",
	"runtime_extension_count", "runtime_extension_seconds", "runtime_stop_reason", "runtime_graceful_stop_at",
	"checkpointed_at", "checkpoint_kind", "checkpoint_capability", "checkpoint_size_bytes", "checkpoint_error",
	"recovery_state", "recovery_queued_at", "recovery_started_at", "recovery_attempt_count",
	"target_branch", "working_branch", "base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest", "archived_at", "archived_by_user_id", "automation_run_id", "pr_creation_state", "pr_creation_error", "pr_push_state", "pr_push_error", "branch_creation_state", "branch_creation_error", "branch_url", "diff_collected_at", "latest_diff_snapshot_id", "workspace_revision", "workspace_revision_updated_at", "has_unpushed_changes",
	"linear_private", "linear_state_sync_disabled", "linear_identifier_hint", "linear_prepare_state",
	"deleted_at", "git_identity_source", "git_identity_user_id", "created_at",
}

// newMockPool creates a pgxmock pool and returns it with a cleanup.
func newMockPool(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	t.Cleanup(func() { mock.Close() })
	return mock
}

func handlerPRRow(prID uuid.UUID, sessionID *uuid.UUID, orgID uuid.UUID, repo string, now time.Time) []any {
	return []any{
		prID, sessionID, orgID, 42, "https://github.com/" + repo + "/pull/42", repo,
		"Fix bug", (*string)(nil), "open", "pending", "app", "", nil, nil, nil,
		models.PullRequestMergeStateUnknown, false, 0, false, nil, int64(0),
		models.PullRequestMergeWhenReadyStateOff, (*uuid.UUID)(nil), (*time.Time)(nil), "", (*int64)(nil), "", (*time.Time)(nil),
		(*time.Time)(nil), now, now,
	}
}

func TestNewPRService(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	svc := NewPRService(nil, nil, nil, nil, nil, nil, nil, logger)
	require.NotNil(t, svc, "NewPRService should return a non-nil service")
	require.Equal(t, defaultGitHubAPI, svc.baseURL, "NewPRService should set the default GitHub API base URL")
	require.Equal(t, defaultAppBaseURL, svc.appBaseURL, "NewPRService should set the default app base URL for session deep-links")
	require.NotNil(t, svc.httpClient, "NewPRService should initialize an HTTP client")
}

func TestPRService_PRPreviewURLReturnsStableAppRouteAfterCreatingPreviewMetadata(t *testing.T) {
	t.Parallel()

	mock := newMockPool(t)
	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	targetID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	headSHA := "0123456789abcdef0123456789abcdef01234567"

	mock.ExpectQuery("INSERT INTO preview_targets").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewTargetColumns).
			AddRow(targetID, orgID, repoID, "feature/preview", headSHA, "", "", string(models.PreviewSourceTypePullRequest), "acme/web#42@"+headSHA, "https://github.com/acme/web/pull/42", userID, nil, nil, now))
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("target_created"))
	prNumber := 42
	mock.ExpectQuery("INSERT INTO preview_groups").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewGroupColumns).
			AddRow(uuid.New(), orgID, repoID, models.PreviewGroupKindPullRequest, "feature/preview", "", &prNumber, models.PreviewSourceTypePullRequest, "acme/web#42", "https://github.com/acme/web/pull/42", &targetID, headSHA, "target_created", false, &userID, now, now))
	mock.ExpectExec("UPDATE preview_targets SET preview_group_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO preview_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewLinkColumns).
			AddRow(linkID, orgID, targetID, string(models.PreviewLinkTypePullRequest), "github/acme/web/pull/42", &repoID, ptrToIntValue(42), now, now))
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewInstanceColumns))

	svc := NewPRService(nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
	svc.SetAppBaseURL("https://app.143.dev")
	svc.SetPreviewOriginTemplate("https://{id}.preview.143.dev")
	svc.SetPreviewTeardown(db.NewPreviewStore(mock), nil)

	run := &models.Session{
		ID:                sessionID,
		OrgID:             orgID,
		TriggeredByUserID: &userID,
	}
	repo := &models.Repository{
		ID:       repoID,
		OrgID:    orgID,
		FullName: "acme/web",
	}

	url := svc.prPreviewURL(context.Background(), run, repo, "acme", "web", 42, "feature/preview", headSHA, "https://github.com/acme/web/pull/42")

	require.Equal(t, "https://app.143.dev/previews/github/acme/web/pull/42?launch=1", url, "PR preview URL should use the stable app route even when preview target metadata is created")
	require.NoError(t, mock.ExpectationsWereMet(), "all preview metadata expectations should be met")
}

func ptrToIntValue(value int) *int {
	return &value
}

func TestHandleAutoPreviewEvent_StartsForPolicyEnabledPullRequest(t *testing.T) {
	t.Parallel()

	repoMock := newMockPool(t)
	previewMock := newMockPool(t)
	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	starter := &recordingAutoPreviewStarter{}

	svc := &PRService{
		repos:              db.NewRepositoryStore(repoMock),
		previews:           db.NewPreviewStore(previewMock),
		autoPreviewStarter: starter,
		logger:             zerolog.Nop(),
	}

	repoMock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(githubRepositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, strPtr("go"), strPtr(""), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
	previewMock.ExpectQuery("SELECT .+ FROM repository_preview_policies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(githubRepositoryPreviewPolicyTestCols()).
			AddRow(uuid.New(), orgID, repoID, string(models.PreviewAutoModeWarm), userID, now, now))

	event := autoPreviewPullRequestEvent(orgID)
	err := svc.handleAutoPreviewEvent(context.Background(), event)

	require.NoError(t, err, "auto preview webhook policy handling should not fail")
	require.True(t, starter.called, "auto preview starter should be invoked for enabled policy")
	require.Equal(t, models.PreviewAutoModeWarm, starter.mode, "auto preview starter should receive the configured mode")
	require.Equal(t, "feature", starter.headRef, "auto preview starter should receive the PR head branch")
	require.NoError(t, repoMock.ExpectationsWereMet(), "repository expectations should be met")
	require.NoError(t, previewMock.ExpectationsWereMet(), "preview expectations should be met")
}

func TestHandleAutoPreviewEvent_StartsForReopenedAndSynchronizePullRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action string
	}{
		{name: "reopened", action: "reopened"},
		{name: "synchronize", action: "synchronize"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repoMock := newMockPool(t)
			previewMock := newMockPool(t)
			orgID := uuid.New()
			repoID := uuid.New()
			userID := uuid.New()
			now := time.Now()
			starter := &recordingAutoPreviewStarter{}

			svc := &PRService{
				repos:              db.NewRepositoryStore(repoMock),
				previews:           db.NewPreviewStore(previewMock),
				autoPreviewStarter: starter,
				logger:             zerolog.Nop(),
			}

			repoMock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(githubRepositoryTestCols()).
					AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, strPtr("go"), strPtr(""), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
			previewMock.ExpectQuery("SELECT .+ FROM repository_preview_policies").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(githubRepositoryPreviewPolicyTestCols()).
					AddRow(uuid.New(), orgID, repoID, string(models.PreviewAutoModeOn), userID, now, now))

			event := autoPreviewPullRequestEvent(orgID)
			event.Action = tt.action
			err := svc.handleAutoPreviewEvent(context.Background(), event)

			require.NoError(t, err, "eligible auto preview webhook action should not fail")
			require.True(t, starter.called, "auto preview starter should be invoked for eligible PR action")
			require.Equal(t, models.PreviewAutoModeOn, starter.mode, "auto preview starter should receive the configured mode")
			require.NoError(t, repoMock.ExpectationsWereMet(), "repository expectations should be met")
			require.NoError(t, previewMock.ExpectationsWereMet(), "preview expectations should be met")
		})
	}
}

func TestHandleAutoPreviewEvent_SkipsDraftAndForkPullRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*PullRequestEvent)
	}{
		{name: "draft", mutate: func(e *PullRequestEvent) { e.PR.Draft = true }},
		{name: "fork", mutate: func(e *PullRequestEvent) { e.PR.Head.Repo.Fork = true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			starter := &recordingAutoPreviewStarter{}
			svc := &PRService{
				repos:              db.NewRepositoryStore(newMockPool(t)),
				previews:           db.NewPreviewStore(newMockPool(t)),
				autoPreviewStarter: starter,
				logger:             zerolog.Nop(),
			}
			orgID := uuid.New()
			event := autoPreviewPullRequestEvent(orgID)
			tt.mutate(&event)

			err := svc.handleAutoPreviewEvent(context.Background(), event)

			require.NoError(t, err, "unsafe auto preview webhook should be skipped without error")
			require.False(t, starter.called, "auto preview starter should not be invoked for skipped PRs")
		})
	}
}

func TestHandleAutoPreviewEvent_SkipsDisabledPolicyAndNonDefaultBranch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*PullRequestEvent)
		setupMocks func(t *testing.T, orgID uuid.UUID, repoMock, previewMock pgxmock.PgxPoolIface)
	}{
		{
			name:   "disabled policy",
			mutate: func(*PullRequestEvent) {},
			setupMocks: func(t *testing.T, orgID uuid.UUID, repoMock, previewMock pgxmock.PgxPoolIface) {
				t.Helper()
				repoID := uuid.New()
				now := time.Now()
				repoMock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(githubRepositoryTestCols()).
						AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, strPtr("go"), strPtr(""), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
				previewMock.ExpectQuery("SELECT .+ FROM repository_preview_policies").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(githubRepositoryPreviewPolicyTestCols()).
						AddRow(uuid.New(), orgID, repoID, string(models.PreviewAutoModeOff), uuid.New(), now, now))
			},
		},
		{
			name: "non-default base branch",
			mutate: func(e *PullRequestEvent) {
				e.PR.Base.Ref = "release"
			},
			setupMocks: func(*testing.T, uuid.UUID, pgxmock.PgxPoolIface, pgxmock.PgxPoolIface) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repoMock := newMockPool(t)
			previewMock := newMockPool(t)
			starter := &recordingAutoPreviewStarter{}
			svc := &PRService{
				repos:              db.NewRepositoryStore(repoMock),
				previews:           db.NewPreviewStore(previewMock),
				autoPreviewStarter: starter,
				logger:             zerolog.Nop(),
			}
			orgID := uuid.New()
			event := autoPreviewPullRequestEvent(orgID)
			tt.mutate(&event)
			tt.setupMocks(t, orgID, repoMock, previewMock)

			err := svc.handleAutoPreviewEvent(context.Background(), event)

			require.NoError(t, err, "skipped auto preview webhook should not fail")
			require.False(t, starter.called, "auto preview starter should not be invoked for skipped PRs")
			require.NoError(t, repoMock.ExpectationsWereMet(), "repository expectations should be met")
			require.NoError(t, previewMock.ExpectationsWereMet(), "preview expectations should be met")
		})
	}
}

type recordingAutoPreviewStarter struct {
	called  bool
	mode    models.PreviewAutoMode
	headRef string
}

func (s *recordingAutoPreviewStarter) StartAutoPullRequestPreview(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ models.Repository, _ int, headRef, _ string, _ string, mode models.PreviewAutoMode) error {
	s.called = true
	s.mode = mode
	s.headRef = headRef
	return nil
}

func autoPreviewPullRequestEvent(orgID uuid.UUID) PullRequestEvent {
	event := PullRequestEvent{
		Action:     "opened",
		Number:     42,
		OwnerOrgID: &orgID,
	}
	event.Repository.ID = 123
	event.Repository.FullName = "acme/app"
	event.Repository.DefaultBranch = "main"
	event.PR.HTMLURL = "https://github.com/acme/app/pull/42"
	event.PR.Head.SHA = "0123456789012345678901234567890123456789"
	event.PR.Head.Ref = "feature"
	event.PR.Head.Repo.FullName = "acme/app"
	event.PR.Base.Ref = "main"
	return event
}

func githubRepositoryPreviewPolicyTestCols() []string {
	return []string{"id", "org_id", "repository_id", "auto_mode", "updated_by_user_id", "created_at", "updated_at"}
}

func githubRepositoryTestCols() []string {
	return []string{
		"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description",
		"clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at",
	}
}

type recordingAutomationEventTriggerer struct {
	calls []automationevents.GitHubEventTriggerRequest
}

func (r *recordingAutomationEventTriggerer) TriggerGitHubEvent(_ context.Context, req automationevents.GitHubEventTriggerRequest) error {
	r.calls = append(r.calls, req)
	return nil
}

func expectGitHubAutomationRepositoryLookup(mock pgxmock.PgxPoolIface, orgID, repoID uuid.UUID, githubRepoID int64, fullName string) {
	now := time.Now()
	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": fullName}).
		WillReturnRows(pgxmock.NewRows(githubRepositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), githubRepoID, fullName, "main", false, strPtr("go"), strPtr(""),
				"https://github.com/"+fullName+".git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
}

func expectUnknownPullRequestLookup(mock pgxmock.PgxPoolIface, orgID uuid.UUID, fullName string, number int) {
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": fullName, "github_pr_number": number}).
		WillReturnError(pgx.ErrNoRows)
}

func TestPRService_TriggersGitHubEventAutomationsFromWebhooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		event     models.AutomationGitHubEvent
		withPR    bool
		run       func(*testing.T, *PRService, uuid.UUID, int64, string)
		wantActor string
		wantBody  string
	}{
		{
			name:   "pull request opened",
			event:  models.AutomationGitHubEventPullRequestOpened,
			withPR: true,
			run: func(t *testing.T, svc *PRService, orgID uuid.UUID, githubRepoID int64, fullName string) {
				t.Helper()
				event := PullRequestEvent{Action: "opened", Number: 42, OwnerOrgID: &orgID}
				event.Sender.Login = "octocat"
				event.Repository.ID = githubRepoID
				event.Repository.FullName = fullName
				event.PR.HTMLURL = "https://github.com/acme/app/pull/42"
				event.PR.Title = "Add checkout"
				event.PR.Body = "Please review"
				require.NoError(t, svc.HandlePullRequestEvent(context.Background(), event), "opened PR webhook should not fail")
			},
			wantActor: "octocat",
			wantBody:  "Add checkout\n\nPlease review",
		},
		{
			name:  "issue comment on pull request",
			event: models.AutomationGitHubEventIssueCommentCreated,
			run: func(t *testing.T, svc *PRService, orgID uuid.UUID, githubRepoID int64, fullName string) {
				t.Helper()
				event := IssueCommentEvent{Action: "created", OwnerOrgID: &orgID}
				event.Sender.Login = "commenter"
				event.Repository.ID = githubRepoID
				event.Repository.FullName = fullName
				event.Issue.Number = 42
				event.Issue.PullRequest = &struct{}{}
				event.Comment.Body = "Can you handle this?"
				require.NoError(t, svc.HandleIssueCommentEvent(context.Background(), event), "PR issue_comment webhook should not fail")
			},
			wantActor: "commenter",
			wantBody:  "Can you handle this?",
		},
		{
			name:   "pull request review submitted",
			event:  models.AutomationGitHubEventPullRequestReviewSubmitted,
			withPR: true,
			run: func(t *testing.T, svc *PRService, orgID uuid.UUID, githubRepoID int64, fullName string) {
				t.Helper()
				event := PullRequestReviewEvent{Action: "submitted", OwnerOrgID: &orgID}
				event.Sender.Login = "reviewer"
				event.Repository.ID = githubRepoID
				event.Repository.FullName = fullName
				event.PullRequest.Number = 42
				event.Review.State = "commented"
				event.Review.Body = "Looks close"
				require.NoError(t, svc.HandlePullRequestReviewEvent(context.Background(), event), "review webhook should not fail")
			},
			wantActor: "reviewer",
			wantBody:  "Looks close",
		},
		{
			name:   "inline pull request review comment",
			event:  models.AutomationGitHubEventPullRequestReviewCommentCreated,
			withPR: true,
			run: func(t *testing.T, svc *PRService, orgID uuid.UUID, githubRepoID int64, fullName string) {
				t.Helper()
				event := PullRequestReviewCommentEvent{Action: "created", OwnerOrgID: &orgID}
				event.Sender.Login = "inline-reviewer"
				event.Repository.ID = githubRepoID
				event.Repository.FullName = fullName
				event.PullRequest.Number = 42
				event.Comment.Body = "Please adjust this line"
				require.NoError(t, svc.HandlePullRequestReviewCommentEvent(context.Background(), event), "review comment webhook should not fail")
			},
			wantActor: "inline-reviewer",
			wantBody:  "Please adjust this line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			repoID := uuid.New()
			githubRepoID := int64(123)
			fullName := "acme/app"
			repoMock := newMockPool(t)
			prMock := newMockPool(t)
			triggerer := &recordingAutomationEventTriggerer{}
			expectGitHubAutomationRepositoryLookup(repoMock, orgID, repoID, githubRepoID, fullName)
			if tt.withPR {
				expectUnknownPullRequestLookup(prMock, orgID, fullName, 42)
			}

			svc := &PRService{
				repos:                   db.NewRepositoryStore(repoMock),
				pullRequests:            db.NewPullRequestStore(prMock),
				automationEventTriggers: triggerer,
				logger:                  zerolog.Nop(),
			}

			tt.run(t, svc, orgID, githubRepoID, fullName)

			require.Len(t, triggerer.calls, 1, "webhook should trigger exactly one automation event")
			require.Equal(t, tt.event, triggerer.calls[0].Event, "webhook should map to the expected automation event")
			require.Equal(t, orgID, triggerer.calls[0].OrgID, "automation trigger should use the resolved repository org")
			require.Equal(t, repoID, triggerer.calls[0].RepositoryID, "automation trigger should use the resolved repository id")
			require.Equal(t, fullName, triggerer.calls[0].Repository, "automation trigger should include repository name")
			require.Equal(t, 42, triggerer.calls[0].PullRequestNumber, "automation trigger should include PR number")
			require.Equal(t, tt.wantActor, triggerer.calls[0].Actor, "automation trigger should include GitHub actor")
			require.Equal(t, tt.wantBody, triggerer.calls[0].Body, "automation trigger should include event text")
			require.NoError(t, repoMock.ExpectationsWereMet(), "repository expectations should be met")
			require.NoError(t, prMock.ExpectationsWereMet(), "pull request expectations should be met")
		})
	}
}

func TestSetBaseURL(t *testing.T) {
	t.Parallel()

	svc := &PRService{}
	svc.SetBaseURL("https://custom.api.example.com")
	require.Equal(t, "https://custom.api.example.com", svc.baseURL, "SetBaseURL should update the base URL")
}

func TestSetAppBaseURL(t *testing.T) {
	t.Parallel()

	svc := &PRService{}
	svc.SetAppBaseURL("https://frontend.example.com/")
	require.Equal(t, "https://frontend.example.com", svc.appBaseURL, "SetAppBaseURL should trim the trailing slash from the app base URL")
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

func TestSetIntegrationStore(t *testing.T) {
	t.Parallel()

	svc := &PRService{}
	require.Nil(t, svc.integrations, "integrations should be nil initially")

	mockPool := newMockPool(t)
	store := db.NewIntegrationStore(mockPool)
	svc.SetIntegrationStore(store)
	require.NotNil(t, svc.integrations, "SetIntegrationStore should set the integration store")
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
		sessions:     sessionStore,
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
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
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
				AddRow(sessionID, &issueID, orgID, "", "", "", "claude-code", "completed", "full", "low",
					nil,
					nil, nil, false, nil, nil, nil,
					nil, nil, nil, false,
					nil, nil, nil, nil, nil,
					nil, nil, nil, nil, nil,
					nil,                                // model_override
					nil,                                // reasoning_effort
					nil,                                // triggered_by_user_id
					nil, 0, now, "none", int64(0), nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, workspace_generation, snapshot_key
					nil,      // pending_snapshot_key
					nil,      // pending_snapshot_set_at
					nil,      // runtime_soft_deadline_at
					nil,      // runtime_hard_deadline_at
					nil,      // runtime_last_progress_at
					"",       // runtime_last_progress_type
					"",       // runtime_last_progress_strength
					0,        // runtime_extension_count
					0,        // runtime_extension_seconds
					"",       // runtime_stop_reason
					nil,      // runtime_graceful_stop_at
					nil,      // checkpointed_at
					"",       // checkpoint_kind
					"",       // checkpoint_capability
					int64(0), // checkpoint_size_bytes
					nil,      // checkpoint_error
					"",       // recovery_state
					nil,      // recovery_queued_at
					nil,      // recovery_started_at
					0,        // recovery_attempt_count
					nil,      // target_branch
					nil,      // working_branch
					nil,      // base_commit_sha
					nil,      // repository_id
					nil,      // diff_stats
					nil,      // diff_history
					nil,      // input_manifest
					nil, nil, // archived_at, archived_by_user_id
					nil,                           // automation_run_id
					"idle",                        // pr_creation_state
					(*string)(nil),                // pr_creation_error
					"idle",                        // pr_push_state
					(*string)(nil),                // pr_push_error
					"idle",                        // branch_creation_state
					(*string)(nil),                // branch_creation_error
					(*string)(nil),                // branch_url
					nil,                           // diff_collected_at
					nil,                           // latest_diff_snapshot_id
					int64(0),                      // workspace_revision
					now,                           // workspace_revision_updated_at
					false,                         // has_unpushed_changes
					false,                         // linear_private
					false,                         // linear_state_sync_disabled
					(*string)(nil),                // linear_identifier_hint
					models.LinearPrepareStateNone, // linear_prepare_state
					nil,                           // deleted_at
					nil,                           // git_identity_source
					nil,                           // git_identity_user_id
					now),
		)

	// Mock: UpdateStatus for issue.
	issueMock.ExpectExec("UPDATE issues SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Mock: no existing deploy, then create deploy.
	deployMock.ExpectQuery("SELECT id, pull_request_id, org_id, environment, deployed_at, commit_sha, created_at\n\t\tFROM deploys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "pull_request_id", "org_id", "environment", "deployed_at", "commit_sha", "created_at"}),
		)
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

func TestHandlePullRequestEvent_MergedWithNilSessionID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	deployMock := newMockPool(t)
	jobMock := newMockPool(t)

	prStore := db.NewPullRequestStore(prMock)
	deployStore := db.NewDeployStore(deployMock)
	jobStore := db.NewJobStore(jobMock)

	svc := &PRService{
		pullRequests: prStore,
		sessions:     db.NewSessionStore(newMockPool(t)),
		issues:       db.NewIssueStore(newMockPool(t)),
		deploys:      deployStore,
		jobs:         jobStore,
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR with nil session_id.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, (*uuid.UUID)(nil), orgID, "testorg/testrepo", now)...),
		)

	// Mock: UpdateStatus to merged.
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Mock: no existing deploy, then create deploy.
	deployMock.ExpectQuery("SELECT id, pull_request_id, org_id, environment, deployed_at, commit_sha, created_at\n\t\tFROM deploys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "pull_request_id", "org_id", "environment", "deployed_at", "commit_sha", "created_at"}),
		)
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
	require.NoError(t, err, "HandlePullRequestEvent should not return an error for a merged PR with nil session_id")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR store expectations should be met")
	require.NoError(t, deployMock.ExpectationsWereMet(), "all deploy store expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "all job store expectations should be met")
}

// TestHandlePullRequestEvent_MergedPrefersMergeCommitSHA confirms the webhook
// path threads the merge commit SHA through to the deploy row and the
// evaluate_experiment job, matching what the API merge path emits. Without
// this preference, squash/rebase merges would record the pre-merge head SHA
// in deploys.commit_sha — a different commit than what's actually on main.
func TestHandlePullRequestEvent_MergedPrefersMergeCommitSHA(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	now := time.Now()
	mergeCommitSHA := "merge-commit-abc"

	prMock := newMockPool(t)
	deployMock := newMockPool(t)
	jobMock := newMockPool(t)

	svc := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		sessions:     db.NewSessionStore(newMockPool(t)),
		issues:       db.NewIssueStore(newMockPool(t)),
		deploys:      db.NewDeployStore(deployMock),
		jobs:         db.NewJobStore(jobMock),
		logger:       zerolog.Nop(),
	}

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, (*uuid.UUID)(nil), orgID, "testorg/testrepo", now)...),
		)
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	deployMock.ExpectQuery("SELECT id, pull_request_id, org_id, environment, deployed_at, commit_sha, created_at\n\t\tFROM deploys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "pull_request_id", "org_id", "environment", "deployed_at", "commit_sha", "created_at"}))
	// Assert the deploy insert receives the merge commit SHA, not the head.
	deployMock.ExpectQuery("INSERT INTO deploys").
		WithArgs(pgx.NamedArgs{
			"pull_request_id": prID,
			"org_id":          orgID,
			"environment":     "production",
			"commit_sha":      &mergeCommitSHA,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "deployed_at", "created_at"}).AddRow(uuid.New(), now, now))

	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	event := PullRequestEvent{Action: "closed", Number: 42}
	event.PR.Merged = true
	event.PR.Head.SHA = "head-pre-merge"
	event.PR.MergeCommitSHA = mergeCommitSHA
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestEvent should not return an error for a merged PR")
	require.NoError(t, deployMock.ExpectationsWereMet(), "deploy insert should receive the merge commit SHA, not the head SHA")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR store expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "all job store expectations should be met")
}

func TestHandlePullRequestEvent_MergedFallsBackToHeadSHAWhenMergeCommitMissing(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	now := time.Now()
	headSHA := "head-fallback-abc"

	prMock := newMockPool(t)
	deployMock := newMockPool(t)
	jobMock := newMockPool(t)

	svc := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		sessions:     db.NewSessionStore(newMockPool(t)),
		issues:       db.NewIssueStore(newMockPool(t)),
		deploys:      db.NewDeployStore(deployMock),
		jobs:         db.NewJobStore(jobMock),
		logger:       zerolog.Nop(),
	}

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, (*uuid.UUID)(nil), orgID, "testorg/testrepo", now)...),
		)
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	deployMock.ExpectQuery("SELECT id, pull_request_id, org_id, environment, deployed_at, commit_sha, created_at\n\t\tFROM deploys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "pull_request_id", "org_id", "environment", "deployed_at", "commit_sha", "created_at"}))
	deployMock.ExpectQuery("INSERT INTO deploys").
		WithArgs(pgx.NamedArgs{
			"pull_request_id": prID,
			"org_id":          orgID,
			"environment":     "production",
			"commit_sha":      &headSHA,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "deployed_at", "created_at"}).AddRow(uuid.New(), now, now))

	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	event := PullRequestEvent{Action: "closed", Number: 42}
	event.PR.Merged = true
	event.PR.Head.SHA = headSHA
	event.PR.MergeCommitSHA = ""
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestEvent should not return an error for a merged PR with no merge_commit_sha")
	require.NoError(t, deployMock.ExpectationsWereMet(), "deploy insert should fall back to the head SHA when merge_commit_sha is empty")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR store expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "all job store expectations should be met")
}

func TestHandlePullRequestEvent_ClosedWithoutMergeFlow(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	jobMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests: prStore,
		jobs:         db.NewJobStore(jobMock),
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)

	// Mock: UpdateStatus to closed.
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	dedupeKey := pullRequestStateSyncDedupeKey(prID, "")
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgx.NamedArgs{
			"org_id":     orgID,
			"queue":      prHealthSyncQueue,
			"job_type":   prHealthSyncJobType,
			"payload":    pgxmock.AnyArg(),
			"priority":   6,
			"dedupe_key": &dedupeKey,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	event := PullRequestEvent{
		Action: "closed",
		Number: 42,
	}
	event.PR.Merged = false
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestEvent should not return an error for a closed-without-merge PR")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR store expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "closed pull request webhooks should enqueue a state sync with the generic dedupe key")
}

func TestHandlePullRequestEvent_ClosedWithoutMergeReturnsStatusUpdateError(t *testing.T) {
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

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("update failed"))

	event := PullRequestEvent{Action: "closed", Number: 42}
	event.PR.Merged = false
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.Error(t, err, "HandlePullRequestEvent should return status update failures for closed PRs")
	require.Contains(t, err.Error(), "update PR status to closed", "HandlePullRequestEvent should wrap the closed-status update error")
	require.NoError(t, prMock.ExpectationsWereMet(), "all pull request expectations should be met")
}

// organizationColumns matches OrganizationStore.GetByID's SELECT list.
var organizationColumns = []string{"id", "name", "settings", "created_at", "updated_at"}

func TestHandlePullRequestEvent_AutoArchiveOnCloseWhenEnabled(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	sessionMock := newMockPool(t)
	orgMock := newMockPool(t)

	svc := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		sessions:     db.NewSessionStore(sessionMock),
		orgs:         db.NewOrganizationStore(orgMock),
		logger:       zerolog.Nop(),
	}

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	orgMock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(organizationColumns).
				AddRow(orgID, "Test Org", json.RawMessage(`{"auto_archive_on_pr_close": true}`), now, now),
		)
	sessionMock.ExpectExec("UPDATE sessions SET archived_at = now\\(\\), archived_by_user_id = NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	event := PullRequestEvent{
		Action: "closed",
		Number: 42,
	}
	event.PR.Merged = false
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err)
	require.NoError(t, prMock.ExpectationsWereMet())
	require.NoError(t, orgMock.ExpectationsWereMet(), "org settings should be fetched")
	require.NoError(t, sessionMock.ExpectationsWereMet(), "session should be archived")
}

func TestHandlePullRequestEvent_AutoArchiveSkippedWhenDisabled(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	sessionMock := newMockPool(t)
	orgMock := newMockPool(t)

	svc := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		sessions:     db.NewSessionStore(sessionMock),
		orgs:         db.NewOrganizationStore(orgMock),
		logger:       zerolog.Nop(),
	}

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	orgMock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(organizationColumns).
				AddRow(orgID, "Test Org", json.RawMessage(`{}`), now, now),
		)
	// No session archive expected — pgxmock.ExpectationsWereMet passes only if
	// no unmocked calls were made, but it does not fail on un-called expectations
	// that we never set. Leaving sessionMock with zero expectations asserts that
	// ArchiveSystem was not invoked.

	event := PullRequestEvent{
		Action: "closed",
		Number: 42,
	}
	event.PR.Merged = false
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err)
	require.NoError(t, prMock.ExpectationsWereMet())
	require.NoError(t, orgMock.ExpectationsWereMet())
	require.NoError(t, sessionMock.ExpectationsWereMet(), "no session archive should happen when toggle is off")
}

func TestPRServiceMaybeAutoArchiveSessionOnPRCloseHandlesOrgAndArchiveFailures(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()
	pr := models.PullRequest{
		ID:             prID,
		OrgID:          orgID,
		SessionID:      &sessionID,
		GitHubRepo:     "testorg/testrepo",
		GitHubPRNumber: 42,
	}

	tests := []struct {
		name         string
		setupOrg     func(mock pgxmock.PgxPoolIface)
		setupSession func(mock pgxmock.PgxPoolIface)
	}{
		{
			name: "returns when org lookup fails",
			setupOrg: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(errors.New("org lookup failed"))
			},
			setupSession: func(pgxmock.PgxPoolIface) {},
		},
		{
			name: "returns when org settings cannot be parsed",
			setupOrg: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(organizationColumns).
							AddRow(orgID, "Test Org", []byte(`{"auto_archive_on_pr_close":`), now, now),
					)
			},
			setupSession: func(pgxmock.PgxPoolIface) {},
		},
		{
			name: "returns when archiving the session fails",
			setupOrg: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(organizationColumns).
							AddRow(orgID, "Test Org", json.RawMessage(`{"auto_archive_on_pr_close": true}`), now, now),
					)
			},
			setupSession: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE sessions SET archived_at = now\\(\\), archived_by_user_id = NULL").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("archive failed"))
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgMock := newMockPool(t)
			sessionMock := newMockPool(t)

			service := &PRService{
				orgs:     db.NewOrganizationStore(orgMock),
				sessions: db.NewSessionStore(sessionMock),
				logger:   zerolog.Nop(),
			}

			tt.setupOrg(orgMock)
			tt.setupSession(sessionMock)

			service.maybeAutoArchiveSessionOnPRClose(context.Background(), pr, nil, false)

			require.NoError(t, orgMock.ExpectationsWereMet(), "organization expectations should be met")
			require.NoError(t, sessionMock.ExpectationsWereMet(), "session expectations should be met")
		})
	}
}

func TestPRServiceMaybeAutoArchiveSessionOnPRCloseHandlesSnapshotFailures(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()
	pr := models.PullRequest{
		ID:             prID,
		OrgID:          orgID,
		SessionID:      &sessionID,
		GitHubRepo:     "testorg/testrepo",
		GitHubPRNumber: 42,
	}

	tests := []struct {
		name         string
		setupSession func(mock pgxmock.PgxPoolIface)
		snapshotErr  error
	}{
		{
			name: "logs when reloading the snapshot key fails",
			setupSession: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE sessions SET archived_at = now\\(\\), archived_by_user_id = NULL").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("session load failed"))
			},
		},
		{
			name: "logs when snapshot cleanup fails",
			setupSession: func(mock pgxmock.PgxPoolIface) {
				snapshotKey := "snap-key"
				row := newPRHealthSessionRow(sessionID, orgID, now, models.SessionStatusCompleted)
				setPRHealthSessionRowValue(row, "snapshot_key", &snapshotKey)

				mock.ExpectExec("UPDATE sessions SET archived_at = now\\(\\), archived_by_user_id = NULL").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(prHealthSessionColumns).AddRow(row...))
			},
			snapshotErr: errors.New("snapshot delete failed"),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgMock := newMockPool(t)
			sessionMock := newMockPool(t)

			orgMock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
				WithArgs(pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(organizationColumns).
						AddRow(orgID, "Test Org", json.RawMessage(`{"auto_archive_on_pr_close": true}`), now, now),
				)

			snapshotStore := &prTestSnapshotStore{deleteErr: tt.snapshotErr}
			service := &PRService{
				orgs:      db.NewOrganizationStore(orgMock),
				sessions:  db.NewSessionStore(sessionMock),
				snapshots: snapshotStore,
				logger:    zerolog.Nop(),
			}

			tt.setupSession(sessionMock)
			service.maybeAutoArchiveSessionOnPRClose(context.Background(), pr, nil, false)

			require.NoError(t, orgMock.ExpectationsWereMet(), "organization expectations should be met")
			require.NoError(t, sessionMock.ExpectationsWereMet(), "session expectations should be met")
		})
	}
}

func TestPRServiceRunMergedPullRequestFollowUpsHandlesWarningPaths(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	tests := []struct {
		name         string
		setupSession func(mock pgxmock.PgxPoolIface)
		setupIssue   func(mock pgxmock.PgxPoolIface)
		setupDeploy  func(mock pgxmock.PgxPoolIface)
		snapshotErr  error
	}{
		{
			name: "continues when session loading fails",
			setupSession: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("session load failed"))
			},
			setupIssue: func(pgxmock.PgxPoolIface) {},
			setupDeploy: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT id, pull_request_id, org_id, environment, deployed_at, commit_sha, created_at\n\t\tFROM deploys").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "pull_request_id", "org_id", "environment", "deployed_at", "commit_sha", "created_at"}))
				mock.ExpectQuery("INSERT INTO deploys").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "deployed_at", "created_at"}).AddRow(uuid.New(), now, now))
			},
		},
		{
			name: "continues when issue, snapshot, and deploy creation fail",
			setupSession: func(mock pgxmock.PgxPoolIface) {
				issueID := uuid.New()
				snapshotKey := "snap-key"
				sessionRow := newPRHealthSessionRow(sessionID, orgID, now, models.SessionStatusCompleted)
				setPRHealthSessionRowValue(sessionRow, "primary_issue_id", &issueID)
				setPRHealthSessionRowValue(sessionRow, "agent_type", "claude-code")
				setPRHealthSessionRowValue(sessionRow, "autonomy_level", "full")
				setPRHealthSessionRowValue(sessionRow, "sandbox_state", "snapshot")
				setPRHealthSessionRowValue(sessionRow, "snapshot_key", &snapshotKey)

				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).AddRow(sessionRow...),
					)
			},
			setupIssue: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE issues SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("issue update failed"))
			},
			setupDeploy: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT id, pull_request_id, org_id, environment, deployed_at, commit_sha, created_at\n\t\tFROM deploys").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "pull_request_id", "org_id", "environment", "deployed_at", "commit_sha", "created_at"}))
				mock.ExpectQuery("INSERT INTO deploys").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("deploy create failed"))
			},
			snapshotErr: errors.New("snapshot delete failed"),
		},
		{
			name:         "continues when deploy lookup fails",
			setupSession: func(pgxmock.PgxPoolIface) {},
			setupIssue:   func(pgxmock.PgxPoolIface) {},
			setupDeploy: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT id, pull_request_id, org_id, environment, deployed_at, commit_sha, created_at\n\t\tFROM deploys").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("deploy lookup failed"))
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sessionMock := newMockPool(t)
			issueMock := newMockPool(t)
			deployMock := newMockPool(t)

			service := &PRService{
				sessions:  db.NewSessionStore(sessionMock),
				issues:    db.NewIssueStore(issueMock),
				deploys:   db.NewDeployStore(deployMock),
				snapshots: &prTestSnapshotStore{deleteErr: tt.snapshotErr},
				logger:    zerolog.Nop(),
			}

			pr := models.PullRequest{
				ID:         prID,
				OrgID:      orgID,
				SessionID:  &sessionID,
				GitHubRepo: "testorg/testrepo",
			}

			tt.setupSession(sessionMock)
			tt.setupIssue(issueMock)
			tt.setupDeploy(deployMock)

			service.runMergedPullRequestFollowUps(context.Background(), pr, "commit-sha")

			require.NoError(t, sessionMock.ExpectationsWereMet(), "session expectations should be met")
			require.NoError(t, issueMock.ExpectationsWereMet(), "issue expectations should be met")
			require.NoError(t, deployMock.ExpectationsWereMet(), "deploy expectations should be met")
		})
	}
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
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)

	event := PullRequestEvent{
		Action: "opened",
		Number: 42,
	}
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestEvent should ignore non-closed actions")
}

func TestHandlePullRequestEvent_OpenedEnqueuesHealthSync(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	jobMock := newMockPool(t)

	svc := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		jobs:         db.NewJobStore(jobMock),
		logger:       zerolog.Nop(),
	}

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	event := PullRequestEvent{Action: "opened", Number: 42}
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestEvent should enqueue a health sync for opened PRs")
	require.NoError(t, prMock.ExpectationsWereMet(), "all pull request expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "all job expectations should be met")
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
	jobMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)
	jobStore := db.NewJobStore(jobMock)

	svc := &PRService{
		pullRequests: prStore,
		jobs:         jobStore,
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)

	// Mock: UpdateReviewStatus.
	prMock.ExpectExec("UPDATE pull_requests SET review_status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Mock: Enqueue reinforce_memories job (triggered on approval).
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()),
		)

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
	require.NoError(t, jobMock.ExpectationsWereMet(), "all job store expectations should be met")
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
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
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
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
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
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
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
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
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
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
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
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
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

func TestListBranches_Success(t *testing.T) {
	t.Parallel()

	branches := []GitHubBranch{
		{Name: "main", Protected: true},
		{Name: "develop", Protected: false},
		{Name: "feature/foo", Protected: false},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method, "ListBranches should use GET")
		require.Contains(t, r.URL.Path, "/repos/owner/repo/branches", "request path should target branches endpoint")
		require.Equal(t, "token test-token", r.Header.Get("Authorization"), "should send authorization header")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(branches)
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	result, err := svc.ListBranches(context.Background(), "test-token", "owner", "repo")
	require.NoError(t, err, "ListBranches should not return an error")
	require.Len(t, result, 3, "should return all branches")
	require.Equal(t, "main", result[0].Name, "first branch should be main")
	require.True(t, result[0].Protected, "main branch should be protected")
	require.Equal(t, "feature/foo", result[2].Name, "third branch should be feature/foo")
}

func TestListBranches_Pagination(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			// Return exactly 100 branches to trigger pagination.
			branches := make([]GitHubBranch, 100)
			for i := range branches {
				branches[i] = GitHubBranch{Name: fmt.Sprintf("branch-%d", i)}
			}
			json.NewEncoder(w).Encode(branches)
		} else {
			// Second page returns fewer than 100.
			branches := []GitHubBranch{{Name: "last-branch"}}
			json.NewEncoder(w).Encode(branches)
		}
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	result, err := svc.ListBranches(context.Background(), "test-token", "owner", "repo")
	require.NoError(t, err, "ListBranches should not return an error")
	require.Len(t, result, 101, "should return all branches across pages")
	require.Equal(t, 2, callCount, "should make exactly 2 API calls")
	require.Equal(t, "last-branch", result[100].Name, "last branch should be from second page")
}

func TestListBranches_APIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"internal error"}`))
	}))
	defer server.Close()

	svc := &PRService{
		baseURL:    server.URL,
		httpClient: server.Client(),
		logger:     zerolog.Nop(),
	}

	result, err := svc.ListBranches(context.Background(), "test-token", "owner", "repo")
	require.Error(t, err, "ListBranches should return an error on API failure")
	require.Nil(t, result, "result should be nil on error")
	require.Contains(t, err.Error(), "list branches", "error should include context")
}

func TestGetInstallationToken_DelegatesToTokenProvider(t *testing.T) {
	t.Parallel()

	tokenSvc := &Service{
		cache: make(map[int64]*cachedToken),
	}
	tokenSvc.cache[42] = &cachedToken{
		Token:     "cached-install-token",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}

	svc := &PRService{
		tokenProvider: tokenSvc,
		logger:        zerolog.Nop(),
	}

	token, err := svc.GetInstallationToken(context.Background(), 42)
	require.NoError(t, err, "GetInstallationToken should not return an error")
	require.Equal(t, "cached-install-token", token, "should return the cached token")
}

func TestHandleCheckSuiteEvent_NonCompleted(t *testing.T) {
	t.Parallel()

	svc := &PRService{logger: zerolog.Nop()}

	event := CheckSuiteEvent{Action: "requested"}
	err := svc.HandleCheckSuiteEvent(context.Background(), event)
	require.NoError(t, err, "should ignore non-completed events")
}

func TestHandleCheckSuiteEvent_Success(t *testing.T) {
	t.Parallel()

	prMock := newMockPool(t)
	jobMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	svc := &PRService{
		pullRequests: prStore,
		jobs:         db.NewJobStore(jobMock),
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)

	// Mock: UpdateCIStatus.
	prMock.ExpectExec("UPDATE pull_requests SET ci_status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	dedupeKey := pullRequestStateSyncDedupeKey(prID, "check_suite_completed")
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgx.NamedArgs{
			"org_id":     orgID,
			"queue":      prHealthSyncQueue,
			"job_type":   prHealthSyncJobType,
			"payload":    pgxmock.AnyArg(),
			"priority":   6,
			"dedupe_key": &dedupeKey,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	conclusion := "success"
	event := CheckSuiteEvent{Action: "completed"}
	event.CheckSuite.Conclusion = &conclusion
	event.CheckSuite.PullRequests = []struct {
		Number int `json:"number"`
	}{{Number: 42}}
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandleCheckSuiteEvent(context.Background(), event)
	require.NoError(t, err, "should process check suite event without error")
	require.NoError(t, prMock.ExpectationsWereMet(), "all database expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "check suite completion should enqueue a scoped health sync")
}

func TestHandleCheckSuiteEvent_Failure(t *testing.T) {
	t.Parallel()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	svc := &PRService{
		pullRequests: prStore,
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns a PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)

	// Mock: UpdateCIStatus with failure.
	prMock.ExpectExec("UPDATE pull_requests SET ci_status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	conclusion := "failure"
	event := CheckSuiteEvent{Action: "completed"}
	event.CheckSuite.Conclusion = &conclusion
	event.CheckSuite.PullRequests = []struct {
		Number int `json:"number"`
	}{{Number: 42}}
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandleCheckSuiteEvent(context.Background(), event)
	require.NoError(t, err, "should process check suite failure event without error")
	require.NoError(t, prMock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestHandleCheckRunEvent_CompletedEnqueuesHealthSync(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	jobMock := newMockPool(t)
	svc := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		jobs:         db.NewJobStore(jobMock),
		logger:       zerolog.Nop(),
	}

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)
	dedupeKey := pullRequestStateSyncDedupeKey(prID, "check_run_completed")
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgx.NamedArgs{
			"org_id":     orgID,
			"queue":      prHealthSyncQueue,
			"job_type":   prHealthSyncJobType,
			"payload":    pgxmock.AnyArg(),
			"priority":   6,
			"dedupe_key": &dedupeKey,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	event := CheckRunEvent{Action: "completed"}
	event.Repository.FullName = "testorg/testrepo"
	event.CheckRun.PullRequests = []struct {
		Number int `json:"number"`
	}{{Number: 42}}

	err := svc.HandleCheckRunEvent(context.Background(), event)
	require.NoError(t, err, "HandleCheckRunEvent should enqueue a health sync for completed check runs")
	require.NoError(t, prMock.ExpectationsWereMet(), "all pull request expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "check run completion should enqueue a scoped health sync")
}

func TestHandleStatusEvent_EnqueuesHealthSyncForHeadSHA(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	jobMock := newMockPool(t)
	svc := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		jobs:         db.NewJobStore(jobMock),
		logger:       zerolog.Nop(),
	}

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id = .+ AND github_repo = .+ AND head_sha = .+ AND status = 'open'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "github_repo": "testorg/testrepo", "head_sha": "head-sha"}).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)
	dedupeKey := pullRequestStateSyncDedupeKey(prID, "status:ci/circleci: frontend_lint_format_license:failure")
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgx.NamedArgs{
			"org_id":     orgID,
			"queue":      prHealthSyncQueue,
			"job_type":   prHealthSyncJobType,
			"payload":    pgxmock.AnyArg(),
			"priority":   6,
			"dedupe_key": &dedupeKey,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	event := StatusEvent{
		State:      "failure",
		SHA:        "head-sha",
		Context:    "ci/circleci: frontend_lint_format_license",
		OwnerOrgID: &orgID,
	}
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandleStatusEvent(context.Background(), event)
	require.NoError(t, err, "HandleStatusEvent should enqueue a health sync for matching open PR heads")
	require.NoError(t, prMock.ExpectationsWereMet(), "all pull request expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "status event should enqueue a scoped health sync")
}

func TestHandleCheckSuiteEvent_PRNotFound(t *testing.T) {
	t.Parallel()

	prMock := newMockPool(t)
	prStore := db.NewPullRequestStore(prMock)

	svc := &PRService{
		pullRequests: prStore,
		logger:       zerolog.Nop(),
	}

	// Mock: GetByRepoAndNumber returns no rows.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPRColumns))

	conclusion := "success"
	event := CheckSuiteEvent{Action: "completed"}
	event.CheckSuite.Conclusion = &conclusion
	event.CheckSuite.PullRequests = []struct {
		Number int `json:"number"`
	}{{Number: 99}}
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandleCheckSuiteEvent(context.Background(), event)
	require.NoError(t, err, "should skip unknown PRs without error")
	require.NoError(t, prMock.ExpectationsWereMet(), "all database expectations should be met")
}

// recordingPreviewStopper captures StopPreview calls for assertions.
type recordingPreviewStopper struct {
	calls   int
	orgID   uuid.UUID
	prevID  uuid.UUID
	nextErr error
}

func (r *recordingPreviewStopper) StopPreview(_ context.Context, orgID, previewID uuid.UUID) error {
	r.calls++
	r.orgID = orgID
	r.prevID = previewID
	return r.nextErr
}

func TestHandlePullRequestEvent_ClosedStopsPreview(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	prPreviewStateID := uuid.New()
	previewInstanceID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	repoMock := newMockPool(t)
	previewMock := newMockPool(t)

	stopper := &recordingPreviewStopper{}

	svc := &PRService{
		pullRequests:   db.NewPullRequestStore(prMock),
		repos:          db.NewRepositoryStore(repoMock),
		previews:       db.NewPreviewStore(previewMock),
		previewStopper: stopper,
		logger:         zerolog.Nop(),
	}

	// 1. GetByRepoAndNumber returns the PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "testorg/testrepo", now)...),
		)

	// 2. UpdateStatus to closed.
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// 3. Repo lookup by full name.
	handlerRepoColumns := []string{
		"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
		"private", "language", "description", "clone_url", "installation_id", "status",
		"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
	}
	repoMock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerRepoColumns).
				AddRow(repoID, orgID, integrationID, int64(12345),
					"testorg/testrepo", "main", false, nil, nil,
					"https://github.com/testorg/testrepo.git", int64(99),
					"active", nil, nil, json.RawMessage(`{}`), now, now),
		)

	// 4. GetPRPreviewState returns a state with a live instance.
	previewMock.ExpectQuery("SELECT .+ FROM pr_preview_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "repo_id", "pr_number", "github_comment_id",
				"last_preview_instance_id", "last_screenshot_blob_path",
				"last_visual_diff_blob_path", "base_snapshot_key", "status",
				"created_at", "updated_at",
			}).AddRow(
				prPreviewStateID, orgID, repoID, 42, nil,
				&previewInstanceID, "", "", "", "running", now, now,
			),
		)

	// 5. UpdatePRPreviewStatus to closed.
	previewMock.ExpectExec("UPDATE pr_preview_state SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	event := PullRequestEvent{Action: "closed", Number: 42}
	event.PR.Merged = false
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err)
	require.Equal(t, 1, stopper.calls, "StopPreview should be called exactly once")
	require.Equal(t, previewInstanceID, stopper.prevID, "StopPreview should receive the preview instance id from pr_preview_state")
	require.Equal(t, orgID, stopper.orgID, "StopPreview should receive the PR's org id")
	require.NoError(t, prMock.ExpectationsWereMet())
	require.NoError(t, repoMock.ExpectationsWereMet())
	require.NoError(t, previewMock.ExpectationsWereMet())
}

// TestHandlePullRequestEvent_MergedStopsPreview exercises the full merged
// branch of HandlePullRequestEvent with preview teardown wired in: PR update,
// deploy record, enqueued job, AND the preview stop + pr_preview_state
// transition to "merged". SessionID is nil so we skip the session/issue path.
func TestHandlePullRequestEvent_MergedStopsPreview(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	prPreviewStateID := uuid.New()
	previewInstanceID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	deployMock := newMockPool(t)
	jobMock := newMockPool(t)
	repoMock := newMockPool(t)
	previewMock := newMockPool(t)

	stopper := &recordingPreviewStopper{}

	svc := &PRService{
		pullRequests:   db.NewPullRequestStore(prMock),
		deploys:        db.NewDeployStore(deployMock),
		jobs:           db.NewJobStore(jobMock),
		repos:          db.NewRepositoryStore(repoMock),
		previews:       db.NewPreviewStore(previewMock),
		previewStopper: stopper,
		logger:         zerolog.Nop(),
	}

	// 1. GetByRepoAndNumber returns a PR with nil session_id.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE github_repo").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, (*uuid.UUID)(nil), orgID, "testorg/testrepo", now)...),
		)

	// 2. UpdateStatus to merged.
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// 3. No existing deploy row, then create deploy.
	deployMock.ExpectQuery("SELECT id, pull_request_id, org_id, environment, deployed_at, commit_sha, created_at\n\t\tFROM deploys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "pull_request_id", "org_id", "environment", "deployed_at", "commit_sha", "created_at"}),
		)
	deployMock.ExpectQuery("INSERT INTO deploys").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "deployed_at", "created_at"}).
				AddRow(uuid.New(), now, now),
		)

	// 4. Enqueue evaluate_experiment job.
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()),
		)

	// 5. Repo lookup (org-scoped).
	handlerRepoColumns := []string{
		"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
		"private", "language", "description", "clone_url", "installation_id", "status",
		"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
	}
	repoMock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerRepoColumns).
				AddRow(repoID, orgID, integrationID, int64(12345),
					"testorg/testrepo", "main", false, nil, nil,
					"https://github.com/testorg/testrepo.git", int64(99),
					"active", nil, nil, json.RawMessage(`{}`), now, now),
		)

	// 6. GetPRPreviewState returns a running state with a live instance.
	previewMock.ExpectQuery("SELECT .+ FROM pr_preview_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "repo_id", "pr_number", "github_comment_id",
				"last_preview_instance_id", "last_screenshot_blob_path",
				"last_visual_diff_blob_path", "base_snapshot_key", "status",
				"created_at", "updated_at",
			}).AddRow(
				prPreviewStateID, orgID, repoID, 42, nil,
				&previewInstanceID, "", "", "", "running", now, now,
			),
		)

	// 7. UpdatePRPreviewStatus — pgx.NamedArgs bind positionally as
	// @status, @id, @org_id. Assert status is the "merged" terminal value.
	previewMock.ExpectExec("UPDATE pr_preview_state SET status").
		WithArgs(models.PRPreviewStatusMerged, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	event := PullRequestEvent{Action: "closed", Number: 42}
	event.PR.Merged = true
	event.PR.Head.SHA = "abc123commit"
	event.Repository.FullName = "testorg/testrepo"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err)
	require.Equal(t, 1, stopper.calls, "StopPreview should be called exactly once on the merged branch")
	require.Equal(t, previewInstanceID, stopper.prevID)
	require.Equal(t, orgID, stopper.orgID)
	require.NoError(t, prMock.ExpectationsWereMet())
	require.NoError(t, deployMock.ExpectationsWereMet())
	require.NoError(t, jobMock.ExpectationsWereMet())
	require.NoError(t, repoMock.ExpectationsWereMet())
	require.NoError(t, previewMock.ExpectationsWereMet())
}

// TestTeardownPRPreview focuses on the merged-vs-closed status decision inside
// teardownPRPreview without exercising the rest of HandlePullRequestEvent
// (which, on the merged branch, also writes a Deploy row and enqueues a job).
func TestTeardownPRPreview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		merged     bool
		wantStatus models.PRPreviewStatus
	}{
		{name: "closed without merge", merged: false, wantStatus: models.PRPreviewStatusClosed},
		{name: "closed via merge", merged: true, wantStatus: models.PRPreviewStatusMerged},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			prID := uuid.New()
			repoID := uuid.New()
			integrationID := uuid.New()
			prPreviewStateID := uuid.New()
			previewInstanceID := uuid.New()
			now := time.Now()

			repoMock := newMockPool(t)
			previewMock := newMockPool(t)

			stopper := &recordingPreviewStopper{}

			svc := &PRService{
				repos:          db.NewRepositoryStore(repoMock),
				previews:       db.NewPreviewStore(previewMock),
				previewStopper: stopper,
				logger:         zerolog.Nop(),
			}

			// Repo lookup by full name.
			handlerRepoColumns := []string{
				"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
				"private", "language", "description", "clone_url", "installation_id", "status",
				"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
			}
			repoMock.ExpectQuery("SELECT .+ FROM repositories").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(handlerRepoColumns).
						AddRow(repoID, orgID, integrationID, int64(12345),
							"testorg/testrepo", "main", false, nil, nil,
							"https://github.com/testorg/testrepo.git", int64(99),
							"active", nil, nil, json.RawMessage(`{}`), now, now),
				)

			// GetPRPreviewState returns a state with a live instance.
			previewMock.ExpectQuery("SELECT .+ FROM pr_preview_state").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows([]string{
						"id", "org_id", "repo_id", "pr_number", "github_comment_id",
						"last_preview_instance_id", "last_screenshot_blob_path",
						"last_visual_diff_blob_path", "base_snapshot_key", "status",
						"created_at", "updated_at",
					}).AddRow(
						prPreviewStateID, orgID, repoID, 42, nil,
						&previewInstanceID, "", "", "", "running", now, now,
					),
				)

			// UpdatePRPreviewStatus — assert the status arg matches the expected
			// terminal value. pgx expands NamedArgs positionally in the order the
			// @name placeholders appear in the SQL: @status, @id, @org_id.
			previewMock.ExpectExec("UPDATE pr_preview_state SET status").
				WithArgs(tc.wantStatus, pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			pr := models.PullRequest{
				ID:             prID,
				OrgID:          orgID,
				GitHubRepo:     "testorg/testrepo",
				GitHubPRNumber: 42,
			}
			svc.teardownPRPreview(context.Background(), pr, tc.merged)

			require.Equal(t, 1, stopper.calls, "StopPreview should be called exactly once")
			require.Equal(t, previewInstanceID, stopper.prevID)
			require.Equal(t, orgID, stopper.orgID)
			require.NoError(t, repoMock.ExpectationsWereMet())
			require.NoError(t, previewMock.ExpectationsWereMet())
		})
	}
}

func TestPRService_SetPreviewTeardown(t *testing.T) {
	t.Parallel()

	previewMock := newMockPool(t)
	store := db.NewPreviewStore(previewMock)
	stopper := &recordingPreviewStopper{}

	svc := &PRService{logger: zerolog.Nop()}
	svc.SetPreviewTeardown(store, stopper)

	require.Same(t, store, svc.previews, "SetPreviewTeardown should wire the preview store")
	require.Equal(t, PreviewStopper(stopper), svc.previewStopper, "SetPreviewTeardown should wire the stopper")
}

// TestTeardownPRPreview_NilGuards exercises the early-return paths when the
// preview subsystem is not wired. teardownPRPreview must be a silent no-op
// in self-hosted configurations that don't construct a preview manager.
func TestTeardownPRPreview_NilGuards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		svc  *PRService
	}{
		{
			name: "previews store nil",
			svc: &PRService{
				previewStopper: &recordingPreviewStopper{},
				repos:          db.NewRepositoryStore(newMockPool(t)),
				logger:         zerolog.Nop(),
			},
		},
		{
			name: "stopper nil",
			svc: &PRService{
				previews: db.NewPreviewStore(newMockPool(t)),
				repos:    db.NewRepositoryStore(newMockPool(t)),
				logger:   zerolog.Nop(),
			},
		},
		{
			name: "repos nil",
			svc: &PRService{
				previews:       db.NewPreviewStore(newMockPool(t)),
				previewStopper: &recordingPreviewStopper{},
				logger:         zerolog.Nop(),
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pr := models.PullRequest{
				OrgID:          uuid.New(),
				GitHubRepo:     "testorg/testrepo",
				GitHubPRNumber: 42,
			}
			require.NotPanics(t, func() {
				tc.svc.teardownPRPreview(context.Background(), pr, false)
			})
		})
	}
}

// TestTeardownPRPreview_RepoLookupError exercises the path where the
// repository lookup fails. The function must log and return without calling
// the preview store.
func TestTeardownPRPreview_RepoLookupError(t *testing.T) {
	t.Parallel()

	repoMock := newMockPool(t)
	previewMock := newMockPool(t)
	stopper := &recordingPreviewStopper{}

	svc := &PRService{
		repos:          db.NewRepositoryStore(repoMock),
		previews:       db.NewPreviewStore(previewMock),
		previewStopper: stopper,
		logger:         zerolog.Nop(),
	}

	repoMock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("repo not found"))

	pr := models.PullRequest{
		OrgID:          uuid.New(),
		GitHubRepo:     "testorg/missing",
		GitHubPRNumber: 42,
	}
	svc.teardownPRPreview(context.Background(), pr, false)

	require.Equal(t, 0, stopper.calls, "StopPreview must not be called when repo lookup fails")
	require.NoError(t, repoMock.ExpectationsWereMet())
	require.NoError(t, previewMock.ExpectationsWereMet(), "preview store must not be queried when repo lookup fails")
}

// TestTeardownPRPreview_NoPreviewState covers the common path where the PR
// has never had a preview created — GetPRPreviewState returns pgx.ErrNoRows.
// This must be silent (no warning log) since it's the default state for
// every freshly-opened PR.
func TestTeardownPRPreview_NoPreviewState(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	repoMock := newMockPool(t)
	previewMock := newMockPool(t)
	stopper := &recordingPreviewStopper{}

	var logBuf bytes.Buffer
	svc := &PRService{
		repos:          db.NewRepositoryStore(repoMock),
		previews:       db.NewPreviewStore(previewMock),
		previewStopper: stopper,
		logger:         zerolog.New(&logBuf),
	}

	handlerRepoColumns := []string{
		"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
		"private", "language", "description", "clone_url", "installation_id", "status",
		"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
	}
	repoMock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerRepoColumns).
				AddRow(repoID, orgID, integrationID, int64(12345),
					"testorg/testrepo", "main", false, nil, nil,
					"https://github.com/testorg/testrepo.git", int64(99),
					"active", nil, nil, json.RawMessage(`{}`), now, now),
		)

	// Empty result set — CollectOneRow inside GetPRPreviewState surfaces
	// pgx.ErrNoRows, which teardownPRPreview must treat as the benign
	// "no preview ever created" case.
	previewMock.ExpectQuery("SELECT .+ FROM pr_preview_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "pr_number", "github_comment_id",
			"last_preview_instance_id", "last_screenshot_blob_path",
			"last_visual_diff_blob_path", "base_snapshot_key", "status",
			"created_at", "updated_at",
		}))

	pr := models.PullRequest{
		OrgID:          orgID,
		GitHubRepo:     "testorg/testrepo",
		GitHubPRNumber: 42,
	}
	svc.teardownPRPreview(context.Background(), pr, false)

	require.Equal(t, 0, stopper.calls, "StopPreview must not be called when no pr_preview_state exists")
	require.NotContains(t, logBuf.String(), "failed to load pr_preview_state",
		"no-rows must be silent — no warning log for the common case")
	require.NoError(t, repoMock.ExpectationsWereMet())
	require.NoError(t, previewMock.ExpectationsWereMet())
}

// TestTeardownPRPreview_PreviewStateLookupError covers the path where the
// pr_preview_state lookup fails with a real DB error (not no-rows). The
// function must log a warning and return without calling the stopper, so
// ops has a breadcrumb instead of a silent swallow.
func TestTeardownPRPreview_PreviewStateLookupError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	repoMock := newMockPool(t)
	previewMock := newMockPool(t)
	stopper := &recordingPreviewStopper{}

	var logBuf bytes.Buffer
	svc := &PRService{
		repos:          db.NewRepositoryStore(repoMock),
		previews:       db.NewPreviewStore(previewMock),
		previewStopper: stopper,
		logger:         zerolog.New(&logBuf),
	}

	handlerRepoColumns := []string{
		"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
		"private", "language", "description", "clone_url", "installation_id", "status",
		"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
	}
	repoMock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerRepoColumns).
				AddRow(repoID, orgID, integrationID, int64(12345),
					"testorg/testrepo", "main", false, nil, nil,
					"https://github.com/testorg/testrepo.git", int64(99),
					"active", nil, nil, json.RawMessage(`{}`), now, now),
		)

	previewMock.ExpectQuery("SELECT .+ FROM pr_preview_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("connection refused"))

	pr := models.PullRequest{
		OrgID:          orgID,
		GitHubRepo:     "testorg/testrepo",
		GitHubPRNumber: 42,
	}
	svc.teardownPRPreview(context.Background(), pr, false)

	require.Equal(t, 0, stopper.calls, "StopPreview must not be called when preview state lookup errors")
	require.Contains(t, logBuf.String(), "failed to load pr_preview_state",
		"a real DB error must be logged, not silently swallowed")
	require.NoError(t, repoMock.ExpectationsWereMet())
	require.NoError(t, previewMock.ExpectationsWereMet())
}

// TestTeardownPRPreview_NilPreviewInstanceID covers the branch where the
// preview state row exists but never had an instance attached to it.
// UpdatePRPreviewStatus still runs; StopPreview must not.
func TestTeardownPRPreview_NilPreviewInstanceID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	prPreviewStateID := uuid.New()
	now := time.Now()

	repoMock := newMockPool(t)
	previewMock := newMockPool(t)
	stopper := &recordingPreviewStopper{}

	svc := &PRService{
		repos:          db.NewRepositoryStore(repoMock),
		previews:       db.NewPreviewStore(previewMock),
		previewStopper: stopper,
		logger:         zerolog.Nop(),
	}

	handlerRepoColumns := []string{
		"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
		"private", "language", "description", "clone_url", "installation_id", "status",
		"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
	}
	repoMock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerRepoColumns).
				AddRow(repoID, orgID, integrationID, int64(12345),
					"testorg/testrepo", "main", false, nil, nil,
					"https://github.com/testorg/testrepo.git", int64(99),
					"active", nil, nil, json.RawMessage(`{}`), now, now),
		)

	previewMock.ExpectQuery("SELECT .+ FROM pr_preview_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "repo_id", "pr_number", "github_comment_id",
				"last_preview_instance_id", "last_screenshot_blob_path",
				"last_visual_diff_blob_path", "base_snapshot_key", "status",
				"created_at", "updated_at",
			}).AddRow(
				prPreviewStateID, orgID, repoID, 42, nil,
				(*uuid.UUID)(nil), "", "", "", "running", now, now,
			),
		)

	previewMock.ExpectExec("UPDATE pr_preview_state SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	pr := models.PullRequest{
		OrgID:          orgID,
		GitHubRepo:     "testorg/testrepo",
		GitHubPRNumber: 42,
	}
	svc.teardownPRPreview(context.Background(), pr, false)

	require.Equal(t, 0, stopper.calls, "StopPreview must not be called when LastPreviewInstanceID is nil")
	require.NoError(t, repoMock.ExpectationsWereMet())
	require.NoError(t, previewMock.ExpectationsWereMet())
}

// TestTeardownPRPreview_SwallowsErrors confirms that a stopper failure and
// an UpdatePRPreviewStatus failure are both logged-and-swallowed: the
// function returns normally so webhook processing continues.
func TestTeardownPRPreview_SwallowsErrors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	prPreviewStateID := uuid.New()
	previewInstanceID := uuid.New()
	now := time.Now()

	repoMock := newMockPool(t)
	previewMock := newMockPool(t)
	stopper := &recordingPreviewStopper{nextErr: fmt.Errorf("preview already gone")}

	svc := &PRService{
		repos:          db.NewRepositoryStore(repoMock),
		previews:       db.NewPreviewStore(previewMock),
		previewStopper: stopper,
		logger:         zerolog.Nop(),
	}

	handlerRepoColumns := []string{
		"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
		"private", "language", "description", "clone_url", "installation_id", "status",
		"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
	}
	repoMock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerRepoColumns).
				AddRow(repoID, orgID, integrationID, int64(12345),
					"testorg/testrepo", "main", false, nil, nil,
					"https://github.com/testorg/testrepo.git", int64(99),
					"active", nil, nil, json.RawMessage(`{}`), now, now),
		)

	previewMock.ExpectQuery("SELECT .+ FROM pr_preview_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "repo_id", "pr_number", "github_comment_id",
				"last_preview_instance_id", "last_screenshot_blob_path",
				"last_visual_diff_blob_path", "base_snapshot_key", "status",
				"created_at", "updated_at",
			}).AddRow(
				prPreviewStateID, orgID, repoID, 42, nil,
				&previewInstanceID, "", "", "", "running", now, now,
			),
		)

	previewMock.ExpectExec("UPDATE pr_preview_state SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("update failed"))

	pr := models.PullRequest{
		OrgID:          orgID,
		GitHubRepo:     "testorg/testrepo",
		GitHubPRNumber: 42,
	}
	require.NotPanics(t, func() {
		svc.teardownPRPreview(context.Background(), pr, true)
	})

	require.Equal(t, 1, stopper.calls, "StopPreview should still be invoked even though it returns an error")
	require.NoError(t, repoMock.ExpectationsWereMet())
	require.NoError(t, previewMock.ExpectationsWereMet())
}

// TestHandlePullRequestEvent_OpenedWith143GeneratedPRTriggersAutoPreview confirms that
// auto-preview policy is applied even when the PR was created by 143 (i.e., when
// getWebhookPullRequest succeeds). Previously, the code returned nil without calling
// handleAutoPreviewEvent for 143-generated PRs.
func TestHandlePullRequestEvent_OpenedWith143GeneratedPRTriggersAutoPreview(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	jobMock := newMockPool(t)
	repoMock := newMockPool(t)
	previewMock := newMockPool(t)
	starter := &recordingAutoPreviewStarter{}

	svc := &PRService{
		pullRequests:       db.NewPullRequestStore(prMock),
		jobs:               db.NewJobStore(jobMock),
		repos:              db.NewRepositoryStore(repoMock),
		previews:           db.NewPreviewStore(previewMock),
		autoPreviewStarter: starter,
		logger:             zerolog.Nop(),
	}

	// 1. GetByOrgRepoAndNumber returns the 143-generated PR.
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "acme/app", now)...),
		)

	// 2. enqueuePullRequestStateSync inserts a job.
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	// 3. handleAutoPreviewEvent: repo lookup.
	repoMock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(githubRepositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, strPtr("go"), strPtr(""), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))

	// 4. handleAutoPreviewEvent: policy lookup returns warm mode.
	previewMock.ExpectQuery("SELECT .+ FROM repository_preview_policies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(githubRepositoryPreviewPolicyTestCols()).
			AddRow(uuid.New(), orgID, repoID, string(models.PreviewAutoModeWarm), userID, now, now))

	event := autoPreviewPullRequestEvent(orgID)
	err := svc.HandlePullRequestEvent(context.Background(), event)

	require.NoError(t, err, "HandlePullRequestEvent for 143-generated PR should not fail")
	require.True(t, starter.called, "auto preview starter should be invoked even for 143-generated PRs")
	require.Equal(t, models.PreviewAutoModeWarm, starter.mode, "auto preview should receive the configured mode")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "all job expectations should be met")
	require.NoError(t, repoMock.ExpectationsWereMet(), "all repo expectations should be met")
	require.NoError(t, previewMock.ExpectationsWereMet(), "all preview expectations should be met")
}

// TestHandlePullRequestEvent_ClosedWith143GeneratedPRTeardownAutoPreview confirms
// that closing a 143-generated PR invokes teardownAutoPreview (via handleAutoPreviewEvent)
// in addition to the regular applyClosedPRTransition path.
func TestHandlePullRequestEvent_ClosedWith143GeneratedPRTeardownAutoPreview(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	prPreviewStateID := uuid.New()
	now := time.Now()

	prMock := newMockPool(t)
	jobMock := newMockPool(t)
	repoMock := newMockPool(t)
	previewMock := newMockPool(t)

	// previewStopper is nil so teardownPRPreview is a no-op (guard check fails).
	svc := &PRService{
		pullRequests:       db.NewPullRequestStore(prMock),
		jobs:               db.NewJobStore(jobMock),
		repos:              db.NewRepositoryStore(repoMock),
		previews:           db.NewPreviewStore(previewMock),
		autoPreviewStarter: &recordingAutoPreviewStarter{},
		logger:             zerolog.Nop(),
	}

	// 1. GetByOrgRepoAndNumber returns the 143-generated PR (nil session).
	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPRColumns).
				AddRow(handlerPRRow(prID, &sessionID, orgID, "acme/app", now)...),
		)

	// 2. applyClosedPRTransition: UpdateStatus to closed.
	prMock.ExpectExec("UPDATE pull_requests SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// 3. enqueuePullRequestStateSync inserts a job.
	jobMock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	// 4. teardownAutoPreview: repo lookup.
	repoMock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(githubRepositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, strPtr("go"), strPtr(""), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))

	// 5. teardownAutoPreview: GetPRPreviewState returns a state (nil LastPreviewInstanceID so no stop).
	previewMock.ExpectQuery("SELECT .+ FROM pr_preview_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "pr_number", "github_comment_id",
			"last_preview_instance_id", "last_screenshot_blob_path",
			"last_visual_diff_blob_path", "base_snapshot_key", "status",
			"created_at", "updated_at",
		}).AddRow(prPreviewStateID, orgID, repoID, 42, nil, nil, "", "", "", "running", now, now))

	// 6. teardownAutoPreview: UpdatePRPreviewStatus to closed.
	previewMock.ExpectExec("UPDATE pr_preview_state SET status").
		WithArgs(models.PRPreviewStatusClosed, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	event := PullRequestEvent{Action: "closed", Number: 42, OwnerOrgID: &orgID}
	event.PR.Merged = false
	event.PR.Head.SHA = "abc123commit"
	event.Repository.FullName = "acme/app"

	err := svc.HandlePullRequestEvent(context.Background(), event)
	require.NoError(t, err, "HandlePullRequestEvent for closed 143-generated PR should not fail")
	require.NoError(t, prMock.ExpectationsWereMet(), "all PR expectations should be met")
	require.NoError(t, jobMock.ExpectationsWereMet(), "all job expectations should be met")
	require.NoError(t, repoMock.ExpectationsWereMet(), "repo lookup for teardownAutoPreview must be called")
	require.NoError(t, previewMock.ExpectationsWereMet(), "GetPRPreviewState and UpdatePRPreviewStatus must be called")
}

func TestHandlePushEvent_UpdatesBranchPreviewGroups(t *testing.T) {
	t.Parallel()

	repoMock := newMockPool(t)
	previewMock := newMockPool(t)
	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	svc := &PRService{
		repos:    db.NewRepositoryStore(repoMock),
		previews: db.NewPreviewStore(previewMock),
		logger:   zerolog.Nop(),
	}

	repoMock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(githubRepositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, strPtr("go"), strPtr(""), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
	previewMock.ExpectExec("UPDATE preview_groups").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	event := PushEvent{
		Ref:        "refs/heads/feature/foo",
		After:      "newcommitsha",
		OwnerOrgID: &orgID,
	}
	event.Repository.FullName = "acme/app"

	err := svc.HandlePushEvent(context.Background(), event)

	require.NoError(t, err, "HandlePushEvent should succeed for a valid branch push")
	require.NoError(t, repoMock.ExpectationsWereMet(), "repository lookup should be called")
	require.NoError(t, previewMock.ExpectationsWereMet(), "preview group SHA update should be called")
}

func TestHandlePushEvent_IgnoresTagPush(t *testing.T) {
	t.Parallel()

	svc := &PRService{
		repos:    db.NewRepositoryStore(newMockPool(t)),
		previews: db.NewPreviewStore(newMockPool(t)),
		logger:   zerolog.Nop(),
	}

	event := PushEvent{Ref: "refs/tags/v1.0.0", After: "abc123"}
	err := svc.HandlePushEvent(context.Background(), event)

	require.NoError(t, err, "HandlePushEvent should silently ignore tag push refs")
}

func TestBranchFromRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref    string
		branch string
		ok     bool
	}{
		{"refs/heads/main", "main", true},
		{"refs/heads/feature/foo", "feature/foo", true},
		{"refs/tags/v1.0", "", false},
		{"refs/heads/", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		branch, ok := BranchFromRef(tt.ref)
		require.Equal(t, tt.ok, ok, "BranchFromRef(%q) ok", tt.ref)
		require.Equal(t, tt.branch, branch, "BranchFromRef(%q) branch", tt.ref)
	}
}

func TestHandleAutoPreviewEvent_SynchronizeUpdatesPRPreviewGroupSHA(t *testing.T) {
	t.Parallel()

	repoMock := newMockPool(t)
	previewMock := newMockPool(t)
	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	starter := &recordingAutoPreviewStarter{}

	svc := &PRService{
		repos:              db.NewRepositoryStore(repoMock),
		previews:           db.NewPreviewStore(previewMock),
		autoPreviewStarter: starter,
		logger:             zerolog.Nop(),
	}

	repoMock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(githubRepositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, strPtr("go"), strPtr(""), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
	previewMock.ExpectQuery("SELECT .+ FROM repository_preview_policies").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(githubRepositoryPreviewPolicyTestCols()).
			AddRow(uuid.New(), orgID, repoID, string(models.PreviewAutoModeOn), userID, now, now))
	previewMock.ExpectExec("UPDATE preview_groups").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	event := autoPreviewPullRequestEvent(orgID)
	event.Action = "synchronize"
	event.PR.Head.SHA = "newheadsha"

	err := svc.handleAutoPreviewEvent(context.Background(), event)

	require.NoError(t, err, "handleAutoPreviewEvent for synchronize should not fail")
	require.True(t, starter.called, "auto preview starter should still be invoked on synchronize")
	require.NoError(t, repoMock.ExpectationsWereMet(), "repository expectations should be met")
	require.NoError(t, previewMock.ExpectationsWereMet(), "preview group SHA update should be called on synchronize")
}
