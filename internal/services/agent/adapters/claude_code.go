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
	}, nil
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
		effortArg = fmt.Sprintf(" --effort %s", prompt.ReasoningEffort)
	}
	if prompt.Continuation && prompt.ResumeSessionID != "" {
		// Subsequent turn with a known session ID: deterministic resume by
		// session id captured from a prior turn's `result` event. We avoid
		// `--continue`, which resumes whatever Claude session is newest in
		// the local data dir and is non-deterministic when stale entries
		// are present.
		msg := shellEscapeDouble(prompt.UserMessage)
		cmd = fmt.Sprintf(
			"claude --print --output-format stream-json --verbose%s --resume %s \"%s\"",
			effortArg,
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
		promptContent := fmt.Sprintf("%s\n\n---\n\n%s", prompt.SystemPrompt, prompt.UserPrompt)
		promptPath := fmt.Sprintf("%s/.143-prompt.md", sandbox.HomeDir)
		if err := provider.WriteFile(ctx, sandbox, promptPath, []byte(promptContent)); err != nil {
			return nil, fmt.Errorf("write prompt file: %w", err)
		}
		cmd = fmt.Sprintf(
			"claude --print --output-format stream-json --verbose%s < %s",
			effortArg,
			promptPath,
		)
	}

	logCh <- agent.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "starting Claude Code CLI",
		Metadata:  map[string]interface{}{"max_tokens": prompt.MaxTokens, "resume": prompt.Continuation},
	}

	// Execute with real-time streaming.
	result := &agent.AgentResult{}
	var stderr bytes.Buffer
	var summaryParts []string
	var lastAssistantContent string

	exitCode, err := provider.ExecStream(ctx, sandbox, cmd, func(line []byte) {
		if len(bytes.TrimSpace(line)) == 0 {
			return
		}

		var event claudeStreamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   string(line),
			}
			return
		}

		switch event.Type {
		case "assistant":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   event.Content,
			}
			// Individual assistant text blocks are persisted as separate output
			// logs — don't merge them into the summary. Track the last one as
			// a fallback in case no "result" event arrives.
			lastAssistantContent = event.Content
			tryExtractConfidence(event.Content, result)

		case "tool_use":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "tool_use",
				Message:   fmt.Sprintf("using tool: %s", event.Tool),
				Metadata:  claudeToolUseMetadata(event),
			}

		case "tool_result":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   string(event.Result),
				Metadata:  map[string]interface{}{"type": "tool_result"},
			}

		case "error":
			msg := event.Message
			if msg == "" {
				msg = event.Content
			}
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "error",
				Message:   msg,
			}

		case "result":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "info",
				Message:   event.Content,
			}
			summaryParts = append(summaryParts, event.Content)
			tryExtractConfidence(event.Content, result)
			if len(event.Result) > 0 {
				var usage agent.TokenUsage
				if err := json.Unmarshal(event.Result, &usage); err == nil && usage.InputTokens > 0 {
					result.TokenUsage = usage
				}
			}
			// Extract session ID from result event if present.
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
	}, &stderr)
	if err != nil {
		return nil, fmt.Errorf("exec claude CLI: %w", err)
	}

	result.ExitCode = exitCode
	if len(summaryParts) > 0 {
		result.Summary = strings.Join(summaryParts, "\n")
	} else if lastAssistantContent != "" {
		// Fallback: if no "result" event arrived, use the last assistant text
		// so that ResultSummary (used for PR titles, etc.) is not empty.
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
		result.Error = fmt.Sprintf("claude CLI exited with code %d", exitCode)
		if stderr.Len() > 0 {
			result.Error += ": " + stderr.String()
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

	return result, nil
}

// WithSandboxProvider re-exports agent.WithSandboxProvider for backward compatibility.
// Callers should prefer agent.WithSandboxProvider directly.
func WithSandboxProvider(ctx context.Context, p agent.SandboxProvider) context.Context {
	return agent.WithSandboxProvider(ctx, p)
}

// claudeStreamEvent represents a single line of Claude Code's stream-json output.
type claudeStreamEvent struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	Message   string          `json:"message,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
}

// claudeToolUseMetadata builds the metadata map for a tool_use log entry,
// preserving the tool name and the parsed input arguments so downstream
// consumers (UI, analytics) can render a descriptive label. Claude's Bash
// tool includes an `input.description` field that the frontend surfaces as
// the primary label — dropping Input here would discard that signal.
func claudeToolUseMetadata(event claudeStreamEvent) map[string]interface{} {
	metadata := map[string]interface{}{"tool": event.Tool}
	if len(event.Input) > 0 {
		var inputMap map[string]interface{}
		if err := json.Unmarshal(event.Input, &inputMap); err == nil {
			metadata["input"] = inputMap
		}
	}
	return metadata
}

// parseStreamOutput processes the streaming JSON output line by line,
// populates the AgentResult, and sends log entries.
func parseStreamOutput(output []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var summaryParts []string
	var lastAssistantContent string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var event claudeStreamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Not JSON — emit as raw output.
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   string(line),
			}
			continue
		}

		switch event.Type {
		case "assistant":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   event.Content,
			}
			// Individual assistant text blocks are persisted as separate output
			// logs — don't merge them into the summary. Track the last one as
			// a fallback in case no "result" event arrives.
			lastAssistantContent = event.Content
			tryExtractConfidence(event.Content, result)

		case "tool_use":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "tool_use",
				Message:   fmt.Sprintf("using tool: %s", event.Tool),
				Metadata:  claudeToolUseMetadata(event),
			}

		case "tool_result":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "output",
				Message:   string(event.Result),
				Metadata:  map[string]interface{}{"type": "tool_result"},
			}

		case "error":
			msg := event.Message
			if msg == "" {
				msg = event.Content
			}
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "error",
				Message:   msg,
			}

		case "result":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "info",
				Message:   event.Content,
			}
			summaryParts = append(summaryParts, event.Content)
			tryExtractConfidence(event.Content, result)
			// Parse token usage if present.
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
