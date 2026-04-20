package adapters

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestAmpAdapter_Name(t *testing.T) {
	t.Parallel()
	adapter := NewAmpAdapter(zerolog.Nop())
	require.Equal(t, models.AgentTypeAmp, adapter.Name(), "adapter name should be amp")
}

func TestAmpAdapter_PreparePrompt(t *testing.T) {
	t.Parallel()

	adapter := NewAmpAdapter(zerolog.Nop())

	prompt, err := adapter.PreparePrompt(context.Background(), &agent.AgentInput{
		Issue:     newTestIssue(models.IssueSourceSentry, true),
		TokenMode: "low",
	})
	require.NoError(t, err)
	require.NotNil(t, prompt)
	require.Equal(t, 50_000, prompt.MaxTokens)
	require.NotEmpty(t, prompt.SystemPrompt)
	require.NotEmpty(t, prompt.UserPrompt)

	_, err = adapter.PreparePrompt(context.Background(), nil)
	require.Error(t, err, "nil input should return error")

	_, err = adapter.PreparePrompt(context.Background(), &agent.AgentInput{Issue: nil})
	require.Error(t, err, "nil issue should return error")
}

func TestAmpAdapter_Execute_StreamJSON(t *testing.T) {
	t.Parallel()

	streamOutput := `{"type":"assistant","content":"Looking at the bug now."}
{"type":"tool_use","tool":"read_file","input":{"path":"main.go"}}
{"type":"tool_result","tool":"read_file","output":"file contents"}
{"type":"assistant","content":"I fixed the null pointer.\n{\"confidence_score\": 0.85, \"confidence_reasoning\": \"clean fix\", \"risk_factors\": []}"}
{"type":"result","content":"Done","usage":{"input_tokens":1200,"output_tokens":340}}
`

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		require.True(t, strings.HasPrefix(cmd, "amp "), "command should invoke amp CLI, got: %s", cmd)
		require.Contains(t, cmd, "--stream-json")
		require.Contains(t, cmd, "--dangerously-allow-all")
		require.Contains(t, cmd, "-m \"${AMP_MODE:-smart}\"",
			"mode must be passed explicitly with AMP_MODE env-var fallback")
		for _, line := range strings.Split(streamOutput, "\n") {
			if line != "" {
				onLine([]byte(line))
			}
		}
		return 0, nil
	}
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		switch {
		case strings.HasPrefix(cmd, "git rev-parse"):
			_, _ = stdout.Write([]byte("true\n"))
			return 0, nil
		case strings.HasPrefix(cmd, "git diff"):
			_, _ = stdout.Write([]byte("diff --git a/main.go b/main.go\n"))
			return 0, nil
		}
		return 0, nil
	}

	adapter := NewAmpAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 100)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		SystemPrompt: "Fix it.",
		UserPrompt:   "Null pointer.",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	require.NotNil(t, result)
	close(logCh)

	require.Equal(t, 0, result.ExitCode)
	require.InDelta(t, 0.85, result.ConfidenceScore, 0.001)
	require.Equal(t, "clean fix", result.ConfidenceReasoning)
	require.Equal(t, 1200, result.TokenUsage.InputTokens)
	require.Equal(t, 340, result.TokenUsage.OutputTokens)
	require.Contains(t, result.Diff, "diff --git")

	var logs []agent.LogEntry
	for entry := range logCh {
		logs = append(logs, entry)
	}
	var sawTool, sawAssistant bool
	for _, l := range logs {
		if l.Level == "tool_use" {
			sawTool = true
		}
		if l.Level == "output" && strings.Contains(l.Message, "null pointer") {
			sawAssistant = true
		}
	}
	require.True(t, sawTool, "expected tool_use log entry")
	require.True(t, sawAssistant, "expected assistant content log entry")

	promptData, ok := provider.Files["/workspace/.143-prompt.md"]
	require.True(t, ok, "prompt file should be written")
	require.Contains(t, string(promptData), "Fix it.")
}

func TestAmpAdapter_Execute_NonZeroExit(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		_, _ = stderr.Write([]byte("amp: invalid api key"))
		return 1, nil
	}

	adapter := NewAmpAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		SystemPrompt: "x",
		UserPrompt:   "y",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	require.Equal(t, 1, result.ExitCode)
	require.Contains(t, result.Error, "exited with code 1")
	require.Contains(t, result.Error, "invalid api key")
}

func TestAmpAdapter_Execute_ContinuationUsesUserMessage(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		return 0, nil
	}
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 0, nil
	}

	adapter := NewAmpAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		Continuation: true,
		UserMessage:  "please continue where we left off",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	close(logCh)

	promptData, ok := provider.Files["/workspace/.143-prompt.md"]
	require.True(t, ok, "continuation should still write the prompt file")
	require.Equal(t, "please continue where we left off", string(promptData),
		"continuation prompt should be the new UserMessage, not empty system/user prompts")
}

func TestAmpAdapter_Execute_MissingProvider(t *testing.T) {
	t.Parallel()

	adapter := NewAmpAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	_, err := adapter.Execute(context.Background(), &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sandbox provider not found")
}

func TestParseAmpStreamLine_UnknownType(t *testing.T) {
	t.Parallel()

	logCh := make(chan agent.LogEntry, 10)
	result := &agent.AgentResult{}
	var summary []string
	var last string

	parseAmpStreamLine([]byte(`{"type":"weird_event","content":"x"}`), result, logCh, &summary, &last)
	close(logCh)

	logs := drain(logCh)
	require.Len(t, logs, 1)
	require.Equal(t, "debug", logs[0].Level, "unknown event types should land in debug log")
}

func TestParseAmpStreamLine_NonJSON(t *testing.T) {
	t.Parallel()

	logCh := make(chan agent.LogEntry, 10)
	result := &agent.AgentResult{}
	var summary []string
	var last string

	parseAmpStreamLine([]byte(`not json at all`), result, logCh, &summary, &last)
	close(logCh)

	logs := drain(logCh)
	require.Len(t, logs, 1)
	require.Equal(t, "output", logs[0].Level)
	require.Contains(t, summary, "not json at all")
}

func TestParseAmpStreamLine_EventTypes(t *testing.T) {
	t.Parallel()

	type check struct {
		level     string
		contains  string
		metaField string
		metaValue interface{}
	}

	tests := []struct {
		name          string
		line          string
		wantLogs      []check
		wantAssistant string
		wantTokens    *agent.TokenUsage
		wantSession   string
	}{
		{
			name:          "assistant falls back to message",
			line:          `{"type":"assistant","message":"hello from amp"}`,
			wantLogs:      []check{{level: "output", contains: "hello from amp"}},
			wantAssistant: "hello from amp",
		},
		{
			name:     "tool_use falls back to name",
			line:     `{"type":"tool_use","name":"bash","input":{"cmd":"ls"}}`,
			wantLogs: []check{{level: "tool_use", contains: "bash", metaField: "tool", metaValue: "bash"}},
		},
		{
			name:     "tool_result falls back to name and result",
			line:     `{"type":"tool_result","name":"bash","result":{"stdout":"ok"}}`,
			wantLogs: []check{{level: "output", contains: "stdout", metaField: "tool", metaValue: "bash"}},
		},
		{
			name:     "thinking goes to debug",
			line:     `{"type":"thinking","content":"considering options"}`,
			wantLogs: []check{{level: "debug", contains: "considering options", metaField: "type", metaValue: "thinking"}},
		},
		{
			name:     "error field wins",
			line:     `{"type":"error","error":"provider unreachable"}`,
			wantLogs: []check{{level: "error", contains: "provider unreachable"}},
		},
		{
			name:     "error falls back to message then content",
			line:     `{"type":"error","content":"fallback text"}`,
			wantLogs: []check{{level: "error", contains: "fallback text"}},
		},
		{
			name:       "result with embedded token usage",
			line:       `{"type":"result","content":"done","result":{"input_tokens":100,"output_tokens":25}}`,
			wantLogs:   []check{{level: "info", contains: "done"}},
			wantTokens: &agent.TokenUsage{InputTokens: 100, OutputTokens: 25},
		},
		{
			name:        "result captures session id",
			line:        `{"type":"result","content":"ok","session_id":"amp-sess-42"}`,
			wantLogs:    []check{{level: "info", contains: "ok"}},
			wantSession: "amp-sess-42",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logCh := make(chan agent.LogEntry, 10)
			result := &agent.AgentResult{}
			var summary []string
			var last string

			parseAmpStreamLine([]byte(tc.line), result, logCh, &summary, &last)
			close(logCh)

			logs := drain(logCh)
			require.Len(t, logs, len(tc.wantLogs))
			for i, want := range tc.wantLogs {
				require.Equal(t, want.level, logs[i].Level)
				require.Contains(t, logs[i].Message, want.contains)
				if want.metaField != "" {
					require.Equal(t, want.metaValue, logs[i].Metadata[want.metaField])
				}
			}
			if tc.wantAssistant != "" {
				require.Equal(t, tc.wantAssistant, last)
			}
			if tc.wantTokens != nil {
				require.Equal(t, tc.wantTokens.InputTokens, result.TokenUsage.InputTokens)
				require.Equal(t, tc.wantTokens.OutputTokens, result.TokenUsage.OutputTokens)
			}
			if tc.wantSession != "" {
				require.Equal(t, tc.wantSession, result.AgentSessionID)
			}
		})
	}
}

func TestAmpAdapter_Execute_WriteFileError(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.WriteFileFn = func(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
		return fmt.Errorf("disk full")
	}

	adapter := NewAmpAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.Error(t, err)
	require.Contains(t, err.Error(), "write prompt file")
}

func TestAmpAdapter_Execute_ExecStreamError(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		return 0, fmt.Errorf("sandbox exec failed")
	}

	adapter := NewAmpAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exec amp CLI")
}

func TestAmpAdapter_Execute_SummaryFromAssistantFallback(t *testing.T) {
	t.Parallel()

	// Only an assistant event — no result/usage — so summary must come from the
	// last assistant content rather than summaryParts.
	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		onLine([]byte(`{"type":"assistant","content":"done, no result event emitted"}`))
		return 0, nil
	}

	adapter := NewAmpAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.NoError(t, err)
	require.Equal(t, "done, no result event emitted", result.Summary)
}

func drain(ch chan agent.LogEntry) []agent.LogEntry {
	var out []agent.LogEntry
	for entry := range ch {
		out = append(out, entry)
	}
	return out
}
