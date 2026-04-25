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
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
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

func TestPreviewHandler_StartPreview_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := defaultPreviewConfig()

	require.Equal(t, "default", cfg.Name, "default preview config should use the default profile name")
	require.Equal(t, "app", cfg.Primary, "default preview config should set the primary service")
	require.Equal(t, []string{"npm", "start"}, cfg.Services["app"].Command, "default preview config should run npm start")
	require.Equal(t, 3000, cfg.Services["app"].Port, "default preview config should expose the default Node port")
	require.Equal(t, "/", cfg.Services["app"].Ready.HTTPPath, "default preview config should include a readiness probe path")

	errs := preview.ValidateConfig(cfg)
	require.Empty(t, errs, "default preview config should satisfy preview validation")
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
		{"ExtendTTL", h.ExtendTTL},
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

func (m *mockPreviewProvider) StartPreview(_ context.Context, _ *agent.Sandbox, cfg *models.PreviewConfig, _ map[string]string) (*preview.PreviewHandle, error) {
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
	"last_path", "memory_limit_mb", "cpu_limit_millis", "recycle_config", "recycle_sandbox", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
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
		"/", 512, 500, json.RawMessage("{}"), json.RawMessage("{}"), "", now, now, nil, nil,
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

	// CreatePreviewInstance: 19 bound args.
	insertArgs := make([]any, 19)
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

// expectAbortReservationNoDestroy emits the pgxmock sequence for an Abort that
// releases the hold without destroying a container (either the sandbox was
// reused so hydratedContainerID is "", or the turn still holds it). Callers
// that need the destroy path should build it inline.
func expectAbortReservationNoDestroy(mock pgxmock.PgxPoolIface) {
	// UpdatePreviewStatus: id, org_id, status, error (4 args).
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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
		"/", 512, 500, recycleConfig, recycleSandbox, "", now, now, now, nil,
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
	h := NewPreviewHandler(mgr, store, sessionStore, sandbox.NoOpFileReader{}, nil, nil, zerolog.Nop())
	require.NotNil(t, h)
	require.NotNil(t, h.manager)
	require.NotNil(t, h.store)
}

// =============================================================================
// readWorkspacePreviewConfig tests
// =============================================================================

// fakeFileReader is a stub FileReader that returns canned ReadFile responses.
// Unused methods panic so this test file fails loudly if something else grows
// a dependency on them.
type fakeFileReader struct {
	content string
	err     error
}

func (f fakeFileReader) ListDir(context.Context, string, string, string) ([]sandbox.FileEntry, error) {
	panic("not used")
}

func (f fakeFileReader) ReadFile(_ context.Context, _, _, _ string) (string, bool, error) {
	return f.content, false, f.err
}

func (f fakeFileReader) ReadFileContext(context.Context, string, string, string, int, int, int) (sandbox.FileContextResult, error) {
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

	// The common "no .143/preview.json committed" case: the underlying FileReader
	// returns sandbox.ErrFileNotFound (wrapped). Must NOT bubble up — caller
	// falls back to built-in defaults.
	h := &PreviewHandler{
		fileReader: fakeFileReader{err: fmt.Errorf("read file .143/preview.json: %w", sandbox.ErrFileNotFound)},
		logger:     zerolog.Nop(),
	}
	cfg, err := h.readWorkspacePreviewConfig(context.Background(), &agent.Sandbox{ID: "c1", WorkDir: "/workspace"}, uuid.New())
	require.NoError(t, err, "ENOENT is expected absence, not an error to surface")
	require.Nil(t, cfg)
}

func TestReadWorkspacePreviewConfig_UnexpectedReadError(t *testing.T) {
	t.Parallel()

	// A non-ENOENT read error (docker exec failure, context cancel, sandbox
	// gone) means we cannot tell whether a committed config exists. Returning
	// (nil, err) makes StartPreview surface a 500 instead of silently swapping
	// in Node.js defaults — which would start the wrong preview for non-Node
	// projects and time out after minutes.
	wantErr := errors.New("docker exec failed: container not running")
	h := &PreviewHandler{
		fileReader: fakeFileReader{err: wantErr},
		logger:     zerolog.Nop(),
	}
	cfg, err := h.readWorkspacePreviewConfig(context.Background(), &agent.Sandbox{ID: "c1", WorkDir: "/workspace"}, uuid.New())
	require.Error(t, err, "non-ENOENT read errors must surface so the caller returns 500")
	require.ErrorIs(t, err, wantErr, "underlying read error must be wrapped, not discarded")
	require.Nil(t, cfg)
}

func TestReadWorkspacePreviewConfig_ParseError(t *testing.T) {
	t.Parallel()

	// Parse errors are a user authoring problem, not infrastructure. We still
	// fall back to defaults (returning the 500 would make the preview strictly
	// worse than just running the default), but log at Warn so the user sees
	// why their committed config didn't take effect.
	h := &PreviewHandler{
		fileReader: fakeFileReader{content: "{not valid json"},
		logger:     zerolog.Nop(),
	}
	cfg, err := h.readWorkspacePreviewConfig(context.Background(), &agent.Sandbox{ID: "c1", WorkDir: "/workspace"}, uuid.New())
	require.NoError(t, err, "invalid JSON must fall through to defaults, not surface as a 500")
	require.Nil(t, cfg)
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
		fileReader: fakeFileReader{content: raw},
		logger:     zerolog.Nop(),
	}
	cfg, err := h.readWorkspacePreviewConfig(context.Background(), &agent.Sandbox{ID: "c1", WorkDir: "/workspace"}, uuid.New())
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, "web", cfg.Primary)
	require.Contains(t, cfg.Services, "web")
	require.Equal(t, 3000, cfg.Services["web"].Port)
}

// sessionRowColumns mirrors db.sessionSelectColumns. Kept inline so a
// schema change in one file flips this test red instead of silently
// returning the wrong shape from pgxmock.
var sessionRowColumns = []string{
	"id", "primary_issue_id", "org_id", "origin", "interaction_mode", "validation_policy", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "worker_node_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "reasoning_effort", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at",
	"sandbox_state", "snapshot_key",
	"runtime_soft_deadline_at", "runtime_hard_deadline_at", "runtime_last_progress_at", "runtime_last_progress_type", "runtime_last_progress_strength",
	"runtime_extension_count", "runtime_extension_seconds", "runtime_stop_reason", "runtime_graceful_stop_at",
	"checkpointed_at", "checkpoint_kind", "checkpoint_capability", "checkpoint_size_bytes", "checkpoint_error",
	"recovery_state", "recovery_queued_at", "recovery_started_at", "recovery_attempt_count",
	"target_branch", "working_branch",
	"base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest",
	"archived_at", "archived_by_user_id", "automation_run_id",
	"pr_creation_state", "pr_creation_error", "diff_collected_at", "latest_diff_snapshot_id",
	"deleted_at", "created_at",
}

func previewSessionRow(values ...interface{}) []interface{} {
	if len(values) == len(sessionRowColumns)-3 {
		row := make([]interface{}, 0, len(values)+3)
		row = append(row, values[:3]...)
		row = append(
			row,
			"",
			"",
			"",
		)
		row = append(row, values[3:]...)
		return row
	}
	return values
}

func sessionRowWithContainer(id, orgID uuid.UUID, containerID string) []interface{} {
	return previewSessionRow(
		id, nil, orgID, "claude_code", "running", "supervised", "low",
		nil, nil, nil, []string{},
		&containerID, nil, false, nil, nil, json.RawMessage(`{}`),
		nil, nil, []string{}, false,
		nil, json.RawMessage(`{}`), nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil, nil, nil, nil,
		0, time.Now(),
		// sandbox_state must be "running" for the reuse branch of
		// acquireSandbox to attach to the lingering container_id; otherwise
		// the stale-ID guard falls through to hydrate/expired.
		"running", nil,
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
		nil, nil,
		nil, nil, nil, nil, nil,
		nil, nil, nil, "idle", (*string)(nil), nil, nil, nil, time.Now(),
	)
}

// sessionRowReuseWithSnapshot builds a row that satisfies both the reuse
// precondition (container_id set, sandbox_state='running') AND the hydrate
// fallback precondition (snapshot_key set). Used by the zombie-reuse tests
// where IsAlive decides which branch the handler takes.
func sessionRowReuseWithSnapshot(id, orgID uuid.UUID, containerID string, snapshotKey *string) []interface{} {
	return previewSessionRow(
		id, nil, orgID, "claude_code", "running", "supervised", "low",
		nil, nil, nil, []string{},
		&containerID, nil, false, nil, nil, json.RawMessage(`{}`),
		nil, nil, []string{}, false,
		nil, json.RawMessage(`{}`), nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil, nil, nil, nil,
		0, time.Now(),
		"running", snapshotKey,
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
		nil, nil,
		nil, nil, nil, nil, nil,
		nil, nil, nil, "idle", (*string)(nil), nil, nil, nil, time.Now(),
	)
}

// sessionRowForHydrate builds a session row with no live container but a
// configurable snapshot key and sandbox state — used to exercise the three
// acquireSandbox branches (SNAPSHOT_EXPIRED, hydrate, NO_SANDBOX).
func sessionRowForHydrate(id, orgID uuid.UUID, snapshotKey *string, sandboxState string) []interface{} {
	return previewSessionRow(
		id, nil, orgID, "claude_code", "running", "supervised", "low",
		nil, nil, nil, []string{},
		nil, nil, false, nil, nil, json.RawMessage(`{}`),
		nil, nil, []string{}, false,
		nil, json.RawMessage(`{}`), nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil, nil, nil, nil,
		0, time.Now(),
		sandboxState, snapshotKey,
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
		nil, nil,
		nil, nil, nil, nil, nil,
		nil, nil, nil, "idle", (*string)(nil), nil, nil, nil, time.Now(),
	)
}

// fakeHydrateSnapshotStore is a minimal SnapshotStore that writes a canned
// payload on Load.
type fakeHydrateSnapshotStore struct {
	payload []byte
	loadErr error
}

func (f *fakeHydrateSnapshotStore) Save(context.Context, string, io.Reader) error {
	return nil
}

func (f *fakeHydrateSnapshotStore) Load(_ context.Context, _ string, w io.Writer) error {
	if f.loadErr != nil {
		return f.loadErr
	}
	_, _ = w.Write(f.payload)
	return nil
}

func (f *fakeHydrateSnapshotStore) Delete(context.Context, string) error { return nil }

// TestPreviewHandler_StartPreview_AutoDetectInfraError exercises the auto-detect
// branch of StartPreview when the workspace file reader returns a non-ENOENT
// error. The handler must surface a 500 instead of silently swapping in Node.js
// defaults — see readWorkspacePreviewConfig's docstring for the rationale.
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
	h.fileReader = fakeFileReader{err: errors.New("docker exec failed: container not running")}

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
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "PREVIEW_CAPACITY_REACHED", resp.Error.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewHandler_StartPreview_ColdStartRetriesAnotherWorkerOnCapacity(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()
	worker1Calls := 0
	worker2Calls := 0

	worker1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		worker1Calls++
		require.Equal(t, http.MethodPost, r.Method, "worker one should receive a start request")
		w.WriteHeader(http.StatusServiceUnavailable)
		require.NoError(t, json.NewEncoder(w).Encode(models.ErrorResponse{
			Error: models.ErrorDetail{
				Code:    "PREVIEW_CAPACITY_REACHED",
				Message: "all preview slots are in use",
			},
		}), "worker one should encode the capacity error")
	}))
	defer worker1.Close()

	returnedPreviewID := uuid.New()
	worker2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		worker2Calls++
		require.Equal(t, http.MethodPost, r.Method, "worker two should receive a start request")
		w.WriteHeader(http.StatusCreated)
		require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.PreviewInstance]{
			Data: &models.PreviewInstance{
				ID:           returnedPreviewID,
				SessionID:    sessionID,
				OrgID:        orgID,
				UserID:       userID,
				Status:       models.PreviewStatusReady,
				WorkerNodeID: "worker-2",
			},
		}), "worker two should encode the preview response")
	}))
	defer worker2.Close()

	sessionStore := db.NewSessionStore(mock)
	previewStore := db.NewPreviewStore(mock)
	nodeStore := db.NewNodeStore(mock)

	h := newPreviewTestHandlerWithManager()
	h.store = previewStore
	h.sessionStore = sessionStore
	h.SetWorkerRuntime(preview.NewWorkerSelector(nodeStore, previewStore), preview.NewWorkerPreviewClient("test-secret"), "api-node")

	worker1Meta, err := json.Marshal(preview.WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: worker1.URL,
	})
	require.NoError(t, err, "should marshal worker one metadata")
	worker2Meta, err := json.Marshal(preview.WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: worker2.URL,
	})
	require.NoError(t, err, "should marshal worker two metadata")

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionRowColumns).
				AddRow(sessionRowForHydrate(sessionID, orgID, nil, "none")...),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(
			pgxmock.NewRows(handlerNodeTestCols).
				AddRow("worker-1", "worker", "worker-1.internal", "active", worker1Meta, now, now).
				AddRow("worker-2", "worker", "worker-2.internal", "active", worker2Meta, now, now),
		)
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(
			pgxmock.NewRows(handlerNodeTestCols).
				AddRow("worker-1", "worker", "worker-1.internal", "active", worker1Meta, now, now).
				AddRow("worker-2", "worker", "worker-2.internal", "active", worker2Meta, now, now),
		)
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(""))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.StartPreview(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "cold-start preview should retry another worker after capacity races")
	var resp models.SingleResponse[*models.PreviewInstance]
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp), "response should decode")
	require.NotNil(t, resp.Data, "response should include a preview instance")
	require.Equal(t, returnedPreviewID, resp.Data.ID, "response should return the preview started on the retry worker")
	require.Equal(t, 1, worker1Calls, "first worker should be tried once")
	require.Equal(t, 1, worker2Calls, "second worker should be tried once after capacity")
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
	updateCfgArgs := make([]any, 9)
	for i := range updateCfgArgs {
		updateCfgArgs[i] = pgxmock.AnyArg()
	}
	mock.ExpectExec("UPDATE preview_instances\\s+SET name").
		WithArgs(updateCfgArgs...).
		WillReturnError(errors.New("db down"))
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
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
// of PublishHydratedContainerID: a concurrent orchestrator already published
// a different container_id, so the preview must destroy its local sandbox and
// surface a NO_SANDBOX error instructing the caller to retry.
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
	// COALESCE returns the orchestrator's pre-existing ID, proving our preview
	// lost the race.
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
	require.Contains(t, w.Body.String(), "NO_SANDBOX")
	require.Equal(t, 1, sp.GetDestroyCalls(), "local container must be destroyed on race loss")
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
	updateCfgArgs := make([]any, 9)
	for i := range updateCfgArgs {
		updateCfgArgs[i] = pgxmock.AnyArg()
	}
	mock.ExpectExec("UPDATE preview_instances\\s+SET name").
		WithArgs(updateCfgArgs...).
		WillReturnError(errors.New("db down"))
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
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
	updateCfgArgs := make([]any, 9)
	for i := range updateCfgArgs {
		updateCfgArgs[i] = pgxmock.AnyArg()
	}
	mock.ExpectExec("UPDATE preview_instances\\s+SET name").
		WithArgs(updateCfgArgs...).
		WillReturnError(errors.New("db down"))
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
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
	updateCfgArgs := make([]any, 9)
	for i := range updateCfgArgs {
		updateCfgArgs[i] = pgxmock.AnyArg()
	}
	mock.ExpectExec("UPDATE preview_instances\\s+SET name").
		WithArgs(updateCfgArgs...).
		WillReturnError(errors.New("db down"))
	expectAbortReservationNoDestroy(mock)

	h := newPreviewHandlerWithMock(mock)
	h.sessionStore = db.NewSessionStore(mock)
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

	req := httptest.NewRequest(http.MethodGet, "/preview", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.GetPreview(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	var resp models.ErrorResponse
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, "NO_ACTIVE_PREVIEW", resp.Error.Code)
	require.NoError(t, mock.ExpectationsWereMet())
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

	// StopPreview calls StopPreviewWithRevocation which does Begin + StopPreview + RevokeAll + Commit.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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

func TestPreviewHandler_ExtendTTL_Success(t *testing.T) {
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

	// ExtendTTL calls GetPreviewInstance.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// ExtendTTL calls UpdatePreviewExpiry.
	mock.ExpectExec("UPDATE preview_instances SET expires_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/preview/extend", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.ExtendTTL(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
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
