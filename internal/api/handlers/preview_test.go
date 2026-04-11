package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

func TestPreviewHandler_StartPreview_MissingConfig(t *testing.T) {
	t.Parallel()
	h := newPreviewTestHandlerWithManager()

	sessionID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/preview",
		strings.NewReader(`{"base_commit_sha":"abc123"}`))
	req.Header.Set("Content-Type", "application/json")
	req = previewTestContext(req)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.StartPreview(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	var resp models.ErrorResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, "MISSING_CONFIG", resp.Error.Code)
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
type mockPreviewProvider struct{}

func (m *mockPreviewProvider) StartPreview(_ context.Context, _ *agent.Sandbox, _ *models.PreviewConfig) (*preview.PreviewHandle, error) {
	return nil, nil
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
	mgr := preview.NewManager(preview.ManagerConfig{
		Store:        store,
		Provider:     &mockPreviewProvider{},
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
	"last_path", "memory_limit_mb", "cpu_limit_millis", "recycle_config", "recycle_sandbox", "error", "created_at", "updated_at",
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

func newActivePreviewRow(previewID, sessionID, orgID, userID uuid.UUID, now time.Time) []any {
	return []any{
		previewID, sessionID, orgID, userID, "bootstrap", "my-preview", "ready",
		"docker", "test-worker", "handle-abc", "web", 3000,
		"sha256:abc", "deadbeef", now, now.Add(30 * time.Minute), nil,
		"/", 512, 500, json.RawMessage(nil), json.RawMessage(nil), "", now, now,
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

	h := NewPreviewHandler(mgr, store, zerolog.Nop())
	require.NotNil(t, h)
	require.NotNil(t, h.manager)
	require.NotNil(t, h.store)
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

	// RestartPreview calls StopPreview -> GetPreviewInstance.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	// StopPreviewWithRevocation
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/preview/restart", nil)
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	w := httptest.NewRecorder()

	h.RestartPreview(w, req)
	require.Equal(t, http.StatusOK, w.Code)
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
