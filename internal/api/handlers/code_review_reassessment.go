package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type codeReviewReassessmentWebhook struct {
	Action     string `json:"action"`
	Number     int    `json:"number"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA  string `json:"sha"`
			Ref  string `json:"ref"`
			Repo struct {
				Fork bool `json:"fork"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			SHA string `json:"sha"`
		} `json:"base"`
	} `json:"pull_request"`
}

func (h *WebhookHandler) reassessCodeReviewsForGitHubEvent(ctx context.Context, owner db.GitHubRepoOwner, eventType string, body []byte, _ string) error {
	if h.codeReviews == nil || h.pullRequests == nil || owner.OrgID == uuid.Nil || owner.RepositoryID == uuid.Nil {
		return nil
	}
	var event codeReviewReassessmentWebhook
	if err := json.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("decode code review reassessment event: %w", err)
	}
	if !codeReviewEventChangesAssessment(eventType, event) || event.Number <= 0 {
		return nil
	}

	pr, err := h.pullRequests.GetByOrgRepoAndNumber(ctx, owner.OrgID, event.Repository.FullName, event.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load pull request for code review reassessment: %w", err)
	}
	if err := h.reassessCodeReviewTarget(ctx, owner, eventType, event, pr); err != nil {
		return err
	}
	return nil
}

func (h *WebhookHandler) reassessCodeReviewTarget(ctx context.Context, owner db.GitHubRepoOwner, eventType string, event codeReviewReassessmentWebhook, pr models.PullRequest) error {
	if strings.TrimSpace(event.PullRequest.Head.SHA) != "" {
		snapshot := db.PullRequestGitHubSnapshot{
			GitHubPRURL: event.PullRequest.HTMLURL,
			Title:       event.PullRequest.Title,
			Body:        nilIfEmpty(event.PullRequest.Body),
			HeadSHA:     nilIfEmpty(event.PullRequest.Head.SHA),
			HeadRef:     nilIfEmpty(event.PullRequest.Head.Ref),
			BaseSHA:     nilIfEmpty(event.PullRequest.Base.SHA),
		}
		if err := h.pullRequests.UpdateGitHubSnapshot(ctx, owner.OrgID, pr.ID, snapshot); err != nil {
			return fmt.Errorf("refresh pull request mirror for code review reassessment: %w", err)
		}
		pr.GitHubPRURL = snapshot.GitHubPRURL
		pr.Title = snapshot.Title
		pr.Body = snapshot.Body
		pr.HeadSHA = snapshot.HeadSHA
		pr.HeadRef = snapshot.HeadRef
		pr.BaseSHA = snapshot.BaseSHA
	}
	changeKey, err := codeReviewMaterialChangeKey(pr)
	if err != nil {
		return fmt.Errorf("build code review material change key: %w", err)
	}
	_, err = h.codeReviews.QueueReviewChanged(ctx, codereviewsvc.ReviewChangedInput{
		OrgID:             owner.OrgID,
		RepositoryID:      owner.RepositoryID,
		PullRequestID:     pr.ID,
		GitHubRepo:        pr.GitHubRepo,
		GitHubPRNumber:    pr.GitHubPRNumber,
		GitHubPRURL:       pr.GitHubPRURL,
		PullRequestTitle:  pr.Title,
		PullRequestAuthor: event.PullRequest.User.Login,
		BaseSHA:           codeReviewStringValue(pr.BaseSHA),
		HeadSHA:           codeReviewStringValue(pr.HeadSHA),
		FromFork:          event.PullRequest.Head.Repo.Fork,
		ChangeKey:         changeKey,
		ChangeReason:      eventType + "." + event.Action,
	})
	if err != nil {
		return fmt.Errorf("queue code review reassessment: %w", err)
	}
	return nil
}

type codeReviewMaterialAssessmentState struct {
	HeadSHA string `json:"head_sha"`
}

func codeReviewMaterialChangeKey(pr models.PullRequest) (string, error) {
	raw, err := json.Marshal(codeReviewMaterialAssessmentState{HeadSHA: codeReviewStringValue(pr.HeadSHA)})
	if err != nil {
		return "", fmt.Errorf("marshal material assessment state: %w", err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("material:%x", sum[:]), nil
}

func codeReviewStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func codeReviewEventChangesAssessment(eventType string, event codeReviewReassessmentWebhook) bool {
	return eventType == "pull_request" && event.Action == "synchronize"
}
