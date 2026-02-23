package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/services/agent"
)

// CodexAdapter runs the Codex CLI inside a sandbox.
type CodexAdapter struct {
	logger zerolog.Logger
}

// NewCodexAdapter creates a new adapter for running Codex CLI.
func NewCodexAdapter(logger zerolog.Logger) *CodexAdapter {
	return &CodexAdapter{
		logger: logger,
	}
}

// Name returns the agent identifier.
func (a *CodexAdapter) Name() string {
	return "codex"
}

// PreparePrompt constructs the prompts for Codex CLI based on the issue context.
// Reuses the shared buildSystemPrompt() and buildUserPrompt() functions.
func (a *CodexAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	if input == nil || input.Issue == nil {
		return nil, fmt.Errorf("agent input and issue are required")
	}

	maxTokens := lowTokenMax
	if input.TokenMode == "high" {
		maxTokens = highTokenMax
	}

	systemPrompt := buildSystemPrompt(input)
	userPrompt := buildUserPrompt(input)
	files := extractFileHints(input)

	return &agent.AgentPrompt{
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		MaxTokens:    maxTokens,
		Files:        files,
	}, nil
}

// Execute runs the Codex CLI inside the sandbox and streams output.
func (a *CodexAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	provider, ok := ctx.Value(sandboxProviderKey{}).(agent.SandboxProvider)
	if !ok {
		return nil, fmt.Errorf("sandbox provider not found in context")
	}

	// Write the prompt to a file in the sandbox.
	promptContent := fmt.Sprintf("%s\n\n---\n\n%s", prompt.SystemPrompt, prompt.UserPrompt)
	promptPath := fmt.Sprintf("%s/.143-prompt.md", sandbox.WorkDir)
	if err := provider.WriteFile(ctx, sandbox, promptPath, []byte(promptContent)); err != nil {
		return nil, fmt.Errorf("write prompt file: %w", err)
	}

	// Build the CLI command.
	// --full-auto: auto-approve all tool calls (non-interactive)
	// -q: pass the prompt as a string
	cmd := fmt.Sprintf(
		"codex --full-auto -q \"$(cat '%s')\"",
		shellEscapeCodex(promptPath),
	)

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "starting Codex CLI",
		Metadata:  map[string]interface{}{"max_tokens": prompt.MaxTokens},
	}

	// Execute the CLI and capture output.
	var stdout, stderr bytes.Buffer
	exitCode, err := provider.Exec(ctx, sandbox, cmd, &stdout, &stderr)
	if err != nil {
		return nil, fmt.Errorf("exec codex CLI: %w", err)
	}

	// Parse the output.
	result := &agent.AgentResult{
		ExitCode: exitCode,
	}
	parseCodexOutput(stdout.Bytes(), result, logCh)

	if stderr.Len() > 0 {
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "error",
			Message:   stderr.String(),
		}
	}

	if exitCode != 0 {
		result.Error = fmt.Sprintf("codex CLI exited with code %d", exitCode)
		if stderr.Len() > 0 {
			result.Error += ": " + stderr.String()
		}
	}

	// Collect the git diff from the sandbox.
	diff, err := collectDiff(ctx, provider, sandbox)
	if err != nil {
		a.logger.Warn().Err(err).Msg("failed to collect git diff")
	} else {
		result.Diff = diff
	}

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "Codex CLI completed",
		Metadata: map[string]interface{}{
			"exit_code":        exitCode,
			"confidence_score": result.ConfidenceScore,
		},
	}

	return result, nil
}

// codexJSONOutput represents Codex CLI's JSON output format.
type codexJSONOutput struct {
	Response string `json:"response"`
	Stats    *struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"stats,omitempty"`
	Error string `json:"error,omitempty"`
}

// parseCodexOutput processes the output from Codex CLI,
// populates the AgentResult, and sends log entries.
func parseCodexOutput(output []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return
	}

	// Try to parse as a single JSON object first.
	var codexResp codexJSONOutput
	if err := json.Unmarshal(trimmed, &codexResp); err == nil && codexResp.Response != "" {
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "output",
			Message:   codexResp.Response,
		}
		result.Summary = codexResp.Response
		tryExtractConfidence(codexResp.Response, result)

		if codexResp.Stats != nil {
			result.TokenUsage = agent.TokenUsage{
				InputTokens:  codexResp.Stats.InputTokens,
				OutputTokens: codexResp.Stats.OutputTokens,
			}
		}
		if codexResp.Error != "" {
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "error",
				Message:   codexResp.Error,
			}
		}
		return
	}

	// Fallback: treat as plain text output.
	text := string(trimmed)
	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "output",
		Message:   text,
	}
	result.Summary = text
	tryExtractConfidence(text, result)
}

// shellEscapeCodex escapes single quotes for safe shell usage.
func shellEscapeCodex(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
