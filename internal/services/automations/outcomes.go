package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

var (
	ErrOutcomeReasonRequired    = errors.New("automation outcome reason is required")
	ErrOutcomeTargetUnavailable = errors.New("automation run does not contain a GitHub pull request target")
	ErrOutcomeActionRequired    = errors.New("changes_requested requires a linked GitHub changes-requested review")
	ErrOutcomeActionInvalid     = errors.New("automation outcome external action is invalid")
	ErrOutcomeSessionMismatch   = errors.New("session is not linked to the automation run")
	ErrOutcomeAlreadyReported   = errors.New("automation run outcome was already reported")
)

type automationOutcomeRunStore interface {
	GetByRunID(ctx context.Context, orgID, runID uuid.UUID) (models.AutomationRun, error)
}

type automationOutcomeWriter interface {
	Create(ctx context.Context, orgID uuid.UUID, outcome *models.AutomationRunOutcome, action *models.AutomationRunExternalAction) (models.AutomationRunOutcome, error)
}

type OutcomeService struct {
	runs     automationOutcomeRunStore
	outcomes automationOutcomeWriter
}

func NewOutcomeService(runs automationOutcomeRunStore, outcomes automationOutcomeWriter) *OutcomeService {
	return &OutcomeService{runs: runs, outcomes: outcomes}
}

type ReportOutcomeRequest struct {
	SessionID          uuid.UUID
	RunID              uuid.UUID
	Decision           models.AutomationOutcomeDecision
	Reason             string
	PullRequestTitle   string
	HeadSHA            string
	ExternalActionType models.AutomationExternalActionType
	ExternalActionURL  string
	ExternalActionID   string
}

type githubOutcomeTarget struct {
	Repository        string
	PullRequestNumber int
	PullRequestURL    string
	PullRequestTitle  string
	HeadSHA           string
}

func (s *OutcomeService) Report(ctx context.Context, orgID uuid.UUID, req ReportOutcomeRequest) (models.AutomationRunOutcome, error) {
	if err := req.Decision.Validate(); err != nil {
		return models.AutomationRunOutcome{}, err
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return models.AutomationRunOutcome{}, ErrOutcomeReasonRequired
	}
	run, err := s.runs.GetByRunID(ctx, orgID, req.RunID)
	if err != nil {
		return models.AutomationRunOutcome{}, fmt.Errorf("load automation run for outcome: %w", err)
	}
	target, err := outcomeTargetFromRun(run)
	if err != nil {
		return models.AutomationRunOutcome{}, err
	}
	// Prefer server-captured trigger metadata when it exists. The optional
	// agent values only enrich legacy snapshots that predate title/head capture.
	title := firstNonEmpty(target.PullRequestTitle, req.PullRequestTitle)
	headSHA := firstNonEmpty(target.HeadSHA, req.HeadSHA)
	outcome := models.AutomationRunOutcome{
		OrgID:             orgID,
		AutomationID:      run.AutomationID,
		AutomationRunID:   run.ID,
		SessionID:         req.SessionID,
		Repository:        target.Repository,
		PullRequestNumber: target.PullRequestNumber,
		PullRequestURL:    target.PullRequestURL,
		PullRequestTitle:  optionalTrimmed(title),
		HeadSHA:           optionalTrimmed(headSHA),
		Decision:          req.Decision,
		Reason:            reason,
		Source:            models.AutomationOutcomeSourceAgentReported,
	}

	action, err := buildOutcomeAction(req, target)
	if err != nil {
		return models.AutomationRunOutcome{}, err
	}
	if req.Decision == models.AutomationOutcomeDecisionChangesRequested && (action == nil || action.ActionType != models.AutomationExternalActionGitHubReviewChangesRequested) {
		return models.AutomationRunOutcome{}, ErrOutcomeActionRequired
	}

	created, err := s.outcomes.Create(ctx, orgID, &outcome, action)
	if errors.Is(err, db.ErrAutomationOutcomeAlreadyReported) {
		return models.AutomationRunOutcome{}, ErrOutcomeAlreadyReported
	}
	if err != nil {
		return models.AutomationRunOutcome{}, fmt.Errorf("record automation outcome: %w", err)
	}
	return created, nil
}

func outcomeTargetFromRun(run models.AutomationRun) (githubOutcomeTarget, error) {
	if run.TriggeredBy != models.AutomationTriggeredByGitHub || len(run.ConfigSnapshot) == 0 {
		return githubOutcomeTarget{}, ErrOutcomeTargetUnavailable
	}
	var snapshot struct {
		GitHub struct {
			Repository        string          `json:"repository"`
			PullRequestNumber json.RawMessage `json:"pull_request_number"`
			PullRequestURL    string          `json:"pull_request_url"`
			PullRequestTitle  string          `json:"pull_request_title"`
			HeadSHA           string          `json:"head_sha"`
		} `json:"github"`
	}
	if err := json.Unmarshal(run.ConfigSnapshot, &snapshot); err != nil {
		return githubOutcomeTarget{}, fmt.Errorf("decode automation GitHub target: %w", err)
	}
	number, err := parseJSONPositiveInt(snapshot.GitHub.PullRequestNumber)
	repository := strings.Trim(strings.TrimSpace(snapshot.GitHub.Repository), "/")
	if err != nil || repository == "" {
		return githubOutcomeTarget{}, ErrOutcomeTargetUnavailable
	}
	pullRequestURL := strings.TrimSpace(snapshot.GitHub.PullRequestURL)
	if pullRequestURL == "" {
		pullRequestURL = fmt.Sprintf("https://github.com/%s/pull/%d", repository, number)
	}
	return githubOutcomeTarget{
		Repository:        repository,
		PullRequestNumber: number,
		PullRequestURL:    pullRequestURL,
		PullRequestTitle:  strings.TrimSpace(snapshot.GitHub.PullRequestTitle),
		HeadSHA:           strings.TrimSpace(snapshot.GitHub.HeadSHA),
	}, nil
}

func parseJSONPositiveInt(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing integer")
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil && number > 0 {
		return number, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	number, err := strconv.Atoi(text)
	if err != nil || number <= 0 {
		return 0, errors.New("integer must be positive")
	}
	return number, nil
}

func buildOutcomeAction(req ReportOutcomeRequest, target githubOutcomeTarget) (*models.AutomationRunExternalAction, error) {
	actionURL := strings.TrimSpace(req.ExternalActionURL)
	actionID := strings.TrimSpace(req.ExternalActionID)
	if req.ExternalActionType == "" {
		if actionURL != "" || actionID != "" {
			return nil, ErrOutcomeActionInvalid
		}
		return nil, nil
	}
	if err := req.ExternalActionType.Validate(); err != nil {
		return nil, err
	}
	if actionURL == "" || !githubActionMatchesTarget(actionURL, target) {
		return nil, ErrOutcomeActionInvalid
	}
	if req.ExternalActionType == models.AutomationExternalActionGitHubReviewChangesRequested && !githubURLLinksReview(actionURL) {
		return nil, ErrOutcomeActionInvalid
	}
	return &models.AutomationRunExternalAction{
		Provider:           "github",
		ActionType:         req.ExternalActionType,
		ExternalID:         optionalTrimmed(actionID),
		URL:                actionURL,
		VerificationStatus: models.AutomationExternalActionVerificationReported,
	}, nil
}

func githubURLLinksReview(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.HasPrefix(parsed.Fragment, "pullrequestreview-") || strings.Contains(parsed.Path, "/reviews/")
}

func githubActionMatchesTarget(rawURL string, target githubOutcomeTarget) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.Scheme != "https" {
		return false
	}
	wantPath := "/" + strings.Trim(target.Repository, "/") + "/pull/" + strconv.Itoa(target.PullRequestNumber)
	path := strings.TrimSuffix(parsed.Path, "/")
	return path == wantPath || strings.HasPrefix(path, wantPath+"/")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func optionalTrimmed(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
