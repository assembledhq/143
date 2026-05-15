// Shared prompt-construction helpers used by every coding-agent adapter
// (claude_code, codex, amp, pi, gemini_cli). The system and user prompts
// are agent-agnostic — only the wire format around them differs per CLI.
package adapters

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/agent"
)

func usesRawTaskPrompt(input *agent.AgentInput) bool {
	return input != nil && (input.PromptStyle == agent.PromptStyleRawTask || input.Manual || (input.Issue != nil && input.Issue.Source == models.IssueSourceManual))
}

// buildSystemPrompt constructs the system prompt with instructions and context.
func buildSystemPrompt(input *agent.AgentInput) string {
	var b strings.Builder

	// Manual sessions skip the bug-fixing template — the user's raw message
	// is the entire prompt. Only inject repo conventions and integration
	// skills so the agent knows what tools and patterns are available.
	if !usesRawTaskPrompt(input) {
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
			if input.RevisionContext.RepairAction == models.PullRequestRepairActionTypeResolveConflicts {
				b.WriteString("\n")
				b.WriteString(agent.ResolveConflictsGuidance(input.RevisionContext.RepairContext.BaseSHA, input.RevisionContext.RepairContext.HeadSHA))
				b.WriteString("\n")
			}
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
	if input.PMContext != nil && !usesRawTaskPrompt(input) {
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
	if usesRawTaskPrompt(input) {
		base := input.UserMessage
		if strings.TrimSpace(base) == "" && input.Issue != nil {
			base = input.Issue.Title
			if input.Issue.Description != nil {
				base = *input.Issue.Description
			}
		}
		base = EnsureSlashCommandsInPrompt(base, input.Commands)
		if len(input.References) > 0 {
			base = buildManualPromptWithReferences(base, input.References)
		}
		return appendAttachmentSection(base, input.Attachments)
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

	return appendAttachmentSection(b.String(), input.Attachments)
}

func appendAttachmentSection(prompt string, attachments []agent.AgentAttachment) string {
	if len(attachments) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\n## Attached files\n")
	for _, attachment := range attachments {
		b.WriteString("- ")
		if attachment.LocalPath != "" {
			b.WriteString("`")
			b.WriteString(attachment.LocalPath)
			b.WriteString("`")
			if attachment.ContentType != "" {
				b.WriteString(" (")
				b.WriteString(attachment.ContentType)
				b.WriteString(")")
			}
		} else {
			b.WriteString("unavailable")
		}
		if attachment.OriginalURL != "" {
			b.WriteString(" from ")
			b.WriteString(attachment.OriginalURL)
		}
		if attachment.Error != "" {
			b.WriteString(" - warning: ")
			b.WriteString(attachment.Error)
		}
		b.WriteString("\n")
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
