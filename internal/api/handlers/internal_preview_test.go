package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	previewsvc "github.com/assembledhq/143/internal/services/preview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type internalPreviewTestInspector struct{}

func (internalPreviewTestInspector) CaptureScreenshot(context.Context, string, models.ScreenshotOpts) (*models.ScreenshotResult, error) {
	return &models.ScreenshotResult{PageTitle: "Preview Title", URL: "http://preview.local"}, nil
}
func (internalPreviewTestInspector) CaptureDOM(context.Context, string, previewsvc.DOMCaptureOpts) (*previewsvc.DOMSnapshot, error) {
	return &previewsvc.DOMSnapshot{}, nil
}
func (internalPreviewTestInspector) ReadConsole(context.Context, string) ([]previewsvc.ConsoleMessage, error) {
	return []previewsvc.ConsoleMessage{{Level: "info", Text: "ready"}}, nil
}
func (internalPreviewTestInspector) InspectElement(context.Context, string, int, int) (*models.ElementInfo, error) {
	return &models.ElementInfo{TagName: "div", DOMPath: "#app"}, nil
}
func (internalPreviewTestInspector) StartScreencast(context.Context, string, int) (string, error) {
	return "screencast-1", nil
}
func (internalPreviewTestInspector) StopScreencast(context.Context, string) (*models.ScreencastResult, error) {
	return &models.ScreencastResult{}, nil
}
func (internalPreviewTestInspector) ExecuteInteraction(context.Context, string, []models.InteractionStep) (*models.InteractionResult, error) {
	return &models.InteractionResult{
		FinalURL: "http://preview.local/final",
		Steps:    []models.StepResult{{StepIndex: 0, Action: "click", Success: true}},
	}, nil
}
func (internalPreviewTestInspector) CaptureMultiViewport(context.Context, string, models.MultiViewportOpts) (*models.MultiViewportResult, error) {
	return &models.MultiViewportResult{}, nil
}
func (internalPreviewTestInspector) ComputeVisualDiff(context.Context, string, string, string) (*models.VisualDiff, error) {
	return &models.VisualDiff{PixelDiffPercent: 1.5, Summary: "changed"}, nil
}
func (internalPreviewTestInspector) RunAssertions(context.Context, string, []previewsvc.Assertion) (*previewsvc.AssertionResult, error) {
	return &previewsvc.AssertionResult{
		Passed: 1,
		Failed: 0,
		Total:  1,
		Results: []previewsvc.AssertionCheck{{
			Passed:  true,
			Message: "ok",
		}},
	}, nil
}
func (internalPreviewTestInspector) Close() error { return nil }

func newInternalPreviewTestHandler(manager *previewsvc.Manager) *InternalPreviewHandler {
	return NewInternalPreviewHandler(&PreviewHandler{logger: zerolog.Nop()}, manager, "worker-1", "worker-secret", zerolog.Nop())
}

func internalPreviewAuthHeader(t *testing.T, claims auth.PreviewTokenClaims) string {
	t.Helper()

	token, err := auth.GeneratePreviewToken("worker-secret", claims)
	require.NoError(t, err, "preview test token should be generated")
	return "Bearer " + token
}

func withPreviewRouteParam(r *http.Request, previewID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("previewID", previewID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func mustJSONBody(t *testing.T, v any) string {
	t.Helper()

	payload, err := json.Marshal(v)
	require.NoError(t, err, "test request bodies should marshal")
	return string(payload)
}

func TestInternalPreviewHandler_AuthorizeFailures(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	handler := newInternalPreviewTestHandler(nil)

	tests := []struct {
		name       string
		header     string
		action     string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "missing token",
			action:     "start",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "UNAUTHORIZED",
		},
		{
			name:       "invalid token",
			header:     "Bearer not-a-token",
			action:     "start",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "UNAUTHORIZED",
		},
		{
			name: "wrong worker target",
			header: internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
				OrgID: orgID, SessionID: &sessionID, TargetNodeID: "worker-2", Action: "start", ExpiresAt: time.Now().Add(time.Minute),
			}),
			action:     "start",
			wantStatus: http.StatusForbidden,
			wantCode:   "WRONG_PREVIEW_WORKER",
		},
		{
			name: "wrong action",
			header: internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
				OrgID: orgID, SessionID: &sessionID, TargetNodeID: "worker-1", Action: "stop", ExpiresAt: time.Now().Add(time.Minute),
			}),
			action:     "start",
			wantStatus: http.StatusForbidden,
			wantCode:   "PREVIEW_ACTION_MISMATCH",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/internal/preview/start", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rr := httptest.NewRecorder()

			_, ok := handler.authorize(rr, req, tt.action)
			require.False(t, ok, "authorize should reject invalid preview tokens")
			require.Equal(t, tt.wantStatus, rr.Code, "authorize should return the expected status code")

			var resp models.ErrorResponse
			require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "authorize should return a JSON error")
			require.Equal(t, tt.wantCode, resp.Error.Code, "authorize should return the expected error code")
		})
	}
}

func TestInternalPreviewHandler_StartPreview_RejectsMismatches(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	handler := newInternalPreviewTestHandler(nil)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid json",
			body:       "{",
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_BODY",
		},
		{
			name: "org mismatch",
			body: mustJSONBody(t, previewsvc.RemoteStartPreviewRequest{
				OrgID: uuid.New(), UserID: userID, SessionID: sessionID,
			}),
			wantStatus: http.StatusForbidden,
			wantCode:   "ORG_MISMATCH",
		},
		{
			name: "session mismatch",
			body: mustJSONBody(t, previewsvc.RemoteStartPreviewRequest{
				OrgID: orgID, UserID: userID, SessionID: uuid.New(),
			}),
			wantStatus: http.StatusForbidden,
			wantCode:   "SESSION_MISMATCH",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/internal/preview/start", strings.NewReader(tt.body))
			req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
				OrgID: orgID, SessionID: &sessionID, TargetNodeID: "worker-1", Action: "start", ExpiresAt: time.Now().Add(time.Minute),
			}))
			rr := httptest.NewRecorder()

			handler.StartPreview(rr, req)
			require.Equal(t, tt.wantStatus, rr.Code, "StartPreview should reject invalid requests before hydrating a preview")

			var resp models.ErrorResponse
			require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "StartPreview should return a JSON error")
			require.Equal(t, tt.wantCode, resp.Error.Code, "StartPreview should return the expected error code")
		})
	}
}

func TestInternalPreviewHandler_StopAndRecycleRejectMismatches(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	previewID := uuid.New()
	handler := newInternalPreviewTestHandler(nil)

	tests := []struct {
		name       string
		handlerFn  http.HandlerFunc
		action     string
		routeID    string
		tokenID    *uuid.UUID
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid preview id",
			handlerFn:  handler.StopPreview,
			action:     "stop",
			routeID:    "not-a-uuid",
			tokenID:    &previewID,
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_PREVIEW_ID",
		},
		{
			name:       "preview mismatch",
			handlerFn:  handler.RecyclePreview,
			action:     "recycle",
			routeID:    previewID.String(),
			tokenID:    ptrUUID(uuid.New()),
			wantStatus: http.StatusForbidden,
			wantCode:   "PREVIEW_MISMATCH",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/internal/preview/"+tt.routeID+"/stop", nil)
			req = withPreviewRouteParam(req, tt.routeID)
			if tt.tokenID != nil {
				req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
					OrgID: orgID, PreviewID: tt.tokenID, TargetNodeID: "worker-1", Action: tt.action, ExpiresAt: time.Now().Add(time.Minute),
				}))
			}
			rr := httptest.NewRecorder()

			tt.handlerFn(rr, req)
			require.Equal(t, tt.wantStatus, rr.Code, "preview lifecycle handlers should reject invalid preview ids")

			var resp models.ErrorResponse
			require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "preview lifecycle handlers should return JSON errors")
			require.Equal(t, tt.wantCode, resp.Error.Code, "preview lifecycle handlers should return the expected error code")
		})
	}
}

func TestInternalPreviewHandler_StopActivePreviewForSession_RejectsMismatches(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	handler := newInternalPreviewTestHandler(nil)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid body",
			body:       "{",
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_BODY",
		},
		{
			name:       "org mismatch",
			body:       mustJSONBody(t, previewsvc.RemoteStopActivePreviewForSessionRequest{OrgID: uuid.New(), SessionID: sessionID}),
			wantStatus: http.StatusForbidden,
			wantCode:   "ORG_MISMATCH",
		},
		{
			name:       "session mismatch",
			body:       mustJSONBody(t, previewsvc.RemoteStopActivePreviewForSessionRequest{OrgID: orgID, SessionID: uuid.New()}),
			wantStatus: http.StatusForbidden,
			wantCode:   "SESSION_MISMATCH",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/internal/preview/stop-session", strings.NewReader(tt.body))
			req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
				OrgID: orgID, SessionID: &sessionID, TargetNodeID: "worker-1", Action: "stop_session", ExpiresAt: time.Now().Add(time.Minute),
			}))
			rr := httptest.NewRecorder()

			handler.StopActivePreviewForSession(rr, req)
			require.Equal(t, tt.wantStatus, rr.Code, "StopActivePreviewForSession should reject invalid requests")

			var resp models.ErrorResponse
			require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "StopActivePreviewForSession should return a JSON error")
			require.Equal(t, tt.wantCode, resp.Error.Code, "StopActivePreviewForSession should return the expected error code")
		})
	}
}

func TestInternalPreviewHandler_InspectorEndpoints(t *testing.T) {
	t.Parallel()

	previewID := uuid.New()
	orgID := uuid.New()
	manager := previewsvc.NewManager(previewsvc.ManagerConfig{Logger: zerolog.Nop(), WorkerNodeID: "worker-1"})
	manager.SetInspector(internalPreviewTestInspector{})
	handler := newInternalPreviewTestHandler(manager)

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		action     string
		handlerFn  http.HandlerFunc
		wantStatus int
	}{
		{"capture screenshot", http.MethodPost, "/internal/preview/" + previewID.String() + "/screenshot", `{}`, "screenshot", handler.CaptureScreenshot, http.StatusOK},
		{"inspect element", http.MethodPost, "/internal/preview/" + previewID.String() + "/inspect", `{"x":10,"y":20}`, "inspect", handler.InspectElement, http.StatusOK},
		{"read console", http.MethodGet, "/internal/preview/" + previewID.String() + "/console", ``, "console", handler.ReadConsole, http.StatusOK},
		{"execute interaction", http.MethodPost, "/internal/preview/" + previewID.String() + "/interact", `{"steps":[{"action":"click"}]}`, "interact", handler.ExecuteInteraction, http.StatusOK},
		{"multi viewport", http.MethodPost, "/internal/preview/" + previewID.String() + "/multi-viewport", `{}`, "multi_viewport", handler.CaptureMultiViewport, http.StatusOK},
		{"visual diff", http.MethodPost, "/internal/preview/" + previewID.String() + "/visual-diff", `{"before_snapshot_id":"before","after_snapshot_id":"after"}`, "visual_diff", handler.ComputeVisualDiff, http.StatusOK},
		{"run assertions", http.MethodPost, "/internal/preview/" + previewID.String() + "/assert", `{"assertions":[{"type":"text"}]}`, "assert", handler.RunAssertions, http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req = withPreviewRouteParam(req, previewID.String())
			req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
				OrgID: orgID, PreviewID: &previewID, TargetNodeID: "worker-1", Action: tt.action, ExpiresAt: time.Now().Add(time.Minute),
			}))
			rr := httptest.NewRecorder()

			tt.handlerFn(rr, req)
			require.Equal(t, tt.wantStatus, rr.Code, "inspector-backed internal preview handlers should succeed with a configured inspector")
		})
	}
}

func TestInternalPreviewHandler_InspectorUnavailable(t *testing.T) {
	t.Parallel()

	previewID := uuid.New()
	orgID := uuid.New()
	handler := newInternalPreviewTestHandler(nil)

	req := httptest.NewRequest(http.MethodPost, "/internal/preview/"+previewID.String()+"/inspect", strings.NewReader(`{"x":1,"y":2}`))
	req = withPreviewRouteParam(req, previewID.String())
	req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
		OrgID: orgID, PreviewID: &previewID, TargetNodeID: "worker-1", Action: "inspect", ExpiresAt: time.Now().Add(time.Minute),
	}))
	rr := httptest.NewRecorder()

	handler.InspectElement(rr, req)
	require.Equal(t, http.StatusNotImplemented, rr.Code, "InspectElement should report when no inspector is configured")
}

func TestInternalPreviewHandler_ProxyFailuresAndHelpers(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	store := db.NewPreviewStore(mock)
	manager := previewsvc.NewManager(previewsvc.ManagerConfig{
		Store:        store,
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})
	handler := newInternalPreviewTestHandler(manager)

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	req := httptest.NewRequest(http.MethodGet, "/internal/preview/"+previewID.String()+"/proxy/assets/app.js", nil)
	req = withPreviewRouteParam(req, previewID.String())
	req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
		OrgID: orgID, PreviewID: &previewID, TargetNodeID: "worker-1", Action: "proxy", ExpiresAt: time.Now().Add(time.Minute),
	}))
	rr := httptest.NewRecorder()

	handler.Proxy(rr, req)
	require.Equal(t, http.StatusBadGateway, rr.Code, "Proxy should return a bad gateway when the preview backend cannot be dialed")

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	wsReq := httptest.NewRequest(http.MethodGet, "/internal/preview/"+previewID.String()+"/proxy/socket", nil)
	wsReq = withPreviewRouteParam(wsReq, previewID.String())
	wsReq.Header.Set("Connection", "upgrade")
	wsReq.Header.Set("Upgrade", "websocket")
	wsReq.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
		OrgID: orgID, PreviewID: &previewID, TargetNodeID: "worker-1", Action: "proxy", ExpiresAt: time.Now().Add(time.Minute),
	}))
	wsResp := httptest.NewRecorder()

	handler.Proxy(wsResp, wsReq)
	require.Equal(t, http.StatusBadGateway, wsResp.Code, "Proxy should return a bad gateway for websocket dial failures")

	cloned := cloneWebSocketRequestForInternalProxy(req, previewID)
	require.Equal(t, "/assets/app.js", cloned.URL.Path, "cloneWebSocketRequestForInternalProxy should trim the internal proxy prefix")
	require.Empty(t, cloned.Header.Get("Authorization"), "cloneWebSocketRequestForInternalProxy should strip auth headers")

	require.Equal(t, "/socket", trimInternalPreviewProxyPath("/internal/preview/"+previewID.String()+"/proxy/socket", previewID), "trimInternalPreviewProxyPath should return the backend path")
	require.Equal(t, "/", trimInternalPreviewProxyPath("/internal/preview/"+previewID.String()+"/proxy", previewID), "trimInternalPreviewProxyPath should normalize the root path")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func ptrUUID(id uuid.UUID) *uuid.UUID {
	return &id
}
