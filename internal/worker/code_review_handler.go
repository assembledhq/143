package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type runCodeReviewPayload struct {
	OrgID         uuid.UUID `json:"org_id"`
	SessionID     uuid.UUID `json:"session_id"`
	MetadataID    uuid.UUID `json:"metadata_id"`
	RepositoryID  uuid.UUID `json:"repository_id"`
	PullRequestID uuid.UUID `json:"pull_request_id"`
	PolicyID      uuid.UUID `json:"policy_id"`
	PolicyVersion int       `json:"policy_version"`
	HeadSHA       string    `json:"head_sha"`
	OutputKey     string    `json:"review_output_key"`
}

func newRunCodeReviewHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
		if stores == nil || stores.CodeReviews == nil {
			return fmt.Errorf("code review store unavailable")
		}
		var job runCodeReviewPayload
		if err := json.Unmarshal(payload, &job); err != nil {
			return fmt.Errorf("decode code review job payload: %w", err)
		}
		if job.OrgID == uuid.Nil || job.SessionID == uuid.Nil {
			return fmt.Errorf("org_id and session_id are required")
		}
		if _, err := stores.CodeReviews.MarkRunning(ctx, job.OrgID, job.SessionID); err != nil {
			return fmt.Errorf("mark code review running: %w", err)
		}
		policy, err := stores.CodeReviews.GetPolicyByID(ctx, job.OrgID, job.PolicyID)
		if err != nil {
			return fmt.Errorf("load captured code review policy: %w", err)
		}
		decision, body := buildUnavailableCodeReviewOutcome(policy.Config(), job)
		raw := "code review orchestration completed in conservative human-review mode"
		result := &models.CodeReviewAgentResult{
			OrgID:         job.OrgID,
			SessionID:     job.SessionID,
			AgentProvider: "143",
			Role:          models.CodeReviewAgentRoleOrchestrator,
			Status:        models.CodeReviewAgentResultStatusCompleted,
			RawOutput:     &raw,
		}
		if err := stores.CodeReviews.CreateAgentResult(ctx, result); err != nil {
			return fmt.Errorf("create conservative code review result: %w", err)
		}
		submission, submitted, err := submitCodeReviewToGitHub(ctx, stores, services, job, body)
		if err != nil {
			return err
		}
		if _, err := stores.CodeReviews.CompleteReview(ctx, job.OrgID, db.CompleteCodeReviewParams{
			SessionID:       job.SessionID,
			Decision:        decision.Decision,
			Acceptable:      decision.Acceptable,
			GitHubReviewID:  submission.GitHubReviewID,
			GitHubReviewURL: submission.GitHubReviewURL,
			FinalReviewBody: body,
		}); err != nil {
			return fmt.Errorf("complete conservative code review: %w", err)
		}
		event := logger.Info().
			Str("org_id", job.OrgID.String()).
			Str("session_id", job.SessionID.String()).
			Bool("github_submitted", submitted)
		if submission.GitHubReviewID != nil {
			event = event.Int64("github_review_id", *submission.GitHubReviewID)
		}
		event.Msg("completed conservative code review")
		return nil
	}
}

func buildUnavailableCodeReviewOutcome(policy models.CodeReviewPolicyConfig, job runCodeReviewPayload) (models.CodeReviewDecisionEvaluation, string) {
	reason := "Automated reviewer agents are not configured for this worker."
	risk := models.CodeReviewRiskEvaluation{Acceptable: false, Reasons: []string{reason}}
	decision := models.EvaluateCodeReviewDecision(policy, risk)
	body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{
		Decision:      decision.Decision,
		Acceptable:    decision.Acceptable,
		RiskReasons:   decision.RiskReasons,
		PolicyVersion: job.PolicyVersion,
		HeadSHA:       job.HeadSHA,
		Summary:       "143 recorded the review request and withheld automated approval.",
	})
	return decision, body
}

type codeReviewSubmission struct {
	GitHubReviewID  *int64
	GitHubReviewURL *string
}

func submitCodeReviewToGitHub(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, body string) (codeReviewSubmission, bool, error) {
	if services == nil || services.CodeReviews == nil {
		return codeReviewSubmission{}, false, nil
	}
	if stores.Repositories == nil || stores.PullRequests == nil {
		return codeReviewSubmission{}, false, fmt.Errorf("submit code review: repository and pull request stores are required")
	}

	repo, err := stores.Repositories.GetByID(ctx, job.OrgID, job.RepositoryID)
	if err != nil {
		return codeReviewSubmission{}, false, fmt.Errorf("load code review repository: %w", err)
	}
	if repo.InstallationID == 0 {
		return codeReviewSubmission{}, false, fmt.Errorf("submit code review: repository %s has no GitHub installation id", repo.ID)
	}
	pr, err := stores.PullRequests.GetByID(ctx, job.OrgID, job.PullRequestID)
	if err != nil {
		return codeReviewSubmission{}, false, fmt.Errorf("load code review pull request: %w", err)
	}

	repository := strings.TrimSpace(pr.GitHubRepo)
	if repository == "" {
		repository = strings.TrimSpace(repo.FullName)
	}
	findings, err := stores.CodeReviews.ListFindings(ctx, job.OrgID, job.SessionID, true)
	if err != nil {
		return codeReviewSubmission{}, false, fmt.Errorf("list selected code review findings: %w", err)
	}
	comments := codeReviewInlineComments(findings)
	result, err := services.CodeReviews.SubmitReview(ctx, codereviewsvc.SubmitReviewRequest{
		InstallationID: repo.InstallationID,
		Repository:     repository,
		PullNumber:     pr.GitHubPRNumber,
		HeadSHA:        job.HeadSHA,
		Decision:       codereviewsvc.SubmitReviewDecisionCommentOnly,
		Body:           body,
		Comments:       comments,
	})
	if err != nil {
		return codeReviewSubmission{}, false, fmt.Errorf("submit code review to GitHub: %w", err)
	}
	return codeReviewSubmission{
		GitHubReviewID:  &result.ID,
		GitHubReviewURL: &result.URL,
	}, true, nil
}

func codeReviewInlineComments(findings []models.CodeReviewFinding) []codereviewsvc.SubmitReviewComment {
	comments := make([]codereviewsvc.SubmitReviewComment, 0, len(findings))
	for _, finding := range findings {
		if finding.Path == nil || strings.TrimSpace(*finding.Path) == "" || finding.StartLine == nil || *finding.StartLine <= 0 {
			continue
		}
		body := strings.TrimSpace(finding.Body)
		if body == "" {
			body = strings.TrimSpace(finding.Summary)
		}
		if body == "" {
			continue
		}
		comments = append(comments, codereviewsvc.SubmitReviewComment{
			Path: *finding.Path,
			Line: *finding.StartLine,
			Body: body,
		})
	}
	return comments
}
