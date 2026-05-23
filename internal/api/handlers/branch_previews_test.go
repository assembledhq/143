package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/preview"
)

type fakeBranchPreviewGitHub struct {
	token         string
	head          string
	configContent string
}

func branchPreviewAnyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

var branchPreviewTargetTestCols = []string{
	"id", "org_id", "repository_id", "branch", "commit_sha", "preview_config_name",
	"resolved_config_digest", "source_type", "source_id", "source_url",
	"created_by_user_id", "request_id", "created_at",
}

var branchPreviewLinkTestCols = []string{
	"id", "org_id", "preview_target_id", "link_type", "slug", "repository_id",
	"pr_number", "created_at", "updated_at",
}

var branchPreviewInstanceTestCols = []string{
	"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
	"provider", "worker_node_id", "preview_handle", "primary_service", "port",
	"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
	"last_path", "memory_limit_mb", "cpu_limit_millis", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
	"preview_holding_container",
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
	return ghservice.PullRequestHead{}, nil
}

func (f fakeBranchPreviewGitHub) GetFileContent(context.Context, string, string, string, string, string) (string, error) {
	if f.configContent != "" {
		return f.configContent, nil
	}
	return `{"preview":{"name":"web","command":["npm","run","dev"],"port":3000}}`, nil
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
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(targetID, orgID, repoID, "feature/previews", head, "", "", "manual", "", "", userID, "req-1", now))

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
			"", 0, 0, nil, nil, "", "", "", now, now, now, nil,
			false,
		))

	// GetPreviewTarget — target belonging to repoID
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoID, "feature/x", "abc123", "", "", "manual", "", "", userID, "req-1", now,
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
			"", 0, 0, nil, nil, "", "", "", now, now, now, nil,
			false,
		))

	// resolveTargetRepoAndActive: GetPreviewTarget
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoID, "feature/x", "abc123", "", "", "manual", "", "", userID, "req-1", now,
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
			"", 0, 0, nil, nil, "", "", "", now, now, now, nil,
			false,
		))

	// resolveTargetRepoAndActive: GetPreviewTarget
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoID, "feature/x", "abc123", "", "", "manual", "", "", userID, "req-1", now,
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
			"", 0, 0, nil, nil, "", "", "", now, now, now, nil,
			false,
		))

	// GetPreviewTarget — target with RepositoryID=repoB
	mock.ExpectQuery("SELECT .+ FROM preview_targets WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewTargetTestCols).AddRow(
			targetID, orgID, repoB, "feature/x", "abc123", "", "", "manual", "", "", userID, "req-1", now,
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
			existingTargetID, orgID, repoID, "feature/x", head, "", "", "manual", "", "", userID, "req-1", now,
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
			existingTargetID, orgID, repoID, "feature/x", head, "", "", "pull_request", "pr-999", "https://github.com/acme/app/pull/1", userID, "req-1", now,
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
			targetID, orgID, repoID, "feature/x", head, "", "", "session", sessionID.String(), "", userID, "req-1", now,
		))

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

	// 6. GetActivePreviewForSession — session preview with matching BaseCommitSHA and status=ready
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			instanceID, sessionID, nil, orgID, userID, "", "", models.PreviewStatusReady,
			"", "", "", "", 0,
			"", head, now, now, nil,
			"", 0, 0, nil, nil, "", "", "", now, now, now, nil,
			false,
		))

	// 7. AttachPreviewTarget (UPDATE preview_instances SET preview_target_id) — returns attached instance
	mock.ExpectQuery("UPDATE preview_instances SET preview_target_id").
		WithArgs(branchPreviewAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(branchPreviewInstanceTestCols).AddRow(
			instanceID, sessionID, &targetID, orgID, userID, "", "", models.PreviewStatusReady,
			"", "", "", "", 0,
			"", head, now, now, nil,
			"", 0, 0, nil, nil, "", "", "", now, now, now, nil,
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

	require.Equal(t, http.StatusCreated, rr.Code, "Create should return 201 when a new session target is created")
	var resp models.SingleResponse[branchPreviewResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Create response should be valid JSON")
	require.Equal(t, targetID, resp.Data.TargetID, "Create should return the newly created target ID")
	require.NotNil(t, resp.Data.PreviewID, "Create should return the reused preview instance ID")
	require.Equal(t, instanceID, *resp.Data.PreviewID, "Create should return the session preview instance ID that was reused")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
