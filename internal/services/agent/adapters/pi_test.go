package adapters

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestPiAdapter_Name(t *testing.T) {
	t.Parallel()
	adapter := NewPiAdapter(zerolog.Nop())
	require.Equal(t, models.AgentTypePi, adapter.Name(), "adapter name should be pi")
}

func TestPiAdapter_PreparePrompt(t *testing.T) {
	t.Parallel()

	adapter := NewPiAdapter(zerolog.Nop())

	prompt, err := adapter.PreparePrompt(context.Background(), &agent.AgentInput{
		Issue:     newTestIssue(models.IssueSourceLinear, true),
		TokenMode: "high",
	})
	require.NoError(t, err)
	require.Equal(t, 200_000, prompt.MaxTokens)

	_, err = adapter.PreparePrompt(context.Background(), nil)
	require.Error(t, err)
}

func TestPiAdapter_Execute_StreamJSON(t *testing.T) {
	t.Parallel()

	streamOutput := `{"type":"assistant","content":"Reading the file."}
{"type":"tool_use","name":"read","input":{"path":"x.go"},"model":"anthropic/claude-sonnet-4-6"}
{"type":"tool_result","name":"read","output":"contents"}
{"type":"assistant","content":"Patched.\n{\"confidence_score\": 0.7, \"confidence_reasoning\": \"reviewed\", \"risk_factors\": []}"}
{"type":"done","content":"complete","usage":{"input_tokens":900,"output_tokens":150}}
`

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		require.True(t, strings.HasPrefix(cmd, "pi "), "command should invoke pi CLI, got: %s", cmd)
		require.Contains(t, cmd, "--mode json")
		require.Contains(t, cmd, "--model")
		require.Contains(t, cmd, "PI_MODEL_CUSTOM", "must respect PI_MODEL_CUSTOM override")
		require.Contains(t, cmd, "PI_MODEL", "must consult PI_MODEL fallback")
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
			_, _ = stdout.Write([]byte("diff --git a/x.go b/x.go\n"))
			return 0, nil
		}
		return 0, nil
	}

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 100)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		SystemPrompt: "Fix it.",
		UserPrompt:   "Bug.",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	require.NotNil(t, result)
	close(logCh)

	require.Equal(t, 0, result.ExitCode)
	require.InDelta(t, 0.7, result.ConfidenceScore, 0.001)
	require.Equal(t, 900, result.TokenUsage.InputTokens)
	require.Equal(t, 150, result.TokenUsage.OutputTokens)
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
		if l.Level == "output" && strings.Contains(l.Message, "Patched") {
			sawAssistant = true
		}
	}
	require.True(t, sawTool)
	require.True(t, sawAssistant)
}

func TestPiAdapter_Execute_NonZeroExit(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		_, _ = stderr.Write([]byte("pi: provider auth failed"))
		return 2, nil
	}

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		SystemPrompt: "x",
		UserPrompt:   "y",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	require.Equal(t, 2, result.ExitCode)
	require.Contains(t, result.Error, "exited with code 2")
	require.Contains(t, result.Error, "provider auth failed")
}

func TestPiAdapter_Execute_ContinuationUsesUserMessage(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		return 0, nil
	}
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 0, nil
	}

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		Continuation: true,
		UserMessage:  "follow-up instruction",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	close(logCh)

	promptData, ok := provider.Files["/workspace/.143-prompt.md"]
	require.True(t, ok, "continuation should still write the prompt file")
	require.Equal(t, "follow-up instruction", string(promptData),
		"continuation prompt should be the new UserMessage, not empty system/user prompts")
}

func TestPiAdapter_Execute_MissingProvider(t *testing.T) {
	t.Parallel()

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	_, err := adapter.Execute(context.Background(), &agent.Sandbox{ID: "t", WorkDir: "/workspace"}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sandbox provider not found")
}

func TestParsePiStreamLine_NonJSON(t *testing.T) {
	t.Parallel()

	logCh := make(chan agent.LogEntry, 10)
	result := &agent.AgentResult{}
	var summary []string
	var last string

	parsePiStreamLine([]byte(`raw text`), result, logCh, &summary, &last)
	close(logCh)

	logs := drain(logCh)
	require.Len(t, logs, 1)
	require.Equal(t, "output", logs[0].Level)
}
