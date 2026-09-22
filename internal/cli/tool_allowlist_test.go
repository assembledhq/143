package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/mcp"
)

type allowlistTestSource struct{ tools []mcp.Tool }

func (s allowlistTestSource) ListTools() []mcp.Tool { return s.tools }
func (s allowlistTestSource) CallTool(context.Context, string, json.RawMessage) *mcp.ToolCallResult {
	return mcp.TextResult("ok")
}

func toolNames(source mcp.ToolSource) []string {
	var names []string
	for _, tool := range source.ListTools() {
		names = append(names, tool.Name)
	}
	return names
}

// TestInternalToolSourceAllowlistFailsClosed proves a per-target turn's
// in-sandbox filter never fails open: with the allowlist in the
// environment, a failed capability fetch exposes only tools that need no
// grant, and a successful fetch exposes exactly the allowlisted tools the
// snapshot grants.
func TestInternalToolSourceAllowlistFailsClosed(t *testing.T) {
	t.Parallel()
	base := allowlistTestSource{tools: []mcp.Tool{{Name: "linear_get_task"}, {Name: "linear_update_task"}, {Name: "slack_send"}, {Name: "log_query"}}}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	var stderr bytes.Buffer
	source, _ := newInternalToolSource(context.Background(), base, "token", failing.URL, &stderr, models.PerTargetToolAllowlist)
	names := toolNames(source)
	require.NotContains(t, names, "linear_update_task", "a write stays hidden when the fetch fails")
	require.NotContains(t, names, "slack_send", "a send stays hidden when the fetch fails")
	require.NotContains(t, names, "linear_get_task", "a read that needs a grant stays hidden when the grants are unknown")
	require.Contains(t, names, "capability_list", "self-inspection stays available")
	require.Contains(t, stderr.String(), "the tool allowlist applies with no grants", "the failure is reported as fail-closed")

	granting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"snapshot": []map[string]any{
				{"id": "issue_sources", "access_level": "read"},
				{"id": "external_comments", "access_level": "write"},
				{"id": "slack_notifications", "access_level": "write"},
			},
		}})
	}))
	defer granting.Close()
	source, _ = newInternalToolSource(context.Background(), base, "token", granting.URL, &bytes.Buffer{}, models.PerTargetToolAllowlist)
	names = toolNames(source)
	require.Contains(t, names, "linear_get_task", "an allowlisted, granted read is available")
	require.NotContains(t, names, "linear_update_task", "a granted write is still denied by the allowlist")
	require.NotContains(t, names, "slack_send", "a granted send is still denied by the allowlist")
	require.NotContains(t, names, "log_query", "an allowlisted read without its grant is denied")

	// Without any token the environment allowlist alone still filters.
	bare := mcp.NewCapabilityFilteredToolSource(base, mcp.ToolCapabilityPolicy{ToolAllowlist: models.PerTargetToolAllowlist})
	require.Empty(t, toolNames(bare), "with no token and no grants nothing that needs a grant is reachable")
}

func TestReviewToolsIntersectWarmEnvironmentAndToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		scope bool
		grant bool
		want  bool
	}{{"granted", true, true, true}, {"stale environment", false, true, false}, {"revoked grant", true, false, false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := append(append([]string{}, models.PerTargetToolAllowlist...), "automation:execute-action", "automation:action-status")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				snapshot := []models.AgentCapabilitySnapshotItem{}
				if tt.grant {
					snapshot = append(snapshot, models.AgentCapabilitySnapshotItem{ID: models.AgentCapabilityAutomationActions, AccessLevel: models.AgentCapabilityAccessWrite})
				}
				allowed := models.PerTargetToolAllowlist
				if tt.scope {
					allowed = env
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"snapshot": snapshot, "tool_allowlist": allowed}}), "serve authenticated snapshot")
			}))
			defer server.Close()
			source, _ := newInternalToolSource(context.Background(), allowlistTestSource{}, "token", server.URL, &bytes.Buffer{}, env)
			require.Equal(t, tt.want, slices.Contains(toolNames(source), "automation_execute_action"), "both grant and current token scopes must allow the write")
			require.NotContains(t, toolNames(source), "slack_send", "narrow exception must not expose general sends")
		})
	}
}
