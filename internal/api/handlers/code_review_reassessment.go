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
	Review struct {
		Body string `json:"body"`
	} `json:"review"`
	Comment struct {
		Body string `json:"body"`
	} `json:"comment"`
	CheckSuite struct {
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	CheckRun struct {
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
	SHA string `json:"sha"`
}

func (h *WebhookHandler) reassessCodeReviewsForGitHubEvent(ctx context.Context, owner db.GitHubRepoOwner, eventType string, body []byte, deliveryID string) error {
	if h.codeReviews == nil || h.pullRequests == nil || owner.OrgID == uuid.Nil || owner.RepositoryID == uuid.Nil {
		return nil
	}
	var event codeReviewReassessmentWebhook
	if err := json.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("decode code review reassessment event: %w", err)
	}
	if !codeReviewEventChangesAssessment(eventType, event) {
		return nil
	}
	if (eventType == "pull_request_review" && event.Action != "dismissed" && codereviewsvc.IsCodeReviewAuthoredBody(event.Review.Body)) ||
		(eventType == "pull_request_review_comment" && codereviewsvc.IsCodeReviewAuthoredBody(event.Comment.Body)) {
		return nil
	}

	numbers := codeReviewReassessmentPullRequestNumbers(eventType, event)
	if eventType == "status" {
		prs, err := h.pullRequests.ListOpenByOrgRepoAndHeadSHA(ctx, owner.OrgID, event.Repository.FullName, event.SHA)
		if err != nil {
			return fmt.Errorf("list pull requests for code review status reassessment: %w", err)
		}
		for _, pr := range prs {
			if err := h.reassessCodeReviewTarget(ctx, owner, eventType, deliveryID, body, event, pr); err != nil {
				return err
			}
		}
		return nil
	}
	for _, number := range numbers {
		pr, err := h.pullRequests.GetByOrgRepoAndNumber(ctx, owner.OrgID, event.Repository.FullName, number)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("load pull request for code review reassessment: %w", err)
		}
		if err := h.reassessCodeReviewTarget(ctx, owner, eventType, deliveryID, body, event, pr); err != nil {
			return err
		}
	}
	return nil
}

func (h *WebhookHandler) reassessCodeReviewTarget(ctx context.Context, owner db.GitHubRepoOwner, eventType, deliveryID string, body []byte, event codeReviewReassessmentWebhook, pr models.PullRequest) error {
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
	changeKey := strings.TrimSpace(deliveryID)
	if changeKey == "" {
		sum := sha256.Sum256(append([]byte(eventType+"\n"), body...))
		changeKey = fmt.Sprintf("%x", sum[:])
	}
	_, err := h.codeReviews.QueueReviewChanged(ctx, codereviewsvc.ReviewChangedInput{
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
		ChangeKey:         eventType + ":" + changeKey,
		ChangeReason:      eventType + "." + event.Action,
	})
	if err != nil {
		return fmt.Errorf("queue code review reassessment: %w", err)
	}
	return nil
}

func codeReviewStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func codeReviewEventChangesAssessment(eventType string, event codeReviewReassessmentWebhook) bool {
	switch eventType {
	case "pull_request":
		switch event.Action {
		case "synchronize", "edited", "reopened", "ready_for_review", "converted_to_draft":
			return true
		}
	case "pull_request_review":
		return event.Action == "submitted" || event.Action == "edited" || event.Action == "dismissed"
	case "pull_request_review_comment":
		return event.Action == "created" || event.Action == "edited" || event.Action == "deleted"
	case "pull_request_review_thread":
		return event.Action == "resolved" || event.Action == "unresolved"
	case "check_suite", "check_run":
		return event.Action == "completed"
	case "status":
		return strings.TrimSpace(event.SHA) != ""
	}
	return false
}

func codeReviewReassessmentPullRequestNumbers(eventType string, event codeReviewReassessmentWebhook) []int {
	seen := make(map[int]struct{})
	add := func(numbers []int, number int) []int {
		if number <= 0 {
			return numbers
		}
		if _, ok := seen[number]; ok {
			return numbers
		}
		seen[number] = struct{}{}
		return append(numbers, number)
	}
	numbers := make([]int, 0, 2)
	switch eventType {
	case "pull_request":
		numbers = add(numbers, event.Number)
	case "pull_request_review", "pull_request_review_comment", "pull_request_review_thread":
		numbers = add(numbers, event.PullRequest.Number)
	case "check_suite":
		for _, ref := range event.CheckSuite.PullRequests {
			numbers = add(numbers, ref.Number)
		}
	case "check_run":
		for _, ref := range event.CheckRun.PullRequests {
			numbers = add(numbers, ref.Number)
		}
	}
	return numbers
}
