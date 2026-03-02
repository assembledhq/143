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
	"github.com/assembledhq/143/internal/testutil"
)

func TestGeminiCLIAdapter_Name(t *testing.T) {
	t.Parallel()
	adapter := NewGeminiCLIAdapter(zerolog.Nop())
	require.Equal(t, "gemini_cli", adapter.Name(), "adapter name should be gemini_cli")
}

func TestGeminiCLIAdapter_PreparePrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		input             *agent.AgentInput
		expectErr         bool
		expectedMaxTokens int
	}{
		{
			name: "low token mode",
			input: &agent.AgentInput{
				Issue:     newTestIssue("sentry", true),
				TokenMode: "low",
			},
			expectedMaxTokens: 50_000,
		},
		{
			name: "high token mode",
			input: &agent.AgentInput{
				Issue:     newTestIssue("linear", true),
				TokenMode: "high",
			},
			expectedMaxTokens: 200_000,
		},
		{
			name:      "nil input returns error",
			input:     nil,
			expectErr: true,
		},
		{
			name: "nil issue returns error",
			input: &agent.AgentInput{
				Issue: nil,
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			adapter := NewGeminiCLIAdapter(zerolog.Nop())
			prompt, err := adapter.PreparePrompt(context.Background(), tt.input)

			if tt.expectErr {
				require.Error(t, err, "PreparePrompt should return an error")
				return
			}

			require.NoError(t, err, "PreparePrompt should not return an error")
			require.NotNil(t, prompt, "prompt should not be nil")
			require.Equal(t, tt.expectedMaxTokens, prompt.MaxTokens, "MaxTokens should match token mode")
			require.NotEmpty(t, prompt.SystemPrompt, "system prompt should not be empty")
			require.NotEmpty(t, prompt.UserPrompt, "user prompt should not be empty")
		})
	}
}

func newTestIssue(source string, hasDescription bool) *models.Issue {
	issue := &models.Issue{
		Source: source,
		Title:  "Test issue",
	}
	if hasDescription {
		desc := "Test description"
		issue.Description = &desc
	}
	return issue
}

func newMockProvider() *testutil.MockSandboxProvider {
	return testutil.NewMockSandboxProvider()
}

func TestGeminiCLIAdapter_Execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		geminiOutput   string
		geminiExitCode int
		diffOutput     string
		diffExitCode   int
		expectErr      bool
		checkResult    func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry)
	}{
		{
			name:           "successful run with JSON output",
			geminiOutput:   `{"response":"I fixed the null pointer issue.\n{\"confidence_score\": 0.9, \"confidence_reasoning\": \"Simple null check\", \"risk_factors\": [\"edge case\"]}","stats":{"inputTokens":1000,"outputTokens":500}}`,
			geminiExitCode: 0,
			diffOutput:     "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-bad\n+good\n",
			diffExitCode:   0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 0, result.ExitCode)
				require.Empty(t, result.Error)
				require.Contains(t, result.Diff, "diff --git")
				require.InDelta(t, 0.9, result.ConfidenceScore, 0.001)
				require.Equal(t, "Simple null check", result.ConfidenceReasoning)
				require.Equal(t, []string{"edge case"}, result.RiskFactors)
				require.Equal(t, 1000, result.TokenUsage.InputTokens)
				require.Equal(t, 500, result.TokenUsage.OutputTokens)
			},
		},
		{
			name:           "JSON output with error field",
			geminiOutput:   `{"response":"Partial fix applied.","error":"rate limit warning"}`,
			geminiExitCode: 0,
			diffOutput:     "",
			diffExitCode:   0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 0, result.ExitCode)
				require.Contains(t, result.Summary, "Partial fix applied.")
				hasError := false
				for _, log := range logs {
					if log.Level == "error" && strings.Contains(log.Message, "rate limit warning") {
						hasError = true
					}
				}
				require.True(t, hasError, "logs should contain error entry from JSON error field")
			},
		},
		{
			name:           "non-zero exit code",
			geminiOutput:   `plain text error output`,
			geminiExitCode: 1,
			diffOutput:     "",
			diffExitCode:   0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 1, result.ExitCode)
				require.Contains(t, result.Error, "exited with code 1")
			},
		},
		{
			name:           "empty output",
			geminiOutput:   "",
			geminiExitCode: 0,
			diffOutput:     "",
			diffExitCode:   0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 0, result.ExitCode)
				require.Empty(t, result.Diff)
				require.Equal(t, 0.0, result.ConfidenceScore)
			},
		},
		{
			name:           "plain text fallback",
			geminiOutput:   "I analyzed the code and fixed the bug by adding a null check.",
			geminiExitCode: 0,
			diffOutput:     "diff --git a/f.go b/f.go\n",
			diffExitCode:   0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 0, result.ExitCode)
				require.Contains(t, result.Summary, "null check")
				hasOutput := false
				for _, log := range logs {
					if log.Level == "output" {
						hasOutput = true
					}
				}
				require.True(t, hasOutput, "plain text should be emitted as output log")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := newMockProvider()
			provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				if strings.HasPrefix(cmd, "gemini") {
					_, _ = stdout.Write([]byte(tt.geminiOutput))
					return tt.geminiExitCode, nil
				}
				if strings.HasPrefix(cmd, "git diff") {
					_, _ = stdout.Write([]byte(tt.diffOutput))
					return tt.diffExitCode, nil
				}
				return 0, nil
			}

			adapter := NewGeminiCLIAdapter(zerolog.Nop())
			sandbox := &agent.Sandbox{
				ID:      "test-sandbox",
				WorkDir: "/workspace",
			}
			prompt := &agent.AgentPrompt{
				SystemPrompt: "Fix the bug.",
				UserPrompt:   "There is a null pointer error.",
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
			promptData, exists := provider.Files["/workspace/.143-prompt.md"]
			require.True(t, exists, "prompt file should have been written")
			require.Contains(t, string(promptData), "Fix the bug.", "prompt file should contain system prompt")
		})
	}
}

func TestGeminiCLIAdapter_Execute_MissingSandboxProvider(t *testing.T) {
	t.Parallel()

	adapter := NewGeminiCLIAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)

	result, err := adapter.Execute(context.Background(), sandbox, prompt, logCh)
	require.Error(t, err, "Execute should fail without sandbox provider in context")
	require.Nil(t, result)
	require.Contains(t, err.Error(), "sandbox provider not found")
}

func TestParseGeminiOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      string
		checkResult func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry)
	}{
		{
			name:   "valid JSON response",
			output: `{"response":"Fixed the bug.","stats":{"inputTokens":200,"outputTokens":100}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, "Fixed the bug.", result.Summary)
				require.Equal(t, 200, result.TokenUsage.InputTokens)
				require.Equal(t, 100, result.TokenUsage.OutputTokens)
			},
		},
		{
			name:   "plain text fallback",
			output: "Some plain text output from gemini",
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, "Some plain text output from gemini", result.Summary)
			},
		},
		{
			name:   "empty output",
			output: "",
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Empty(t, result.Summary)
			},
		},
		{
			name:   "whitespace only",
			output: "   \n\n  ",
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Empty(t, result.Summary)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := &agent.AgentResult{}
			logCh := make(chan agent.LogEntry, 50)

			parseGeminiOutput([]byte(tt.output), result, logCh)
			close(logCh)

			var logs []agent.LogEntry
			for entry := range logCh {
				logs = append(logs, entry)
			}

			tt.checkResult(t, result, logs)
		})
	}
}

func TestShellEscapeGemini(t *testing.T) {
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
			require.Equal(t, tt.expected, shellEscapeGemini(tt.input))
		})
	}
}
