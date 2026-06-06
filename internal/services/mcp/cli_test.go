package mcp

import (
	"bytes"
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/stretchr/testify/require"
)

func TestRunCLI_HelpListsNamespaces(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullCLIRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{"--help"}, &stdout, &stderr)

	require.Equal(t, 0, code, "top-level help should return success")
	require.Empty(t, stderr.String(), "top-level help should not write stderr")
	output := stdout.String()
	require.Contains(t, output, "143-tools <namespace> <action>", "top-level help should show hierarchical usage")
	require.Contains(t, output, "Namespaces:", "top-level help should group commands by namespace")
	require.Contains(t, output, "sentry", "top-level help should include configured provider namespace")
	require.Contains(t, output, "linear", "top-level help should include configured task namespace")
	require.Contains(t, output, "logs", "top-level help should include configured logs namespace")
	require.Contains(t, output, "pr", "top-level help should include 143 PR namespace")
	require.NotContains(t, output, "sentry_list_errors", "top-level help should not list old flat command names")
	require.NotContains(t, output, "linear_list_tasks", "top-level help should not list old flat command names")
}

func TestRunCLI_NamespaceHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		contains  []string
		notString string
	}{
		{
			name:      "provider namespace",
			args:      []string{"sentry", "--help"},
			contains:  []string{"143-tools sentry <action>", "list_errors", "get_error"},
			notString: "sentry_list_errors",
		},
		{
			name:      "logs namespace",
			args:      []string{"logs", "--help"},
			contains:  []string{"143-tools logs <action>", "query", "context", "stats"},
			notString: "log_query",
		},
		{
			name:      "pull request namespace",
			args:      []string{"pr", "--help"},
			contains:  []string{"143-tools pr <action>", "create"},
			notString: "create_pr",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := NewToolRegistry(buildFullCLIRegistry())
			var stdout, stderr bytes.Buffer
			code := RunCLI(context.Background(), tr, tt.args, &stdout, &stderr)

			require.Equal(t, 0, code, "namespace help should return success")
			require.Empty(t, stderr.String(), "namespace help should not write stderr")
			output := stdout.String()
			for _, expected := range tt.contains {
				require.Contains(t, output, expected, "namespace help should include expected usage and actions")
			}
			require.NotContains(t, output, tt.notString, "namespace help should not expose old flat command names")
		})
	}
}

func TestRunCLI_ActionHelp(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullCLIRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{"sentry", "list_errors", "--help"}, &stdout, &stderr)

	require.Equal(t, 0, code, "action help should return success")
	require.Empty(t, stderr.String(), "action help should not write stderr")
	output := stdout.String()
	require.Contains(t, output, "Usage: 143-tools sentry list_errors", "action help should show hierarchical usage")
	require.Contains(t, output, "--severity", "action help should include schema-derived flags")
	require.Contains(t, output, "--limit", "action help should include schema-derived flags")
	require.NotContains(t, output, "sentry_list_errors", "action help should not expose old flat command names")
}

func TestRunCLI_DispatchesHierarchicalCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "error tracker",
			args:     []string{"sentry", "list_errors", "--severity", "high", "--limit", "10"},
			expected: "test error",
		},
		{
			name:     "task manager",
			args:     []string{"linear", "create_task", "--title", "Fix auth bug", "--team_key", "ENG"},
			expected: "Fix auth bug",
		},
		{
			name:     "logs",
			args:     []string{"logs", "query", "--provider", "victorialogs", "--query", "service:api", "--since", "1h", "--limit", "5"},
			expected: "log line",
		},
		{
			name:     "pull request creator",
			args:     []string{"pr", "create", "--draft", "false"},
			expected: "queued",
		},
		{
			name:     "issue creator",
			args:     []string{"issue", "create", "--title", "Follow-up", "--description", "Investigate more"},
			expected: "Follow-up",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := NewToolRegistry(buildFullCLIRegistry())
			var stdout, stderr bytes.Buffer
			code := RunCLI(context.Background(), tr, tt.args, &stdout, &stderr)

			require.Equal(t, 0, code, "hierarchical command should dispatch successfully: stderr=%s", stderr.String())
			require.Contains(t, stdout.String(), tt.expected, "hierarchical command should print tool result")
		})
	}
}

func TestRunCLI_RejectsFlatCommandsWithMigrationHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		args              []string
		expectedMigration string
	}{
		{
			name:              "provider command",
			args:              []string{"sentry_list_errors", "--limit", "5"},
			expectedMigration: "143-tools sentry list_errors",
		},
		{
			name:              "logs command",
			args:              []string{"log_query", "--query", "service:api", "--since", "1h"},
			expectedMigration: "143-tools logs query",
		},
		{
			name:              "pull request command",
			args:              []string{"create_pr", "--help"},
			expectedMigration: "143-tools pr create",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := NewToolRegistry(buildFullCLIRegistry())
			var stdout, stderr bytes.Buffer
			code := RunCLI(context.Background(), tr, tt.args, &stdout, &stderr)

			require.Equal(t, 1, code, "old flat command should fail")
			require.Empty(t, stdout.String(), "old flat command should not print normal output")
			require.Contains(t, stderr.String(), "no longer supported", "old flat command should explain migration")
			require.Contains(t, stderr.String(), tt.expectedMigration, "old flat command should show replacement command")
			require.Contains(t, stderr.String(), "--help", "old flat command should point to help usage")
		})
	}
}

func TestRunCLI_UnknownUnderscoreNameIsUnknownNamespace(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullCLIRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{"foo_bar", "--limit", "5"}, &stdout, &stderr)

	require.Equal(t, 1, code, "unknown command should fail")
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "unknown namespace", "unknown underscore-name should not be treated as a deprecated flat command")
	require.NotContains(t, stderr.String(), "no longer supported", "unknown underscore-name should not falsely claim the command was deprecated")
}

func TestRunCLI_ParsingErrorsAreSelfCorrecting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		contains []string
	}{
		{
			name:     "unknown namespace",
			args:     []string{"unknown", "list"},
			contains: []string{`unknown namespace "unknown"`, "143-tools --help"},
		},
		{
			name:     "missing action",
			args:     []string{"sentry"},
			contains: []string{`missing action for namespace "sentry"`, "143-tools sentry --help"},
		},
		{
			name:     "unknown action",
			args:     []string{"sentry", "wat"},
			contains: []string{`unknown action "wat" for namespace "sentry"`, "143-tools sentry --help"},
		},
		{
			name:     "missing required flag",
			args:     []string{"sentry", "get_error"},
			contains: []string{"missing required flag: --error_id", "143-tools sentry get_error --help"},
		},
		{
			name:     "unexpected positional argument",
			args:     []string{"sentry", "list_errors", "severity", "high"},
			contains: []string{`unexpected argument "severity"`, "143-tools sentry list_errors --help"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := NewToolRegistry(buildFullCLIRegistry())
			var stdout, stderr bytes.Buffer
			code := RunCLI(context.Background(), tr, tt.args, &stdout, &stderr)

			require.Equal(t, 1, code, "invalid command should fail")
			require.Empty(t, stdout.String(), "invalid command should not print normal output")
			for _, expected := range tt.contains {
				require.Contains(t, stderr.String(), expected, "error should contain self-correcting usage guidance")
			}
		})
	}
}

func TestRunCLI_ArrayFlag(t *testing.T) {
	t.Parallel()

	tr := NewToolRegistry(buildFullCLIRegistry())
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), tr, []string{
		"linear", "list_tasks", "--states", "triage,backlog",
	}, &stdout, &stderr)

	require.Equal(t, 0, code, "array flags should still parse as comma-separated values: stderr=%s", stderr.String())
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

	result, err := parseFlagsToJSON([]string{"--severity", "high", "--limit", "25", "--states", "a,b,c"}, schema)

	require.NoError(t, err, "valid flags should parse")
	require.Equal(t, "high", result["severity"], "string flag should remain a string")
	require.Equal(t, float64(25), result["limit"], "number flag should parse as float64")
	require.Equal(t, []string{"a", "b", "c"}, result["states"], "array flag should split comma-separated values")
}

func TestParseFlagsToJSON_InvalidNumber(t *testing.T) {
	t.Parallel()

	schema := ToolSchema{
		Type:       "object",
		Properties: map[string]SchemaProperty{"limit": {Type: "number"}},
	}

	_, err := parseFlagsToJSON([]string{"--limit", "notanumber"}, schema)

	require.Error(t, err, "invalid number flag should return an error")
}

func buildFullCLIRegistry() *integration.Registry {
	reg := buildFullTestRegistry()
	reg.RegisterLogProvider(&mcpLogProvider{name: models.ProviderVictoriaLogs, supportsStats: true})
	return reg
}

func TestDetectOldFlatCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "provider-prefixed", input: "sentry_list_errors", expected: "143-tools sentry list_errors"},
		{name: "logs", input: "log_query", expected: "143-tools logs query"},
		{name: "pull request", input: "create_pr", expected: "143-tools pr create"},
		{name: "not flat", input: "sentry", expected: ""},
		{name: "unknown underscore name", input: "foo_bar", expected: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			commands := buildCLICommands(NewToolRegistry(buildFullCLIRegistry()).ListTools())
			got, ok := replacementForOldFlatCommand(tt.input, commands)
			if tt.expected == "" {
				require.False(t, ok, "non-flat namespace should not be treated as deprecated flat command")
				return
			}
			require.True(t, ok, "old flat command should have a replacement")
			require.Equal(t, tt.expected, got, "old flat command should map to expected hierarchical replacement")
		})
	}
}
