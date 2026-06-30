// Package adapters contains implementations of the agent.AgentAdapter interface
// for specific coding agent CLIs.
package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// OpenCodeAdapter runs the OpenCode CLI inside a sandbox.
//
// OpenCode can route to multiple model providers. 143 stores the credential as
// ProviderOpenCode and only then maps it to provider-specific env vars, making
// OpenCode keys explicit instead of silently borrowing Codex/Claude/Gemini rows.
type OpenCodeAdapter struct {
	logger zerolog.Logger
}

// NewOpenCodeAdapter creates a new adapter for running OpenCode CLI.
func NewOpenCodeAdapter(logger zerolog.Logger) *OpenCodeAdapter {
	return &OpenCodeAdapter{logger: logger}
}

// Name returns the agent identifier.
func (a *OpenCodeAdapter) Name() models.AgentType {
	return models.AgentTypeOpenCode
}

// ResumeMode reports that OpenCode can continue an upstream session by ID.
func (a *OpenCodeAdapter) ResumeMode() agent.SessionResumeMode {
	return agent.ResumeBySessionID
}

// RuntimeProfile declares OpenCode's non-TTY streaming runtime requirements.
func (a *OpenCodeAdapter) RuntimeProfile() agent.AgentRuntimeProfile {
	return openCodeRuntimeProfile
}

// PreparePrompt constructs the prompts for OpenCode based on the issue context.
func (a *OpenCodeAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
	if input == nil {
		return nil, fmt.Errorf("agent input is required")
	}

	maxTokens := resolveTokenLimit(input.TokenMode, input.ContextLimits)

	return &agent.AgentPrompt{
		SystemPrompt: buildSystemPrompt(input),
		UserPrompt:   buildUserPrompt(input),
		MaxTokens:    maxTokens,
		Files:        extractFileHints(input),
		UsageHint: agent.TokenUsageHint{
			AgentType:   models.AgentTypeOpenCode,
			BillingMode: agent.TokenBillingModeUnknown,
		},
	}, nil
}

// Execute runs the OpenCode CLI inside the sandbox and streams output.
func (a *OpenCodeAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	result, err := runStreamingAgent(ctx, openCodeStreamingConfig, a.logger, sandbox, prompt, logCh)
	if err != nil || (result != nil && (result.ExitCode != 0 || result.Error != "")) {
		captureOpenCodeFailureLogs(ctx, sandbox, a.logger, logCh)
	}
	return result, err
}

var openCodeStreamingConfig = streamingAgentConfig{
	DisplayName: "OpenCode",
	CLIName:     "opencode",
	BuildCmd: func(escapedPromptPath string) string {
		return fmt.Sprintf(
			"opencode run --format json --dangerously-skip-permissions --agent build --model \"${OPENCODE_MODEL:-%s}\" --dir \"$PWD\" \"$(cat '%s')\"",
			models.OpenCodeModelGLM52,
			escapedPromptPath,
		)
	},
	BuildResumeCmd: func(escapedPromptPath, escapedResumeSessionID string) string {
		return fmt.Sprintf(
			"opencode run --format json --dangerously-skip-permissions --agent build --model \"${OPENCODE_MODEL:-%s}\" --session '%s' --dir \"$PWD\" \"$(cat '%s')\"",
			models.OpenCodeModelGLM52,
			escapedResumeSessionID,
			escapedPromptPath,
		)
	},
	ParseLine: parseOpenCodeStreamLine,
	Profile:   openCodeRuntimeProfile,
}

var openCodeRuntimeProfile = agent.AgentRuntimeProfile{
	Cancellation:      agent.DefaultCancellationSpec,
	PreferSplitOutput: true,
}

type openCodeStreamEvent struct {
	Type           string          `json:"type"`
	Role           string          `json:"role,omitempty"`
	Content        string          `json:"content,omitempty"`
	Message        string          `json:"message,omitempty"`
	Text           string          `json:"text,omitempty"`
	Name           string          `json:"name,omitempty"`
	Tool           string          `json:"tool,omitempty"`
	Input          json.RawMessage `json:"input,omitempty"`
	Output         string          `json:"output,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          json.RawMessage `json:"error,omitempty"`
	ID             string          `json:"id,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	SessionIDCamel string          `json:"sessionID,omitempty"`
	Part           json.RawMessage `json:"part,omitempty"`
	CostUSD        *float64        `json:"cost_usd,omitempty"`
	TotalCostUSD   *float64        `json:"total_cost_usd,omitempty"`
	Usage          *struct {
		InputTokens         int `json:"input_tokens"`
		CachedInputTokens   int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
		OutputTokens        int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

type openCodeStreamPart struct {
	ID        string            `json:"id,omitempty"`
	SessionID string            `json:"sessionID,omitempty"`
	MessageID string            `json:"messageID,omitempty"`
	Type      string            `json:"type,omitempty"`
	CallID    string            `json:"callID,omitempty"`
	Tool      string            `json:"tool,omitempty"`
	Text      string            `json:"text,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Cost      *float64          `json:"cost,omitempty"`
	State     openCodeToolState `json:"state,omitempty"`
	Tokens    *struct {
		Total     int `json:"total,omitempty"`
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens,omitempty"`
}

type openCodeToolState struct {
	Status   string          `json:"status,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Output   string          `json:"output,omitempty"`
	Title    string          `json:"title,omitempty"`
	Error    json.RawMessage `json:"error,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

func parseOpenCodeStreamLine(line []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry, summaryParts *[]string, lastAssistant *string) {
	var event openCodeStreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		text := string(line)
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "output", Message: text}
		*summaryParts = append(*summaryParts, text)
		return
	}

	if sessionID := firstNonEmpty(event.SessionID, event.SessionIDCamel); sessionID != "" {
		result.AgentSessionID = sessionID
	}

	switch event.Type {
	case "session", "session_start", "started":
		if sessionID := firstNonEmpty(event.SessionID, event.SessionIDCamel, event.ID); sessionID != "" {
			result.AgentSessionID = sessionID
		}
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "debug", Message: string(line)}
	case "step_start":
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "debug", Message: string(line)}
	case "step_finish":
		if part, ok := decodeOpenCodePart(event.Part); ok {
			mergeOpenCodeStepUsage(&result.TokenUsage, part)
		}
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "debug", Message: string(line)}
	case "assistant", "message", "text":
		if part, ok := decodeOpenCodePart(event.Part); ok && part.Type == "text" {
			content := strings.TrimSpace(part.Text)
			if content == "" {
				logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "debug", Message: string(line)}
				return
			}
			logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "output", Message: content}
			*summaryParts = append(*summaryParts, content)
			*lastAssistant = content
			return
		}
		content := firstNonEmpty(event.Content, event.Message, event.Text)
		if content == "" || (event.Type == "message" && event.Role != "" && event.Role != "assistant") {
			logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "debug", Message: string(line)}
			return
		}
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "output", Message: content}
		*summaryParts = append(*summaryParts, content)
		*lastAssistant = content
	case "tool_call", "tool_use":
		if part, ok := decodeOpenCodePart(event.Part); ok && part.Type == "tool" {
			emitOpenCodeToolPart(part, logCh)
			return
		}
		toolName := firstNonEmpty(event.Name, event.Tool)
		if toolName == "" {
			logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "debug", Message: string(line)}
			return
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
		toolName := firstNonEmpty(event.Name, event.Tool)
		output := event.Output
		if output == "" && len(event.Result) > 0 {
			output = string(event.Result)
		}
		metadata := map[string]interface{}{"type": "tool_result"}
		if toolName != "" {
			metadata["tool"] = toolName
		}
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "output", Message: output, Metadata: metadata}
	case "usage", "result", "done":
		if event.Usage != nil {
			mergeTokenUsage(&result.TokenUsage, agent.TokenUsage{
				Reported:            true,
				InputTokens:         event.Usage.InputTokens,
				CachedInputTokens:   event.Usage.CachedInputTokens,
				CacheCreationTokens: event.Usage.CacheCreationTokens,
				OutputTokens:        event.Usage.OutputTokens,
			})
		}
		if event.TotalCostUSD != nil {
			setDirectUSDCost(&result.TokenUsage, *event.TotalCostUSD, "opencode_total_cost_usd")
		}
		if event.CostUSD != nil {
			setDirectUSDCost(&result.TokenUsage, *event.CostUSD, "opencode_cost_usd")
		}
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "info", Message: firstNonEmpty(event.Content, event.Message)}
	case "error":
		if sessionID := firstNonEmpty(event.SessionID, event.SessionIDCamel, event.ID); sessionID != "" {
			result.AgentSessionID = sessionID
		}
		result.Error = firstNonEmpty(openCodeEventErrorMessage(event), event.Message, event.Content, "unknown error")
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "error", Message: result.Error}
	case "permission", "permission_request":
		result.Error = openCodePermissionError(event)
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "error", Message: result.Error}
	default:
		logCh <- agent.LogEntry{Timestamp: time.Now(), Level: "debug", Message: string(line)}
	}
}

func decodeOpenCodePart(raw json.RawMessage) (openCodeStreamPart, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return openCodeStreamPart{}, false
	}
	var part openCodeStreamPart
	if err := json.Unmarshal(raw, &part); err != nil {
		return openCodeStreamPart{}, false
	}
	return part, true
}

func emitOpenCodeToolPart(part openCodeStreamPart, logCh chan<- agent.LogEntry) {
	toolName := strings.TrimSpace(part.Tool)
	if toolName == "" {
		return
	}

	metadata := map[string]interface{}{"tool": toolName}
	if part.CallID != "" {
		metadata["call_id"] = part.CallID
	}
	if input, ok := openCodeRawObject(part.State.Input); ok {
		metadata["input"] = input
	}
	if part.State.Status != "" {
		metadata["status"] = part.State.Status
	}

	message := "using tool: " + toolName
	if part.State.Title != "" {
		message = part.State.Title
	}
	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "tool_use",
		Message:   message,
		Metadata:  metadata,
	}

	resultMetadata := map[string]interface{}{"type": "tool_result", "tool": toolName}
	if part.CallID != "" {
		resultMetadata["call_id"] = part.CallID
	}
	resultMessage := part.State.Output
	if part.State.Status == "error" {
		resultMessage = firstNonEmpty(openCodeRawMessage(part.State.Error), resultMessage)
		resultMetadata["status"] = "error"
	} else if part.State.Status != "" {
		resultMetadata["status"] = part.State.Status
	}
	if resultMessage == "" {
		resultMessage = firstNonEmpty(openCodeRawMessage(part.State.Metadata), part.State.Title)
	}
	if resultMessage == "" {
		return
	}
	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "output",
		Message:   resultMessage,
		Metadata:  resultMetadata,
	}
}

func openCodeRawObject(raw json.RawMessage) (map[string]interface{}, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

func openCodeRawMessage(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	return string(raw)
}

func mergeOpenCodeStepUsage(dst *agent.TokenUsage, part openCodeStreamPart) {
	if part.Tokens != nil {
		dst.Reported = true
		dst.InputTokens += part.Tokens.Input
		dst.CachedInputTokens += part.Tokens.Cache.Read
		dst.CacheCreationTokens += part.Tokens.Cache.Write
		dst.OutputTokens += part.Tokens.Output
		dst.TotalTokens += part.Tokens.Total
	}
	if part.Cost != nil {
		addOpenCodeStepCost(dst, *part.Cost)
	}
}

func addOpenCodeStepCost(dst *agent.TokenUsage, amount float64) {
	dst.Reported = true
	dst.TotalCostUSD += amount
	if dst.Cost == nil || dst.Cost.Unit != agent.TokenCostUnitUSD || dst.Cost.Source != agent.TokenCostSourceDirect {
		dst.Cost = &agent.TokenCost{
			Unit:   agent.TokenCostUnitUSD,
			Source: agent.TokenCostSourceDirect,
		}
	}
	dst.Cost.Amount += amount
	dst.Cost.Detail = "opencode_step_finish_cost"
}

func openCodePermissionError(event openCodeStreamEvent) string {
	message := firstNonEmpty(openCodeEventErrorMessage(event), event.Message, event.Content)
	if message != "" {
		return "OpenCode requested interactive permission: " + message
	}
	if tool := firstNonEmpty(event.Tool, event.Name); tool != "" {
		return "OpenCode requested interactive permission for " + tool
	}
	return "OpenCode requested interactive permission"
}

type openCodeErrorPayload struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Ref     string `json:"ref"`
	Data    struct {
		Message string `json:"message"`
		Ref     string `json:"ref"`
	} `json:"data"`
}

func openCodeEventErrorMessage(event openCodeStreamEvent) string {
	raw := bytes.TrimSpace(event.Error)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}

	var payload openCodeErrorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return string(raw)
	}

	name := strings.TrimSpace(payload.Name)
	message := strings.TrimSpace(firstNonEmpty(payload.Message, payload.Data.Message))
	ref := strings.TrimSpace(firstNonEmpty(payload.Ref, payload.Data.Ref))

	switch {
	case name != "" && message != "":
		text = name + ": " + message
	case message != "":
		text = message
	case name != "":
		text = name
	default:
		text = string(raw)
	}
	if ref != "" {
		text += " (ref: " + ref + ")"
	}
	return text
}

const (
	openCodeFailureLogMaxFiles = 3
	openCodeFailureLogMaxBytes = 64 * 1024
)

var (
	openCodeBearerSecretPattern         = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	openCodeQuotedSecretPattern         = regexp.MustCompile(`(?i)(["'])([A-Za-z0-9_.-]*(?:api[_-]?key|authorization|token|secret|password)[A-Za-z0-9_.-]*)(["']\s*:\s*["'])[^"']*(["'])`)
	openCodeAssignedQuotedSecretPattern = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:api[_-]?key|authorization|token|secret|password)[A-Za-z0-9_.-]*)(\s*[:=]\s*["'])[^"']*(["'])`)
	openCodeNamedSecretPattern          = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:api[_-]?key|authorization|token|secret|password)[A-Za-z0-9_.-]*)(\s*[:=]\s*)[^"'\s,}]+`)
	openCodeSecretPatterns              = []*regexp.Regexp{
		regexp.MustCompile(`sk-or-v1-[A-Za-z0-9_-]+`),
		regexp.MustCompile(`sk-or-[A-Za-z0-9_-]+`),
		regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]+`),
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`AIza[0-9A-Za-z_-]{20,}`),
		regexp.MustCompile(`ghp_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]+`),
		regexp.MustCompile(`xai-[A-Za-z0-9_-]{20,}`),
	}
)

func captureOpenCodeFailureLogs(ctx context.Context, sandbox *agent.Sandbox, logger zerolog.Logger, logCh chan<- agent.LogEntry) {
	provider := agent.SandboxProviderFromContext(ctx)
	if provider == nil || sandbox == nil || strings.TrimSpace(sandbox.HomeDir) == "" {
		return
	}

	logPaths, err := listOpenCodeFailureLogPaths(ctx, provider, sandbox)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to list OpenCode failure logs")
		return
	}
	for _, logPath := range logPaths {
		raw, originalBytes, truncated, err := readOpenCodeFailureLogTail(ctx, provider, sandbox, logPath)
		if err != nil {
			logger.Warn().Err(err).Str("path", logPath).Msg("failed to read OpenCode failure log")
			continue
		}

		content := strings.TrimSpace(redactOpenCodeDiagnosticLog(string(raw)))
		if content == "" {
			content = "<empty OpenCode log file>"
		}

		scope := "full"
		if truncated {
			scope = fmt.Sprintf("last %d bytes", openCodeFailureLogMaxBytes)
		}

		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "error",
			Message:   fmt.Sprintf("OpenCode failure log (%s, %s):\n%s", logPath, scope, content),
			Metadata: map[string]interface{}{
				"source":         "opencode_log",
				"diagnostic":     "opencode_failure_log",
				"path":           logPath,
				"truncated":      truncated,
				"original_bytes": originalBytes,
			},
		}
	}
}

func readOpenCodeFailureLogTail(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox, logPath string) ([]byte, int, bool, error) {
	escapedLogPath := shellEscapeSingle(logPath)

	var sizeStdout, sizeStderr bytes.Buffer
	sizeCmd := fmt.Sprintf("wc -c < '%s'", escapedLogPath)
	sizeExit, err := provider.Exec(ctx, sandbox, sizeCmd, &sizeStdout, &sizeStderr)
	if err != nil {
		return nil, 0, false, fmt.Errorf("stat OpenCode log file: %w", err)
	}
	if sizeExit != 0 {
		return nil, 0, false, fmt.Errorf("stat OpenCode log file exited with code %d: %s", sizeExit, strings.TrimSpace(sizeStderr.String()))
	}

	originalBytes, err := strconv.Atoi(strings.TrimSpace(sizeStdout.String()))
	if err != nil {
		return nil, 0, false, fmt.Errorf("parse OpenCode log file size %q: %w", strings.TrimSpace(sizeStdout.String()), err)
	}

	var tailStdout, tailStderr bytes.Buffer
	tailCmd := fmt.Sprintf("tail -c %d '%s'", openCodeFailureLogMaxBytes, escapedLogPath)
	tailExit, err := provider.Exec(ctx, sandbox, tailCmd, &tailStdout, &tailStderr)
	if err != nil {
		return nil, originalBytes, false, fmt.Errorf("tail OpenCode log file: %w", err)
	}
	if tailExit != 0 {
		return nil, originalBytes, false, fmt.Errorf("tail OpenCode log file exited with code %d: %s", tailExit, strings.TrimSpace(tailStderr.String()))
	}

	return tailStdout.Bytes(), originalBytes, originalBytes > openCodeFailureLogMaxBytes, nil
}

func listOpenCodeFailureLogPaths(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox) ([]string, error) {
	logDir := strings.TrimRight(sandbox.HomeDir, "/") + "/.local/share/opencode/log"
	escapedLogDir := shellEscapeSingle(logDir)
	cmd := fmt.Sprintf(
		"if [ -d '%s' ]; then find '%s' -maxdepth 1 -type f -printf '%%T@ %%p\\n' 2>/dev/null | sort -nr | head -n %d | cut -d' ' -f2-; fi",
		escapedLogDir,
		escapedLogDir,
		openCodeFailureLogMaxFiles,
	)

	var stdout, stderr bytes.Buffer
	exitCode, err := provider.Exec(ctx, sandbox, cmd, &stdout, &stderr)
	if err != nil {
		return nil, fmt.Errorf("discover OpenCode log files: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("discover OpenCode log files exited with code %d: %s", exitCode, strings.TrimSpace(stderr.String()))
	}

	var paths []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		path := strings.TrimSpace(line)
		if path == "" {
			continue
		}
		if path != logDir && !strings.HasPrefix(path, logDir+"/") {
			continue
		}
		paths = append(paths, path)
		if len(paths) >= openCodeFailureLogMaxFiles {
			break
		}
	}
	return paths, nil
}

func redactOpenCodeDiagnosticLog(input string) string {
	output := strings.ReplaceAll(input, "\x00", "")
	output = openCodeBearerSecretPattern.ReplaceAllString(output, "Bearer [REDACTED]")
	output = openCodeQuotedSecretPattern.ReplaceAllString(output, "${1}${2}${3}[REDACTED]${4}")
	output = openCodeAssignedQuotedSecretPattern.ReplaceAllString(output, "${1}${2}[REDACTED]${3}")
	output = openCodeNamedSecretPattern.ReplaceAllString(output, "${1}${2}[REDACTED]")
	for _, pattern := range openCodeSecretPatterns {
		output = pattern.ReplaceAllString(output, "[REDACTED]")
	}
	return output
}
