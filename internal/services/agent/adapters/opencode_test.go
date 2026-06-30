package adapters

import (
	"bufio"
	"context"
	"fmt"
	"io"
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
		name              string
		line              string
		expectedError     string
		expectedSessionID string
	}{
		{
			name:          "terminal error sets result error",
			line:          `{"type":"error","error":"rate limited"}`,
			expectedError: "rate limited",
		},
		{
			name:              "terminal object error preserves message ref and session",
			line:              `{"type":"error","sessionID":"ses_opencode","error":{"name":"UnknownError","data":{"message":"Unexpected server error. Check server logs for details.","ref":"err_123"}}}`,
			expectedError:     "UnknownError: Unexpected server error. Check server logs for details. (ref: err_123)",
			expectedSessionID: "ses_opencode",
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
			require.Equal(t, tt.expectedSessionID, result.AgentSessionID, "OpenCode parser should preserve session ids from terminal failures")
			require.Len(t, logs, 1, "OpenCode parser should emit one log for failures")
			require.Equal(t, "error", logs[0].Level, "OpenCode parser should log failures at error level")
		})
	}
}

func TestCaptureOpenCodeFailureLogs_RedactsAndEmitsLatestLogs(t *testing.T) {
	t.Parallel()

	const (
		newLogPath = "/home/sandbox/.local/share/opencode/log/new.log"
		oldLogPath = "/home/sandbox/.local/share/opencode/log/old.log"
	)
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox"}
	provider := newMockProvider()
	provider.Files[newLogPath] = []byte("request failed\nauthorization: Bearer sk-or-v1-secret\napi_key=\"AIzaabcdefghijklmnopqrstuvwxyz\"\n{\"token\":\"plain-json-token-value\",\"OPENROUTER_API_KEY\":\"plain-json-openrouter\"}\nOPENROUTER_API_KEY=plain-env-openrouter\nOPENROUTER_API_KEY=\"plain env key with spaces\"\n")
	provider.Files[oldLogPath] = []byte("old log")
	provider.ReadFileFn = func(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
		require.FailNow(t, "OpenCode log capture should use bounded tail commands instead of reading full files")
		return nil, nil
	}
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		require.Contains(t, cmd, "/home/sandbox/.local/share/opencode/log", "OpenCode log discovery should inspect the sandbox log directory")
		if writeMockOpenCodeLogExec(t, cmd, stdout, provider.Files, []string{newLogPath, "/home/sandbox/not-opencode.log", oldLogPath}) {
			return 0, nil
		}
		require.FailNow(t, "OpenCode log capture should only issue discovery, size, and tail commands", "cmd: %s", cmd)
		return 0, nil
	}

	logCh := make(chan agent.LogEntry, 4)
	ctx := WithSandboxProvider(context.Background(), provider)

	captureOpenCodeFailureLogs(ctx, sandbox, zerolog.Nop(), logCh)
	close(logCh)

	logs := drain(logCh)
	require.Len(t, logs, 2, "OpenCode log capture should emit logs only for files under the OpenCode log directory")
	require.Equal(t, "error", logs[0].Level, "OpenCode diagnostic logs should be emitted at error level for failed runs")
	require.Contains(t, logs[0].Message, "request failed", "OpenCode diagnostic log should include the captured log content")
	require.NotContains(t, logs[0].Message, "sk-or-v1-secret", "OpenCode diagnostic log should redact OpenRouter keys")
	require.NotContains(t, logs[0].Message, "AIzaabcdefghijklmnopqrstuvwxyz", "OpenCode diagnostic log should redact Google API keys")
	require.NotContains(t, logs[0].Message, "plain-json-token-value", "OpenCode diagnostic log should redact quoted token values")
	require.NotContains(t, logs[0].Message, "plain-json-openrouter", "OpenCode diagnostic log should redact quoted API key values")
	require.NotContains(t, logs[0].Message, "plain-env-openrouter", "OpenCode diagnostic log should redact env-style API key values")
	require.NotContains(t, logs[0].Message, "plain env key with spaces", "OpenCode diagnostic log should redact quoted env-style API key values")
	require.Contains(t, logs[0].Message, `"token":"[REDACTED]"`, "OpenCode diagnostic log should preserve quoted token keys while redacting values")
	require.Contains(t, logs[0].Message, `"OPENROUTER_API_KEY":"[REDACTED]"`, "OpenCode diagnostic log should preserve quoted API key names while redacting values")
	require.Contains(t, logs[0].Message, "OPENROUTER_API_KEY=[REDACTED]", "OpenCode diagnostic log should preserve env-style key names while redacting values")
	require.Contains(t, logs[0].Message, "OPENROUTER_API_KEY=\"[REDACTED]\"", "OpenCode diagnostic log should preserve quoted env-style key names while redacting values")
	require.Contains(t, logs[0].Message, "api_key=\"[REDACTED]\"", "OpenCode diagnostic log should preserve key names while redacting values")
	require.Equal(t, "opencode_log", logs[0].Metadata["source"], "OpenCode diagnostic log should identify its source")
	require.Equal(t, "opencode_failure_log", logs[0].Metadata["diagnostic"], "OpenCode diagnostic log should identify diagnostic kind")
	require.Equal(t, newLogPath, logs[0].Metadata["path"], "OpenCode diagnostic log should record the source file path")
	require.Equal(t, false, logs[0].Metadata["truncated"], "small OpenCode diagnostic logs should not be marked truncated")
}

func TestOpenCodeAdapter_Execute_CapturesFailureLogs(t *testing.T) {
	t.Parallel()

	const logPath = "/home/sandbox/.local/share/opencode/log/server.log"
	sandbox := &agent.Sandbox{
		ID:      "test",
		WorkDir: "/workspace",
		HomeDir: "/home/sandbox",
		Metadata: map[string]string{
			agent.SandboxMetadataBaseCommitSHA: "abc123",
		},
	}
	provider := newMockProvider()
	provider.Files[logPath] = []byte("local OpenCode server stack trace\n")
	provider.ReadFileFn = func(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
		require.FailNow(t, "OpenCode failure capture should use bounded tail commands instead of reading full files")
		return nil, nil
	}
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		require.Contains(t, cmd, "opencode run", "OpenCode adapter should execute the OpenCode CLI")
		onLine([]byte(`{"type":"error","sessionID":"ses_opencode","error":{"name":"UnknownError","data":{"message":"Unexpected server error. Check server logs for details.","ref":"err_123"}}}`))
		return 1, nil
	}
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		switch {
		case writeMockOpenCodeLogExec(t, cmd, stdout, provider.Files, []string{logPath}):
		case strings.HasPrefix(cmd, "git rev-parse"):
			_, _ = stdout.Write([]byte("true\n"))
		case strings.HasPrefix(cmd, "git diff"):
			_, _ = stdout.Write([]byte(""))
		}
		return 0, nil
	}

	adapter := NewOpenCodeAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 16)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, &agent.AgentPrompt{
		SystemPrompt: "Fix it.",
		UserPrompt:   "Bug.",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err, "OpenCode execution should return a result for CLI non-zero exits")
	require.NotNil(t, result, "OpenCode execution should return the non-zero result")
	require.Equal(t, 1, result.ExitCode, "OpenCode execution should preserve the CLI exit code")
	require.Contains(t, result.Error, "opencode CLI exited with code 1", "OpenCode execution should surface the non-zero exit")
	require.Contains(t, result.Error, "UnknownError: Unexpected server error. Check server logs for details. (ref: err_123)", "OpenCode execution should preserve parsed terminal error details")
	close(logCh)

	logs := drain(logCh)
	var capturedLog *agent.LogEntry
	for i := range logs {
		if logs[i].Metadata["source"] == "opencode_log" {
			capturedLog = &logs[i]
			break
		}
	}
	require.NotNil(t, capturedLog, "OpenCode execution should attach local OpenCode logs after failures")
	require.Contains(t, capturedLog.Message, "local OpenCode server stack trace", "captured OpenCode log should include local server diagnostics")
	require.Equal(t, logPath, capturedLog.Metadata["path"], "captured OpenCode log should record the source log file")
}

func TestCaptureOpenCodeFailureLogs_TruncatesLargeLogs(t *testing.T) {
	t.Parallel()

	const largeLogPath = "/home/sandbox/.local/share/opencode/log/large.log"
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox"}
	provider := newMockProvider()
	provider.Files[largeLogPath] = []byte(strings.Repeat("a", openCodeFailureLogMaxBytes+10) + "tail")
	provider.ReadFileFn = func(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
		require.FailNow(t, "OpenCode log capture should not read oversized log files in full")
		return nil, nil
	}
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if writeMockOpenCodeLogExec(t, cmd, stdout, provider.Files, []string{largeLogPath}) {
			return 0, nil
		}
		require.FailNow(t, "OpenCode log capture should only issue discovery, size, and tail commands", "cmd: %s", cmd)
		return 0, nil
	}

	logCh := make(chan agent.LogEntry, 2)
	ctx := WithSandboxProvider(context.Background(), provider)

	captureOpenCodeFailureLogs(ctx, sandbox, zerolog.Nop(), logCh)
	close(logCh)

	logs := drain(logCh)
	require.Len(t, logs, 1, "OpenCode log capture should emit the large log")
	require.Contains(t, logs[0].Message, "tail", "OpenCode diagnostic log should keep the end of oversized logs")
	require.Equal(t, true, logs[0].Metadata["truncated"], "oversized OpenCode diagnostic logs should be marked truncated")
	require.Equal(t, openCodeFailureLogMaxBytes+14, logs[0].Metadata["original_bytes"], "OpenCode diagnostic log should record original byte size")
}

func writeMockOpenCodeLogExec(t *testing.T, cmd string, stdout io.Writer, files map[string][]byte, listedPaths []string) bool {
	t.Helper()

	switch {
	case strings.HasPrefix(cmd, "if [ -d "):
		_, _ = stdout.Write([]byte(strings.Join(listedPaths, "\n") + "\n"))
		return true
	case strings.HasPrefix(cmd, "wc -c < "):
		path := mockOpenCodeLogPathForCommand(t, cmd, listedPaths)
		fmt.Fprintf(stdout, "%d\n", len(files[path]))
		return true
	case strings.HasPrefix(cmd, "tail -c "):
		path := mockOpenCodeLogPathForCommand(t, cmd, listedPaths)
		data := files[path]
		if len(data) > openCodeFailureLogMaxBytes {
			data = data[len(data)-openCodeFailureLogMaxBytes:]
		}
		_, _ = stdout.Write(data)
		return true
	default:
		return false
	}
}

func mockOpenCodeLogPathForCommand(t *testing.T, cmd string, paths []string) string {
	t.Helper()

	for _, path := range paths {
		if strings.Contains(cmd, "'"+path+"'") {
			return path
		}
	}
	require.FailNow(t, "OpenCode log command should target a listed log path", "cmd: %s", cmd)
	return ""
}
