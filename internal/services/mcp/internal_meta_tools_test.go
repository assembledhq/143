package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewInternalMetaToolSourceNormalizesInternalAPIBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		apiURL   string
		expected string
	}{
		{name: "canonical origin", apiURL: "https://143.dev", expected: "https://143.dev"},
		{name: "legacy internal path", apiURL: "https://143.dev/api/v1/internal", expected: "https://143.dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source, ok := NewInternalMetaToolSource(staticToolSource{}, "token", tt.apiURL).(*internalMetaToolSource)
			require.True(t, ok, "constructor should return the internal meta tool source")
			require.Equal(t, tt.expected, source.apiURL, "internal meta tools should append routes to an origin-only base URL")
		})
	}
}

func TestInternalMetaToolSourceListsCodeReviewHistoryTools(t *testing.T) {
	t.Parallel()

	source := NewInternalMetaToolSource(staticToolSource{}, "token", "https://143.dev")
	names := make(map[string]bool)
	for _, tool := range source.ListTools() {
		names[tool.Name] = true
	}
	for _, name := range []string{"code_review_history_list", "code_review_history_get", "code_review_history_policy"} {
		require.True(t, names[name], "internal meta tools should expose %s alongside the session history tools", name)
	}
}

func TestInternalMetaToolSourceCodeReviewHistoryRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		tool          string
		args          string
		expectedPath  string
		expectedQuery map[string]string
	}{
		{
			name:         "list forwards filters as query params",
			tool:         "code_review_history_list",
			args:         `{"decision":"blocked","status":"completed","outcome":"completed_not_approved","acceptable":false,"search":"auth","created_after":"2026-01-02T15:04:05Z","cursor":"row-1","limit":25}`,
			expectedPath: "/api/v1/internal/code-reviews",
			expectedQuery: map[string]string{
				"decision":      "blocked",
				"status":        "completed",
				"outcome":       "completed_not_approved",
				"acceptable":    "false",
				"search":        "auth",
				"created_after": "2026-01-02T15:04:05Z",
				"cursor":        "row-1",
				"limit":         "25",
			},
		},
		{
			name:          "get targets one review with opt-in raw output",
			tool:          "code_review_history_get",
			args:          `{"session_id":"9b8c1c33-8ddc-4d75-8f68-f6a72f2b6c1d","include_raw_output":true}`,
			expectedPath:  "/api/v1/internal/code-reviews/9b8c1c33-8ddc-4d75-8f68-f6a72f2b6c1d",
			expectedQuery: map[string]string{"include_raw_output": "true"},
		},
		{
			name:          "policy without id resolves the active policy",
			tool:          "code_review_history_policy",
			args:          `{}`,
			expectedPath:  "/api/v1/internal/code-reviews/policy",
			expectedQuery: map[string]string{},
		},
		{
			name:          "policy with id fetches a historical version",
			tool:          "code_review_history_policy",
			args:          `{"policy_id":"5c2f9a51-40cb-45f7-8f0d-6bc47d4e2a11"}`,
			expectedPath:  "/api/v1/internal/code-reviews/policies/5c2f9a51-40cb-45f7-8f0d-6bc47d4e2a11",
			expectedQuery: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotPath, gotAuth string
			var gotQuery map[string][]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotQuery = r.URL.Query()
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[]}`))
			}))
			defer server.Close()

			source := NewInternalMetaToolSource(staticToolSource{}, "token", server.URL)
			result := source.CallTool(context.Background(), tt.tool, json.RawMessage(tt.args))

			require.False(t, result.IsError, "tool call should proxy to the internal API without error")
			require.Equal(t, tt.expectedPath, gotPath, "tool should hit the expected internal API route")
			require.Equal(t, "Bearer token", gotAuth, "tool should authenticate with the sandbox internal token")
			require.Len(t, gotQuery, len(tt.expectedQuery), "tool should forward exactly the provided filters")
			for key, expected := range tt.expectedQuery {
				require.Equal(t, []string{expected}, gotQuery[key], "query param %s should be forwarded", key)
			}
		})
	}
}

func TestInternalMetaToolSourceCodeReviewHistoryGetArgumentErrors(t *testing.T) {
	t.Parallel()

	source := NewInternalMetaToolSource(staticToolSource{}, "token", "https://143.dev")

	missing := source.CallTool(context.Background(), "code_review_history_get", json.RawMessage(`{}`))
	require.True(t, missing.IsError, "get without session_id should fail before hitting the API")
	require.Contains(t, missing.Content[0].Text, "session_id is required", "error should name the missing argument")

	malformed := source.CallTool(context.Background(), "code_review_history_get", json.RawMessage(`{not json`))
	require.True(t, malformed.IsError, "get with malformed JSON should fail before hitting the API")
	require.Contains(t, malformed.Content[0].Text, "invalid JSON", "malformed input should be reported as invalid JSON, not a missing field")
}
