package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
)

type githubAutomationStore interface {
	ListEnabledByGitHubEvent(ctx context.Context, orgID, repositoryID uuid.UUID, event models.AutomationGitHubEvent) ([]models.Automation, error)
}

type githubAutomationRunStore interface {
	CreateRunInTx(ctx context.Context, tx pgx.Tx, run *models.AutomationRun) (bool, error)
	ClaimTriggerDedupe(ctx context.Context, orgID, automationID uuid.UUID, dedupeKey string, expiresAt time.Time) (bool, error)
}

type githubAutomationJobStore interface {
	EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	Notify(ctx context.Context, jobID uuid.UUID)
}

type githubEventTxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type GitHubEventTriggerRequest struct {
	OrgID             uuid.UUID
	RepositoryID      uuid.UUID
	Event             models.AutomationGitHubEvent
	Repository        string
	PullRequestNumber int
	PullRequestURL    string
	PullRequestTitle  string
	HeadSHA           string
	Actor             string
	ActorType         string
	Body              string
	ProviderEventID   string
	EventID           string
	DedupeGroupID     string
	BaseBranch        string
	Path              string
	ReviewState       string
}

type GitHubEventTriggerService struct {
	automations  githubAutomationStore
	runs         githubAutomationRunStore
	jobs         githubAutomationJobStore
	txStarter    githubEventTxStarter
	capabilities githubCapabilityResolver
	logger       zerolog.Logger
	now          func() time.Time
}

type githubCapabilityResolver interface {
	ResolveForSession(ctx context.Context, in agentcapabilities.ResolveInput) ([]models.AgentCapabilitySnapshotItem, error)
}

const githubFeedbackDebounceWindow = 90 * time.Second

func NewGitHubEventTriggerService(automations githubAutomationStore, runs githubAutomationRunStore, jobs githubAutomationJobStore, txStarter githubEventTxStarter, logger zerolog.Logger) *GitHubEventTriggerService {
	return &GitHubEventTriggerService{
		automations: automations,
		runs:        runs,
		jobs:        jobs,
		txStarter:   txStarter,
		logger:      logger,
		now:         time.Now,
	}
}

func (s *GitHubEventTriggerService) SetCapabilityResolver(resolver githubCapabilityResolver) {
	s.capabilities = resolver
}

func (s *GitHubEventTriggerService) TriggerGitHubEvent(ctx context.Context, req GitHubEventTriggerRequest) error {
	if s == nil || s.automations == nil || s.runs == nil || s.jobs == nil || s.txStarter == nil {
		return nil
	}
	if err := req.Event.Validate(); err != nil {
		return err
	}
	req = normalizeGitHubEventTriggerRequest(req)
	automations, err := s.automations.ListEnabledByGitHubEvent(ctx, req.OrgID, req.RepositoryID, req.Event)
	if err != nil {
		return fmt.Errorf("list github event automations: %w", err)
	}
	for _, automation := range automations {
		if err := s.triggerAutomation(ctx, automation, req); err != nil {
			return err
		}
	}
	return nil
}

func (s *GitHubEventTriggerService) triggerAutomation(ctx context.Context, automation models.Automation, req GitHubEventTriggerRequest) error {
	matches, err := automationMatchesGitHubEventFilters(automation, req)
	if err != nil {
		return err
	}
	if !matches {
		return nil
	}
	if claimed, err := s.claimDedupe(ctx, automation, req); err != nil {
		return err
	} else if !claimed {
		return nil
	}

	configSnapshot, err := automation.BuildConfigSnapshot()
	if err != nil {
		return fmt.Errorf("build config snapshot: %w", err)
	}
	configSnapshot, err = withGitHubEventSnapshot(configSnapshot, req)
	if err != nil {
		return err
	}

	triggerContext, err := githubRunTriggerContext(req)
	if err != nil {
		return err
	}
	provider := models.AutomationEventProviderGitHub
	run := models.AutomationRun{
		AutomationID:    automation.ID,
		OrgID:           automation.OrgID,
		TriggeredBy:     models.AutomationTriggeredByGitHub,
		Provider:        &provider,
		ProviderEventID: optionalString(req.ProviderEventID),
		TriggerContext:  triggerContext,
		GoalSnapshot:    githubEventGoalSnapshot(automation.Goal, req),
		ConfigSnapshot:  configSnapshot,
		Status:          models.AutomationRunStatusPending,
	}
	if s.capabilities != nil {
		snapshot, err := s.capabilities.ResolveForSession(ctx, agentcapabilities.ResolveInput{
			OrgID:         automation.OrgID,
			RepositoryID:  automation.RepositoryID,
			SessionOrigin: models.SessionOriginAutomation,
			AutomationID:  &automation.ID,
		})
		if err != nil {
			return fmt.Errorf("resolve github-triggered automation capabilities: %w", err)
		}
		run.CapabilitySnapshot = snapshot
	}
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin github automation run tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := s.runs.CreateRunInTx(ctx, tx, &run)
	if err != nil {
		return fmt.Errorf("create github-triggered automation run: %w", err)
	}
	if !created {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit duplicate github automation run tx: %w", err)
		}
		return nil
	}
	dedupeKey := fmt.Sprintf("automation_run:%s", run.ID.String())
	payload := map[string]string{
		"org_id":            automation.OrgID.String(),
		"automation_id":     automation.ID.String(),
		"automation_run_id": run.ID.String(),
	}
	jobID, err := s.jobs.EnqueueInTx(ctx, tx, automation.OrgID, "default", models.JobTypeAutomationRun, payload, 5, &dedupeKey)
	if err != nil {
		return fmt.Errorf("enqueue github-triggered automation run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit github automation run tx: %w", err)
	}
	s.jobs.Notify(ctx, jobID)
	return nil
}

func withGitHubEventSnapshot(raw json.RawMessage, req GitHubEventTriggerRequest) (json.RawMessage, error) {
	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("decode config snapshot: %w", err)
		}
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	decoded["github_event"] = string(req.Event)
	decoded["github_trigger"] = githubEventTriggerLabel(req.Event)
	if feedbackType := githubFeedbackType(req.Event); feedbackType != "" {
		decoded["github_feedback_type"] = feedbackType
	}
	decoded["github"] = map[string]any{
		"repository":          req.Repository,
		"pull_request_number": req.PullRequestNumber,
		"pull_request_url":    req.PullRequestURL,
		"actor":               req.Actor,
	}
	if req.PullRequestTitle != "" {
		decoded["github"].(map[string]any)["pull_request_title"] = req.PullRequestTitle
	}
	if req.HeadSHA != "" {
		decoded["github"].(map[string]any)["head_sha"] = req.HeadSHA
	}
	if req.ActorType != "" {
		decoded["github"].(map[string]any)["actor_type"] = req.ActorType
	}
	if req.ProviderEventID != "" {
		decoded["github"].(map[string]any)["provider_event_id"] = req.ProviderEventID
	}
	if req.EventID != "" {
		decoded["github"].(map[string]any)["event_id"] = req.EventID
	}
	if req.DedupeGroupID != "" {
		decoded["github"].(map[string]any)["dedupe_group_id"] = req.DedupeGroupID
	}
	if req.BaseBranch != "" {
		decoded["github"].(map[string]any)["base_branch"] = req.BaseBranch
	}
	if req.Path != "" {
		decoded["github"].(map[string]any)["path"] = req.Path
	}
	if req.ReviewState != "" {
		decoded["github"].(map[string]any)["review_state"] = req.ReviewState
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("encode config snapshot: %w", err)
	}
	return out, nil
}

func githubEventGoalSnapshot(goal string, req GitHubEventTriggerRequest) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(goal))
	b.WriteString("\n\nGitHub event context:\n")
	b.WriteString("- Trigger: ")
	b.WriteString(githubEventTriggerLabel(req.Event))
	if feedbackType := githubFeedbackType(req.Event); feedbackType != "" {
		b.WriteString("\n- Feedback type: ")
		b.WriteString(feedbackType)
	}
	b.WriteString("\n- Event: ")
	b.WriteString(string(req.Event))
	if req.Repository != "" {
		b.WriteString("\n- Repository: ")
		b.WriteString(req.Repository)
	}
	if req.PullRequestNumber > 0 {
		b.WriteString(fmt.Sprintf("\n- PR #%d", req.PullRequestNumber))
	}
	if req.PullRequestURL != "" {
		b.WriteString("\n- URL: ")
		b.WriteString(req.PullRequestURL)
	}
	if req.PullRequestTitle != "" {
		b.WriteString("\n- PR title: ")
		b.WriteString(req.PullRequestTitle)
	}
	if req.HeadSHA != "" {
		b.WriteString("\n- Head SHA: ")
		b.WriteString(req.HeadSHA)
	}
	if req.Actor != "" {
		b.WriteString("\n- Actor: ")
		b.WriteString(req.Actor)
	}
	if req.BaseBranch != "" {
		b.WriteString("\n- Base branch: ")
		b.WriteString(req.BaseBranch)
	}
	if req.Path != "" {
		b.WriteString("\n- Path: ")
		b.WriteString(req.Path)
	}
	if req.ReviewState != "" {
		b.WriteString("\n- Review state: ")
		b.WriteString(req.ReviewState)
	}
	if strings.TrimSpace(req.Body) != "" {
		b.WriteString("\n\nEvent text:\n")
		b.WriteString(strings.TrimSpace(req.Body))
	}
	return b.String()
}

func normalizeGitHubEventTriggerRequest(req GitHubEventTriggerRequest) GitHubEventTriggerRequest {
	req.Repository = strings.TrimSpace(req.Repository)
	req.PullRequestURL = strings.TrimSpace(req.PullRequestURL)
	if req.PullRequestURL == "" && req.Repository != "" && req.PullRequestNumber > 0 {
		req.PullRequestURL = fmt.Sprintf("https://github.com/%s/pull/%d", strings.Trim(req.Repository, "/"), req.PullRequestNumber)
	}
	req.PullRequestTitle = strings.TrimSpace(req.PullRequestTitle)
	req.HeadSHA = strings.TrimSpace(req.HeadSHA)
	req.Actor = strings.TrimSpace(req.Actor)
	req.ActorType = strings.TrimSpace(req.ActorType)
	if req.ActorType == "" && strings.HasSuffix(strings.ToLower(req.Actor), "[bot]") {
		req.ActorType = "Bot"
	}
	req.ProviderEventID = strings.TrimSpace(req.ProviderEventID)
	if req.ProviderEventID != "" && req.PullRequestNumber > 0 {
		suffix := fmt.Sprintf(":pr:%d", req.PullRequestNumber)
		if !strings.HasSuffix(req.ProviderEventID, suffix) {
			req.ProviderEventID += suffix
		}
	}
	req.EventID = strings.TrimSpace(req.EventID)
	req.DedupeGroupID = strings.TrimSpace(req.DedupeGroupID)
	return req
}

func githubRunTriggerContext(req GitHubEventTriggerRequest) (json.RawMessage, error) {
	context := map[string]any{
		"provider": "github",
		"event":    string(req.Event),
	}
	if req.ProviderEventID != "" {
		context["provider_event_id"] = req.ProviderEventID
	}
	if req.EventID != "" {
		context["event_id"] = req.EventID
	}
	if req.DedupeGroupID != "" {
		context["dedupe_group_id"] = req.DedupeGroupID
	}
	out, err := json.Marshal(context)
	if err != nil {
		return nil, fmt.Errorf("encode github trigger context: %w", err)
	}
	return out, nil
}

func optionalString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func (s *GitHubEventTriggerService) claimDedupe(ctx context.Context, automation models.Automation, req GitHubEventTriggerRequest) (bool, error) {
	key := githubTriggerDedupeKey(req)
	if key == "" {
		return true, nil
	}
	claimed, err := s.runs.ClaimTriggerDedupe(ctx, automation.OrgID, automation.ID, key, s.now().Add(githubFeedbackDebounceWindow))
	if err != nil {
		return false, fmt.Errorf("claim github trigger dedupe: %w", err)
	}
	return claimed, nil
}

func githubTriggerDedupeKey(req GitHubEventTriggerRequest) string {
	if !isGitHubFeedbackEvent(req.Event) {
		return ""
	}
	groupID := strings.TrimSpace(req.DedupeGroupID)
	if groupID != "" {
		return "feedback:" + groupID
	}
	eventID := strings.TrimSpace(req.EventID)
	if eventID != "" {
		return "feedback:" + eventID
	}
	if req.PullRequestNumber <= 0 {
		return ""
	}
	return fmt.Sprintf("feedback:pr:%s:%d", req.RepositoryID, req.PullRequestNumber)
}

func automationMatchesGitHubEventFilters(automation models.Automation, req GitHubEventTriggerRequest) (bool, error) {
	if len(automation.GitHubEventFilters) == 0 || string(automation.GitHubEventFilters) == "{}" {
		return true, nil
	}
	var filters models.AutomationGitHubEventFilters
	if err := json.Unmarshal(automation.GitHubEventFilters, &filters); err != nil {
		return false, fmt.Errorf("decode github event filters: %w", err)
	}
	if len(filters.BaseBranches) > 0 && req.BaseBranch != "" && !containsFold(filters.BaseBranches, req.BaseBranch) {
		return false, nil
	}
	if len(filters.Authors) > 0 && !containsFold(filters.Authors, req.Actor) {
		return false, nil
	}
	if len(filters.Paths) > 0 && req.Path != "" && !matchesPathFilter(filters.Paths, req.Path) {
		return false, nil
	}
	if len(filters.FeedbackTypes) > 0 && isGitHubFeedbackEvent(req.Event) && !containsFold(filters.FeedbackTypes, githubFeedbackType(req.Event)) {
		return false, nil
	}
	if len(filters.ReviewStates) > 0 && req.ReviewState != "" && !containsFold(filters.ReviewStates, req.ReviewState) {
		return false, nil
	}
	return true, nil
}

func containsFold(values []string, candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), candidate) {
			return true
		}
	}
	return false
}

func matchesPathFilter(patterns []string, path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(path, pattern) {
			return true
		}
		if pattern == path {
			return true
		}
		// Match at path-segment boundaries: prefix, middle, or suffix — not arbitrary substring.
		if strings.HasPrefix(path, pattern+"/") ||
			strings.Contains(path, "/"+pattern+"/") ||
			strings.HasSuffix(path, "/"+pattern) {
			return true
		}
	}
	return false
}

func isGitHubFeedbackEvent(event models.AutomationGitHubEvent) bool {
	switch event {
	case models.AutomationGitHubEventIssueCommentCreated,
		models.AutomationGitHubEventPullRequestReviewSubmitted,
		models.AutomationGitHubEventPullRequestReviewCommentCreated:
		return true
	default:
		return false
	}
}

func githubEventTriggerLabel(event models.AutomationGitHubEvent) string {
	switch event {
	case models.AutomationGitHubEventPullRequestOpened:
		return "PR opened"
	case models.AutomationGitHubEventPullRequestUpdated:
		return "PR updated"
	case models.AutomationGitHubEventPullRequestMerged:
		return "PR merged"
	case models.AutomationGitHubEventCheckSuiteCompleted,
		models.AutomationGitHubEventCheckRunCompleted:
		return "Checks finished"
	case models.AutomationGitHubEventIssueCommentCreated,
		models.AutomationGitHubEventPullRequestReviewSubmitted,
		models.AutomationGitHubEventPullRequestReviewCommentCreated:
		return "New PR feedback"
	default:
		return string(event)
	}
}

func githubFeedbackType(event models.AutomationGitHubEvent) string {
	switch event {
	case models.AutomationGitHubEventIssueCommentCreated:
		return "PR conversation comment"
	case models.AutomationGitHubEventPullRequestReviewSubmitted:
		return "Submitted review"
	case models.AutomationGitHubEventPullRequestReviewCommentCreated:
		return "Inline review comment"
	default:
		return ""
	}
}
