package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunCLI_Help(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{"--help"}, &stdout, &stderr)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "143-tools") {
		t.Error("help output missing '143-tools' usage line")
	}
	if !strings.Contains(output, "sentry_list_errors") {
		t.Error("help output missing sentry tools")
	}
	if !strings.Contains(output, "linear_list_tasks") {
		t.Error("help output missing linear tools")
	}
}

func TestRunCLI_ToolHelp(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{"sentry_list_errors", "--help"}, &stdout, &stderr)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "--severity") {
		t.Error("tool help missing --severity flag")
	}
	if !strings.Contains(output, "--limit") {
		t.Error("tool help missing --limit flag")
	}
}

func TestRunCLI_ListErrors(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{
		"sentry_list_errors", "--severity", "high", "--limit", "10",
	}, &stdout, &stderr)

	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "test error") {
		t.Errorf("expected 'test error' in output, got: %s", output)
	}
}

func TestRunCLI_GetError(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{
		"sentry_get_error", "--error_id", "abc-123",
	}, &stdout, &stderr)

	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "abc-123") {
		t.Errorf("expected error ID in output, got: %s", output)
	}
}

func TestRunCLI_CreateTask(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{
		"linear_create_task", "--title", "Fix auth bug", "--team_key", "ENG",
	}, &stdout, &stderr)

	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "Fix auth bug") {
		t.Errorf("expected task title in output, got: %s", output)
	}
}

func TestRunCLI_UnknownTool(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{"nonexistent_tool"}, &stdout, &stderr)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "unknown tool") {
		t.Errorf("expected 'unknown tool' in stderr, got: %s", stderr.String())
	}
}

func TestRunCLI_UnknownToolOutputsJSONError(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildTestRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{"nonexistent_tool"}, &stdout, &stderr)
	require.Equal(t, 1, code, "unknown tools should exit non-zero")
	require.Empty(t, stdout.String(), "unknown tool errors should not write non-JSON stdout")

	var payload map[string]map[string]string
	require.NoError(t, json.Unmarshal(stderr.Bytes(), &payload), "unknown tool errors should be JSON")
	require.Equal(t, "UNKNOWN_TOOL", payload["error"]["code"], "unknown tool errors should use a stable code")
	require.Contains(t, payload["error"]["message"], "nonexistent_tool", "unknown tool error should name the tool")
}

func TestRunCLI_MissingRequired(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	var stdout, stderr bytes.Buffer
	// sentry_get_error requires --error_id
	code := RunCLI(context.Background(), tr, []string{"sentry_get_error"}, &stdout, &stderr)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "missing required flag") {
		t.Errorf("expected 'missing required flag' in stderr, got: %s", stderr.String())
	}
}

func TestRunCLI_NoArgs(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{}, &stdout, &stderr)

	if code != 0 {
		t.Errorf("exit code = %d, want 0 (should show help)", code)
	}
	if !strings.Contains(stdout.String(), "Available tools") {
		t.Error("expected help output with 'Available tools'")
	}
}

func TestRunCLI_ArrayFlag(t *testing.T) {
	t.Parallel()
	tr := NewToolRegistry(buildTestRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{
		"linear_list_tasks", "--states", "triage,backlog",
	}, &stdout, &stderr)

	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
}

func TestParseFlagsToJSON(t *testing.T) {
	t.Parallel()
	schema := ToolSchema{
		Type: "object",
		Properties: map[string]SchemaProperty{
			"severity": {Type: "string"},
			"limit":    {Type: "number"},
			"states":   {Type: "array"},
		},
	}

	args := []string{"--severity", "high", "--limit", "25", "--states", "a,b,c"}
	result, err := parseFlagsToJSON(args, schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["severity"] != "high" {
		t.Errorf("severity = %v, want 'high'", result["severity"])
	}
	if result["limit"] != float64(25) {
		t.Errorf("limit = %v, want 25", result["limit"])
	}
	states, ok := result["states"].([]string)
	if !ok || len(states) != 3 {
		t.Errorf("states = %v, want [a b c]", result["states"])
	}
}

func TestParseFlagsToJSON_BooleanFlagWithoutValue(t *testing.T) {
	t.Parallel()
	schema := ToolSchema{
		Type: "object",
		Properties: map[string]SchemaProperty{
			"include_archived": {Type: "boolean"},
			"limit":            {Type: "number"},
		},
	}

	result, err := parseFlagsToJSON([]string{"--include_archived", "--limit", "5"}, schema)
	require.NoError(t, err, "boolean flag parsing should accept value-less flags")
	require.Equal(t, true, result["include_archived"], "value-less boolean flag should parse as true")
}

func TestParseFlagsToJSON_KebabFlagAliasesSchemaSnakeCase(t *testing.T) {
	t.Parallel()
	schema := ToolSchema{
		Type: "object",
		Properties: map[string]SchemaProperty{
			"tab_id":            {Type: "string"},
			"message_file":      {Type: "string"},
			"include_archived":  {Type: "boolean"},
			"client_message_id": {Type: "string"},
		},
	}

	result, err := parseFlagsToJSON([]string{
		"--tab-id", "tab-1",
		"--message-file", "/tmp/message.txt",
		"--include-archived",
		"--client-message-id", "agent-tool-1",
	}, schema)
	require.NoError(t, err, "kebab-case CLI flags should parse against snake_case schema keys")
	require.Equal(t, "tab-1", result["tab_id"], "kebab tab-id should populate tab_id")
	require.Equal(t, "/tmp/message.txt", result["message_file"], "kebab message-file should populate message_file")
	require.Equal(t, true, result["include_archived"], "value-less kebab boolean should populate include_archived")
	require.Equal(t, "agent-tool-1", result["client_message_id"], "kebab client-message-id should populate client_message_id")
}

func TestRunCLI_SessionTabHelpUsesKebabFlags(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullTestRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{"session_tabs_send", "--help"}, &stdout, &stderr)
	require.Equal(t, 0, code, "tool help should exit successfully")
	require.Empty(t, stderr.String(), "tool help should not write stderr")
	require.Contains(t, stdout.String(), "--tab-id", "session tab help should prefer kebab-case flags")
	require.Contains(t, stdout.String(), "--message-file", "session tab help should prefer kebab-case flags")
	require.NotContains(t, stdout.String(), "--tab_id", "session tab help should not expose snake_case flags")
}

func TestParseFlagsToJSON_InvalidNumber(t *testing.T) {
	t.Parallel()
	schema := ToolSchema{
		Type: "object",
		Properties: map[string]SchemaProperty{
			"limit": {Type: "number"},
		},
	}

	_, err := parseFlagsToJSON([]string{"--limit", "notanumber"}, schema)
	if err == nil {
		t.Error("expected error for non-numeric value, got nil")
	}
}
