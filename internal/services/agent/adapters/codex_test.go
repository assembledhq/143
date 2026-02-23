package adapters

import (
	"context"
	"testing"

	"github.com/rs/zerolog"

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
