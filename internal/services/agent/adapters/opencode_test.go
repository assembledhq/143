package adapters

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestOpenCodeAdapter_ResumeMode(t *testing.T) {
	t.Parallel()

	adapter := NewOpenCodeAdapter(zerolog.Nop())
	require.Equal(t, models.AgentTypeOpenCode, adapter.Name(), "OpenCode adapter should report the OpenCode agent type")
	require.Equal(t, agent.ResumeBySessionID, adapter.ResumeMode(), "OpenCode should resume by explicit session id")
	require.Equal(t, agent.DefaultCancellationSpec, adapter.RuntimeProfile().Cancellation, "OpenCode should use the default cancellation behavior")
	require.True(t, adapter.RuntimeProfile().PreferSplitOutput, "OpenCode should request split stdout/stderr parsing")
}

func TestParseOpenCodeStreamFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		fixture           string
		expectedSessionID string
		expectedError     string
		expectedSummary   string
		expectUsage       bool
	}{
		{
			name:              "simple answer",
			fixture:           "simple_answer.jsonl",
			expectedSessionID: "ses_fixture_simple",
			expectedSummary:   "Here is the answer.",
			expectUsage:       true,
		},
		{
			name:              "tool failure",
			fixture:           "tool_and_failure.jsonl",
			expectedSessionID: "ses_fixture_tools",
			expectedError:     "command failed: go test ./...",
		},
		{
			name:              "file edit",
			fixture:           "file_edit.jsonl",
			expectedSessionID: "ses_fixture_file_edit",
			expectedSummary:   "Created .opencode-fixture.tmp.",
		},
		{
			name:              "shell command success",
			fixture:           "shell_command.jsonl",
			expectedSessionID: "ses_fixture_shell",
			expectedSummary:   "The current directory is /workspace.",
		},
		{
			name:              "permission request",
			fixture:           "permission_request.jsonl",
			expectedSessionID: "ses_fixture_permission",
			expectedError:     "OpenCode requested interactive permission: approval required for edit",
		},
		{
			name:              "session continuation via started event",
			fixture:           "continuation.jsonl",
			expectedSessionID: "ses_fixture_continuation",
			expectedSummary:   "Resuming from the prior session.",
			expectUsage:       true,
		},
		{
			name:              "auth failure",
			fixture:           "auth_failure.jsonl",
			expectedSessionID: "ses_fixture_auth",
			expectedError:     "authentication failed: invalid API key",
		},
		{
			name:              "rate limit failure",
			fixture:           "rate_limit.jsonl",
			expectedSessionID: "ses_fixture_rate_limit",
			expectedError:     "rate limited: retry after 60s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := parseOpenCodeFixture(t, tt.fixture)
			require.Equal(t, tt.expectedSessionID, result.AgentSessionID, "fixture parser should capture the OpenCode session id")
			require.Equal(t, tt.expectedError, result.Error, "fixture parser should surface terminal OpenCode errors")
			if tt.expectedSummary != "" {
				require.Contains(t, result.Summary, tt.expectedSummary, "fixture parser should preserve assistant text in the result summary")
			}
			require.Equal(t, tt.expectUsage, result.TokenUsage.Reported, "fixture parser should capture usage only when the fixture reports it")
		})
	}
}

func parseOpenCodeFixture(t *testing.T, fixture string) agent.AgentResult {
	t.Helper()

	file, err := os.Open(filepath.Join("testdata", "opencode", fixture))
	require.NoError(t, err, "OpenCode fixture should be readable")
	defer func() {
		require.NoError(t, file.Close(), "OpenCode fixture should close cleanly")
	}()

	result := agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 16)
	summary := []string{}
	last := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parseOpenCodeStreamLine(scanner.Bytes(), &result, logCh, &summary, &last)
	}
	require.NoError(t, scanner.Err(), "OpenCode fixture scanner should not fail")
	close(logCh)
	result.Summary = strings.Join(summary, "\n")
	return result
}

func TestOpenCodeStreamingConfigBuildsRunCommands(t *testing.T) {
	t.Parallel()

	run := openCodeStreamingConfig.BuildCmd("/tmp/prompt.md")
	require.Contains(t, run, "opencode run", "OpenCode command should use the non-interactive run subcommand")
	require.Contains(t, run, "--format json", "OpenCode command should request JSON output")
	require.Contains(t, run, "--dangerously-skip-permissions", "OpenCode command should disable interactive permission prompts inside the sandbox")
	require.Contains(t, run, "--agent build", "OpenCode command should use the deterministic build agent")
	require.Contains(t, run, "--model \"${OPENCODE_MODEL:-"+models.OpenCodeModelGLM52+"}\"", "OpenCode command should default to GLM 5.2")
	require.Contains(t, run, "--dir \"$PWD\"", "OpenCode command should bind execution to the sandbox workspace directory")
	require.Contains(t, run, "$(cat '/tmp/prompt.md')", "OpenCode command should pass the rendered prompt content")

	resume := openCodeStreamingConfig.BuildResumeCmd("/tmp/prompt.md", "sess_123")
	require.Contains(t, resume, "--model \"${OPENCODE_MODEL:-"+models.OpenCodeModelGLM52+"}\"", "OpenCode resume command should default to GLM 5.2")
	require.Contains(t, resume, "--session 'sess_123'", "OpenCode resume command should target the explicit prior session id")
	require.Contains(t, resume, "--dir \"$PWD\"", "OpenCode resume command should bind execution to the sandbox workspace directory")
	require.NotContains(t, resume, "--continue", "OpenCode resume should not use nondeterministic latest-session continuation")
}

func TestParseOpenCodeStreamLine(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 8)
	summary := []string{}
	last := ""

	lines := []string{
		`{"type":"session","sessionID":"ses_123"}`,
		`{"type":"message","role":"assistant","content":"implemented the change"}`,
		`{"type":"tool_call","name":"bash","input":{"command":"go test ./..."}}`,
		`{"type":"tool_result","name":"bash","output":"ok"}`,
		`{"type":"usage","usage":{"input_tokens":12,"cache_read_input_tokens":3,"output_tokens":5},"cost_usd":0.0042}`,
	}

	for _, line := range lines {
		parseOpenCodeStreamLine([]byte(line), result, logCh, &summary, &last)
	}
	close(logCh)

	logs := drain(logCh)
	require.Equal(t, "ses_123", result.AgentSessionID, "OpenCode parser should capture camelCase session ids")
	require.Equal(t, "implemented the change", last, "OpenCode parser should track the last assistant message")
	require.Contains(t, strings.Join(summary, "\n"), "implemented the change", "OpenCode parser should add assistant content to the result summary")
	require.True(t, result.TokenUsage.Reported, "OpenCode parser should capture token usage")
	require.Equal(t, 12, result.TokenUsage.InputTokens, "OpenCode parser should capture input tokens")
	require.Equal(t, 3, result.TokenUsage.CachedInputTokens, "OpenCode parser should capture cached input tokens")
	require.Equal(t, 5, result.TokenUsage.OutputTokens, "OpenCode parser should capture output tokens")
	require.NotNil(t, result.TokenUsage.Cost, "OpenCode parser should capture direct reported cost")
	require.Equal(t, agent.TokenCostSourceDirect, result.TokenUsage.Cost.Source, "OpenCode parser should mark direct reported cost as direct")
	require.Len(t, logs, 5, "OpenCode parser should emit one log per input line")
	require.Equal(t, "tool_use", logs[2].Level, "OpenCode parser should surface tool calls")
	require.Equal(t, "bash", logs[2].Metadata["tool"], "OpenCode parser should preserve tool names")
}

func TestParseOpenCodeStreamLine_ErrorAndPermissionEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		line          string
		expectedError string
	}{
		{
			name:          "terminal error sets result error",
			line:          `{"type":"error","error":"rate limited"}`,
			expectedError: "rate limited",
		},
		{
			name:          "permission event fails fast",
			line:          `{"type":"permission","message":"approval required for bash"}`,
			expectedError: "OpenCode requested interactive permission: approval required for bash",
		},
		{
			name:          "permission request event fails fast",
			line:          `{"type":"permission_request","tool":"edit"}`,
			expectedError: "OpenCode requested interactive permission for edit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := &agent.AgentResult{}
			logCh := make(chan agent.LogEntry, 1)
			summary := []string{}
			last := ""

			parseOpenCodeStreamLine([]byte(tt.line), result, logCh, &summary, &last)
			close(logCh)

			logs := drain(logCh)
			require.Equal(t, tt.expectedError, result.Error, "OpenCode parser should convert terminal failures into result errors")
			require.Len(t, logs, 1, "OpenCode parser should emit one log for failures")
			require.Equal(t, "error", logs[0].Level, "OpenCode parser should log failures at error level")
		})
	}
}
