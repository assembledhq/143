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
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/agent"
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

// PreparePrompt constructs the system and user prompts for Claude Code
// based on the issue context.
func (a *ClaudeCodeAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
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

// Execute runs the Claude Code CLI inside the sandbox and streams output.
func (a *ClaudeCodeAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
	provider := agent.SandboxProviderFromContext(ctx)
	if provider == nil {
		return nil, fmt.Errorf("sandbox provider not found in context")
	}

	var cmd string
	if prompt.Continuation {
		// Subsequent turn: resume the latest session with --continue.
		msg := shellEscapeDouble(prompt.UserMessage)
		cmd = fmt.Sprintf(
			"claude --print --output-format stream-json --continue --max-tokens %d --prompt \"%s\"",
			prompt.MaxTokens,
			msg,
		)
	} else {
		// First turn: write prompt file and run fresh.
		promptContent := fmt.Sprintf("%s\n\n---\n\n%s", prompt.SystemPrompt, prompt.UserPrompt)
		promptPath := fmt.Sprintf("%s/.143-prompt.md", sandbox.WorkDir)
		if err := provider.WriteFile(ctx, sandbox, promptPath, []byte(promptContent)); err != nil {
			return nil, fmt.Errorf("write prompt file: %w", err)
		}
		cmd = fmt.Sprintf(
			"claude --print --output-format stream-json --max-tokens %d --prompt-file %s",
			prompt.MaxTokens,
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
			// logs — don't merge them into the summary. The summary only
			// contains the final "result" event content.
			tryExtractConfidence(event.Content, result)

		case "tool_use":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "tool_use",
				Message:   fmt.Sprintf("using tool: %s", event.Tool),
				Metadata:  map[string]interface{}{"tool": event.Tool},
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
	result.Summary = strings.Join(summaryParts, "\n")

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
	diff, err := collectDiff(ctx, provider, sandbox)
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

// buildSystemPrompt constructs the system prompt with instructions and context.
func buildSystemPrompt(input *agent.AgentInput) string {
	var b strings.Builder

	// Manual sessions skip the bug-fixing template — the user's raw message
	// is the entire prompt. Only inject repo conventions and integration
	// skills so the agent knows what tools and patterns are available.
	if input.Issue.Source != models.IssueSourceManual {
		base := prompts.CodingTaskPreamble()
		b.WriteString(base)
		if !strings.HasSuffix(base, "\n\n") {
			b.WriteString("\n\n")
		}
	}

	// Repo conventions from context docs.
	if len(input.ContextDocs) > 0 {
		b.WriteString("## Repository Conventions\n\n")
		for _, doc := range input.ContextDocs {
			b.WriteString(doc)
			b.WriteString("\n\n")
		}
	}

	// Revision context: inject reviewer feedback for revision runs.
	if input.RevisionContext != nil {
		b.WriteString("## Revision Instructions\n\n")
		b.WriteString("This is a REVISION run. A previous fix was submitted as a PR, and reviewers have ")
		b.WriteString("requested changes. Apply the feedback below to improve the fix.\n\n")
		if input.RevisionContext.FormattedFeedback != "" {
			b.WriteString("### Reviewer Feedback\n\n")
			b.WriteString(input.RevisionContext.FormattedFeedback)
			b.WriteString("\n\n")
		}
		if input.RevisionContext.CommentSummary != "" {
			b.WriteString("### Summary\n\n")
			b.WriteString(input.RevisionContext.CommentSummary)
			b.WriteString("\n\n")
		}
		if input.RevisionContext.PreviousDiff != "" {
			b.WriteString("### Previous Diff\n\n")
			b.WriteString("```diff\n")
			b.WriteString(input.RevisionContext.PreviousDiff)
			b.WriteString("\n```\n\n")
		}
	}

	// Integration tools: inject CLI skills doc so the agent knows what's available.
	if input.IntegrationSkills != "" {
		b.WriteString(input.IntegrationSkills)
		b.WriteString("\n\n")
	}

	// PM context: inject PM guidance when available (never set for manual sessions).
	if input.PMContext != nil {
		b.WriteString("## Product Manager Analysis\n\n")
		if input.PMContext.Reasoning != "" {
			b.WriteString("**Why this is a priority:** ")
			b.WriteString(input.PMContext.Reasoning)
			b.WriteString("\n\n")
		}
		if input.PMContext.Approach != "" {
			b.WriteString("**Suggested approach:** ")
			b.WriteString(input.PMContext.Approach)
			b.WriteString("\n\n")
		}
		if input.PMContext.Risk != "" {
			b.WriteString("**Risk to watch for:** ")
			b.WriteString(input.PMContext.Risk)
			b.WriteString("\n\n")
		}
		if len(input.PMContext.RelatedIssues) > 0 {
			b.WriteString("**Related issues (same root cause):**\n")
			for _, issue := range input.PMContext.RelatedIssues {
				b.WriteString("- ")
				b.WriteString(issue)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		if input.PMContext.RootCause != "" {
			b.WriteString("**Root cause hypothesis:** ")
			b.WriteString(input.PMContext.RootCause)
			b.WriteString("\n\n")
		}
	}

	return b.String()
}

// buildUserPrompt constructs the user prompt with issue-specific details.
func buildUserPrompt(input *agent.AgentInput) string {
	// Manual sessions: pass through the user's raw message without any wrapping.
	if input.Issue.Source == models.IssueSourceManual {
		if input.Issue.Description != nil {
			return *input.Issue.Description
		}
		return input.Issue.Title
	}

	var b strings.Builder

	b.WriteString(fmt.Sprintf("## Issue: %s\n\n", input.Issue.Title))

	if input.Issue.Description != nil && *input.Issue.Description != "" {
		b.WriteString(fmt.Sprintf("### Description\n\n%s\n\n", *input.Issue.Description))
	}

	// Add stack trace from raw data if this is a Sentry issue.
	if input.Issue.Source == models.IssueSourceSentry {
		stackTrace := extractStackTrace(input.Issue.RawData)
		if stackTrace != "" {
			b.WriteString(fmt.Sprintf("### Stack Trace\n\n```\n%s\n```\n\n", stackTrace))
		}
	}

	// Customer impact context.
	if input.Issue.OccurrenceCount > 0 || input.Issue.AffectedCustomerCount > 0 {
		b.WriteString("### Customer Impact\n\n")
		if input.Issue.OccurrenceCount > 0 {
			b.WriteString(fmt.Sprintf("- Occurrences: %d\n", input.Issue.OccurrenceCount))
		}
		if input.Issue.AffectedCustomerCount > 0 {
			b.WriteString(fmt.Sprintf("- Affected customers: %d\n", input.Issue.AffectedCustomerCount))
		}
		b.WriteString("\n")
	}

	// Severity.
	if input.Issue.Severity != "" {
		b.WriteString(fmt.Sprintf("- Severity: %s\n\n", input.Issue.Severity))
	}

	// Complexity context.
	if input.ComplexityEstimate != nil {
		b.WriteString("### Complexity Assessment\n\n")
		b.WriteString(fmt.Sprintf("- Tier: %d\n", input.ComplexityEstimate.Tier))
		b.WriteString(fmt.Sprintf("- Reasoning: %s\n\n", input.ComplexityEstimate.Reasoning))
	}

	return b.String()
}

// extractFileHints parses the issue's raw data for file paths from
// Sentry stack trace frames.
func extractFileHints(input *agent.AgentInput) []string {
	if input.Issue.Source != models.IssueSourceSentry || len(input.Issue.RawData) == 0 {
		return nil
	}

	var rawData struct {
		Entries []struct {
			Type string `json:"type"`
			Data struct {
				Values []struct {
					Stacktrace struct {
						Frames []struct {
							Filename string `json:"filename"`
							AbsPath  string `json:"absPath"`
						} `json:"frames"`
					} `json:"stacktrace"`
				} `json:"values"`
			} `json:"data"`
		} `json:"entries"`
	}

	if err := json.Unmarshal(input.Issue.RawData, &rawData); err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var files []string
	for _, entry := range rawData.Entries {
		if entry.Type != "exception" {
			continue
		}
		for _, value := range entry.Data.Values {
			for _, frame := range value.Stacktrace.Frames {
				path := frame.Filename
				if frame.AbsPath != "" {
					path = frame.AbsPath
				}
				if path == "" || seen[path] {
					continue
				}
				// Skip standard library / vendor frames.
				if strings.HasPrefix(path, "<") || strings.Contains(path, "node_modules") || strings.Contains(path, "site-packages") {
					continue
				}
				seen[path] = true
				files = append(files, path)
			}
		}
	}

	return files
}

// extractStackTrace pulls a human-readable stack trace from Sentry raw data.
func extractStackTrace(rawData json.RawMessage) string {
	if len(rawData) == 0 {
		return ""
	}

	var data struct {
		Entries []struct {
			Type string `json:"type"`
			Data struct {
				Values []struct {
					Type       string `json:"type"`
					Value      string `json:"value"`
					Stacktrace struct {
						Frames []struct {
							Filename string `json:"filename"`
							Function string `json:"function"`
							LineNo   int    `json:"lineNo"`
						} `json:"frames"`
					} `json:"stacktrace"`
				} `json:"values"`
			} `json:"data"`
		} `json:"entries"`
	}

	if err := json.Unmarshal(rawData, &data); err != nil {
		return ""
	}

	var b strings.Builder
	for _, entry := range data.Entries {
		if entry.Type != "exception" {
			continue
		}
		for _, value := range entry.Data.Values {
			b.WriteString(fmt.Sprintf("%s: %s\n", value.Type, value.Value))
			for _, frame := range value.Stacktrace.Frames {
				b.WriteString(fmt.Sprintf("  at %s (%s:%d)\n", frame.Function, frame.Filename, frame.LineNo))
			}
		}
	}

	return b.String()
}

// claudeStreamEvent represents a single line of Claude Code's stream-json output.
type claudeStreamEvent struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	Message   string          `json:"message,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
}

// parseStreamOutput processes the streaming JSON output line by line,
// populates the AgentResult, and sends log entries.
func parseStreamOutput(output []byte, result *agent.AgentResult, logCh chan<- agent.LogEntry) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var summaryParts []string

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
			// logs — don't merge them into the summary.
			tryExtractConfidence(event.Content, result)

		case "tool_use":
			logCh <- agent.LogEntry{
				Timestamp: time.Now(),
				Level:     "tool_use",
				Message:   fmt.Sprintf("using tool: %s", event.Tool),
				Metadata:  map[string]interface{}{"tool": event.Tool},
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

	result.Summary = strings.Join(summaryParts, "\n")
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

// shellEscapeDouble escapes characters for safe use inside double-quoted shell strings.
// Handles backslash, double quote, dollar sign, backtick, and exclamation mark
// (history expansion in bash, harmless in POSIX sh but escaped for safety).
func shellEscapeDouble(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`$`, `\$`,
		"`", "\\`",
		`!`, `\!`,
	)
	return r.Replace(s)
}

// collectDiff runs git diff inside the sandbox to capture changes.
// Returns an empty string (not an error) when the workspace is not a git repository,
// which happens when no repository was configured for the issue.
func collectDiff(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox) (string, error) {
	// Check if the workspace is a git repository before attempting diff.
	var checkStdout, checkStderr bytes.Buffer
	checkExit, err := provider.Exec(ctx, sandbox, "git rev-parse --is-inside-work-tree", &checkStdout, &checkStderr)
	if err != nil {
		return "", fmt.Errorf("check git repo: %w", err)
	}
	if checkExit != 0 {
		// Not a git repository — no diff to collect.
		return "", nil
	}

	var stdout, stderr bytes.Buffer
	exitCode, err := provider.Exec(ctx, sandbox, "git diff", &stdout, &stderr)
	if err != nil {
		return "", fmt.Errorf("exec git diff: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("git diff exited with code %d: %s", exitCode, stderr.String())
	}
	return stdout.String(), nil
}
