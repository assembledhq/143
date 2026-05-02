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
	if prompt.Continuation {
		// Subsequent turn: resume the latest session with --continue.
		// The prompt is a positional argument to --print.
		msg := shellEscapeDouble(prompt.UserMessage)
		cmd = fmt.Sprintf(
			"claude --print --output-format stream-json --verbose%s --continue \"%s\"",
			effortArg,
			msg,
		)
	} else {
		// First turn: write prompt file under $HOME (not WorkDir so it
		// doesn't pollute the cloned repo's git status) and pipe it into
		// claude via stdin.
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

// buildSystemPrompt constructs the system prompt with instructions and context.
func buildSystemPrompt(input *agent.AgentInput) string {
	var b strings.Builder

	// Manual sessions skip the bug-fixing template — the user's raw message
	// is the entire prompt. Only inject repo conventions and integration
	// skills so the agent knows what tools and patterns are available.
	isManual := input.Manual || (input.Issue != nil && input.Issue.Source == models.IssueSourceManual)
	if !isManual {
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
		if input.RevisionContext.RepairContext != nil {
			b.WriteString("### Repair Context\n\n")
			if input.RevisionContext.RepairAction != "" {
				b.WriteString("Repair action: `")
				b.WriteString(string(input.RevisionContext.RepairAction))
				b.WriteString("`\n\n")
			}
			b.WriteString(fmt.Sprintf("PR #%d in `%s`.\n\n", input.RevisionContext.RepairContext.PullRequestNumber, input.RevisionContext.RepairContext.Repository))
			b.WriteString(fmt.Sprintf("- head SHA: `%s`\n", input.RevisionContext.RepairContext.HeadSHA))
			b.WriteString(fmt.Sprintf("- base SHA: `%s`\n", input.RevisionContext.RepairContext.BaseSHA))
			b.WriteString(fmt.Sprintf("- merge state: `%s`\n", input.RevisionContext.RepairContext.MergeState))
			if len(input.RevisionContext.RepairContext.FailingChecks) > 0 {
				b.WriteString("\nFailed checks:\n")
				for _, check := range input.RevisionContext.RepairContext.FailingChecks {
					b.WriteString(fmt.Sprintf("- `%s` (%s)", check.Name, check.Category))
					if check.Summary != "" {
						b.WriteString(": ")
						b.WriteString(check.Summary)
					}
					b.WriteString("\n")
					for _, annotation := range check.Annotations {
						b.WriteString("  - annotation: ")
						b.WriteString(annotation)
						b.WriteString("\n")
					}
					if check.LogExcerpt != "" {
						b.WriteString("  - log excerpt: ")
						b.WriteString(check.LogExcerpt)
						b.WriteString("\n")
					}
					if check.DetailsURL != "" {
						b.WriteString("  - details: ")
						b.WriteString(check.DetailsURL)
						b.WriteString("\n")
					}
				}
				b.WriteString("\n")
			}
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

	if len(input.LinkedIssues) > 0 {
		entries := make([]prompts.LinkedIssueContextEntry, 0, len(input.LinkedIssues))
		for _, linked := range input.LinkedIssues {
			entry := prompts.LinkedIssueContextEntry{
				Role:       string(linked.Role),
				Source:     string(linked.Source),
				Title:      linked.Title,
				ExternalID: linked.ExternalID,
				StateName:  linked.StateName,
				StateType:  linked.StateType,
				Priority:   linked.Priority,
				Assignee:   linked.AssigneeName,
				TeamKey:    linked.TeamKey,
				TeamName:   linked.TeamName,
				URL:        linked.URL,
			}
			if linked.Role == models.SessionIssueLinkRolePrimary {
				entry.Description = linked.Description
			}
			for _, attachment := range linked.Attachments {
				entry.Attachments = append(entry.Attachments, prompts.LinkedIssueAttachment{
					Title:  attachment.Title,
					URL:    attachment.URL,
					Source: attachment.Source,
				})
			}
			for _, comment := range linked.Comments {
				entry.Comments = append(entry.Comments, prompts.LinkedIssueComment{
					Author: comment.Author,
					Body:   comment.Body,
				})
			}
			entries = append(entries, entry)
		}
		b.WriteString("## Linked Issues Context\n\n")
		b.WriteString(prompts.LinkedIssuesContext(prompts.LinkedIssueContextData{LinkedIssues: entries}))
		b.WriteString("\n\n")
	}

	return b.String()
}

// buildUserPrompt constructs the user prompt with issue-specific details.
func buildUserPrompt(input *agent.AgentInput) string {
	// Manual sessions: pass through the user's raw message without any wrapping.
	isManual := input.Manual || (input.Issue != nil && input.Issue.Source == models.IssueSourceManual)
	if isManual {
		base := input.UserMessage
		if strings.TrimSpace(base) == "" && input.Issue != nil {
			base = input.Issue.Title
			if input.Issue.Description != nil {
				base = *input.Issue.Description
			}
		}
		base = EnsureSlashCommandsInPrompt(base, input.Commands)
		if len(input.References) == 0 {
			return base
		}
		return buildManualPromptWithReferences(base, input.References)
	}

	if input.Issue == nil {
		return "No issue context was provided. Use the session title and any repository context to complete the task."
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

func buildManualPromptWithReferences(message string, references []models.SessionInputReference) string {
	var b strings.Builder
	b.WriteString(message)
	b.WriteString("\n\n## Referenced context\n")
	for _, reference := range references {
		b.WriteString("- ")
		if reference.Token != "" {
			b.WriteString(reference.Token)
		} else {
			b.WriteString(reference.Display)
		}
		if reference.Path != "" && reference.Path != reference.Display {
			b.WriteString(" (")
			b.WriteString(reference.Path)
			b.WriteString(")")
		}
		if reference.ID != "" {
			b.WriteString(" [")
			b.WriteString(reference.ID)
			b.WriteString("]")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// extractFileHints parses the issue's raw data for file paths from
// Sentry stack trace frames.
func extractFileHints(input *agent.AgentInput) []string {
	if input == nil || input.Issue == nil {
		return nil
	}
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
