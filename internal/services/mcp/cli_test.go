package mcp

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
