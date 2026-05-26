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

	"github.com/assembledhq/143/internal/models"
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
func (a *GeminiCLIAdapter) Name() models.AgentType {
	return models.AgentTypeGeminiCLI
}

// ResumeMode reports that Gemini resumes prior turns by explicit session ID
// (captured from any stream event carrying `session_id` and threaded back
// into `gemini --resume <id>`).
func (a *GeminiCLIAdapter) ResumeMode() agent.SessionResumeMode {
	return agent.ResumeBySessionID
}

// PreparePrompt constructs the prompts for Gemini CLI based on the issue context.
func (a *GeminiCLIAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	if input == nil {
		return nil, fmt.Errorf("agent input is required")
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
		UsageHint: agent.TokenUsageHint{
			AgentType:   models.AgentTypeGeminiCLI,
			BillingMode: agent.TokenBillingModeUnknown,
		},
	}, nil
}

// geminiRuntimeProfile captures Gemini CLI's interactive runtime needs.
var geminiRuntimeProfile = agent.AgentRuntimeProfile{
	Cancellation:      agent.DefaultCancellationSpec,
	PreferSplitOutput: true,
}

// RuntimeProfile declares Gemini's interactive runtime requirements.
func (a *GeminiCLIAdapter) RuntimeProfile() agent.AgentRuntimeProfile {
	return geminiRuntimeProfile
}

// Execute runs the Gemini CLI inside the sandbox and streams output.
func (a *GeminiCLIAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	provider := agent.SandboxProviderFromContext(ctx)
	if provider == nil {
		return nil, fmt.Errorf("sandbox provider not found in context")
	}

	var cmd string
	if prompt.Continuation && prompt.ResumeSessionID != "" {
		// Subsequent turn with a known session ID: deterministic resume by
		// session id captured from a prior turn's stream output. We avoid
		// `--resume latest`, which picks up whichever Gemini session is
		// newest in the local session storage and is non-deterministic when
		// stale entries are present.
		msg := shellEscapeDouble(prompt.UserMessage)
		cmd = fmt.Sprintf(
			"gemini --resume %s --approval-mode=yolo --output-format stream-json -p \"%s\"",
			shellEscapeSingle(prompt.ResumeSessionID),
			msg,
		)
	} else {
		// Fresh exec — used for first turns and as the fallback for
		// continuation turns when the session ID was never captured (the
		// orchestrator embeds the prior conversation history into UserPrompt
		// in that case so the agent has the full context).
		promptContent := fmt.Sprintf("%s\n\n---\n\n%s", prompt.SystemPrompt, prompt.UserPrompt)
		promptPath := fmt.Sprintf("%s/.143-prompt.md", sandbox.HomeDir)
		if err := provider.WriteFile(ctx, sandbox, promptPath, []byte(promptContent)); err != nil {
			return nil, fmt.Errorf("write prompt file: %w", err)
		}
		cmd = fmt.Sprintf(
			"gemini -p \"$(cat '%s')\" --approval-mode=yolo --output-format stream-json",
			shellEscapeSingle(promptPath),
		)
	}

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "starting Gemini CLI",
		Metadata:  map[string]interface{}{"max_tokens": prompt.MaxTokens, "resume": prompt.Continuation},
	}

	result := &agent.AgentResult{}
	var summaryParts []string
	var lastAssistantContent string

	runResult, err := runInteractiveCommand(ctx, sandbox, InteractiveRunSpec{
		Cmd:     cmd,
		Profile: geminiRuntimeProfile,
		OnStdout: func(line []byte) {
			parseGeminiStreamLine(line, result, logCh, &summaryParts, &lastAssistantContent)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("exec gemini CLI: %w", err)
	}

	exitCode := runResult.ExitCode
	stderr := runResult.Stderr
	result.ExitCode = exitCode
	if len(summaryParts) > 0 {
		result.Summary = strings.Join(summaryParts, "\n")
	} else if lastAssistantContent != "" {
		result.Summary = lastAssistantContent
	}

	if len(stderr) > 0 {
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "error",
			Message:   string(stderr),
		}
	}

	if exitCode != 0 {
		result.Error = fmt.Sprintf("gemini CLI exited with code %d", exitCode)
		if len(stderr) > 0 {
			result.Error += ": " + string(stderr)
		}
	}

	// Collect the git diff from the sandbox.
	diff, err := collectDiff(ctx, provider, sandbox, a.logger)
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
			"exit_code": exitCode,
		},
	}

	result.TokenUsage = agent.FinalizeTokenUsage(result.TokenUsage, prompt.UsageHint)

	return result, nil
}

// parseGeminiStreamLine processes a single line of Gemini streaming output.
func parseGeminiStreamLine(line []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry, summaryParts *[]string, lastAssistant *string) {
	var event geminiStreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		text := string(line)
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "output",
			Message:   text,
		}
		*summaryParts = append(*summaryParts, text)
		return
	}

	// Capture session_id from whichever event carries it. Gemini's stream
	// shape varies across versions (session-lifecycle, result, or usage
	// events have all carried it at different times, in both snake_case
	// and camelCase), so we accept it wherever it appears rather than
	// gating on event.Type or a single field name.
	if id := event.SessionID; id != "" {
		result.AgentSessionID = id
	} else if id := event.SessionIDCamel; id != "" {
		result.AgentSessionID = id
	}

	// Handle legacy single-object JSON format (no type field).
	if event.Type == "" {
		var legacy geminiJSONOutput
		if err := json.Unmarshal(line, &legacy); err == nil && legacy.Response != "" {
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   legacy.Response,
			}
			*summaryParts = append(*summaryParts, legacy.Response)
			if legacy.Stats != nil {
				mergeTokenUsage(&result.TokenUsage, agent.TokenUsage{
					InputTokens:  legacy.Stats.InputTokens,
					OutputTokens: legacy.Stats.OutputTokens,
				})
			}
			if legacy.Error != "" {
				logCh <- agent.LogEntry{
					Timestamp: time.Now(),
					Level:     "error",
					Message:   legacy.Error,
				}
			}
			return
		}
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
		// Individual text blocks are persisted as separate output logs —
		// don't merge them into the summary. Track as fallback.
		*lastAssistant = content

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
			*summaryParts = append(*summaryParts, content)
		}
		if event.Stats != nil {
			mergeTokenUsage(&result.TokenUsage, agent.TokenUsage{
				InputTokens:  event.Stats.InputTokens,
				OutputTokens: event.Stats.OutputTokens,
			})
		}
		if len(event.Result) > 0 {
			var usage agent.TokenUsage
			if err := json.Unmarshal(event.Result, &usage); err == nil {
				mergeTokenUsage(&result.TokenUsage, usage)
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

		if geminiResp.Stats != nil {
			mergeTokenUsage(&result.TokenUsage, agent.TokenUsage{
				InputTokens:  geminiResp.Stats.InputTokens,
				OutputTokens: geminiResp.Stats.OutputTokens,
			})
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
	// SessionID is captured from any event that carries it (Gemini emits
	// this on session-lifecycle and result events) so `gemini --resume <id>`
	// can deterministically resume this conversation on the next turn.
	// Both snake_case and camelCase variants are accepted because Gemini's
	// stream-json schema has shipped both spellings across versions.
	SessionID      string `json:"session_id,omitempty"`
	SessionIDCamel string `json:"sessionId,omitempty"`
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
	var lastAssistantContent string

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
			lastAssistantContent = content

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
			}
			if event.Stats != nil {
				mergeTokenUsage(&result.TokenUsage, agent.TokenUsage{
					InputTokens:  event.Stats.InputTokens,
					OutputTokens: event.Stats.OutputTokens,
				})
			}
			if len(event.Result) > 0 {
				var usage agent.TokenUsage
				if err := json.Unmarshal(event.Result, &usage); err == nil {
					mergeTokenUsage(&result.TokenUsage, usage)
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

	if len(summaryParts) > 0 {
		result.Summary = strings.Join(summaryParts, "\n")
	} else if lastAssistantContent != "" {
		result.Summary = lastAssistantContent
	}
}
