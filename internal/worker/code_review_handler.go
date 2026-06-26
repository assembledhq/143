package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
		pr, err := stores.PullRequests.GetByID(ctx, job.OrgID, job.PullRequestID)
		if err != nil {
			return fmt.Errorf("load code review pull request: %w", err)
		}
		health, err := loadStoredCodeReviewHealth(ctx, stores, job, pr)
		if err != nil {
			return fmt.Errorf("load code review health: %w", err)
		}
		agentResults, err := stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
		if err != nil {
			return fmt.Errorf("list code review agent results: %w", err)
		}
		findings, err := stores.CodeReviews.ListFindings(ctx, job.OrgID, job.SessionID, false)
		if err != nil {
			return fmt.Errorf("list code review findings: %w", err)
		}
		changedFiles, changedFilesAvailable, err := loadCodeReviewChangedFiles(ctx, stores, services, job, pr)
		if err != nil {
			return fmt.Errorf("load code review changed files: %w", err)
		}
		decision, body := evaluateLiveCodeReviewOutcome(liveCodeReviewOutcomeInput{
			Policy:                policy.Config(),
			Job:                   job,
			PullRequest:           pr,
			Health:                health,
			AgentResults:          agentResults,
			Findings:              findings,
			ChangedFiles:          changedFiles,
			ChangedFilesAvailable: changedFilesAvailable,
		})
		raw := "code review orchestration evaluated stored reviewer evidence and pull request health"
		result := &models.CodeReviewAgentResult{
			OrgID:         job.OrgID,
			SessionID:     job.SessionID,
			AgentProvider: "143",
			Role:          models.CodeReviewAgentRoleOrchestrator,
			Status:        models.CodeReviewAgentResultStatusCompleted,
			RawOutput:     &raw,
		}
		if err := stores.CodeReviews.CreateAgentResult(ctx, result); err != nil {
			return fmt.Errorf("create code review orchestration result: %w", err)
		}
		submission, submitted, err := submitCodeReviewToGitHub(ctx, stores, services, job, decision.Decision, body)
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
			return fmt.Errorf("complete code review: %w", err)
		}
		event := logger.Info().
			Str("org_id", job.OrgID.String()).
			Str("session_id", job.SessionID.String()).
			Bool("github_submitted", submitted)
		if submission.GitHubReviewID != nil {
			event = event.Int64("github_review_id", *submission.GitHubReviewID)
		}
		event.Str("decision", string(decision.Decision)).Msg("completed code review")
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
		Template:      policy.FinalReviewTemplate,
	})
	return decision, body
}

type codeReviewSubmission struct {
	GitHubReviewID  *int64
	GitHubReviewURL *string
}

type liveCodeReviewOutcomeInput struct {
	Policy                models.CodeReviewPolicyConfig
	Job                   runCodeReviewPayload
	PullRequest           models.PullRequest
	Health                *models.PullRequestHealthResponse
	AgentResults          []models.CodeReviewAgentResult
	Findings              []models.CodeReviewFinding
	ChangedFiles          []codereviewsvc.PullRequestFile
	ChangedFilesAvailable bool
}

func evaluateLiveCodeReviewOutcome(input liveCodeReviewOutcomeInput) (models.CodeReviewDecisionEvaluation, string) {
	policy := models.ResolveCodeReviewPolicyConfig(&input.Policy)
	reviewerQuorum, reviewerFailures := codeReviewReviewerEvidence(input.AgentResults)
	blockingFindings := codeReviewBlockingFindings(input.Findings)
	risk := models.EvaluateCodeReviewRisk(policy, models.CodeReviewRiskInput{
		FilesChanged:           len(input.ChangedFiles),
		LinesChanged:           codeReviewLinesChanged(input.ChangedFiles),
		ChangedPaths:           codeReviewChangedPaths(input.ChangedFiles),
		Categories:             codeReviewChangedCategories(input.ChangedFiles),
		ChecksPassing:          codeReviewChecksPassing(policy, input.Health),
		RequiredChecksPassing:  codeReviewRequiredChecksPassing(policy, input.Health),
		DescriptionPassed:      codeReviewDescriptionPassed(policy, input.PullRequest),
		Mergeable:              codeReviewMergeable(input.Health),
		UpToDate:               codeReviewUpToDate(input.Health),
		Author:                 codeReviewAuthor(input.PullRequest),
		ContextFetchFailed:     input.Health == nil || !input.ChangedFilesAvailable,
		HeadSHAChanged:         codeReviewHeadChanged(input.Job.HeadSHA, input.PullRequest, input.Health),
		BlockingFindings:       blockingFindings,
		ReviewerDisagreement:   reviewerFailures > 0,
		UnresolvedHumanThreads: 0,
	})
	if reviewerQuorum < policy.AgentRoster.RequireReviewerQuorum {
		risk.Acceptable = false
		risk.Reasons = append(risk.Reasons, fmt.Sprintf("reviewer quorum %d is below policy requirement %d", reviewerQuorum, policy.AgentRoster.RequireReviewerQuorum))
	}
	decision := models.EvaluateCodeReviewDecision(policy, risk)
	body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{
		Decision:      decision.Decision,
		Acceptable:    decision.Acceptable,
		RiskReasons:   decision.RiskReasons,
		PolicyVersion: input.Job.PolicyVersion,
		HeadSHA:       input.Job.HeadSHA,
		Summary:       codeReviewOutcomeSummary(decision),
		Template:      policy.FinalReviewTemplate,
	})
	return decision, body
}

type codeReviewFileLister interface {
	ListPullRequestFiles(ctx context.Context, req codereviewsvc.PullRequestFilesRequest) ([]codereviewsvc.PullRequestFile, error)
}

func loadCodeReviewChangedFiles(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, pr models.PullRequest) ([]codereviewsvc.PullRequestFile, bool, error) {
	if services == nil || services.CodeReviews == nil {
		return nil, false, nil
	}
	lister, ok := services.CodeReviews.(codeReviewFileLister)
	if !ok {
		return nil, false, nil
	}
	if stores == nil || stores.Repositories == nil {
		return nil, false, fmt.Errorf("repository store is required")
	}
	repo, err := stores.Repositories.GetByID(ctx, job.OrgID, job.RepositoryID)
	if err != nil {
		return nil, false, fmt.Errorf("load code review repository: %w", err)
	}
	if repo.InstallationID == 0 {
		return nil, false, fmt.Errorf("repository %s has no GitHub installation id", repo.ID)
	}
	repository := strings.TrimSpace(pr.GitHubRepo)
	if repository == "" {
		repository = strings.TrimSpace(repo.FullName)
	}
	files, err := lister.ListPullRequestFiles(ctx, codereviewsvc.PullRequestFilesRequest{
		InstallationID: repo.InstallationID,
		Repository:     repository,
		PullNumber:     pr.GitHubPRNumber,
	})
	if err != nil {
		return nil, false, err
	}
	return files, true, nil
}

func loadStoredCodeReviewHealth(ctx context.Context, stores *Stores, job runCodeReviewPayload, pr models.PullRequest) (*models.PullRequestHealthResponse, error) {
	if stores == nil || stores.PullRequests == nil {
		return nil, nil
	}
	resp := &models.PullRequestHealthResponse{
		PullRequestID:     pr.ID,
		PullRequestNumber: pr.GitHubPRNumber,
		Repository:        pr.GitHubRepo,
		URL:               pr.GitHubPRURL,
		Status:            pr.Status,
		MergeState:        pr.MergeState,
		HasConflicts:      pr.HasConflicts,
		FailingTestCount:  pr.FailingTestCount,
		HealthVersion:     pr.HealthVersion,
		CanMerge:          pr.Status == models.PullRequestStatusOpen && pr.MergeState == models.PullRequestMergeStateClean && !pr.HasConflicts && pr.FailingTestCount == 0,
	}
	if pr.HeadSHA != nil {
		resp.HeadSHA = *pr.HeadSHA
	}
	if pr.BaseSHA != nil {
		resp.BaseSHA = *pr.BaseSHA
	}
	current, err := stores.PullRequests.GetHealthCurrent(ctx, job.OrgID, job.PullRequestID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return resp, nil
	}
	var summary models.PullRequestHealthSummary
	if err := json.Unmarshal(current.SummaryJSON, &summary); err != nil {
		return nil, fmt.Errorf("decode code review health summary: %w", err)
	}
	resp.HeadSHA = current.HeadSHA
	resp.BaseSHA = current.BaseSHA
	resp.MergeState = summary.MergeState
	resp.HasConflicts = summary.HasConflicts
	resp.FailingTestCount = summary.FailingTestCount
	resp.Checks = summary.Checks
	resp.ChecksConfirmed = summary.ChecksConfirmed || len(summary.Checks) > 0
	resp.CanMerge = pr.Status == models.PullRequestStatusOpen &&
		summary.MergeState == models.PullRequestMergeStateClean &&
		!summary.HasConflicts &&
		codeReviewAllChecksPassing(summary.ChecksConfirmed, summary.Checks)
	return resp, nil
}

func codeReviewReviewerEvidence(results []models.CodeReviewAgentResult) (quorum int, failures int) {
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleReviewer {
			continue
		}
		switch result.Status {
		case models.CodeReviewAgentResultStatusCompleted:
			quorum++
		case models.CodeReviewAgentResultStatusFailed, models.CodeReviewAgentResultStatusTimedOut:
			failures++
		}
	}
	return quorum, failures
}

func codeReviewBlockingFindings(findings []models.CodeReviewFinding) int {
	count := 0
	for _, finding := range findings {
		switch finding.Severity {
		case models.CodeReviewFindingSeverityHigh, models.CodeReviewFindingSeverityCritical:
			count++
		}
	}
	return count
}

func codeReviewLinesChanged(files []codereviewsvc.PullRequestFile) int {
	lines := 0
	for _, file := range files {
		lines += file.Additions + file.Deletions
	}
	return lines
}

func codeReviewChangedPaths(files []codereviewsvc.PullRequestFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.Filename) == "" {
			continue
		}
		paths = append(paths, file.Filename)
	}
	return paths
}

func codeReviewChangedCategories(files []codereviewsvc.PullRequestFile) []string {
	seen := make(map[string]struct{})
	categories := make([]string, 0)
	for _, file := range files {
		for _, category := range codeReviewPathCategories(file.Filename) {
			if _, ok := seen[category]; ok {
				continue
			}
			seen[category] = struct{}{}
			categories = append(categories, category)
		}
	}
	return categories
}

func codeReviewPathCategories(path string) []string {
	normalized := strings.ToLower(strings.TrimSpace(path))
	categories := make([]string, 0, 2)
	switch {
	case normalized == "go.mod" || normalized == "go.sum" ||
		strings.HasSuffix(normalized, "package-lock.json") ||
		strings.HasSuffix(normalized, "pnpm-lock.yaml") ||
		strings.HasSuffix(normalized, "yarn.lock") ||
		strings.HasSuffix(normalized, "cargo.lock") ||
		strings.HasSuffix(normalized, "poetry.lock") ||
		strings.Contains(normalized, "requirements.txt"):
		categories = append(categories, "dependencies")
	}
	if strings.Contains(normalized, "migration") || strings.Contains(normalized, "/migrations/") {
		categories = append(categories, "migrations")
	}
	if strings.Contains(normalized, "auth") || strings.Contains(normalized, "session") {
		categories = append(categories, "auth")
	}
	if strings.Contains(normalized, "billing") || strings.Contains(normalized, "invoice") || strings.Contains(normalized, "payment") {
		categories = append(categories, "billing")
	}
	if strings.Contains(normalized, "permission") || strings.Contains(normalized, "role") || strings.Contains(normalized, "rbac") {
		categories = append(categories, "permissions")
	}
	if strings.Contains(normalized, "crypto") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "token") {
		categories = append(categories, "crypto")
	}
	if strings.Contains(normalized, ".github/workflows/") || strings.Contains(normalized, "terraform") || strings.Contains(normalized, "deploy") || strings.Contains(normalized, "infra") {
		categories = append(categories, "infra")
	}
	return categories
}

func codeReviewDescriptionPassed(policy models.CodeReviewPolicyConfig, pr models.PullRequest) bool {
	for _, requirement := range policy.DescriptionPolicy.Requirements {
		if !requirement.Required {
			continue
		}
		if strings.TrimSpace(requirement.Key) == "" {
			continue
		}
		if pr.Body == nil || strings.TrimSpace(*pr.Body) == "" {
			return false
		}
	}
	return true
}

func codeReviewChecksPassing(policy models.CodeReviewPolicyConfig, health *models.PullRequestHealthResponse) bool {
	if !policy.RiskPolicy.RequirePassingChecks {
		return true
	}
	if health == nil {
		return false
	}
	return codeReviewAllChecksPassing(health.ChecksConfirmed, health.Checks)
}

func codeReviewRequiredChecksPassing(policy models.CodeReviewPolicyConfig, health *models.PullRequestHealthResponse) map[string]bool {
	statuses := make(map[string]bool, len(policy.RiskPolicy.RequiredChecks))
	if health == nil {
		return statuses
	}
	for _, required := range policy.RiskPolicy.RequiredChecks {
		required = strings.TrimSpace(required)
		if required == "" {
			continue
		}
		for _, check := range health.Checks {
			if strings.EqualFold(strings.TrimSpace(check.Name), required) && check.Status == models.PullRequestCheckStatusPassed {
				statuses[required] = true
				break
			}
		}
	}
	return statuses
}

func codeReviewAllChecksPassing(confirmed bool, checks []models.PullRequestCheckSummary) bool {
	if len(checks) == 0 {
		return confirmed
	}
	for _, check := range checks {
		if check.Status != models.PullRequestCheckStatusPassed {
			return false
		}
	}
	return true
}

func codeReviewMergeable(health *models.PullRequestHealthResponse) bool {
	return health != nil && health.Status == models.PullRequestStatusOpen && health.CanMerge && health.MergeState == models.PullRequestMergeStateClean && !health.HasConflicts
}

func codeReviewUpToDate(health *models.PullRequestHealthResponse) bool {
	return health != nil && health.MergeState != models.PullRequestMergeStateBehind
}

func codeReviewHeadChanged(reviewedHead string, pr models.PullRequest, health *models.PullRequestHealthResponse) bool {
	if reviewedHead == "" {
		return true
	}
	if pr.HeadSHA != nil && strings.TrimSpace(*pr.HeadSHA) != "" && *pr.HeadSHA != reviewedHead {
		return true
	}
	if health != nil && strings.TrimSpace(health.HeadSHA) != "" && health.HeadSHA != reviewedHead {
		return true
	}
	return false
}

func codeReviewAuthor(pr models.PullRequest) string {
	return string(pr.AuthoredBy)
}

func codeReviewOutcomeSummary(decision models.CodeReviewDecisionEvaluation) string {
	if decision.Decision == models.CodeReviewDecisionApproved {
		return "143 reviewed the stored PR health and reviewer evidence and found the change acceptable under policy."
	}
	return "143 reviewed the stored PR health and reviewer evidence and withheld automated approval."
}

func submitCodeReviewToGitHub(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, decision models.CodeReviewDecision, body string) (codeReviewSubmission, bool, error) {
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
		Decision:       codeReviewSubmitDecision(decision),
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

func codeReviewSubmitDecision(decision models.CodeReviewDecision) codereviewsvc.SubmitReviewDecision {
	if decision == models.CodeReviewDecisionApproved {
		return codereviewsvc.SubmitReviewDecisionApproved
	}
	return codereviewsvc.SubmitReviewDecisionCommentOnly
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
