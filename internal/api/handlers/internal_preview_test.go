package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
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

type internalPreviewErrorInspector struct{}

func (internalPreviewErrorInspector) CaptureScreenshot(context.Context, string, models.ScreenshotOpts) (*models.ScreenshotResult, error) {
	return nil, errors.New("screenshot failed")
}
func (internalPreviewErrorInspector) CaptureDOM(context.Context, string, previewsvc.DOMCaptureOpts) (*previewsvc.DOMSnapshot, error) {
	return nil, errors.New("dom failed")
}
func (internalPreviewErrorInspector) ReadConsole(context.Context, string) ([]previewsvc.ConsoleMessage, error) {
	return nil, errors.New("console failed")
}
func (internalPreviewErrorInspector) InspectElement(context.Context, string, int, int) (*models.ElementInfo, error) {
	return nil, errors.New("inspect failed")
}
func (internalPreviewErrorInspector) StartScreencast(context.Context, string, int) (string, error) {
	return "", errors.New("screencast start failed")
}
func (internalPreviewErrorInspector) StopScreencast(context.Context, string) (*models.ScreencastResult, error) {
	return nil, errors.New("screencast stop failed")
}
func (internalPreviewErrorInspector) ExecuteInteraction(context.Context, string, []models.InteractionStep) (*models.InteractionResult, error) {
	return nil, errors.New("interaction failed")
}
func (internalPreviewErrorInspector) CaptureMultiViewport(context.Context, string, models.MultiViewportOpts) (*models.MultiViewportResult, error) {
	return nil, errors.New("multi viewport failed")
}
func (internalPreviewErrorInspector) ComputeVisualDiff(context.Context, string, string, string) (*models.VisualDiff, error) {
	return nil, errors.New("visual diff failed")
}
func (internalPreviewErrorInspector) RunAssertions(context.Context, string, []previewsvc.Assertion) (*previewsvc.AssertionResult, error) {
	return nil, errors.New("assertions failed")
}
func (internalPreviewErrorInspector) Close() error { return nil }

type internalPreviewTestProvider struct {
	dialFn func(context.Context, string) (previewsvc.PreviewStream, error)
}

func (p internalPreviewTestProvider) StartPreview(context.Context, *agent.Sandbox, *models.PreviewConfig, previewsvc.StartPreviewOptions, previewsvc.ServiceObserver) (*previewsvc.PreviewHandle, error) {
	return nil, errors.New("not implemented")
}

func (p internalPreviewTestProvider) StopPreview(context.Context, string) error { return nil }

func (p internalPreviewTestProvider) DialPreview(ctx context.Context, handle string) (previewsvc.PreviewStream, error) {
	return p.dialFn(ctx, handle)
}

func (p internalPreviewTestProvider) PreviewStatus(context.Context, string) (*previewsvc.PreviewStatusSnapshot, error) {
	return nil, nil
}

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

func TestInternalPreviewHandler_AuthorizePreviewActionRequiresRuntimeIdentity(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	previewID := uuid.New()
	handler := newInternalPreviewTestHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/internal/preview/"+previewID.String()+"/proxy", nil)
	req = withPreviewRouteParam(req, previewID.String())
	req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
		OrgID:        orgID,
		PreviewID:    &previewID,
		TargetNodeID: "worker-1",
		Action:       "proxy",
		ExpiresAt:    time.Now().Add(time.Minute),
	}))
	rr := httptest.NewRecorder()

	_, _, ok := handler.authorizePreviewAction(rr, req, "proxy")
	require.False(t, ok, "authorizePreviewAction should reject preview proxy tokens without runtime identity")
	require.Equal(t, http.StatusForbidden, rr.Code, "authorizePreviewAction should fail closed on missing runtime identity")

	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "authorizePreviewAction should return a JSON error")
	require.Equal(t, "PREVIEW_RUNTIME_MISMATCH", resp.Error.Code, "authorizePreviewAction should report runtime identity mismatch")
}

func TestInternalPreviewHandler_AuthorizePreviewActionRejectsStaleRuntime(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	staleRuntimeID := uuid.New()
	now := time.Now().UTC()

	store := db.NewPreviewStore(mock)
	handler := NewInternalPreviewHandler(&PreviewHandler{store: store, logger: zerolog.Nop()}, nil, "worker-1", "worker-secret", zerolog.Nop())

	mock.ExpectQuery("SELECT .+ FROM preview_runtimes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "preview_instance_id", "runtime_epoch", "worker_node_id",
			"endpoint_url", "preview_handle", "primary_port", "status", "lease_expires_at",
			"last_heartbeat_at", "drain_requested_at", "stopped_at", "error", "created_at", "updated_at",
		}).AddRow(
			runtimeID, orgID, previewID, 2, "worker-1",
			"http://worker-1.internal", "handle-1", 3000, string(models.PreviewRuntimeStatusReady), now.Add(time.Minute),
			now, nil, nil, "", now, now,
		))

	req := httptest.NewRequest(http.MethodGet, "/internal/preview/"+previewID.String()+"/proxy", nil)
	req = withPreviewRouteParam(req, previewID.String())
	req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
		OrgID:        orgID,
		PreviewID:    &previewID,
		TargetNodeID: "worker-1",
		RuntimeID:    &staleRuntimeID,
		RuntimeEpoch: 1,
		Action:       "proxy",
		ExpiresAt:    time.Now().Add(time.Minute),
	}))
	rr := httptest.NewRecorder()

	_, _, ok := handler.authorizePreviewAction(rr, req, "proxy")
	require.False(t, ok, "authorizePreviewAction should reject proxy tokens for stale runtimes")
	require.Equal(t, http.StatusForbidden, rr.Code, "authorizePreviewAction should fail closed on stale runtime identity")

	var resp models.ErrorResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "authorizePreviewAction should return a JSON error")
	require.Equal(t, "PREVIEW_RUNTIME_MISMATCH", resp.Error.Code, "authorizePreviewAction should report runtime identity mismatch")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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

func TestInternalPreviewHandler_InspectorEndpoints_RejectInvalidBodiesAndSurfaceFailures(t *testing.T) {
	t.Parallel()

	previewID := uuid.New()
	orgID := uuid.New()

	t.Run("invalid bodies", func(t *testing.T) {
		t.Parallel()

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
			wantCode   string
			wantStatus int
		}{
			{"screenshot", http.MethodPost, "/internal/preview/" + previewID.String() + "/screenshot", "{", "screenshot", handler.CaptureScreenshot, "INVALID_BODY", http.StatusBadRequest},
			{"inspect", http.MethodPost, "/internal/preview/" + previewID.String() + "/inspect", "{", "inspect", handler.InspectElement, "INVALID_BODY", http.StatusBadRequest},
			{"interact", http.MethodPost, "/internal/preview/" + previewID.String() + "/interact", "{", "interact", handler.ExecuteInteraction, "INVALID_BODY", http.StatusBadRequest},
			{"multi viewport", http.MethodPost, "/internal/preview/" + previewID.String() + "/multi-viewport", "{", "multi_viewport", handler.CaptureMultiViewport, "INVALID_BODY", http.StatusBadRequest},
			{"visual diff", http.MethodPost, "/internal/preview/" + previewID.String() + "/visual-diff", "{", "visual_diff", handler.ComputeVisualDiff, "INVALID_BODY", http.StatusBadRequest},
			{"assertions", http.MethodPost, "/internal/preview/" + previewID.String() + "/assert", "{", "assert", handler.RunAssertions, "INVALID_BODY", http.StatusBadRequest},
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
				require.Equal(t, tt.wantStatus, rr.Code, "internal preview handlers should reject malformed JSON")

				var resp models.ErrorResponse
				require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "internal preview handlers should return a JSON error for malformed JSON")
				require.Equal(t, tt.wantCode, resp.Error.Code, "internal preview handlers should return the expected error code")
			})
		}
	})

	t.Run("inspector failures", func(t *testing.T) {
		t.Parallel()

		manager := previewsvc.NewManager(previewsvc.ManagerConfig{Logger: zerolog.Nop(), WorkerNodeID: "worker-1"})
		manager.SetInspector(internalPreviewErrorInspector{})
		handler := newInternalPreviewTestHandler(manager)

		tests := []struct {
			name       string
			method     string
			path       string
			body       string
			action     string
			handlerFn  http.HandlerFunc
			wantCode   string
			wantStatus int
		}{
			{"screenshot", http.MethodPost, "/internal/preview/" + previewID.String() + "/screenshot", `{}`, "screenshot", handler.CaptureScreenshot, "SCREENSHOT_FAILED", http.StatusInternalServerError},
			{"inspect", http.MethodPost, "/internal/preview/" + previewID.String() + "/inspect", `{"x":1,"y":2}`, "inspect", handler.InspectElement, "INSPECT_FAILED", http.StatusInternalServerError},
			{"console", http.MethodGet, "/internal/preview/" + previewID.String() + "/console", ``, "console", handler.ReadConsole, "CONSOLE_READ_FAILED", http.StatusInternalServerError},
			{"interact", http.MethodPost, "/internal/preview/" + previewID.String() + "/interact", `{"steps":[{"action":"click"}]}`, "interact", handler.ExecuteInteraction, "INTERACTION_FAILED", http.StatusInternalServerError},
			{"multi viewport", http.MethodPost, "/internal/preview/" + previewID.String() + "/multi-viewport", `{}`, "multi_viewport", handler.CaptureMultiViewport, "MULTI_VIEWPORT_FAILED", http.StatusInternalServerError},
			{"visual diff", http.MethodPost, "/internal/preview/" + previewID.String() + "/visual-diff", `{"before_snapshot_id":"before","after_snapshot_id":"after"}`, "visual_diff", handler.ComputeVisualDiff, "VISUAL_DIFF_FAILED", http.StatusInternalServerError},
			{"assertions", http.MethodPost, "/internal/preview/" + previewID.String() + "/assert", `{"assertions":[{"type":"text"}]}`, "assert", handler.RunAssertions, "ASSERTIONS_FAILED", http.StatusInternalServerError},
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
				require.Equal(t, tt.wantStatus, rr.Code, "internal preview handlers should surface inspector failures")

				var resp models.ErrorResponse
				require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "internal preview handlers should return a JSON error for inspector failures")
				require.Equal(t, tt.wantCode, resp.Error.Code, "internal preview handlers should return the expected inspector failure code")
			})
		}
	})
}

func TestInternalPreviewHandler_ProxyFailuresAndHelpers(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
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
		OrgID: orgID, PreviewID: &previewID, TargetNodeID: "worker-1", RuntimeID: &runtimeID, RuntimeEpoch: 1, Action: "proxy", ExpiresAt: time.Now().Add(time.Minute),
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
		OrgID: orgID, PreviewID: &previewID, TargetNodeID: "worker-1", RuntimeID: &runtimeID, RuntimeEpoch: 1, Action: "proxy", ExpiresAt: time.Now().Add(time.Minute),
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

func TestInternalPreviewHandler_ProxyHTTP_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	go func() {
		defer serverConn.Close()

		req, readErr := http.ReadRequest(bufio.NewReader(serverConn))
		if readErr != nil {
			return
		}
		require.Equal(t, "/assets/app.js", req.URL.Path, "internal proxy should trim the worker proxy prefix before dialing the preview backend")
		require.Empty(t, req.Header.Get("Authorization"), "internal proxy should strip authorization headers before dialing the preview backend")
		require.Equal(t, "csrf_token=csrf-token; session_token=session-token", req.Header.Get("Cookie"), "internal proxy should preserve preview-app cookies for in-sandbox auth and CSRF")
		_, _ = io.WriteString(serverConn, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 2\r\n\r\nok")
	}()

	store := db.NewPreviewStore(mock)
	manager := previewsvc.NewManager(previewsvc.ManagerConfig{
		Store:        store,
		Provider:     internalPreviewTestProvider{dialFn: func(context.Context, string) (previewsvc.PreviewStream, error) { return clientConn, nil }},
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})
	handler := newInternalPreviewTestHandler(manager)

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	req := httptest.NewRequest(http.MethodGet, "/internal/preview/"+previewID.String()+"/proxy/assets/app.js", nil)
	req = withPreviewRouteParam(req, previewID.String())
	req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
		OrgID: orgID, PreviewID: &previewID, TargetNodeID: "worker-1", RuntimeID: &runtimeID, RuntimeEpoch: 1, Action: "proxy", ExpiresAt: time.Now().Add(time.Minute),
	}))
	req.Header.Set("Cookie", "__Host-preview_session=preview-secret; csrf_token=csrf-token; session_token=session-token")
	rr := httptest.NewRecorder()

	handler.Proxy(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "Proxy should relay successful HTTP responses from the preview backend")
	require.Equal(t, "ok", rr.Body.String(), "Proxy should relay the preview backend response body")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestInternalPreviewHandler_ProxyHTTP_LogsRequestAndRuntimeOnProxyError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	requestRead := make(chan struct{})
	go func() {
		defer serverConn.Close()
		_, _ = http.ReadRequest(bufio.NewReader(serverConn))
		close(requestRead)
	}()

	store := db.NewPreviewStore(mock)
	manager := previewsvc.NewManager(previewsvc.ManagerConfig{
		Store:        store,
		Provider:     internalPreviewTestProvider{dialFn: func(context.Context, string) (previewsvc.PreviewStream, error) { return clientConn, nil }},
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})
	var logs bytes.Buffer
	handler := NewInternalPreviewHandler(&PreviewHandler{logger: zerolog.Nop()}, manager, "worker-1", "worker-secret", zerolog.New(&logs))

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	req := httptest.NewRequest(http.MethodGet, "/internal/preview/"+previewID.String()+"/proxy/static/js/ApplicationRoutes.chunk.js?cache=miss", nil)
	req = withPreviewRouteParam(req, previewID.String())
	req.Host = "100.124.235.85:8080"
	req.Header.Set("Sec-Fetch-Dest", "script")
	req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
		OrgID: orgID, PreviewID: &previewID, TargetNodeID: "worker-1", RuntimeID: &runtimeID, RuntimeEpoch: 3, Action: "proxy", ExpiresAt: time.Now().Add(time.Minute),
	}))
	rr := httptest.NewRecorder()

	handler.Proxy(rr, req)
	<-requestRead

	require.Equal(t, http.StatusBadGateway, rr.Code, "Proxy should return a bad gateway when the preview backend drops the connection")
	require.NotEmpty(t, logs.String(), "internal proxy error should emit a structured log")

	var event map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event), "internal proxy error log should be valid JSON")
	require.Equal(t, "internal preview proxy error", event["message"], "internal proxy error log should keep the event message")
	require.Equal(t, previewID.String(), event["preview_id"], "internal proxy error log should include preview id")
	require.Equal(t, orgID.String(), event["org_id"], "internal proxy error log should include org id")
	require.Equal(t, http.MethodGet, event["request_method"], "internal proxy error log should include request method")
	require.Equal(t, "/internal/preview/"+previewID.String()+"/proxy/static/js/ApplicationRoutes.chunk.js", event["request_path"], "internal proxy error log should include internal request path without query string")
	require.Equal(t, true, event["query_present"], "internal proxy error log should record whether a query string was present without logging it")
	require.Equal(t, "script", event["sec_fetch_dest"], "internal proxy error log should include fetch destination")
	require.Equal(t, "/static/js/ApplicationRoutes.chunk.js", event["backend_path"], "internal proxy error log should include trimmed backend path")
	require.Equal(t, runtimeID.String(), event["runtime_id"], "internal proxy error log should include runtime id")
	require.Equal(t, float64(3), event["runtime_epoch"], "internal proxy error log should include runtime epoch")
	require.Equal(t, "worker-1", event["target_node_id"], "internal proxy error log should include token target node")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestInternalPreviewHandler_ProxyWebSocket_HijackUnsupported(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	runtimeID := uuid.New()
	now := time.Now().UTC()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	store := db.NewPreviewStore(mock)
	manager := previewsvc.NewManager(previewsvc.ManagerConfig{
		Store:        store,
		Provider:     internalPreviewTestProvider{dialFn: func(context.Context, string) (previewsvc.PreviewStream, error) { return clientConn, nil }},
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})
	handler := newInternalPreviewTestHandler(manager)

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	req := httptest.NewRequest(http.MethodGet, "/internal/preview/"+previewID.String()+"/proxy/socket", nil)
	req = withPreviewRouteParam(req, previewID.String())
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Authorization", internalPreviewAuthHeader(t, auth.PreviewTokenClaims{
		OrgID: orgID, PreviewID: &previewID, TargetNodeID: "worker-1", RuntimeID: &runtimeID, RuntimeEpoch: 1, Action: "proxy", ExpiresAt: time.Now().Add(time.Minute),
	}))
	rr := httptest.NewRecorder()

	handler.Proxy(rr, req)
	require.Equal(t, http.StatusInternalServerError, rr.Code, "Proxy should report when websocket hijacking is unavailable")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

type closeTrackingConn struct {
	net.Conn
	closed bool
}

func (c *closeTrackingConn) Close() error {
	c.closed = true
	if c.Conn != nil {
		return c.Conn.Close()
	}
	return nil
}

type errReadCloser struct{ err error }

func (errReadCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (e errReadCloser) Close() error           { return e.err }

func TestConnClosingBody_ClosePrefersBodyError(t *testing.T) {
	t.Parallel()

	conn := &closeTrackingConn{}
	body := &connClosingBody{ReadCloser: errReadCloser{err: errors.New("body close failed")}, conn: conn}

	err := body.Close()
	require.Error(t, err, "connClosingBody should surface body close failures")
	require.Contains(t, err.Error(), "body close failed", "connClosingBody should prefer the body close error")
	require.True(t, conn.closed, "connClosingBody should still close the underlying connection")
}

func TestCopyWithHMRSnoopToClient_ForwardsTraffic(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	watcher, err := previewsvc.NewHMRWatcher(previewsvc.HMRWatcherConfig{
		Inspector: internalPreviewTestInspector{},
		Store:     db.NewPreviewStore(mock),
		Logger:    zerolog.Nop(),
		BlobDir:   t.TempDir(),
	})
	require.NoError(t, err, "HMR watcher should be created for websocket snooping tests")

	previewID := uuid.New()
	watcher.StartWatching(previewID, uuid.New())

	var dst strings.Builder
	src := strings.NewReader(`{"type":"update","updates":[]}`)

	copyWithHMRSnoopToClient(zerolog.Nop(), watcher, &dst, src, previewID)
	require.Equal(t, `{"type":"update","updates":[]}`, dst.String(), "copyWithHMRSnoopToClient should forward websocket traffic unchanged")
}

func ptrUUID(id uuid.UUID) *uuid.UUID {
	return &id
}
