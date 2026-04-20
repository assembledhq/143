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

// AmpAdapter runs the Sourcegraph Amp CLI inside a sandbox.
//
// Amp uses agent "modes" (smart, deep, large, rush) instead of model names —
// each mode bundles a model, system prompt, and tool set. The mode is passed
// explicitly via `-m` at invocation time, resolved in-shell from AMP_MODE
// with a "smart" fallback.
type AmpAdapter struct {
	logger zerolog.Logger
}

// NewAmpAdapter creates a new adapter for running Amp CLI.
func NewAmpAdapter(logger zerolog.Logger) *AmpAdapter {
	return &AmpAdapter{
		logger: logger,
	}
}

// Name returns the agent identifier.
func (a *AmpAdapter) Name() models.AgentType {
	return models.AgentTypeAmp
}

// PreparePrompt constructs the prompts for Amp based on the issue context.
func (a *AmpAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
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

// Execute runs the Amp CLI inside the sandbox and streams output.
func (a *AmpAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	provider := agent.SandboxProviderFromContext(ctx)
	if provider == nil {
		return nil, fmt.Errorf("sandbox provider not found in context")
	}

	// Amp does not expose a stable continuation flag in headless mode. On
	// Path A (snapshot restore) we still execute a fresh `amp -x` against the
	// restored filesystem, using the user's new message as the prompt.
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

	// `--dangerously-allow-all` skips Amp's interactive permission prompts;
	// the container is already isolated so additional in-CLI gating is noise.
	// `-m` is resolved in-shell from AMP_MODE (set via per-run model override
	// or agent_config.amp.AMP_MODE) with a curated default, so the active mode
	// is visible in the command line (greppable in logs) and doesn't depend on
	// Amp reading AMP_MODE from env itself.
	cmd := fmt.Sprintf(
		"amp -x \"$(cat '%s')\" --stream-json --dangerously-allow-all -m \"${AMP_MODE:-%s}\"",
		shellEscapeSingle(promptPath),
		models.AmpModeSmart,
	)

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "starting Amp CLI",
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
		parseAmpStreamLine(line, result, logCh, &summaryParts, &lastAssistantContent)
	}, &stderr)
	if err != nil {
		return nil, fmt.Errorf("exec amp CLI: %w", err)
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
		result.Error = fmt.Sprintf("amp CLI exited with code %d", exitCode)
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
		Message:   "Amp CLI completed",
		Metadata: map[string]interface{}{
			"exit_code":        exitCode,
			"confidence_score": result.ConfidenceScore,
		},
	}

	return result, nil
}

// ampStreamEvent represents a single line of Amp's --stream-json output.
// Amp emits Claude Code-compatible events; tolerate provider-specific extras.
type ampStreamEvent struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	Message   string          `json:"message,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Output    string          `json:"output,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Usage     *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// parseAmpStreamLine processes a single line of Amp streaming output.
func parseAmpStreamLine(line []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry, summaryParts *[]string, lastAssistant *string) {
	var event ampStreamEvent
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
	case "assistant", "text":
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

	case "result", "usage":
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
		// Amp has shipped both shapes: a dedicated `usage` object, and a
		// `result` payload that sometimes packs the same counters. We accept
		// either; `result` is checked last so it wins when both are present.
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
		if event.SessionID != "" {
			result.AgentSessionID = event.SessionID
		}

	default:
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "debug",
			Message:   string(line),
		}
	}
}
