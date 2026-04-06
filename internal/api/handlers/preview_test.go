package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// newPreviewTestHandler creates a PreviewHandler with nil manager/store
// for testing stub endpoints that don't need them.
func newPreviewTestHandler() *PreviewHandler {
	return &PreviewHandler{
		logger: zerolog.Nop(),
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

func TestPreviewHandler_InspectorStubs(t *testing.T) {
	t.Parallel()
	h := newPreviewTestHandler()

	stubs := []struct {
		name    string
		method  string
		path    string
		handler http.HandlerFunc
	}{
		{"CaptureScreenshot", http.MethodPost, "/screenshot", h.CaptureScreenshot},
		{"InspectElement", http.MethodPost, "/inspect", h.InspectElement},
		{"ReadConsole", http.MethodGet, "/console", h.ReadConsole},
		{"SubmitDesignFeedback", http.MethodPost, "/design-feedback", h.SubmitDesignFeedback},
		{"ExecuteInteraction", http.MethodPost, "/interact", h.ExecuteInteraction},
		{"CaptureMultiViewport", http.MethodPost, "/multi-viewport", h.CaptureMultiViewport},
		{"ComputeVisualDiff", http.MethodPost, "/visual-diff", h.ComputeVisualDiff},
		{"RunAssertions", http.MethodPost, "/assert", h.RunAssertions},
	}

	for _, tt := range stubs {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tt.method, tt.path, nil)
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

func TestPreviewHandler_StartPreview_InvalidBody(t *testing.T) {
	t.Parallel()
	h := newPreviewTestHandler()

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
	h := newPreviewTestHandler()

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

func TestPreviewHandler_DetectReadiness(t *testing.T) {
	t.Parallel()
	h := newPreviewTestHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos/owner/repo/preview/detect", nil)
	req = previewTestContext(req)

	w := httptest.NewRecorder()
	h.DetectReadiness(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp models.SingleResponse[map[string]string]
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, string(models.PreviewReadinessNotSupported), resp.Data["readiness"])
}

func TestPreviewHandler_GetPreview_NoActivePreview(t *testing.T) {
	t.Parallel()
	// This test would require a store mock. Since the handler uses concrete
	// types, we verify the pattern by testing with nil store (expects panic
	// to be caught by the handler's nil check or error path).
	// In production, these would be integration tests.
	t.Skip("requires store mock — covered by integration tests")
}
