package slackbot

import (
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type SlackContextReferenceKind string

const (
	SlackContextRepository  SlackContextReferenceKind = "repository"
	SlackContextPullRequest SlackContextReferenceKind = "pull_request"
	SlackContextIssue       SlackContextReferenceKind = "issue"
	SlackContextSentry      SlackContextReferenceKind = "sentry"
	SlackContextPreview     SlackContextReferenceKind = "preview"
	SlackContextBranch      SlackContextReferenceKind = "branch"
	SlackContextFilePath    SlackContextReferenceKind = "file_path"
	SlackContextURL         SlackContextReferenceKind = "url"
	SlackContextSession     SlackContextReferenceKind = "session"
)

type SlackContextReference struct {
	Kind       SlackContextReferenceKind `json:"kind"`
	Value      string                    `json:"value"`
	Source     string                    `json:"source"`
	ResolvedID *uuid.UUID                `json:"resolved_id,omitempty"`
	Metadata   map[string]any            `json:"metadata,omitempty"`
}

type SlackContextOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type SlackRepositoryResolutionSource string

const (
	SlackRepositoryResolutionSourceExplicitReference SlackRepositoryResolutionSource = "slack_explicit_reference"
	SlackRepositoryResolutionSourceChannelDefault    SlackRepositoryResolutionSource = "slack_channel_default"
	SlackRepositoryResolutionSourceInstallDefault    SlackRepositoryResolutionSource = "slack_install_default"
	SlackRepositoryResolutionSourceOrgDefault        SlackRepositoryResolutionSource = "org_default"
	SlackRepositoryResolutionSourceSingleRepo        SlackRepositoryResolutionSource = "single_repo_fallback"
	SlackRepositoryResolutionSourceMissing           SlackRepositoryResolutionSource = "missing"
)

type SlackRepositoryDefault struct {
	RepositoryID   uuid.UUID                       `json:"repository_id"`
	RepositoryName string                          `json:"repository_name,omitempty"`
	Branch         string                          `json:"branch,omitempty"`
	Source         SlackRepositoryResolutionSource `json:"source"`
}

type SlackContextResolveInput struct {
	Settings              models.EffectiveSlackChannelSettings
	Text                  string
	References            []SlackContextReference
	RepositoryDefaults    []SlackRepositoryDefault
	TriggeringSlackUserID string
}

type SlackContextResolveResult struct {
	References                 []SlackContextReference
	RepositoryID               *uuid.UUID
	RepositoryResolutionSource SlackRepositoryResolutionSource
	Branch                     string
	PullRequestID              *uuid.UUID
	PreviewID                  *uuid.UUID
	RoutingMode                SlackRoutingMode
	ContextSummary             SlackSessionContextSummary
	Missing                    []MissingSlackContext
}

func ResolveSlackContext(input SlackContextResolveInput) SlackContextResolveResult {
	result := SlackContextResolveResult{
		References:                 append([]SlackContextReference(nil), input.References...),
		RepositoryID:               input.Settings.DefaultRepositoryID,
		RepositoryResolutionSource: SlackRepositoryResolutionSourceMissing,
		RoutingMode:                SlackRoutingMode(input.Settings.RoutingMode),
	}
	if result.RepositoryID != nil {
		result.RepositoryResolutionSource = SlackRepositoryResolutionSourceInstallDefault
	}
	if result.RoutingMode == "" {
		result.RoutingMode = SlackRoutingModeAuto
	}
	if input.Settings.DefaultBranch != nil {
		result.Branch = strings.TrimSpace(*input.Settings.DefaultBranch)
	}
	if override := RoutingOverrideFromText(input.Text); override != "" {
		result.RoutingMode = override
	}
	hasExplicitRepositoryReference := false
	hasResolvedExplicitRepositoryReference := false
	for _, ref := range input.References {
		switch ref.Kind {
		case SlackContextBranch:
			if result.Branch == "" {
				result.Branch = strings.TrimSpace(ref.Value)
			}
		case SlackContextRepository:
			hasExplicitRepositoryReference = true
			if result.ContextSummary.RepositoryName == "" {
				result.ContextSummary.RepositoryName = strings.TrimSpace(ref.Value)
			}
			if ref.ResolvedID != nil {
				hasResolvedExplicitRepositoryReference = true
				result.RepositoryID = ref.ResolvedID
				result.RepositoryResolutionSource = SlackRepositoryResolutionSourceExplicitReference
			}
		case SlackContextPullRequest:
			if result.ContextSummary.PullRequestURL == "" {
				result.ContextSummary.PullRequestURL = strings.TrimSpace(ref.Value)
			}
			if ref.ResolvedID != nil {
				result.PullRequestID = ref.ResolvedID
			}
		case SlackContextPreview:
			if result.ContextSummary.PreviewURL == "" {
				result.ContextSummary.PreviewURL = strings.TrimSpace(ref.Value)
			}
			if ref.ResolvedID != nil {
				result.PreviewID = ref.ResolvedID
			}
		}
	}
	missingRepositoryReason := "Choose a repository before starting durable work from Slack."
	if hasExplicitRepositoryReference && !hasResolvedExplicitRepositoryReference {
		result.RepositoryID = nil
		result.RepositoryResolutionSource = SlackRepositoryResolutionSourceMissing
		result.Branch = ""
		missingRepositoryReason = "Choose a connected repository before starting durable work from Slack."
	}
	if result.RepositoryID == nil {
		for _, candidate := range input.RepositoryDefaults {
			if hasExplicitRepositoryReference && !hasResolvedExplicitRepositoryReference {
				break
			}
			if candidate.RepositoryID == uuid.Nil {
				continue
			}
			repoID := candidate.RepositoryID
			result.RepositoryID = &repoID
			result.RepositoryResolutionSource = candidate.Source
			if result.RepositoryResolutionSource == "" {
				result.RepositoryResolutionSource = SlackRepositoryResolutionSourceMissing
			}
			if result.Branch == "" {
				result.Branch = strings.TrimSpace(candidate.Branch)
			}
			if result.ContextSummary.RepositoryName == "" {
				result.ContextSummary.RepositoryName = strings.TrimSpace(candidate.RepositoryName)
			}
			break
		}
	}
	if result.ContextSummary.Branch == "" {
		result.ContextSummary.Branch = result.Branch
	}
	if result.RepositoryID == nil && result.RoutingMode != SlackRoutingModeAnswerOnly {
		result.Missing = append(result.Missing, MissingSlackContext{
			Kind:   "repository",
			Reason: missingRepositoryReason,
		})
	}
	text := normalizeSlackCommandText(input.Text)
	if asksForPreview(text) && !hasAnyReference(input.References, SlackContextPreview, SlackContextPullRequest, SlackContextBranch, SlackContextSession, SlackContextRepository) {
		result.Missing = append(result.Missing, MissingSlackContext{
			Kind:   "preview_target",
			Reason: "Choose a branch, PR, session, or repository before creating a preview.",
		})
	}
	if asksToFixPR(text) && !hasAnyReference(input.References, SlackContextPullRequest) {
		result.Missing = append(result.Missing, MissingSlackContext{
			Kind:   "pull_request",
			Reason: "Choose the pull request to repair.",
		})
	}
	result.ContextSummary.Missing = result.Missing
	return result
}

func RoutingOverrideFromText(text string) SlackRoutingMode {
	cleaned := normalizeSlackCommandText(text)
	if strings.HasPrefix(cleaned, "ask ") || cleaned == "ask" {
		return SlackRoutingModeAnswerOnly
	}
	if strings.HasPrefix(cleaned, "start ") || cleaned == "start" {
		return SlackRoutingModeStartWork
	}
	return ""
}

func normalizeSlackCommandText(text string) string {
	cleaned := strings.TrimSpace(strings.ToLower(text))
	for strings.HasPrefix(cleaned, "<@") {
		end := strings.Index(cleaned, ">")
		if end < 0 {
			break
		}
		cleaned = strings.TrimSpace(cleaned[end+1:])
	}
	return strings.Join(strings.Fields(cleaned), " ")
}

func asksForPreview(text string) bool {
	return strings.Contains(text, "preview")
}

func asksToFixPR(text string) bool {
	return strings.Contains(text, "fix this pr") ||
		strings.Contains(text, "fix the pr") ||
		strings.Contains(text, "repair this pr") ||
		strings.Contains(text, "repair the pr")
}

func hasAnyReference(refs []SlackContextReference, kinds ...SlackContextReferenceKind) bool {
	wanted := make(map[SlackContextReferenceKind]bool, len(kinds))
	for _, kind := range kinds {
		wanted[kind] = true
	}
	for _, ref := range refs {
		if wanted[ref.Kind] {
			return true
		}
	}
	return false
}
