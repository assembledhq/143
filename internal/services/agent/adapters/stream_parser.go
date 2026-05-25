package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/services/agent"
)

// agentStreamEvent is the union of fields emitted by the amp and pi stream-JSON
// protocols (both Claude Code-compatible). Fields not present on a given event
// are zero-valued.
type agentStreamEvent struct {
	Type         string          `json:"type"`
	Content      string          `json:"content,omitempty"`
	Message      string          `json:"message,omitempty"`
	Tool         string          `json:"tool,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	Output       string          `json:"output,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        string          `json:"error,omitempty"`
	Model        string          `json:"model,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	TotalCostUSD *float64        `json:"total_cost_usd,omitempty"`
	CostUSD      *float64        `json:"cost_usd,omitempty"`
	Usage        *struct {
		InputTokens         int `json:"input_tokens"`
		CachedInputTokens   int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
		OutputTokens        int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// streamParseConfig captures the small per-adapter differences in event
// dispatch. Everything else is identical between amp and pi.
type streamParseConfig struct {
	// MessageAsAssistant routes a type=="message" event through the assistant
	// branch (content → output log, updates lastAssistant).
	MessageAsAssistant bool
	// DoneAsResult routes a type=="done" event through the result/usage branch.
	DoneAsResult bool
	// CaptureToolModel records event.Model in tool_use metadata.
	CaptureToolModel bool
	// CaptureSessionID records event.SessionID on the shared AgentResult when
	// a result/usage event carries one.
	CaptureSessionID bool
}

// parseAgentStreamLine processes a single line of streaming JSON output for an
// agent that emits Claude Code-compatible events. Unknown event types fall
// through to a debug log; non-JSON lines are emitted as plain output.
func parseAgentStreamLine(
	line []byte,
	cfg streamParseConfig,
	result *agent.AgentResult,
	logCh chan<- agent.LogEntry,
	summaryParts *[]string,
	lastAssistant *string,
) {
	var event agentStreamEvent
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

	switch {
	case event.Type == "assistant" || event.Type == "text" ||
		(cfg.MessageAsAssistant && event.Type == "message"):
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

	case event.Type == "tool_use" || event.Type == "tool_call":
		toolName := event.Tool
		if toolName == "" {
			toolName = event.Name
		}
		metadata := map[string]interface{}{"tool": toolName}
		if cfg.CaptureToolModel && event.Model != "" {
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

	case event.Type == "tool_result":
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

	case event.Type == "thinking":
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "debug",
			Message:   event.Content,
			Metadata:  map[string]interface{}{"type": "thinking"},
		}

	case event.Type == "error":
		msg := event.Error
		if msg == "" {
			msg = event.Message
		}
		if msg == "" {
			msg = event.Content
		}
		if msg == "" {
			msg = "unknown error"
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "error",
			Message:   msg,
		}

	case isHumanInputEventType(event.Type):
		req, ok := normalizeGenericHumanInputEvent(line, "")
		if !ok {
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "debug",
				Message:   string(line),
			}
			return
		}
		result.RequiresHumanInput = true
		logCh <- agent.LogEntry{
			Timestamp:  time.Now(),
			Level:      "human_input",
			Message:    req.Body,
			Metadata:   map[string]interface{}{"request_kind": string(req.Kind), "title": req.Title},
			HumanInput: &req,
		}

	case event.Type == "result" || event.Type == "usage" ||
		(cfg.DoneAsResult && event.Type == "done"):
		content := event.Content
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "info",
			Message:   content,
		}
		if content != "" {
			*summaryParts = append(*summaryParts, content)
		}
		// Both shapes ship in the wild: a dedicated `usage` object, and a
		// `result` payload that sometimes packs the same counters. Accept
		// either; `result` is checked last so it wins when both are present.
		if event.Usage != nil {
			mergeTokenUsage(&result.TokenUsage, agent.TokenUsage{
				Reported:            true,
				InputTokens:         event.Usage.InputTokens,
				CachedInputTokens:   event.Usage.CachedInputTokens,
				CacheCreationTokens: event.Usage.CacheCreationTokens,
				OutputTokens:        event.Usage.OutputTokens,
			})
		}
		if len(event.Result) > 0 {
			var usage agent.TokenUsage
			if err := json.Unmarshal(event.Result, &usage); err == nil {
				usage.Reported = true
				mergeTokenUsage(&result.TokenUsage, usage)
			}
		}
		if event.TotalCostUSD != nil {
			setDirectUSDCost(&result.TokenUsage, *event.TotalCostUSD, "stream_event_total_cost_usd")
		}
		if event.CostUSD != nil {
			setDirectUSDCost(&result.TokenUsage, *event.CostUSD, "stream_event_cost_usd")
		}
		if cfg.CaptureSessionID && event.SessionID != "" {
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

// streamingAgentConfig drives runStreamingAgent. BuildCmd receives the already
// shell-escaped prompt file path and returns the full command line.
type streamingAgentConfig struct {
	DisplayName string // "Amp" / "Pi" — used in start/completion log messages.
	CLIName     string // "amp" / "pi" — used in the exec error message.
	BuildCmd    func(escapedPromptPath string) string
	ParseConfig streamParseConfig
	Profile     agent.AgentRuntimeProfile
}

// runStreamingAgent implements the shared Execute flow for agents that (a)
// take their prompt via a file on disk, (b) emit Claude Code-compatible
// stream JSON, and (c) don't have a headless continuation flag.
func runStreamingAgent(
	ctx context.Context,
	cfg streamingAgentConfig,
	logger zerolog.Logger,
	sandbox *agent.Sandbox,
	prompt *agent.AgentPrompt,
	logCh chan<- agent.LogEntry,
) (*agent.AgentResult, error) {
	provider := agent.SandboxProviderFromContext(ctx)
	if provider == nil {
		return nil, fmt.Errorf("sandbox provider not found in context")
	}

	var promptContent string
	if prompt.Continuation {
		promptContent = prompt.UserMessage
		// Amp/Pi have no headless resume flag, so continuation replays against
		// the restored filesystem with only the new user message as the prompt.
		// Emit an explicit log so "the agent forgot the original task" reports
		// are debuggable without reading the adapter source.
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "info",
			Message: fmt.Sprintf(
				"%s has no headless resume; continuation prompt is the new user message only (prior conversation context is not replayed)",
				cfg.DisplayName,
			),
		}
	} else {
		promptContent = fmt.Sprintf("%s\n\n---\n\n%s", prompt.SystemPrompt, prompt.UserPrompt)
	}
	// Write under $HOME (not WorkDir) so the file doesn't pollute the cloned
	// repo's git status.
	promptPath := fmt.Sprintf("%s/.143-prompt.md", sandbox.HomeDir)
	if err := provider.WriteFile(ctx, sandbox, promptPath, []byte(promptContent)); err != nil {
		return nil, fmt.Errorf("write prompt file: %w", err)
	}

	cmd := cfg.BuildCmd(shellEscapeSingle(promptPath))

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   fmt.Sprintf("starting %s CLI", cfg.DisplayName),
		Metadata:  map[string]interface{}{"max_tokens": prompt.MaxTokens, "resume": prompt.Continuation},
	}

	result := &agent.AgentResult{}
	var summaryParts []string
	var lastAssistantContent string

	runResult, err := runInteractiveCommand(ctx, sandbox, InteractiveRunSpec{
		Cmd:     cmd,
		Profile: cfg.Profile,
		OnStdout: func(line []byte) {
			parseAgentStreamLine(line, cfg.ParseConfig, result, logCh, &summaryParts, &lastAssistantContent)
		},
	})
	if err != nil {
		if len(runResult.Stderr) > 0 {
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "error",
				Message:   string(runResult.Stderr),
			}
		}
		return nil, fmt.Errorf("exec %s CLI: %w", cfg.CLIName, err)
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
		result.Error = fmt.Sprintf("%s CLI exited with code %d", cfg.CLIName, exitCode)
		errorDetail := strings.TrimSpace(string(stderr))
		if errorDetail == "" {
			// TTY transports merge stderr into the visible output stream, so
			// preserve the last visible line as the best-effort failure detail.
			if len(summaryParts) > 0 {
				errorDetail = strings.TrimSpace(summaryParts[len(summaryParts)-1])
			} else if lastAssistantContent != "" {
				errorDetail = strings.TrimSpace(lastAssistantContent)
			}
		}
		if errorDetail != "" {
			result.Error += ": " + errorDetail
		}
	}

	diff, err := collectDiff(ctx, provider, sandbox, logger)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to collect git diff")
	} else {
		result.Diff = diff
	}

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   fmt.Sprintf("%s CLI completed", cfg.DisplayName),
		Metadata: map[string]interface{}{
			"exit_code": exitCode,
		},
	}

	result.TokenUsage = agent.FinalizeTokenUsage(result.TokenUsage, prompt.UsageHint)

	return result, nil
}
