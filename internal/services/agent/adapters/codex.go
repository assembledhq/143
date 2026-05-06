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
func (a *CodexAdapter) Name() models.AgentType {
	return models.AgentTypeCodex
}

// ResumeMode reports that Codex resumes prior turns by explicit session ID
// (captured from the `thread.started` event Codex emits at the start of each
// `--json` run and threaded back into `codex exec resume <id>`).
func (a *CodexAdapter) ResumeMode() agent.SessionResumeMode {
	return agent.ResumeBySessionID
}

// PreparePrompt constructs the prompts for Codex CLI based on the issue context.
// Reuses the shared buildSystemPrompt() and buildUserPrompt() functions.
func (a *CodexAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	if input == nil {
		return nil, fmt.Errorf("agent input is required")
	}

	maxTokens := resolveTokenLimit(input.TokenMode, input.ContextLimits)

	systemPrompt := buildSystemPrompt(input)
	userPrompt := buildUserPrompt(input)
	files := extractFileHints(input)

	return &agent.AgentPrompt{
		SystemPrompt:    systemPrompt,
		UserPrompt:      userPrompt,
		MaxTokens:       maxTokens,
		ReasoningEffort: input.ReasoningEffort,
		Files:           files,
	}, nil
}

// codexRuntimeProfile captures Codex's interactive runtime needs.
var codexRuntimeProfile = agent.AgentRuntimeProfile{
	Cancellation:      agent.DefaultCancellationSpec,
	PreferSplitOutput: true,
}

// RuntimeProfile declares Codex's interactive runtime requirements.
func (a *CodexAdapter) RuntimeProfile() agent.AgentRuntimeProfile {
	return codexRuntimeProfile
}

// Execute runs the Codex CLI inside the sandbox and streams output.
func (a *CodexAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	provider := agent.SandboxProviderFromContext(ctx)
	if provider == nil {
		return nil, fmt.Errorf("sandbox provider not found in context")
	}

	// Use --dangerously-bypass-approvals-and-sandbox (aka --yolo) to skip
	// Codex's internal bwrap sandboxing. The container is already isolated
	// by Docker + gVisor, and bwrap fails because gVisor doesn't support the
	// unprivileged user namespaces that bwrap requires.
	var cmd string
	reasoningArg := ""
	if prompt.ReasoningEffort != "" {
		reasoningArg = fmt.Sprintf(" -c '%s'", shellEscapeSingle(fmt.Sprintf(`model_reasoning_effort="%s"`, prompt.ReasoningEffort)))
	}
	if prompt.Continuation && prompt.ResumeSessionID != "" {
		// Subsequent turn with a known session ID: deterministic resume by
		// thread/session id captured from a prior turn's `thread.started`
		// event. We avoid `codex exec resume --last` because `--last` reads
		// whichever rollout file is newest in ~/.codex/sessions, which is
		// non-deterministic when stale entries are present.
		msg := shellEscapeDouble(prompt.UserMessage)
		cmd = fmt.Sprintf(
			"codex exec resume --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check --json%s %s \"%s\"",
			reasoningArg,
			shellEscapeCodex(prompt.ResumeSessionID),
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
			"codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check --json%s \"$(cat '%s')\"",
			reasoningArg,
			shellEscapeCodex(promptPath),
		)
	}

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "starting Codex CLI",
		Metadata:  map[string]interface{}{"max_tokens": prompt.MaxTokens, "resume": prompt.Continuation},
	}

	result := &agent.AgentResult{}
	var summaryParts []string
	var lastAssistantContent string
	lastOutputByType := make(map[string]string)

	runResult, err := runInteractiveCommand(ctx, sandbox, InteractiveRunSpec{
		Cmd:     cmd,
		Profile: codexRuntimeProfile,
		OnStdout: func(line []byte) {
			parseCodexStreamLine(line, result, logCh, &summaryParts, lastOutputByType, &lastAssistantContent)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("exec codex CLI: %w", err)
	}

	exitCode := runResult.ExitCode
	stderr := runResult.Stderr
	result.ExitCode = exitCode
	if len(summaryParts) > 0 {
		result.Summary = strings.Join(summaryParts, "\n")
	} else if lastAssistantContent != "" {
		result.Summary = lastAssistantContent
	}

	// Filter refresh-token errors once and reuse the result.
	var filteredStderr string
	if len(stderr) > 0 {
		filteredStderr = filterRefreshTokenLines(string(stderr))
		if filteredStderr != "" {
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "error",
				Message:   filteredStderr,
			}
		}
	}

	if exitCode != 0 {
		result.Error = fmt.Sprintf("codex CLI exited with code %d", exitCode)
		if filteredStderr != "" {
			result.Error += ": " + filteredStderr
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
		Message:   "Codex CLI completed",
		Metadata: map[string]interface{}{
			"exit_code":        exitCode,
			"confidence_score": result.ConfidenceScore,
		},
	}

	return result, nil
}

// isDuplicateOutput returns true if content matches the previous output of the
// same event type and should be suppressed. Tracks per-type to avoid cross-type
// deduplication (e.g. a "message" and "item.completed" with the same text).
func isDuplicateOutput(eventType, content string, lastByType map[string]string) bool {
	if content == "" {
		return false
	}
	if prev, ok := lastByType[eventType]; ok && prev == content {
		return true
	}
	lastByType[eventType] = content
	return false
}

// parseCodexStreamLine processes a single line of Codex streaming output.
func parseCodexStreamLine(line []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry, summaryParts *[]string, lastByType map[string]string, lastAssistant *string) {
	// Suppress refresh-token errors regardless of how they arrive (stdout or
	// stderr). The Codex CLI sometimes writes these to stdout at shutdown.
	if isRefreshTokenError(string(line)) {
		return
	}

	var event codexStreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		text := string(line)
		if isDuplicateOutput("raw", text, lastByType) {
			return
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "output",
			Message:   text,
		}
		*summaryParts = append(*summaryParts, text)
		tryExtractConfidence(text, result)
		return
	}

	// Handle legacy single-object JSON format (no type field).
	if event.Type == "" {
		var legacy codexJSONOutput
		if err := json.Unmarshal(line, &legacy); err == nil && legacy.Response != "" {
			if !isDuplicateOutput("legacy", legacy.Response, lastByType) {
				logCh <- agent.LogEntry{
					Timestamp: time.Now(),
					Level:     "output",
					Message:   legacy.Response,
				}
				*summaryParts = append(*summaryParts, legacy.Response)
			}
			tryExtractConfidence(legacy.Response, result)
			if legacy.Stats != nil {
				result.TokenUsage = agent.TokenUsage{
					InputTokens:  legacy.Stats.InputTokens,
					OutputTokens: legacy.Stats.OutputTokens,
				}
			}
			return
		}
	}

	switch event.Type {
	case "thread.started":
		// Capture the Codex thread/session ID so `codex exec resume <id>` can
		// deterministically resume this conversation on the next turn.
		if event.ThreadID != "" {
			result.AgentSessionID = event.ThreadID
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "debug",
			Message:   string(line),
		}

	case "message", "text", "assistant":
		content := event.Content
		if content == "" {
			content = event.Message
		}
		if isDuplicateOutput(event.Type, content, lastByType) {
			return
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "output",
			Message:   content,
		}
		// Individual text blocks are persisted as separate output logs —
		// don't merge them into the summary. Track as fallback.
		*lastAssistant = content
		tryExtractConfidence(content, result)

	case "function_call", "tool_use", "tool_call":
		toolName := event.Name
		metadata := map[string]interface{}{"tool": toolName}
		if len(event.Arguments) > 0 {
			var argsMap map[string]interface{}
			if err := json.Unmarshal(event.Arguments, &argsMap); err == nil {
				metadata["input"] = argsMap
			} else {
				var argsStr string
				if err := json.Unmarshal(event.Arguments, &argsStr); err == nil {
					var innerArgs map[string]interface{}
					if err := json.Unmarshal([]byte(argsStr), &innerArgs); err == nil {
						metadata["input"] = innerArgs
					} else {
						metadata["input"] = argsStr
					}
				}
			}
		}
		if event.CallID != "" {
			metadata["call_id"] = event.CallID
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "tool_use",
			Message:   fmt.Sprintf("using tool: %s", toolName),
			Metadata:  metadata,
		}

	case "function_call_output", "tool_result":
		metadata := map[string]interface{}{"type": "tool_result"}
		if event.Name != "" {
			metadata["tool"] = event.Name
		}
		if event.CallID != "" {
			metadata["call_id"] = event.CallID
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
		// Suppress refresh-token errors entirely — they are expected when
		// tokens are shared and showing them is alarming and unhelpful.
		if isRefreshTokenError(msg) {
			return
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

	case "item.completed":
		if event.Item != nil {
			switch event.Item.Type {
			case "agent_message":
				text := event.Item.Text
				if text != "" {
					if isDuplicateOutput("item.completed.agent_message", text, lastByType) {
						return
					}
					logCh <- agent.LogEntry{
						Timestamp: time.Now(),
						Level:     "output",
						Message:   text,
					}
					// Individual text blocks are persisted as separate output logs.
					*lastAssistant = text
					tryExtractConfidence(text, result)
				}
			case "command_execution":
				metadata := map[string]interface{}{
					"tool":   "command_execution",
					"input":  map[string]interface{}{"command": event.Item.Command},
					"status": event.Item.Status,
				}
				if event.Item.ExitCode != nil {
					metadata["exit_code"] = *event.Item.ExitCode
				}
				logCh <- agent.LogEntry{
					Timestamp: time.Now(),
					Level:     "tool_use",
					Message:   "using tool: command_execution",
					Metadata:  metadata,
				}
				if event.Item.AggregatedOutput != "" {
					logCh <- agent.LogEntry{
						Timestamp: time.Now(),
						Level:     "output",
						Message:   event.Item.AggregatedOutput,
						Metadata:  map[string]interface{}{"type": "tool_result"},
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

	case "turn.completed":
		if len(event.Usage) > 0 {
			var usage struct {
				InputTokens       int `json:"input_tokens"`
				CachedInputTokens int `json:"cached_input_tokens"`
				OutputTokens      int `json:"output_tokens"`
			}
			if err := json.Unmarshal(event.Usage, &usage); err == nil && usage.InputTokens > 0 {
				result.TokenUsage = agent.TokenUsage{
					InputTokens:  usage.InputTokens,
					OutputTokens: usage.OutputTokens,
				}
			}
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "debug",
			Message:   string(line),
		}

	default:
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "debug",
			Message:   string(line),
		}
	}
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

// codexItem represents a nested item inside a Codex CLI stream event
// (used by item.started / item.completed events).
type codexItem struct {
	ID               string `json:"id,omitempty"`
	Type             string `json:"type,omitempty"`
	Text             string `json:"text,omitempty"`
	Command          string `json:"command,omitempty"`
	AggregatedOutput string `json:"aggregated_output,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Status           string `json:"status,omitempty"`
}

// codexStreamEvent represents a single line of Codex CLI's stream-json output.
type codexStreamEvent struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Output    string          `json:"output,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Message   string          `json:"message,omitempty"`
	Stats     *struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"stats,omitempty"`
	Error string          `json:"error,omitempty"`
	Item  *codexItem      `json:"item,omitempty"`
	Usage json.RawMessage `json:"usage,omitempty"`
	// ThreadID is emitted by Codex on the `thread.started` event at the start
	// of each `--json` run. Codex's CLI exposes its conversation state under
	// the `thread` name; `codex exec resume <id>` accepts this same value as
	// the rollout/session identifier for deterministic resumes.
	ThreadID string `json:"thread_id,omitempty"`
}

// parseCodexStreamOutput processes the streaming JSONL output from Codex CLI,
// populates the AgentResult, and sends log entries with detailed tool use metadata.
// Falls back to parseCodexOutput for legacy single-object JSON responses.
func parseCodexStreamOutput(output []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return
	}

	// Detect legacy single-object JSON format (non-streaming).
	var legacyResp codexJSONOutput
	if err := json.Unmarshal(trimmed, &legacyResp); err == nil && legacyResp.Response != "" {
		parseCodexOutput(output, result, logCh)
		return
	}

	// Parse as streaming JSONL line by line, reusing parseCodexStreamLine
	// for consistent deduplication and refresh-token filtering. Note: this
	// intentionally adds dedup + refresh-token suppression to buffered
	// output parsing that previously lacked it.
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var summaryParts []string
	var lastAssistantContent string
	lastByType := make(map[string]string)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		parseCodexStreamLine(line, result, logCh, &summaryParts, lastByType, &lastAssistantContent)
	}

	if len(summaryParts) > 0 {
		result.Summary = strings.Join(summaryParts, "\n")
	} else if lastAssistantContent != "" {
		result.Summary = lastAssistantContent
	}
}

// isRefreshTokenError returns true if the message contains token-refresh error
// indicators that should be suppressed from user-visible logs.
func isRefreshTokenError(msg string) bool {
	return strings.Contains(msg, "refresh_token_reused") ||
		strings.Contains(msg, "Failed to refresh token") ||
		strings.Contains(msg, "refresh token was already used") ||
		strings.Contains(msg, "invalid_grant")
}

// filterRefreshTokenLines removes refresh-token error lines from stderr
// output. Splits by newline and removes only lines matching refresh-token
// errors, preserving any real error lines mixed in.
func filterRefreshTokenLines(stderr string) string {
	lines := strings.Split(stderr, "\n")
	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !isRefreshTokenError(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// shellEscapeCodex escapes single quotes in a path for use inside a
// single-quoted shell string. It is only used on internally-generated
// file paths (e.g. promptPath), never on user-supplied input.
func shellEscapeCodex(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
