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

func TestCodexAdapter_Name(t *testing.T) {
	a := NewCodexAdapter(zerolog.Nop())
	if a.Name() != models.AgentTypeCodex {
		t.Errorf("expected name 'codex', got %q", a.Name())
	}
}

func TestCodexAdapter_PreparePrompt(t *testing.T) {
	a := NewCodexAdapter(zerolog.Nop())

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
			name: "nil issue",
			input: &agent.AgentInput{
				Issue: nil,
			},
			wantErr: true,
		},
		{
			name: "basic issue low tokens",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:    "NilPointerException in user service",
					Severity: "high",
					Source:   models.IssueSourceSentry,
				},
				TokenMode: "low",
			},
			wantErr:   false,
			wantToken: lowTokenMax,
		},
		{
			name: "high token mode",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:    "Complex refactor needed",
					Severity: "medium",
					Source:   models.IssueSourceSentry,
				},
				TokenMode: "high",
			},
			wantErr:   false,
			wantToken: highTokenMax,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt, err := a.PreparePrompt(context.Background(), tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if prompt.MaxTokens != tt.wantToken {
				t.Errorf("expected %d max tokens, got %d", tt.wantToken, prompt.MaxTokens)
			}
			if prompt.SystemPrompt == "" {
				t.Error("expected non-empty system prompt")
			}
			if prompt.UserPrompt == "" {
				t.Error("expected non-empty user prompt")
			}
		})
	}
}

func TestParseCodexOutput_JSON(t *testing.T) {
	output := []byte(`{"response": "Fixed the null pointer by adding a nil check.", "stats": {"inputTokens": 1500, "outputTokens": 500}}`)

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)

	parseCodexOutput(output, result, logCh)
	close(logCh)

	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if result.TokenUsage.InputTokens != 1500 {
		t.Errorf("expected 1500 input tokens, got %d", result.TokenUsage.InputTokens)
	}
	if result.TokenUsage.OutputTokens != 500 {
		t.Errorf("expected 500 output tokens, got %d", result.TokenUsage.OutputTokens)
	}
}

func TestParseCodexOutput_PlainText(t *testing.T) {
	output := []byte("I fixed the bug by adding a nil check on line 42.")

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)

	parseCodexOutput(output, result, logCh)
	close(logCh)

	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

func TestParseCodexOutput_WithConfidence(t *testing.T) {
	output := []byte(`{"response": "Fixed it.\n\n` + "```json\\n{\\\"confidence_score\\\": 0.85, \\\"confidence_reasoning\\\": \\\"Simple nil check\\\", \\\"risk_factors\\\": [\\\"none\\\"]}\\n```" + `"}`)

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)

	parseCodexOutput(output, result, logCh)
	close(logCh)

	if result.ConfidenceScore != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", result.ConfidenceScore)
	}
}

func TestParseCodexOutput_Empty(t *testing.T) {
	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)

	parseCodexOutput([]byte(""), result, logCh)
	close(logCh)

	if result.Summary != "" {
		t.Errorf("expected empty summary, got %q", result.Summary)
	}
}

func TestParseCodexOutput_JSONWithError(t *testing.T) {
	output := []byte(`{"response": "Attempted fix.", "error": "rate limit exceeded"}`)

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)

	parseCodexOutput(output, result, logCh)
	close(logCh)

	if result.Summary != "Attempted fix." {
		t.Errorf("expected summary 'Attempted fix.', got %q", result.Summary)
	}

	// Verify error log entry was sent.
	foundError := false
	for entry := range logCh {
		if entry.Level == "error" {
			foundError = true
		}
	}
	_ = foundError // error entry was consumed from channel above
}

func TestParseCodexStreamOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      string
		checkResult func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry)
	}{
		{
			name: "streaming output with function calls",
			output: `{"type":"message","content":"Let me investigate the issue..."}
{"type":"function_call","name":"shell","arguments":{"command":"cat main.go"},"call_id":"call_1"}
{"type":"function_call_output","call_id":"call_1","output":"package main\nfunc main() {}"}
{"type":"message","content":"Found the bug. Applying fix."}
{"type":"function_call","name":"apply_patch","arguments":{"patch":"--- a/main.go\n+++ b/main.go"},"call_id":"call_2"}
{"type":"function_call_output","call_id":"call_2","output":"Patch applied successfully."}
{"type":"message","content":"Fixed the null pointer issue."}
{"type":"usage","stats":{"inputTokens":900,"outputTokens":400}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "Let me investigate the issue...")
				require.Contains(t, result.Summary, "Fixed the null pointer issue.")
				require.Equal(t, 900, result.TokenUsage.InputTokens)
				require.Equal(t, 400, result.TokenUsage.OutputTokens)

				toolUseCount := 0
				for _, log := range logs {
					if log.Level == "tool_use" {
						toolUseCount++
						require.NotNil(t, log.Metadata)
						require.NotEmpty(t, log.Metadata["tool"])
					}
				}
				require.Equal(t, 2, toolUseCount, "should have 2 tool_use log entries")
			},
		},
		{
			name: "function call metadata includes input and call_id",
			output: `{"type":"function_call","name":"shell","arguments":{"command":"go test ./..."},"call_id":"call_abc"}
{"type":"function_call_output","call_id":"call_abc","output":"PASS"}`,
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
				require.Equal(t, "shell", toolLog.Metadata["tool"])
				require.Equal(t, "call_abc", toolLog.Metadata["call_id"])
				inputMap, ok := toolLog.Metadata["input"].(map[string]interface{})
				require.True(t, ok, "input should be a map")
				require.Equal(t, "go test ./...", inputMap["command"])

				// Check tool_result has call_id too.
				var resultLog *agent.LogEntry
				for i, log := range logs {
					if log.Metadata != nil && log.Metadata["type"] == "tool_result" {
						resultLog = &logs[i]
						break
					}
				}
				require.NotNil(t, resultLog, "should have tool_result log")
				require.Equal(t, "call_abc", resultLog.Metadata["call_id"])
			},
		},
		{
			name:   "double-encoded arguments string",
			output: `{"type":"function_call","name":"shell","arguments":"{\"command\":\"ls -la\"}","call_id":"call_1"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "tool_use", logs[0].Level)
				inputMap, ok := logs[0].Metadata["input"].(map[string]interface{})
				require.True(t, ok, "double-encoded arguments should be unwrapped")
				require.Equal(t, "ls -la", inputMap["command"])
			},
		},
		{
			name:   "falls back to legacy JSON",
			output: `{"response":"Fixed the null pointer by adding a nil check.","stats":{"inputTokens":1500,"outputTokens":500}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "nil check")
				require.Equal(t, 1500, result.TokenUsage.InputTokens)
				require.Equal(t, 500, result.TokenUsage.OutputTokens)
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
			output: `{"type":"message","content":"Starting..."}
{"type":"error","error":"context deadline exceeded"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				hasError := false
				for _, log := range logs {
					if log.Level == "error" && strings.Contains(log.Message, "context deadline") {
						hasError = true
					}
				}
				require.True(t, hasError, "should have error log entry")
			},
		},
		{
			name: "thinking events logged as debug",
			output: `{"type":"thinking","content":"Let me consider the approach..."}
{"type":"message","content":"Here is the fix."}`,
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
			output: `{"type":"message","content":"Done.\n{\"confidence_score\": 0.92, \"confidence_reasoning\": \"Straightforward fix\", \"risk_factors\": [\"none\"]}"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.InDelta(t, 0.92, result.ConfidenceScore, 0.001)
				require.Equal(t, "Straightforward fix", result.ConfidenceReasoning)
			},
		},
		{
			name:   "tool_use event type variant",
			output: `{"type":"tool_use","name":"read_file","arguments":{"path":"main.go"},"call_id":"c1"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "tool_use", logs[0].Level)
				require.Equal(t, "read_file", logs[0].Metadata["tool"])
				require.Equal(t, "c1", logs[0].Metadata["call_id"])
			},
		},
		{
			name:   "tool_call event type variant",
			output: `{"type":"tool_call","name":"shell","arguments":{"command":"ls"}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "tool_use", logs[0].Level)
				require.Equal(t, "shell", logs[0].Metadata["tool"])
			},
		},
		{
			name:   "function_call_output with name field",
			output: `{"type":"function_call_output","name":"shell","call_id":"c2","output":"done"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "output", logs[0].Level)
				require.Equal(t, "shell", logs[0].Metadata["tool"])
				require.Equal(t, "c2", logs[0].Metadata["call_id"])
			},
		},
		{
			name:   "tool_result with result field fallback",
			output: `{"type":"tool_result","call_id":"c3","result":"file contents here"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Contains(t, logs[0].Message, "file contents here")
			},
		},
		{
			name:   "result event with Result JSON token usage",
			output: `{"type":"result","content":"All done.","result":{"input_tokens":2000,"output_tokens":800}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "All done.")
				require.Equal(t, 2000, result.TokenUsage.InputTokens)
				require.Equal(t, 800, result.TokenUsage.OutputTokens)
			},
		},
		{
			name:   "assistant event type variant",
			output: `{"type":"assistant","content":"Working on it..."}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "Working on it...")
			},
		},
		{
			name:   "text event type variant",
			output: `{"type":"text","content":"Some text output"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "Some text output")
			},
		},
		{
			name:   "error event with message fallback",
			output: `{"type":"error","message":"out of memory"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "error", logs[0].Level)
				require.Contains(t, logs[0].Message, "out of memory")
			},
		},
		{
			name:   "error event with content fallback",
			output: `{"type":"error","content":"something broke"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "error", logs[0].Level)
				require.Contains(t, logs[0].Message, "something broke")
			},
		},
		{
			name:   "item.completed agent_message surfaces as output",
			output: `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I found the bug and fixed it."}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "output", logs[0].Level)
				require.Contains(t, logs[0].Message, "I found the bug and fixed it.")
				require.Contains(t, result.Summary, "I found the bug and fixed it.")
			},
		},
		{
			name:   "item.completed command_execution surfaces as tool_use",
			output: `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/bash -lc 'ls -la /workspace'","aggregated_output":"total 8\ndrwxr-xr-x  2 root root 4096 main.go","exit_code":0,"status":"completed"}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 2, "should have tool_use + tool_result")
				require.Equal(t, "tool_use", logs[0].Level)
				require.Equal(t, "command_execution", logs[0].Metadata["tool"])
				inputMap, ok := logs[0].Metadata["input"].(map[string]interface{})
				require.True(t, ok)
				require.Contains(t, inputMap["command"], "ls -la /workspace")
				require.Equal(t, "output", logs[1].Level)
				require.Equal(t, "tool_result", logs[1].Metadata["type"])
			},
		},
		{
			name:   "item.completed command_execution with failed status",
			output: `{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"/bin/bash -c 'ls'","aggregated_output":"bwrap: No permissions","exit_code":1,"status":"failed"}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 2)
				require.Equal(t, "tool_use", logs[0].Level)
				require.Equal(t, "failed", logs[0].Metadata["status"])
				exitCode, ok := logs[0].Metadata["exit_code"]
				require.True(t, ok)
				require.Equal(t, 1, exitCode)
			},
		},
		{
			name:   "turn.completed extracts usage info",
			output: `{"type":"turn.completed","usage":{"input_tokens":45439,"cached_input_tokens":39296,"output_tokens":870}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 45439, result.TokenUsage.InputTokens)
				require.Equal(t, 870, result.TokenUsage.OutputTokens)
				require.Len(t, logs, 1)
				require.Equal(t, "debug", logs[0].Level)
			},
		},
		{
			name: "full Codex CLI stream with item events",
			output: `{"type":"thread.started","thread_id":"019d049e-f7f8-7b71-bb08-e174ba50c73c"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I'm going to inspect the workspace."}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/bash -lc 'ls /workspace'","aggregated_output":"main.go","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"I found the issue and applied a fix."}}
{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":500,"output_tokens":200}}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "I'm going to inspect the workspace.")
				require.Contains(t, result.Summary, "I found the issue and applied a fix.")
				require.Equal(t, 1000, result.TokenUsage.InputTokens)
				require.Equal(t, 200, result.TokenUsage.OutputTokens)

				// Count visible entries by level.
				outputCount := 0
				toolUseCount := 0
				debugCount := 0
				for _, log := range logs {
					switch log.Level {
					case "output":
						outputCount++
					case "tool_use":
						toolUseCount++
					case "debug":
						debugCount++
					}
				}
				require.Equal(t, 3, outputCount, "2 agent_messages + 1 tool_result should produce output logs")
				require.Equal(t, 1, toolUseCount, "command_execution should produce tool_use log")
				require.GreaterOrEqual(t, debugCount, 3, "thread.started, turn.started, turn.completed should be debug")
			},
		},
		{
			name:   "unknown event type logged as debug",
			output: `{"type":"custom_unknown","content":"whatever"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "debug", logs[0].Level)
			},
		},
		{
			name:   "non-JSON line in stream",
			output: "some raw text that is not JSON",
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Len(t, logs, 1)
				require.Equal(t, "output", logs[0].Level)
				require.Contains(t, result.Summary, "some raw text")
			},
		},
		{
			name:   "message with empty content falls back to Message field",
			output: `{"type":"message","message":"from message field"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "from message field")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := &agent.AgentResult{}
			logCh := make(chan agent.LogEntry, 100)

			parseCodexStreamOutput([]byte(tt.output), result, logCh)
			close(logCh)

			var logs []agent.LogEntry
			for entry := range logCh {
				logs = append(logs, entry)
			}

			tt.checkResult(t, result, logs)
		})
	}
}

func TestCodexAdapter_Execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		codexOutput   string
		codexExitCode int
		stderrOutput  string
		diffOutput    string
		diffExitCode  int
		expectErr     bool
		checkResult   func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry)
	}{
		{
			name: "successful run with streaming JSON",
			codexOutput: `{"type":"message","content":"Investigating the issue..."}
{"type":"function_call","name":"shell","arguments":{"command":"go test ./..."},"call_id":"c1"}
{"type":"function_call_output","call_id":"c1","output":"PASS"}
{"type":"message","content":"Applied the fix."}
{"type":"usage","stats":{"inputTokens":800,"outputTokens":300}}`,
			codexExitCode: 0,
			diffOutput:    "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n",
			diffExitCode:  0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 0, result.ExitCode)
				require.Empty(t, result.Error)
				require.Contains(t, result.Diff, "diff --git")
				require.Contains(t, result.Summary, "Investigating the issue...")
				require.Contains(t, result.Summary, "Applied the fix.")
				require.Equal(t, 800, result.TokenUsage.InputTokens)
				require.Equal(t, 300, result.TokenUsage.OutputTokens)
			},
		},
		{
			name:          "non-zero exit code with stderr",
			codexOutput:   "",
			codexExitCode: 1,
			stderrOutput:  "command not found",
			diffOutput:    "",
			diffExitCode:  0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 1, result.ExitCode)
				require.Contains(t, result.Error, "exited with code 1")
				require.Contains(t, result.Error, "command not found")
			},
		},
		{
			name:          "empty output",
			codexOutput:   "",
			codexExitCode: 0,
			diffOutput:    "",
			diffExitCode:  0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, 0, result.ExitCode)
				require.Empty(t, result.Diff)
			},
		},
		{
			name:          "legacy JSON format",
			codexOutput:   `{"response":"Fixed the null pointer by adding a nil check.","stats":{"inputTokens":1500,"outputTokens":500}}`,
			codexExitCode: 0,
			diffOutput:    "diff --git a/f.go b/f.go\n",
			diffExitCode:  0,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Contains(t, result.Summary, "nil check")
				require.Equal(t, 1500, result.TokenUsage.InputTokens)
				require.Equal(t, 500, result.TokenUsage.OutputTokens)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := testutil.NewMockSandboxProvider()
			provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				if strings.HasPrefix(cmd, "codex") {
					_, _ = stdout.Write([]byte(tt.codexOutput))
					if tt.stderrOutput != "" {
						_, _ = stderr.Write([]byte(tt.stderrOutput))
					}
					return tt.codexExitCode, nil
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

			adapter := NewCodexAdapter(zerolog.Nop())
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

func TestCodexAdapter_Execute_ExecError(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "codex") {
			return 0, context.DeadlineExceeded
		}
		return 0, nil
	}

	adapter := NewCodexAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "exec codex CLI")
}

func TestCodexAdapter_Execute_WriteFileError(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.WriteFileFn = func(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
		return context.DeadlineExceeded
	}

	adapter := NewCodexAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "write prompt file")
}

func TestCodexAdapter_Execute_MissingSandboxProvider(t *testing.T) {
	t.Parallel()

	adapter := NewCodexAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)

	result, err := adapter.Execute(context.Background(), sandbox, prompt, logCh)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "sandbox provider not found")
}

func TestCodexAdapter_Execute_ContinuationWithoutSessionIDUsesResumeLast(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "codex exec resume") {
			_, _ = stdout.Write([]byte(`{"type":"message","content":"continuing prior session"}`))
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

	adapter := NewCodexAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}
	prompt := &agent.AgentPrompt{
		UserMessage:  "Please tighten the test case.",
		MaxTokens:    50_000,
		Continuation: true,
	}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.NoError(t, err, "continuation should succeed without an explicit session ID")
	require.NotNil(t, result, "continuation should return a result")
	require.Contains(t, provider.ExecCalls[0], "codex exec resume --last --dangerously-bypass-approvals-and-sandbox --sandbox danger-full-access", "continuation without a session ID should resume the latest restored Codex session")
	_, exists := provider.Files["/workspace/.143-prompt.md"]
	require.False(t, exists, "continuation should not write a fresh prompt file")
}

func TestShellEscapeCodex(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no special chars", "/tmp/prompt.md", "/tmp/prompt.md"},
		{"single quote", "/tmp/it's-a-prompt.md", "/tmp/it'\\''s-a-prompt.md"},
		{"multiple quotes", "it's a 'test'", "it'\\''s a '\\''test'\\''"},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellEscapeCodex(tt.input)
			if got != tt.expected {
				t.Errorf("shellEscapeCodex(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
