package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/services/mcp"
	"github.com/stretchr/testify/require"
)

func TestPreviewToolLifecycleRequests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, tool, args, method, path, body, reply, expected string
	}{
		{name: "read session control", tool: "preview_control", args: `{"session_id":"session"}`, method: "GET", path: "/api/v1/sessions/session/preview/control", reply: `{"owner":"human"}`, expected: `{"owner":"human"}`},
		{name: "read branch control", tool: "preview_control", args: `{"preview_id":"branch"}`, method: "GET", path: "/api/v1/previews/branch/control", reply: `{"owner":"agent"}`, expected: `{"owner":"agent"}`},
		{name: "restart session", tool: "preview_restart", args: `{"session_id":"session"}`, method: "POST", path: "/api/v1/sessions/session/preview/restart", reply: `{"status":"starting"}`, expected: `{"status":"starting"}`},
		{name: "stop session", tool: "preview_stop", args: `{"session_id":"session"}`, method: "DELETE", path: "/api/v1/sessions/session/preview", expected: `{"session_id":"session","status":"stopped"}`},
		{name: "stop branch", tool: "preview_stop", args: `{"preview_id":"branch"}`, method: "POST", path: "/api/v1/previews/branch/stop", expected: `{"preview_id":"branch","status":"stopping"}`},
		{name: "filtered console", tool: "preview_console", args: `{"preview_id":"branch","level":"warn & error"}`, method: "GET", path: "/api/v1/previews/branch/console?level=warn+%26+error", reply: `[{"level":"error","message":"bad render"}]`, expected: `[{"level":"error","message":"bad render"}]`},
		{name: "unfiltered console", tool: "preview_console", args: `{"session_id":"session"}`, method: "GET", path: "/api/v1/sessions/session/preview/console", reply: `[]`, expected: `[]`},
		{name: "responsive snapshots", tool: "preview_multi_viewport", args: `{"session_id":"session","path":"/settings","delay_ms":25,"viewports":"[{\"width\":390,\"height\":844}]"}`, method: "POST", path: "/api/v1/sessions/session/preview/multi-viewport", body: `{"path":"/settings","delay_ms":25,"viewports":[{"width":390,"height":844}]}`, reply: `{"snapshots":["mobile"]}`, expected: `{"snapshots":["mobile"]}`},
		{name: "default responsive snapshots", tool: "preview_multi_viewport", args: `{"preview_id":"branch"}`, method: "POST", path: "/api/v1/previews/branch/multi-viewport", body: `{"path":"","delay_ms":0}`, reply: `{"snapshots":[]}`, expected: `{"snapshots":[]}`},
		{name: "compare exact snapshots", tool: "preview_visual_diff", args: `{"preview_id":"branch","before_snapshot_id":"before","after_snapshot_id":"after"}`, method: "POST", path: "/api/v1/previews/branch/visual-diff", body: `{"before_snapshot_id":"before","after_snapshot_id":"after"}`, reply: `{"changed_pixels":12}`, expected: `{"changed_pixels":12}`},
		{name: "assert browser content", tool: "preview_assert", args: `{"session_id":"session","assertions":"[{\"selector\":\"h1\",\"text\":\"Ready\"}]"}`, method: "POST", path: "/api/v1/sessions/session/preview/assert", body: `{"assertions":[{"selector":"h1","text":"Ready"}]}`, reply: `{"passed":true}`, expected: `{"passed":true}`},
		{name: "update explicit false and config", tool: "preview_update", args: `{"session_id":"session","path":"/dashboard","reload_browser":false,"config":"{\"port\":3000}"}`, method: "POST", path: "/api/v1/sessions/session/preview/update", body: `{"path":"/dashboard","wait":false,"force_mode":"","reload_browser":false,"config":{"port":3000}}`, reply: `{"mode":"soft_service_restart"}`, expected: `{"mode":"soft_service_restart"}`},
		{name: "list branch previews", tool: "preview_list", args: `{}`, method: "GET", path: "/api/v1/previews", reply: `[]`, expected: `[]`},
	}
	for _, tt := range tests {
		for _, fail := range []bool{false, true} {
			name := tt.name
			if fail {
				name += " backend unavailable"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				var requests []struct{ method, path, auth, body string }
				var mu sync.Mutex
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					payload, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					mu.Lock()
					requests = append(requests, struct{ method, path, auth, body string }{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), string(payload)})
					mu.Unlock()
					if fail {
						w.WriteHeader(503)
						return
					}
					if tt.reply == "" {
						w.WriteHeader(204)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if _, err := io.WriteString(w, `{"data":`+tt.reply+`}`); err != nil {
						t.Error(err)
					}
				}))
				defer closeCLIHTTPTestServer(t, server)
				executor := &previewToolExecutor{client: NewClient(Config{ServerURL: server.URL, Token: "user-token"})}
				result := executor.call(context.Background(), tt.tool, json.RawMessage(tt.args))
				require.Equal(t, fail, result.IsError, "backend failure must remain visible to the agent")
				if fail {
					require.Contains(t, firstText(result), "503", "failed operation must preserve HTTP status")
				} else {
					require.JSONEq(t, tt.expected, firstText(result), "tool must return exact server result or lifecycle acknowledgement")
				}
				mu.Lock()
				observed := append([]struct{ method, path, auth, body string }(nil), requests...)
				mu.Unlock()
				require.Len(t, observed, 1, "lifecycle action must issue exactly one request")
				require.Equal(t, tt.method, observed[0].method, "tool must use correct lifecycle method")
				require.Equal(t, tt.path, observed[0].path, "tool must target correct session or branch endpoint")
				require.Equal(t, "Bearer user-token", observed[0].auth, "public tools must use user's bearer credentials")
				if tt.body == "" {
					require.Empty(t, observed[0].body, "read and empty lifecycle requests must not invent payloads")
				} else {
					require.JSONEq(t, tt.body, observed[0].body, "payload must retain explicit false, snapshots and viewport bounds")
				}
			})
		}
	}
}

func TestPreviewToolLifecycleValidation(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, tool, args, expected string }{
		{"control requires target", "preview_control", `{}`, "required"},
		{"restart requires session", "preview_restart", `{}`, "required"},
		{"console rejects invalid JSON", "preview_console", `{`, "invalid"},
		{"stop rejects invalid JSON", "preview_stop", `{`, "invalid"},
		{"list rejects invalid JSON", "preview_list", `{`, "invalid"},
		{"status rejects invalid JSON", "preview_status", `{`, "invalid"},
		{"control rejects invalid JSON", "preview_control", `{`, "invalid"},
		{"multi rejects invalid JSON", "preview_multi_viewport", `{`, "invalid"},
		{"multi requires target", "preview_multi_viewport", `{}`, "required"},
		{"multi rejects viewport object", "preview_multi_viewport", `{"preview_id":"branch","viewports":"{}"}`, "JSON array"},
		{"diff requires snapshots", "preview_visual_diff", `{"preview_id":"branch"}`, "required"},
		{"diff requires target", "preview_visual_diff", `{"before_snapshot_id":"a","after_snapshot_id":"b"}`, "required"},
		{"update requires session", "preview_update", `{}`, "required"},
		{"update rejects config list", "preview_update", `{"session_id":"session","config":"[]"}`, "JSON object"},
		{"assert rejects invalid JSON payload", "preview_assert", `{"session_id":"session","assertions":"["}`, "assertions must be JSON"},
		{"handoff requires reason", "preview_request_handoff", `{"session_id":"session"}`, "reason is required"},
		{"handoff rejects invalid JSON", "preview_request_handoff", `{`, "invalid"},
		{"unknown tool", "preview_unknown", `{}`, "unknown preview tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// A nil client makes an accidental request fail this test immediately.
			result := (&previewToolExecutor{}).call(context.Background(), tt.tool, json.RawMessage(tt.args))
			require.True(t, result.IsError, "invalid arguments must be rejected before issuing requests")
			require.Contains(t, firstText(result), tt.expected, "agent must receive a useful validation error")
		})
	}
}

func TestRemoteToolSourceGatewayLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                     string
		listStatus, invokeStatus int
	}{
		{name: "remote integration tool succeeds"},
		{name: "listing fails closed", listStatus: 401},
		{name: "invocation failure returned", invokeStatus: 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls []string
			var bodies []string
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				mu.Lock()
				calls = append(calls, r.Method+" "+r.URL.Path)
				bodies = append(bodies, string(body))
				mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer public-token" {
					t.Error("gateway must use user credential")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "GET" {
					if tt.listStatus != 0 {
						w.WriteHeader(tt.listStatus)
						return
					}
					if _, err := io.WriteString(w, `{"data":{"tools":[{"name":"issue_read","description":"Read an issue","inputSchema":{"type":"object"}}]}}`); err != nil {
						t.Error(err)
					}
					return
				}
				if tt.invokeStatus != 0 {
					w.WriteHeader(tt.invokeStatus)
					return
				}
				if _, err := io.WriteString(w, `{"data":{"content":[{"type":"text","text":"issue result"}]}}`); err != nil {
					t.Error(err)
				}
			}))
			defer closeCLIHTTPTestServer(t, server)
			source, err := NewRemoteToolSource(context.Background(), Config{ServerURL: server.URL, Token: "public-token"})
			if tt.listStatus != 0 {
				require.ErrorContains(t, err, "401", "tool list failure must not grant an empty or unrestricted source")
				require.Nil(t, source, "failed listing must not return a source")
				return
			}
			require.NoError(t, err, "valid listing should create remote source")
			names := make([]string, 0)
			for _, tool := range source.ListTools() {
				names = append(names, tool.Name)
			}
			require.Equal(t, "issue_read", names[0], "remote tools must preserve server ordering")
			require.Contains(t, names, "preview_control", "local preview tools should accompany integration tools")
			result := source.CallTool(context.Background(), "issue_read", json.RawMessage(`{"id":"issue"}`))
			if tt.invokeStatus != 0 {
				require.True(t, result.IsError, "gateway failure must reach tool caller")
				require.Contains(t, firstText(result), "503", "gateway failure should preserve status")
			} else {
				require.Equal(t, mcp.TextResult("issue result"), result, "integration response must be preserved exactly")
			}
			mu.Lock()
			observed := append([]string(nil), calls...)
			payloads := append([]string(nil), bodies...)
			mu.Unlock()
			require.Equal(t, []string{"GET /api/v1/cli/tools", "POST /api/v1/cli/tools/invoke"}, observed, "integration tools must use gateway instead of exposing org credentials")
			require.JSONEq(t, `{"tool":"issue_read","args":{"id":"issue"}}`, payloads[1], "invocation must preserve exact tool and arguments")
		})
	}
}
