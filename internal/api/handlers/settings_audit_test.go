package handlers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSettingsAuditDiff_NoChange(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"pm_model":"claude-sonnet-4-5","max_concurrent_runs":10}`)
	changes := settingsAuditDiff(raw, raw)
	require.Empty(t, changes, "identical blobs must not produce changes")
}

func TestSettingsAuditDiff_ScalarChange(t *testing.T) {
	t.Parallel()

	old := json.RawMessage(`{"pm_model":"claude-sonnet-4-5"}`)
	new_ := json.RawMessage(`{"pm_model":"claude-opus-4-7"}`)
	changes := settingsAuditDiff(old, new_)
	require.Len(t, changes, 1)
	require.Equal(t, map[string]any{"before": "claude-sonnet-4-5", "after": "claude-opus-4-7"}, changes["pm_model"])
}

func TestSettingsAuditDiff_NestedChangeFlattened(t *testing.T) {
	t.Parallel()

	old := json.RawMessage(`{"context_limits":{"max_open_issues":100,"pm_max_tokens":50000}}`)
	new_ := json.RawMessage(`{"context_limits":{"max_open_issues":300,"pm_max_tokens":50000}}`)
	changes := settingsAuditDiff(old, new_)
	require.Len(t, changes, 1, "only the leaf that differs should be reported")
	require.Equal(t,
		map[string]any{"before": float64(100), "after": float64(300)},
		changes["context_limits.max_open_issues"],
		"nested changes must report as dotted paths so the UI renders one row per leaf")
}

func TestSettingsAuditDiff_KeyAdded(t *testing.T) {
	t.Parallel()

	old := json.RawMessage(`{"pm_model":"claude-sonnet-4-5"}`)
	new_ := json.RawMessage(`{"pm_model":"claude-sonnet-4-5","llm_model":"gpt-5.4-mini"}`)
	changes := settingsAuditDiff(old, new_)
	require.Len(t, changes, 1)
	entry, ok := changes["llm_model"].(map[string]any)
	require.True(t, ok)
	require.Nil(t, entry["before"], "absent-on-old must surface as nil so the UI renders an added-field row")
	require.Equal(t, "gpt-5.4-mini", entry["after"])
}

func TestSettingsAuditDiff_KeyRemoved(t *testing.T) {
	t.Parallel()

	old := json.RawMessage(`{"pm_model":"claude-sonnet-4-5","llm_model":"gpt-5.4-mini"}`)
	new_ := json.RawMessage(`{"pm_model":"claude-sonnet-4-5"}`)
	changes := settingsAuditDiff(old, new_)
	require.Len(t, changes, 1)
	entry, ok := changes["llm_model"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "gpt-5.4-mini", entry["before"])
	require.Nil(t, entry["after"])
}

func TestSettingsAuditDiff_EmptyOldBlob(t *testing.T) {
	t.Parallel()

	// First-ever settings write: every incoming key is a change.
	old := json.RawMessage(nil)
	new_ := json.RawMessage(`{"pm_model":"claude-sonnet-4-5"}`)
	changes := settingsAuditDiff(old, new_)
	require.Len(t, changes, 1)
	entry := changes["pm_model"].(map[string]any)
	require.Nil(t, entry["before"])
	require.Equal(t, "claude-sonnet-4-5", entry["after"])
}

func TestSettingsAuditDiff_DeeplyNestedAgentConfig(t *testing.T) {
	t.Parallel()

	old := json.RawMessage(`{"agent_config":{"codex":{"OPENAI_MODEL":"gpt-5.4"}}}`)
	new_ := json.RawMessage(`{"agent_config":{"codex":{"OPENAI_MODEL":"gpt-5.3-codex"}}}`)
	changes := settingsAuditDiff(old, new_)
	require.Len(t, changes, 1)
	require.Equal(t,
		map[string]any{"before": "gpt-5.4", "after": "gpt-5.3-codex"},
		changes["agent_config.codex.OPENAI_MODEL"],
		"multi-level nesting must flatten to a single dotted path")
}

func TestSettingsAuditDiff_ObjectToScalarTreatedAsReplacement(t *testing.T) {
	t.Parallel()

	// If one side is a scalar and the other is an object at the same key, the
	// diff must not recurse — the whole value is the change.
	old := json.RawMessage(`{"product_context":null}`)
	new_ := json.RawMessage(`{"product_context":{"direction":"ship faster"}}`)
	changes := settingsAuditDiff(old, new_)
	require.Len(t, changes, 1)
	require.Contains(t, changes, "product_context")
}
