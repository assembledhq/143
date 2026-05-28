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

func TestAmpAdapter_Name(t *testing.T) {
	t.Parallel()
	adapter := NewAmpAdapter(zerolog.Nop())
	require.Equal(t, models.AgentTypeAmp, adapter.Name(), "adapter name should be amp")
}

func TestAmpAdapter_ResumeMode(t *testing.T) {
	t.Parallel()

	adapter := NewAmpAdapter(zerolog.Nop())
	require.Equal(t, agent.ResumeBySessionID, adapter.ResumeMode(), "amp should support deterministic continuation by thread id")
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

	prompt, err = adapter.PreparePrompt(context.Background(), &agent.AgentInput{Issue: nil})
	require.NoError(t, err, "issue-less sessions should still prepare prompts")
	require.NotNil(t, prompt, "issue-less sessions should still return a prompt")
	require.NotEmpty(t, prompt.UserPrompt, "issue-less sessions should still include a user prompt")
}

func TestAmpAdapter_Execute_StreamJSON(t *testing.T) {
	t.Parallel()

	streamOutput := `{"type":"assistant","content":"Looking at the bug now."}
{"type":"tool_use","tool":"read_file","input":{"path":"main.go"}}
{"type":"tool_result","tool":"read_file","output":"file contents"}
{"type":"result","content":"Done","usage":{"input_tokens":1200,"output_tokens":340},"session_id":"amp-sess-1"}
`

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		require.NotContains(t, cmd, ".143-agent.pid", "amp adapter must not embed pidfile scaffolding (provider internal)")
		require.NotContains(t, cmd, "& pid=$!", "amp adapter must not embed shell-shim wrapping (provider internal)")
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

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "Fix it.",
		UserPrompt:   "Null pointer.",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	require.NotNil(t, result)
	close(logCh)

	require.Equal(t, 0, result.ExitCode)
	require.Equal(t, 1200, result.TokenUsage.InputTokens)
	require.Equal(t, 340, result.TokenUsage.OutputTokens)
	require.Equal(t, "amp-sess-1", result.AgentSessionID,
		"amp should capture session_id from the result event")
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
		if l.Level == "output" && strings.Contains(l.Message, "Looking at the bug") {
			sawAssistant = true
		}
	}
	require.True(t, sawTool, "expected tool_use log entry")
	require.True(t, sawAssistant, "expected assistant content log entry")

	promptData, ok := provider.Files["/home/sandbox/.143-prompt.md"]
	require.True(t, ok, "prompt file should be written under HomeDir")
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

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "x",
		UserPrompt:   "y",
		MaxTokens:    50_000,
	}, logCh)
	require.NoError(t, err)
	require.Equal(t, 1, result.ExitCode)
	require.Contains(t, result.Error, "exited with code 1")
	require.Contains(t, result.Error, "invalid api key")
}

func TestAmpAdapter_Execute_ContinuationResumesThread(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecStreamFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
		require.NotContains(t, cmd, ".143-agent.pid", "amp continuation must not embed pidfile scaffolding (provider internal)")
		require.NotContains(t, cmd, "& pid=$!", "amp continuation must not embed shell-shim wrapping (provider internal)")
		require.Contains(t, cmd, "amp threads continue 'amp-thread-123'", "amp continuation should resume the upstream thread")
		require.Contains(t, cmd, "-x \"$(cat '/home/sandbox/.143-prompt.md')\"", "amp continuation should pass the follow-up prompt file")
		return 0, nil
	}
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 0, nil
	}

	adapter := NewAmpAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		Continuation:    true,
		ResumeSessionID: "amp-thread-123",
		UserMessage:     "please continue where we left off",
		MaxTokens:       50_000,
	}, logCh)
	require.NoError(t, err)
	close(logCh)

	promptData, ok := provider.Files["/home/sandbox/.143-prompt.md"]
	require.True(t, ok, "continuation should still write the prompt file under HomeDir")
	require.Equal(t, "please continue where we left off", string(promptData),
		"continuation prompt should be the new UserMessage, not empty system/user prompts")
}

func TestAmpAdapter_Execute_AppendsHumanInputAnswer(t *testing.T) {
	t.Parallel()

	answerText := "Use option B."
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

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		Continuation: true,
		UserMessage:  "the agent requested input",
		MaxTokens:    50_000,
		HumanInputAnswer: &agent.HumanInputAnswer{
			AnswerText:        &answerText,
			SelectedChoiceIDs: []string{"choice-b"},
		},
	}, logCh)
	require.NoError(t, err, "Execute should append normalized human input answers")

	promptData, ok := provider.Files["/home/sandbox/.143-prompt.md"]
	require.True(t, ok, "prompt file should be written")
	require.Contains(t, string(promptData), "Human input answer", "prompt should include a structured answer section")
	require.Contains(t, string(promptData), "Use option B.", "prompt should include the answer text")
	require.Contains(t, string(promptData), "choice-b", "prompt should include selected choices")
}

func TestAmpAdapter_Execute_MissingProvider(t *testing.T) {
	t.Parallel()

	adapter := NewAmpAdapter(zerolog.Nop())
	logCh := make(chan agent.LogEntry, 10)
	_, err := adapter.Execute(context.Background(), &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sandbox provider not found")
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

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
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

	_, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
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

	result, err := adapter.Execute(ctx, &agent.Sandbox{ID: "t", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}, &agent.AgentPrompt{
		SystemPrompt: "x", UserPrompt: "y", MaxTokens: 50_000,
	}, logCh)
	require.NoError(t, err)
	require.Equal(t, "done, no result event emitted", result.Summary)
}

// TestAmpAdapter_ShellModeResolution mirrors Pi's injection-safety test at the
// bash level: AMP_MODE expansion inside the double-quoted `-m` argument must
// fall back to the hardcoded default when unset, pick up the env var when set,
// and preserve shell metacharacters literally rather than re-parsing them.
func TestAmpAdapter_ShellModeResolution(t *testing.T) {
	t.Parallel()

	const expr = `echo "${AMP_MODE:-smart}"`

	tests := []struct {
		name string
		env  []string
		want string
	}{
		{
			name: "falls back to default when unset",
			env:  nil,
			want: "smart",
		},
		{
			name: "uses AMP_MODE when set",
			env:  []string{"AMP_MODE=deep"},
			want: "deep",
		},
		{
			name: "injection attempt stays inside the argument",
			env:  []string{`AMP_MODE=foo"; rm -rf / ; echo "`},
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
