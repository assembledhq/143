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
			name:    "nil issue",
			input:   &agent.AgentInput{Issue: nil},
			wantErr: true,
		},
		{
			name: "low token mode",
			input: &agent.AgentInput{
				Issue:     &models.Issue{Title: "Bug", Source: models.IssueSourceSentry},
				TokenMode: "low",
			},
			wantToken: lowTokenMax,
		},
		{
			name: "high token mode",
			input: &agent.AgentInput{
				Issue:     &models.Issue{Title: "Bug", Source: models.IssueSourceSentry},
				TokenMode: "high",
			},
			wantToken: highTokenMax,
		},
		{
			name: "default token mode",
			input: &agent.AgentInput{
				Issue: &models.Issue{Title: "Bug"},
			},
			wantToken: lowTokenMax,
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
			claudeOutput: `{"type":"assistant","content":"Analyzing the issue..."}
{"type":"tool_use","tool":"edit_file"}
{"type":"tool_result","result":"done"}
{"type":"result","content":"Fixed the bug."}`,
			claudeExitCode: 0,
			diffOutput:     "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n",
			diffExitCode:   0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 0, result.ExitCode)
				require.Empty(t, result.Error)
				require.Contains(t, result.Diff, "diff --git")
				require.Contains(t, result.Summary, "Analyzing the issue...")
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
				if strings.HasPrefix(cmd, "claude") {
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
			sandbox := &agent.Sandbox{ID: "test-sandbox", WorkDir: "/workspace"}
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
			promptData, exists := provider.Files["/workspace/.143-prompt.md"]
			require.True(t, exists, "prompt file should have been written")
			require.Contains(t, string(promptData), "Fix the bug.")
		})
	}
}

func TestClaudeCodeAdapter_Execute_ExecError(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "claude") {
			return 0, context.DeadlineExceeded
		}
		return 0, nil
	}

	adapter := NewClaudeCodeAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}
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
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}
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
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)

	result, err := adapter.Execute(context.Background(), sandbox, prompt, logCh)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "sandbox provider not found")
}

func TestClaudeCodeAdapter_Execute_ContinuationUsesContinueMode(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "claude") {
			_, _ = stdout.Write([]byte(`{"type":"assistant","content":"continuing the session"}`))
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
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}
	prompt := &agent.AgentPrompt{
		UserMessage:  "Please tighten the guard clause.",
		MaxTokens:    50_000,
		Continuation: true,
	}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.NoError(t, err, "continuation should succeed")
	require.NotNil(t, result, "continuation should return a result")
	require.Contains(t, provider.ExecCalls[0], "--continue", "continuation should use Claude's continue mode")
	_, exists := provider.Files["/workspace/.143-prompt.md"]
	require.False(t, exists, "continuation should not write a fresh prompt file")
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
			name: "assistant events build summary",
			output: `{"type":"assistant","content":"Investigating..."}
{"type":"assistant","content":"Found the bug."}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "Investigating...")
				require.Contains(t, result.Summary, "Found the bug.")
			},
		},
		{
			name:   "tool_use event",
			output: `{"type":"tool_use","tool":"edit_file"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "tool_use", logs[0].Level)
				require.Contains(t, logs[0].Message, "edit_file")
			},
		},
		{
			name:   "tool_result event",
			output: `{"type":"tool_result","result":"file updated"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "output", logs[0].Level)
				require.Equal(t, "tool_result", logs[0].Metadata["type"])
			},
		},
		{
			name:   "error event with message field",
			output: `{"type":"error","message":"rate limit exceeded"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "error", logs[0].Level)
				require.Contains(t, logs[0].Message, "rate limit")
			},
		},
		{
			name:   "error event with content fallback",
			output: `{"type":"error","content":"something failed"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "error", logs[0].Level)
				require.Contains(t, logs[0].Message, "something failed")
			},
		},
		{
			name:   "result event with token usage",
			output: `{"type":"result","content":"Done.","result":{"input_tokens":1000,"output_tokens":500}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "Done.")
				require.Equal(t, 1000, result.TokenUsage.InputTokens)
				require.Equal(t, 500, result.TokenUsage.OutputTokens)
			},
		},
		{
			name:   "result event with confidence",
			output: `{"type":"result","content":"Fixed.\n{\"confidence_score\": 0.95, \"confidence_reasoning\": \"Straightforward\", \"risk_factors\": [\"none\"]}"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.InDelta(t, 0.95, result.ConfidenceScore, 0.001)
				require.Equal(t, "Straightforward", result.ConfidenceReasoning)
			},
		},
		{
			name:   "unknown event type logged as debug",
			output: `{"type":"unknown_type","content":"blah"}`,
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
// buildSystemPrompt
// ---------------------------------------------------------------------------

func TestBuildSystemPrompt_IncludesPMContext(t *testing.T) {
	t.Parallel()

	issue := &models.Issue{
		Title: "Test issue",
	}
	input := &agent.AgentInput{
		Issue: issue,
		PMContext: &agent.PMTaskContext{
			Approach:      "Check handlers/billing.go:42",
			Risk:          "Be careful with retries",
			Reasoning:     "High impact",
			RelatedIssues: []string{"Payment timeout"},
			RootCause:     "Missing nil check",
		},
	}

	prompt := buildSystemPrompt(input)
	require.Contains(t, prompt, "Product Manager Analysis", "system prompt should include PM context header")
	require.Contains(t, prompt, "High impact", "system prompt should include PM reasoning")
	require.Contains(t, prompt, "Check handlers/billing.go:42", "system prompt should include PM approach")
	require.Contains(t, prompt, "Missing nil check", "system prompt should include PM root cause")
}

func TestBuildSystemPrompt_IncludesRevisionContext(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue: &models.Issue{Title: "Bug"},
		RevisionContext: &agent.RevisionContext{
			FormattedFeedback: "Please handle the edge case.",
			CommentSummary:    "Missing nil check in handler",
			PreviousDiff:      "--- a/main.go\n+++ b/main.go",
		},
	}

	prompt := buildSystemPrompt(input)
	require.Contains(t, prompt, "Revision Instructions")
	require.Contains(t, prompt, "REVISION run")
	require.Contains(t, prompt, "Please handle the edge case.")
	require.Contains(t, prompt, "Missing nil check in handler")
	require.Contains(t, prompt, "--- a/main.go")
}

func TestBuildSystemPrompt_IncludesContextDocs(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue:       &models.Issue{Title: "Bug"},
		ContextDocs: []string{"Use Go 1.22", "Run tests with make test"},
	}

	prompt := buildSystemPrompt(input)
	require.Contains(t, prompt, "Repository Conventions")
	require.Contains(t, prompt, "Use Go 1.22")
	require.Contains(t, prompt, "Run tests with make test")
}

func TestBuildSystemPrompt_Minimal(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue: &models.Issue{Title: "Bug"},
	}

	prompt := buildSystemPrompt(input)
	require.NotEmpty(t, prompt)
	require.NotContains(t, prompt, "Revision Instructions")
	require.NotContains(t, prompt, "Product Manager Analysis")
	require.NotContains(t, prompt, "Repository Conventions")
}

func TestBuildSystemPrompt_ManualSessionSkipsBaseTemplate(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue:       &models.Issue{Title: "help me refactor", Source: models.IssueSourceManual},
		ContextDocs: []string{"Use Go 1.22"},
	}

	prompt := buildSystemPrompt(input)
	require.NotContains(t, prompt, "coding agent tasked with fixing a bug", "manual sessions should not include bug-fixing template")
	require.NotContains(t, prompt, "testing_requirements", "manual sessions should not include testing requirements")
	require.NotContains(t, prompt, "confidence_score", "manual sessions should not include confidence format")
	require.Contains(t, prompt, "Repository Conventions", "manual sessions should still include repo conventions")
	require.Contains(t, prompt, "Use Go 1.22")
}

// ---------------------------------------------------------------------------
// buildUserPrompt
// ---------------------------------------------------------------------------

func TestBuildUserPrompt(t *testing.T) {
	t.Parallel()

	desc := "Users see 500 errors on /api/billing"

	tests := []struct {
		name        string
		input       *agent.AgentInput
		wantStrings []string
	}{
		{
			name: "basic issue with description",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:       "Billing crash",
					Source:      models.IssueSourceSentry,
					Description: &desc,
				},
			},
			wantStrings: []string{"Billing crash", "500 errors"},
		},
		{
			name: "sentry issue with stack trace",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "NullPointer",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"type": "TypeError",
									"value": "null is not an object",
									"stacktrace": {
										"frames": [{
											"filename": "app.js",
											"function": "handleRequest",
											"lineNo": 42
										}]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantStrings: []string{"Stack Trace", "TypeError", "handleRequest"},
		},
		{
			name: "customer impact",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:                 "Error",
					Source:                models.IssueSourceSentry,
					OccurrenceCount:       150,
					AffectedCustomerCount: 25,
				},
			},
			wantStrings: []string{"Customer Impact", "150", "25"},
		},
		{
			name: "severity",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:    "Error",
					Source:   models.IssueSourceSentry,
					Severity: "critical",
				},
			},
			wantStrings: []string{"critical"},
		},
		{
			name: "complexity estimate",
			input: &agent.AgentInput{
				Issue: &models.Issue{Title: "Error", Source: models.IssueSourceLinear},
				ComplexityEstimate: &agent.ComplexityEstimate{
					Tier:      2,
					Reasoning: "Multiple files affected",
				},
			},
			wantStrings: []string{"Complexity Assessment", "Tier: 2", "Multiple files affected"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			prompt := buildUserPrompt(tt.input)
			for _, s := range tt.wantStrings {
				require.Contains(t, prompt, s)
			}
		})
	}
}

func TestBuildUserPrompt_ManualSessionReturnsRawMessage(t *testing.T) {
	t.Parallel()

	msg := "help me improve the margins in the session"
	input := &agent.AgentInput{
		Issue: &models.Issue{
			Title:       "help me improve the margins",
			Source:      models.IssueSourceManual,
			Description: &msg,
		},
	}

	prompt := buildUserPrompt(input)
	require.Equal(t, msg, prompt, "manual session should return raw user message")
	require.NotContains(t, prompt, "## Issue:")
	require.NotContains(t, prompt, "### Description")
	require.NotContains(t, prompt, "Customer Impact")
	require.NotContains(t, prompt, "Severity")
}

// ---------------------------------------------------------------------------
// extractFileHints
// ---------------------------------------------------------------------------

func TestExtractFileHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     *agent.AgentInput
		wantFiles []string
		wantNil   bool
	}{
		{
			name: "non-sentry source returns nil",
			input: &agent.AgentInput{
				Issue: &models.Issue{Title: "Bug", Source: models.IssueSourceLinear},
			},
			wantNil: true,
		},
		{
			name: "empty raw data returns nil",
			input: &agent.AgentInput{
				Issue: &models.Issue{Title: "Bug", Source: models.IssueSourceSentry, RawData: nil},
			},
			wantNil: true,
		},
		{
			name: "extracts filenames from sentry frames",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [
											{"filename": "src/handler.go", "absPath": ""},
											{"filename": "src/service.go", "absPath": "/app/src/service.go"}
										]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantFiles: []string{"src/handler.go", "/app/src/service.go"},
		},
		{
			name: "skips standard lib and vendor frames",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [
											{"filename": "<frozen importlib>"},
											{"filename": "node_modules/express/lib/router.js"},
											{"filename": "site-packages/django/core/handlers.py"},
											{"filename": "src/app.go"}
										]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantFiles: []string{"src/app.go"},
		},
		{
			name: "deduplicates paths",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [
											{"filename": "src/app.go"},
											{"filename": "src/app.go"}
										]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantFiles: []string{"src/app.go"},
		},
		{
			name: "skips non-exception entries",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "breadcrumbs",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [{"filename": "should_not_appear.go"}]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantNil: true,
		},
		{
			name: "invalid JSON returns nil",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:   "Bug",
					Source:  models.IssueSourceSentry,
					RawData: json.RawMessage(`{not valid json`),
				},
			},
			wantNil: true,
		},
		{
			name: "absPath preferred over filename",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [
											{"filename": "handler.go", "absPath": "/app/src/handler.go"}
										]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantFiles: []string{"/app/src/handler.go"},
		},
		{
			name: "skips frames with empty path",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [
											{"filename": "", "absPath": ""},
											{"filename": "src/real.go"}
										]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantFiles: []string{"src/real.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			files := extractFileHints(tt.input)
			if tt.wantNil {
				require.Nil(t, files)
			} else {
				require.Equal(t, tt.wantFiles, files)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// extractStackTrace
// ---------------------------------------------------------------------------

func TestExtractStackTrace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawData json.RawMessage
		want    []string // substrings expected in result
		wantNil bool
	}{
		{
			name:    "empty raw data",
			rawData: nil,
			wantNil: true,
		},
		{
			name:    "invalid JSON",
			rawData: json.RawMessage(`{broken`),
			wantNil: true,
		},
		{
			name: "valid sentry data",
			rawData: json.RawMessage(`{
				"entries": [{
					"type": "exception",
					"data": {
						"values": [{
							"type": "TypeError",
							"value": "null is not an object",
							"stacktrace": {
								"frames": [{
									"filename": "app.js",
									"function": "handleRequest",
									"lineNo": 42
								},{
									"filename": "router.js",
									"function": "dispatch",
									"lineNo": 100
								}]
							}
						}]
					}
				}]
			}`),
			want: []string{"TypeError: null is not an object", "handleRequest", "app.js:42", "dispatch", "router.js:100"},
		},
		{
			name: "skips non-exception entries",
			rawData: json.RawMessage(`{
				"entries": [{
					"type": "breadcrumbs",
					"data": {"values": []}
				}]
			}`),
			wantNil: true,
		},
		{
			name: "multiple exception values",
			rawData: json.RawMessage(`{
				"entries": [{
					"type": "exception",
					"data": {
						"values": [
							{
								"type": "RootError",
								"value": "connection refused",
								"stacktrace": {"frames": [{"filename": "db.go", "function": "connect", "lineNo": 10}]}
							},
							{
								"type": "WrapperError",
								"value": "init failed",
								"stacktrace": {"frames": [{"filename": "main.go", "function": "init", "lineNo": 5}]}
							}
						]
					}
				}]
			}`),
			want: []string{"RootError: connection refused", "WrapperError: init failed", "db.go:10", "main.go:5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := extractStackTrace(tt.rawData)
			if tt.wantNil {
				require.Empty(t, result)
				return
			}
			for _, s := range tt.want {
				require.Contains(t, result, s)
			}
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
		name         string
		isGitRepo    bool // whether git rev-parse returns 0
		diffStdout   string
		diffExitCode int
		execErr      error
		wantDiff     string
		wantErr      bool
	}{
		{
			name:       "successful diff",
			isGitRepo:  true,
			diffStdout: "diff --git a/f.go b/f.go\n+fixed\n",
			wantDiff:   "diff --git a/f.go b/f.go\n+fixed\n",
		},
		{
			name:         "non-zero diff exit code",
			isGitRepo:    true,
			diffExitCode: 1,
			wantErr:      true,
		},
		{
			name:    "exec error on rev-parse",
			execErr: context.DeadlineExceeded,
			wantErr: true,
		},
		{
			name:      "not a git repo returns empty diff",
			isGitRepo: false,
			wantDiff:  "",
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
				if strings.HasPrefix(cmd, "git diff") {
					_, _ = stdout.Write([]byte(tt.diffStdout))
					return tt.diffExitCode, nil
				}
				return 0, nil
			}

			sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}
			diff, err := collectDiff(context.Background(), provider, sandbox)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantDiff, diff)
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
