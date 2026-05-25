package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/preview"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/testutil"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// newPreviewTestHandler creates a PreviewHandler with nil manager/store
// for testing endpoints that don't need a live manager.
func newPreviewTestHandler() *PreviewHandler {
	return &PreviewHandler{
		logger: zerolog.Nop(),
	}
}

// newPreviewTestHandlerWithInspector creates a PreviewHandler with a manager
// that has a nil inspector (used to verify nil inspector returns 501).
func newPreviewTestHandlerWithManager() *PreviewHandler {
	m := preview.NewManager(preview.ManagerConfig{
		Logger:       zerolog.Nop(),
		WorkerNodeID: "test",
	})
	return &PreviewHandler{
		manager: m,
		logger:  zerolog.Nop(),
	}
}

func previewTestContext(r *http.Request) *http.Request {
	ctx := middleware.WithUser(r.Context(), &models.User{
		ID:    uuid.New(),
		OrgID: uuid.New(),
		Role:  "member",
	})
	ctx = middleware.WithOrgID(ctx, uuid.New())
	return r.WithContext(ctx)
}

func TestPreviewHandler_InspectorNotConfigured(t *testing.T) {
	t.Parallel()
	h := newPreviewTestHandlerWithManager()

	// SubmitDesignFeedback is excluded — it does not require the inspector.
	stubs := []struct {
		name    string
		method  string
		path    string
		body    string
		handler http.HandlerFunc
	}{
		{"CaptureScreenshot", http.MethodPost, "/screenshot", "{}", h.CaptureScreenshot},
		{"InspectElement", http.MethodPost, "/inspect", `{"x":10,"y":10}`, h.InspectElement},
		{"ReadConsole", http.MethodGet, "/console", "", h.ReadConsole},
		{"ExecuteInteraction", http.MethodPost, "/interact", `{"steps":[{"action":"click"}]}`, h.ExecuteInteraction},
		{"CaptureMultiViewport", http.MethodPost, "/multi-viewport", "{}", h.CaptureMultiViewport},
		{"ComputeVisualDiff", http.MethodPost, "/visual-diff", `{"before_snapshot_id":"a","after_snapshot_id":"b"}`, h.ComputeVisualDiff},
		{"RunAssertions", http.MethodPost, "/assert", `{"assertions":[{"type":"no_console_errors"}]}`, h.RunAssertions},
	}

	for _, tt := range stubs {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var bodyReader *strings.Reader
			if tt.body != "" {
				bodyReader = strings.NewReader(tt.body)
			} else {
				bodyReader = strings.NewReader("")
			}
			req := httptest.NewRequest(tt.method, tt.path, bodyReader)
			req = previewTestContext(req)
			w := httptest.NewRecorder()

			tt.handler(w, req)

			require.Equal(t, http.StatusNotImplemented, w.Code)

			var resp models.ErrorResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			require.Equal(t, "PREVIEW_INSPECTOR_NOT_AVAILABLE", resp.Error.Code)
		})
	}
}

func TestPreviewHandler_InspectorNilManager(t *testing.T) {
	t.Parallel()
	// Handler with nil manager should also return 501 gracefully.
	h := newPreviewTestHandler()

	req := httptest.NewRequest(http.MethodPost, "/screenshot", strings.NewReader("{}"))
	req = previewTestContext(req)
	w := httptest.NewRecorder()

	h.CaptureScreenshot(w, req)

	require.Equal(t, http.StatusNotImplemented, w.Code)

	var resp models.ErrorResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, "PREVIEW_INSPECTOR_NOT_AVAILABLE", resp.Error.Code)
}

func TestPreviewHandler_StartPreview_InvalidBody(t *testing.T) {
	t.Parallel()
	h := newPreviewTestHandlerWithManager()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/not-a-uuid/preview", strings.NewReader("{}"))
	req = previewTestContext(req)

	// Set up chi URL params.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.StartPreview(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPreviewHandler_ReservationPlaceholderConfig(t *testing.T) {
	t.Parallel()

	// reservationPlaceholderConfig only has to satisfy ValidateConfig so the
	// reservation row is creatable when the client doesn't supply a config.
	// The real config (workspace .143/config.json) is loaded post-hydrate;
	// this placeholder is never executed.
	cfg := reservationPlaceholderConfig()

	require.Equal(t, "placeholder", cfg.Name)
	require.Equal(t, "app", cfg.Primary)
	require.NotEmpty(t, cfg.Services["app"].Command, "placeholder must define a command so ValidateConfig passes")

	errs := preview.ValidateConfig(cfg)
	require.Empty(t, errs, "reservation placeholder must satisfy preview validation")
}

func TestPreviewHandler_ManagerNotConfigured(t *testing.T) {
	t.Parallel()
	// Handler with nil manager should return 501 for manager-dependent endpoints.
	h := newPreviewTestHandler()

	handlers := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"StartPreview", h.StartPreview},
		{"GetPreview", h.GetPreview},
		{"StopPreview", h.StopPreview},
		{"RestartPreview", h.RestartPreview},
		{"MintBootstrapToken", h.MintBootstrapToken},
	}

	for _, tt := range handlers {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader("{}"))
			req = previewTestContext(req)
			w := httptest.NewRecorder()

			tt.handler(w, req)

			require.Equal(t, http.StatusNotImplemented, w.Code)

			var resp models.ErrorResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			require.Equal(t, "PREVIEW_NOT_AVAILABLE", resp.Error.Code)
		})
	}
}

func TestPreviewHandler_HelperMethods(t *testing.T) {
	t.Parallel()

	t.Run("preview http error string handling", func(t *testing.T) {
		t.Parallel()

		var nilErr *previewHTTPError
		require.Equal(t, "", nilErr.Error(), "nil previewHTTPError should stringify to an empty string")

		msgErr := newPreviewHTTPError(http.StatusBadRequest, "BAD", "bad request", nil)
		require.Equal(t, "bad request", msgErr.Error(), "previewHTTPError should return its message when no wrapped error is present")

		wrapped := newPreviewHTTPError(http.StatusBadGateway, "BAD_GATEWAY", "gateway failed", errors.New("boom"))
		require.Equal(t, "boom", wrapped.Error(), "previewHTTPError should return the wrapped error when present")
	})

	t.Run("write preview http error variants", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = previewTestContext(req)

		rr := httptest.NewRecorder()
		writePreviewHTTPError(rr, req, nil)
		require.Equal(t, http.StatusOK, rr.Code, "writePreviewHTTPError should be a no-op for nil errors")

		rr = httptest.NewRecorder()
		writePreviewHTTPError(rr, req, newPreviewHTTPError(http.StatusBadRequest, "INVALID", "bad input", nil))
		require.Equal(t, http.StatusBadRequest, rr.Code, "writePreviewHTTPError should emit plain preview errors")

		rr = httptest.NewRecorder()
		writePreviewHTTPError(rr, req, newPreviewHTTPError(http.StatusBadGateway, "FAILED", "worker failed", errors.New("boom")))
		require.Equal(t, http.StatusBadGateway, rr.Code, "writePreviewHTTPError should emit wrapped preview errors")
		require.Contains(t, rr.Body.String(), "FAILED", "writePreviewHTTPError should preserve the error code")
	})

	t.Run("worker runtime helpers", func(t *testing.T) {
		t.Parallel()

		h := newPreviewTestHandler()
		require.False(t, h.workerRoutingEnabled(), "worker routing should be disabled until both selector and client are configured")
		require.False(t, h.isLocalWorker(preview.WorkerNode{ID: "worker-a"}), "isLocalWorker should reject non-matching worker IDs")
		require.Error(t, func() error {
			_, err := h.resolvePreviewWorker(context.Background(), "worker-a")
			return err
		}(), "resolvePreviewWorker should fail when no selector is configured")

		h.workerSelector = &preview.WorkerSelector{}
		h.workerClient = preview.NewWorkerPreviewClient("secret")
		h.localNodeID = "worker-a"
		require.True(t, h.workerRoutingEnabled(), "worker routing should be enabled when selector and client are configured")
		require.True(t, h.isLocalWorker(preview.WorkerNode{ID: "worker-a"}), "isLocalWorker should accept matching worker IDs")
	})

	t.Run("manager and worker error helpers", func(t *testing.T) {
		t.Parallel()

		h := newPreviewTestHandler()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = previewTestContext(req)

		rr := httptest.NewRecorder()
		require.False(t, h.requireManager(rr, req), "requireManager should reject handlers without a preview manager")
		require.Equal(t, http.StatusNotImplemented, rr.Code, "requireManager should return 501 when no preview manager is configured")

		rr = httptest.NewRecorder()
		h.writeWorkerClientError(rr, req, &preview.WorkerRequestError{StatusCode: http.StatusConflict, Code: "NO_SANDBOX", Message: "missing sandbox"})
		require.Equal(t, http.StatusConflict, rr.Code, "writeWorkerClientError should preserve structured worker status codes")
		require.Contains(t, rr.Body.String(), "NO_SANDBOX", "writeWorkerClientError should preserve structured worker codes")

		rr = httptest.NewRecorder()
		h.writeWorkerClientError(rr, req, errors.New("boom"))
		require.Equal(t, http.StatusBadGateway, rr.Code, "writeWorkerClientError should map generic worker failures to 502")
		require.Contains(t, rr.Body.String(), "PREVIEW_WORKER_REQUEST_FAILED", "writeWorkerClientError should emit the generic worker failure code")
	})
}

func TestPreviewHandler_DetectReadiness_NoConfig(t *testing.T) {
	t.Parallel()
	h := newPreviewTestHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos/owner/repo/preview/detect", nil)
	req = previewTestContext(req)

	w := httptest.NewRecorder()
	h.DetectReadiness(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[models.PreviewDetectionResult]
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, models.PreviewReadinessNotSupported, resp.Data.Readiness)
}

func TestPreviewHandler_DetectReadiness_WithConfig(t *testing.T) {
	t.Parallel()
	h := newPreviewTestHandler()

	// Encode a valid single-service preview config as base64url.
	config := `{
		"version": "3",
		"name": "frontend",
		"command": ["npm", "run", "dev"],
		"port": 3000,
		"ready": {"http_path": "/"},
		"credentials": {"mode": "none"},
		"network": {"mode": "managed"}
	}`
	configB64 := base64.RawURLEncoding.EncodeToString([]byte(config))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos/owner/repo/preview/detect?config="+configB64, nil)
	req = previewTestContext(req)

	w := httptest.NewRecorder()
	h.DetectReadiness(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[models.PreviewDetectionResult]
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, models.PreviewReadinessReady, resp.Data.Readiness)
	require.Equal(t, "frontend", resp.Data.PrimaryService)
	require.Contains(t, resp.Data.Services, "frontend")
}

func TestPreviewHandler_DetectReadiness_AdminSetupRequired(t *testing.T) {
	t.Parallel()
	h := newPreviewTestHandler()

	config := `{
		"version": "3",
		"name": "fullstack",
		"primary": "frontend",
		"services": {
			"frontend": {"command": ["npm", "run", "dev"], "port": 3000, "ready": {"http_path": "/"}},
			"backend": {"command": ["python", "app.py"], "port": 4000, "ready": {"http_path": "/health"}}
		},
		"credentials": {"mode": "managed_env", "credential_set": "staging", "env": ["DATABASE_URL"], "inject_into": ["backend"]},
		"network": {"mode": "managed"}
	}`
	configB64 := base64.RawURLEncoding.EncodeToString([]byte(config))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos/owner/repo/preview/detect?config="+configB64, nil)
	req = previewTestContext(req)

	w := httptest.NewRecorder()
	h.DetectReadiness(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[models.PreviewDetectionResult]
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, models.PreviewReadinessAdminSetupRequired, resp.Data.Readiness)
	require.Len(t, resp.Data.MissingCredentials, 1)
	require.Equal(t, "staging", resp.Data.MissingCredentials[0].CredentialSet)
}

func TestPreviewHandler_ExecuteInteraction_TooManySteps(t *testing.T) {
	t.Parallel()
	h := newPreviewTestHandlerWithManager()
	// Even with a manager (but no inspector), too-many-steps check should
	// fail at the inspector check first (501).
	// But let's verify with a real inspector scenario using nil inspector.

	steps := make([]map[string]string, 25) // exceeds maxInteractionSteps=20
	for i := range steps {
		steps[i] = map[string]string{"action": "click", "selector": "#btn"}
	}
	body, _ := json.Marshal(map[string]any{"steps": steps})

	req := httptest.NewRequest(http.MethodPost, "/interact", strings.NewReader(string(body)))
	req = previewTestContext(req)
	w := httptest.NewRecorder()

	h.ExecuteInteraction(w, req)

	// Without inspector configured, we get 501 before the step count check.
	require.Equal(t, http.StatusNotImplemented, w.Code)
}

// =============================================================================
// Handler tests with pgxmock store
// =============================================================================

// mockPreviewProvider is a no-op provider for handler tests.
type mockPreviewProvider struct {
	startHandle *preview.PreviewHandle
	startConfig *models.PreviewConfig
}

type previewFakeLiveSandboxCounter struct {
	count int
	calls atomic.Int64
}

func (p *previewFakeLiveSandboxCounter) CountLiveSandboxes(context.Context) (int, error) {
	p.calls.Add(1)
	return p.count, nil
}

func (m *mockPreviewProvider) StartPreview(_ context.Context, _ *agent.Sandbox, cfg *models.PreviewConfig, _ map[string]string, _ preview.ServiceObserver) (*preview.PreviewHandle, error) {
	m.startConfig = cfg
	if m.startHandle != nil {
		return m.startHandle, nil
	}
	return &preview.PreviewHandle{
		Handle:      "handle-new",
		PrimaryPort: 3000,
	}, nil
}
func (m *mockPreviewProvider) StopPreview(_ context.Context, _ string) error { return nil }
func (m *mockPreviewProvider) DialPreview(_ context.Context, _ string) (preview.PreviewStream, error) {
	return nil, nil
}
func (m *mockPreviewProvider) PreviewStatus(_ context.Context, _ string) (*preview.PreviewStatusSnapshot, error) {
	return nil, nil
}

func newPreviewHandlerWithMock(mock pgxmock.PgxPoolIface) *PreviewHandler {
	store := db.NewPreviewStore(mock)
	provider := &mockPreviewProvider{}
	mgr := preview.NewManager(preview.ManagerConfig{
		Store:        store,
		Provider:     provider,
		Logger:       zerolog.Nop(),
		WorkerNodeID: "test-worker",
	})
	return &PreviewHandler{
		manager: mgr,
		store:   store,
		logger:  zerolog.Nop(),
	}
}

// validWorkspaceConfigJSON is a minimal but well-formed .143/config.json,
// used by tests that need StartPreview to traverse past the post-hydrate
// auto-detect step (PREVIEW_NO_CONFIG) and exercise the actual launch path.
const validWorkspaceConfigJSON = `{
  "name": "test-app",
  "primary": "app",
  "services": {
    "app": {
      "command": ["echo", "ok"],
      "port": 3000,
      "ready": {"http_path": "/"}
    }
  }
}`

// previewTestContextWithIDs creates a request context with specific org/user IDs and chi URL param.
func previewTestContextWithIDs(r *http.Request, orgID, userID uuid.UUID, sessionID string) *http.Request {
	ctx := middleware.WithUser(r.Context(), &models.User{
		ID:    userID,
		OrgID: orgID,
		Role:  "member",
	})
	ctx = middleware.WithOrgID(ctx, orgID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return r.WithContext(ctx)
}

var previewInstanceTestCols = []string{
	"id", "session_id", "org_id", "user_id", "profile_name", "name", "status",
	"provider", "worker_node_id", "preview_handle", "primary_service", "port",
	"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
	"last_path", "memory_limit_mb", "cpu_limit_millis", "disk_limit_mb", "recycle_config", "recycle_sandbox", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
	"preview_holding_container",
}

var handlerPreviewServiceTestCols = []string{
	"id", "preview_instance_id", "service_name", "role", "status",
	"command", "cwd", "port", "pid", "error", "created_at",
}

var handlerPreviewInfraTestCols = []string{
	"id", "preview_instance_id", "infra_name", "template",
	"container_id", "status", "host", "port", "credentials_hash", "error", "created_at",
}

var handlerPreviewLogTestCols = []string{
	"id", "preview_instance_id", "org_id", "level", "step", "message",
	"metadata", "created_at",
}

var handlerPreviewSnapshotTestCols = []string{
	"id", "preview_instance_id", "trigger", "url_path", "blob_ref",
	"viewport_width", "viewport_height", "console_errors", "file_changes", "created_at",
}

var handlerNodeTestCols = []string{
	"id", "mode", "host", "status", "metadata", "started_at", "last_heartbeat_at",
}

// newReservedPreviewRow builds the pgxmock row that CreatePreviewInstance
// returns on a successful Reserve: 'starting' status, default Node config,
// preview_holding_container still FALSE (AcquirePreviewHold flips it in a
// separate UPDATE). Columns must match previewInstanceTestCols.
func newReservedPreviewRow(previewID, sessionID, orgID, userID uuid.UUID, now time.Time) []any {
	return []any{
		previewID, sessionID, orgID, userID, "bootstrap", "default", "starting",
		"docker", "test-worker", "", "app", 3000,
		"sha256:000", "", now, now.Add(30 * time.Minute), nil,
		"/", 512, 500, 10240, json.RawMessage("{}"), json.RawMessage("{}"), "", now, now, nil, nil,
		false,
	}
}

// expectReserveSuccess emits the pgxmock sequence for a clean ReservePreview:
// no existing active preview, all concurrency caps below their thresholds, a
// successful INSERT, and AcquirePreviewHold. Returns the preview id seeded into
// the INSERT's RETURNING row so tests can thread it into later expectations.
//
// Use this when a test's scenario requires Reserve to succeed so it can exercise
// downstream branches (hydrate, autodetect, launch failure, etc.).
func expectReserveSuccess(mock pgxmock.PgxPoolIface, sessionID, orgID, userID uuid.UUID) uuid.UUID {
	previewID := uuid.New()
	now := time.Now()

	// GetActivePreviewForSession — no active preview.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	// checkConcurrencyCaps: user (2 args: org+user), org (1 arg), worker (1 arg).
	// pgxmock treats a missing WithArgs as "expect 0 args", so the per-cap argc
	// has to match exactly or the whole Reserve fails with a generic 422.
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	// CreatePreviewInstance: 20 bound args.
	insertArgs := make([]any, 20)
	for i := range insertArgs {
		insertArgs[i] = pgxmock.AnyArg()
	}
	mock.ExpectQuery("INSERT INTO preview_instances").
		WithArgs(insertArgs...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newReservedPreviewRow(previewID, sessionID, orgID, userID, now)...),
		)
	// AcquirePreviewHold RETURNS session_id (id + org_id).
	mock.ExpectQuery("UPDATE preview_instances\\s+SET preview_holding_container = TRUE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(sessionID))

	return previewID
}

func previewAnyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

// expectAbortReservationNoDestroy emits the pgxmock sequence for an Abort that
// releases the hold without destroying a container (either the sandbox was
// reused so hydratedContainerID is "", or the turn still holds it). Callers
// that need the destroy path should build it inline.
func expectAbortReservationNoDestroy(mock pgxmock.PgxPoolIface) {
	// UpdatePreviewStatus(failed): parent status + child cascades in one transaction.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()
	// ReleasePreviewHold returns destroyNow=false because turn_holds=true.
	mock.ExpectQuery("WITH released AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(uuid.New(), "", false),
		)
}

func newActivePreviewRow(previewID, sessionID, orgID, userID uuid.UUID, now time.Time) []any {
	recycleConfig, err := json.Marshal(models.PreviewConfig{
		Name:    "my-preview",
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {
				Command: []string{"npm", "run", "dev"},
				Port:    3000,
				Ready:   models.ReadinessProbe{HTTPPath: "/"},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	recycleSandbox, err := json.Marshal(agent.Sandbox{
		ID:       "sandbox-1",
		Provider: "docker",
		WorkDir:  "/workspace",
	})
	if err != nil {
		panic(err)
	}

	return []any{
		previewID, sessionID, orgID, userID, "bootstrap", "my-preview", "ready",
		"docker", "test-worker", "handle-abc", "web", 3000,
		"sha256:abc", "deadbeef", now, now.Add(30 * time.Minute), nil,
		"/", 512, 500, 10240, recycleConfig, recycleSandbox, "", now, now, now, nil,
		false,
	}
}

func TestNewPreviewHandler(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewPreviewStore(mock)
	mgr := preview.NewManager(preview.ManagerConfig{
		Store:        store,
		Logger:       zerolog.Nop(),
		WorkerNodeID: "w1",
	})

	sessionStore := db.NewSessionStore(mock)
	repoStore := db.NewRepositoryStore(mock)
	h := NewPreviewHandler(mgr, store, sessionStore, repoStore, sandbox.NoOpFileReader{}, nil, nil, zerolog.Nop())
	require.NotNil(t, h)
	require.NotNil(t, h.manager)
	require.NotNil(t, h.store)
}

// =============================================================================
// resolveSandboxWorkDir tests
// =============================================================================

// repositoryRowColumns mirrors the SELECT projection of RepositoryStore.GetByID.
// Keep in sync with internal/db/repositories.go.
var repositoryRowColumns = []string{
	"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
	"private", "language", "description", "clone_url", "installation_id", "status",
	"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
}

// repoRow returns a minimal valid Repository row for pgxmock with the given
// full_name. Other fields hold zero/default values — only full_name matters
// for resolveSandboxWorkDir.
func repoRow(repoID, orgID uuid.UUID, fullName string) []any {
	now := time.Now()
	return []any{
		repoID, orgID, uuid.New(), int64(1), fullName, "main",
		false, (*string)(nil), (*string)(nil), "https://example.invalid/x.git",
		int64(1), "active",
		(*time.Time)(nil), (*float64)(nil), json.RawMessage(`{}`), now, now,
	}
}

func TestResolveSandboxWorkDir_NilRepoID(t *testing.T) {
	t.Parallel()

	h := &PreviewHandler{logger: zerolog.Nop()}
	got := h.resolveSandboxWorkDir(context.Background(), &models.Session{ID: uuid.New(), OrgID: uuid.New()})
	require.Equal(t, "/workspace", got, "missing RepositoryID must fall back to legacy /workspace default")
}

func TestResolveSandboxWorkDir_NilRepoStore(t *testing.T) {
	t.Parallel()

	// RepositoryID set but no repoStore wired (e.g. legacy/test handlers) —
	// must not panic; falls back to default.
	repoID := uuid.New()
	h := &PreviewHandler{logger: zerolog.Nop()}
	got := h.resolveSandboxWorkDir(context.Background(), &models.Session{
		ID: uuid.New(), OrgID: uuid.New(), RepositoryID: &repoID,
	})
	require.Equal(t, "/workspace", got, "nil repoStore must fall back rather than panic")
}

func TestResolveSandboxWorkDir_LookupSuccess(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM repositories\\s+WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repositoryRowColumns).
				AddRow(repoRow(repoID, orgID, "assembledhq/143")...),
		)

	h := &PreviewHandler{
		logger:    zerolog.Nop(),
		repoStore: db.NewRepositoryStore(mock),
	}
	got := h.resolveSandboxWorkDir(context.Background(), &models.Session{
		ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID,
	})
	// Slug for "assembledhq/143" is "143"; default HomeDir is "/home/sandbox".
	// Must match orchestrator.go's per-session WorkDir derivation so
	// readWorkspacePreviewConfig finds .143/config.json on disk.
	require.Equal(t, "/home/sandbox/143", got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveSandboxWorkDir_LookupError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM repositories\\s+WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db down"))

	h := &PreviewHandler{
		logger:    zerolog.Nop(),
		repoStore: db.NewRepositoryStore(mock),
	}
	got := h.resolveSandboxWorkDir(context.Background(), &models.Session{
		ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID,
	})
	// Losing auto-detect for one preview start is preferable to refusing to
	// hydrate at all when the DB has a transient hiccup.
	require.Equal(t, "/workspace", got, "lookup error must fall back, not bubble up")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// readWorkspacePreviewConfig tests
// =============================================================================

// fakeFileReader is a stub FileReader that returns canned ReadFile responses.
// Unused methods panic so this test file fails loudly if something else grows
// a dependency on them. lastWorkDir captures the workDir argument from the
// most recent ReadFile call so tests can assert that the handler is asking the
// reader to look in the right repo-rooted path (not the legacy /workspace).
type fakeFileReader struct {
	content        string
	err            error
	contentsByPath map[string]string
	errorsByPath   map[string]error
	lastWorkDir    *string
}

func (f *fakeFileReader) ListDir(context.Context, string, string, string) ([]sandbox.FileEntry, error) {
	panic("not used")
}

func (f *fakeFileReader) ReadFile(_ context.Context, _, workDir, path string) (string, bool, error) {
	if f.lastWorkDir != nil {
		*f.lastWorkDir = workDir
	}
	if f.contentsByPath != nil || f.errorsByPath != nil {
		content := f.contentsByPath[path]
		err := f.errorsByPath[path]
		return content, false, err
	}
	return f.content, false, f.err
}

func (f *fakeFileReader) ReadFileContext(context.Context, string, string, string, int, int, int) (sandbox.FileContextResult, error) {
	panic("not used")
}

func TestReadWorkspacePreviewConfig_NilReader(t *testing.T) {
	t.Parallel()

	h := &PreviewHandler{logger: zerolog.Nop()}
	cfg, err := h.readWorkspacePreviewConfig(context.Background(), &agent.Sandbox{}, uuid.New())
	require.NoError(t, err, "nil fileReader is the no-op case, not an infrastructure failure")
	require.Nil(t, cfg, "nil fileReader must fall through so caller uses defaults")
}

func TestReadWorkspacePreviewConfig_FileNotFound(t *testing.T) {
	t.Parallel()

	// The common "no .143/config.json committed" case: the underlying FileReader
	// returns sandbox.ErrFileNotFound (wrapped). Must NOT bubble up — caller
	// surfaces PREVIEW_NO_CONFIG instead of an infrastructure failure.
	h := &PreviewHandler{
		fileReader: &fakeFileReader{errorsByPath: map[string]error{
			repoconfig.ConfigPath: fmt.Errorf("read file %s: %w", repoconfig.ConfigPath, sandbox.ErrFileNotFound),
		}},
		logger: zerolog.Nop(),
	}
	cfg, err := h.readWorkspacePreviewConfig(context.Background(), &agent.Sandbox{ID: "c1", WorkDir: "/workspace"}, uuid.New())
	require.NoError(t, err, "ENOENT is expected absence, not an error to surface")
	require.Nil(t, cfg)
}

func TestReadWorkspacePreviewConfig_UnexpectedReadError(t *testing.T) {
	t.Parallel()

	// A non-ENOENT read error (docker exec failure, context cancel, sandbox
	// gone) means we cannot tell whether a committed config exists. Returning
	// (nil, err) makes StartPreview surface a 500 instead of guessing — the
	// caller distinguishes this from "definitively absent" (which yields
	// PREVIEW_NO_CONFIG, not 500).
	wantErr := errors.New("docker exec failed: container not running")
	h := &PreviewHandler{
		fileReader: &fakeFileReader{errorsByPath: map[string]error{
			repoconfig.ConfigPath: wantErr,
		}},
		logger: zerolog.Nop(),
	}
	cfg, err := h.readWorkspacePreviewConfig(context.Background(), &agent.Sandbox{ID: "c1", WorkDir: "/workspace"}, uuid.New())
	require.Error(t, err, "non-ENOENT read errors must surface so the caller returns 500")
	require.ErrorIs(t, err, wantErr, "underlying read error must be wrapped, not discarded")
	require.Nil(t, cfg)
}

func TestReadWorkspacePreviewConfig_ParseError(t *testing.T) {
	t.Parallel()

	// Parse errors are a user authoring problem, not missing config. Returning
	// an explicit invalid-config error lets the caller avoid the misleading
	// PREVIEW_NO_CONFIG path when the repo did commit .143/config.json.
	h := &PreviewHandler{
		fileReader: &fakeFileReader{content: "{not valid json"},
		logger:     zerolog.Nop(),
	}
	cfg, err := h.readWorkspacePreviewConfig(context.Background(), &agent.Sandbox{ID: "c1", WorkDir: "/workspace"}, uuid.New())
	require.Error(t, err, "invalid JSON must surface as invalid preview config")
	require.ErrorIs(t, err, preview.ErrInvalidConfig, "invalid workspace config should use the stable invalid-config sentinel")
	require.Nil(t, cfg, "invalid preview config should not return a fallback config")
}

func TestReadWorkspacePreviewConfig_ValidConfig(t *testing.T) {
	t.Parallel()

	raw := `{
		"name": "dogfood",
		"primary": "web",
		"services": {
			"web": {
				"command": ["npm", "run", "dev"],
				"port": 3000,
				"ready": {"http_path": "/"}
			}
		},
		"credentials": {"mode": "none"},
		"network": {"mode": "managed"}
	}`

	h := &PreviewHandler{
		fileReader: &fakeFileReader{content: raw},
		logger:     zerolog.Nop(),
	}
	cfg, err := h.readWorkspacePreviewConfig(context.Background(), &agent.Sandbox{ID: "c1", WorkDir: "/workspace"}, uuid.New())
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, "web", cfg.Primary)
	require.Contains(t, cfg.Services, "web")
	require.Equal(t, 3000, cfg.Services["web"].Port)
}

func TestReadWorkspacePreviewConfig_PrefersConfigJSON(t *testing.T) {
	t.Parallel()

	raw := `{
		"preview": {
			"name": "dogfood",
			"primary": "web",
			"services": {
				"web": {
					"command": ["npm", "run", "dev"],
					"port": 3000,
					"ready": {"http_path": "/"}
				}
			},
			"credentials": {"mode": "none"},
			"network": {"mode": "managed"}
		}
	}`

	h := &PreviewHandler{
		fileReader: &fakeFileReader{content: raw},
		logger:     zerolog.Nop(),
	}
	cfg, err := h.readWorkspacePreviewConfig(context.Background(), &agent.Sandbox{ID: "c1", WorkDir: "/workspace"}, uuid.New())
	require.NoError(t, err, "readWorkspacePreviewConfig should accept .143/config.json with a nested preview section")
	require.NotNil(t, cfg, "readWorkspacePreviewConfig should return parsed preview config from .143/config.json")
	require.Equal(t, "web", cfg.Primary, "readWorkspacePreviewConfig should parse the nested preview section from .143/config.json")
}

// sessionRowColumns mirrors db.sessionSelectColumns. Kept inline so a
// schema change in one file flips this test red instead of silently
// returning the wrong shape from pgxmock.
var sessionRowColumns = []string{
	"id", "primary_issue_id", "org_id", "origin", "interaction_mode", "validation_policy", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier",
	"container_id", "worker_node_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "reasoning_effort", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at",
	"sandbox_state", "snapshot_key", "pending_snapshot_key", "pending_snapshot_set_at",
	"runtime_soft_deadline_at", "runtime_hard_deadline_at", "runtime_last_progress_at", "runtime_last_progress_type", "runtime_last_progress_strength",
	"runtime_extension_count", "runtime_extension_seconds", "runtime_stop_reason", "runtime_graceful_stop_at",
	"checkpointed_at", "checkpoint_kind", "checkpoint_capability", "checkpoint_size_bytes", "checkpoint_error",
	"recovery_state", "recovery_queued_at", "recovery_started_at", "recovery_attempt_count",
	"target_branch", "working_branch",
	"base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest",
	"archived_at", "archived_by_user_id", "automation_run_id",
	"pr_creation_state", "pr_creation_error", "pr_push_state", "pr_push_error", "branch_creation_state", "branch_creation_error", "branch_url", "diff_collected_at", "latest_diff_snapshot_id", "has_unpushed_changes",
	"linear_private", "linear_state_sync_disabled", "linear_identifier_hint", "linear_prepare_state",
	"deleted_at", "git_identity_source", "git_identity_user_id", "created_at",
}

func previewSessionRow(id, orgID uuid.UUID, containerID *string, snapshotKey *string, sandboxState string) []interface{} {
	now := time.Now()
	byColumn := map[string]interface{}{
		"id":                             id,
		"primary_issue_id":               nil,
		"org_id":                         orgID,
		"origin":                         string(models.SessionOriginManual),
		"interaction_mode":               string(models.SessionInteractionModeInteractive),
		"validation_policy":              string(models.SessionValidationPolicyOnTurnComplete),
		"agent_type":                     "claude_code",
		"status":                         "running",
		"autonomy_level":                 "supervised",
		"token_mode":                     "low",
		"container_id":                   containerID,
		"turn_holding_container":         false,
		"token_usage":                    json.RawMessage(`{}`),
		"failure_next_steps":             []string{},
		"failure_retry_advised":          false,
		"revision_context":               json.RawMessage(`{}`),
		"current_turn":                   0,
		"last_activity_at":               now,
		"sandbox_state":                  sandboxState,
		"snapshot_key":                   snapshotKey,
		"runtime_last_progress_type":     "",
		"runtime_last_progress_strength": "",
		"runtime_extension_count":        0,
		"runtime_extension_seconds":      0,
		"runtime_stop_reason":            "",
		"checkpoint_kind":                "",
		"checkpoint_capability":          "",
		"checkpoint_size_bytes":          int64(0),
		"recovery_state":                 "",
		"recovery_attempt_count":         0,
		"pr_creation_state":              "idle",
		"pr_creation_error":              (*string)(nil),
		"pr_push_state":                  "idle",
		"pr_push_error":                  (*string)(nil),
		"branch_creation_state":          "idle",
		"branch_creation_error":          (*string)(nil),
		"branch_url":                     (*string)(nil),
		"has_unpushed_changes":           false,
		"linear_private":                 false,
		"linear_state_sync_disabled":     false,
		"linear_identifier_hint":         (*string)(nil),
		"linear_prepare_state":           string(models.LinearPrepareStateNone),
		"deleted_at":                     nil,
		"git_identity_source":            nil,
		"git_identity_user_id":           nil,
		"created_at":                     now,
	}
	row := make([]interface{}, len(sessionRowColumns))
	for i, col := range sessionRowColumns {
		row[i] = byColumn[col]
	}
	return row
}

func sessionRowWithContainer(id, orgID uuid.UUID, containerID string) []interface{} {
	// sandbox_state must be "running" for the reuse branch of acquireSandbox
	// to attach to the lingering container_id; otherwise the stale-ID guard
	// falls through to hydrate/expired.
	return previewSessionRow(id, orgID, &containerID, nil, "running")
}

// sessionRowWithContainerAndRepo is sessionRowWithContainer plus a non-nil
// repository_id, used by tests that need acquireSandbox to invoke the repo
// lookup in resolveSandboxWorkDir.
func sessionRowWithContainerAndRepo(id, orgID, repoID uuid.UUID, containerID string) []interface{} {
	row := sessionRowWithContainer(id, orgID, containerID)
	for i, name := range sessionRowColumns {
		if name == "repository_id" {
			row[i] = &repoID
			return row
		}
	}
	panic("repository_id column missing from sessionRowColumns")
}

// sessionRowReuseWithSnapshot builds a row that satisfies both the reuse
// precondition (container_id set, sandbox_state='running') AND the hydrate
// fallback precondition (snapshot_key set). Used by the zombie-reuse tests
// where IsAlive decides which branch the handler takes.
func sessionRowReuseWithSnapshot(id, orgID uuid.UUID, containerID string, snapshotKey *string) []interface{} {
	return previewSessionRow(id, orgID, &containerID, snapshotKey, "running")
}

// sessionRowForHydrate builds a session row with no live container but a
// configurable snapshot key and sandbox state — used to exercise the three
// acquireSandbox branches (SNAPSHOT_EXPIRED, hydrate, NO_SANDBOX).
func sessionRowForHydrate(id, orgID uuid.UUID, snapshotKey *string, sandboxState string) []interface{} {
	return previewSessionRow(id, orgID, nil, snapshotKey, sandboxState)
}

// fakeHydrateSnapshotStore is a minimal SnapshotStore that writes a canned
// payload on Load.
type fakeHydrateSnapshotStore struct {
	payload   []byte
	loadErr   error
	loadCalls int
}

func (f *fakeHydrateSnapshotStore) Save(context.Context, string, io.Reader) error {
	return nil
}

func (f *fakeHydrateSnapshotStore) Load(_ context.Context, _ string, w io.Writer) error {
	f.loadCalls++
	if f.loadErr != nil {
		return f.loadErr
	}
	_, _ = w.Write(f.payload)
	return nil
}

func (f *fakeHydrateSnapshotStore) Delete(context.Context, string) error { return nil }

// TestPreviewHandler_StartPreview_NoWorkspaceConfig covers the common path where
// the user clicks Start Preview on a repo that has no .143/config.json
// committed and supplies no explicit config. The handler must abort the
// reservation and surface PREVIEW_NO_CONFIG (422) — there is no longer a silent
// "npm start on :3000" fallback that wastes ~90s on a doomed readiness probe.
func TestPreviewHandler_StartPreview_NoWorkspaceConfig(t *testing.T) {
	t.Parallel()

	expectedMessage := "This repo has no .143/config.json committed with a preview section. Add one (see docs/guides/previews.md) so the preview knows what command to run."

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowWithContainer(sessionID, orgID, "container-1")...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	// fileReader returns ENOENT → readWorkspacePreviewConfig yields (nil, nil)
	// → handler aborts the reservation with PREVIEW_NO_CONFIG.
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	h.fileReader = &fakeFileReader{errorsByPath: map[string]error{
		repoconfig.ConfigPath: fmt.Errorf("read %s: %w", repoconfig.ConfigPath, sandbox.ErrFileNotFound),
	}}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "missing workspace config must surface as 422")

	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "PREVIEW_NO_CONFIG", resp.Error.Code, "missing workspace config should use the stable no-config error code")
	require.Equal(t, expectedMessage, resp.Error.Message, "missing workspace config should return the capitalized user-facing guidance")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPreviewHandler_StartPreview_AutoDetectUsesRepoSlugWorkDir is the
// regression test for the production bug where preview start always failed
// with PREVIEW_NO_CONFIG (or, before that, fell into the npm-start fallback)
// because the handler asked the file reader to look in /workspace, but the
// orchestrator actually checks the repo out at /home/sandbox/<slug>. This
// test wires a sessionRow with a repository_id, mocks the repo lookup, and
// asserts that the workDir argument fileReader.ReadFile receives matches
// the orchestrator's per-session WorkDir derivation — not the legacy default.
func TestPreviewHandler_StartPreview_AutoDetectUsesRepoSlugWorkDir(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowWithContainerAndRepo(sessionID, orgID, repoID, "container-1")...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	// resolveSandboxWorkDir → repoStore.GetByID lookup.
	mock.ExpectQuery("SELECT .+ FROM repositories\\s+WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(repositoryRowColumns).
				AddRow(repoRow(repoID, orgID, "assembledhq/143")...),
		)
	// fileReader returns ENOENT → handler aborts with PREVIEW_NO_CONFIG.
	// We don't care about the response here; we care about the workDir the
	// reader was called with, captured below via lastWorkDir.
	expectAbortReservationNoDestroy(mock)

	var capturedWorkDir string
	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	h.repoStore = db.NewRepositoryStore(mock)
	h.fileReader = &fakeFileReader{
		errorsByPath: map[string]error{
			repoconfig.ConfigPath: fmt.Errorf("read %s: %w", repoconfig.ConfigPath, sandbox.ErrFileNotFound),
		},
		lastWorkDir: &capturedWorkDir,
	}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()
	h.StartPreview(w, req)

	require.Equal(t, "/home/sandbox/143", capturedWorkDir,
		"file reader must be invoked with the repo-rooted WorkDir, not the legacy /workspace")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPreviewHandler_StartPreview_AutoDetectInfraError exercises the auto-detect
// branch when the workspace file reader returns a non-ENOENT error. The handler
// must surface a 500 instead of guessing — distinct from PREVIEW_NO_CONFIG,
// which means the file is definitively absent.
func TestPreviewHandler_StartPreview_AutoDetectInfraError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()

	// GetByID returns a session with a live container so acquireSandbox takes
	// the reuse branch.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowWithContainer(sessionID, orgID, "container-1")...),
		)
	// ReservePreview — the fix for bug #2: capacity / existing-preview checks
	// now run BEFORE any sandbox interaction, so a pre-hydrate autodetect
	// failure cannot leak a container.
	expectReserveSuccess(mock, sessionID, orgID, userID)
	// readWorkspacePreviewConfig fails → AbortReservation releases the hold.
	// Reused container (hydratedContainerID == ""), so no FinalizeContainerDestroy.
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	h.fileReader = &fakeFileReader{errorsByPath: map[string]error{
		repoconfig.ConfigPath: errors.New("docker exec failed: container not running"),
	}}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "non-ENOENT read failure must surface as 500")

	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "PREVIEW_CONFIG_READ_FAILED", resp.Error.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_StartPreview_AutoDetectInvalidConfig(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowWithContainer(sessionID, orgID, "container-1")...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	h.fileReader = &fakeFileReader{content: "{not valid json"}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "invalid committed config should be a user-actionable 422")

	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp), "response body should decode as an error response")
	require.Equal(t, "PREVIEW_CONFIG_INVALID", resp.Error.Code, "invalid committed config should use the stable invalid-config code")
	require.Contains(t, resp.Error.Message, "Invalid .143/config.json preview config", "message should name the committed config file")
	require.Contains(t, resp.Error.Message, "invalid character", "message should include the parser's specific failure")
	require.Contains(t, resp.Error.Message, "Fix the committed config", "message should include a recovery action")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewHandler_StartPreview_SnapshotUnavailable_NoSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()

	// Session row with no container + no snapshot, but not destroyed by the
	// reaper, means the sandbox was never saved successfully. Surface that as a
	// distinct unavailable state instead of "expired".
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, nil, "snapshotted")...),
		)
	// ReservePreview succeeds before we realize the session has no usable
	// sandbox, so the preview row exists and must be cleaned up via Abort.
	expectReserveSuccess(mock, sessionID, orgID, userID)
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "no container + no snapshot must return 409")
	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "SNAPSHOT_UNAVAILABLE", resp.Error.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_StartPreview_SnapshotExpired_SandboxDestroyed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	key := "some-key"

	// snapshot_key present but sandbox_state='destroyed' means we still treat
	// the snapshot as gone — same SNAPSHOT_EXPIRED guard rails out the request.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, &key, "destroyed")...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusGone, w.Code)
	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "SNAPSHOT_EXPIRED", resp.Error.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_StartPreview_NoSandbox_WhenHydrateNotConfigured(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	key := "snap-key"

	// Hydrate is viable (snapshot present, state != destroyed) but the handler
	// was constructed without a sandbox provider / snapshot store — surface
	// NO_SANDBOX (409) rather than a misleading 500 or SNAPSHOT_EXPIRED.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, &key, "snapshotted")...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	// leave sandboxProvider / snapshots nil

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "hydrate not configured must return 409 NO_SANDBOX")
	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "NO_SANDBOX", resp.Error.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_StartPreview_CapacityReached(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()

	// Session with a live container so acquireSandbox short-circuits to reuse,
	// and readWorkspacePreviewConfig is a no-op (nil fileReader) → defaults.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowWithContainer(sessionID, orgID, "container-1")...),
		)
	// StartPreview: check existing active preview — none.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	// checkConcurrencyCaps: per-user COUNT at the limit → ErrPreviewCapacity.
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(999))

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "capacity errors must map to 503")
	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp), "response body should decode as an error response")
	require.Equal(t, preview.PreviewCapacityCode, resp.Error.Code, "capacity errors should keep their stable API code")
	require.Equal(t, "You have reached your per-user preview limit: 999 active previews out of 4 allowed. Stop one of your previews or ask an admin to raise the per-user preview limit in General settings.", resp.Error.Message, "per-user capacity errors should explain the configured user limit")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewHandler_StartPreview_WorkerRoutedEnqueuesStartPreviewJob(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	now := time.Now().UTC()

	sessionStore := db.NewSessionStore(mock)
	previewStore := db.NewPreviewStore(mock)
	nodeStore := db.NewNodeStore(mock)
	jobStore := db.NewJobStore(mock)

	mgr := preview.NewManager(preview.ManagerConfig{
		Store:        previewStore,
		Logger:       zerolog.Nop(),
		WorkerNodeID: "api-node",
	})
	h := NewPreviewHandler(mgr, previewStore, sessionStore, nil, sandbox.NoOpFileReader{}, nil, nil, zerolog.Nop())
	h.jobStore = jobStore
	h.SetWorkerRuntime(preview.NewWorkerSelector(nodeStore, previewStore), preview.NewWorkerPreviewClient("test-secret"), "api-node")

	workerMeta, err := json.Marshal(preview.WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://worker-a.internal",
	})
	require.NoError(t, err, "should marshal worker metadata")

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, nil, "none")...),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(
			pgxmock.NewRows(handlerNodeTestCols).
				AddRow("worker-a", "worker", "worker-a", "active", workerMeta, now, now),
		)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM preview_instances WHERE worker_node_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM preview_instances WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM preview_instances WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM preview_instances WHERE worker_node_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("INSERT INTO preview_instances").
		WithArgs(previewAnyArgs(20)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newReservedPreviewRow(previewID, sessionID, orgID, userID, now)...),
		)
	mock.ExpectQuery("UPDATE preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(sessionID))
	mock.ExpectQuery("INSERT INTO jobs \\(org_id, queue, job_type, payload, priority, dedupe_key, target_node_id\\)").
		WithArgs(previewAnyArgs(7)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "worker-routed start should return 202 after enqueueing startup: %s", w.Body.String())
	var resp models.SingleResponse[*models.PreviewInstance]
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp), "response should decode as a preview instance")
	require.Equal(t, previewID, resp.Data.ID, "response should return the reserved preview")
	require.Equal(t, models.PreviewStatusStarting, resp.Data.Status, "response should show startup in progress")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestPreviewHandler_StartPreview_HydrateReachesLaunch verifies that when
// hydrate is configured and the session has a usable snapshot, the handler
// reserves the preview, hydrates a fresh sandbox, and proceeds into
// manager.LaunchPreview. We stop short of simulating the full launch DB dance
// by failing UpdatePreviewReservationConfig — enough to prove the handler
// crossed into Launch after acquireSandbox, and that Abort cleans up.
func TestPreviewHandler_StartPreview_HydrateReachesLaunch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	key := "snap-key"

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, &key, "snapshotted")...),
		)
	// Reserve runs BEFORE hydrate under the new flow — that ordering is the
	// capacity-leak fix (bug #2) and the hold-race fix (bug #3).
	expectReserveSuccess(mock, sessionID, orgID, userID)
	// Hydrate publishes container_id (CAS) + transitions sandbox_state=running
	// inside the same UPDATE. Echo the proposed id back (no lost race).
	mock.ExpectQuery("UPDATE sessions\\s+SET container_id = COALESCE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"coalesce"}).AddRow("test-sandbox"))
	// Launch's first DB call is UpdatePreviewReservationConfig (needsUpdate is
	// always true here because the reservation persisted an empty recycle
	// sandbox). Failing it proves the handler reached Launch; Abort then
	// releases the hold.
	updateCfgArgs := make([]any, 10)
	for i := range updateCfgArgs {
		updateCfgArgs[i] = pgxmock.AnyArg()
	}
	mock.ExpectExec("UPDATE preview_instances\\s+SET name").
		WithArgs(updateCfgArgs...).
		WillReturnError(errors.New("db down"))
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	// fileReader returns a valid workspace config so the post-hydrate
	// auto-detect doesn't short-circuit with PREVIEW_NO_CONFIG before Launch.
	h.fileReader = &fakeFileReader{content: validWorkspaceConfigJSON}
	sp := testutil.NewMockSandboxProvider()
	// RestoreFn drains the io.Pipe reader so the Load goroutine's Write
	// completes and HydrateSandboxFromSnapshot returns instead of deadlocking.
	sp.RestoreFn = func(_ context.Context, _ *agent.Sandbox, r io.Reader) error {
		_, _ = io.Copy(io.Discard, r)
		return nil
	}
	h.sandboxProvider = sp
	h.snapshots = &fakeHydrateSnapshotStore{payload: []byte("snap")}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "launch failure must surface as 422 PREVIEW_START_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPreviewHandler_StartPreview_HydrateFails covers the error return from
// HydrateSandboxFromSnapshot (preview.go: provider.Create → acquireSandbox
// returns err, errCode="").
func TestPreviewHandler_StartPreview_HydrateFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	key := "snap-key"

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, &key, "snapshotted")...),
		)
	// Reserve succeeds; acquireSandbox fails at provider.Create (no UPDATE
	// sessions fires because PublishHydratedContainerID is never reached).
	// The reservation must be aborted — hydratedID="" so no destroy.
	expectReserveSuccess(mock, sessionID, orgID, userID)
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	sp := testutil.NewMockSandboxProvider()
	sp.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		return nil, errors.New("create boom")
	}
	h.sandboxProvider = sp
	h.snapshots = &fakeHydrateSnapshotStore{payload: []byte("snap")}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "PREVIEW_HYDRATE_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_StartPreview_HydrateCapacityReached(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	key := "snap-key"

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, &key, "snapshotted")...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	mock.ExpectQuery("SELECT COALESCE\\(container_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"container_id"}).AddRow(""))
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	h.sandboxCapacity = agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   &previewFakeLiveSandboxCounter{count: 1},
		MaxActive: 1,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})
	sp := testutil.NewMockSandboxProvider()
	sp.CreateFn = func(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
		t.Fatalf("provider.Create must not be called when live sandbox capacity is full")
		return nil, nil
	}
	h.sandboxProvider = sp
	h.snapshots = &fakeHydrateSnapshotStore{payload: []byte("snap")}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "preview hydrate capacity should surface as 503")
	require.Contains(t, w.Body.String(), preview.PreviewCapacityCode, "preview hydrate capacity should use the capacity error code")
	require.Contains(t, w.Body.String(), preview.PreviewCapacityMessage, "preview hydrate capacity should use user-facing copy")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestPreviewHandler_StartPreview_PublishContainerIDFails covers the
// PublishHydratedContainerID error branch that destroys the just-created
// sandbox (preview.go: CAS failure in hydrate path).
func TestPreviewHandler_StartPreview_PublishContainerIDFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	key := "snap-key"

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, &key, "snapshotted")...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	mock.ExpectQuery("UPDATE sessions\\s+SET container_id = COALESCE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db down"))
	// acquireSandbox destroyed the local container before returning, so the
	// handler passes hydratedID="" to AbortReservation — no finalize/destroy
	// on the manager side.
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	sp := testutil.NewMockSandboxProvider()
	sp.RestoreFn = func(_ context.Context, _ *agent.Sandbox, r io.Reader) error {
		_, _ = io.Copy(io.Discard, r)
		return nil
	}
	h.sandboxProvider = sp
	h.snapshots = &fakeHydrateSnapshotStore{payload: []byte("snap")}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "PREVIEW_HYDRATE_FAILED")
	require.Equal(t, 1, sp.GetDestroyCalls(), "acquireSandbox must destroy the local container when publish fails")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPreviewHandler_StartPreview_PublishLosesRace covers the CAS-loss branch
// of PublishHydratedContainerID: a concurrent orchestrator publishes a
// different container_id while we're mid-restore (after our pre-hydrate peek
// found the row clear), so the CAS detects the loss and the preview must
// destroy its local sandbox and surface a SANDBOX_BUSY error instructing the
// caller to retry.
func TestPreviewHandler_StartPreview_PublishLosesRace(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	key := "snap-key"

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, &key, "snapshotted")...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	// Pre-hydrate peek (single-column COALESCE) finds container_id still NULL
	// (the orchestrator hasn't published yet) — hydrate proceeds.
	mock.ExpectQuery("SELECT COALESCE\\(container_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"container_id"}).AddRow(""))
	// COALESCE returns the orchestrator's pre-existing ID, proving our preview
	// lost the race during the snapshot restore window.
	mock.ExpectQuery("UPDATE sessions\\s+SET container_id = COALESCE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"coalesce"}).AddRow("orch-winner"))
	// Handler passes hydratedID="" to Abort because acquireSandbox already
	// destroyed the local container on the race-loss branch.
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	sp := testutil.NewMockSandboxProvider()
	sp.RestoreFn = func(_ context.Context, _ *agent.Sandbox, r io.Reader) error {
		_, _ = io.Copy(io.Discard, r)
		return nil
	}
	h.sandboxProvider = sp
	h.snapshots = &fakeHydrateSnapshotStore{payload: []byte("snap")}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "SANDBOX_BUSY")
	require.Equal(t, 1, sp.GetDestroyCalls(), "local container must be destroyed on race loss")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPreviewHandler_StartPreview_PrehydratePeekShortCircuit covers the fast
// path of race detection: the peer (typically a continue_session turn)
// publishes container_id between our initial session read and the start of
// hydrate. The pre-hydrate peek finds the row populated and returns
// SANDBOX_BUSY without ever touching the snapshot store or sandbox provider.
//
// This avoids the historical failure where full restore + container create
// (~20s) blew past the HTTP server's 15s WriteTimeout, surfacing as a 502
// EOF on the API instead of a clean 409 SANDBOX_BUSY.
func TestPreviewHandler_StartPreview_PrehydratePeekShortCircuit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	key := "snap-key"

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, &key, "snapshotted")...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	// Pre-hydrate peek (single-column COALESCE) finds container_id has been
	// published since our first read — short-circuit out without restoring
	// or creating a container.
	mock.ExpectQuery("SELECT COALESCE\\(container_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"container_id"}).AddRow("orch-winner"))
	// hydratedID="" because we never created a container.
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	counter := &previewFakeLiveSandboxCounter{count: 99}
	h.sandboxCapacity = agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{
		Counter:   counter,
		MaxActive: 1,
		NodeID:    "worker-1",
		Logger:    zerolog.Nop(),
	})
	sp := testutil.NewMockSandboxProvider()
	h.sandboxProvider = sp
	// Snapshot store presence required so we don't trip the NO_SANDBOX guard.
	snaps := &fakeHydrateSnapshotStore{payload: []byte("snap")}
	h.snapshots = snaps

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "SANDBOX_BUSY")
	require.Equal(t, int64(0), counter.calls.Load(), "pre-hydrate peek should short-circuit before consuming live sandbox capacity")
	require.Equal(t, 0, snaps.loadCalls, "must not load the snapshot when peek detects the race")
	require.Equal(t, 0, sp.GetDestroyCalls(), "no container to destroy when peek short-circuits")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPreviewHandler_StartPreview_ReuseZombieFallsThroughToHydrate covers the
// case where the session row says "running with container_id=X" but the
// container has been removed out-of-band (e.g. docker prune, host reboot).
// IsAlive returns (false, nil); acquireSandbox must skip the reuse branch and
// hydrate from snapshot instead of attaching to a dead ID.
func TestPreviewHandler_StartPreview_ReuseZombieFallsThroughToHydrate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	key := "snap-key"

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowReuseWithSnapshot(sessionID, orgID, "zombie-id", &key)...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	// Hydrate is reached → publishes the provider's new ID via COALESCE CAS.
	mock.ExpectQuery("UPDATE sessions\\s+SET container_id = COALESCE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"coalesce"}).AddRow("test-sandbox"))
	// Fail Launch at UpdatePreviewReservationConfig to prove the handler
	// advanced past acquireSandbox into the manager's launch phase.
	updateCfgArgs := make([]any, 10)
	for i := range updateCfgArgs {
		updateCfgArgs[i] = pgxmock.AnyArg()
	}
	mock.ExpectExec("UPDATE preview_instances\\s+SET name").
		WithArgs(updateCfgArgs...).
		WillReturnError(errors.New("db down"))
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	h.fileReader = &fakeFileReader{content: validWorkspaceConfigJSON}
	sp := testutil.NewMockSandboxProvider()
	sp.IsAliveFn = func(_ context.Context, sb *agent.Sandbox) (bool, error) {
		require.Equal(t, "zombie-id", sb.ID)
		return false, nil
	}
	sp.RestoreFn = func(_ context.Context, _ *agent.Sandbox, r io.Reader) error {
		_, _ = io.Copy(io.Discard, r)
		return nil
	}
	h.sandboxProvider = sp
	h.snapshots = &fakeHydrateSnapshotStore{payload: []byte("snap")}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	// Reaching the manager's Launch-phase UpdateReservationConfig proves the
	// zombie fallthrough landed in hydrate rather than attaching to "zombie-id".
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPreviewHandler_StartPreview_ReuseInspectErrorFallsThroughToHydrate covers
// the transient-liveness-error branch: rather than attaching blindly to an ID
// we can't verify, acquireSandbox logs and falls through to hydrate, which
// will either succeed (bringing up a fresh, known-good container) or return
// an actionable error to the caller.
func TestPreviewHandler_StartPreview_ReuseInspectErrorFallsThroughToHydrate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	key := "snap-key"

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowReuseWithSnapshot(sessionID, orgID, "maybe-dead", &key)...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	mock.ExpectQuery("UPDATE sessions\\s+SET container_id = COALESCE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"coalesce"}).AddRow("test-sandbox"))
	updateCfgArgs := make([]any, 10)
	for i := range updateCfgArgs {
		updateCfgArgs[i] = pgxmock.AnyArg()
	}
	mock.ExpectExec("UPDATE preview_instances\\s+SET name").
		WithArgs(updateCfgArgs...).
		WillReturnError(errors.New("db down"))
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	h.fileReader = &fakeFileReader{content: validWorkspaceConfigJSON}
	sp := testutil.NewMockSandboxProvider()
	sp.IsAliveFn = func(_ context.Context, _ *agent.Sandbox) (bool, error) {
		return false, errors.New("docker daemon flaked")
	}
	sp.RestoreFn = func(_ context.Context, _ *agent.Sandbox, r io.Reader) error {
		_, _ = io.Copy(io.Discard, r)
		return nil
	}
	h.sandboxProvider = sp
	h.snapshots = &fakeHydrateSnapshotStore{payload: []byte("snap")}

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestPreviewHandler_StartPreview_ReuseAttachesWhenContainerAlive covers the
// happy reuse branch of acquireSandbox: with sandbox_state='running' and a
// live container_id, and the sandbox provider confirming liveness, we attach
// to the existing container rather than hydrating a duplicate.
func TestPreviewHandler_StartPreview_ReuseAttachesWhenContainerAlive(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowWithContainer(sessionID, orgID, "live-container")...),
		)
	expectReserveSuccess(mock, sessionID, orgID, userID)
	// Fail Launch at UpdatePreviewReservationConfig. Absence of a COALESCE CAS
	// in the expectation list is what proves the reuse branch fired — the
	// ExpectationsWereMet check at the bottom will reject an unexpected hydrate.
	updateCfgArgs := make([]any, 10)
	for i := range updateCfgArgs {
		updateCfgArgs[i] = pgxmock.AnyArg()
	}
	mock.ExpectExec("UPDATE preview_instances\\s+SET name").
		WithArgs(updateCfgArgs...).
		WillReturnError(errors.New("db down"))
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
	h.fileReader = &fakeFileReader{content: validWorkspaceConfigJSON}
	sp := testutil.NewMockSandboxProvider()
	sp.IsAliveFn = func(_ context.Context, sb *agent.Sandbox) (bool, error) {
		require.Equal(t, "live-container", sb.ID)
		return true, nil
	}
	h.sandboxProvider = sp

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "launch failure surfaces as 422 PREVIEW_START_FAILED")
	require.Equal(t, 0, sp.GetDestroyCalls(), "live-reuse must never destroy the attached container")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestClassifyLaunchError verifies the error-code mapping that turns
// preview-launch failures into actionable HTTP responses. Before this
// existed, every launch failure became a generic 422 PREVIEW_START_FAILED
// with the message "failed to start preview" — the actual cause never
// reached the user, so the frontend rendered the unhelpful
// "Failed to start preview: failed to start preview".
func TestClassifyLaunchError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		err          error
		wantCode     string
		wantStatus   int
		mustContain  string
		mustContain2 string
	}{
		{
			name:        "image unavailable",
			err:         fmt.Errorf("provider start preview: provision infrastructure %q: %w: pull %q: registry unreachable", "db", preview.ErrInfraImageUnavailable, "postgres:17-alpine"),
			wantCode:    "PREVIEW_INFRA_IMAGE_UNAVAILABLE",
			wantStatus:  http.StatusUnprocessableEntity,
			mustContain: "postgres:17-alpine",
		},
		{
			name:        "infra start failed",
			err:         fmt.Errorf("%w: create container: out of memory", preview.ErrInfraStartFailed),
			wantCode:    "PREVIEW_INFRA_START_FAILED",
			wantStatus:  http.StatusUnprocessableEntity,
			mustContain: "out of memory",
		},
		{
			name:        "infra unhealthy",
			err:         fmt.Errorf("%w: infrastructure %q (postgres-17): health check timed out after 60 seconds", preview.ErrInfraUnhealthy, "db"),
			wantCode:    "PREVIEW_INFRA_UNHEALTHY",
			wantStatus:  http.StatusUnprocessableEntity,
			mustContain: "health check timed out",
		},
		{
			name:        "init script failed",
			err:         fmt.Errorf("%w: infrastructure %q script %q: exit 1", preview.ErrInitScriptFailed, "db", "seed.sql"),
			wantCode:    "PREVIEW_INIT_SCRIPT_FAILED",
			wantStatus:  http.StatusUnprocessableEntity,
			mustContain: "seed.sql",
		},
		{
			name:        "service not ready",
			err:         fmt.Errorf("%w: primary service %q (port 3000): timeout", preview.ErrServiceNotReady, "app"),
			wantCode:    "PREVIEW_SERVICE_NOT_READY",
			wantStatus:  http.StatusUnprocessableEntity,
			mustContain: "port 3000",
		},
		{
			name:        "unclassified surfaces underlying cause",
			err:         errors.New("provider start preview: something weird happened"),
			wantCode:    "PREVIEW_START_FAILED",
			wantStatus:  http.StatusUnprocessableEntity,
			mustContain: "something weird happened",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyLaunchError(tc.err)
			require.NotNil(t, got)
			require.Equal(t, tc.wantStatus, got.status)
			require.Equal(t, tc.wantCode, got.code)
			require.Contains(t, got.message, tc.mustContain, "user-visible message must surface the underlying cause")
			require.NotEqual(t, "failed to start preview", got.message, "must not regress to the opaque generic message")
		})
	}

	require.Nil(t, classifyLaunchError(nil), "nil error must not produce a response")
}

func TestPreviewHandler_SetAuditEmitter(t *testing.T) {
	t.Parallel()

	h := newPreviewTestHandler()
	require.Nil(t, h.audit)

	// SetAuditEmitter should set the audit field (even nil is valid).
	h.SetAuditEmitter(nil)
	require.Nil(t, h.audit)
}

func TestPreviewHandler_GetActivePreview_InvalidSessionID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)

	req := httptest.NewRequest(http.MethodGet, "/preview", nil)
	req = previewTestContextWithIDs(req, uuid.New(), uuid.New(), "not-a-uuid")
	w := httptest.NewRecorder()

	h.GetPreview(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	var resp models.ErrorResponse
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, "INVALID_SESSION_ID", resp.Error.Code)
}

func TestPreviewHandler_GetActivePreview_NoActivePreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()

	// Return no rows for GetActivePreviewForSession.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT [\\s\\S]+ FROM preview_instances[\\s\\S]+status IN \\('stopped', 'expired', 'failed'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	req := httptest.NewRequest(http.MethodGet, "/preview", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.GetPreview(w, req)
	require.Equal(t, http.StatusNotFound, w.Code, "no active or failed preview should return 404: %s", w.Body.String())

	var resp models.ErrorResponse
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, "NO_ACTIVE_PREVIEW", resp.Error.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_GetPreview_ReturnsLatestStoppedPreviewWhenNoActivePreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()
	stoppedAt := now.Add(10 * time.Minute)
	stoppedRow := newActivePreviewRow(previewID, sessionID, orgID, userID, now)
	stoppedRow[6] = "stopped"
	stoppedRow[16] = &stoppedAt

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT .+ FROM preview_instances[\\s\\S]+status IN \\('stopped', 'expired', 'failed'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(stoppedRow...),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(stoppedRow...),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_services").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewServiceTestCols))
	mock.ExpectQuery("SELECT .+ FROM preview_infrastructure").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewInfraTestCols))

	req := httptest.NewRequest(http.MethodGet, "/preview", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.GetPreview(w, req)
	require.Equal(t, http.StatusOK, w.Code, "stopped preview history should return successfully: %s", w.Body.String())

	var resp models.SingleResponse[*models.PreviewStatusResponse]
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, models.PreviewStatusStopped, resp.Data.Instance.Status, "handler should return the latest stopped preview status")
	require.NotNil(t, resp.Data.Instance.StoppedAt, "handler should include the preview stop timestamp")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewHandler_GetPreview_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// getActivePreview calls GetActivePreviewForSession.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// GetStatus calls GetPreviewInstance.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// GetStatus calls ListServicesByPreview.
	mock.ExpectQuery("SELECT .+ FROM preview_services").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewServiceTestCols))

	// GetStatus calls ListInfraByPreview.
	mock.ExpectQuery("SELECT .+ FROM preview_infrastructure").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewInfraTestCols))

	req := httptest.NewRequest(http.MethodGet, "/preview", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.GetPreview(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_StopPreview_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// getActivePreview
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// StopPreview calls GetPreviewInstance.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// StopPreview calls StopPreviewWithRevocation which does
	// Begin + StopPreview (instance UPDATE + child cascades) + RevokeAll + Commit.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodDelete, "/preview", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StopPreview(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_SetLifetime_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	mock.ExpectExec("UPDATE preview_instances SET expires_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPatch, "/preview/lifetime", strings.NewReader(`{"duration_seconds":300}`))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.SetLifetime(w, req)
	require.Equal(t, http.StatusOK, w.Code, "SetLifetime should return OK")
	require.Contains(t, w.Body.String(), `"status":"updated"`, "SetLifetime response should confirm the update")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewHandler_SetLifetime_RejectsLongDuration(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()

	req := httptest.NewRequest(http.MethodPatch, "/preview/lifetime", strings.NewReader(`{"duration_seconds":3600}`))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.SetLifetime(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "SetLifetime should reject durations above the per-adjustment cap")
	require.Contains(t, w.Body.String(), "duration_seconds must be between", "SetLifetime should explain the accepted bounds")
	require.NoError(t, mock.ExpectationsWereMet(), "invalid durations should not query the database")
}

func TestPreviewHandler_SetLifetime_RejectsShortDuration(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()

	req := httptest.NewRequest(http.MethodPatch, "/preview/lifetime", strings.NewReader(`{"duration_seconds":30}`))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.SetLifetime(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "SetLifetime should reject durations below the minimum")
	require.Contains(t, w.Body.String(), "duration_seconds must be between", "SetLifetime should explain the accepted bounds")
	require.NoError(t, mock.ExpectationsWereMet(), "invalid durations should not query the database")
}

func TestPreviewHandler_GetLogs_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// getActivePreview
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// ListLogsByPreview returns empty.
	mock.ExpectQuery("SELECT .+ FROM preview_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewLogTestCols))

	req := httptest.NewRequest(http.MethodGet, "/preview/logs", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.GetLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_GetLogs_UsesLatestFailedPreviewWhenNoActivePreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()
	failedRow := newActivePreviewRow(previewID, sessionID, orgID, userID, now)
	failedRow[6] = "failed"
	failedRow[22] = "preview service failed"

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT [\\s\\S]+ FROM preview_instances[\\s\\S]+status = 'failed'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(failedRow...),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPreviewLogTestCols).
				AddRow(uuid.New(), previewID, orgID, "error", "start", "full preview startup log", json.RawMessage("{}"), now),
		)

	req := httptest.NewRequest(http.MethodGet, "/preview/logs", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.GetLogs(w, req)

	require.Equal(t, http.StatusOK, w.Code, "failed previews should expose persisted startup logs")
	require.Contains(t, w.Body.String(), "full preview startup log", "response should include the persisted preview log")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewHandler_GetServices_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// getActivePreview
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// ListServicesByPreview returns empty.
	mock.ExpectQuery("SELECT .+ FROM preview_services").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewServiceTestCols))

	req := httptest.NewRequest(http.MethodGet, "/preview/services", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.GetServices(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_GetSnapshots_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// getActivePreview
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// ListSnapshotsByPreview returns empty.
	mock.ExpectQuery("SELECT .+ FROM preview_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(handlerPreviewSnapshotTestCols))

	req := httptest.NewRequest(http.MethodGet, "/preview/snapshots", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.GetSnapshots(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_MintBootstrapToken_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// getActivePreview
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// MintBootstrapToken calls GetPreviewInstance.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// MintBootstrapToken calls CreateAccessSession.
	accessSessionCols := []string{
		"id", "org_id", "user_id", "preview_instance_id",
		"session_token_hash", "issued_at", "expires_at", "revoked_at", "last_accessed_at", "created_at",
	}
	sessID := uuid.New()
	mock.ExpectQuery("INSERT INTO preview_access_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(accessSessionCols).
				AddRow(sessID, orgID, userID, previewID, "hash", now, now.Add(5*time.Minute), nil, now, now),
		)

	req := httptest.NewRequest(http.MethodPost, "/preview/bootstrap", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.MintBootstrapToken(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[map[string]string]
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Data["token"])
	require.Equal(t, previewID.String(), resp.Data["preview_id"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_RestartPreview_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// getActivePreview
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// RestartPreview should recycle the active preview in place.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	mock.ExpectExec("UPDATE preview_instances SET status = @status.+NOT IN").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET status = @status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET expires_at = @expires_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/preview/restart", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.RestartPreview(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"data":{"status":"restarting"}}`, w.Body.String(), "restart endpoint should acknowledge the recycle")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_SubmitDesignFeedback_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// getActivePreview
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// CreatePreviewLog
	logID := uuid.New()
	mock.ExpectQuery("INSERT INTO preview_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(handlerPreviewLogTestCols).
				AddRow(logID, previewID, orgID, "info", "design_feedback", "design feedback submitted", json.RawMessage(`{}`), now),
		)

	body := `{"type":"color_change","description":"make it red"}`
	req := httptest.NewRequest(http.MethodPost, "/preview/design-feedback", strings.NewReader(body))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.SubmitDesignFeedback(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_SubmitDesignFeedback_MissingType(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	h := newPreviewHandlerWithMock(mock)
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// getActivePreview
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	body := `{"description":"no type field"}`
	req := httptest.NewRequest(http.MethodPost, "/preview/design-feedback", strings.NewReader(body))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.SubmitDesignFeedback(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	var resp models.ErrorResponse
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, "MISSING_TYPE", resp.Error.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}
