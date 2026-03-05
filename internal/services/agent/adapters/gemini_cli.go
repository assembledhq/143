// Package adapters contains implementations of the agent.AgentAdapter interface
// for specific coding agent CLIs.
package adapters

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/services/agent"
)

// GeminiCLIAdapter runs the Gemini CLI inside a sandbox.
type GeminiCLIAdapter struct {
	logger zerolog.Logger
}

// NewGeminiCLIAdapter creates a new adapter for running Gemini CLI.
func NewGeminiCLIAdapter(logger zerolog.Logger) *GeminiCLIAdapter {
	return &GeminiCLIAdapter{
		logger: logger,
	}
}

// Name returns the agent identifier.
func (a *GeminiCLIAdapter) Name() string {
	return "gemini_cli"
}

// PreparePrompt constructs the prompts for Gemini CLI based on the issue context.
func (a *GeminiCLIAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
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

// Execute runs the Gemini CLI inside the sandbox and streams output.
func (a *GeminiCLIAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	provider, ok := ctx.Value(sandboxProviderKey{}).(agent.SandboxProvider)
	if !ok {
		return nil, fmt.Errorf("sandbox provider not found in context")
	}

	// Write the prompt to a file in the sandbox so we can reference it.
	promptContent := fmt.Sprintf("%s\n\n---\n\n%s", prompt.SystemPrompt, prompt.UserPrompt)
	promptPath := fmt.Sprintf("%s/.143-prompt.md", sandbox.WorkDir)
	if err := provider.WriteFile(ctx, sandbox, promptPath, []byte(promptContent)); err != nil {
		return nil, fmt.Errorf("write prompt file: %w", err)
	}

	// Gemini CLI headless: -p for non-interactive, --yolo to auto-approve,
	// --output-format stream-json for streaming JSONL with tool use detail.
	cmd := fmt.Sprintf(
		"gemini -p \"$(cat '%s')\" --yolo --output-format stream-json",
		shellEscapeGemini(promptPath),
	)

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "starting Gemini CLI",
		Metadata:  map[string]interface{}{"max_tokens": prompt.MaxTokens},
	}

	// Execute the CLI and capture output.
	var stdout, stderr bytes.Buffer
	exitCode, err := provider.Exec(ctx, sandbox, cmd, &stdout, &stderr)
	if err != nil {
		return nil, fmt.Errorf("exec gemini CLI: %w", err)
	}

	// Parse the JSON output.
	result := &agent.AgentResult{
		ExitCode: exitCode,
	}
	parseGeminiStreamOutput(stdout.Bytes(), result, logCh)

	if stderr.Len() > 0 {
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "error",
			Message:   stderr.String(),
		}
	}

	if exitCode != 0 {
		result.Error = fmt.Sprintf("gemini CLI exited with code %d", exitCode)
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
		Message:   "Gemini CLI completed",
		Metadata: map[string]interface{}{
			"exit_code":        exitCode,
			"confidence_score": result.ConfidenceScore,
		},
	}

	return result, nil
}

// geminiJSONOutput represents Gemini CLI's --output-format json response.
type geminiJSONOutput struct {
	Response string `json:"response"`
	Stats    *struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"stats,omitempty"`
	Error string `json:"error,omitempty"`
}

// parseGeminiOutput processes the JSON output from Gemini CLI,
// populates the AgentResult, and sends log entries.
func parseGeminiOutput(output []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return
	}

	// Try to parse as a single JSON object first.
	var geminiResp geminiJSONOutput
	if err := json.Unmarshal(trimmed, &geminiResp); err == nil && geminiResp.Response != "" {
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "output",
			Message:   geminiResp.Response,
		}
		result.Summary = geminiResp.Response
		tryExtractConfidence(geminiResp.Response, result)

		if geminiResp.Stats != nil {
			result.TokenUsage = agent.TokenUsage{
				InputTokens:  geminiResp.Stats.InputTokens,
				OutputTokens: geminiResp.Stats.OutputTokens,
			}
		}
		if geminiResp.Error != "" {
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "error",
				Message:   geminiResp.Error,
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

// geminiStreamEvent represents a single line of Gemini CLI's stream-json output.
type geminiStreamEvent struct {
	Type    string          `json:"type"`
	Content string          `json:"content,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  string          `json:"output,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Message string          `json:"message,omitempty"`
	Stats   *struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"stats,omitempty"`
	Error string `json:"error,omitempty"`
}

// parseGeminiStreamOutput processes the streaming JSONL output from Gemini CLI,
// populates the AgentResult, and sends log entries with detailed tool use metadata.
// Falls back to parseGeminiOutput for legacy single-object JSON responses.
func parseGeminiStreamOutput(output []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return
	}

	// Detect legacy single-object JSON format (non-streaming).
	var legacyResp geminiJSONOutput
	if err := json.Unmarshal(trimmed, &legacyResp); err == nil && legacyResp.Response != "" {
		parseGeminiOutput(output, result, logCh)
		return
	}

	// Parse as streaming JSONL line by line.
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var summaryParts []string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var event geminiStreamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Not JSON — emit as raw output and include in summary.
			text := string(line)
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   text,
			}
			summaryParts = append(summaryParts, text)
			tryExtractConfidence(text, result)
			continue
		}

		switch event.Type {
		case "text", "assistant":
			content := event.Content
			if content == "" {
				content = event.Message
			}
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   content,
			}
			summaryParts = append(summaryParts, content)
			tryExtractConfidence(content, result)

		case "tool_call", "tool_use":
			toolName := event.Tool
			if toolName == "" {
				toolName = event.Name
			}
			metadata := map[string]interface{}{"tool": toolName}
			if len(event.Input) > 0 {
				var inputMap map[string]interface{}
				if err := json.Unmarshal(event.Input, &inputMap); err == nil {
					metadata["input"] = inputMap
				}
			}
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "tool_use",
				Message:   fmt.Sprintf("using tool: %s", toolName),
				Metadata:  metadata,
			}

		case "tool_result":
			toolName := event.Tool
			if toolName == "" {
				toolName = event.Name
			}
			metadata := map[string]interface{}{"type": "tool_result"}
			if toolName != "" {
				metadata["tool"] = toolName
			}
			outputMsg := event.Output
			if outputMsg == "" && len(event.Result) > 0 {
				outputMsg = string(event.Result)
			}
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   outputMsg,
				Metadata:  metadata,
			}

		case "thinking":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "debug",
				Message:   event.Content,
				Metadata:  map[string]interface{}{"type": "thinking"},
			}

		case "error":
			msg := event.Error
			if msg == "" {
				msg = event.Message
			}
			if msg == "" {
				msg = event.Content
			}
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "error",
				Message:   msg,
			}

		case "usage", "result":
			content := event.Content
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "info",
				Message:   content,
			}
			if content != "" {
				summaryParts = append(summaryParts, content)
				tryExtractConfidence(content, result)
			}
			if event.Stats != nil {
				result.TokenUsage = agent.TokenUsage{
					InputTokens:  event.Stats.InputTokens,
					OutputTokens: event.Stats.OutputTokens,
				}
			}
			if len(event.Result) > 0 {
				var usage agent.TokenUsage
				if err := json.Unmarshal(event.Result, &usage); err == nil && usage.InputTokens > 0 {
					result.TokenUsage = usage
				}
			}

		default:
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "debug",
				Message:   string(line),
			}
		}
	}

	result.Summary = strings.Join(summaryParts, "\n")
}

// shellEscapeGemini escapes single quotes for safe shell usage.
func shellEscapeGemini(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
