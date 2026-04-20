// Package adapters contains implementations of the agent.AgentAdapter interface
// for specific coding agent CLIs.
package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// PiAdapter runs the Pi CLI (npm: @mariozechner/pi-coding-agent) inside a sandbox.
//
// Pi is a meta-agent that can route to many providers (Anthropic, OpenAI,
// Google, Moonshot, etc.) using a single CLI. The active model is selected
// via the --model flag, with PI_MODEL_CUSTOM (free-form) winning over
// PI_MODEL (curated dropdown). Provider auth env vars are inherited from
// the other configured agents in the orchestrator.
type PiAdapter struct {
	logger zerolog.Logger
}

// NewPiAdapter creates a new adapter for running Pi CLI.
func NewPiAdapter(logger zerolog.Logger) *PiAdapter {
	return &PiAdapter{
		logger: logger,
	}
}

// Name returns the agent identifier.
func (a *PiAdapter) Name() models.AgentType {
	return models.AgentTypePi
}

// PreparePrompt constructs the prompts for Pi based on the issue context.
func (a *PiAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	if input == nil || input.Issue == nil {
		return nil, fmt.Errorf("agent input and issue are required")
	}

	maxTokens := resolveTokenLimit(input.TokenMode, input.ContextLimits)

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

// Execute runs the Pi CLI inside the sandbox and streams output.
func (a *PiAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	provider := agent.SandboxProviderFromContext(ctx)
	if provider == nil {
		return nil, fmt.Errorf("sandbox provider not found in context")
	}

	// Pi has no headless resume flag; on Path A (snapshot restore) we run a
	// fresh `pi -p` against the restored filesystem with the user's new
	// message as the prompt.
	var promptContent string
	if prompt.Continuation {
		promptContent = prompt.UserMessage
	} else {
		promptContent = fmt.Sprintf("%s\n\n---\n\n%s", prompt.SystemPrompt, prompt.UserPrompt)
	}
	promptPath := fmt.Sprintf("%s/.143-prompt.md", sandbox.WorkDir)
	if err := provider.WriteFile(ctx, sandbox, promptPath, []byte(promptContent)); err != nil {
		return nil, fmt.Errorf("write prompt file: %w", err)
	}

	// Resolve the active model inside the shell so that PI_MODEL_CUSTOM can
	// override PI_MODEL. Falls back to a sensible default if both are unset.
	// Both env vars are populated by the orchestrator from AgentEnvConfig["pi"].
	cmd := fmt.Sprintf(
		"pi -p \"$(cat '%s')\" --mode json --model \"${PI_MODEL_CUSTOM:-${PI_MODEL:-%s}}\"",
		shellEscapeSingle(promptPath),
		models.PiModelClaudeSonnet46,
	)

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "starting Pi CLI",
		Metadata:  map[string]interface{}{"max_tokens": prompt.MaxTokens, "resume": prompt.Continuation},
	}

	result := &agent.AgentResult{}
	var stderr bytes.Buffer
	var summaryParts []string
	var lastAssistantContent string

	exitCode, err := provider.ExecStream(ctx, sandbox, cmd, func(line []byte) {
		if len(bytes.TrimSpace(line)) == 0 {
			return
		}
		parsePiStreamLine(line, result, logCh, &summaryParts, &lastAssistantContent)
	}, &stderr)
	if err != nil {
		return nil, fmt.Errorf("exec pi CLI: %w", err)
	}

	result.ExitCode = exitCode
	if len(summaryParts) > 0 {
		result.Summary = strings.Join(summaryParts, "\n")
	} else if lastAssistantContent != "" {
		result.Summary = lastAssistantContent
	}

	if stderr.Len() > 0 {
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "error",
			Message:   stderr.String(),
		}
	}

	if exitCode != 0 {
		result.Error = fmt.Sprintf("pi CLI exited with code %d", exitCode)
		if stderr.Len() > 0 {
			result.Error += ": " + stderr.String()
		}
	}

	diff, err := collectDiff(ctx, provider, sandbox)
	if err != nil {
		a.logger.Warn().Err(err).Msg("failed to collect git diff")
	} else {
		result.Diff = diff
	}

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "Pi CLI completed",
		Metadata: map[string]interface{}{
			"exit_code":        exitCode,
			"confidence_score": result.ConfidenceScore,
		},
	}

	return result, nil
}

// piStreamEvent represents a single line of Pi's --mode json output.
type piStreamEvent struct {
	Type    string          `json:"type"`
	Content string          `json:"content,omitempty"`
	Message string          `json:"message,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  string          `json:"output,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
	Model   string          `json:"model,omitempty"`
	Usage   *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// parsePiStreamLine processes a single line of Pi's JSON stream.
func parsePiStreamLine(line []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry, summaryParts *[]string, lastAssistant *string) {
	var event piStreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		text := string(line)
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "output",
			Message:   text,
		}
		*summaryParts = append(*summaryParts, text)
		tryExtractConfidence(text, result)
		return
	}

	switch event.Type {
	case "assistant", "text", "message":
		content := event.Content
		if content == "" {
			content = event.Message
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "output",
			Message:   content,
		}
		*lastAssistant = content
		tryExtractConfidence(content, result)

	case "tool_use", "tool_call":
		toolName := event.Tool
		if toolName == "" {
			toolName = event.Name
		}
		metadata := map[string]interface{}{"tool": toolName}
		if event.Model != "" {
			metadata["model"] = event.Model
		}
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

	case "result", "usage", "done":
		content := event.Content
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "info",
			Message:   content,
		}
		if content != "" {
			*summaryParts = append(*summaryParts, content)
			tryExtractConfidence(content, result)
		}
		// Pi emits token counters either via a dedicated `usage` object or
		// packed into the `result` payload. We accept either; `result` is
		// checked last so it wins when both are present.
		if event.Usage != nil {
			result.TokenUsage = agent.TokenUsage{
				InputTokens:  event.Usage.InputTokens,
				OutputTokens: event.Usage.OutputTokens,
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
