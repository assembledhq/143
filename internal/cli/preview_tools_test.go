package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/assembledhq/143/internal/services/mcp"
	"github.com/stretchr/testify/require"
)

func TestPreviewToolExecutor_SessionScreenshotOmitsInlineBase64(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, http.MethodPost, r.Method, "screenshot should use POST")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody), "request body should be JSON")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"data":{"page_title":"Home","url":"https://preview.test/","png_base64":"abc","captured_at":"2026-06-27T00:00:00Z"}}`))
		require.NoError(t, err, "test response should write")
	}))
	defer server.Close()

	executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "token"})}
	result := executor.screenshot(context.Background(), mustJSON(map[string]any{
		"session_id":    "session-1",
		"path":          "/dashboard",
		"viewport_w":    1024,
		"viewport_h":    768,
		"inline_base64": false,
	}))

	require.False(t, result.IsError, "screenshot should succeed")
	require.Equal(t, "/api/v1/sessions/session-1/preview/screenshot", gotPath, "screenshot should target session preview endpoint")
	require.Equal(t, "/dashboard", gotBody["path"], "screenshot should forward requested path")
	require.NotContains(t, firstText(result), "png_base64", "inline_base64=false should remove large screenshot payloads from CLI output")
}

func TestPreviewToolExecutor_InteractParsesJSONSteps(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/sessions/session-1/preview/interact", r.URL.Path, "interact should target session preview endpoint")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody), "request body should be JSON")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"data":{"steps":[{"action":"click","success":true}]}}`))
		require.NoError(t, err, "test response should write")
	}))
	defer server.Close()

	executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "token"})}
	result := executor.interact(context.Background(), mustJSON(map[string]string{
		"session_id": "session-1",
		"steps":      `[{"action":"click","selector":"[data-testid=save]"}]`,
	}))

	require.False(t, result.IsError, "interact should succeed")
	steps, ok := gotBody["steps"].([]any)
	require.True(t, ok, "steps should be forwarded as a JSON array")
	require.Len(t, steps, 1, "one interaction step should be forwarded")
	require.Contains(t, firstText(result), `"success": true`, "interact should print response JSON")
}

func TestPreviewToolExecutor_ScreenshotTargetsPreviewID(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, http.MethodPost, r.Method, "screenshot should use POST")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"data":{"artifact":{"url":"/api/v1/uploads/files/org/preview.png"},"captured_at":"2026-06-27T00:00:00Z"}}`))
		require.NoError(t, err, "test response should write")
	}))
	defer server.Close()

	executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "token"})}
	result := executor.screenshot(context.Background(), mustJSON(map[string]any{
		"preview_id": "preview-1",
		"path":       "/",
	}))

	require.False(t, result.IsError, "screenshot should succeed")
	require.Equal(t, "/api/v1/previews/preview-1/screenshot", gotPath, "screenshot should target preview-id endpoint")
	require.Contains(t, firstText(result), "preview.png", "screenshot should print artifact metadata")
}

func TestPreviewToolExecutor_ListSessionPreview(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, http.MethodGet, r.Method, "list by session should read status")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"data":{"instance":{"id":"preview-1","session_id":"session-1","status":"ready"},"preview_origin":"https://preview.test"}}`))
		require.NoError(t, err, "test response should write")
	}))
	defer server.Close()

	executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "token"})}
	result := executor.list(context.Background(), mustJSON(map[string]string{"session_id": "session-1"}))

	require.False(t, result.IsError, "list by session should succeed")
	require.Equal(t, "/api/v1/sessions/session-1/preview", gotPath, "list should target session status endpoint")
	require.Contains(t, firstText(result), `"preview_id": "preview-1"`, "list should wrap the session preview status in a list")
}

func TestPreviewToolExecutor_BranchCreateWaitPollsUntilReady(t *testing.T) {
	t.Parallel()

	var statusCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /api/v1/repositories":
			_, err := w.Write([]byte(`{"data":[{"id":"repo-1","full_name":"acme/app"}]}`))
			require.NoError(t, err, "repositories response should write")
		case http.MethodGet + " /api/v1/repositories/repo-1/branches":
			_, err := w.Write([]byte(`{"data":[{"name":"feature"}]}`))
			require.NoError(t, err, "branches response should write")
		case http.MethodPost + " /api/v1/previews":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body), "create request body should decode")
			require.Equal(t, "repo-1", body["repository_id"], "create should use resolved repository id")
			require.Equal(t, "feature", body["branch"], "create should use requested branch")
			_, err := w.Write([]byte(`{"data":{"target_id":"target-1","preview_id":"preview-1","repository_full_name":"acme/app","branch":"feature","status":"starting"}}`))
			require.NoError(t, err, "create response should write")
		case http.MethodGet + " /api/v1/previews/preview-1":
			atomic.AddInt32(&statusCalls, 1)
			_, err := w.Write([]byte(`{"data":{"target_id":"target-1","preview_id":"preview-1","repository_full_name":"acme/app","branch":"feature","status":"running","preview_url":"https://preview.test"}}`))
			require.NoError(t, err, "status response should write")
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "token"})}
	result := executor.create(context.Background(), mustJSON(map[string]any{
		"repository": "acme/app",
		"branch":     "feature",
		"wait":       true,
	}))

	require.False(t, result.IsError, "branch create with wait should succeed")
	require.EqualValues(t, 1, atomic.LoadInt32(&statusCalls), "branch create wait should poll status until live")
	require.Contains(t, firstText(result), `"status": "running"`, "wait result should return the ready preview status")
}

func TestPreviewToolExecutor_UpdateWaitReturnsUpdateResponse(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/sessions/session-1/preview/update", r.URL.Path, "update should target session preview endpoint")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody), "update request body should decode")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"data":{"preview_id":"preview-1","session_id":"session-1","mode":"soft_service_restart","action":"restarting","status":"ready","message":"preview service restart started"}}`))
		require.NoError(t, err, "update response should write")
	}))
	defer server.Close()

	executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "token"})}
	result := executor.update(context.Background(), mustJSON(map[string]any{
		"session_id": "session-1",
		"wait":       true,
	}))

	require.False(t, result.IsError, "update with wait should succeed")
	require.Equal(t, true, gotBody["wait"], "update should let the server perform the bounded wait")
	require.Contains(t, firstText(result), `"mode": "soft_service_restart"`, "update wait should preserve selected mode")
	require.Contains(t, firstText(result), `"action": "restarting"`, "update wait should preserve selected action")
}

func TestPreviewToolExecutor_InspectAllowsTopLeftCoordinate(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/sessions/session-1/preview/inspect", r.URL.Path, "inspect should target session preview endpoint")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody), "request body should be JSON")
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"data":{"tag":"div"}}`))
		require.NoError(t, err, "test response should write")
	}))
	defer server.Close()

	executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "token"})}
	result := executor.inspect(context.Background(), mustJSON(map[string]any{
		"session_id": "session-1",
		"x":          0,
		"y":          0,
	}))

	require.False(t, result.IsError, "inspecting the top-left pixel (0,0) should be allowed, not treated as missing coordinates")
	require.EqualValues(t, 0, gotBody["x"], "x=0 should be forwarded")
	require.EqualValues(t, 0, gotBody["y"], "y=0 should be forwarded")
}

func TestPreviewToolExecutor_InspectRequiresSelectorOrCoordinates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no HTTP request should be made when neither selector nor coordinates are given, got %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "token"})}
	result := executor.inspect(context.Background(), mustJSON(map[string]string{"session_id": "session-1"}))

	require.True(t, result.IsError, "omitting both selector and coordinates should be rejected")
	require.Contains(t, firstText(result), "selector or x/y coordinates are required", "error should explain the requirement")
}

func TestPreviewToolExecutor_RejectsAmbiguousTarget(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no HTTP request should be made when the target is ambiguous, got %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "token"})}
	both := mustJSON(map[string]string{"session_id": "session-1", "preview_id": "preview-1"})

	cases := []struct {
		name string
		run  func() *mcp.ToolCallResult
	}{
		{"screenshot", func() *mcp.ToolCallResult { return executor.screenshot(context.Background(), both) }},
		{"status", func() *mcp.ToolCallResult { return executor.status(context.Background(), both) }},
		{"stop", func() *mcp.ToolCallResult { return executor.stop(context.Background(), both) }},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := tc.run()
			require.True(t, result.IsError, "supplying both session_id and preview_id should be rejected")
			require.Contains(t, firstText(result), "not both", "error should explain the ambiguity")
		})
	}
}

func TestPreviewToolExecutor_CreateRejectsAmbiguousTarget(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no HTTP request should be made when create target is ambiguous, got %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "token"})}
	result := executor.create(context.Background(), mustJSON(map[string]string{
		"session_id": "session-1",
		"repository": "acme/app",
		"branch":     "feature",
	}))

	require.True(t, result.IsError, "supplying session_id and branch target should be rejected")
	require.Contains(t, firstText(result), "not both", "error should explain the ambiguity")
}
