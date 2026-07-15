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
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPreviewHandler_RemoteWorkerRoutesLifecycleAndInspectorEndpoints(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name         string
		method       string
		workerMethod string
		body         string
		handler      func(*PreviewHandler, http.ResponseWriter, *http.Request)
		path         string
		wantStatus   int
		responseCode int
		serverAssert func(t *testing.T, w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID)
	}

	tests := []testCase{
		{
			name:         "stop preview",
			method:       http.MethodDelete,
			workerMethod: http.MethodPost,
			handler:      func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.StopPreview(w, r) },
			path:         "/internal/preview/%s/stop",
			wantStatus:   http.StatusOK,
			responseCode: http.StatusOK,
			serverAssert: func(t *testing.T, w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				claims, err := auth.ValidatePreviewToken("test-secret", token)
				require.NoError(t, err, "remote stop requests should carry a valid preview token")
				require.Equal(t, "stop", claims.Action, "remote stop requests should use the stop action")
				require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[map[string]string]{Data: map[string]string{"status": "stopped"}}), "remote stop should encode a success response")
			},
		},
		{
			name:         "restart preview",
			method:       http.MethodPost,
			workerMethod: http.MethodPost,
			handler:      func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.RestartPreview(w, r) },
			path:         "/internal/preview/%s/recycle",
			wantStatus:   http.StatusOK,
			responseCode: http.StatusOK,
			serverAssert: func(t *testing.T, w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				claims, err := auth.ValidatePreviewToken("test-secret", token)
				require.NoError(t, err, "remote recycle requests should carry a valid preview token")
				require.Equal(t, "recycle", claims.Action, "remote recycle requests should use the recycle action")
				require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[map[string]string]{Data: map[string]string{"status": "restarting"}}), "remote recycle should encode a success response")
			},
		},
		{
			name:         "capture screenshot",
			method:       http.MethodPost,
			workerMethod: http.MethodPost,
			body:         `{}`,
			handler:      func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureScreenshot(w, r) },
			path:         "/internal/preview/%s/screenshot",
			wantStatus:   http.StatusOK,
			responseCode: http.StatusOK,
			serverAssert: func(t *testing.T, w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				claims, err := auth.ValidatePreviewToken("test-secret", token)
				require.NoError(t, err, "remote screenshot requests should carry a valid preview token")
				require.Equal(t, "screenshot", claims.Action, "remote screenshot requests should use the screenshot action")
				require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.ScreenshotResult]{Data: &models.ScreenshotResult{PageTitle: "Preview Title", URL: "http://preview.local"}}), "remote screenshot should encode a success response")
			},
		},
		{
			name:         "inspect element",
			method:       http.MethodPost,
			workerMethod: http.MethodPost,
			body:         `{"x":10,"y":20}`,
			handler:      func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.InspectElement(w, r) },
			path:         "/internal/preview/%s/inspect",
			wantStatus:   http.StatusOK,
			responseCode: http.StatusOK,
			serverAssert: func(t *testing.T, w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				claims, err := auth.ValidatePreviewToken("test-secret", token)
				require.NoError(t, err, "remote inspect requests should carry a valid preview token")
				require.Equal(t, "inspect", claims.Action, "remote inspect requests should use the inspect action")
				require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.ElementInfo]{Data: &models.ElementInfo{TagName: "div", DOMPath: "#app"}}), "remote inspect should encode a success response")
			},
		},
		{
			name:         "read console",
			method:       http.MethodGet,
			workerMethod: http.MethodGet,
			handler:      func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ReadConsole(w, r) },
			path:         "/internal/preview/%s/console",
			wantStatus:   http.StatusOK,
			responseCode: http.StatusOK,
			serverAssert: func(t *testing.T, w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				claims, err := auth.ValidatePreviewToken("test-secret", token)
				require.NoError(t, err, "remote console requests should carry a valid preview token")
				require.Equal(t, "console", claims.Action, "remote console requests should use the console action")
				require.NoError(t, json.NewEncoder(w).Encode(models.ListResponse[previewsvc.ConsoleMessage]{Data: []previewsvc.ConsoleMessage{{Level: "info", Text: "ready"}}}), "remote console should encode a success response")
			},
		},
		{
			name:         "execute interaction",
			method:       http.MethodPost,
			workerMethod: http.MethodPost,
			body:         `{"steps":[{"action":"click"}]}`,
			handler:      func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ExecuteInteraction(w, r) },
			path:         "/internal/preview/%s/interact",
			wantStatus:   http.StatusOK,
			responseCode: http.StatusOK,
			serverAssert: func(t *testing.T, w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				claims, err := auth.ValidatePreviewToken("test-secret", token)
				require.NoError(t, err, "remote interaction requests should carry a valid preview token")
				require.Equal(t, "interact", claims.Action, "remote interaction requests should use the interact action")
				require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.InteractionResult]{Data: &models.InteractionResult{
					FinalURL: "http://preview.local/final",
					Steps:    []models.StepResult{{StepIndex: 0, Action: "click", Success: true}},
				}}), "remote interaction should encode a success response")
			},
		},
		{
			name:         "capture multi viewport",
			method:       http.MethodPost,
			workerMethod: http.MethodPost,
			body:         `{}`,
			handler:      func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureMultiViewport(w, r) },
			path:         "/internal/preview/%s/multi-viewport",
			wantStatus:   http.StatusOK,
			responseCode: http.StatusOK,
			serverAssert: func(t *testing.T, w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				claims, err := auth.ValidatePreviewToken("test-secret", token)
				require.NoError(t, err, "remote multi-viewport requests should carry a valid preview token")
				require.Equal(t, "multi_viewport", claims.Action, "remote multi-viewport requests should use the multi_viewport action")
				require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.MultiViewportResult]{Data: &models.MultiViewportResult{}}), "remote multi-viewport should encode a success response")
			},
		},
		{
			name:         "compute visual diff",
			method:       http.MethodPost,
			workerMethod: http.MethodPost,
			body:         `{"before_snapshot_id":"before","after_snapshot_id":"after"}`,
			handler:      func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ComputeVisualDiff(w, r) },
			path:         "/internal/preview/%s/visual-diff",
			wantStatus:   http.StatusOK,
			responseCode: http.StatusOK,
			serverAssert: func(t *testing.T, w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				claims, err := auth.ValidatePreviewToken("test-secret", token)
				require.NoError(t, err, "remote diff requests should carry a valid preview token")
				require.Equal(t, "visual_diff", claims.Action, "remote diff requests should use the visual_diff action")
				require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*models.VisualDiff]{Data: &models.VisualDiff{PixelDiffPercent: 2.5, Summary: "changed"}}), "remote diff should encode a success response")
			},
		},
		{
			name:         "run assertions",
			method:       http.MethodPost,
			workerMethod: http.MethodPost,
			body:         `{"assertions":[{"type":"text"}]}`,
			handler:      func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.RunAssertions(w, r) },
			path:         "/internal/preview/%s/assert",
			wantStatus:   http.StatusOK,
			responseCode: http.StatusOK,
			serverAssert: func(t *testing.T, w http.ResponseWriter, r *http.Request, orgID, previewID uuid.UUID) {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				claims, err := auth.ValidatePreviewToken("test-secret", token)
				require.NoError(t, err, "remote assertions requests should carry a valid preview token")
				require.Equal(t, "assert", claims.Action, "remote assertions requests should use the assert action")
				require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[*previewsvc.AssertionResult]{Data: &previewsvc.AssertionResult{
					Passed:  1,
					Failed:  0,
					Total:   1,
					Results: []previewsvc.AssertionCheck{{Passed: true, Message: "ok"}},
				}}), "remote assertions should encode a success response")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			sessionID := uuid.New()
			previewID := uuid.New()
			now := time.Now().UTC()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, tt.workerMethod, r.Method, "worker-routed preview handlers should use the expected internal worker method")
				require.Equal(t, http.StatusOK, tt.responseCode, "table-driven worker responses should return success")
				require.Equal(t, strings.Replace(tt.path, "%s", previewID.String(), 1), r.URL.Path, "worker-routed preview handlers should target the expected worker endpoint")
				tt.serverAssert(t, w, r, orgID, previewID)
			}))
			defer server.Close()

			h := newPreviewHandlerWithMock(mock)
			nodeStore := db.NewNodeStore(mock)
			h.SetWorkerRuntime(previewsvc.NewWorkerSelector(nodeStore, h.store), previewsvc.NewWorkerPreviewClient("test-secret"), "api-node")

			metadata, err := json.Marshal(previewsvc.WorkerNodeMetadata{
				PreviewCapable:         true,
				PreviewInternalBaseURL: server.URL,
			})
			require.NoError(t, err, "worker metadata should marshal")

			mock.ExpectQuery("SELECT .+ FROM preview_instances").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(previewInstanceTestCols).
						AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
				)
			mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
				WithArgs(pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(handlerNodeTestCols).
						AddRow("test-worker", "worker", "worker.internal", "active", metadata, now, now),
				)

			req := httptest.NewRequest(tt.method, "/preview", strings.NewReader(tt.body))
			req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
			rr := httptest.NewRecorder()

			tt.handler(h, rr, req)
			require.Equal(t, tt.wantStatus, rr.Code, "worker-routed preview handlers should return success")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPreviewHandler_RemoteInspectorEndpoints_ResolutionFailures(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name       string
		method     string
		body       string
		handler    func(*PreviewHandler, http.ResponseWriter, *http.Request)
		wantCode   string
		wantStatus int
	}

	tests := []testCase{
		{"screenshot", http.MethodPost, `{}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureScreenshot(w, r) }, "PREVIEW_WORKER_RESOLUTION_FAILED", http.StatusBadGateway},
		{"inspect", http.MethodPost, `{"x":10,"y":20}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.InspectElement(w, r) }, "PREVIEW_WORKER_RESOLUTION_FAILED", http.StatusBadGateway},
		{"console", http.MethodGet, ``, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ReadConsole(w, r) }, "PREVIEW_WORKER_RESOLUTION_FAILED", http.StatusBadGateway},
		{"interact", http.MethodPost, `{"steps":[{"action":"click"}]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ExecuteInteraction(w, r) }, "PREVIEW_WORKER_RESOLUTION_FAILED", http.StatusBadGateway},
		{"multi viewport", http.MethodPost, `{}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureMultiViewport(w, r) }, "PREVIEW_WORKER_RESOLUTION_FAILED", http.StatusBadGateway},
		{"visual diff", http.MethodPost, `{"before_snapshot_id":"before","after_snapshot_id":"after"}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ComputeVisualDiff(w, r) }, "PREVIEW_WORKER_RESOLUTION_FAILED", http.StatusBadGateway},
		{"assert", http.MethodPost, `{"assertions":[{"type":"text"}]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.RunAssertions(w, r) }, "PREVIEW_WORKER_RESOLUTION_FAILED", http.StatusBadGateway},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			sessionID := uuid.New()
			previewID := uuid.New()
			now := time.Now().UTC()

			h := newPreviewHandlerWithMock(mock)
			nodeStore := db.NewNodeStore(mock)
			h.SetWorkerRuntime(previewsvc.NewWorkerSelector(nodeStore, h.store), previewsvc.NewWorkerPreviewClient("test-secret"), "api-node")

			mock.ExpectQuery("SELECT .+ FROM preview_instances").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(previewInstanceTestCols).
						AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
				)
			mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
				WithArgs(pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(handlerNodeTestCols))

			req := httptest.NewRequest(tt.method, "/preview", strings.NewReader(tt.body))
			req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
			rr := httptest.NewRecorder()

			tt.handler(h, rr, req)
			require.Equal(t, tt.wantStatus, rr.Code, "worker-routed preview handlers should fail closed when the worker cannot be resolved")

			var resp models.ErrorResponse
			require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "worker-routed preview handlers should return a JSON error for resolution failures")
			require.Equal(t, tt.wantCode, resp.Error.Code, "worker-routed preview handlers should return the expected worker resolution error code")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPreviewHandler_RemoteInspectorEndpoints_LocalWorkerRequiresInspector(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		method  string
		body    string
		handler func(*PreviewHandler, http.ResponseWriter, *http.Request)
	}

	tests := []testCase{
		{"screenshot", http.MethodPost, `{}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureScreenshot(w, r) }},
		{"inspect", http.MethodPost, `{"x":10,"y":20}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.InspectElement(w, r) }},
		{"console", http.MethodGet, ``, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ReadConsole(w, r) }},
		{"interact", http.MethodPost, `{"steps":[{"action":"click"}]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ExecuteInteraction(w, r) }},
		{"multi viewport", http.MethodPost, `{}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureMultiViewport(w, r) }},
		{"visual diff", http.MethodPost, `{"before_snapshot_id":"before","after_snapshot_id":"after"}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ComputeVisualDiff(w, r) }},
		{"assert", http.MethodPost, `{"assertions":[{"type":"text"}]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.RunAssertions(w, r) }},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			sessionID := uuid.New()
			previewID := uuid.New()
			now := time.Now().UTC()

			h := newPreviewHandlerWithMock(mock)
			nodeStore := db.NewNodeStore(mock)
			h.SetWorkerRuntime(previewsvc.NewWorkerSelector(nodeStore, h.store), previewsvc.NewWorkerPreviewClient("test-secret"), "local-worker")

			metadata, err := json.Marshal(previewsvc.WorkerNodeMetadata{
				PreviewCapable:         true,
				PreviewInternalBaseURL: "http://worker.local",
			})
			require.NoError(t, err, "worker metadata should marshal")

			mock.ExpectQuery("SELECT .+ FROM preview_instances").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(previewInstanceTestCols).
						AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
				)
			mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
				WithArgs(pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(handlerNodeTestCols).
						AddRow("local-worker", "worker", "worker.local", "active", metadata, now, now),
				)

			req := httptest.NewRequest(tt.method, "/preview", strings.NewReader(tt.body))
			req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
			rr := httptest.NewRecorder()

			tt.handler(h, rr, req)
			require.Equal(t, http.StatusNotImplemented, rr.Code, "worker-routed preview handlers should still require a local inspector when the selected worker is the current node")

			var resp models.ErrorResponse
			require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "worker-routed preview handlers should return a JSON error when the local inspector is unavailable")
			require.Equal(t, "PREVIEW_INSPECTOR_NOT_AVAILABLE", resp.Error.Code, "worker-routed preview handlers should return the inspector-unavailable code")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPreviewHandler_InspectorEndpointValidationErrors(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name       string
		method     string
		body       string
		handler    func(*PreviewHandler, http.ResponseWriter, *http.Request)
		wantStatus int
		wantCode   string
	}

	tests := []testCase{
		{"capture screenshot invalid body", http.MethodPost, "{", func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureScreenshot(w, r) }, http.StatusBadRequest, "INVALID_BODY"},
		{"inspect invalid coordinates", http.MethodPost, `{"x":10001,"y":0}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.InspectElement(w, r) }, http.StatusBadRequest, "INVALID_COORDINATES"},
		{"interaction missing steps", http.MethodPost, `{"steps":[]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ExecuteInteraction(w, r) }, http.StatusBadRequest, "MISSING_STEPS"},
		{"multi viewport too many", http.MethodPost, `{"viewports":[{"name":"1","width":1,"height":1},{"name":"2","width":1,"height":1},{"name":"3","width":1,"height":1},{"name":"4","width":1,"height":1},{"name":"5","width":1,"height":1},{"name":"6","width":1,"height":1}]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureMultiViewport(w, r) }, http.StatusBadRequest, "TOO_MANY_VIEWPORTS"},
		{"visual diff missing ids", http.MethodPost, `{"before_snapshot_id":"","after_snapshot_id":"after"}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ComputeVisualDiff(w, r) }, http.StatusBadRequest, "MISSING_SNAPSHOT_IDS"},
		{"assertions missing", http.MethodPost, `{"assertions":[]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.RunAssertions(w, r) }, http.StatusBadRequest, "MISSING_ASSERTIONS"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			sessionID := uuid.New()
			previewID := uuid.New()
			now := time.Now().UTC()

			h := newPreviewHandlerWithMock(mock)
			h.manager.SetInspector(internalPreviewTestInspector{})

			mock.ExpectQuery("SELECT .+ FROM preview_instances").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(previewInstanceTestCols).
						AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
				)

			req := httptest.NewRequest(tt.method, "/preview", strings.NewReader(tt.body))
			req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
			rr := httptest.NewRecorder()

			tt.handler(h, rr, req)
			require.Equal(t, tt.wantStatus, rr.Code, "preview validation failures should return the expected status")

			var resp models.ErrorResponse
			require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "preview validation failures should return a JSON error")
			require.Equal(t, tt.wantCode, resp.Error.Code, "preview validation failures should return the expected error code")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPreviewHandler_LocalInspectorEndpoints_Success(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name       string
		method     string
		body       string
		handler    func(*PreviewHandler, http.ResponseWriter, *http.Request)
		wantStatus int
	}

	tests := []testCase{
		{"screenshot", http.MethodPost, `{}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureScreenshot(w, r) }, http.StatusOK},
		{"inspect", http.MethodPost, `{"x":10,"y":20}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.InspectElement(w, r) }, http.StatusOK},
		{"console", http.MethodGet, ``, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ReadConsole(w, r) }, http.StatusOK},
		{"interact", http.MethodPost, `{"steps":[{"action":"click"}]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ExecuteInteraction(w, r) }, http.StatusOK},
		{"multi viewport", http.MethodPost, `{}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureMultiViewport(w, r) }, http.StatusOK},
		{"visual diff", http.MethodPost, `{"before_snapshot_id":"before","after_snapshot_id":"after"}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ComputeVisualDiff(w, r) }, http.StatusOK},
		{"assert", http.MethodPost, `{"assertions":[{"type":"text"}]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.RunAssertions(w, r) }, http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			sessionID := uuid.New()
			previewID := uuid.New()
			now := time.Now().UTC()

			h := newPreviewHandlerWithMock(mock)
			h.manager.SetInspector(internalPreviewTestInspector{})

			mock.ExpectQuery("SELECT .+ FROM preview_instances").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(previewInstanceTestCols).
						AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
				)

			req := httptest.NewRequest(tt.method, "/preview", strings.NewReader(tt.body))
			req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
			rr := httptest.NewRecorder()

			tt.handler(h, rr, req)
			require.Equal(t, tt.wantStatus, rr.Code, "non-worker-routed preview handlers should use the local inspector successfully")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestPreviewHandler_RemoteInspectorEndpoints_PropagateStructuredWorkerErrors(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name    string
		method  string
		body    string
		handler func(*PreviewHandler, http.ResponseWriter, *http.Request)
	}

	tests := []testCase{
		{"screenshot", http.MethodPost, `{}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureScreenshot(w, r) }},
		{"inspect", http.MethodPost, `{"x":10,"y":20}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.InspectElement(w, r) }},
		{"console", http.MethodGet, ``, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ReadConsole(w, r) }},
		{"interact", http.MethodPost, `{"steps":[{"action":"click"}]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ExecuteInteraction(w, r) }},
		{"multi viewport", http.MethodPost, `{}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.CaptureMultiViewport(w, r) }},
		{"visual diff", http.MethodPost, `{"before_snapshot_id":"before","after_snapshot_id":"after"}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.ComputeVisualDiff(w, r) }},
		{"assert", http.MethodPost, `{"assertions":[{"type":"text"}]}`, func(h *PreviewHandler, w http.ResponseWriter, r *http.Request) { h.RunAssertions(w, r) }},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			sessionID := uuid.New()
			previewID := uuid.New()
			now := time.Now().UTC()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusConflict)
				require.NoError(t, json.NewEncoder(w).Encode(models.ErrorResponse{
					Error: models.ErrorDetail{Code: "NO_SANDBOX", Message: "preview worker rejected the request"},
				}), "worker test server should encode a structured error response")
			}))
			defer server.Close()

			h := newPreviewHandlerWithMock(mock)
			nodeStore := db.NewNodeStore(mock)
			h.SetWorkerRuntime(previewsvc.NewWorkerSelector(nodeStore, h.store), previewsvc.NewWorkerPreviewClient("test-secret"), "api-node")

			metadata, err := json.Marshal(previewsvc.WorkerNodeMetadata{
				PreviewCapable:         true,
				PreviewInternalBaseURL: server.URL,
			})
			require.NoError(t, err, "worker metadata should marshal")

			mock.ExpectQuery("SELECT .+ FROM preview_instances").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(previewInstanceTestCols).
						AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
				)
			mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
				WithArgs(pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(handlerNodeTestCols).
						AddRow("remote-worker", "worker", "worker.internal", "active", metadata, now, now),
				)

			req := httptest.NewRequest(tt.method, "/preview", strings.NewReader(tt.body))
			req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
			rr := httptest.NewRecorder()

			tt.handler(h, rr, req)
			require.Equal(t, http.StatusConflict, rr.Code, "worker-routed preview handlers should propagate structured worker errors")
			require.Contains(t, rr.Body.String(), "NO_SANDBOX", "worker-routed preview handlers should preserve structured worker error codes")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// screenshotPNGInspector returns a fixed PNG payload (and no artifact, mimicking
// an unconfigured upload store) so the inline-base64 default/override behaviour
// of CaptureScreenshot can be exercised.
type screenshotPNGInspector struct {
	internalPreviewTestInspector
}

func (screenshotPNGInspector) CaptureScreenshot(context.Context, string, models.ScreenshotOpts) (*models.ScreenshotResult, error) {
	return &models.ScreenshotResult{PageTitle: "Preview Title", URL: "http://preview.local", PNG: []byte("fake-png-bytes")}, nil
}

func TestPreviewHandler_CaptureScreenshot_InlineBase64Default(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           string
		wantPNGPresent bool
	}{
		{"defaults to inline when no artifact", `{}`, true},
		{"explicit inline true", `{"inline_base64":true}`, true},
		{"explicit inline false omits png", `{"inline_base64":false}`, false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			sessionID := uuid.New()
			previewID := uuid.New()
			now := time.Now().UTC()

			h := newPreviewHandlerWithMock(mock)
			h.manager.SetInspector(screenshotPNGInspector{})

			mock.ExpectQuery("SELECT .+ FROM preview_instances").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(previewInstanceTestCols).
						AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
				)

			req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(tt.body))
			req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
			rr := httptest.NewRecorder()

			h.CaptureScreenshot(rr, req)
			require.Equal(t, http.StatusOK, rr.Code, "screenshot capture should succeed")

			var resp models.SingleResponse[captureScreenshotResponse]
			require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp), "response should decode")
			if tt.wantPNGPresent {
				require.NotEmpty(t, resp.Data.PNGBase64, "png_base64 should be inlined")
			} else {
				require.Empty(t, resp.Data.PNGBase64, "png_base64 should be omitted when inline_base64 is false")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

type interactScreenshotInspector struct {
	internalPreviewTestInspector
}

func (interactScreenshotInspector) ExecuteInteraction(context.Context, string, []models.InteractionStep) (*models.InteractionResult, error) {
	return &models.InteractionResult{
		FinalURL: "http://preview.local/final",
		Steps: []models.StepResult{{
			StepIndex:  0,
			Action:     "click",
			Success:    true,
			Screenshot: &models.ScreenshotResult{PageTitle: "Step", URL: "http://preview.local", PNG: []byte("fake-png-bytes")},
		}},
	}, nil
}

// ScreenshotResult.PNG serializes as png_base64 (for worker transport), so
// handlers that don't persist-and-strip would leak full base64 images into the
// agent transcript. The interact handler must strip step screenshots' bytes
// after attaching artifacts.
func TestPreviewHandler_Interact_StripsInlineScreenshotBytes(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	now := time.Now().UTC()

	h := newPreviewHandlerWithMock(mock)
	h.manager.SetInspector(interactScreenshotInspector{})

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(`{"steps":[{"action":"click"}]}`))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	rr := httptest.NewRecorder()

	h.ExecuteInteraction(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "interact should succeed")

	body := rr.Body.String()
	require.NotContains(t, body, "png_base64", "interact response must not inline base64 screenshot bytes")
	require.Contains(t, body, "\"page_title\":\"Step\"", "step screenshot metadata should still be present")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

type multiViewportScreenshotInspector struct {
	internalPreviewTestInspector
}

func (multiViewportScreenshotInspector) CaptureMultiViewport(context.Context, string, models.MultiViewportOpts) (*models.MultiViewportResult, error) {
	return &models.MultiViewportResult{
		Captures: []models.ViewportCapture{{
			Viewport:   models.ViewportSpec{Name: "desktop", Width: 1440, Height: 900},
			Screenshot: models.ScreenshotResult{PageTitle: "Desktop", URL: "http://preview.local", PNG: []byte("fake-png-bytes")},
		}},
	}, nil
}

// The multi-viewport handler embeds a ScreenshotResult per capture; like the
// interact handler it must strip the inline PNG bytes after attaching artifacts
// so large base64 images do not leak into the agent transcript.
func TestPreviewHandler_MultiViewport_StripsInlineScreenshotBytes(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	now := time.Now().UTC()

	h := newPreviewHandlerWithMock(mock)
	h.manager.SetInspector(multiViewportScreenshotInspector{})

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newActivePreviewRow(previewID, sessionID, orgID, userID, now)...),
		)

	req := httptest.NewRequest(http.MethodPost, "/preview", strings.NewReader(`{}`))
	req = previewTestContextWithIDs(req, orgID, userID, sessionID.String())
	rr := httptest.NewRecorder()

	h.CaptureMultiViewport(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, "multi-viewport should succeed")

	body := rr.Body.String()
	require.NotContains(t, body, "png_base64", "multi-viewport response must not inline base64 screenshot bytes")
	require.Contains(t, body, "\"page_title\":\"Desktop\"", "capture screenshot metadata should still be present")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
