package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type SlackPreviewControl struct {
	previewHandler       *PreviewHandler
	branchPreviewHandler *BranchPreviewHandler
	pullRequests         *db.PullRequestStore
	repos                *db.RepositoryStore
	frontendURL          string
}

func NewSlackPreviewControl(previewHandler *PreviewHandler, branchPreviewHandler *BranchPreviewHandler, pullRequests *db.PullRequestStore, repos *db.RepositoryStore, frontendURL string) *SlackPreviewControl {
	return &SlackPreviewControl{
		previewHandler:       previewHandler,
		branchPreviewHandler: branchPreviewHandler,
		pullRequests:         pullRequests,
		repos:                repos,
		frontendURL:          strings.TrimRight(strings.TrimSpace(frontendURL), "/"),
	}
}

func slackPreviewSourceID(repositoryID uuid.UUID, branch, commitSHA, configName string) string {
	return fmt.Sprintf("slack:%s:%s:%s:%s", repositoryID, strings.TrimSpace(branch), strings.TrimSpace(commitSHA), strings.TrimSpace(configName))
}

func (c *SlackPreviewControl) CreatePreviewForSlack(ctx context.Context, orgID uuid.UUID, target models.SlackPreviewTarget, actor models.SlackActor) (models.PreviewInstance, error) {
	if c == nil {
		return models.PreviewInstance{}, fmt.Errorf("preview control is not configured")
	}
	if c.previewHandler == nil && c.branchPreviewHandler == nil {
		return models.PreviewInstance{}, fmt.Errorf("preview handler is not configured")
	}
	if actor.UserID == uuid.Nil {
		return models.PreviewInstance{}, fmt.Errorf("slack preview actor user is required")
	}
	if target.Kind == models.SlackPreviewTargetSession {
		if c.previewHandler == nil || target.SessionID == nil {
			return models.PreviewInstance{}, fmt.Errorf("session preview handler is not configured")
		}
		instance, _, startErr := c.previewHandler.startPreviewFromRequest(ctx, orgID, actor.UserID, *target.SessionID, startPreviewRequest{
			ProfileName: target.ConfigName,
		})
		if startErr != nil {
			return models.PreviewInstance{}, fmt.Errorf("%s: %s", startErr.code, startErr.message)
		}
		if instance == nil {
			return models.PreviewInstance{}, fmt.Errorf("preview start returned no instance")
		}
		return *instance, nil
	}
	if c.branchPreviewHandler == nil {
		return models.PreviewInstance{}, fmt.Errorf("branch preview handler is not configured")
	}
	branchTarget, err := c.branchPreviewTarget(ctx, orgID, target)
	if err != nil {
		return models.PreviewInstance{}, err
	}
	instance, err := c.branchPreviewHandler.StartPreviewForSlack(ctx, orgID, actor.UserID, branchTarget)
	if err != nil {
		return models.PreviewInstance{}, err
	}
	if instance == nil {
		return models.PreviewInstance{}, fmt.Errorf("preview start returned no instance")
	}
	return *instance, nil
}

func (c *SlackPreviewControl) branchPreviewTarget(ctx context.Context, orgID uuid.UUID, target models.SlackPreviewTarget) (SlackBranchPreviewTarget, error) {
	switch target.Kind {
	case models.SlackPreviewTargetBranch, models.SlackPreviewTargetCommit, models.SlackPreviewTargetRepository:
		if target.RepositoryID == uuid.Nil {
			return SlackBranchPreviewTarget{}, fmt.Errorf("repository_id is required for Slack %s preview", target.Kind)
		}
		return SlackBranchPreviewTarget{
			RepositoryID:      target.RepositoryID,
			Branch:            target.Branch,
			CommitSHA:         target.CommitSHA,
			PreviewConfigName: target.ConfigName,
			SourceType:        models.PreviewSourceTypeManual,
			SourceID:          slackPreviewSourceID(target.RepositoryID, target.Branch, target.CommitSHA, target.ConfigName),
		}, nil
	case models.SlackPreviewTargetPullRequest:
		if c.pullRequests == nil || c.repos == nil {
			return SlackBranchPreviewTarget{}, fmt.Errorf("pull request preview dependencies are not configured")
		}
		if target.PullRequestID == nil {
			return SlackBranchPreviewTarget{}, fmt.Errorf("pull_request_id is required for Slack PR preview")
		}
		pr, err := c.pullRequests.GetByID(ctx, orgID, *target.PullRequestID)
		if err != nil {
			return SlackBranchPreviewTarget{}, fmt.Errorf("load pull request for Slack preview: %w", err)
		}
		repo, err := c.repos.GetByFullName(ctx, orgID, pr.GitHubRepo)
		if err != nil {
			return SlackBranchPreviewTarget{}, fmt.Errorf("load pull request repository for Slack preview: %w", err)
		}
		branch := target.Branch
		if branch == "" && pr.HeadRef != nil {
			branch = *pr.HeadRef
		}
		sha := target.CommitSHA
		if sha == "" && pr.HeadSHA != nil {
			sha = *pr.HeadSHA
		}
		if strings.TrimSpace(branch) == "" && strings.TrimSpace(sha) == "" {
			return SlackBranchPreviewTarget{}, fmt.Errorf("pull request has no head ref or head sha")
		}
		sourceID := fmt.Sprintf("%s#%d", pr.GitHubRepo, pr.GitHubPRNumber)
		if sha != "" {
			sourceID += "@" + sha
		}
		return SlackBranchPreviewTarget{
			RepositoryID:      repo.ID,
			Branch:            branch,
			CommitSHA:         sha,
			PreviewConfigName: target.ConfigName,
			SourceType:        models.PreviewSourceTypePullRequest,
			SourceID:          sourceID,
			SourceURL:         pr.GitHubPRURL,
		}, nil
	default:
		return SlackBranchPreviewTarget{}, fmt.Errorf("unsupported Slack preview target %q", target.Kind)
	}
}

func (c *SlackPreviewControl) OpenPreviewURL(ctx context.Context, orgID, previewID uuid.UUID, actor models.SlackActor) (string, error) {
	if previewID == uuid.Nil {
		return "", fmt.Errorf("preview id is required")
	}
	return c.previewURL(previewID), nil
}

func (c *SlackPreviewControl) previewURL(previewID uuid.UUID) string {
	path := "/previews/" + previewID.String()
	if c == nil || c.frontendURL == "" {
		return path
	}
	return c.frontendURL + path
}
