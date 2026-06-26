package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/preview"
)

type fakeBranchPreviewGitHub struct {
	token         string
	head          string
	prHead        ghservice.PullRequestHead
	configContent string
}

type fakeBranchPreviewGitHubWithDetails struct {
	fakeBranchPreviewGitHub
	details ghservice.InstallationDetails
}

func (f fakeBranchPreviewGitHubWithDetails) GetInstallationDetails(context.Context, int64) (ghservice.InstallationDetails, error) {
	return f.details, nil
}

type fakeBranchPreviewGitHubWithErrors struct {
	fakeBranchPreviewGitHub
	tokenErr  error
	configErr error
}

func (f fakeBranchPreviewGitHubWithErrors) GetInstallationToken(context.Context, int64) (string, error) {
	if f.tokenErr != nil {
		return "", f.tokenErr
	}
	return f.fakeBranchPreviewGitHub.token, nil
}

func (f fakeBranchPreviewGitHubWithErrors) GetFileContent(context.Context, string, string, string, string, string) (string, error) {
	if f.configErr != nil {
		return "", f.configErr
	}
	return f.fakeBranchPreviewGitHub.GetFileContent(context.Background(), "", "", "", "", "")
}

func branchPreviewAnyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func ptrToUUID(value uuid.UUID) *uuid.UUID {
	return &value
}

func ptrToInt(value int) *int {
	return &value
}

var branchPreviewTargetTestCols = []string{
	"id", "org_id", "repository_id", "branch", "commit_sha", "preview_config_name",
	"resolved_config_digest", "source_type", "source_id", "source_url",
	"created_by_user_id", "request_id", "preview_group_id", "created_at",
}

var branchPreviewGroupTestCols = []string{
	"id", "org_id", "repository_id", "group_kind", "branch", "preview_config_name",
	"pull_request_number", "source_type", "source_id", "source_url", "current_target_id",
	"latest_commit_sha", "current_status", "pinned", "created_by_user_id", "created_at", "last_activity_at",
}

var branchPreviewCurrentSummaryTestCols = []string{
	"id", "org_id", "repository_id", "group_kind", "branch", "preview_config_name",
	"pull_request_number", "source_type", "source_id", "source_url", "current_target_id",
	"latest_commit_sha", "current_status", "pinned", "created_by_user_id", "created_at", "last_activity_at",
	"repository_full_name", "status", "freshness", "running_commit_sha", "current_preview_id",
	"expires_at", "stopped_at", "stopped_reason", "error", "current_phase", "attempt_count", "target_count",
	"resumable", "resume_estimate_seconds",
}

var branchPreviewLinkTestCols = []string{
	"id", "org_id", "preview_target_id", "link_type", "slug", "repository_id",
	"pr_number", "created_at", "updated_at",
}

var branchPreviewInstanceTestCols = []string{
	"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
	"provider", "worker_node_id", "preview_handle", "primary_service", "port",
	"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
	"last_path", "memory_limit_mb", "cpu_limit_millis", "disk_limit_mb", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
	"source_workspace_revision", "source_workspace_revision_updated_at", "runtime_workspace_revision", "runtime_workspace_revision_updated_at", "runtime_workspace_revision_source", "unavailable_reason", "preview_holding_container",
}

var branchPreviewRuntimeTestCols = []string{
	"id", "org_id", "preview_instance_id", "runtime_epoch", "worker_node_id",
	"endpoint_url", "preview_handle", "primary_port", "status", "lease_expires_at",
	"last_heartbeat_at", "drain_requested_at", "stopped_at", "error", "unavailable_reason", "created_at", "updated_at",
}

var branchPreviewStartupCacheTestCols = []string{
	"id", "org_id", "repo_id", "snapshot_key", "base_key", "commit_sha", "blob_path",
	"size_bytes", "worker_node_id", "last_used_at", "created_at",
}

var branchPreviewNodeTestCols = []string{
	"id", "mode", "host", "status", "drain_intent", "metadata", "started_at", "last_heartbeat_at",
	"drain_requested_at", "drain_budget_expires_at", "drain_requested_by", "drain_reason",
}

func (f fakeBranchPreviewGitHub) GetInstallationToken(context.Context, int64) (string, error) {
	return f.token, nil
}

func (f fakeBranchPreviewGitHub) ResolveBranchHead(context.Context, string, string, string, string) (string, error) {
	return f.head, nil
}

func (f fakeBranchPreviewGitHub) CommitExists(context.Context, string, string, string, string) error {
	return nil
}

func (f fakeBranchPreviewGitHub) GetPullRequestHead(context.Context, string, string, string, int) (ghservice.PullRequestHead, error) {
	if f.prHead.Number != 0 || f.prHead.Branch != "" || f.prHead.SHA != "" || f.prHead.State != "" || f.prHead.HTMLURL != "" {
		return f.prHead, nil
	}
	return ghservice.PullRequestHead{Number: 7, HTMLURL: "https://github.com/acme/app/pull/7", State: "open", Branch: "feature/previews", SHA: f.head}, nil
}

func (f fakeBranchPreviewGitHub) GetFileContent(context.Context, string, string, string, string, string) (string, error) {
	if f.configContent != "" {
		return f.configContent, nil
	}
	return `{"preview":{"name":"web","command":["npm","run","dev"],"port":3000}}`, nil
}

func expectBranchPreviewGroupUpsert(mock pgxmock.PgxPoolIface, orgID, repoID, targetID, userID uuid.UUID, branch, sha, configName string, sourceType models.PreviewSourceType, sourceID, sourceURL string, now time.Time) {
	groupID := uuid.New()
	kind := models.PreviewGroupKindBranch
	groupSourceType := sourceType
	groupSourceID := sourceID
	var prNumber *int
	if _, _, number, ok := db.ParsePRSourceID(sourceID); ok {
		kind = models.PreviewGroupKindPullRequest
		groupSourceType = models.PreviewSourceTypePullRequest
		groupSourceID = fmt.Sprintf("acme/app#%d", number)
		prNumber = &number
	} else if sourceID != "" {
		kind = models.PreviewGroupKindSource
	}
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(branchPreviewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("target_created"))
	mock.ExpectQuery("INSERT INTO preview_groups").
		WithArgs(branchPreviewAnyArgs(15)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewGroupTestCols).AddRow(
			groupID, orgID, repoID, kind, branch, configName,
			prNumber, groupSourceType, groupSourceID, sourceURL, &targetID,
			sha, "target_created", false, &userID, now, now,
		))
	mock.ExpectExec("UPDATE preview_targets SET preview_group_id").
		WithArgs(branchPreviewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
}

func TestBranchPreviewHandler_ResponseForPreviewUsesTargetPreviewOrigin(t *testing.T) {
	t.Parallel()

	targetID := uuid.New()
	previewID := uuid.New()
	repoID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	handler := NewBranchPreviewHandler(
		nil,
		nil,
		nil,
		nil,
		"https://143.dev",
		"https://{id}.preview.143.dev",
	)
	target := &models.PreviewTarget{
		ID:              targetID,
		OrgID:           orgID,
		RepositoryID:    repoID,
		Branch:          "feature/login",
		CommitSHA:       "abcdef1234567890abcdef1234567890abcdef12",
		SourceType:      models.PreviewSourceTypePullRequest,
		SourceID:        "acme/app#7",
		CreatedByUserID: userID,
		CreatedAt:       now,
	}
	instance := &models.PreviewInstance{
		ID:              previewID,
		PreviewTargetID: &targetID,
		OrgID:           orgID,
		UserID:          userID,
		Status:          models.PreviewStatusReady,
		ExpiresAt:       now.Add(time.Hour),
	}

	resp := handler.responseForPreview(targetID.String(), target, instance)

	require.NotNil(t, resp.PreviewURL, "response should expose a preview URL when a template is configured")
	require.Equal(t, "https://"+targetID.String()+".preview.143.dev", *resp.PreviewURL, "branch preview URL should use the stable preview target host instead of the runtime instance host")
	require.NotContains(t, *resp.PreviewURL, previewID.String(), "branch preview URL should survive runtime restarts that replace the instance ID")
}

func TestBranchPreviewHandler_CreateResolvesBranchHeadAndCreatesTarget(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	targetID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	head := "0123456789abcdef0123456789abcdef01234567"

	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil, "https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now))

	mock.ExpectQuery("INSERT INTO preview_targets").
		WithArgs(branchPreviewAnyArgs(11)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(targetID, orgID, repoID, "feature/previews", head, "", "", "manual", "", "", userID, nil, nil, now))
	expectBranchPreviewGroupUpsert(mock, orgID, repoID, targetID, userID, "feature/previews", head, "", models.PreviewSourceTypeManual, "", "", now)

	mock.ExpectQuery("INSERT INTO preview_links").
		WithArgs(branchPreviewAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewLinkTestCols).AddRow(linkID, orgID, targetID, "target", targetID.String(), &repoID, (*int)(nil), now, now))

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: head},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	body := bytes.NewBufferString(`{"repository_id":"` + repoID.String() + `","branch":"feature/previews"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews", body)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, "Create should return created for a valid branch preview target")
	var resp models.SingleResponse[branchPreviewResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Create response should be valid JSON")
	require.Equal(t, targetID, resp.Data.TargetID, "Create should return the created target ID")
	require.Equal(t, "target_created", resp.Data.Status, "Create should report target_created before a runtime is attached")
	require.Equal(t, "https://app.143.dev/previews/"+targetID.String(), resp.Data.StableURL, "Create should return the stable target URL")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandlerWorkerSelectionRequirementsRequireStaticEgress(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	handler := NewBranchPreviewHandler(nil, nil, nil, nil, "", "")
	handler.SetStaticEgressSettings(previewStaticEgressOrgStore{
		settings: json.RawMessage(`{"sandbox_network":{"static_egress_enabled":true}}`),
	}, "203.0.113.10")

	reqs, err := handler.workerSelectionRequirements(context.Background(), orgID)

	require.NoError(t, err, "branch preview worker selection should read org network settings")
	require.True(t, reqs.StaticEgressRequired, "branch preview worker selection should require static-egress-capable workers for opted-in orgs")
	require.Equal(t, "203.0.113.10", reqs.StaticEgressPublicIP, "branch preview worker selection should require workers verified against the configured static egress public IP")
}

func TestDerivePRPreviewLaunch(t *testing.T) {
	t.Parallel()

	previewURL := "https://target.preview.143.dev"
	staleURL := "https://old.preview.143.dev"
	tests := []struct {
		name     string
		resp     branchPreviewResponse
		opts     prPreviewLaunchOptions
		expected branchPreviewLaunch
	}{
		{
			name: "ready latest preview opens",
			resp: branchPreviewResponse{
				Status:          string(models.PreviewStatusReady),
				PreviewID:       ptrToUUID(uuid.New()),
				PreviewURL:      &previewURL,
				CommitSHA:       "abc123",
				LatestCommitSHA: "abc123",
			},
			opts: prPreviewLaunchOptions{
				CanRead:         true,
				CanCreate:       true,
				ClickedOpen:     true,
				LatestCommitSHA: "abc123",
			},
			expected: branchPreviewLaunch{
				Action:           models.PreviewLaunchActionOpen,
				Reason:           models.PreviewLaunchReasonReady,
				AutoOpen:         true,
				RepresentsLatest: true,
				PrimaryLabel:     "Open preview",
			},
		},
		{
			name: "starting latest preview waits and auto opens for open intent",
			resp: branchPreviewResponse{
				Status:          string(models.PreviewStatusStarting),
				PreviewID:       ptrToUUID(uuid.New()),
				PreviewURL:      &previewURL,
				CommitSHA:       "abc123",
				LatestCommitSHA: "abc123",
			},
			opts: prPreviewLaunchOptions{
				CanRead:         true,
				CanCreate:       true,
				ClickedOpen:     true,
				LatestCommitSHA: "abc123",
			},
			expected: branchPreviewLaunch{
				Action:           models.PreviewLaunchActionWait,
				Reason:           models.PreviewLaunchReasonStarting,
				AutoOpen:         true,
				RepresentsLatest: true,
				PrimaryLabel:     "Opening when ready",
			},
		},
		{
			name: "resumable stopped preview resumes",
			resp: branchPreviewResponse{
				Status:                string(models.PreviewStatusStopped),
				TargetID:              uuid.New(),
				CommitSHA:             "abc123",
				LatestCommitSHA:       "abc123",
				Resumable:             true,
				ResumeEstimateSeconds: ptrToInt(30),
			},
			opts: prPreviewLaunchOptions{
				CanRead:         true,
				CanCreate:       true,
				ClickedOpen:     true,
				LatestCommitSHA: "abc123",
			},
			expected: branchPreviewLaunch{
				Action:           models.PreviewLaunchActionResume,
				Reason:           models.PreviewLaunchReasonResumable,
				AutoOpen:         true,
				RepresentsLatest: true,
				PrimaryLabel:     "Resume preview",
				Message:          "This preview is ready to resume in about 30 seconds.",
			},
		},
		{
			name: "stale preview starts latest and does not auto open",
			resp: branchPreviewResponse{
				Status:              string(models.PreviewStatusReady),
				PreviewID:           ptrToUUID(uuid.New()),
				PreviewURL:          &staleURL,
				CommitSHA:           "abc123",
				LatestCommitSHA:     "def456",
				NewCommitsAvailable: true,
			},
			opts: prPreviewLaunchOptions{
				CanRead:         true,
				CanCreate:       true,
				ClickedOpen:     true,
				LatestCommitSHA: "def456",
			},
			expected: branchPreviewLaunch{
				Action:           models.PreviewLaunchActionStartLatest,
				Reason:           models.PreviewLaunchReasonStale,
				AutoOpen:         false,
				RepresentsLatest: false,
				PrimaryLabel:     "Restart",
				SecondaryLabel:   "Open stale preview",
				StalePreviewURL:  &staleURL,
				Message:          "This preview is for abc123; the pull request is now at def456.",
			},
		},
		{
			name: "failed latest preview retries",
			resp: branchPreviewResponse{
				Status:          string(models.PreviewStatusFailed),
				PreviewID:       ptrToUUID(uuid.New()),
				CommitSHA:       "abc123",
				LatestCommitSHA: "abc123",
			},
			opts: prPreviewLaunchOptions{
				CanRead:         true,
				CanCreate:       true,
				LatestCommitSHA: "abc123",
			},
			expected: branchPreviewLaunch{
				Action:           models.PreviewLaunchActionRetry,
				Reason:           models.PreviewLaunchReasonFailed,
				AutoOpen:         false,
				RepresentsLatest: true,
				PrimaryLabel:     "Retry preview",
			},
		},
		{
			name: "closed pull request is terminal",
			resp: branchPreviewResponse{Status: "target_created", CommitSHA: "abc123", LatestCommitSHA: "abc123"},
			opts: prPreviewLaunchOptions{
				CanRead:         true,
				CanCreate:       true,
				PRClosed:        true,
				LatestCommitSHA: "abc123",
			},
			expected: branchPreviewLaunch{
				Action:           models.PreviewLaunchActionClosed,
				Reason:           models.PreviewLaunchReasonPullRequestClosed,
				AutoOpen:         false,
				RepresentsLatest: true,
				Message:          "This pull request is closed, so 143 will not start a new preview by default.",
			},
		},
		{
			name: "viewer without runtime is blocked",
			resp: branchPreviewResponse{Status: "target_created", CommitSHA: "abc123", LatestCommitSHA: "abc123"},
			opts: prPreviewLaunchOptions{
				CanRead:         true,
				CanCreate:       false,
				LatestCommitSHA: "abc123",
			},
			expected: branchPreviewLaunch{
				Action:           models.PreviewLaunchActionBlocked,
				Reason:           models.PreviewLaunchReasonRoleForbidden,
				AutoOpen:         false,
				RepresentsLatest: true,
				Message:          "You can open existing previews, but you do not have permission to start a new preview for this pull request.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := derivePRPreviewLaunch(tt.resp, tt.opts)

			require.Equal(t, tt.expected, *actual, "launch decision should match the PR preview state")
		})
	}
}

func TestBranchPreviewRuntimeMatchesWorkerRequirements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]string
		req      preview.WorkerSelectionRequirements
		expected bool
	}{
		{
			name:     "static egress required rejects legacy direct runtime",
			metadata: nil,
			req:      preview.WorkerSelectionRequirements{StaticEgressRequired: true},
			expected: false,
		},
		{
			name:     "static egress required accepts static runtime",
			metadata: map[string]string{agent.SandboxMetadataEgressMode: agent.SandboxEgressModeStatic},
			req:      preview.WorkerSelectionRequirements{StaticEgressRequired: true},
			expected: true,
		},
		{
			name:     "direct egress rejects static runtime after setting is disabled",
			metadata: map[string]string{agent.SandboxMetadataEgressMode: agent.SandboxEgressModeStatic},
			req:      preview.WorkerSelectionRequirements{},
			expected: false,
		},
		{
			name:     "direct egress accepts legacy direct runtime",
			metadata: nil,
			req:      preview.WorkerSelectionRequirements{},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sandboxBytes, err := json.Marshal(agent.Sandbox{ID: "sandbox-1", Provider: "docker", Metadata: tt.metadata})
			require.NoError(t, err, "test sandbox should marshal")
			instance := &models.PreviewInstance{RecycleSandbox: sandboxBytes}

			actual := branchPreviewRuntimeMatchesWorkerRequirements(instance, tt.req)

			require.Equal(t, tt.expected, actual, "preview runtime reuse should match the current network requirement")
		})
	}
}

func TestBranchPreviewHandler_GetPullRequestRejectsPreviewTokenWithoutReadScope(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil, "https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: "0123456789abcdef0123456789abcdef01234567"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/github/acme/app/pull/7", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("owner", "acme")
	rctx.URLParams.Add("repo", "app")
	rctx.URLParams.Add("number", "7")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
		OrgID:         orgID,
		Scopes:        []string{"previews:create"},
		RepositoryIDs: []uuid.UUID{repoID},
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.GetPullRequest(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "GetPullRequest should reject preview API tokens without read scope")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_GetPullRequestStatusIntentDoesNotCreateOrStart(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()
	head := "0123456789abcdef0123456789abcdef01234567"
	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}
	repoRow := []any{repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil, "https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now}
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(repoRow...))
	mock.ExpectQuery("SELECT .+ FROM preview_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewLinkTestCols))
	mock.ExpectQuery("SELECT .+ FROM preview_targets").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols))
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(repoRow...))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: head},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/github/acme/app/pull/7?intent=status", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("owner", "acme")
	rctx.URLParams.Add("repo", "app")
	rctx.URLParams.Add("number", "7")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.GetPullRequest(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "status intent should return a launch decision instead of creating a preview")
	var resp models.SingleResponse[branchPreviewResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "status intent response should be valid JSON")
	require.Equal(t, "target_created", resp.Data.Status, "status intent should describe the current target state without creating a target")
	require.NotNil(t, resp.Data.Launch, "status intent should still return a launch decision")
	require.Equal(t, models.PreviewLaunchActionStart, resp.Data.Launch.Action, "status intent should describe that opening would start a preview")
	require.False(t, resp.Data.Launch.AutoOpen, "status intent must not auto-open because it was not a launch gesture")
	require.NoError(t, mock.ExpectationsWereMet(), "status intent must not create targets, links, or jobs")
}

func TestBranchPreviewHandler_GetPullRequestUsesStableLinkForNamedConfigTarget(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	targetID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	head := "0123456789abcdef0123456789abcdef01234567"
	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}
	repoRow := []any{repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil, "https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now}
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(repoRow...))
	mock.ExpectQuery("SELECT .+ FROM preview_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewLinkTestCols).AddRow(
			linkID, orgID, targetID, string(models.PreviewLinkTypePullRequest), "github/acme/app/pull/7", &repoID, ptrToInt(7), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoID, "feature/previews", head, "web", "", string(models.PreviewSourceTypePullRequest),
			"acme/app#7@"+head, "https://github.com/acme/app/pull/7", userID, nil, nil, now,
		))
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols))
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(repoRow...))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: head},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/github/acme/app/pull/7?intent=status", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("owner", "acme")
	rctx.URLParams.Add("repo", "app")
	rctx.URLParams.Add("number", "7")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.GetPullRequest(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "status intent should resolve the linked PR target")
	var resp models.SingleResponse[branchPreviewResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "linked target response should be valid JSON")
	require.Equal(t, targetID, resp.Data.TargetID, "stable PR link should preserve the linked target even when it has a named config")
	require.Equal(t, "web", resp.Data.PreviewConfigName, "stable PR link should preserve the target preview config name")
	require.Equal(t, models.PreviewLaunchActionStart, resp.Data.Launch.Action, "linked target without runtime should still describe the start action")
	require.NoError(t, mock.ExpectationsWereMet(), "linked target lookup must not fall back to creating another target")
}

func TestBranchPreviewHandler_GetPullRequestReturnsBlockedLaunchForConfigProblems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		configContent  string
		expectedReason models.PreviewLaunchReason
	}{
		{
			name: "multiple preview configs require selection",
			configContent: `{"preview":{"configs":{
				"api":{"name":"api","command":["go","run","."],"port":8080},
				"web":{"name":"web","command":["npm","run","dev"],"port":3000}
			}}}`,
			expectedReason: models.PreviewLaunchReasonConfigRequired,
		},
		{
			name:           "invalid preview config blocks before launch",
			configContent:  `{"preview":`,
			expectedReason: models.PreviewLaunchReasonConfigInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgx mock should initialize")
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			repoID := uuid.New()
			integrationID := uuid.New()
			now := time.Now()
			head := "0123456789abcdef0123456789abcdef01234567"
			repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}
			repoRow := []any{repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil, "https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now}
			mock.ExpectQuery("SELECT .+ FROM repositories").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(repoCols).AddRow(repoRow...))
			mock.ExpectQuery("SELECT .+ FROM preview_links").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(branchPreviewLinkTestCols))
			mock.ExpectQuery("SELECT .+ FROM preview_targets").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols))
			mock.ExpectQuery("SELECT .+ FROM repositories").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(repoCols).AddRow(repoRow...))

			handler := NewBranchPreviewHandler(
				db.NewPreviewStore(mock),
				db.NewRepositoryStore(mock),
				fakeBranchPreviewGitHub{token: "ghs_test", head: head, configContent: tt.configContent},
				nil,
				"https://app.143.dev",
				"https://{id}.preview.143.dev",
			)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/github/acme/app/pull/7", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("owner", "acme")
			rctx.URLParams.Add("repo", "app")
			rctx.URLParams.Add("number", "7")
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()

			handler.GetPullRequest(rr, req)

			require.Equal(t, http.StatusOK, rr.Code, "config problems should render the stable PR page with blocked launch guidance")
			var resp models.SingleResponse[branchPreviewResponse]
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "blocked launch response should be valid JSON")
			require.NotNil(t, resp.Data.Launch, "blocked launch response should include launch decision")
			require.Equal(t, models.PreviewLaunchActionBlocked, resp.Data.Launch.Action, "config problem should block launch")
			require.Equal(t, tt.expectedReason, resp.Data.Launch.Reason, "blocked launch should describe the config problem")
			require.False(t, resp.Data.Launch.AutoOpen, "config-blocked previews must not auto-open")
			require.NoError(t, mock.ExpectationsWereMet(), "config-blocked launch must not create targets, links, or jobs")
		})
	}
}

func TestBranchPreviewHandler_CreateRejectsAmbiguousPreviewConfig(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()
	head := "0123456789abcdef0123456789abcdef01234567"

	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil, "https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{
			token: "ghs_test",
			head:  head,
			configContent: `{"preview":{"configs":{
				"api":{"name":"api","command":["go","run","."],"port":8080},
				"web":{"name":"web","command":["npm","run","dev"],"port":3000}
			}}}`,
		},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	body := bytes.NewBufferString(`{"repository_id":"` + repoID.String() + `","branch":"feature/previews"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews", body)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "Create should reject ambiguous committed preview configs before creating a target")
	require.Contains(t, rr.Body.String(), "available configs", "Create should return the available config names")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_StopRejectsPreviewTokenWithoutStopScope(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	previewID := uuid.New()
	targetID := uuid.New()
	now := time.Now()

	// GetPreviewInstance — instance with PreviewTargetID set
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			previewID, uuid.Nil, &targetID, orgID, userID, "", "", models.PreviewStatusReady,
			"", "", "", "", 0,
			"", "", now, now, nil,
			"", 0, 0, 10240, nil, nil, "", nil, "", now, now, now, nil,
			(*int64)(nil), (*time.Time)(nil), (*int64)(nil), (*time.Time)(nil), "", "",
			false,
		))

	// GetPreviewTarget — target belonging to repoID
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoID, "feature/x", "abc123", "", "", "manual", "", "", userID, nil, nil, now,
		))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: "abc123"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/"+previewID.String()+"/stop", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", previewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
		OrgID:         orgID,
		Scopes:        []string{"previews:read"},
		RepositoryIDs: []uuid.UUID{repoID},
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Stop(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "Stop should reject preview API tokens without stop scope")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_RestartRejectsPreviewTokenWithoutCreateScope(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	previewID := uuid.New()
	targetID := uuid.New()
	now := time.Now()

	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}

	// resolveTargetRepoAndActive: GetPreviewInstance
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			previewID, uuid.Nil, &targetID, orgID, userID, "", "", models.PreviewStatusReady,
			"", "", "", "", 0,
			"", "", now, now, nil,
			"", 0, 0, 10240, nil, nil, "", nil, "", now, now, now, nil,
			(*int64)(nil), (*time.Time)(nil), (*int64)(nil), (*time.Time)(nil), "", "",
			false,
		))

	// resolveTargetRepoAndActive: GetPreviewTarget
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoID, "feature/x", "abc123", "", "", "manual", "", "", userID, nil, nil, now,
		))

	// resolveTargetRepoAndActive: repos.GetByID
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(
			repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil,
			"https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now,
		))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: "abc123"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/"+previewID.String()+"/restart", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", previewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
		OrgID:         orgID,
		Scopes:        []string{"previews:read"},
		RepositoryIDs: []uuid.UUID{repoID},
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Restart(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "Restart should reject preview API tokens without create scope")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_StartLatestRejectsPreviewTokenWithoutCreateScope(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	previewID := uuid.New()
	targetID := uuid.New()
	now := time.Now()

	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}

	// resolveTargetRepoAndActive: GetPreviewInstance
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			previewID, uuid.Nil, &targetID, orgID, userID, "", "", models.PreviewStatusReady,
			"", "", "", "", 0,
			"", "", now, now, nil,
			"", 0, 0, 10240, nil, nil, "", nil, "", now, now, now, nil,
			(*int64)(nil), (*time.Time)(nil), (*int64)(nil), (*time.Time)(nil), "", "",
			false,
		))

	// resolveTargetRepoAndActive: GetPreviewTarget
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoID, "feature/x", "abc123", "", "", "manual", "", "", userID, nil, nil, now,
		))

	// resolveTargetRepoAndActive: repos.GetByID
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(
			repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil,
			"https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now,
		))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: "abc123"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/"+previewID.String()+"/start-latest", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", previewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
		OrgID:         orgID,
		Scopes:        []string{"previews:read"},
		RepositoryIDs: []uuid.UUID{repoID},
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.StartLatest(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "StartLatest should reject preview API tokens without create scope")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_StartLatestRollsBackReservationWhenJobDedupeConflicts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	targetID := uuid.New()
	oldPreviewID := uuid.New()
	newPreviewID := uuid.New()
	runtimeID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	head := "0123456789abcdef0123456789abcdef01234567"
	workerID := "worker-1"
	workerBaseURL := "http://worker-1.internal:8080"
	workerMetadata := []byte(`{"preview_capable":true,"preview_rpc_auth_check":true,"preview_internal_base_url":"` + workerBaseURL + `"}`)
	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}

	store := db.NewPreviewStore(mock)
	manager := preview.NewManager(preview.ManagerConfig{
		Store:        store,
		Provider:     &mockPreviewProvider{},
		Logger:       zerolog.Nop(),
		MaxPerWorker: 3,
	})
	handler := NewBranchPreviewHandler(
		store,
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: head},
		manager,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)
	handler.SetWorkerRuntime(db.NewJobStore(mock), preview.NewWorkerSelectorWithMaxPerWorker(db.NewNodeStore(mock), store, 3))

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			oldPreviewID, uuid.Nil, &targetID, orgID, userID, "", "", models.PreviewStatusFailed,
			"docker", workerID, "", "webserver", 0,
			"sha256:old", head, now, now.Add(30*time.Minute), &now,
			"", 0, 0, 10240, nil, nil, "failed", nil, "build failed", now, now, now, nil,
			(*int64)(nil), (*time.Time)(nil), (*int64)(nil), (*time.Time)(nil), "", "",
			false,
		))
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoID, "feature/previews", head, "", "", "manual", "", "", userID, nil, nil, now,
		))
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(
			repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil,
			"https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now,
		))
	mock.ExpectQuery("INSERT INTO preview_links").
		WithArgs(branchPreviewAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewLinkTestCols).AddRow(
			linkID, orgID, targetID, "target", targetID.String(), &repoID, (*int)(nil), now, now,
		))
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(branchPreviewNodeTestCols).AddRow(
			workerID, models.NodeModeWorker, "worker-1.local", models.NodeStatusActive, models.DrainIntentNone, workerMetadata, now, now, nil, nil, "", "",
		))
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(branchPreviewAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}))
	mock.ExpectQuery("SELECT[\\s\\S]+user_standalone[\\s\\S]+worker_total").
		WithArgs(branchPreviewAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows([]string{"user_standalone", "org_standalone", "worker_total"}).AddRow(0, 0, 0))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols))
	mock.ExpectQuery("SELECT[\\s\\S]+user_standalone[\\s\\S]+worker_total").
		WithArgs(branchPreviewAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows([]string{"user_standalone", "org_standalone", "worker_total"}).AddRow(0, 0, 0))
	mock.ExpectQuery("INSERT INTO preview_instances").
		WithArgs(branchPreviewAnyArgs(22)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			newPreviewID, uuid.Nil, &targetID, orgID, userID, models.PreviewProfileBootstrap, "app", models.PreviewStatusStarting,
			"docker", workerID, "", "app", 0,
			"sha256:reservation", head, now, now.Add(30*time.Minute), nil,
			"/", 1024, 500, 10*1024, nil, nil, "reserved", nil, "", now, now, nil, nil,
			(*int64)(nil), (*time.Time)(nil), (*int64)(nil), (*time.Time)(nil), "", "",
			false,
		))
	mock.ExpectQuery("INSERT INTO preview_runtimes").
		WithArgs(branchPreviewAnyArgs(9)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewRuntimeTestCols).AddRow(
			runtimeID, orgID, newPreviewID, 1, workerID,
			workerBaseURL, "", 0, models.PreviewRuntimeStatusStarting, now.Add(90*time.Second),
			nil, nil, nil, "", "", now, now,
		))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(branchPreviewAnyArgs(7)...).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/"+oldPreviewID.String()+"/start-latest", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", oldPreviewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.StartLatest(rr, req)

	require.Equal(t, http.StatusConflict, rr.Code, "StartLatest should reject a deduped enqueue after reservation")
	require.Contains(t, rr.Body.String(), "PREVIEW_START_IN_PROGRESS", "StartLatest should expose a retryable startup-in-progress error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_MintBootstrapTokenRejectsPreviewTokenForDifferentRepository(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoA := uuid.New()
	repoB := uuid.New()
	previewID := uuid.New()
	targetID := uuid.New()
	now := time.Now()

	// GetPreviewInstance — instance with PreviewTargetID pointing to repoB's target
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			previewID, uuid.Nil, &targetID, orgID, userID, "", "", models.PreviewStatusReady,
			"", "", "", "", 0,
			"", "", now, now, nil,
			"", 0, 0, 10240, nil, nil, "", nil, "", now, now, now, nil,
			(*int64)(nil), (*time.Time)(nil), (*int64)(nil), (*time.Time)(nil), "", "",
			false,
		))

	// GetPreviewTarget — target with RepositoryID=repoB
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoB, "feature/x", "abc123", "", "", "manual", "", "", userID, nil, nil, now,
		))

	// A non-nil manager is required to pass the early nil-guard in MintBootstrapToken.
	// We construct a minimal one; the 403 fires before the manager is ever called.
	mgr := preview.NewManager(preview.ManagerConfig{
		Store:  db.NewPreviewStore(mock),
		Logger: zerolog.Nop(),
	})
	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test"},
		mgr,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/"+previewID.String()+"/bootstrap-token", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", previewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	// Token is scoped to repoA, but the preview's target belongs to repoB
	ctx = middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
		OrgID:         orgID,
		Scopes:        []string{"previews:read"},
		RepositoryIDs: []uuid.UUID{repoA},
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.MintBootstrapToken(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "MintBootstrapToken should reject preview API tokens scoped to a different repository")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_CreateDeduplicatesByIdempotencyKeyHeader(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	existingTargetID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	head := "0123456789abcdef0123456789abcdef01234567"

	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}

	// 1. repos.GetByID
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(
			repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil,
			"https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now,
		))

	// 2. GetPreviewTargetByIdempotencyKey — returns existing target
	mock.ExpectQuery("SELECT .+ FROM preview_targets target JOIN preview_idempotency_keys").
		WithArgs(branchPreviewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			existingTargetID, orgID, repoID, "feature/x", head, "", "", "manual", "", "", userID, nil, nil, now,
		))

	// 3. UpsertPreviewLink
	mock.ExpectQuery("INSERT INTO preview_links").
		WithArgs(branchPreviewAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewLinkTestCols).AddRow(
			linkID, orgID, existingTargetID, "target", existingTargetID.String(), &repoID, (*int)(nil), now, now,
		))

	// 4. GetActivePreviewForTarget — no active instance
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: head},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	body := bytes.NewBufferString(`{"repository_id":"` + repoID.String() + `","branch":"feature/x"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews", body)
	req.Header.Set("Idempotency-Key", "test-key-123")
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Create should return 200 on idempotency-key hit")
	var resp models.SingleResponse[branchPreviewResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Create response should be valid JSON")
	require.Equal(t, existingTargetID, resp.Data.TargetID, "Create should return the existing target ID on idempotency-key hit")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_CreateDeduplicatesBySourceExternalID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	existingTargetID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	head := "0123456789abcdef0123456789abcdef01234567"

	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}

	// 1. repos.GetByID
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(
			repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil,
			"https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now,
		))

	// 2. GetPreviewTargetBySource — returns existing target
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(branchPreviewAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			existingTargetID, orgID, repoID, "feature/x", head, "", "", "pull_request", "pr-999", "https://github.com/acme/app/pull/1", userID, nil, nil, now,
		))

	// 3. UpsertPreviewLink
	mock.ExpectQuery("INSERT INTO preview_links").
		WithArgs(branchPreviewAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewLinkTestCols).AddRow(
			linkID, orgID, existingTargetID, "target", existingTargetID.String(), &repoID, (*int)(nil), now, now,
		))

	// 4. GetActivePreviewForTarget — no active instance
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: head},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	body := bytes.NewBufferString(`{"repository_id":"` + repoID.String() + `","branch":"feature/x","source":{"type":"pull_request","external_id":"pr-999","url":"https://github.com/acme/app/pull/1"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews", body)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Create should return 200 on source external_id deduplication hit")
	var resp models.SingleResponse[branchPreviewResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Create response should be valid JSON")
	require.Equal(t, existingTargetID, resp.Data.TargetID, "Create should return the existing target ID on source deduplication hit")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_CreateReusesSessionPreviewWhenCommitSHAsMatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	targetID := uuid.New()
	sessionID := uuid.New()
	instanceID := uuid.New()
	linkID := uuid.New()
	now := time.Now()
	head := "0123456789abcdef0123456789abcdef01234567"

	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}

	// 1. repos.GetByID
	mock.ExpectQuery("SELECT .+ FROM repositories WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(
			repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil,
			"https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now,
		))

	// 2. GetPreviewTargetBySource — no existing target for this session
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(branchPreviewAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols))

	// 3. CreatePreviewTarget (INSERT INTO preview_targets) — new target with source_type=session
	mock.ExpectQuery("INSERT INTO preview_targets").
		WithArgs(branchPreviewAnyArgs(11)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoID, "feature/x", head, "", "", "session", sessionID.String(), "", userID, nil, nil, now,
		))
	groupID := uuid.New()
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(branchPreviewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("target_created"))
	mock.ExpectQuery("UPDATE preview_groups[\\s\\S]+group_kind = @group_kind").
		WithArgs(branchPreviewAnyArgs(14)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewGroupTestCols))
	mock.ExpectQuery("INSERT INTO preview_groups").
		WithArgs(branchPreviewAnyArgs(15)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewGroupTestCols).AddRow(
			groupID, orgID, repoID, models.PreviewGroupKindSource, "feature/x", "",
			(*int)(nil), models.PreviewSourceTypeSession, sessionID.String(), "", &targetID,
			head, "target_created", false, &userID, now, now,
		))
	mock.ExpectExec("UPDATE preview_targets SET preview_group_id").
		WithArgs(branchPreviewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// 4. UpsertPreviewLink
	mock.ExpectQuery("INSERT INTO preview_links").
		WithArgs(branchPreviewAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewLinkTestCols).AddRow(
			linkID, orgID, targetID, "target", targetID.String(), &repoID, (*int)(nil), now, now,
		))

	// 5. GetActivePreviewForTarget — no active instance for new target
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols))

	// 6. GetActivePreviewForSession — session preview with matching BaseCommitSHA, status=ready,
	// and a non-empty PreviewHandle so the liveness check passes.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			instanceID, sessionID, nil, orgID, userID, "", "", models.PreviewStatusReady,
			"", "", "hdl-session-1", "", 0,
			"", head, now, now, nil,
			"", 0, 0, 10240, nil, nil, "", nil, "", now, now, now, nil,
			(*int64)(nil), (*time.Time)(nil), (*int64)(nil), (*time.Time)(nil), "", "",
			false,
		))

	// 7. AttachPreviewTarget (UPDATE preview_instances SET preview_target_id) — returns attached instance
	mock.ExpectQuery("UPDATE preview_instances SET preview_target_id").
		WithArgs(branchPreviewAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			instanceID, sessionID, &targetID, orgID, userID, "", "", models.PreviewStatusReady,
			"", "", "", "", 0,
			"", head, now, now, nil,
			"", 0, 0, 10240, nil, nil, "", nil, "", now, now, now, nil,
			(*int64)(nil), (*time.Time)(nil), (*int64)(nil), (*time.Time)(nil), "", "",
			false,
		))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: head},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	body := bytes.NewBufferString(`{"repository_id":"` + repoID.String() + `","branch":"feature/x","source":{"type":"session","external_id":"` + sessionID.String() + `"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews", body)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, "Create should return 201 when a new session target is created: %s", rr.Body.String())
	var resp models.SingleResponse[branchPreviewResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Create response should be valid JSON")
	require.Equal(t, targetID, resp.Data.TargetID, "Create should return the newly created target ID")
	require.NotNil(t, resp.Data.PreviewID, "Create should return the reused preview instance ID")
	require.Equal(t, instanceID, *resp.Data.PreviewID, "Create should return the session preview instance ID that was reused")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_APITokenManagementEndpointsAreDeprecated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		path    string
		handler func(*BranchPreviewHandler, http.ResponseWriter, *http.Request)
	}{
		{
			name:    "list tokens",
			method:  http.MethodGet,
			path:    "/api/v1/previews/api-tokens",
			handler: (*BranchPreviewHandler).ListAPITokens,
		},
		{
			name:    "create token",
			method:  http.MethodPost,
			path:    "/api/v1/previews/api-tokens",
			handler: (*BranchPreviewHandler).CreateAPIToken,
		},
		{
			name:    "revoke token",
			method:  http.MethodDelete,
			path:    "/api/v1/previews/api-tokens/" + uuid.NewString(),
			handler: (*BranchPreviewHandler).RevokeAPIToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgx mock should initialize")
			defer mock.Close()

			handler := NewBranchPreviewHandler(
				db.NewPreviewStore(mock),
				db.NewRepositoryStore(mock),
				fakeBranchPreviewGitHub{},
				nil,
				"https://app.143.dev",
				"https://{id}.preview.143.dev",
			)
			handler.SetAPITokenStore(db.NewPreviewAPITokenStore(mock))

			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(`{"name":"ci-token","scopes":["previews:read"]}`))
			req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
			rr := httptest.NewRecorder()

			tt.handler(handler, rr, req)

			require.Equal(t, http.StatusGone, rr.Code, "deprecated preview token management endpoint should return 410")
			require.Contains(t, rr.Body.String(), "PREVIEW_API_TOKENS_DEPRECATED", "response should identify the deprecation")
			require.Contains(t, rr.Body.String(), "External API", "response should direct callers to external API tokens")
			require.NoError(t, mock.ExpectationsWereMet(), "deprecated endpoint should not query the preview token store")
		})
	}
}

func TestBranchPreviewHandler_UpdatePolicyEmitsAudit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	policyID := uuid.New()
	now := time.Now()

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)
	handler.SetAuditEmitter(newAuditEmitterForTest(mock))

	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, (*string)(nil), (*string)(nil), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
	mock.ExpectQuery("SELECT .+ FROM repository_preview_policies").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryPreviewPolicyTestCols()))
	mock.ExpectQuery("INSERT INTO repository_preview_policies").
		WithArgs(previewHandlerAnyArgs(10)...).
		WillReturnRows(pgxmock.NewRows(repositoryPreviewPolicyTestCols()).
			AddRow(policyID, orgID, repoID, string(models.PreviewAutoModeWarm), string(models.PreviewSessionPrewarmModeOff), false, false, true, true, "", userID, now, now))
	expectAuditInsert(mock)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/repositories/"+repoID.String()+"/preview-policy", bytes.NewBufferString(`{"auto_mode":"warm"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("repository_id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.UpdatePolicy(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "UpdatePolicy should update the repository policy")
	require.Contains(t, rr.Body.String(), `"auto_mode":"warm"`, "UpdatePolicy should return the updated mode")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_UpdatePolicyRejectsMissingSelectedGitHubPermission(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	details := ghservice.InstallationDetails{}
	details.Permissions.Issues = "write"
	details.Permissions.Statuses = "read"

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHubWithDetails{details: details},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, (*string)(nil), (*string)(nil), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
	mock.ExpectQuery("SELECT .+ FROM repository_preview_policies").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryPreviewPolicyTestCols()))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/repositories/"+repoID.String()+"/preview-policy", bytes.NewBufferString(`{"pr_preview_surfaces_enabled":true}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("repository_id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.UpdatePolicy(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "UpdatePolicy should reject enabled status surfacing without statuses write permission")
	require.Contains(t, rr.Body.String(), "GITHUB_PERMISSION_MISSING", "response should identify the missing GitHub permission")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_UpdatePolicyRequiresBothGitHubPermissionsForStalePolicy(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	policyID := uuid.New()
	now := time.Now()
	details := ghservice.InstallationDetails{}
	details.Permissions.Issues = "write"
	details.Permissions.Statuses = "read"

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHubWithDetails{details: details},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, (*string)(nil), (*string)(nil), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
	mock.ExpectQuery("SELECT .+ FROM repository_preview_policies").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryPreviewPolicyTestCols()).
			AddRow(policyID, orgID, repoID, string(models.PreviewAutoModeOff), string(models.PreviewSessionPrewarmModeOff), false, false, true, false, "", userID, now, now))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/repositories/"+repoID.String()+"/preview-policy", bytes.NewBufferString(`{"pr_preview_surfaces_enabled":true}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("repository_id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.UpdatePolicy(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "UpdatePolicy should require commit status permission even when an old policy row disabled statuses")
	require.Contains(t, rr.Body.String(), "GITHUB_PERMISSION_MISSING", "response should identify the missing GitHub permission")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_UpdatePolicySessionPrewarmOnlyPreservesAutoMode(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	policyID := uuid.New()
	now := time.Now()

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)
	handler.SetAuditEmitter(newAuditEmitterForTest(mock))

	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, (*string)(nil), (*string)(nil), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
	mock.ExpectQuery("SELECT .+ FROM repository_preview_policies").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryPreviewPolicyTestCols()).
			AddRow(policyID, orgID, repoID, string(models.PreviewAutoModeWarm), string(models.PreviewSessionPrewarmModeOff), false, false, true, true, "", userID, now, now))
	mock.ExpectQuery("INSERT INTO repository_preview_policies").
		WithArgs(previewHandlerAnyArgs(10)...).
		WillReturnRows(pgxmock.NewRows(repositoryPreviewPolicyTestCols()).
			AddRow(policyID, orgID, repoID, string(models.PreviewAutoModeWarm), string(models.PreviewSessionPrewarmModeSmart), false, false, true, true, "", userID, now, now))
	expectAuditInsert(mock)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/repositories/"+repoID.String()+"/preview-policy", bytes.NewBufferString(`{"session_prewarm_mode":"smart"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("repository_id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.UpdatePolicy(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "UpdatePolicy should update only the session prewarm mode")
	require.Contains(t, rr.Body.String(), `"auto_mode":"warm"`, "UpdatePolicy should preserve the existing auto-preview mode")
	require.Contains(t, rr.Body.String(), `"session_prewarm_mode":"smart"`, "UpdatePolicy should return the updated session prewarm mode")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_UpdatePolicyUntrustedForkOnlyPreservesModes(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	policyID := uuid.New()
	now := time.Now()

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)
	handler.SetAuditEmitter(newAuditEmitterForTest(mock))

	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, (*string)(nil), (*string)(nil), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
	mock.ExpectQuery("SELECT .+ FROM repository_preview_policies").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryPreviewPolicyTestCols()).
			AddRow(policyID, orgID, repoID, string(models.PreviewAutoModeWarm), string(models.PreviewSessionPrewarmModeSmart), false, false, true, true, "", userID, now, now))
	mock.ExpectQuery("INSERT INTO repository_preview_policies").
		WithArgs(previewHandlerAnyArgs(10)...).
		WillReturnRows(pgxmock.NewRows(repositoryPreviewPolicyTestCols()).
			AddRow(policyID, orgID, repoID, string(models.PreviewAutoModeWarm), string(models.PreviewSessionPrewarmModeSmart), true, false, true, true, "", userID, now, now))
	expectAuditInsert(mock)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/repositories/"+repoID.String()+"/preview-policy", bytes.NewBufferString(`{"session_prewarm_untrusted_fork":true}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("repository_id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.UpdatePolicy(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "UpdatePolicy should update only the untrusted fork policy")
	require.Contains(t, rr.Body.String(), `"auto_mode":"warm"`, "UpdatePolicy should preserve the existing auto-preview mode")
	require.Contains(t, rr.Body.String(), `"session_prewarm_mode":"smart"`, "UpdatePolicy should preserve the existing session prewarm mode")
	require.Contains(t, rr.Body.String(), `"session_prewarm_untrusted_fork":true`, "UpdatePolicy should return the updated fork policy")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_TestPolicyPreviewStartsDefaultBranchTarget(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	targetID := uuid.New()
	now := time.Now()
	headSHA := "0123456789abcdef0123456789abcdef01234567"

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "token", head: headSHA},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	mock.ExpectQuery("SELECT id, org_id, integration_id, github_id").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(repositoryTestCols()).
			AddRow(repoID, orgID, uuid.New(), int64(123), "acme/app", "main", false, (*string)(nil), (*string)(nil), "https://github.com/acme/app.git", int64(456), "active", (*time.Time)(nil), (*float64)(nil), []byte(`{}`), now, now))
	mock.ExpectQuery("SELECT .+ FROM preview_targets[\\s\\S]+source_type = @source_type").
		WithArgs(previewHandlerAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols))
	mock.ExpectQuery("INSERT INTO preview_targets").
		WithArgs(previewHandlerAnyArgs(11)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).
			AddRow(targetID, orgID, repoID, "main", headSHA, "web", "", models.PreviewSourceTypeManual, fmt.Sprintf("preview-policy-test:%s:main:%s:web", repoID, headSHA), "https://github.com/acme/app/tree/main", userID, (*string)(nil), (*uuid.UUID)(nil), now))
	expectBranchPreviewGroupUpsert(mock, orgID, repoID, targetID, userID, "main", headSHA, "web", models.PreviewSourceTypeManual, fmt.Sprintf("preview-policy-test:%s:main:%s:web", repoID, headSHA), "https://github.com/acme/app/tree/main", now)
	mock.ExpectQuery("INSERT INTO preview_links").
		WithArgs(previewHandlerAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewLinkTestCols).
			AddRow(uuid.New(), orgID, targetID, models.PreviewLinkTypeTarget, targetID.String(), &repoID, (*int)(nil), now, now))
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(previewHandlerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/"+repoID.String()+"/preview-policy/test-preview", bytes.NewBufferString(`{}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("repository_id", repoID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.TestPolicyPreview(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, "TestPolicyPreview should create a branch preview target for the default branch")
	require.Contains(t, rr.Body.String(), `"status":"target_created"`, "response should return the target-created preview when no worker runtime is configured")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestGitHubPreviewSurfacePermissionHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		permissions ghservice.InstallationPermissions
		wantComment bool
		wantStatus  bool
	}{
		{
			name:        "issues write and statuses write allow both surfaces",
			permissions: ghservice.InstallationPermissions{Issues: "write", Statuses: "write"},
			wantComment: true,
			wantStatus:  true,
		},
		{
			name:        "pull request write can publish comments",
			permissions: ghservice.InstallationPermissions{PullRequests: "write", Statuses: "read"},
			wantComment: true,
			wantStatus:  false,
		},
		{
			name:        "read permissions cannot publish surfaces",
			permissions: ghservice.InstallationPermissions{Issues: "read", PullRequests: "read", Statuses: "read"},
			wantComment: false,
			wantStatus:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantComment, githubPRCommentPermissionOK(tt.permissions), "comment permission helper should match selected GitHub permissions")
			require.Equal(t, tt.wantStatus, githubCommitStatusPermissionOK(tt.permissions), "status permission helper should match selected GitHub permissions")
		})
	}
}

func repositoryPreviewPolicyTestCols() []string {
	return []string{"id", "org_id", "repository_id", "auto_mode", "session_prewarm_mode", "session_prewarm_untrusted_fork", "pr_preview_surfaces_enabled", "github_pr_comment_enabled", "github_commit_status_enabled", "preview_config_name", "updated_by_user_id", "created_at", "updated_at"}
}

func repositoryTestCols() []string {
	return []string{
		"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description",
		"clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at",
	}
}

func TestBranchPreviewHandler_GetConfigOptionsRejectsPreviewTokenWithoutReadScope(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(repoID, orgID, integrationID, int64(123), "acme/app", "main", true, nil, nil, "https://github.com/acme/app.git", int64(456), "active", nil, nil, []byte(`{}`), now, now))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/config-options?repository_id="+repoID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
		OrgID:         orgID,
		Scopes:        []string{"previews:create"},
		RepositoryIDs: []uuid.UUID{repoID},
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.GetConfigOptions(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "GetConfigOptions should reject preview API tokens without read scope")
	require.Contains(t, rr.Body.String(), "PREVIEW_TOKEN_FORBIDDEN")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBranchPreviewHandler_ResolveLinkRejectsPreviewTokenWithoutReadScope(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	targetID := uuid.New()
	linkID := uuid.New()
	now := time.Now()

	// GetPreviewLinkBySlug
	mock.ExpectQuery("SELECT .+ FROM preview_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewLinkTestCols).AddRow(
			linkID, orgID, targetID, "target", targetID.String(), &repoID, (*int)(nil), now, now,
		))

	// GetPreviewTarget
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoID, "feature/x", "abc123", "", "", "manual", "", "", userID, nil, nil, now,
		))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/links/target/"+targetID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("link_type", "target")
	rctx.URLParams.Add("*", targetID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
		OrgID:         orgID,
		Scopes:        []string{"previews:create"},
		RepositoryIDs: []uuid.UUID{repoID},
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.ResolveLink(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "ResolveLink should reject preview API tokens without read scope")
	require.Contains(t, rr.Body.String(), "PREVIEW_TOKEN_FORBIDDEN")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBranchPreviewHandler_ListRejectsPreviewTokenForWrongRepo(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	otherRepoID := uuid.New()

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	// Token scoped to otherRepoID, request queries repoID — should be forbidden.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews?repository_id="+repoID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
		OrgID:         orgID,
		Scopes:        []string{"previews:read"},
		RepositoryIDs: []uuid.UUID{otherRepoID},
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "List should reject preview API tokens not scoped to the requested repository")
	require.Contains(t, rr.Body.String(), "PREVIEW_TOKEN_FORBIDDEN")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBranchPreviewHandler_ListSupportsLegacyAndIndexParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		tokenRepo *uuid.UUID
	}{
		{name: "legacy filters", query: "repository_id=%s&branch=feature&status=ready"},
		{name: "index filters", query: "repository_id=%s&scope=running&q=%23%34%32&limit=20"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgx mock should initialize")
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			repoID := uuid.New()
			handler := NewBranchPreviewHandler(
				db.NewPreviewStore(mock),
				db.NewRepositoryStore(mock),
				fakeBranchPreviewGitHub{},
				nil,
				"https://app.143.dev",
				"https://{id}.preview.143.dev",
			)

			mock.ExpectQuery("FROM preview_targets target[\\s\\S]+LIMIT @limit").
				WithArgs(previewHandlerAnyArgs(8)...).
				WillReturnRows(pgxmock.NewRows(branchPreviewSummaryTestCols()))
			mock.ExpectQuery("WITH target_previews AS").
				WithArgs(previewHandlerAnyArgs(5)...).
				WillReturnRows(pgxmock.NewRows([]string{"running", "resumable", "recent"}).AddRow(0, 0, 0))
			mock.ExpectQuery("COUNT\\(\\*\\)[\\s\\S]+user_id = @user_id").
				WithArgs(previewHandlerAnyArgs(2)...).
				WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
			mock.ExpectQuery("COUNT\\(\\*\\)::int[\\s\\S]+repository_preview_policies").
				WithArgs(previewHandlerAnyArgs(1)...).
				WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/previews?"+fmt.Sprintf(tt.query, repoID.String()), nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
			ctx = middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
				OrgID:         orgID,
				Scopes:        []string{"previews:read"},
				RepositoryIDs: []uuid.UUID{repoID},
			})
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()

			handler.List(rr, req)

			require.Equal(t, http.StatusOK, rr.Code, "List should accept legacy and index query shapes")
			require.Contains(t, rr.Body.String(), `"data":[]`, "List should return a list response")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestBranchPreviewHandler_ListCurrentReturnsGroupedRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	groupID := uuid.New()
	targetID := uuid.New()
	previewID := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	mock.ExpectQuery("FROM preview_groups pg[\\s\\S]+latest\\.stopped_reason = 'session_prewarm_policy'[\\s\\S]+COALESCE\\(latest\\.status, pg\\.current_status\\)[\\s\\S]+IN").
		WithArgs(branchPreviewAnyArgs(7)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewCurrentSummaryTestCols).AddRow(
			groupID, orgID, repoID, models.PreviewGroupKindBranch, "feature/shared", "",
			nil, models.PreviewSourceTypeManual, "", "", &targetID,
			"abc123", string(models.PreviewStatusReady), false, &userID, now, now,
			"acme/app", string(models.PreviewStatusReady), models.PreviewCurrentFreshnessCurrent, "abc123", &previewID,
			&now, nil, "", "", "ready", 3, 2, false, (*int)(nil),
		))
	mock.ExpectQuery("SELECT[\\s\\S]+COUNT\\(\\*\\) FILTER").
		WithArgs(branchPreviewAnyArgs(4)...).
		WillReturnRows(pgxmock.NewRows([]string{"running", "resumable", "attention", "recent"}).AddRow(1, 0, 0, 0))
	mock.ExpectQuery("COUNT\\(\\*\\)[\\s\\S]+user_id = @user_id").
		WithArgs(branchPreviewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("COUNT\\(\\*\\)::int[\\s\\S]+repository_preview_policies").
		WithArgs(branchPreviewAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/current?scope=running", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.ListCurrent(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "ListCurrent should return grouped preview rows")
	require.Contains(t, rr.Body.String(), `"preview_group_id":"`+groupID.String()+`"`, "response should expose the group ID")
	require.Contains(t, rr.Body.String(), `"primary_label":"Open"`, "response should include launch recommendation")
	require.Contains(t, rr.Body.String(), `"attention":0`, "response should include attention counts")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_GetCurrentReturnsSummary(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	groupID := uuid.New()
	targetID := uuid.New()
	previewID := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	mock.ExpectQuery("FROM preview_groups pg[\\s\\S]+pg\\.id = @group_id").
		WithArgs(branchPreviewAnyArgs(8)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewCurrentSummaryTestCols).AddRow(
			groupID, orgID, repoID, models.PreviewGroupKindBranch, "feature/get-current", "",
			nil, models.PreviewSourceTypeManual, "", "", &targetID,
			"abc123", string(models.PreviewStatusReady), false, &userID, now, now,
			"acme/app", string(models.PreviewStatusReady), models.PreviewCurrentFreshnessCurrent, "abc123", &previewID,
			&now, nil, "", "", "ready", 1, 1, false, (*int)(nil),
		))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/current/"+groupID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	req = req.WithContext(withChiParam(req.Context(), "preview_group_id", groupID.String()))
	rr := httptest.NewRecorder()

	handler.GetCurrent(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "GetCurrent should return 200 for a valid group ID")
	require.Contains(t, rr.Body.String(), `"preview_group_id":"`+groupID.String()+`"`, "response should include the group ID")
	require.Contains(t, rr.Body.String(), `"primary_label":"Open"`, "response should include launch recommendation")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_GetCurrentReturns404WhenNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	groupID := uuid.New()
	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	mock.ExpectQuery("FROM preview_groups pg[\\s\\S]+pg\\.id = @group_id").
		WithArgs(branchPreviewAnyArgs(8)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewCurrentSummaryTestCols))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/current/"+groupID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	req = req.WithContext(withChiParam(req.Context(), "preview_group_id", groupID.String()))
	rr := httptest.NewRecorder()

	handler.GetCurrent(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "GetCurrent should return 404 when the group does not exist")
	require.Contains(t, rr.Body.String(), "PREVIEW_NOT_FOUND", "response should include the not-found error code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_CurrentHistoryReturnsTargets(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	groupID := uuid.New()
	targetID := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	mock.ExpectQuery("FROM preview_groups WHERE id = @group_id").
		WithArgs(branchPreviewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewGroupTestCols).AddRow(
			groupID, orgID, repoID, models.PreviewGroupKindBranch, "feature/history", "",
			nil, models.PreviewSourceTypeManual, "", "", &targetID,
			"abc123", string(models.PreviewStatusStopped), false, &userID, now, now,
		))
	histCols := []string{"target_id", "commit_sha", "preview_config_name", "source_type", "source_id", "created_at", "is_latest_head"}
	mock.ExpectQuery("SELECT target.id AS target_id[\\s\\S]+FROM preview_targets target").
		WithArgs(branchPreviewAnyArgs(5)...).
		WillReturnRows(pgxmock.NewRows(histCols).AddRow(
			targetID, "abc123", "web", string(models.PreviewSourceTypeManual), "", now, true,
		))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/current/"+groupID.String()+"/history", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	req = req.WithContext(withChiParam(req.Context(), "preview_group_id", groupID.String()))
	rr := httptest.NewRecorder()

	handler.CurrentHistory(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "CurrentHistory should return 200")
	require.Contains(t, rr.Body.String(), targetID.String(), "response should include the target ID")
	require.Contains(t, rr.Body.String(), `"is_latest_head":true`, "response should indicate the latest head target")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_StopCurrentNoOpWhenNoActivePreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	groupID := uuid.New()
	targetID := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	// Summary has no active preview (CurrentPreviewID is nil).
	mock.ExpectQuery("FROM preview_groups pg[\\s\\S]+pg\\.id = @group_id").
		WithArgs(branchPreviewAnyArgs(8)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewCurrentSummaryTestCols).AddRow(
			groupID, orgID, repoID, models.PreviewGroupKindBranch, "feature/idle", "",
			nil, models.PreviewSourceTypeManual, "", "", &targetID,
			"abc123", string(models.PreviewStatusStopped), false, &userID, now, now,
			"acme/app", string(models.PreviewStatusStopped), models.PreviewCurrentFreshnessCurrent, "abc123", (*uuid.UUID)(nil),
			(*time.Time)(nil), &now, "user", "", "preview.stopped", 1, 1, false, (*int)(nil),
		))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/current/"+groupID.String()+"/stop", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	req = req.WithContext(withChiParam(req.Context(), "preview_group_id", groupID.String()))
	rr := httptest.NewRecorder()

	handler.StopCurrent(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "StopCurrent should return 200 when there is no active preview to stop")
	require.Contains(t, rr.Body.String(), `"preview_group_id":"`+groupID.String()+`"`, "response should include the group ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_StopCurrentReturnsErrorWithoutPreviewService(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	groupID := uuid.New()
	targetID := uuid.New()
	previewID := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	// Handler has no stopper and no manager — StopCurrent should report unavailable.
	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	mock.ExpectQuery("FROM preview_groups pg[\\s\\S]+pg\\.id = @group_id").
		WithArgs(branchPreviewAnyArgs(8)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewCurrentSummaryTestCols).AddRow(
			groupID, orgID, repoID, models.PreviewGroupKindBranch, "feature/running", "",
			nil, models.PreviewSourceTypeManual, "", "", &targetID,
			"abc123", string(models.PreviewStatusReady), false, &userID, now, now,
			"acme/app", string(models.PreviewStatusReady), models.PreviewCurrentFreshnessCurrent, "abc123", &previewID,
			&now, nil, "", "", "ready", 1, 1, false, (*int)(nil),
		))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/current/"+groupID.String()+"/stop", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	req = req.WithContext(withChiParam(req.Context(), "preview_group_id", groupID.String()))
	rr := httptest.NewRecorder()

	handler.StopCurrent(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "StopCurrent should return 500 when no preview service is configured")
	require.Contains(t, rr.Body.String(), "PREVIEW_UNAVAILABLE", "response should report the unavailable error code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func branchPreviewSummaryTestCols() []string {
	return []string{
		"target_id", "preview_id", "repository_id", "repository_full_name", "branch", "commit_sha", "preview_config_name",
		"source_type", "source_id", "source_url", "status", "created_at", "sort_created_at", "expires_at", "stopped_at", "stopped_reason",
		"current_phase", "error", "resumable", "resume_estimate_seconds",
	}
}

func previewHandlerAnyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func TestBranchPreviewExpiresAt_NilTTLUsesDefaultHardTTL(t *testing.T) {
	t.Parallel()
	before := time.Now()
	got := branchPreviewExpiresAt(nil)
	after := time.Now()
	lo := before.Add(preview.DefaultHardTTL)
	hi := after.Add(preview.DefaultHardTTL)
	require.False(t, got.Before(lo), "nil TTL expiry should be at least DefaultHardTTL from now")
	require.False(t, got.After(hi), "nil TTL expiry should be at most DefaultHardTTL from now")
}

func TestBranchPreviewExpiresAt_ZeroTTLUsesDefaultHardTTL(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	before := time.Now()
	got := branchPreviewExpiresAt(&zero)
	after := time.Now()
	lo := before.Add(preview.DefaultHardTTL)
	hi := after.Add(preview.DefaultHardTTL)
	require.False(t, got.Before(lo), "zero TTL expiry should be at least DefaultHardTTL from now")
	require.False(t, got.After(hi), "zero TTL expiry should be at most DefaultHardTTL from now")
}

func TestBranchPreviewExpiresAt_BelowMinimumClampsToMinLifetimeTTL(t *testing.T) {
	t.Parallel()
	tooShort := int64(preview.MinLifetimeTTL.Seconds()) - 1
	before := time.Now()
	got := branchPreviewExpiresAt(&tooShort)
	after := time.Now()
	lo := before.Add(preview.MinLifetimeTTL)
	hi := after.Add(preview.MinLifetimeTTL)
	require.False(t, got.Before(lo), "sub-minimum TTL should be clamped to MinLifetimeTTL (lower bound)")
	require.False(t, got.After(hi), "sub-minimum TTL should be clamped to MinLifetimeTTL (upper bound)")
}

func TestBranchPreviewExpiresAt_AboveMaximumClampsToDefaultMaxTTL(t *testing.T) {
	t.Parallel()
	tooLong := int64(preview.DefaultMaxTTL.Seconds()) + 1
	before := time.Now()
	got := branchPreviewExpiresAt(&tooLong)
	after := time.Now()
	lo := before.Add(preview.DefaultMaxTTL)
	hi := after.Add(preview.DefaultMaxTTL)
	require.False(t, got.Before(lo), "over-maximum TTL should be clamped to DefaultMaxTTL (lower bound)")
	require.False(t, got.After(hi), "over-maximum TTL should be clamped to DefaultMaxTTL (upper bound)")
}

func TestBranchPreviewExpiresAt_WithinRangePassesThrough(t *testing.T) {
	t.Parallel()
	mid := int64((preview.MinLifetimeTTL + preview.DefaultMaxTTL) / 2 / time.Second)
	midDuration := time.Duration(mid) * time.Second
	before := time.Now()
	got := branchPreviewExpiresAt(&mid)
	after := time.Now()
	lo := before.Add(midDuration)
	hi := after.Add(midDuration)
	require.False(t, got.Before(lo), "mid-range TTL should pass through unchanged (lower bound)")
	require.False(t, got.After(hi), "mid-range TTL should pass through unchanged (upper bound)")
}

func TestBranchPreviewExpiresAt_ExactMinimumPassesThrough(t *testing.T) {
	t.Parallel()
	exact := int64(preview.MinLifetimeTTL.Seconds())
	before := time.Now()
	got := branchPreviewExpiresAt(&exact)
	after := time.Now()
	lo := before.Add(preview.MinLifetimeTTL)
	hi := after.Add(preview.MinLifetimeTTL)
	require.False(t, got.Before(lo), "exact minimum TTL should pass through (lower bound)")
	require.False(t, got.After(hi), "exact minimum TTL should pass through (upper bound)")
}

// TestBranchPreviewHandler_StopFailsClosedOnPreviewTargetDBError verifies that
// when GetPreviewTarget returns a non-ErrNoRows error, Stop returns 500 rather
// than silently skipping the scope check and proceeding with the stop.
func TestBranchPreviewHandler_StopFailsClosedOnPreviewTargetDBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	targetID := uuid.New()
	now := time.Now()

	// GetPreviewInstance succeeds with PreviewTargetID set.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			previewID, uuid.Nil, &targetID, orgID, userID, "", "", models.PreviewStatusReady,
			"", "", "", "", 0,
			"", "", now, now, nil,
			"", 0, 0, 10240, nil, nil, "", nil, "", now, now, now, nil,
			(*int64)(nil), (*time.Time)(nil), (*int64)(nil), (*time.Time)(nil), "", "",
			false,
		))

	// GetPreviewTarget returns a non-ErrNoRows DB error.
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("connection refused"))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/"+previewID.String()+"/stop", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", previewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Stop(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "Stop should return 500 when GetPreviewTarget fails with a non-ErrNoRows error")
	require.Contains(t, rr.Body.String(), "PREVIEW_TARGET_LOOKUP_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBranchPreviewHandler_DecoratePreviewResponsePopulatesRepoMetadata verifies
// that decoratePreviewResponse fills in RepositoryFullName and GitHubBranchURL
// from the repos store when RepositoryID and Branch are set.
func TestBranchPreviewHandler_DecoratePreviewResponsePopulatesRepoMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	integrationID := uuid.New()
	now := time.Now()

	repoCols := []string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoCols).AddRow(
			repoID, orgID, integrationID, int64(42), "acme/app", "main", false, nil, nil,
			"https://github.com/acme/app.git", int64(1), "active", nil, nil, []byte(`{}`), now, now,
		))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	resp := &branchPreviewResponse{
		RepositoryID: repoID,
		Branch:       "feature/x",
		Status:       "target_created",
		// PreviewID is nil → decoratePreviewResponse skips service/infra DB calls
	}
	handler.decoratePreviewResponse(context.Background(), orgID, resp)

	require.Equal(t, "acme/app", resp.RepositoryFullName, "decoratePreviewResponse should populate RepositoryFullName from repos store")
	require.Equal(t, "https://github.com/acme/app/tree/feature/x", resp.GitHubBranchURL, "decoratePreviewResponse should populate GitHubBranchURL")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBranchPreviewExpiresAt_ExactMaximumPassesThrough(t *testing.T) {
	t.Parallel()
	exact := int64(preview.DefaultMaxTTL.Seconds())
	before := time.Now()
	got := branchPreviewExpiresAt(&exact)
	after := time.Now()
	lo := before.Add(preview.DefaultMaxTTL)
	hi := after.Add(preview.DefaultMaxTTL)
	require.False(t, got.Before(lo), "exact maximum TTL should pass through (lower bound)")
	require.False(t, got.After(hi), "exact maximum TTL should pass through (upper bound)")
}

type fakeSessionPreviewRestarter struct {
	instance   *models.PreviewInstance
	action     string
	err        *previewHTTPError
	gotOrg     uuid.UUID
	gotUser    uuid.UUID
	gotSession uuid.UUID
	calls      int
}

func (f *fakeSessionPreviewRestarter) RestartSessionPreview(_ context.Context, orgID, userID, sessionID uuid.UUID, _ startPreviewRequest) (*models.PreviewInstance, string, *previewHTTPError) {
	f.calls++
	f.gotOrg, f.gotUser, f.gotSession = orgID, userID, sessionID
	if f.err != nil {
		return nil, "", f.err
	}
	return f.instance, f.action, nil
}

func sessionPreviewInstanceRow(previewID, sessionID, orgID, userID uuid.UUID, status models.PreviewStatus, now time.Time, stoppedAt *time.Time) []any {
	return []any{
		previewID, sessionID, (*uuid.UUID)(nil), orgID, userID, "", "", status,
		"", "", "", "", 0,
		"", "", now, now, stoppedAt,
		"", 0, 0, 10240, nil, nil, "", nil, "", now, now, now, nil,
		(*int64)(nil), (*time.Time)(nil), (*int64)(nil), (*time.Time)(nil), "",
		"",
		false,
	}
}

func TestBranchPreviewHandler_RestartDelegatesSessionPreviews(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	oldPreviewID := uuid.New()
	newPreviewID := uuid.New()
	now := time.Now()
	stoppedAt := now.Add(-10 * time.Minute)

	// resolveTargetRepoAndActive: GetPreviewInstance → stopped session preview
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			sessionPreviewInstanceRow(oldPreviewID, sessionID, orgID, userID, models.PreviewStatusStopped, now, &stoppedAt)...,
		))

	restarter := &fakeSessionPreviewRestarter{
		instance: &models.PreviewInstance{
			ID:        newPreviewID,
			SessionID: sessionID,
			OrgID:     orgID,
			Status:    models.PreviewStatusStarting,
			ExpiresAt: now.Add(30 * time.Minute),
		},
		action: "started",
	}

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: "abc123"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)
	handler.SetSessionPreviewRestarter(restarter)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/"+oldPreviewID.String()+"/restart", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", oldPreviewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Restart(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Restart should delegate session previews to the session restart flow")
	require.Equal(t, 1, restarter.calls, "the session restarter should be invoked exactly once")
	require.Equal(t, sessionID, restarter.gotSession, "the restarter should receive the preview's session ID")
	require.Equal(t, userID, restarter.gotUser, "the restarter should receive the requesting user")

	var resp models.SingleResponse[branchPreviewResponse]
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "Restart should return a preview response")
	require.NotNil(t, resp.Data.PreviewID, "Restart should return the resulting instance ID")
	require.Equal(t, newPreviewID, *resp.Data.PreviewID, "Restart should surface the fresh instance so pollers can follow it")
	require.Equal(t, string(models.PreviewStatusStarting), resp.Data.Status, "Restart should surface the fresh instance status")
	require.NotNil(t, resp.Data.PreviewURL, "Restart should include the new preview URL")
	require.Contains(t, *resp.Data.PreviewURL, newPreviewID.String(), "the preview URL should point at the fresh instance host")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_RestartSessionPreviewWithoutRestarterConflicts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			sessionPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusStopped, now, nil)...,
		))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: "abc123"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/"+previewID.String()+"/restart", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", previewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Restart(rr, req)

	require.Equal(t, http.StatusConflict, rr.Code, "Restart without a wired session restarter should keep the no-target conflict")
	require.Contains(t, rr.Body.String(), "PREVIEW_HAS_NO_TARGET", "the conflict should carry the no-target code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_RestartSessionPreviewRejectsPreviewAPIToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			sessionPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusStopped, now, nil)...,
		))

	restarter := &fakeSessionPreviewRestarter{}
	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: "abc123"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)
	handler.SetSessionPreviewRestarter(restarter)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/previews/"+previewID.String()+"/restart", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", previewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
		OrgID:  orgID,
		Scopes: []string{"previews:read", "previews:create", "previews:stop"},
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Restart(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "preview API tokens must not drive session preview restarts")
	require.Equal(t, 0, restarter.calls, "the session restarter should not be invoked for token-authenticated requests")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_SelectWorkerForRestart_DegradesWhenSnapshotWorkerAtCapacity(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	targetID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	warmWorker := "worker-a-warm"
	fallbackWorker := "worker-z-fallback"
	workerMetadata := func(baseURL string) []byte {
		return []byte(fmt.Sprintf(`{"preview_capable":true,"preview_rpc_auth_check":true,"preview_internal_base_url":%q}`, baseURL))
	}

	store := db.NewPreviewStore(mock)
	manager := preview.NewManager(preview.ManagerConfig{
		Store:        store,
		Provider:     &mockPreviewProvider{},
		Logger:       zerolog.Nop(),
		MaxPerWorker: 3,
	})
	handler := NewBranchPreviewHandler(
		store,
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		manager,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)
	handler.SetWorkerRuntime(db.NewJobStore(mock), preview.NewWorkerSelectorWithMaxPerWorker(db.NewNodeStore(mock), store, 3))

	mock.ExpectQuery("FROM preview_startup_cache cache").
		WithArgs(branchPreviewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(branchPreviewStartupCacheTestCols).
				AddRow(uuid.New(), orgID, repoID, "snapshot-key", "base-key", "abcdef1", "/cache/snapshot.tar.gz", int64(1024), warmWorker, now, now),
		)
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
		WithArgs(branchPreviewAnyArgs(1)...).
		WillReturnRows(
			pgxmock.NewRows(branchPreviewNodeTestCols).
				AddRow(warmWorker, models.NodeModeWorker, "warm.local", models.NodeStatusActive, models.DrainIntentNone, workerMetadata("http://warm.local"), now, now, nil, nil, "", ""),
		)
	mock.ExpectQuery("SELECT[\\s\\S]+user_standalone[\\s\\S]+worker_total").
		WithArgs(branchPreviewAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows([]string{"user_standalone", "org_standalone", "worker_total"}).AddRow(0, 0, 3))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(
			pgxmock.NewRows(branchPreviewNodeTestCols).
				AddRow(warmWorker, models.NodeModeWorker, "warm.local", models.NodeStatusActive, models.DrainIntentNone, workerMetadata("http://warm.local"), now, now, nil, nil, "", "").
				AddRow(fallbackWorker, models.NodeModeWorker, "fallback.local", models.NodeStatusActive, models.DrainIntentNone, workerMetadata("http://fallback.local"), now, now, nil, nil, "", ""),
		)
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(branchPreviewAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}))
	mock.ExpectQuery("SELECT[\\s\\S]+user_standalone[\\s\\S]+worker_total").
		WithArgs(branchPreviewAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows([]string{"user_standalone", "org_standalone", "worker_total"}).AddRow(0, 0, 0))

	worker, err := handler.selectBranchPreviewWorkerForStart(context.Background(), orgID, userID, targetID, true, preview.WorkerSelectionRequirements{})

	require.NoError(t, err, "restart worker selection should degrade when the snapshot worker is at capacity")
	require.Equal(t, fallbackWorker, worker.ID, "restart should fall back to a normal available worker")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_DecoratePreviewResponseAddsResumability(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	targetID := uuid.New()
	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		nil,
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)
	resp := branchPreviewResponse{
		TargetID:  targetID,
		Status:    string(models.PreviewStatusStopped),
		StableURL: "https://app.143.dev/previews/" + targetID.String(),
	}

	mock.ExpectQuery("preview_startup_cache[\\s\\S]+JOIN nodes[\\s\\S]+n\\.status = 'active'").
		WithArgs(branchPreviewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"resumable"}).AddRow(true))

	handler.decoratePreviewResponse(context.Background(), orgID, &resp)

	require.True(t, resp.Resumable, "stopped preview response should be marked resumable when a startup snapshot exists")
	require.NotNil(t, resp.ResumeEstimateSeconds, "resumable preview response should include an estimate")
	require.Equal(t, 30, *resp.ResumeEstimateSeconds, "resumable preview response should use the warm resume estimate")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_GetFollowsActiveSessionPreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	oldPreviewID := uuid.New()
	newPreviewID := uuid.New()
	now := time.Now()
	stoppedAt := now.Add(-10 * time.Minute)

	// Get: GetPreviewInstance → old stopped session preview
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			sessionPreviewInstanceRow(oldPreviewID, sessionID, orgID, userID, models.PreviewStatusStopped, now, &stoppedAt)...,
		))
	// Get: GetActivePreviewForSession → fresh starting instance for the session
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			sessionPreviewInstanceRow(newPreviewID, sessionID, orgID, userID, models.PreviewStatusStarting, now, nil)...,
		))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: "abc123"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/"+oldPreviewID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", oldPreviewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Get should succeed for a stopped session preview")
	var resp models.SingleResponse[branchPreviewResponse]
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "Get should return a preview response")
	require.NotNil(t, resp.Data.PreviewID, "Get should return an instance ID")
	require.Equal(t, newPreviewID, *resp.Data.PreviewID, "Get should follow the session's current active preview so pollers converge on the replacement")
	require.Equal(t, string(models.PreviewStatusStarting), resp.Data.Status, "Get should surface the replacement's status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_GetEnrichesSessionPreviewMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	expiresAt := now.Add(30 * time.Minute)
	branch := "143/7c2ade9e/the-preview-failure-looks-like-dependency-installation-is"
	commit := "99d637f392f9051fdb9d5b7d9304ce4fab607b79"

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			sessionPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusUnavailable, now, &now)...,
		))
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols))
	mock.ExpectQuery("JOIN sessions sess ON sess\\.id = pi\\.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewSummaryTestCols()).AddRow(
			previewID, &previewID, repoID, "assembledhq/assembled", branch, commit,
			"", models.PreviewSourceTypeSession, sessionID.String(), "", "unavailable", now,
			now, &expiresAt, &now, "", "unavailable", "preview runtime endpoint mismatch",
			false, (*int)(nil),
		))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{token: "ghs_test", head: "abc123"},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/"+previewID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("preview_id", previewID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Get should succeed for a session preview")
	var resp models.SingleResponse[branchPreviewResponse]
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "Get should return a preview response")
	require.Equal(t, previewID, resp.Data.TargetID, "session preview detail should use the runtime preview ID as the target ID")
	require.Equal(t, repoID, resp.Data.RepositoryID, "session preview detail should include the owning session repository ID")
	require.Equal(t, "assembledhq/assembled", resp.Data.RepositoryFullName, "session preview detail should include the repository full name")
	require.Equal(t, branch, resp.Data.Branch, "session preview detail should include the session branch")
	require.Equal(t, commit, resp.Data.CommitSHA, "session preview detail should include the session base commit")
	require.Equal(t, models.PreviewSourceTypeSession, resp.Data.SourceType, "session preview detail should identify the source as a session")
	require.Equal(t, sessionID.String(), resp.Data.SourceID, "session preview detail should include the owning session ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBranchPreviewHandler_GetSessionPreviewRejectsTokenWithoutRepositoryAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		withAuth func(context.Context, uuid.UUID, uuid.UUID) context.Context
	}{
		{
			name: "preview API token",
			withAuth: func(ctx context.Context, orgID, otherRepoID uuid.UUID) context.Context {
				return middleware.WithPreviewAPIToken(ctx, &models.PreviewAPIToken{
					OrgID:         orgID,
					Scopes:        []string{"previews:read"},
					RepositoryIDs: []uuid.UUID{otherRepoID},
				})
			},
		},
		{
			name: "external API token",
			withAuth: func(ctx context.Context, orgID, otherRepoID uuid.UUID) context.Context {
				return middleware.WithAPIIdentity(ctx, &models.APIClient{ID: uuid.New(), OrgID: orgID}, &models.APIToken{
					ID:            uuid.New(),
					OrgID:         orgID,
					Scopes:        []string{"previews:read"},
					RepositoryIDs: []uuid.UUID{otherRepoID},
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgx mock should initialize")
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			sessionID := uuid.New()
			previewID := uuid.New()
			repoID := uuid.New()
			otherRepoID := uuid.New()
			now := time.Now()
			expiresAt := now.Add(30 * time.Minute)

			mock.ExpectQuery("SELECT .+ FROM preview_instances").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
					sessionPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusUnavailable, now, &now)...,
				))
			mock.ExpectQuery("SELECT .+ FROM preview_instances").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols))
			mock.ExpectQuery("JOIN sessions sess ON sess\\.id = pi\\.session_id").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(branchPreviewSummaryTestCols()).AddRow(
					previewID, &previewID, repoID, "assembledhq/assembled", "feature/session-preview", "abc123",
					"", models.PreviewSourceTypeSession, sessionID.String(), "", "unavailable", now,
					now, &expiresAt, &now, "", "unavailable", "preview runtime endpoint mismatch",
					false, (*int)(nil),
				))

			handler := NewBranchPreviewHandler(
				db.NewPreviewStore(mock),
				db.NewRepositoryStore(mock),
				fakeBranchPreviewGitHub{},
				nil,
				"https://app.143.dev",
				"https://{id}.preview.143.dev",
			)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/previews/"+previewID.String(), nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("preview_id", previewID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = tt.withAuth(ctx, orgID, otherRepoID)
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()

			handler.Get(rr, req)

			require.Equal(t, http.StatusForbidden, rr.Code, "Get should reject token access to session previews in another repository")
			require.Contains(t, rr.Body.String(), "PREVIEW_TOKEN_FORBIDDEN", "session preview access failure should use the preview token forbidden code")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestBranchPreviewHandler_EnrichSessionPreviewResponseReturnsCopy(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	expiresAt := now.Add(30 * time.Minute)
	previewURL := "https://" + previewID.String() + ".preview.143.dev"
	base := branchPreviewResponse{
		PreviewID:  &previewID,
		Status:     string(models.PreviewStatusUnavailable),
		Error:      "preview runtime endpoint mismatch",
		StableURL:  "https://app.143.dev/previews/" + previewID.String(),
		PreviewURL: &previewURL,
		ExpiresAt:  &expiresAt,
		StoppedAt:  &now,
	}
	original := base

	mock.ExpectQuery("JOIN sessions sess ON sess\\.id = pi\\.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewSummaryTestCols()).AddRow(
			previewID, &previewID, repoID, "assembledhq/assembled", "feature/session-preview", "abc123",
			"", models.PreviewSourceTypeSession, sessionID.String(), "", "unavailable", now,
			now, &expiresAt, &now, "", "unavailable", "preview runtime endpoint mismatch",
			false, (*int)(nil),
		))

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(mock),
		db.NewRepositoryStore(mock),
		fakeBranchPreviewGitHub{},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	enriched := handler.enrichSessionPreviewResponse(context.Background(), orgID, base)

	require.Equal(t, original, base, "enrichSessionPreviewResponse should not mutate the input response")
	require.Equal(t, previewID, enriched.TargetID, "enriched copy should use the runtime preview ID as the target ID")
	require.Equal(t, repoID, enriched.RepositoryID, "enriched copy should include the owning session repository ID")
	require.Equal(t, "assembledhq/assembled", enriched.RepositoryFullName, "enriched copy should include repository metadata")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestEnrichPreviewPolicyConfigReadiness_PreservesReadinessOnTransientError verifies that
// transient GitHub errors (token fetch failure, API outage) do not demote DB-computed
// preview readiness. A repo with proven previews should not flash "not ready" during
// temporary GitHub unavailability.
func TestEnrichPreviewPolicyConfigReadiness_PreservesReadinessOnTransientError(t *testing.T) {
	t.Parallel()

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(nil),
		db.NewRepositoryStore(nil),
		fakeBranchPreviewGitHubWithErrors{
			fakeBranchPreviewGitHub: fakeBranchPreviewGitHub{token: "ghs_test"},
			tokenErr:                fmt.Errorf("GitHub API unavailable"),
		},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	repo := models.Repository{
		FullName:      "acme/app",
		DefaultBranch: "main",
	}
	policy := &models.RepositoryPreviewPolicySummary{
		PreviewConfigured:      true,
		PreviewSuccessRecorded: true,
		PreviewReady:           true,
	}

	handler.enrichPreviewPolicyConfigReadiness(context.Background(), repo, policy)

	require.True(t, policy.PreviewReady, "transient token fetch error should not demote DB-computed preview readiness")
	require.True(t, policy.PreviewConfigured, "transient token fetch error should not demote DB-computed preview configured flag")
	require.Empty(t, policy.PreviewReadinessMissingReason, "transient error should not set a readiness missing reason")
}

// TestEnrichPreviewPolicyConfigReadiness_SetsNotConfiguredOnMissingFile verifies that
// a definitive 404 from GitHub (the config file does not exist on the default branch)
// correctly marks the repository as not configured and not ready.
func TestEnrichPreviewPolicyConfigReadiness_SetsNotConfiguredOnMissingFile(t *testing.T) {
	t.Parallel()

	handler := NewBranchPreviewHandler(
		db.NewPreviewStore(nil),
		db.NewRepositoryStore(nil),
		fakeBranchPreviewGitHubWithErrors{
			fakeBranchPreviewGitHub: fakeBranchPreviewGitHub{token: "ghs_test"},
			configErr: &ghservice.GitHubAPIError{
				Method:     http.MethodGet,
				Path:       "/repos/acme/app/contents/.143/config.json",
				StatusCode: http.StatusNotFound,
			},
		},
		nil,
		"https://app.143.dev",
		"https://{id}.preview.143.dev",
	)

	repo := models.Repository{
		FullName:      "acme/app",
		DefaultBranch: "main",
	}
	policy := &models.RepositoryPreviewPolicySummary{
		PreviewConfigured:      true,
		PreviewSuccessRecorded: true,
		PreviewReady:           true,
	}

	handler.enrichPreviewPolicyConfigReadiness(context.Background(), repo, policy)

	require.False(t, policy.PreviewConfigured, "404 on config file should mark the repository as not configured")
	require.False(t, policy.PreviewReady, "404 on config file should mark the repository as not ready")
	require.Equal(t, "Add .143/config.json first", policy.PreviewReadinessMissingReason)
}
