package preview

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// =============================================================================
// GitHub client interface
// =============================================================================

// GitHubClient abstracts the GitHub API methods needed for PR preview integration.
// Implementations wrap the go-github library or a test double.
type GitHubClient interface {
	CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) (int64, error)
	UpdateIssueComment(ctx context.Context, owner, repo string, commentID int64, body string) error
	CreateDeployment(ctx context.Context, owner, repo, ref, env string) (int64, error)
	UpdateDeploymentStatus(ctx context.Context, owner, repo string, deploymentID int64, state, targetURL string) error
	CreateCommitStatus(ctx context.Context, owner, repo, sha, state, context_, targetURL, description string) error
}

// =============================================================================
// Constants
// =============================================================================

const (
	// commitStatusContext is the GitHub commit status context name.
	commitStatusContext = "preview/143"

	// defaultAppBaseURL is the fallback base URL for the 143 web app.
	defaultAppBaseURL = "https://app.143.dev"

	// commentHeader is the consistent header so we can identify our comments.
	commentHeader = "<!-- 143-preview-comment -->"
)

// GitHub commit status states.
const (
	commitStatusPending  = "pending"
	commitStatusSuccess  = "success"
	commitStatusInactive = "failure"
)

// GitHub deployment states.
const (
	deploymentStatePending  = "pending"
	deploymentStateSuccess  = "success"
	deploymentStateInactive = "inactive"
)

// =============================================================================
// PRPreviewIntegration
// =============================================================================

// PRPreviewIntegration manages the GitHub PR comment lifecycle and deployment
// status for preview sessions. It maintains a single updating comment per PR
// and creates GitHub deployments with environment URLs.
type PRPreviewIntegration struct {
	store      *db.PreviewStore
	gh         GitHubClient
	logger     zerolog.Logger
	appBaseURL string
}

// NewPRPreviewIntegration creates a new PRPreviewIntegration. If appBaseURL is
// empty, it falls back to defaultAppBaseURL.
func NewPRPreviewIntegration(store *db.PreviewStore, gh GitHubClient, logger zerolog.Logger, appBaseURL string) *PRPreviewIntegration {
	if appBaseURL == "" {
		appBaseURL = defaultAppBaseURL
	}
	return &PRPreviewIntegration{
		store:      store,
		gh:         gh,
		logger:     logger,
		appBaseURL: appBaseURL,
	}
}

// =============================================================================
// OnPROpened
// =============================================================================

// OnPROpened is called when a PR is opened for a repo with preview configured.
// It creates the initial PR comment with a "Launch Preview" button and
// initializes the PR preview state record.
func (p *PRPreviewIntegration) OnPROpened(
	ctx context.Context,
	orgID, repoID uuid.UUID,
	prNumber int,
	owner, repo string,
	sessionID uuid.UUID,
) error {
	sessionURL := p.buildSessionURL(sessionID)
	body := p.buildNeverStartedComment(sessionURL)

	commentID, err := p.gh.CreateIssueComment(ctx, owner, repo, prNumber, body)
	if err != nil {
		return fmt.Errorf("create PR comment: %w", err)
	}

	state := &models.PRPreviewState{
		OrgID:           orgID,
		RepoID:          repoID,
		PRNumber:        prNumber,
		GitHubCommentID: &commentID,
		Status:          models.PRPreviewStatusNeverStarted,
	}

	if err := p.store.UpsertPRPreviewState(ctx, state); err != nil {
		return fmt.Errorf("upsert PR preview state: %w", err)
	}

	p.logger.Info().
		Str("owner", owner).
		Str("repo", repo).
		Int("pr_number", prNumber).
		Int64("comment_id", commentID).
		Msg("posted initial preview comment on PR")

	return nil
}

// =============================================================================
// OnPreviewStarted
// =============================================================================

// OnPreviewStarted is called when a preview instance begins starting for a PR.
// It updates the PR comment to show "starting" state, sets the commit status
// to pending, and creates a GitHub deployment.
func (p *PRPreviewIntegration) OnPreviewStarted(
	ctx context.Context,
	prState *models.PRPreviewState,
	previewInstance *models.PreviewInstance,
	owner, repo string,
	sessionURL string,
) error {
	// Update the PR preview state.
	prState.LastPreviewInstanceID = &previewInstance.ID
	prState.Status = models.PRPreviewStatusRunning
	if err := p.store.UpsertPRPreviewState(ctx, prState); err != nil {
		return fmt.Errorf("upsert PR preview state: %w", err)
	}

	// Update the PR comment.
	body := p.buildRunningComment(sessionURL, nil, time.Now())
	if err := p.updateComment(ctx, owner, repo, prState, body); err != nil {
		p.logger.Warn().Err(err).Msg("failed to update PR comment for preview started")
	}

	// Set commit status to pending.
	if previewInstance.BaseCommitSHA != "" {
		if err := p.gh.CreateCommitStatus(
			ctx, owner, repo, previewInstance.BaseCommitSHA,
			commitStatusPending, commitStatusContext,
			sessionURL, "Preview is starting...",
		); err != nil {
			p.logger.Warn().Err(err).Msg("failed to create pending commit status")
		}
	}

	// Create a deployment.
	deploymentID, err := p.gh.CreateDeployment(ctx, owner, repo, previewInstance.BaseCommitSHA, "preview")
	if err != nil {
		p.logger.Warn().Err(err).Msg("failed to create GitHub deployment")
	} else {
		if err := p.gh.UpdateDeploymentStatus(ctx, owner, repo, deploymentID, deploymentStatePending, sessionURL); err != nil {
			p.logger.Warn().Err(err).Msg("failed to set deployment status to pending")
		}
	}

	return nil
}

// =============================================================================
// OnPreviewReady
// =============================================================================

// OnPreviewReady is called when a preview instance is fully ready and serving
// traffic. It updates the PR comment with a live link and optional screenshot,
// sets the commit status to success, and updates the deployment.
func (p *PRPreviewIntegration) OnPreviewReady(
	ctx context.Context,
	prState *models.PRPreviewState,
	previewInstance *models.PreviewInstance,
	owner, repo string,
	sessionURL string,
	screenshotURL string,
) error {
	// Update state with screenshot if provided.
	if screenshotURL != "" {
		prState.LastScreenshotBlobPath = screenshotURL
	}
	prState.Status = models.PRPreviewStatusRunning
	if err := p.store.UpsertPRPreviewState(ctx, prState); err != nil {
		return fmt.Errorf("upsert PR preview state: %w", err)
	}

	// Update the PR comment.
	var screenshotPtr *string
	if screenshotURL != "" {
		screenshotPtr = &screenshotURL
	}
	body := p.buildRunningComment(sessionURL, screenshotPtr, time.Now())
	if err := p.updateComment(ctx, owner, repo, prState, body); err != nil {
		p.logger.Warn().Err(err).Msg("failed to update PR comment for preview ready")
	}

	// Set commit status to success.
	if previewInstance.BaseCommitSHA != "" {
		if err := p.gh.CreateCommitStatus(
			ctx, owner, repo, previewInstance.BaseCommitSHA,
			commitStatusSuccess, commitStatusContext,
			sessionURL, "Preview is live",
		); err != nil {
			p.logger.Warn().Err(err).Msg("failed to create success commit status")
		}
	}

	// Update deployment to success.
	if previewInstance.BaseCommitSHA != "" {
		deploymentID, err := p.gh.CreateDeployment(ctx, owner, repo, previewInstance.BaseCommitSHA, "preview")
		if err != nil {
			p.logger.Warn().Err(err).Msg("failed to get/create deployment for ready state")
		} else {
			if err := p.gh.UpdateDeploymentStatus(ctx, owner, repo, deploymentID, deploymentStateSuccess, sessionURL); err != nil {
				p.logger.Warn().Err(err).Msg("failed to set deployment status to success")
			}
		}
	}

	return nil
}

// =============================================================================
// OnPreviewStopped
// =============================================================================

// OnPreviewStopped is called when a preview instance stops (idle timeout,
// manual stop, or expiration). It updates the PR comment to show "Re-launch"
// and sets the commit status to inactive.
func (p *PRPreviewIntegration) OnPreviewStopped(
	ctx context.Context,
	prState *models.PRPreviewState,
	previewInstance *models.PreviewInstance,
	owner, repo string,
	sessionURL string,
) error {
	prState.Status = models.PRPreviewStatusStopped
	if err := p.store.UpsertPRPreviewState(ctx, prState); err != nil {
		return fmt.Errorf("upsert PR preview state: %w", err)
	}

	// Build the stopped comment.
	var stoppedAt time.Time
	if previewInstance.StoppedAt != nil {
		stoppedAt = *previewInstance.StoppedAt
	} else {
		stoppedAt = time.Now()
	}
	var screenshotPtr *string
	if prState.LastScreenshotBlobPath != "" {
		screenshotPtr = &prState.LastScreenshotBlobPath
	}
	body := p.buildStoppedComment(sessionURL, stoppedAt, screenshotPtr)
	if err := p.updateComment(ctx, owner, repo, prState, body); err != nil {
		p.logger.Warn().Err(err).Msg("failed to update PR comment for preview stopped")
	}

	// Set commit status to inactive.
	if previewInstance.BaseCommitSHA != "" {
		if err := p.gh.CreateCommitStatus(
			ctx, owner, repo, previewInstance.BaseCommitSHA,
			commitStatusInactive, commitStatusContext,
			sessionURL, "Preview stopped",
		); err != nil {
			p.logger.Warn().Err(err).Msg("failed to create inactive commit status")
		}
	}

	// Mark deployment inactive.
	if previewInstance.BaseCommitSHA != "" {
		deploymentID, err := p.gh.CreateDeployment(ctx, owner, repo, previewInstance.BaseCommitSHA, "preview")
		if err != nil {
			p.logger.Warn().Err(err).Msg("failed to get/create deployment for stopped state")
		} else {
			if err := p.gh.UpdateDeploymentStatus(ctx, owner, repo, deploymentID, deploymentStateInactive, sessionURL); err != nil {
				p.logger.Warn().Err(err).Msg("failed to set deployment status to inactive")
			}
		}
	}

	return nil
}

// =============================================================================
// OnPRClosed
// =============================================================================

// OnPRClosed is called when a PR is closed or merged. It updates the PR
// preview state to the appropriate terminal status and updates the comment
// with final information.
func (p *PRPreviewIntegration) OnPRClosed(
	ctx context.Context,
	orgID, repoID uuid.UUID,
	prNumber int,
	owner, repo string,
	merged bool,
) error {
	prState, err := p.store.GetPRPreviewState(ctx, orgID, repoID, prNumber)
	if err != nil {
		return fmt.Errorf("get PR preview state: %w", err)
	}

	if merged {
		prState.Status = models.PRPreviewStatusMerged
	} else {
		prState.Status = models.PRPreviewStatusClosed
	}

	if err := p.store.UpsertPRPreviewState(ctx, prState); err != nil {
		return fmt.Errorf("upsert PR preview state: %w", err)
	}

	var screenshotPtr *string
	if prState.LastScreenshotBlobPath != "" {
		screenshotPtr = &prState.LastScreenshotBlobPath
	}
	body := p.buildClosedComment(merged, screenshotPtr)
	if err := p.updateComment(ctx, owner, repo, prState, body); err != nil {
		p.logger.Warn().Err(err).Msg("failed to update PR comment for PR closed")
	}

	p.logger.Info().
		Str("owner", owner).
		Str("repo", repo).
		Int("pr_number", prNumber).
		Bool("merged", merged).
		Msg("updated preview comment for PR close")

	return nil
}

// =============================================================================
// OnAgentChanges
// =============================================================================

// OnAgentChanges is called when the agent makes file changes while a preview
// is running. It updates the PR comment to reflect that the preview has been
// updated with the agent's changes.
func (p *PRPreviewIntegration) OnAgentChanges(
	ctx context.Context,
	prState *models.PRPreviewState,
	owner, repo string,
	sessionURL string,
	fileCount int,
	screenshotURL string,
) error {
	if screenshotURL != "" {
		prState.LastScreenshotBlobPath = screenshotURL
	}
	if err := p.store.UpsertPRPreviewState(ctx, prState); err != nil {
		return fmt.Errorf("upsert PR preview state: %w", err)
	}

	var screenshotPtr *string
	if screenshotURL != "" {
		screenshotPtr = &screenshotURL
	}
	body := p.buildAgentChangesComment(sessionURL, fileCount, screenshotPtr, time.Now())
	if err := p.updateComment(ctx, owner, repo, prState, body); err != nil {
		p.logger.Warn().Err(err).Msg("failed to update PR comment for agent changes")
	}

	return nil
}

// =============================================================================
// Comment body builders
// =============================================================================

func (p *PRPreviewIntegration) buildNeverStartedComment(sessionURL string) string {
	var b strings.Builder
	b.WriteString(commentHeader)
	b.WriteString("\n")
	b.WriteString("### \U0001F50D Preview available for this PR\n\n")
	b.WriteString(fmt.Sprintf("**[Launch Preview](%s)** \u2014 starts a live preview of this change\n\n", sanitizeMarkdownURL(sessionURL)))
	b.WriteString("---\n")
	b.WriteString("*Powered by [143](https://143.dev)*\n")
	return b.String()
}

func (p *PRPreviewIntegration) buildRunningComment(sessionURL string, screenshotURL *string, startedAt time.Time) string {
	var b strings.Builder
	b.WriteString(commentHeader)
	b.WriteString("\n")
	b.WriteString("### \u2705 Preview is live\n\n")
	b.WriteString(fmt.Sprintf("**[\U0001F517 Open Preview](%s)**\n\n", sanitizeMarkdownURL(sessionURL)))

	if screenshotURL != nil {
		b.WriteString(fmt.Sprintf("![Preview screenshot](%s)\n\n", sanitizeMarkdownURL(*screenshotURL)))
	}

	b.WriteString(fmt.Sprintf("Started: %s\n\n", formatTimestamp(startedAt)))
	b.WriteString("---\n")
	b.WriteString("*Powered by [143](https://143.dev)*\n")
	return b.String()
}

func (p *PRPreviewIntegration) buildStoppedComment(sessionURL string, stoppedAt time.Time, screenshotURL *string) string {
	var b strings.Builder
	b.WriteString(commentHeader)
	b.WriteString("\n")
	b.WriteString("### \U0001F50D Preview available for this PR\n\n")
	b.WriteString(fmt.Sprintf("**[Re-launch Preview](%s)** \u2014 starts a live preview of this change\n\n", sanitizeMarkdownURL(sessionURL)))
	b.WriteString(fmt.Sprintf("Last preview: %s (stopped \u2014 idle timeout)\n\n", formatRelativeTime(stoppedAt)))

	if screenshotURL != nil {
		b.WriteString(fmt.Sprintf("![Last preview screenshot](%s)\n\n", sanitizeMarkdownURL(*screenshotURL)))
	}

	b.WriteString("---\n")
	b.WriteString("*Powered by [143](https://143.dev)*\n")
	return b.String()
}

func (p *PRPreviewIntegration) buildClosedComment(merged bool, screenshotURL *string) string {
	var b strings.Builder
	b.WriteString(commentHeader)
	b.WriteString("\n")

	if merged {
		b.WriteString("### \U0001F389 PR merged\n\n")
	} else {
		b.WriteString("### \U0001F6AB PR closed\n\n")
	}

	b.WriteString("Preview session has ended.\n\n")

	if screenshotURL != nil {
		b.WriteString(fmt.Sprintf("**Final screenshot:**\n\n![Final preview screenshot](%s)\n\n", sanitizeMarkdownURL(*screenshotURL)))
	}

	b.WriteString("---\n")
	b.WriteString("*Powered by [143](https://143.dev)*\n")
	return b.String()
}

func (p *PRPreviewIntegration) buildAgentChangesComment(sessionURL string, fileCount int, screenshotURL *string, updatedAt time.Time) string {
	var b strings.Builder
	b.WriteString(commentHeader)
	b.WriteString("\n")
	b.WriteString("### \u2705 Preview is live\n\n")
	b.WriteString(fmt.Sprintf("**[\U0001F517 Open Preview](%s)**\n\n", sanitizeMarkdownURL(sessionURL)))

	filesWord := "files"
	if fileCount == 1 {
		filesWord = "file"
	}
	b.WriteString(fmt.Sprintf("\U0001F916 Agent updated preview \u2014 %d %s changed (%s)\n\n", fileCount, filesWord, formatTimestamp(updatedAt)))

	if screenshotURL != nil {
		b.WriteString(fmt.Sprintf("![Preview screenshot](%s)\n\n", sanitizeMarkdownURL(*screenshotURL)))
	}

	b.WriteString("---\n")
	b.WriteString("*Powered by [143](https://143.dev)*\n")
	return b.String()
}

// =============================================================================
// Helpers
// =============================================================================

// updateComment updates an existing PR comment, or logs a warning if the
// comment ID is not yet set.
func (p *PRPreviewIntegration) updateComment(ctx context.Context, owner, repo string, prState *models.PRPreviewState, body string) error {
	if prState.GitHubCommentID == nil {
		// No comment exists yet; create one.
		commentID, err := p.gh.CreateIssueComment(ctx, owner, repo, prState.PRNumber, body)
		if err != nil {
			return fmt.Errorf("create PR comment: %w", err)
		}
		prState.GitHubCommentID = &commentID
		if err := p.store.UpsertPRPreviewState(ctx, prState); err != nil {
			p.logger.Warn().Err(err).Msg("failed to persist comment ID after creation")
		}
		return nil
	}

	return p.gh.UpdateIssueComment(ctx, owner, repo, *prState.GitHubCommentID, body)
}

// buildSessionURL constructs the app URL for a preview session.
func (p *PRPreviewIntegration) buildSessionURL(sessionID uuid.UUID) string {
	return fmt.Sprintf("%s/sessions/%s?preview=1", p.appBaseURL, sessionID)
}

// sanitizeMarkdownURL escapes characters in a URL that could break Markdown
// link/image syntax (e.g., unbalanced parentheses).
func sanitizeMarkdownURL(rawURL string) string {
	if _, err := url.Parse(rawURL); err != nil {
		return ""
	}
	return strings.ReplaceAll(rawURL, ")", "%29")
}

// formatTimestamp formats a time as a human-readable string.
func formatTimestamp(t time.Time) string {
	return t.UTC().Format("Jan 2, 2006 15:04 UTC")
}

// formatRelativeTime returns a human-readable relative time string.
func formatRelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}
