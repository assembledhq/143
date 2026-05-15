package adapters

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
)

// ---------------------------------------------------------------------------
// ClaudeCodeAdapter.Name / PreparePrompt
// ---------------------------------------------------------------------------

func TestClaudeCodeAdapter_Name(t *testing.T) {
	t.Parallel()
	a := NewClaudeCodeAdapter(zerolog.Nop())
	require.Equal(t, models.AgentTypeClaudeCode, a.Name())
}

func TestClaudeCodeAdapter_PreparePrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     *agent.AgentInput
		wantErr   bool
		wantToken int
	}{
		{
			name:    "nil input",
			input:   nil,
			wantErr: true,
		},
		{
			name:      "nil issue",
			input:     &agent.AgentInput{Issue: nil},
			wantErr:   false,
			wantToken: defaultLowTokenMax,
		},
		{
			name: "low token mode",
			input: &agent.AgentInput{
				Issue:     &models.Issue{Title: "Bug", Source: models.IssueSourceSentry},
				TokenMode: "low",
			},
			wantToken: defaultLowTokenMax,
		},
		{
			name: "high token mode",
			input: &agent.AgentInput{
				Issue:     &models.Issue{Title: "Bug", Source: models.IssueSourceSentry},
				TokenMode: "high",
			},
			wantToken: defaultHighTokenMax,
		},
		{
			name: "default token mode",
			input: &agent.AgentInput{
				Issue: &models.Issue{Title: "Bug"},
			},
			wantToken: defaultLowTokenMax,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewClaudeCodeAdapter(zerolog.Nop())
			prompt, err := a.PreparePrompt(context.Background(), tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, prompt)
			require.Equal(t, tt.wantToken, prompt.MaxTokens)
			require.NotEmpty(t, prompt.SystemPrompt)
			require.NotEmpty(t, prompt.UserPrompt)
		})
	}
}

func TestResolveTokenLimit(t *testing.T) {
	t.Parallel()

	// Without context limits (nil), uses defaults
	require.Equal(t, defaultLowTokenMax, resolveTokenLimit("low", nil))
	require.Equal(t, defaultHighTokenMax, resolveTokenLimit("high", nil))
	require.Equal(t, defaultLowTokenMax, resolveTokenLimit("", nil))

	// With custom context limits
	custom := &models.ContextLimits{
		AgentLowTokenMax:  75_000,
		AgentHighTokenMax: 250_000,
	}
	require.Equal(t, 75_000, resolveTokenLimit("low", custom))
	require.Equal(t, 250_000, resolveTokenLimit("high", custom))
	require.Equal(t, 75_000, resolveTokenLimit("", custom))

	// Partial override (only low set)
	partial := &models.ContextLimits{AgentLowTokenMax: 60_000}
	require.Equal(t, 60_000, resolveTokenLimit("low", partial))
	require.Equal(t, defaultHighTokenMax, resolveTokenLimit("high", partial))
}

func TestClaudeCodeAdapter_PreparePrompt_WithContextLimits(t *testing.T) {
	t.Parallel()

	a := NewClaudeCodeAdapter(zerolog.Nop())
	input := &agent.AgentInput{
		Issue:     &models.Issue{Title: "Bug", Source: models.IssueSourceSentry},
		TokenMode: "high",
		ContextLimits: &models.ContextLimits{
			AgentHighTokenMax: 300_000,
		},
	}
	prompt, err := a.PreparePrompt(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, 300_000, prompt.MaxTokens, "should use org-specific high token limit")
}

// ---------------------------------------------------------------------------
// ClaudeCodeAdapter.Execute
// ---------------------------------------------------------------------------

func TestClaudeCodeAdapter_Execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		claudeOutput   string
		claudeExitCode int
		diffOutput     string
		diffExitCode   int
		stderrOutput   string
		expectErr      bool
		checkResult    func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry)
	}{
		{
			name: "successful run with streaming JSON",
			claudeOutput: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Analyzing the issue..."}]},"session_id":"sess-1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-1","name":"edit_file","input":{"path":"main.go"}}]},"session_id":"sess-1"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"done"}]},"session_id":"sess-1"}
{"type":"result","subtype":"success","is_error":false,"result":"Fixed the bug.","session_id":"sess-1"}`,
			claudeExitCode: 0,
			diffOutput:     "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n",
			diffExitCode:   0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 0, result.ExitCode)
				require.Empty(t, result.Error)
				require.Contains(t, result.Diff, "diff --git")
				// Assistant text blocks are kept as separate logs, not merged into summary.
				require.NotContains(t, result.Summary, "Analyzing the issue...")
				require.Contains(t, result.Summary, "Fixed the bug.")
			},
		},
		{
			name:           "non-zero exit code with stderr",
			claudeOutput:   "",
			claudeExitCode: 1,
			stderrOutput:   "authentication error",
			diffOutput:     "",
			diffExitCode:   0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 1, result.ExitCode)
				require.Contains(t, result.Error, "exited with code 1")
				require.Contains(t, result.Error, "authentication error")
			},
		},
		{
			name:           "empty output",
			claudeOutput:   "",
			claudeExitCode: 0,
			diffOutput:     "",
			diffExitCode:   0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 0, result.ExitCode)
				require.Empty(t, result.Diff)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := testutil.NewMockSandboxProvider()
			provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				if strings.Contains(cmd, "claude --print") {
					_, _ = stdout.Write([]byte(tt.claudeOutput))
					if tt.stderrOutput != "" {
						_, _ = stderr.Write([]byte(tt.stderrOutput))
					}
					return tt.claudeExitCode, nil
				}
				if strings.HasPrefix(cmd, "git rev-parse") {
					_, _ = stdout.Write([]byte("true\n"))
					return 0, nil
				}
				if strings.HasPrefix(cmd, "git diff") {
					_, _ = stdout.Write([]byte(tt.diffOutput))
					return tt.diffExitCode, nil
				}
				return 0, nil
			}

			adapter := NewClaudeCodeAdapter(zerolog.Nop())
			sandbox := &agent.Sandbox{ID: "test-sandbox", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
			prompt := &agent.AgentPrompt{
				SystemPrompt: "Fix the bug.",
				UserPrompt:   "Null pointer error.",
				MaxTokens:    50_000,
			}

			logCh := make(chan agent.LogEntry, 100)
			ctx := WithSandboxProvider(context.Background(), provider)

			result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)

			close(logCh)
			var logs []agent.LogEntry
			for entry := range logCh {
				logs = append(logs, entry)
			}

			tt.checkResult(t, result, logs)

			// Verify prompt file was written.
			promptData, exists := provider.Files["/home/sandbox/.143-prompt.md"]
			require.True(t, exists, "prompt file should have been written")
			require.Contains(t, string(promptData), "Fix the bug.")
			require.NotContains(t, provider.ExecCalls[0], ".143-agent.pid", "claude adapter must not embed pidfile scaffolding (provider internal)")
			require.NotContains(t, provider.ExecCalls[0], "& pid=$!", "claude adapter must not embed shell-shim wrapping (provider internal)")
		})
	}
}

func TestClaudeCodeAdapter_Execute_ExecError(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.Contains(cmd, "claude --print") {
			return 0, context.DeadlineExceeded
		}
		return 0, nil
	}

	adapter := NewClaudeCodeAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "exec claude CLI")
}

func TestClaudeCodeAdapter_Execute_WriteFileError(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.WriteFileFn = func(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
		return context.DeadlineExceeded
	}

	adapter := NewClaudeCodeAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "write prompt file")
}

func TestClaudeCodeAdapter_Execute_MissingSandboxProvider(t *testing.T) {
	t.Parallel()

	adapter := NewClaudeCodeAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)

	result, err := adapter.Execute(context.Background(), sandbox, prompt, logCh)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "sandbox provider not found")
}

func TestClaudeCodeAdapter_Execute_ContinuationWithSessionIDUsesResumeByID(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.Contains(cmd, "claude --print") {
			_, _ = stdout.Write([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"continuing the session"}]}}`))
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git rev-parse") {
			_, _ = stdout.Write([]byte("true\n"))
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git diff") {
			_, _ = stdout.Write([]byte("diff --git a/main.go b/main.go\n"))
			return 0, nil
		}
		return 0, nil
	}

	adapter := NewClaudeCodeAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{
		UserMessage:     "Please tighten the guard clause.",
		MaxTokens:       50_000,
		Continuation:    true,
		ResumeSessionID: "claude-session-abc",
	}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.NoError(t, err, "continuation should succeed")
	require.NotNil(t, result, "continuation should return a result")
	require.NotContains(t, provider.ExecCalls[0], "--continue", "continuation must not use --continue, which is non-deterministic")
	require.Contains(t, provider.ExecCalls[0], "--resume claude-session-abc", "continuation should resume by explicit session ID")
	_, exists := provider.Files["/home/sandbox/.143-prompt.md"]
	require.False(t, exists, "deterministic resume should not write a fresh prompt file")
}

func TestPrepareClaudeHumanInputHooks_ConfiguresActionableToolApprovals(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	sandbox := &agent.Sandbox{ID: "s1", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	_, envPrefix, err := prepareClaudeHumanInputHooks(context.Background(), provider, sandbox, &agent.AgentPrompt{})
	require.NoError(t, err, "prepareClaudeHumanInputHooks should write hook settings")
	require.Contains(t, envPrefix, "CLAUDE_143_HUMAN_INPUT_TOOLS=", "hook command environment should carry the shared tool matcher list")
	for _, matcher := range claudeHumanInputHookMatchers {
		require.Contains(t, envPrefix, matcher, "hook command environment should include matcher %s", matcher)
	}

	settingsBytes := provider.Files["/home/sandbox/.143-claude-settings.json"]
	require.NotEmpty(t, settingsBytes, "Claude settings should be written into the sandbox home")

	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(settingsBytes, &settings), "Claude settings should be valid JSON")
	require.NotEmpty(t, settings.Hooks["PreToolUse"], "settings should configure PreToolUse hooks")

	matchers := map[string]bool{}
	for _, hook := range settings.Hooks["PreToolUse"] {
		matchers[hook.Matcher] = true
	}
	for _, matcher := range []string{"AskUserQuestion", "Bash", "Edit", "MultiEdit", "Write", "WebFetch", "WebSearch"} {
		require.True(t, matchers[matcher], "PreToolUse hooks should route %s approvals through 143", matcher)
	}
}

func TestClaudeCodeAdapter_Execute_ContinuationWithoutSessionIDFallsBackToFreshExec(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.Contains(cmd, "claude --print") {
			_, _ = stdout.Write([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"continuing the session"}]}}`))
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git rev-parse") {
			_, _ = stdout.Write([]byte("true\n"))
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git diff") {
			return 0, nil
		}
		return 0, nil
	}

	adapter := NewClaudeCodeAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{
		SystemPrompt: "system",
		UserPrompt:   "history-embedded user prompt",
		UserMessage:  "Please tighten the guard clause.",
		MaxTokens:    50_000,
		Continuation: true,
	}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.NoError(t, err, "continuation should succeed when falling back to fresh exec")
	require.NotNil(t, result, "continuation should return a result")
	require.NotContains(t, provider.ExecCalls[0], "--continue", "continuation must not use --continue, which is non-deterministic")
	require.NotContains(t, provider.ExecCalls[0], "--resume", "continuation without an id must not pass --resume")
	contents, exists := provider.Files["/home/sandbox/.143-prompt.md"]
	require.True(t, exists, "fresh exec must write the system+user prompt to a file")
	require.Contains(t, string(contents), "history-embedded user prompt", "prompt file should carry the orchestrator-provided history-embedded user prompt")
}

func TestClaudeCodeAdapter_Execute_AcceptsFileEditsWithoutBypassingAllPermissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prompt *agent.AgentPrompt
	}{
		{
			name: "fresh turn",
			prompt: &agent.AgentPrompt{
				SystemPrompt: "system",
				UserPrompt:   "user prompt",
				MaxTokens:    50_000,
			},
		},
		{
			name: "resume by session id",
			prompt: &agent.AgentPrompt{
				UserMessage:     "Please continue.",
				MaxTokens:       50_000,
				Continuation:    true,
				ResumeSessionID: "claude-session-abc",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := testutil.NewMockSandboxProvider()
			provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				if strings.HasPrefix(cmd, "claude") {
					_, _ = stdout.Write([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`))
					return 0, nil
				}
				if strings.HasPrefix(cmd, "git rev-parse") {
					_, _ = stdout.Write([]byte("true\n"))
					return 0, nil
				}
				if strings.HasPrefix(cmd, "git diff") {
					return 0, nil
				}
				return 0, nil
			}

			adapter := NewClaudeCodeAdapter(zerolog.Nop())
			sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
			logCh := make(chan agent.LogEntry, 10)
			ctx := WithSandboxProvider(context.Background(), provider)

			result, err := adapter.Execute(ctx, sandbox, tt.prompt, logCh)
			require.NoError(t, err, "execute should succeed")
			require.NotNil(t, result, "execute should return a result")
			require.NotEmpty(t, provider.ExecCalls, "execute should invoke the Claude CLI")
			require.Contains(t, provider.ExecCalls[0], "--permission-mode acceptEdits", "Claude CLI should auto-approve file edits inside the gVisor sandbox")
			require.NotContains(t, provider.ExecCalls[0], "--dangerously-skip-permissions", "Claude CLI should not bypass every permission check while public internet egress is available")
		})
	}
}

func TestClaudeCodeAdapter_ResumeMode(t *testing.T) {
	t.Parallel()

	adapter := NewClaudeCodeAdapter(zerolog.Nop())
	require.Equal(t, agent.ResumeBySessionID, adapter.ResumeMode())
}

func TestClaudeCodeAdapter_Execute_CapturesResultEventSessionID(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.Contains(cmd, "claude --print") {
			// Claude Code emits the session id on its terminal `result` event.
			_, _ = stdout.Write([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n" +
				`{"type":"result","subtype":"success","result":"summary","session_id":"claude-session-xyz"}`))
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git rev-parse") {
			_, _ = stdout.Write([]byte("true\n"))
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git diff") {
			return 0, nil
		}
		return 0, nil
	}

	adapter := NewClaudeCodeAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{SystemPrompt: "system", UserPrompt: "user", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "claude-session-xyz", result.AgentSessionID, "result event's session_id must populate AgentSessionID for next-turn resume")
}

func TestClaudeCodeAdapter_Execute_IncludesReasoningEffortOverride(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.Contains(cmd, "claude --print") {
			_, _ = stdout.Write([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`))
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git rev-parse") {
			_, _ = stdout.Write([]byte("true\n"))
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git diff") {
			return 0, nil
		}
		return 0, nil
	}

	adapter := NewClaudeCodeAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{
		SystemPrompt:    "test",
		UserPrompt:      "test",
		MaxTokens:       50_000,
		ReasoningEffort: models.ReasoningEffortHigh,
	}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.NoError(t, err, "execute should succeed with a reasoning effort override")
	require.NotNil(t, result, "execute should return a result")
	require.Contains(t, provider.ExecCalls[0], "--effort high", "claude command should include the requested effort level")
}

// ---------------------------------------------------------------------------
// parseStreamOutput (Claude Code streaming JSON)
// ---------------------------------------------------------------------------

func TestParseStreamOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      string
		checkResult func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry)
	}{
		{
			name: "assistant text blocks stay as separate logs, summary falls back to last",
			output: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Investigating..."}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Found the bug."}]}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 2)
				require.Equal(t, "output", logs[0].Level)
				require.Equal(t, "Investigating...", logs[0].Message)
				require.Equal(t, "Found the bug.", logs[1].Message)
				// Without a result event, summary falls back to last assistant text.
				require.Equal(t, "Found the bug.", result.Summary)
			},
		},
		{
			name:   "system init event hidden as debug",
			output: `{"type":"system","subtype":"init","tools":["Bash"],"session_id":"s"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "debug", logs[0].Level)
				require.Equal(t, "s", result.AgentSessionID)
			},
		},
		{
			name:   "assistant tool_use block",
			output: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-1","name":"edit_file","input":{}}]}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "tool_use", logs[0].Level)
				require.Contains(t, logs[0].Message, "edit_file")
				require.Equal(t, "edit_file", logs[0].Metadata["tool"])
				require.Equal(t, "tu-1", logs[0].Metadata["call_id"])
			},
		},
		{
			name:   "assistant tool_use block with input preserves description",
			output: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-2","name":"Bash","input":{"command":"ls -la","description":"List files"}}]}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "tool_use", logs[0].Level)
				input, ok := logs[0].Metadata["input"].(map[string]interface{})
				require.True(t, ok, "expected input metadata to be a map")
				require.Equal(t, "ls -la", input["command"])
				require.Equal(t, "List files", input["description"])
			},
		},
		{
			name: "mixed text and tool_use blocks emit separate logs in order",
			output: `{"type":"assistant","message":{"role":"assistant","content":[` +
				`{"type":"text","text":"I'll edit the file."},` +
				`{"type":"tool_use","id":"tu-3","name":"edit_file","input":{"path":"x.go"}}` +
				`]}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 2)
				require.Equal(t, "output", logs[0].Level)
				require.Equal(t, "I'll edit the file.", logs[0].Message)
				require.Equal(t, "tool_use", logs[1].Level)
				require.Equal(t, "edit_file", logs[1].Metadata["tool"])
			},
		},
		{
			name:   "user tool_result block (string content)",
			output: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"file updated"}]}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "output", logs[0].Level)
				require.Equal(t, "file updated", logs[0].Message)
				require.Equal(t, "tool_result", logs[0].Metadata["type"])
				require.Equal(t, "tu-1", logs[0].Metadata["call_id"])
			},
		},
		{
			name:   "user tool_result block (array content)",
			output: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":[{"type":"text","text":"line1\n"},{"type":"text","text":"line2"}]}]}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "output", logs[0].Level)
				require.Equal(t, "line1\nline2", logs[0].Message)
				require.Equal(t, "tool_result", logs[0].Metadata["type"])
			},
		},
		{
			name:   "user tool_result block flagged as error",
			output: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"boom","is_error":true}]}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, true, logs[0].Metadata["is_error"])
			},
		},
		{
			name:   "result event with summary, usage, and session id",
			output: `{"type":"result","subtype":"success","result":"Done.","session_id":"sess-z","usage":{"input_tokens":1000,"output_tokens":500}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "Done.")
				require.Equal(t, 1000, result.TokenUsage.InputTokens)
				require.Equal(t, 500, result.TokenUsage.OutputTokens)
				require.Equal(t, "sess-z", result.AgentSessionID)
			},
		},
		{
			name:   "result event with deferred AskUserQuestion emits human input request",
			output: `{"type":"result","subtype":"success","stop_reason":"tool_deferred","session_id":"sess-z","deferred_tool_use":{"id":"toolu_01abc","name":"AskUserQuestion","input":{"questions":[{"header":"Framework","question":"Which framework?","multiSelect":false,"options":[{"label":"React","description":"Use React"},{"label":"Vue","description":"Use Vue"}]}]}}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.True(t, result.RequiresHumanInput, "deferred AskUserQuestion should pause the orchestrator")
				require.Equal(t, "sess-z", result.AgentSessionID, "deferred result should still capture session id")
				require.Len(t, logs, 2, "deferred result should log summary plus human input request")
				require.Equal(t, "human_input", logs[1].Level, "deferred tool should emit a structured human input log")
				require.NotNil(t, logs[1].HumanInput, "human input log should carry the normalized request")
				require.Equal(t, "toolu_01abc", logs[1].HumanInput.ProviderRequestID, "provider request id should come from deferred tool id")
				require.Equal(t, models.HumanInputRequestKindSingleChoice, logs[1].HumanInput.Kind, "single-select options should map to single_choice")
				require.Equal(t, "Framework", logs[1].HumanInput.Title, "header should become the request title")
				require.Equal(t, "Which framework?", logs[1].HumanInput.Body, "question should become the request body")
				require.Equal(t, []models.HumanInputChoice{
					{ID: "react", Label: "React", Description: "Use React"},
					{ID: "vue", Label: "Vue", Description: "Use Vue"},
				}, logs[1].HumanInput.Choices, "options should be normalized into action rows")
			},
		},
		{
			name:   "result event with top level cost and usage",
			output: `{"type":"result","content":"Done.","total_cost_usd":0.37,"usage":{"input_tokens":1000,"output_tokens":500,"cache_read_input_tokens":250,"cache_creation_input_tokens":125}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 1000, result.TokenUsage.InputTokens)
				require.Equal(t, 500, result.TokenUsage.OutputTokens)
				require.Equal(t, 250, result.TokenUsage.CachedInputTokens)
				require.Equal(t, 125, result.TokenUsage.CacheCreationTokens)
				require.Equal(t, 0.37, result.TokenUsage.TotalCostUSD)
				require.NotNil(t, result.TokenUsage.Cost)
				require.Equal(t, agent.TokenCostSourceDirect, result.TokenUsage.Cost.Source)
			},
		},
		{
			name:   "result event accepts output-only usage",
			output: `{"type":"result","subtype":"success","result":"Done.","usage":{"input_tokens":0,"output_tokens":42}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 0, result.TokenUsage.InputTokens)
				require.Equal(t, 42, result.TokenUsage.OutputTokens)
			},
		},
		{
			name:   "result event with confidence in summary",
			output: `{"type":"result","subtype":"success","result":"Fixed.\n{\"confidence_score\": 0.95, \"confidence_reasoning\": \"Straightforward\", \"risk_factors\": [\"none\"]}"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.InDelta(t, 0.95, result.ConfidenceScore, 0.001)
				require.Equal(t, "Straightforward", result.ConfidenceReasoning)
			},
		},
		{
			name:   "unknown event type logged as debug",
			output: `{"type":"unknown_type","payload":{}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "debug", logs[0].Level)
			},
		},
		{
			name:   "non-JSON line emitted as raw output",
			output: `this is not json`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "output", logs[0].Level)
				require.Contains(t, logs[0].Message, "this is not json")
			},
		},
		{
			name:   "empty output",
			output: "",
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Empty(t, result.Summary)
				require.Empty(t, logs)
			},
		},
		{
			name:   "blank lines are skipped",
			output: "\n\n  \n",
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Empty(t, logs)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := &agent.AgentResult{}
			logCh := make(chan agent.LogEntry, 100)
			parseStreamOutput([]byte(tt.output), result, logCh)
			close(logCh)

			var logs []agent.LogEntry
			for entry := range logCh {
				logs = append(logs, entry)
			}
			tt.checkResult(t, result, logs)
		})
	}
}

// ---------------------------------------------------------------------------
// tryExtractConfidence
// ---------------------------------------------------------------------------

func TestTryExtractConfidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		text          string
		wantScore     float64
		wantReasoning string
		wantRisks     []string
	}{
		{
			name:      "no confidence block",
			text:      "Just some regular text with no confidence data.",
			wantScore: 0,
		},
		{
			name:          "valid confidence block",
			text:          `Here is my fix. {"confidence_score": 0.88, "confidence_reasoning": "Simple fix", "risk_factors": ["edge case"]}`,
			wantScore:     0.88,
			wantReasoning: "Simple fix",
			wantRisks:     []string{"edge case"},
		},
		{
			name:      "malformed JSON around confidence_score",
			text:      `{"confidence_score": bad_value}`,
			wantScore: 0,
		},
		{
			name:      "missing braces",
			text:      `"confidence_score": 0.5`,
			wantScore: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := &agent.AgentResult{}
			tryExtractConfidence(tt.text, result)
			require.InDelta(t, tt.wantScore, result.ConfidenceScore, 0.001)
			if tt.wantReasoning != "" {
				require.Equal(t, tt.wantReasoning, result.ConfidenceReasoning)
			}
			if tt.wantRisks != nil {
				require.Equal(t, tt.wantRisks, result.RiskFactors)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// collectDiff
// ---------------------------------------------------------------------------

func TestCollectDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		isGitRepo         bool // whether git rev-parse returns 0
		baseCommitSHA     string
		diffStdout        string
		diffExitCode      int
		untrackedStdout   string
		untrackedDiffs    map[string]string
		untrackedExitCode int
		execErr           error
		wantDiff          string
		wantErr           bool
	}{
		{
			name:          "successful diff uses immutable base commit",
			isGitRepo:     true,
			baseCommitSHA: "abc123",
			diffStdout:    "diff --git a/f.go b/f.go\n+fixed\n",
			wantDiff:      "diff --git a/f.go b/f.go\n+fixed\n",
		},
		{
			name:            "includes untracked files as synthetic additions",
			isGitRepo:       true,
			baseCommitSHA:   "abc123",
			diffStdout:      "diff --git a/f.go b/f.go\n+fixed\n",
			untrackedStdout: "new.txt\n",
			untrackedDiffs: map[string]string{
				"new.txt": "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+hello\n",
			},
			untrackedExitCode: 1,
			wantDiff:          "diff --git a/f.go b/f.go\n+fixed\ndiff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+hello\n",
		},
		{
			name:          "non-zero diff exit code",
			isGitRepo:     true,
			baseCommitSHA: "abc123",
			diffExitCode:  1,
			wantErr:       true,
		},
		{
			name:          "exec error on rev-parse",
			baseCommitSHA: "abc123",
			execErr:       context.DeadlineExceeded,
			wantErr:       true,
		},
		{
			name:          "not a git repo returns empty diff",
			baseCommitSHA: "abc123",
			isGitRepo:     false,
			wantDiff:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := testutil.NewMockSandboxProvider()
			provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				if tt.execErr != nil {
					return 0, tt.execErr
				}
				if strings.HasPrefix(cmd, "git rev-parse") {
					if tt.isGitRepo {
						_, _ = stdout.Write([]byte("true\n"))
						return 0, nil
					}
					_, _ = stderr.Write([]byte("fatal: not a git repository\n"))
					return 128, nil
				}
				if strings.HasPrefix(cmd, "git diff --find-renames --binary --no-index") {
					for filePath, diff := range tt.untrackedDiffs {
						if strings.Contains(cmd, filePath) {
							_, _ = stdout.Write([]byte(diff))
							return tt.untrackedExitCode, nil
						}
					}
					return tt.untrackedExitCode, nil
				}
				if strings.HasPrefix(cmd, "git diff --find-renames --binary") {
					_, _ = stdout.Write([]byte(tt.diffStdout))
					return tt.diffExitCode, nil
				}
				if strings.HasPrefix(cmd, "git ls-files --others --exclude-standard") {
					_, _ = stdout.Write([]byte(tt.untrackedStdout))
					return 0, nil
				}
				return 0, nil
			}

			sandbox := &agent.Sandbox{
				ID:      "test",
				WorkDir: "/workspace",
				HomeDir: "/home/sandbox",
				Metadata: map[string]string{
					"base_commit_sha": tt.baseCommitSHA,
				},
			}
			diff, err := collectDiff(context.Background(), provider, sandbox, zerolog.Nop())
			if tt.wantErr {
				require.Error(t, err, "collectDiff should return an error")
				return
			}
			require.NoError(t, err, "collectDiff should not return an error")
			require.Equal(t, tt.wantDiff, diff, "collectDiff should return the expected authoritative diff")
		})
	}
}

// ---------------------------------------------------------------------------
// WithSandboxProvider
// ---------------------------------------------------------------------------

func TestWithSandboxProvider(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	ctx := WithSandboxProvider(context.Background(), provider)

	retrieved := agent.SandboxProviderFromContext(ctx)
	require.NotNil(t, retrieved)
	require.Equal(t, provider, retrieved)
}

func TestShellEscapeSingle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"it's", "it'\\''s"},
		{"/path/to/file", "/path/to/file"},
		{"don't stop", "don'\\''t stop"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, shellEscapeSingle(tt.input))
		})
	}
}
