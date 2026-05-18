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
	"github.com/assembledhq/143/internal/services/sessiondiff"
)

const (
	defaultLowTokenMax  = 50_000
	defaultHighTokenMax = 200_000

	claudeCodeFileEditPermissionArg = " --permission-mode acceptEdits"
)

// resolveTokenLimit returns the appropriate max token limit based on
// the token mode and optional org-specific context limits.
func resolveTokenLimit(mode string, limits *models.ContextLimits) int {
	low := defaultLowTokenMax
	high := defaultHighTokenMax
	if limits != nil {
		if limits.AgentLowTokenMax > 0 {
			low = limits.AgentLowTokenMax
		}
		if limits.AgentHighTokenMax > 0 {
			high = limits.AgentHighTokenMax
		}
	}
	if mode == "high" {
		return high
	}
	return low
}

// ClaudeCodeAdapter runs the Claude Code CLI inside a sandbox.
type ClaudeCodeAdapter struct {
	logger zerolog.Logger
}

// NewClaudeCodeAdapter creates a new adapter for running Claude Code CLI.
func NewClaudeCodeAdapter(logger zerolog.Logger) *ClaudeCodeAdapter {
	return &ClaudeCodeAdapter{
		logger: logger,
	}
}

// Name returns the agent identifier.
func (a *ClaudeCodeAdapter) Name() models.AgentType {
	return models.AgentTypeClaudeCode
}

// ResumeMode reports that Claude Code resumes prior turns by explicit session
// ID (captured from the `result` event's `session_id` field and threaded back
// into `claude --resume <id>`).
func (a *ClaudeCodeAdapter) ResumeMode() agent.SessionResumeMode {
	return agent.ResumeBySessionID
}

// PreparePrompt constructs the system and user prompts for Claude Code
// based on the issue context.
func (a *ClaudeCodeAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
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
		UsageHint: agent.TokenUsageHint{
			AgentType:   models.AgentTypeClaudeCode,
			BillingMode: agent.TokenBillingModeUnknown,
		},
	}, nil
}

// claudeCodeRuntimeProfile is shared by Execute and the test surface.
var claudeCodeRuntimeProfile = agent.AgentRuntimeProfile{
	Cancellation:      agent.DefaultCancellationSpec,
	PreferSplitOutput: true,
}

// RuntimeProfile declares Claude Code's interactive runtime requirements.
// Claude honors SIGINT cleanly so no TTY is required.
func (a *ClaudeCodeAdapter) RuntimeProfile() agent.AgentRuntimeProfile {
	return claudeCodeRuntimeProfile
}

// Execute runs the Claude Code CLI inside the sandbox and streams output.
func (a *ClaudeCodeAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	provider := agent.SandboxProviderFromContext(ctx)
	if provider == nil {
		return nil, fmt.Errorf("sandbox provider not found in context")
	}

	var cmd string
	effortArg := ""
	if prompt.ReasoningEffort != "" {
		effortArg = fmt.Sprintf(" --effort %s", shellEscapeSingle(string(prompt.ReasoningEffort)))
	}
	// Auto-approve file edits in the per-session gVisor sandbox without
	// bypassing every Claude Code permission check. This removes the
	// file-by-file approval loop while preserving Claude's remaining tool
	// checks for network-capable actions such as WebFetch and arbitrary Bash.
	if prompt.Continuation && prompt.ResumeSessionID != "" {
		settingsArg, envPrefix, err := prepareClaudeHumanInputHooks(ctx, provider, sandbox, prompt)
		if err != nil {
			return nil, err
		}
		// Subsequent turn with a known session ID: deterministic resume by
		// session id captured from a prior turn's `result` event. We avoid
		// `--continue`, which resumes whatever Claude session is newest in
		// the local data dir and is non-deterministic when stale entries
		// are present.
		msg := shellEscapeDouble(prompt.UserMessage)
		cmd = fmt.Sprintf(
			"%sclaude --print --output-format stream-json --verbose%s%s%s --resume %s \"%s\"",
			envPrefix,
			effortArg,
			settingsArg,
			claudeCodeFileEditPermissionArg,
			shellEscapeSingle(prompt.ResumeSessionID),
			msg,
		)
	} else {
		// Fresh exec — used for first turns and as the fallback for
		// continuation turns when the session ID was never captured (the
		// orchestrator embeds the prior conversation history into UserPrompt
		// in that case so the agent has the full context). Write the prompt
		// under $HOME (not WorkDir, which would pollute git status) and pipe
		// it into claude via stdin.
		promptContent := composeFreshExecPrompt(prompt.SystemPrompt, prompt.UserPrompt)
		promptPath := fmt.Sprintf("%s/.143-prompt.md", sandbox.HomeDir)
		if err := provider.WriteFile(ctx, sandbox, promptPath, []byte(promptContent)); err != nil {
			return nil, fmt.Errorf("write prompt file: %w", err)
		}
		settingsArg, envPrefix, err := prepareClaudeHumanInputHooks(ctx, provider, sandbox, prompt)
		if err != nil {
			return nil, err
		}
		cmd = fmt.Sprintf(
			"%sclaude --print --output-format stream-json --verbose%s%s%s < %s",
			envPrefix,
			effortArg,
			settingsArg,
			claudeCodeFileEditPermissionArg,
			promptPath,
		)
	}

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "starting Claude Code CLI",
		Metadata:  map[string]interface{}{"max_tokens": prompt.MaxTokens, "resume": prompt.Continuation},
	}

	result := &agent.AgentResult{}
	var summaryParts []string
	var lastAssistantContent string

	runResult, err := runInteractiveCommand(ctx, sandbox, InteractiveRunSpec{
		Cmd:     cmd,
		Profile: claudeCodeRuntimeProfile,
		OnStdout: func(line []byte) {
			parseClaudeStreamLine(line, result, logCh, &summaryParts, &lastAssistantContent)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("exec claude CLI: %w", err)
	}

	exitCode := runResult.ExitCode
	stderr := runResult.Stderr
	result.ExitCode = exitCode
	if len(summaryParts) > 0 {
		result.Summary = strings.Join(summaryParts, "\n")
	} else if lastAssistantContent != "" {
		// Fallback: if no "result" event arrived, use the last assistant text
		// so that ResultSummary (used for PR titles, etc.) is not empty.
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
		result.Error = fmt.Sprintf("claude CLI exited with code %d", exitCode)
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
		Message:   "Claude Code CLI completed",
		Metadata: map[string]interface{}{
			"exit_code":        exitCode,
			"confidence_score": result.ConfidenceScore,
		},
	}

	result.TokenUsage = agent.FinalizeTokenUsage(result.TokenUsage, prompt.UsageHint)

	return result, nil
}

func prepareClaudeHumanInputHooks(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox, prompt *agent.AgentPrompt) (string, string, error) {
	hookPath := fmt.Sprintf("%s/.143-claude-human-input-hook.mjs", sandbox.HomeDir)
	settingsPath := fmt.Sprintf("%s/.143-claude-settings.json", sandbox.HomeDir)

	if err := provider.WriteFile(ctx, sandbox, hookPath, []byte(claudeHumanInputHookScript)); err != nil {
		return "", "", fmt.Errorf("write Claude human input hook: %w", err)
	}

	preToolHooks := make([]map[string]any, 0, len(claudeHumanInputHookMatchers))
	for _, matcher := range claudeHumanInputHookMatchers {
		preToolHooks = append(preToolHooks, map[string]any{
			"matcher": matcher,
			"hooks": []map[string]any{
				{
					"type":    "command",
					"command": fmt.Sprintf("node '%s'", shellEscapeSingle(hookPath)),
				},
			},
		})
	}

	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": preToolHooks,
		},
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return "", "", fmt.Errorf("marshal Claude settings: %w", err)
	}
	if err := provider.WriteFile(ctx, sandbox, settingsPath, settingsJSON); err != nil {
		return "", "", fmt.Errorf("write Claude settings: %w", err)
	}

	envPrefix := fmt.Sprintf(
		"CLAUDE_143_HUMAN_INPUT_TOOLS='%s' ",
		shellEscapeSingle(strings.Join(claudeHumanInputHookMatchers, ",")),
	)
	if prompt != nil && prompt.HumanInputAnswer != nil {
		answerPath := fmt.Sprintf("%s/.143-claude-human-input-answer.json", sandbox.HomeDir)
		answerJSON, err := json.Marshal(prompt.HumanInputAnswer)
		if err != nil {
			return "", "", fmt.Errorf("marshal Claude human input answer: %w", err)
		}
		if err := provider.WriteFile(ctx, sandbox, answerPath, answerJSON); err != nil {
			return "", "", fmt.Errorf("write Claude human input answer: %w", err)
		}
		envPrefix += fmt.Sprintf("CLAUDE_143_HUMAN_INPUT_ANSWER='%s' ", shellEscapeSingle(answerPath))
	}

	return fmt.Sprintf(" --settings '%s'", shellEscapeSingle(settingsPath)), envPrefix, nil
}

var claudeHumanInputHookMatchers = []string{
	"AskUserQuestion",
	"Bash",
	"Edit",
	"MultiEdit",
	"Write",
	"WebFetch",
	"WebSearch",
	"NotebookEdit",
}

const claudeHumanInputHookScript = `#!/usr/bin/env node
import fs from "node:fs";

const stdin = await new Promise((resolve) => {
  let data = "";
  process.stdin.setEncoding("utf8");
  process.stdin.on("data", (chunk) => { data += chunk; });
  process.stdin.on("end", () => resolve(data));
});

let event = {};
try {
  event = stdin.trim() ? JSON.parse(stdin) : {};
} catch {
  event = {};
}

const toolName = event.tool_name || event.toolName || event.name || "";
const interceptedTools = new Set(
  String(process.env.CLAUDE_143_HUMAN_INPUT_TOOLS || "")
    .split(",")
    .map((tool) => tool.trim())
    .filter(Boolean)
);
if (!interceptedTools.has(toolName)) {
  process.exit(0);
}

function output(payload) {
  process.stdout.write(JSON.stringify(payload));
}

function selectedLabels(answer, input) {
  const selected = new Set(answer.selected_choice_ids || answer.SelectedChoiceIDs || []);
  const questions = Array.isArray(input.questions) ? input.questions : [];
  const labels = [];
  for (const question of questions) {
    const options = Array.isArray(question.options) ? question.options : [];
    for (const option of options) {
      const label = typeof option === "string" ? option : String(option.label || option.value || option.id || "");
      const id = String((typeof option === "string" ? option : option.id || option.value || label) || "")
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, "");
      if (selected.has(id) || selected.has(label)) {
        labels.push(label);
      }
    }
  }
  return labels;
}

function normalizedString(value) {
  return String(value || "").trim();
}

function collectEventRequestIDs(event, input) {
  return new Set([
    event.tool_use_id,
    event.toolUseID,
    event.tool_call_id,
    event.toolCallID,
    event.request_id,
    event.requestID,
    event.provider_request_id,
    event.providerRequestID,
    event.id,
    input.tool_use_id,
    input.toolUseID,
    input.tool_call_id,
    input.toolCallID,
    input.request_id,
    input.requestID,
    input.provider_request_id,
    input.providerRequestID,
    input.id
  ].map(normalizedString).filter(Boolean));
}

function answerProviderRequestID(answer) {
  return normalizedString(
    answer.provider_request_id ||
    answer.ProviderRequestID ||
    answer.providerRequestID
  );
}

function deferForHumanInput(reason) {
  output({
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "defer",
      permissionDecisionReason: reason || "143.dev will ask the user."
    }
  });
}

const answerPath = process.env.CLAUDE_143_HUMAN_INPUT_ANSWER;
if (!answerPath) {
  deferForHumanInput("143.dev will ask the user.");
  process.exit(0);
}

let answer = {};
let answerLoaded = false;
try {
  answer = JSON.parse(fs.readFileSync(answerPath, "utf8"));
  answerLoaded = true;
} catch {
  answer = {};
}

const input = event.tool_input || event.toolInput || {};
if (!answerLoaded) {
  deferForHumanInput("143.dev will ask the user.");
  process.exit(0);
}

const expectedRequestID = answerProviderRequestID(answer);
if (expectedRequestID && !collectEventRequestIDs(event, input).has(expectedRequestID)) {
  deferForHumanInput("143.dev will ask the user.");
  process.exit(0);
}

const updatedInput = { ...input };
const answerText = answer.answer_text || answer.AnswerText || null;
const selected = answer.selected_choice_ids || answer.SelectedChoiceIDs || [];
const payload = answer.answer_payload || answer.AnswerPayload || {};
const status = answer.status || answer.Status || "";
const rawDecision = payload.decision || answer.decision || answer.Decision || selected[0] || "";
const decision = String(rawDecision || "").toLowerCase();
const cancelled = status === "cancelled" || payload.cancelled === true || decision === "cancel" || decision === "cancelled";
const denied = cancelled || decision === "deny" || decision === "denied" || decision === "reject" || decision === "rejected";
if (answerText) {
  updatedInput.answer = answerText;
}
if (selected.length) {
  updatedInput.selected_choice_ids = selected;
}
const labels = selectedLabels(answer, input);
if (labels.length) {
  updatedInput.selected_options = labels;
}
if (payload.edited_input && typeof payload.edited_input === "object") {
  Object.assign(updatedInput, payload.edited_input);
}
if (payload.edited_command && typeof payload.edited_command === "string") {
  updatedInput.command = payload.edited_command;
}

try {
  fs.rmSync(answerPath, { force: true });
} catch {
  // Best effort: if the sandbox filesystem refuses deletion, still apply the
  // matching answer so the resumed Claude turn can complete.
}

if (denied) {
  output({
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: answerText || "143.dev denied this request."
    }
  });
  process.exit(0);
}

output({
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    permissionDecision: "allow",
    permissionDecisionReason: "143.dev supplied the human answer.",
    updatedInput
  }
});
`

// parseClaudeStreamLine processes a single non-empty stdout line from the
// Claude Code CLI's `--output-format stream-json` output.
//
// Claude Code wraps assistant text and tool_use blocks inside
// `message.content[]` (an Anthropic-API-shaped array of typed blocks), and
// echoes tool results back as `{"type":"user","message":{"content":[{"type":"tool_result",...}]}}`.
// Treating the top-level event as if it carried a flat `content` string
// produces empty assistant logs and dumps tool results as raw JSON cards in
// the UI, so we walk the nested blocks here and emit one structured LogEntry
// per block.
func parseClaudeStreamLine(line []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry, summaryParts *[]string, lastAssistant *string) {
	var event claudeStreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "output",
			Message:   string(line),
		}
		return
	}
	if event.SessionID != "" {
		result.AgentSessionID = event.SessionID
	}

	switch event.Type {
	case "system":
		// Init metadata (model, tools available, mcp servers, etc.) — keep at
		// debug so the timeline doesn't show a setup blob before any output.
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "debug",
			Message:   string(line),
		}

	case "assistant":
		emitClaudeAssistantBlocks(event, result, logCh, lastAssistant)

	case "user":
		emitClaudeUserBlocks(event, logCh)

	case "error":
		// `error` is not part of the documented stream-json schema but we keep
		// the case so unexpected-but-typed errors still surface as errors
		// rather than as raw debug blobs.
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "error",
			Message:   string(line),
		}

	case "result":
		summary := decodeClaudeResultSummary(event)
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "info",
			Message:   summary,
		}
		if req, ok := normalizeClaudeDeferredToolUse(event); ok {
			result.RequiresHumanInput = true
			logCh <- agent.LogEntry{
				Timestamp:  time.Now(),
				Level:      "human_input",
				Message:    req.Body,
				Metadata:   claudeHumanInputMetadata(req),
				HumanInput: &req,
			}
		}
		if summary != "" {
			*summaryParts = append(*summaryParts, summary)
			tryExtractConfidence(summary, result)
		}
		if len(event.Usage) > 0 {
			mergeTokenUsage(&result.TokenUsage, parseClaudeUsage(event.Usage))
		}
		if event.TotalCostUSD != nil {
			setDirectUSDCost(&result.TokenUsage, *event.TotalCostUSD, "claude_result_total_cost_usd")
		}
		if event.CostUSD != nil {
			setDirectUSDCost(&result.TokenUsage, *event.CostUSD, "claude_result_cost_usd")
		}

	default:
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "debug",
			Message:   string(line),
		}
	}
}

// emitClaudeAssistantBlocks fans out the `content[]` blocks inside an
// `assistant` event into one LogEntry per block. A single assistant turn can
// mix text and tool_use blocks, so we cannot collapse the array into one log.
func emitClaudeAssistantBlocks(event claudeStreamEvent, result *agent.AgentResult, logCh chan<- agent.LogEntry, lastAssistant *string) {
	if event.Message == nil || len(event.Message.Content) == 0 {
		return
	}
	for _, block := range event.Message.Content {
		switch block.Type {
		case "text":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   block.Text,
			}
			*lastAssistant = block.Text
			tryExtractConfidence(block.Text, result)

		case "tool_use":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "tool_use",
				Message:   fmt.Sprintf("using tool: %s", block.Name),
				Metadata:  claudeToolUseMetadata(block),
			}

		case "thinking":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "debug",
				Message:   block.Thinking,
				Metadata:  map[string]interface{}{"type": "thinking"},
			}

		default:
			if blob, err := json.Marshal(block); err == nil {
				logCh <- agent.LogEntry{
					Timestamp: time.Now(),
					Level:     "debug",
					Message:   string(blob),
				}
			}
		}
	}
}

// emitClaudeUserBlocks turns tool_result blocks (echoed back inside
// `{"type":"user",...}` events) into output logs tagged with
// `metadata.type=tool_result` so the frontend pairs them with the preceding
// tool_use card instead of rendering raw JSON.
func emitClaudeUserBlocks(event claudeStreamEvent, logCh chan<- agent.LogEntry) {
	if event.Message == nil {
		return
	}
	for _, block := range event.Message.Content {
		if block.Type != "tool_result" {
			continue
		}
		metadata := map[string]interface{}{"type": "tool_result"}
		if block.ToolUseID != "" {
			metadata["call_id"] = block.ToolUseID
		}
		if block.IsError {
			metadata["is_error"] = true
		}
		logCh <- agent.LogEntry{
			Timestamp: time.Now(),
			Level:     "output",
			Message:   decodeClaudeToolResultContent(block.Content),
			Metadata:  metadata,
		}
	}
}

// decodeClaudeResultSummary extracts the summary string from a `result`
// event's `result` field, which is a JSON-encoded string in current versions
// but historically has appeared as an object — fall back to the raw payload
// rather than dropping the summary on the floor.
func decodeClaudeResultSummary(event claudeStreamEvent) string {
	if len(event.Result) == 0 {
		return event.Content
	}
	var s string
	if err := json.Unmarshal(event.Result, &s); err == nil {
		return s
	}
	return string(event.Result)
}

// decodeClaudeToolResultContent renders a tool_result `content` payload to a
// printable string. The Anthropic schema allows either a bare string or an
// array of `{type, text}` blocks; we accept both.
func decodeClaudeToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			b.WriteString(blk.Text)
		}
		return b.String()
	}
	return string(raw)
}

func parseClaudeUsage(raw json.RawMessage) agent.TokenUsage {
	var usage struct {
		InputTokens         int `json:"input_tokens"`
		CachedInputTokens   int `json:"cached_input_tokens,omitempty"`
		CacheReadTokens     int `json:"cache_read_input_tokens,omitempty"`
		CacheCreationTokens int `json:"cache_creation_input_tokens,omitempty"`
		OutputTokens        int `json:"output_tokens"`
		TotalTokens         int `json:"total_tokens,omitempty"`
	}
	if err := json.Unmarshal(raw, &usage); err != nil {
		return agent.TokenUsage{}
	}
	cachedInputTokens := usage.CachedInputTokens
	if cachedInputTokens == 0 {
		cachedInputTokens = usage.CacheReadTokens
	}
	return agent.TokenUsage{
		Reported:            true,
		InputTokens:         usage.InputTokens,
		CachedInputTokens:   cachedInputTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		OutputTokens:        usage.OutputTokens,
		TotalTokens:         usage.TotalTokens,
	}
}

// WithSandboxProvider re-exports agent.WithSandboxProvider for backward compatibility.
// Callers should prefer agent.WithSandboxProvider directly.
func WithSandboxProvider(ctx context.Context, p agent.SandboxProvider) context.Context {
	return agent.WithSandboxProvider(ctx, p)
}

// claudeStreamEvent represents a single line of Claude Code's stream-json
// output. The shape mirrors the `--output-format stream-json` events: each
// turn produces one or more `assistant` events whose `message.content` is an
// Anthropic-API-shaped block array, with tool results echoed back via
// `user` events and a terminal `result` event carrying the summary.
type claudeStreamEvent struct {
	Type            string                 `json:"type"`
	Subtype         string                 `json:"subtype,omitempty"`
	Content         string                 `json:"content,omitempty"`
	Message         *claudeMessageBody     `json:"message,omitempty"`
	SessionID       string                 `json:"session_id,omitempty"`
	Result          json.RawMessage        `json:"result,omitempty"`
	Usage           json.RawMessage        `json:"usage,omitempty"`
	TotalCostUSD    *float64               `json:"total_cost_usd,omitempty"`
	CostUSD         *float64               `json:"cost_usd,omitempty"`
	IsError         bool                   `json:"is_error,omitempty"`
	StopReason      string                 `json:"stop_reason,omitempty"`
	DeferredToolUse *claudeDeferredToolUse `json:"deferred_tool_use,omitempty"`
}

// claudeMessageBody is the inner Anthropic Messages API payload carried by
// `assistant` and `user` events.
type claudeMessageBody struct {
	Role    string               `json:"role,omitempty"`
	Content []claudeContentBlock `json:"content,omitempty"`
}

// claudeContentBlock is a single typed block. Field set varies by Type:
// text / tool_use / tool_result / thinking each populate a different subset.
type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type claudeDeferredToolUse struct {
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

func normalizeClaudeDeferredToolUse(event claudeStreamEvent) (agent.HumanInputRequest, bool) {
	if event.DeferredToolUse == nil {
		return agent.HumanInputRequest{}, false
	}
	tool := *event.DeferredToolUse
	rawPayload, _ := json.Marshal(tool)
	if strings.EqualFold(tool.Name, "AskUserQuestion") {
		return normalizeClaudeAskUserQuestion(tool, rawPayload), true
	}
	title := fmt.Sprintf("Approve %s?", tool.Name)
	if tool.Name == "" {
		title = "Approve agent action?"
	}
	var contextText *string
	if len(tool.Input) > 0 {
		preview := strings.TrimSpace(string(tool.Input))
		if len(preview) > 1200 {
			preview = preview[:1200] + "..."
		}
		if preview != "" {
			contextText = &preview
		}
	}
	return agent.HumanInputRequest{
		ProviderRequestID: tool.ID,
		Kind:              models.HumanInputRequestKindToolApproval,
		Title:             title,
		Body:              "Claude needs approval before it can continue.",
		Context:           contextText,
		Choices: []models.HumanInputChoice{
			{ID: "approve", Label: "Approve", Kind: "positive"},
			{ID: "deny", Label: "Deny", Kind: "negative"},
		},
		ResponseSchema:  json.RawMessage(claudeToolApprovalResponseSchema),
		ProviderPayload: rawPayload,
	}, true
}

const claudeToolApprovalResponseSchema = `{
  "type": "object",
  "required": ["decision"],
  "properties": {
    "decision": { "type": "string", "enum": ["approve", "deny"] },
    "reason": { "type": "string" },
    "edited_command": { "type": "string" },
    "edited_input": { "type": "object" },
    "cancelled": { "type": "boolean" }
  },
  "additionalProperties": true
}`

func normalizeClaudeAskUserQuestion(tool claudeDeferredToolUse, rawPayload json.RawMessage) agent.HumanInputRequest {
	var input struct {
		Questions []struct {
			Header      string            `json:"header"`
			Question    string            `json:"question"`
			MultiSelect bool              `json:"multiSelect"`
			Options     []json.RawMessage `json:"options"`
		} `json:"questions"`
		Context string `json:"context"`
	}
	_ = json.Unmarshal(tool.Input, &input)

	title := "Claude needs input"
	body := "Claude is waiting for input."
	var contextText *string
	if strings.TrimSpace(input.Context) != "" {
		trimmed := strings.TrimSpace(input.Context)
		contextText = &trimmed
	}

	kind := models.HumanInputRequestKindFreeText
	var choices []models.HumanInputChoice
	seenChoiceIDs := map[string]int{}
	for i, question := range input.Questions {
		if i == 0 {
			if strings.TrimSpace(question.Header) != "" {
				title = strings.TrimSpace(question.Header)
			}
			if strings.TrimSpace(question.Question) != "" {
				body = strings.TrimSpace(question.Question)
			}
		}
		if len(question.Options) == 0 {
			continue
		}
		if question.MultiSelect {
			kind = models.HumanInputRequestKindMultiChoice
		} else if kind != models.HumanInputRequestKindMultiChoice {
			kind = models.HumanInputRequestKindSingleChoice
		}
		for _, rawOption := range question.Options {
			choice := normalizeClaudeQuestionOption(rawOption, seenChoiceIDs)
			if choice.ID != "" && choice.Label != "" {
				choices = append(choices, choice)
			}
		}
	}

	return agent.HumanInputRequest{
		ProviderRequestID: tool.ID,
		Kind:              kind,
		Title:             title,
		Body:              body,
		Context:           contextText,
		Choices:           choices,
		ProviderPayload:   rawPayload,
	}
}

func normalizeClaudeQuestionOption(raw json.RawMessage, seen map[string]int) models.HumanInputChoice {
	var label string
	var description string
	var id string
	var preview string
	var kind string
	var destructive bool

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		label = strings.TrimSpace(asString)
		id = slugChoiceID(label)
	} else {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err == nil {
			label = firstStringValue(obj, "label", "title", "name", "value", "id")
			id = firstStringValue(obj, "id", "value")
			description = firstStringValue(obj, "description", "detail", "subtitle")
			preview = firstStringValue(obj, "preview", "command", "diff")
			kind = firstStringValue(obj, "kind", "type")
			if v, ok := obj["destructive"].(bool); ok {
				destructive = v
			}
		}
		if id == "" {
			id = slugChoiceID(label)
		} else {
			id = slugChoiceID(id)
		}
	}
	if label == "" {
		label = id
	}
	if id == "" {
		id = "choice"
	}
	if seen[id] > 0 {
		seen[id]++
		id = fmt.Sprintf("%s-%d", id, seen[id])
	} else {
		seen[id] = 1
	}
	return models.HumanInputChoice{
		ID:          id,
		Label:       label,
		Description: description,
		Preview:     preview,
		Kind:        kind,
		Destructive: destructive,
	}
}

func firstStringValue(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := obj[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func slugChoiceID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func claudeHumanInputMetadata(req agent.HumanInputRequest) map[string]interface{} {
	return map[string]interface{}{
		"provider":            string(models.AgentTypeClaudeCode),
		"provider_request_id": req.ProviderRequestID,
		"request_kind":        string(req.Kind),
		"title":               req.Title,
	}
}

// claudeToolUseMetadata builds the metadata map for a tool_use log entry,
// preserving the tool name and the parsed input arguments so downstream
// consumers (UI, analytics) can render a descriptive label. Claude's Bash
// tool includes an `input.description` field that the frontend surfaces as
// the primary label — dropping Input here would discard that signal.
func claudeToolUseMetadata(block claudeContentBlock) map[string]interface{} {
	metadata := map[string]interface{}{"tool": block.Name}
	if len(block.Input) > 0 {
		var inputMap map[string]interface{}
		if err := json.Unmarshal(block.Input, &inputMap); err == nil {
			metadata["input"] = inputMap
		}
	}
	if block.ID != "" {
		metadata["call_id"] = block.ID
	}
	return metadata
}

// parseStreamOutput processes a buffer of streaming JSON output line by
// line, delegating each line to parseClaudeStreamLine so the buffered and
// streaming code paths can never disagree on event-shape interpretation.
func parseStreamOutput(output []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var summaryParts []string
	var lastAssistantContent string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		parseClaudeStreamLine(line, result, logCh, &summaryParts, &lastAssistantContent)
	}

	if len(summaryParts) > 0 {
		result.Summary = strings.Join(summaryParts, "\n")
	} else if lastAssistantContent != "" {
		result.Summary = lastAssistantContent
	}
}

// tryExtractConfidence attempts to find and parse a confidence JSON block
// from agent output text.
func tryExtractConfidence(text string, result *agent.AgentResult) {
	// Look for ```json ... ``` blocks containing confidence_score.
	idx := strings.Index(text, "\"confidence_score\"")
	if idx == -1 {
		return
	}

	// Find the enclosing braces.
	start := strings.LastIndex(text[:idx], "{")
	end := strings.Index(text[idx:], "}")
	if start == -1 || end == -1 {
		return
	}
	jsonStr := text[start : idx+end+1]

	var confidence struct {
		Score     float64  `json:"confidence_score"`
		Reasoning string   `json:"confidence_reasoning"`
		Risks     []string `json:"risk_factors"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &confidence); err != nil {
		return
	}

	if confidence.Score > 0 {
		result.ConfidenceScore = confidence.Score
		result.ConfidenceReasoning = confidence.Reasoning
		result.RiskFactors = confidence.Risks
	}
}

// collectDiff runs git diff inside the sandbox to capture changes.
// Returns an empty string (not an error) when the workspace is not a git repository,
// which happens when no repository was configured for the issue.
//
// The base commit SHA and target branch are read from sandbox.Metadata, which
// the orchestrator is responsible for populating both on the initial clone
// (RunAgent) and on every continue path (ContinueSession). The base SHA is the
// immutable fallback; the target branch lets sessiondiff.Collect compute a
// dynamic merge-base diff so integrating the target branch back into the
// working branch (e.g. `git pull origin main` or merging main to resolve PR
// conflicts) does not inflate the diff with target-branch changes. When the
// base SHA is missing, sessiondiff.Collect returns ErrNoBaseCommitSHA —
// adapters log and leave result.Diff unset so the persistence layer
// preserves the previous diff rather than clobbering it with an empty string.
//
// logger is forwarded to Collect so merge-base fallbacks (transient fetch
// failures, missing remote refs) show up in adapter-scoped logs at debug
// level — making it diagnosable when a session's Changes tab silently
// regresses to the inflated baseCommitSHA-snapshot view.
func collectDiff(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox, logger zerolog.Logger) (string, error) {
	baseCommitSHA := ""
	targetBranch := ""
	if sandbox != nil && sandbox.Metadata != nil {
		baseCommitSHA = sandbox.Metadata[sessiondiff.SandboxMetadataBaseCommitSHA]
		targetBranch = sandbox.Metadata[sessiondiff.SandboxMetadataTargetBranch]
	}
	return sessiondiff.Collect(ctx, provider, sandbox, baseCommitSHA, targetBranch, logger)
}
