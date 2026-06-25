package adapters

import (
	"context"
	"fmt"
	"io"
	"os/exec"
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

func TestPiAdapter_ResumeMode(t *testing.T) {
	t.Parallel()

	adapter := NewPiAdapter(zerolog.Nop())
	require.Equal(t, agent.ResumeBySessionID, adapter.ResumeMode(), "pi should support deterministic continuation by session id")
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

	streamOutput := `{"type":"session","session_id":"pi-session-1"}
{"type":"assistant","content":"Reading the file."}
{"type":"tool_use","name":"read","input":{"path":"x.go"},"model":"anthropic/claude-sonnet-4-6"}
{"type":"tool_result","name":"read","output":"contents"}
{"type":"done","content":"complete","usage":{"input_tokens":900,"output_tokens":150}}
`

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		// Adapters no longer hand-roll PTY/pidfile glue — that lives in the
		// provider-internal interactive handle. The adapter command is the
		// raw CLI invocation only.
		require.NotContains(t, cmd, ".143-agent.pid", "pi adapter must not embed pidfile scaffolding (provider internal)")
		require.NotContains(t, cmd, ".143-agent.tty", "pi adapter must not embed ttyfile scaffolding (provider internal)")
		require.NotContains(t, cmd, "python3 -c", "pi adapter must not embed PTY shim (provider internal)")
		require.Contains(t, cmd, "--mode json")
		require.Contains(t, cmd, "--api-key")
		require.Contains(t, cmd, "PI_API_KEY", "must inject the dedicated Pi API key")
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

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "Fix it.",
		UserPrompt:   "Bug.",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	require.NotNil(t, result)
	close(logCh)

	require.Equal(t, 0, result.ExitCode)
	require.Equal(t, 900, result.TokenUsage.InputTokens)
	require.Equal(t, 150, result.TokenUsage.OutputTokens)
	require.Equal(t, "pi-session-1", result.AgentSessionID, "pi should capture session_id from the session header")
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
		if l.Level == "output" && strings.Contains(l.Message, "Reading the file") {
			sawAssistant = true
		}
	}
	require.True(t, sawTool)
	require.True(t, sawAssistant)
}

func TestPiAdapter_Execute_NonZeroExit(t *testing.T) {
	t.Parallel()

	// Pi runs under a TTY, so stderr is merged into the visible stdout
	// stream by the transport itself. The adapter is expected to surface
	// the last visible line as the failure detail when the CLI exits
	// non-zero — this test asserts that contract through the new handle
	// transport (no wrapper-side PTY, no separate stderr writer).
	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		onLine([]byte("pi: provider auth failed"))
		return 2, nil
	}

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "x",
		UserPrompt:   "y",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	require.Equal(t, 2, result.ExitCode)
	require.Contains(t, result.Error, "exited with code 2")
	require.Contains(t, result.Error, "provider auth failed")
}

func TestPiAdapter_Execute_NonZeroExit_IncludesMergedOutputWhenStderrEmpty(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		onLine([]byte("pi: provider auth failed"))
		return 2, nil
	}

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "x",
		UserPrompt:   "y",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	require.Equal(t, 2, result.ExitCode)
	require.Contains(t, result.Error, "exited with code 2", "non-zero exits should still surface the exit code")
	require.Contains(t, result.Error, "provider auth failed",
		"non-zero exits should preserve failure detail even when the wrapper only exposes it on the merged output stream")
}

func TestPiAdapter_Execute_ContinuationResumesSession(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		require.NotContains(t, cmd, ".143-agent.pid", "pi continuation must not embed pidfile scaffolding (provider internal)")
		require.NotContains(t, cmd, ".143-agent.tty", "pi continuation must not embed ttyfile scaffolding (provider internal)")
		require.NotContains(t, cmd, "python3 -c", "pi continuation must not embed PTY shim (provider internal)")
		require.Contains(t, cmd, "--session 'pi-session-123'", "pi continuation should resume the upstream session")
		require.Contains(t, cmd, "-p \"$(cat '/home/sandbox/.143-prompt.md')\"", "pi continuation should pass the follow-up prompt file")
		return 0, nil
	}
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 0, nil
	}

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		Continuation:    true,
		ResumeSessionID: "pi-session-123",
		UserMessage:     "follow-up instruction",
		MaxTokens:       50_000,
	}, logCh)
	require.NoError(t, err)
	close(logCh)

	promptData, ok := provider.Files["/home/sandbox/.143-prompt.md"]
	require.True(t, ok, "continuation should still write the prompt file")
	require.Equal(t, "follow-up instruction", string(promptData),
		"continuation prompt should be the new UserMessage, not empty system/user prompts")
}

func TestPiAdapter_Execute_MissingProvider(t *testing.T) {
	t.Parallel()

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	_, err := adapter.Execute(context.Background(), &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sandbox provider not found")
}

func TestPiAdapter_Execute_WriteFileError(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.WriteFileFn = func(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
		return fmt.Errorf("no space left on device")
	}

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.Error(t, err)
	require.Contains(t, err.Error(), "write prompt file")
}

func TestPiAdapter_Execute_ExecStreamError(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		return 0, fmt.Errorf("pi sandbox exec blew up")
	}

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exec pi CLI")
}

func TestPiAdapter_Execute_SummaryFromAssistantFallback(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		onLine([]byte(`{"type":"assistant","content":"final message, no result event"}`))
		return 0, nil
	}

	adapter := NewPiAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.NoError(t, err)
	require.Equal(t, "final message, no result event", result.Summary)
}

// TestPiAdapter_ShellModelResolution verifies at the bash level that
// PI_MODEL_CUSTOM wins over PI_MODEL wins over the hardcoded fallback, and that
// a value containing shell metacharacters is preserved literally (no injection)
// when expanded inside the command's double-quoted substitution.
func TestPiAdapter_ShellModelResolution(t *testing.T) {
	t.Parallel()

	// Same expansion pattern the adapter emits; isolated so we can drive it
	// through bash with different env var combinations.
	const expr = `echo "${PI_MODEL_CUSTOM:-${PI_MODEL:-anthropic/claude-opus-4-8}}"`

	tests := []struct {
		name string
		env  []string
		want string
	}{
		{
			name: "falls back to default when nothing set",
			env:  nil,
			want: "anthropic/claude-opus-4-8",
		},
		{
			name: "uses PI_MODEL when only PI_MODEL is set",
			env:  []string{"PI_MODEL=openai/gpt-5.4"},
			want: "openai/gpt-5.4",
		},
		{
			name: "PI_MODEL_CUSTOM wins over PI_MODEL",
			env:  []string{"PI_MODEL=openai/gpt-5.4", "PI_MODEL_CUSTOM=moonshot/kimi-k2.6"},
			want: "moonshot/kimi-k2.6",
		},
		{
			name: "injection attempt stays inside the argument",
			env:  []string{`PI_MODEL_CUSTOM=foo"; rm -rf / ; echo "`},
			want: `foo"; rm -rf / ; echo "`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("bash", "-c", expr)
			cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, tc.env...)
			out, err := cmd.Output()
			require.NoError(t, err)
			require.Equal(t, tc.want, strings.TrimRight(string(out), "\n"))
		})
	}
}
