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
	require.Equal(t, models.AgentTypeGeminiCLI, adapter.Name(), "adapter name should be gemini_cli")
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
				Issue:     newTestIssue(models.IssueSourceSentry, true),
				TokenMode: "low",
			},
			expectedMaxTokens: 50_000,
		},
		{
			name: "high token mode",
			input: &agent.AgentInput{
				Issue:     newTestIssue(models.IssueSourceLinear, true),
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
			name: "nil issue still prepares prompt",
			input: &agent.AgentInput{
				Issue: nil,
			},
			expectedMaxTokens: 50_000,
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

func newTestIssue(source models.IssueSource, hasDescription bool) *models.Issue {
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

			adapter := NewGeminiCLIAdapter(zerolog.Nop())
			sandbox := &agent.Sandbox{
				ID:       "test-sandbox",
				WorkDir:  "/workspace",
				HomeDir:  "/home/sandbox",
				Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"},
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
			promptData, exists := provider.Files["/home/sandbox/.143-prompt.md"]
			require.True(t, exists, "prompt file should have been written")
			require.Contains(t, string(promptData), "Fix the bug.", "prompt file should contain system prompt")
		})
	}
}

func TestGeminiCLIAdapter_Execute_WriteFileError(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.WriteFileFn = func(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
		return context.DeadlineExceeded
	}

	adapter := NewGeminiCLIAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "write prompt file")
}

func TestGeminiCLIAdapter_Execute_ExecError(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "gemini") {
			return 0, context.DeadlineExceeded
		}
		return 0, nil
	}

	adapter := NewGeminiCLIAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "exec gemini CLI")
}

func TestGeminiCLIAdapter_Execute_MissingSandboxProvider(t *testing.T) {
	t.Parallel()

	adapter := NewGeminiCLIAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
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

func TestParseGeminiStreamOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      string
		checkResult func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry)
	}{
		{
			name: "streaming output with tool use",
			output: `{"type":"text","content":"Let me look at the code..."}
{"type":"tool_call","tool":"read_file","input":{"path":"main.go"}}
{"type":"tool_result","tool":"read_file","output":"package main\nfunc main() {}"}
{"type":"text","content":"Found the issue. Fixing now."}
{"type":"tool_call","tool":"edit_file","input":{"path":"main.go","old_text":"bad","new_text":"good"}}
{"type":"tool_result","tool":"edit_file","output":"File updated."}
{"type":"text","content":"Fixed the null pointer issue."}
{"type":"usage","stats":{"inputTokens":1200,"outputTokens":350}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				// Assistant text blocks stay as separate logs; summary has last one.
				require.NotContains(t, result.Summary, "Let me look at the code...")
				require.Equal(t, "Fixed the null pointer issue.", result.Summary)
				require.Equal(t, 1200, result.TokenUsage.InputTokens)
				require.Equal(t, 350, result.TokenUsage.OutputTokens)

				toolUseCount := 0
				for _, log := range logs {
					if log.Level == "tool_use" {
						toolUseCount++
						require.NotNil(t, log.Metadata)
						require.NotEmpty(t, log.Metadata["tool"])
						require.NotNil(t, log.Metadata["input"], "tool_use should have input metadata")
					}
				}
				require.Equal(t, 2, toolUseCount, "should have 2 tool_use log entries")
			},
		},
		{
			name: "tool use metadata contains input details",
			output: `{"type":"tool_call","tool":"read_file","input":{"path":"src/handler.go","line_start":10,"line_end":50}}
{"type":"tool_result","tool":"read_file","output":"func handler() {}"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				var toolLog *agent.LogEntry
				for i, log := range logs {
					if log.Level == "tool_use" {
						toolLog = &logs[i]
						break
					}
				}
				require.NotNil(t, toolLog, "should have tool_use log")
				require.Equal(t, "read_file", toolLog.Metadata["tool"])
				inputMap, ok := toolLog.Metadata["input"].(map[string]interface{})
				require.True(t, ok, "input should be a map")
				require.Equal(t, "src/handler.go", inputMap["path"])
			},
		},
		{
			name:   "tool_call with name field instead of tool",
			output: `{"type":"tool_call","name":"shell","input":{"command":"go test ./..."}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "tool_use", logs[0].Level)
				require.Equal(t, "shell", logs[0].Metadata["tool"])
			},
		},
		{
			name:   "falls back to legacy JSON",
			output: `{"response":"Fixed the bug.","stats":{"inputTokens":200,"outputTokens":100}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, "Fixed the bug.", result.Summary)
				require.Equal(t, 200, result.TokenUsage.InputTokens)
				require.Equal(t, 100, result.TokenUsage.OutputTokens)
			},
		},
		{
			name:   "plain text emitted as raw output",
			output: "Just some plain text output",
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				hasOutput := false
				for _, log := range logs {
					if log.Level == "output" && strings.Contains(log.Message, "plain text") {
						hasOutput = true
					}
				}
				require.True(t, hasOutput, "should emit plain text as output")
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
			name: "error event in stream",
			output: `{"type":"text","content":"Starting analysis..."}
{"type":"error","error":"rate limit exceeded"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				hasError := false
				for _, log := range logs {
					if log.Level == "error" && strings.Contains(log.Message, "rate limit") {
						hasError = true
					}
				}
				require.True(t, hasError, "should have error log entry")
			},
		},
		{
			name: "thinking events logged as debug",
			output: `{"type":"thinking","content":"I need to check the imports..."}
{"type":"text","content":"Let me fix the imports."}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				hasThinking := false
				for _, log := range logs {
					if log.Level == "debug" && log.Metadata != nil && log.Metadata["type"] == "thinking" {
						hasThinking = true
					}
				}
				require.True(t, hasThinking, "should have thinking debug log")
			},
		},
		{
			name:   "confidence extraction from stream",
			output: `{"type":"text","content":"Fixed it.\n{\"confidence_score\": 0.85, \"confidence_reasoning\": \"Simple fix\", \"risk_factors\": [\"edge case\"]}"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.InDelta(t, 0.85, result.ConfidenceScore, 0.001)
				require.Equal(t, "Simple fix", result.ConfidenceReasoning)
				require.Equal(t, []string{"edge case"}, result.RiskFactors)
			},
		},
		{
			name:   "tool_result with name field",
			output: `{"type":"tool_result","name":"shell","output":"PASS"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "output", logs[0].Level)
				require.Equal(t, "shell", logs[0].Metadata["tool"])
			},
		},
		{
			name:   "tool_result with result field fallback",
			output: `{"type":"tool_result","tool":"read","result":"file data"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Contains(t, logs[0].Message, "file data")
			},
		},
		{
			name:   "tool_use event variant",
			output: `{"type":"tool_use","tool":"write_file","input":{"path":"f.go","content":"package main"}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "tool_use", logs[0].Level)
				require.Equal(t, "write_file", logs[0].Metadata["tool"])
			},
		},
		{
			name:   "result event with Result JSON token usage",
			output: `{"type":"result","content":"Complete.","result":{"input_tokens":1500,"output_tokens":600}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "Complete.")
				require.Equal(t, 1500, result.TokenUsage.InputTokens)
				require.Equal(t, 600, result.TokenUsage.OutputTokens)
			},
		},
		{
			name:   "error event with message fallback",
			output: `{"type":"error","message":"timeout"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "error", logs[0].Level)
				require.Contains(t, logs[0].Message, "timeout")
			},
		},
		{
			name:   "error event with content fallback",
			output: `{"type":"error","content":"crashed"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "error", logs[0].Level)
				require.Contains(t, logs[0].Message, "crashed")
			},
		},
		{
			name:   "unknown event type",
			output: `{"type":"custom_event","content":"data"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "debug", logs[0].Level)
			},
		},
		{
			name:   "non-JSON line in stream",
			output: "raw text not JSON",
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "output", logs[0].Level)
				require.Contains(t, result.Summary, "raw text")
			},
		},
		{
			name:   "assistant event type variant",
			output: `{"type":"assistant","message":"Using message field."}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				// Falls back to last assistant text when no result event.
				require.Equal(t, "Using message field.", result.Summary)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := &agent.AgentResult{}
			logCh := make(chan agent.LogEntry, 100)

			parseGeminiStreamOutput([]byte(tt.output), result, logCh)
			close(logCh)

			var logs []agent.LogEntry
			for entry := range logCh {
				logs = append(logs, entry)
			}

			tt.checkResult(t, result, logs)
		})
	}
}

func TestGeminiCLIAdapter_Execute_StreamingOutput(t *testing.T) {
	t.Parallel()

	streamOutput := `{"type":"text","content":"Analyzing the code..."}
{"type":"tool_call","tool":"read_file","input":{"path":"main.go"}}
{"type":"tool_result","tool":"read_file","output":"package main"}
{"type":"text","content":"Applied the fix."}
{"type":"usage","stats":{"inputTokens":800,"outputTokens":200}}`

	provider := newMockProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "gemini") {
			_, _ = stdout.Write([]byte(streamOutput))
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

	adapter := NewGeminiCLIAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test-sandbox", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{SystemPrompt: "Fix the bug.", UserPrompt: "Null pointer error.", MaxTokens: 50_000}

	logCh := make(chan agent.LogEntry, 100)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 0, result.ExitCode)
	require.Contains(t, result.Diff, "diff --git")
	require.Equal(t, 800, result.TokenUsage.InputTokens)

	close(logCh)
	var logs []agent.LogEntry
	for entry := range logCh {
		logs = append(logs, entry)
	}

	// Should have tool_use entries from streaming output.
	toolUseCount := 0
	for _, log := range logs {
		if log.Level == "tool_use" {
			toolUseCount++
		}
	}
	require.Equal(t, 1, toolUseCount, "should have 1 tool_use log entry")
}

func TestGeminiCLIAdapter_Execute_ContinuationWithoutSessionIDFallsBackToFreshExec(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "gemini") {
			_, _ = stdout.Write([]byte(`{"type":"text","content":"continuing gemini session"}`))
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

	adapter := NewGeminiCLIAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{
		SystemPrompt: "system",
		UserPrompt:   "history-embedded user prompt",
		UserMessage:  "Please include a regression test.",
		MaxTokens:    50_000,
		Continuation: true,
	}

	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.NoError(t, err, "continuation should succeed when falling back to fresh exec")
	require.NotNil(t, result, "continuation should return a result")
	require.NotContains(t, provider.ExecCalls[0], "--resume", "continuation without a session ID must not use --resume (latest is non-deterministic)")
	contents, exists := provider.Files["/home/sandbox/.143-prompt.md"]
	require.True(t, exists, "fresh exec must write the system+user prompt to a file")
	require.Contains(t, string(contents), "history-embedded user prompt", "prompt file should carry the orchestrator-provided history-embedded user prompt")
}

func TestGeminiCLIAdapter_ParseStreamLine_CapturesSessionID(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 4)
	var summaryParts []string
	lastAssistant := ""

	parseGeminiStreamLine(
		[]byte(`{"type":"result","content":"done","session_id":"gemini-session-xyz"}`),
		result,
		logCh,
		&summaryParts,
		&lastAssistant,
	)

	require.Equal(t, "gemini-session-xyz", result.AgentSessionID, "session_id on any event should populate AgentSessionID for downstream resume")
}

func TestGeminiCLIAdapter_ParseStreamLine_CapturesCamelCaseSessionIDOnLifecycleEvent(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 4)
	var summaryParts []string
	lastAssistant := ""

	// Older / different Gemini stream shapes emit session id under
	// `sessionId` (camelCase) on a session-lifecycle event; capture must
	// not depend on the exact event type or the exact field spelling.
	parseGeminiStreamLine(
		[]byte(`{"type":"session_started","sessionId":"gemini-session-camel"}`),
		result,
		logCh,
		&summaryParts,
		&lastAssistant,
	)

	require.Equal(t, "gemini-session-camel", result.AgentSessionID, "camelCase sessionId on a lifecycle event must populate AgentSessionID")
}

func TestGeminiCLIAdapter_ResumeMode(t *testing.T) {
	t.Parallel()

	adapter := NewGeminiCLIAdapter(zerolog.Nop())
	require.Equal(t, agent.ResumeBySessionID, adapter.ResumeMode())
}
