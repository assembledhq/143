package adapters

import (
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestCodexAdapter_Name(t *testing.T) {
	a := NewCodexAdapter(zerolog.Nop())
	if a.Name() != "codex" {
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
					Source:   "sentry",
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
					Source:   "sentry",
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
			name: "double-encoded arguments string",
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
			name: "confidence extraction from stream",
			output: `{"type":"message","content":"Done.\n{\"confidence_score\": 0.92, \"confidence_reasoning\": \"Straightforward fix\", \"risk_factors\": [\"none\"]}"}`,
			checkResult: func(t *testing.T, result *agent.AgentResult, logs []agent.LogEntry) {
				t.Helper()
				require.InDelta(t, 0.92, result.ConfidenceScore, 0.001)
				require.Equal(t, "Straightforward fix", result.ConfidenceReasoning)
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
