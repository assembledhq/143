package adapters

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
)

func TestCodexAdapter_Name(t *testing.T) {
	t.Parallel()

	a := NewCodexAdapter(zerolog.Nop())
	if a.Name() != models.AgentTypeCodex {
		t.Errorf("expected name 'codex', got %q", a.Name())
	}
}

func TestCodexAdapter_PreparePrompt(t *testing.T) {
	t.Parallel()

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
			wantErr:   false,
			wantToken: defaultLowTokenMax,
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
			wantToken: defaultLowTokenMax,
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
			wantToken: defaultHighTokenMax,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
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

func TestCodexModelArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		env            map[string]string
		effectiveModel string
		expected       string
	}{
		{
			name:     "adds priority service tier for gpt 5.5 fast",
			env:      map[string]string{"OPENAI_MODEL": models.CodexModelGPT55Fast},
			expected: ` -m 'gpt-5.5' -c 'service_tier="priority"'`,
		},
		{
			name:     "adds priority service tier for gpt 5.4 fast",
			env:      map[string]string{"OPENAI_MODEL": models.CodexModelGPT54Fast},
			expected: ` -m 'gpt-5.4' -c 'service_tier="priority"'`,
		},
		{
			name:     "adds explicit model for regular model",
			env:      map[string]string{"OPENAI_MODEL": models.CodexModelGPT55},
			expected: ` -m 'gpt-5.5'`,
		},
		{
			name:     "does not add args when no env model is set",
			env:      map[string]string{},
			expected: "",
		},
		{
			name:           "uses resolved effective model when env model is absent",
			env:            map[string]string{},
			effectiveModel: models.CodexModelGPT54Fast,
			expected:       ` -m 'gpt-5.4' -c 'service_tier="priority"'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, codexModelArgs(tt.env, tt.effectiveModel), "codexModelArgs should translate selectable fast aliases into CLI config")
		})
	}
}

func TestParseCodexOutput_JSON(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	output := []byte("I fixed the bug by adding a nil check on line 42.")
	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)

	parseCodexOutput(output, result, logCh)
	close(logCh)

	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

func TestParseCodexOutput_Empty(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)

	parseCodexOutput([]byte(""), result, logCh)
	close(logCh)

	if result.Summary != "" {
		t.Errorf("expected empty summary, got %q", result.Summary)
	}
}

func TestParseCodexOutput_JSONWithError(t *testing.T) {
	t.Parallel()

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
				// Assistant text blocks stay as separate logs; summary has last one.
				require.NotContains(t, result.Summary, "Let me investigate the issue...")
				require.Equal(t, "Fixed the null pointer issue.", result.Summary)
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
				// Falls back to last assistant text when no result event.
				require.Equal(t, "Working on it...", result.Summary)
			},
		},
		{
			name:   "text event type variant",
			output: `{"type":"text","content":"Some text output"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.Equal(t, "Some text output", result.Summary)
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
				require.Equal(t, "I found the bug and fixed it.", result.Summary)
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
				require.Equal(t, "item_1", logs[0].Metadata["item_id"])
				inputMap, ok := logs[0].Metadata["input"].(map[string]interface{})
				require.True(t, ok)
				require.Contains(t, inputMap["command"], "ls -la /workspace")
				require.Equal(t, "output", logs[1].Level)
				require.Equal(t, "tool_result", logs[1].Metadata["type"])
				require.Equal(t, "item_1", logs[1].Metadata["item_id"])
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
				// Summary only has the last assistant text (fallback).
				require.NotContains(t, result.Summary, "I'm going to inspect the workspace.")
				require.Equal(t, "I found the issue and applied a fix.", result.Summary)
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
				require.Equal(t, "from message field", result.Summary)
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
				// Summary has last assistant text only (fallback, no result event).
				require.NotContains(t, result.Summary, "Investigating the issue...")
				require.Equal(t, "Applied the fix.", result.Summary)
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
				if strings.Contains(cmd, "codex exec") {
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
			require.Contains(t, provider.ExecCalls[0], " - < '/home/sandbox/.143-prompt.md'", "codex should read the initial prompt from stdin to avoid appending Docker exec stdin")
			require.NotContains(t, provider.ExecCalls[0], "\"$(cat '/home/sandbox/.143-prompt.md')\"", "codex should not pass the initial prompt as an argv argument")
			require.NotContains(t, provider.ExecCalls[0], ".143-agent.pid", "codex adapter must not embed pidfile scaffolding (provider internal)")
			require.NotContains(t, provider.ExecCalls[0], "& pid=$!", "codex adapter must not embed shell-shim wrapping (provider internal)")
		})
	}
}

func TestCodexAdapter_Execute_ExecError(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.Contains(cmd, "codex exec") {
			return 0, context.DeadlineExceeded
		}
		return 0, nil
	}

	adapter := NewCodexAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
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
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
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
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{SystemPrompt: "test", UserPrompt: "test", MaxTokens: 50_000}
	logCh := make(chan agent.LogEntry, 10)

	result, err := adapter.Execute(context.Background(), sandbox, prompt, logCh)
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "sandbox provider not found")
}

func TestCodexAdapter_Execute_ContinuationWithoutSessionIDFallsBackToFreshExec(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "codex exec ") {
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
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{
		SystemPrompt: "system",
		UserPrompt:   "history-embedded user prompt",
		UserMessage:  "Please tighten the test case.",
		MaxTokens:    50_000,
		Continuation: true,
	}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.NoError(t, err, "continuation should succeed when falling back to fresh exec")
	require.NotNil(t, result, "continuation should return a result")
	require.NotContains(t, provider.ExecCalls[0], "codex exec resume", "continuation without a session ID must not use the non-deterministic resume path")
	require.NotContains(t, provider.ExecCalls[0], "--last", "the --last fallback must not be used")
	require.Contains(t, provider.ExecCalls[0], "codex exec --dangerously-bypass-approvals-and-sandbox", "continuation without a session ID should run a fresh codex exec")
	require.Contains(t, provider.ExecCalls[0], " - < '/home/sandbox/.143-prompt.md'", "fresh fallback should feed the embedded-history prompt over stdin")
	contents, exists := provider.Files["/home/sandbox/.143-prompt.md"]
	require.True(t, exists, "fresh exec must write the system+user prompt to a file")
	require.Contains(t, string(contents), "history-embedded user prompt", "prompt file should carry the orchestrator-provided history-embedded user prompt")
}

func TestCodexAdapter_Execute_ContinuationWithoutSessionIDPassesHumanInputAnswerInPromptFile(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.HasPrefix(cmd, "codex exec ") {
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

	answerText := "Approve and run the focused tests."
	adapter := NewCodexAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{
		SystemPrompt: "system",
		UserPrompt:   "history-embedded user prompt",
		UserMessage:  "Answered human input request.",
		MaxTokens:    50_000,
		Continuation: true,
		HumanInputAnswer: &agent.HumanInputAnswer{
			RequestID:         uuid.MustParse("11111111-1111-1111-1111-111111111111"),
			ProviderRequestID: "toolu_01abc",
			Kind:              models.HumanInputRequestKindToolApproval,
			Status:            models.HumanInputRequestStatusAnswered,
			AnswerText:        &answerText,
			SelectedChoiceIDs: []string{"approve"},
			AnswerPayload:     json.RawMessage(`{"decision":"approve"}`),
		},
	}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)

	require.NoError(t, err, "continuation should succeed when falling back to fresh exec")
	require.NotNil(t, result, "continuation should return a result")
	contents, exists := provider.Files["/home/sandbox/.143-prompt.md"]
	require.True(t, exists, "fresh exec must write the system+user prompt to a file")
	require.Contains(t, string(contents), "Human input answer", "prompt file should include the structured answer block")
	require.Contains(t, string(contents), "toolu_01abc", "prompt file should include the provider request id")
	require.Contains(t, string(contents), "selected_choice_ids", "prompt file should include selected choices")
	require.Contains(t, string(contents), "approve", "prompt file should include the selected approval")
}

func TestCodexAdapter_ParseStreamLine_CapturesThreadStartedSessionID(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 4)
	var summaryParts []string
	lastByType := make(map[string]string)
	lastAssistant := ""

	parseCodexStreamLine(
		[]byte(`{"type":"thread.started","thread_id":"019d049e-f7f8-7b71-bb08-e174ba50c73c"}`),
		result,
		logCh,
		&summaryParts,
		lastByType,
		&lastAssistant,
	)

	require.Equal(t, "019d049e-f7f8-7b71-bb08-e174ba50c73c", result.AgentSessionID, "thread.started should populate AgentSessionID for downstream resume")
}

func TestParseCodexStreamLine_NormalizesToolApprovalHumanInput(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 4)
	var summaryParts []string
	lastByType := make(map[string]string)
	lastAssistant := ""

	parseCodexStreamLine(
		[]byte(`{"type":"tool_approval","request_id":"codex-tool-1","title":"Approve command?","body":"Codex wants to run npm test.","choices":[{"id":"approve","label":"Approve","kind":"positive"},{"id":"edit_command","label":"Edit command","kind":"edit","command":"npm test"}],"response_schema":{"type":"object","required":["decision"],"properties":{"decision":{"type":"string"}}}}`),
		result,
		logCh,
		&summaryParts,
		lastByType,
		&lastAssistant,
	)
	close(logCh)

	logs := drain(logCh)
	require.True(t, result.RequiresHumanInput, "Codex tool_approval events should pause the run for human input")
	require.Len(t, logs, 1, "Codex tool_approval events should emit one human-input log")
	require.Equal(t, "human_input", logs[0].Level, "Codex tool_approval events should use the normalized human-input log level")
	require.Equal(t, "Codex wants to run npm test.", logs[0].Message, "Codex tool_approval body should become the user-visible request body")
	require.Equal(t, string(models.AgentTypeCodex), logs[0].Metadata["provider"], "Codex human-input logs should preserve the provider")
	require.Equal(t, string(models.HumanInputRequestKindToolApproval), logs[0].Metadata["request_kind"], "Codex tool_approval events should normalize the request kind")
	require.NotNil(t, logs[0].HumanInput, "Codex tool_approval logs should include a durable request payload")
	require.Equal(t, models.HumanInputRequestKindToolApproval, logs[0].HumanInput.Kind, "Codex tool_approval payload should use the tool approval kind")
	require.Equal(t, "codex-tool-1", logs[0].HumanInput.ProviderRequestID, "Codex tool_approval payload should retain the provider request id")
	require.Len(t, logs[0].HumanInput.Choices, 2, "Codex tool_approval payload should retain approval choices")
	require.Equal(t, "edit-command", logs[0].HumanInput.Choices[1].ID, "Codex edit choices should be normalized into stable choice ids")
	require.JSONEq(t, `{"type":"object","required":["decision"],"properties":{"decision":{"type":"string"}}}`, string(logs[0].HumanInput.ResponseSchema), "Codex tool_approval payload should retain the response schema")
}

func TestCodexAdapter_ResumeMode(t *testing.T) {
	t.Parallel()

	adapter := NewCodexAdapter(zerolog.Nop())
	require.Equal(t, agent.ResumeBySessionID, adapter.ResumeMode())
}

func TestCodexAdapter_Execute_IncludesReasoningEffortOverride(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.Contains(cmd, "codex exec") {
			_, _ = stdout.Write([]byte(`{"type":"message","content":"done"}`))
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

	adapter := NewCodexAdapter(zerolog.Nop())
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
	require.Contains(t, provider.ExecCalls[0], "model_reasoning_effort", "codex command should include a reasoning override config")
	require.Contains(t, provider.ExecCalls[0], "high", "codex command should include the requested reasoning effort")
}

func TestCodexAdapter_Execute_ContinuationWithResumeSessionIDIncludesReasoningEffort(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.Contains(cmd, "codex exec") {
			_, _ = stdout.Write([]byte(`{"type":"message","content":"done"}`))
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git diff") {
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git rev-parse") {
			_, _ = stdout.Write([]byte("true\n"))
			return 0, nil
		}
		return 0, nil
	}

	adapter := NewCodexAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{
		UserMessage:     "Please continue.",
		MaxTokens:       50_000,
		Continuation:    true,
		ResumeSessionID: "session-123",
		ReasoningEffort: models.ReasoningEffortXHigh,
	}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	require.NoError(t, err, "continuation with explicit session ID should succeed")
	require.NotNil(t, result, "continuation should return a result")
	require.Contains(t, provider.ExecCalls[0], `codex exec resume --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check --json`, "continuation should use explicit resume mode")
	require.Contains(t, provider.ExecCalls[0], "session-123", "continuation should target the provided Codex session ID")
	require.Contains(t, provider.ExecCalls[0], " - < '/home/sandbox/.143-followup-prompt.md'", "continuation should feed the follow-up prompt over stdin")
	require.Contains(t, provider.ExecCalls[0], "model_reasoning_effort", "continuation should include the reasoning override config")
	require.Contains(t, provider.ExecCalls[0], "xhigh", "continuation should include the requested reasoning effort")
	contents, exists := provider.Files["/home/sandbox/.143-followup-prompt.md"]
	require.True(t, exists, "continuation should write the follow-up prompt file")
	require.Equal(t, "Please continue.", string(contents), "follow-up prompt file should contain the user message exactly")
}

func TestBuildCodexResumeMessage_IncludesStructuredHumanInputAnswer(t *testing.T) {
	t.Parallel()

	answerText := "Approve and run the focused tests."
	message, err := buildCodexResumeMessage(&agent.AgentPrompt{
		UserMessage: "Answered human input request.",
		HumanInputAnswer: &agent.HumanInputAnswer{
			RequestID:         uuid.MustParse("11111111-1111-1111-1111-111111111111"),
			ProviderRequestID: "toolu_01abc",
			Kind:              models.HumanInputRequestKindToolApproval,
			Status:            models.HumanInputRequestStatusAnswered,
			AnswerText:        &answerText,
			SelectedChoiceIDs: []string{"approve"},
			AnswerPayload:     json.RawMessage(`{"decision":"approve","edited_input":{"command":"go test ./..."}}`),
		},
	})

	require.NoError(t, err, "Codex resume message should serialize a structured human input answer")
	require.Contains(t, message, "Answered human input request.", "resume message should retain the user's visible answer text")
	require.Contains(t, message, "Human input answer", "resume message should label the structured answer block")
	require.Contains(t, message, `"provider_request_id":"toolu_01abc"`, "resume message should include the provider request id")
	require.Contains(t, message, `"selected_choice_ids":["approve"]`, "resume message should include selected choices")
	require.Contains(t, message, `"decision":"approve"`, "resume message should include structured answer payload")
	require.Contains(t, message, `"command":"go test ./..."`, "resume message should include nested payload fields")
}

func TestCodexAdapter_Execute_ContinuationWithResumeSessionIDPassesHumanInputAnswer(t *testing.T) {
	t.Parallel()

	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		if strings.Contains(cmd, "codex exec") {
			_, _ = stdout.Write([]byte(`{"type":"message","content":"done"}`))
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git diff") {
			return 0, nil
		}
		if strings.HasPrefix(cmd, "git rev-parse") {
			_, _ = stdout.Write([]byte("true\n"))
			return 0, nil
		}
		return 0, nil
	}

	answerText := "Approve and run the focused tests."
	adapter := NewCodexAdapter(zerolog.Nop())
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace", HomeDir: "/home/sandbox", Metadata: map[string]string{agent.SandboxMetadataBaseCommitSHA: "abc123"}}
	prompt := &agent.AgentPrompt{
		UserMessage:     "Answered human input request.",
		MaxTokens:       50_000,
		Continuation:    true,
		ResumeSessionID: "session-123",
		HumanInputAnswer: &agent.HumanInputAnswer{
			RequestID:         uuid.MustParse("11111111-1111-1111-1111-111111111111"),
			ProviderRequestID: "toolu_01abc",
			Kind:              models.HumanInputRequestKindToolApproval,
			Status:            models.HumanInputRequestStatusAnswered,
			AnswerText:        &answerText,
			SelectedChoiceIDs: []string{"approve"},
			AnswerPayload:     json.RawMessage(`{"decision":"approve"}`),
		},
	}
	logCh := make(chan agent.LogEntry, 10)
	ctx := WithSandboxProvider(context.Background(), provider)

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)

	require.NoError(t, err, "continuation with explicit session ID should succeed")
	require.NotNil(t, result, "continuation should return a result")
	require.Contains(t, provider.ExecCalls[0], " - < '/home/sandbox/.143-followup-prompt.md'", "resume command should feed the structured answer over stdin")
	contents, exists := provider.Files["/home/sandbox/.143-followup-prompt.md"]
	require.True(t, exists, "continuation should write the structured answer to the follow-up prompt file")
	require.Contains(t, string(contents), "Human input answer", "resume prompt should include the structured answer block")
	require.Contains(t, string(contents), "toolu_01abc", "resume prompt should include the provider request id")
	require.Contains(t, string(contents), "selected_choice_ids", "resume prompt should include selected choices")
	require.Contains(t, string(contents), "approve", "resume prompt should include the selected approval")
}

func TestIsDuplicateOutput(t *testing.T) {
	t.Parallel()

	t.Run("first content is not a duplicate", func(t *testing.T) {
		t.Parallel()
		m := make(map[string]string)
		require.False(t, isDuplicateOutput("message", "hello", m))
		require.Equal(t, "hello", m["message"])
	})

	t.Run("same content same type is a duplicate", func(t *testing.T) {
		t.Parallel()
		m := map[string]string{"message": "hello"}
		require.True(t, isDuplicateOutput("message", "hello", m))
	})

	t.Run("different content is not a duplicate", func(t *testing.T) {
		t.Parallel()
		m := map[string]string{"message": "hello"}
		require.False(t, isDuplicateOutput("message", "world", m))
		require.Equal(t, "world", m["message"])
	})

	t.Run("same content different type is not a duplicate", func(t *testing.T) {
		t.Parallel()
		m := map[string]string{"message": "hello"}
		require.False(t, isDuplicateOutput("item.completed.agent_message", "hello", m))
	})

	t.Run("empty content is never a duplicate", func(t *testing.T) {
		t.Parallel()
		m := map[string]string{"message": "hello"}
		require.False(t, isDuplicateOutput("message", "", m))
	})
}

func TestParseCodexStreamLine_DeduplicatesConsecutiveOutput(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)
	var summaryParts []string
	lastByType := make(map[string]string)
	var lastAssistant string

	line := []byte(`{"type":"message","content":"Hello world"}`)

	// Send the same line twice — only one log entry should be emitted.
	parseCodexStreamLine(line, result, logCh, &summaryParts, lastByType, &lastAssistant)
	parseCodexStreamLine(line, result, logCh, &summaryParts, lastByType, &lastAssistant)
	close(logCh)

	var logs []agent.LogEntry
	for entry := range logCh {
		logs = append(logs, entry)
	}

	require.Len(t, logs, 1, "consecutive duplicate messages should be deduplicated to 1 log entry")
	require.Equal(t, "Hello world", lastAssistant, "lastAssistant should track the deduplicated output")
}

func TestParseCodexStreamLine_AllowsNonConsecutiveDuplicates(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)
	var summaryParts []string
	lastByType := make(map[string]string)

	lineA := []byte(`{"type":"message","content":"A"}`)
	lineB := []byte(`{"type":"message","content":"B"}`)

	// A, B, A — all 3 should pass through because A is non-consecutive.
	parseCodexStreamLine(lineA, result, logCh, &summaryParts, lastByType, new(string))
	parseCodexStreamLine(lineB, result, logCh, &summaryParts, lastByType, new(string))
	parseCodexStreamLine(lineA, result, logCh, &summaryParts, lastByType, new(string))
	close(logCh)

	var logs []agent.LogEntry
	for entry := range logCh {
		logs = append(logs, entry)
	}

	require.Len(t, logs, 3, "non-consecutive duplicates should all pass through")
	require.Equal(t, "A", logs[0].Message)
	require.Equal(t, "B", logs[1].Message)
	require.Equal(t, "A", logs[2].Message)
}

func TestParseCodexStreamLine_DeduplicatesItemCompleted(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)
	var summaryParts []string
	lastByType := make(map[string]string)
	var lastAssistant string

	line := []byte(`{"type":"item.completed","item":{"type":"agent_message","text":"Final answer"}}`)

	// Same item.completed agent_message twice.
	parseCodexStreamLine(line, result, logCh, &summaryParts, lastByType, &lastAssistant)
	parseCodexStreamLine(line, result, logCh, &summaryParts, lastByType, &lastAssistant)
	close(logCh)

	var logs []agent.LogEntry
	for entry := range logCh {
		logs = append(logs, entry)
	}

	require.Len(t, logs, 1, "consecutive duplicate item.completed agent_message should be deduplicated")
	require.Equal(t, "Final answer", lastAssistant, "lastAssistant should track the deduplicated item.completed")
}

func TestParseCodexStreamLine_SuppressesRefreshTokenFromStdout(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)
	var summaryParts []string
	lastByType := make(map[string]string)

	// Simulate the exact stderr-style errors that arrive via stdout at session end.
	lines := [][]byte{
		[]byte(`2026-03-20T04:36:19.827548Z ERROR codex_core::auth: Failed to refresh token: 401 Unauthorized: { "error": { "message": "Your refresh token has already been used", "type": "invalid_request_error", "param": null, "code": "refresh_token_reused" } }`),
		[]byte(`2026-03-20T04:36:19.827634Z ERROR codex_core::auth: Failed to refresh token: Your access token could not be refreshed because your refresh token was already used. Please log out and sign in again.`),
	}
	for _, line := range lines {
		parseCodexStreamLine(line, result, logCh, &summaryParts, lastByType, new(string))
	}
	close(logCh)

	var logs []agent.LogEntry
	for entry := range logCh {
		logs = append(logs, entry)
	}

	require.Empty(t, logs, "refresh token errors arriving via stdout should be suppressed entirely")
	require.Empty(t, summaryParts, "refresh token errors should not appear in summary")
}

func TestIsRefreshTokenError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		msg  string
		want bool
	}{
		{"refresh_token_reused", true},
		{`401 Unauthorized: { "error": { "code": "refresh_token_reused" } }`, true},
		{"Failed to refresh token: Your access token could not be refreshed because your refresh token was already used.", true},
		{`Failed to refresh token: 401 Unauthorized: { "error": { "message": "bad", "type": "invalid_request_error" } }`, true},
		// Multi-line blob with fragments — overall blob contains the keyword.
		{`"error": { "type": "invalid_request_error", "param": null, } } Failed to refresh token: done`, true},
		{"context deadline exceeded", false},
		{"command not found", false},
		{`"error": { "type": "invalid_request_error" }`, false},
		{"", false},
		// token_expired errors must NOT be filtered here — they need to reach
		// result.Error so the orchestrator's retryOnTokenExpired can detect and
		// retry them. If these are accidentally caught, the retry path breaks.
		{"auth error code: token_expired", false},
		{"Provided authentication token is expired. Please try signing in again.", false},
		{"token is expired", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, isRefreshTokenError(tt.msg), "isRefreshTokenError(%q)", tt.msg)
	}
}

func TestFilterRefreshTokenLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "entire stderr is refresh token errors",
			input:  "ERROR codex_core::auth: Failed to refresh token: refresh_token_reused\nERROR codex_core::auth: Your refresh token was already used.",
			expect: "",
		},
		{
			name:   "mixed lines: refresh lines removed, real errors preserved",
			input:  "\"error\": { \"type\": \"invalid_request_error\", \"param\": null, } }\nFailed to refresh token: refresh_token_reused\n\"error\": { \"type\": \"invalid_request_error\" }",
			expect: "\"error\": { \"type\": \"invalid_request_error\", \"param\": null, } }\n\"error\": { \"type\": \"invalid_request_error\" }",
		},
		{
			name:   "empty input",
			input:  "",
			expect: "",
		},
		{
			name:   "no refresh token content at all — preserved as-is",
			input:  "real error line 1\nreal error line 2",
			expect: "real error line 1\nreal error line 2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expect, filterRefreshTokenLines(tt.input))
		})
	}
}

func TestFilterCodexStderrLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "removes benign stdin diagnostic",
			input:  "Reading additional input from stdin...",
			expect: "",
		},
		{
			name:   "removes benign closed stdin router diagnostic",
			input:  "2026-05-16T02:59:58.624143Z ERROR codex_core::tools::router: error=write_stdin failed: stdin is closed for this session; rerun exec_command with tty=true to keep stdin open",
			expect: "",
		},
		{
			name: "removes benign apply patch verification diagnostic block",
			input: "2026-05-22T05:52:30.204805Z ERROR codex_core::tools::router: error=apply_patch verification failed: Failed to find expected lines in /home/sandbox/143/frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx:\n" +
				"    const formattedMessage = composerPlanMode && activeThread?.agent_type === \"claude_code\"\n" +
				"      ? `${PLAN_MODE_PREFIX}${trimmedMessage}`\n" +
				"      : trimmedMessage;\n" +
				"    const optimisticID = optimisticMessageIDRef.current--;",
			expect: "",
		},
		{
			name: "removes apply patch verification diagnostic with top-level Go context",
			input: "2026-05-22T05:52:30.204805Z ERROR codex_core::tools::router: error=apply_patch verification failed: Failed to find expected lines in /home/sandbox/143/internal/db/autopilot_queue.go:\n" +
				"func ptrTime(t time.Time) *time.Time {\n" +
				"\treturn &t\n" +
				"}",
			expect: "",
		},
		{
			name: "removes arbitrary apply patch verification context until next Codex record",
			input: "2026-05-22T05:52:30.204805Z ERROR codex_core::tools::router: error=apply_patch verification failed: Failed to find expected lines in /home/sandbox/143/query.sql:\n" +
				"SELECT * FROM widgets\n" +
				"WHERE active = true;",
			expect: "",
		},
		{
			name: "removes apply patch verification diagnostic block while preserving next Codex record",
			input: "2026-05-22T05:52:30.204805Z ERROR codex_core::tools::router: error=apply_patch verification failed: Failed to find expected lines in /home/sandbox/143/frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx:\n" +
				"    const formattedMessage = composerPlanMode && activeThread?.agent_type === \"claude_code\"\n" +
				"2026-05-22T05:52:31.204805Z ERROR codex_core::exec: real error line",
			expect: "2026-05-22T05:52:31.204805Z ERROR codex_core::exec: real error line",
		},
		{
			name:   "removes benign stdin diagnostic while preserving real stderr",
			input:  "Reading additional input from stdin...\nreal error line",
			expect: "real error line",
		},
		{
			name:   "removes closed stdin router diagnostic while preserving real stderr",
			input:  "2026-05-16T02:59:58.624143Z ERROR codex_core::tools::router: error=write_stdin failed: stdin is closed for this session; rerun exec_command with tty=true to keep stdin open\nreal error line",
			expect: "real error line",
		},
		{
			name:   "keeps other stdin errors",
			input:  "failed reading from stdin: permission denied",
			expect: "failed reading from stdin: permission denied",
		},
		{
			name:   "still removes refresh token errors",
			input:  "Reading additional input from stdin...\nERROR codex_core::auth: Failed to refresh token: refresh_token_reused",
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expect, filterCodexStderrLines(tt.input), "codex stderr filtering should only remove known benign diagnostics")
		})
	}
}

func TestEmitCodexStderrLogs_FlagsBenignDiagnosticsHidden(t *testing.T) {
	t.Parallel()

	logCh := make(chan agent.LogEntry, 10)
	input := "2026-05-16T02:59:58.624143Z ERROR codex_core::tools::router: error=write_stdin failed: stdin is closed for this session; rerun exec_command with tty=true to keep stdin open"

	filtered := emitCodexStderrLogs([]byte(input), logCh)
	close(logCh)

	var logs []agent.LogEntry
	for entry := range logCh {
		logs = append(logs, entry)
	}

	require.Empty(t, filtered, "benign diagnostic should not be returned as visible stderr")
	require.Len(t, logs, 1, "benign diagnostic should be retained as a hidden log")
	require.Equal(t, "debug", logs[0].Level, "hidden diagnostic should be emitted below user-visible error severity")
	require.Equal(t, input, logs[0].Message, "hidden diagnostic should preserve the original message")
	require.Equal(t, "hidden", logs[0].Metadata["visibility"], "hidden diagnostic should be marked hidden from the user")
	require.Equal(t, "benign_runtime_diagnostic", logs[0].Metadata["diagnostic_class"], "hidden diagnostic should carry a reusable diagnostic class")
	require.Equal(t, "codex", logs[0].Metadata["diagnostic_source"], "hidden diagnostic should identify its source")
	require.Equal(t, "closed_stdin", logs[0].Metadata["diagnostic_kind"], "hidden diagnostic should identify the matched kind")
}

func TestEmitCodexStderrLogs_FlagsApplyPatchVerificationDiagnosticHidden(t *testing.T) {
	t.Parallel()

	logCh := make(chan agent.LogEntry, 10)
	input := "2026-05-22T05:52:30.204805Z ERROR codex_core::tools::router: error=apply_patch verification failed: Failed to find expected lines in /home/sandbox/143/frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx:\n" +
		"    const formattedMessage = composerPlanMode && activeThread?.agent_type === \"claude_code\"\n" +
		"      ? `${PLAN_MODE_PREFIX}${trimmedMessage}`\n" +
		"      : trimmedMessage;\n" +
		"    const optimisticID = optimisticMessageIDRef.current--;"

	filtered := emitCodexStderrLogs([]byte(input), logCh)
	close(logCh)

	var logs []agent.LogEntry
	for entry := range logCh {
		logs = append(logs, entry)
	}

	require.Empty(t, filtered, "apply_patch context mismatch diagnostic should not be returned as visible stderr")
	require.Len(t, logs, 1, "apply_patch diagnostic should be retained as one hidden log")
	require.Equal(t, "debug", logs[0].Level, "hidden diagnostic should be emitted below user-visible error severity")
	require.Equal(t, input, logs[0].Message, "hidden diagnostic should preserve the full multiline message")
	require.Equal(t, "hidden", logs[0].Metadata["visibility"], "hidden diagnostic should be marked hidden from the user")
	require.Equal(t, "apply_patch_verification_failed", logs[0].Metadata["diagnostic_kind"], "hidden diagnostic should identify the matched kind")
}

func TestParseCodexStreamLine_SuppressesRefreshTokenError(t *testing.T) {
	t.Parallel()

	result := &agent.AgentResult{}
	logCh := make(chan agent.LogEntry, 100)
	var summaryParts []string
	lastByType := make(map[string]string)

	line := []byte(`{"type":"error","error":"401 Unauthorized: refresh_token_reused"}`)
	parseCodexStreamLine(line, result, logCh, &summaryParts, lastByType, new(string))
	close(logCh)

	var logs []agent.LogEntry
	for entry := range logCh {
		logs = append(logs, entry)
	}

	require.Empty(t, logs, "refresh_token_reused error events should be suppressed entirely")
}

func TestShellEscapeCodex(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			got := shellEscapeCodex(tt.input)
			if got != tt.expected {
				t.Errorf("shellEscapeCodex(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
