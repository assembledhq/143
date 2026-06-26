package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type LinearIssueEventTriggerRequest struct {
	OrgID           uuid.UUID
	ProviderEventID string
	EventType       models.LinearAutomationEvent
	OccurredAt      *time.Time
	Issue           LinearIssueEvent
}

type LinearIssueEvent struct {
	ID           string
	Identifier   string
	Title        string
	URL          string
	Description  string
	Priority     int
	PriorityName string
	StateName    string
	StateType    string
	TeamID       string
	TeamKey      string
	TeamName     string
	Labels       []string
	IssueType    string
}

type LinearEventTriggerService struct {
	triggers     pagerDutyEventTriggerStore
	automations  pagerDutyEventAutomationStore
	runs         pagerDutyEventAutomationRunStore
	jobs         pagerDutyEventJobStore
	txStarter    pagerDutyEventTxStarter
	defaultRepos pagerDutyDefaultRepositoryLoader
	capabilities githubCapabilityResolver
	logger       zerolog.Logger
	now          func() time.Time
}

func NewLinearEventTriggerService(
	triggers pagerDutyEventTriggerStore,
	automations pagerDutyEventAutomationStore,
	runs pagerDutyEventAutomationRunStore,
	jobs pagerDutyEventJobStore,
	txStarter pagerDutyEventTxStarter,
	logger zerolog.Logger,
) *LinearEventTriggerService {
	return &LinearEventTriggerService{
		triggers:    triggers,
		automations: automations,
		runs:        runs,
		jobs:        jobs,
		txStarter:   txStarter,
		logger:      logger,
		now:         time.Now,
	}
}

func (s *LinearEventTriggerService) SetCapabilityResolver(resolver githubCapabilityResolver) {
	s.capabilities = resolver
}

func (s *LinearEventTriggerService) SetDefaultRepositoryResolver(loader pagerDutyDefaultRepositoryLoader) {
	s.defaultRepos = loader
}

func (s *LinearEventTriggerService) TriggerLinearIssueEvent(ctx context.Context, req LinearIssueEventTriggerRequest) error {
	if s == nil || s.triggers == nil || s.automations == nil || s.runs == nil || s.jobs == nil || s.txStarter == nil {
		return nil
	}
	if err := req.EventType.Validate(); err != nil {
		return err
	}
	triggers, err := s.triggers.ListEnabledByProviderEvent(ctx, req.OrgID, models.AutomationEventProviderLinear, string(req.EventType))
	if err != nil {
		return fmt.Errorf("list linear event triggers: %w", err)
	}
	for _, trigger := range triggers {
		matches, err := linearTriggerMatches(trigger.Filter, req.Issue)
		if err != nil {
			return fmt.Errorf("match linear trigger %s: %w", trigger.ID, err)
		}
		if !matches {
			continue
		}
		if err := s.createRunForTrigger(ctx, trigger, req); err != nil {
			return err
		}
	}
	return nil
}

func (s *LinearEventTriggerService) createRunForTrigger(ctx context.Context, trigger models.AutomationEventTrigger, req LinearIssueEventTriggerRequest) error {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin linear automation run tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	automation, err := s.automations.LockByIDForUpdate(ctx, tx, req.OrgID, trigger.AutomationID)
	if err != nil {
		return fmt.Errorf("lock linear event automation: %w", err)
	}

	status := models.AutomationRunStatusPending
	var completedAt *time.Time
	var resultSummary *string
	if !automation.Enabled {
		status = models.AutomationRunStatusSkipped
		now := s.now()
		completedAt = &now
		summary := "automation disabled before Linear issue-triggered run could start"
		resultSummary = &summary
	} else {
		inFlight, err := s.automations.CountInFlightRuns(ctx, tx, req.OrgID, automation.ID)
		if err != nil {
			return fmt.Errorf("count linear automation in-flight runs: %w", err)
		}
		maxConcurrent := automation.MaxConcurrent
		if maxConcurrent <= 0 {
			maxConcurrent = 1
		}
		if inFlight >= maxConcurrent {
			status = models.AutomationRunStatusSkipped
			now := s.now()
			completedAt = &now
			summary := fmt.Sprintf("max_concurrent saturated for Linear issue trigger (%d/%d in flight)", inFlight, maxConcurrent)
			resultSummary = &summary
		}
	}

	filter, err := parseLinearTriggerFilter(trigger.Filter)
	if err != nil {
		return err
	}
	if status == models.AutomationRunStatusPending && filter.CooldownMinutes > 0 {
		since := s.now().Add(-time.Duration(filter.CooldownMinutes) * time.Minute)
		recent, err := s.runs.CountRecentProviderTriggerRuns(ctx, tx, req.OrgID, automation.ID, trigger.ID, models.AutomationEventProviderLinear, since)
		if err != nil {
			return fmt.Errorf("count linear automation cooldown runs: %w", err)
		}
		if recent > 0 {
			status = models.AutomationRunStatusSkipped
			now := s.now()
			completedAt = &now
			summary := fmt.Sprintf("cooldown active for Linear trigger (%d prior run(s) in the last %d minutes)", recent, filter.CooldownMinutes)
			resultSummary = &summary
		}
	}

	repository, err := s.resolveRepository(ctx, trigger, automation, req.OrgID)
	if err != nil {
		return err
	}
	if status == models.AutomationRunStatusPending && repository.RepositoryID == nil {
		status = models.AutomationRunStatusSkipped
		now := s.now()
		completedAt = &now
		summary := "repository_unmapped: Linear issue trigger is not mapped to a repository"
		resultSummary = &summary
	}

	configSnapshot, err := automation.BuildConfigSnapshot()
	if err != nil {
		return fmt.Errorf("build linear automation config snapshot: %w", err)
	}
	configSnapshot, err = withLinearEventSnapshot(configSnapshot, trigger, req, repository)
	if err != nil {
		return err
	}
	triggerContext, err := linearTriggerContext(trigger, req, repository)
	if err != nil {
		return err
	}

	provider := models.AutomationEventProviderLinear
	providerEventID := req.ProviderEventID
	run := models.AutomationRun{
		AutomationID:    automation.ID,
		OrgID:           automation.OrgID,
		TriggeredBy:     models.AutomationTriggeredByProviderEvent,
		TriggerID:       &trigger.ID,
		Provider:        &provider,
		ProviderEventID: &providerEventID,
		TriggerContext:  triggerContext,
		GoalSnapshot:    linearEventGoalSnapshot(automation.Goal, req),
		ConfigSnapshot:  configSnapshot,
		Status:          status,
		CompletedAt:     completedAt,
		ResultSummary:   resultSummary,
	}
	if status == models.AutomationRunStatusPending && s.capabilities != nil {
		snapshot, err := s.capabilities.ResolveForSession(ctx, agentcapabilities.ResolveInput{
			OrgID:         automation.OrgID,
			RepositoryID:  repository.RepositoryID,
			SessionOrigin: models.SessionOriginAutomation,
			AutomationID:  &automation.ID,
		})
		if err != nil {
			return fmt.Errorf("resolve linear-triggered automation capabilities: %w", err)
		}
		run.CapabilitySnapshot = snapshot
	}

	created, err := s.runs.CreateRunInTx(ctx, tx, &run)
	if err != nil {
		return fmt.Errorf("create linear-triggered automation run: %w", err)
	}
	if !created {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit duplicate linear automation run tx: %w", err)
		}
		return nil
	}

	var jobID uuid.UUID
	if status == models.AutomationRunStatusPending {
		dedupeKey := fmt.Sprintf("automation_run:%s", run.ID.String())
		payload := map[string]string{
			"org_id":            automation.OrgID.String(),
			"automation_id":     automation.ID.String(),
			"automation_run_id": run.ID.String(),
		}
		jobID, err = s.jobs.EnqueueInTx(ctx, tx, automation.OrgID, "default", models.JobTypeAutomationRun, payload, 5, &dedupeKey)
		if err != nil {
			return fmt.Errorf("enqueue linear-triggered automation run: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit linear automation run tx: %w", err)
	}
	if jobID != uuid.Nil {
		s.jobs.Notify(ctx, jobID)
	}
	return nil
}

type linearTriggerFilter struct {
	TeamKeys        []string `json:"team_keys"`
	TeamIDs         []string `json:"team_ids"`
	Labels          []string `json:"labels"`
	Tags            []string `json:"tags"`
	IssueTypes      []string `json:"issue_types"`
	StateTypes      []string `json:"state_types"`
	StateNames      []string `json:"state_names"`
	Priorities      []string `json:"priorities"`
	TitleContains   string   `json:"title_contains"`
	CooldownMinutes int      `json:"cooldown_minutes"`
}

func linearTriggerMatches(raw json.RawMessage, issue LinearIssueEvent) (bool, error) {
	filter, err := parseLinearTriggerFilter(raw)
	if err != nil {
		return false, err
	}
	if !matchesStringFilter(filter.TeamKeys, issue.TeamKey, true) {
		return false, nil
	}
	if !matchesStringFilter(filter.TeamIDs, issue.TeamID, true) {
		return false, nil
	}
	if !matchesAnyString(filter.Labels, issue.Labels, true) {
		return false, nil
	}
	if !matchesAnyString(filter.Tags, issue.Labels, true) {
		return false, nil
	}
	if !matchesStringFilter(filter.IssueTypes, issue.IssueType, true) {
		return false, nil
	}
	if !matchesStringFilter(filter.StateTypes, issue.StateType, true) {
		return false, nil
	}
	if !matchesStringFilter(filter.StateNames, issue.StateName, true) {
		return false, nil
	}
	if len(filter.Priorities) > 0 && !matchesStringFilter(filter.Priorities, issue.PriorityName, true) && !matchesStringFilter(filter.Priorities, fmt.Sprintf("%d", issue.Priority), true) {
		return false, nil
	}
	if strings.TrimSpace(filter.TitleContains) != "" &&
		!strings.Contains(strings.ToLower(issue.Title), strings.ToLower(strings.TrimSpace(filter.TitleContains))) {
		return false, nil
	}
	return true, nil
}

func parseLinearTriggerFilter(raw json.RawMessage) (linearTriggerFilter, error) {
	filter := linearTriggerFilter{}
	if len(raw) == 0 {
		return filter, nil
	}
	if err := json.Unmarshal(raw, &filter); err != nil {
		return linearTriggerFilter{}, fmt.Errorf("decode linear trigger filter: %w", err)
	}
	return filter, nil
}

type linearRepositoryResolution struct {
	RepositoryID *uuid.UUID
	Source       string
}

func (s *LinearEventTriggerService) resolveRepository(ctx context.Context, trigger models.AutomationEventTrigger, automation models.Automation, orgID uuid.UUID) (linearRepositoryResolution, error) {
	if trigger.RepositoryID != nil {
		return linearRepositoryResolution{RepositoryID: trigger.RepositoryID, Source: "trigger"}, nil
	}
	if automation.RepositoryID != nil {
		return linearRepositoryResolution{RepositoryID: automation.RepositoryID, Source: "automation"}, nil
	}
	if s.defaultRepos != nil {
		repositoryID, err := s.defaultRepos.LoadDefaultWorkRepositoryID(ctx, orgID)
		if err != nil {
			return linearRepositoryResolution{}, fmt.Errorf("lookup shared default work repository: %w", err)
		}
		if repositoryID != nil {
			return linearRepositoryResolution{RepositoryID: repositoryID, Source: "org_default"}, nil
		}
	}
	return linearRepositoryResolution{Source: "repository_unmapped"}, nil
}

func withLinearEventSnapshot(raw json.RawMessage, trigger models.AutomationEventTrigger, req LinearIssueEventTriggerRequest, repository linearRepositoryResolution) (json.RawMessage, error) {
	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("decode config snapshot: %w", err)
		}
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	linear := linearEventMap(req)
	if repository.Source != "" && repository.RepositoryID != nil {
		linear["repository_id"] = repository.RepositoryID.String()
		linear["repository_source"] = repository.Source
	} else if trigger.RepositoryID != nil {
		linear["repository_id"] = trigger.RepositoryID.String()
	} else if repository.Source != "" {
		linear["repository_source"] = repository.Source
	}
	decoded["linear"] = linear
	out, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("encode config snapshot: %w", err)
	}
	return out, nil
}

func linearTriggerContext(trigger models.AutomationEventTrigger, req LinearIssueEventTriggerRequest, repository linearRepositoryResolution) (json.RawMessage, error) {
	context := linearEventMap(req)
	context["provider"] = string(models.AutomationEventProviderLinear)
	if repository.Source != "" && repository.RepositoryID != nil {
		context["repository_id"] = repository.RepositoryID.String()
		context["repository_source"] = repository.Source
	} else if trigger.RepositoryID != nil {
		context["repository_id"] = trigger.RepositoryID.String()
	} else if repository.Source != "" {
		context["repository_source"] = repository.Source
	}
	out, err := json.Marshal(context)
	if err != nil {
		return nil, fmt.Errorf("encode linear trigger context: %w", err)
	}
	return out, nil
}

func linearEventMap(req LinearIssueEventTriggerRequest) map[string]any {
	out := map[string]any{
		"event_type":        string(req.EventType),
		"provider_event_id": req.ProviderEventID,
		"issue_id":          req.Issue.ID,
		"identifier":        req.Issue.Identifier,
		"title":             req.Issue.Title,
		"priority_number":   req.Issue.Priority,
		"labels":            req.Issue.Labels,
	}
	addString(out, "url", req.Issue.URL)
	addString(out, "team_id", req.Issue.TeamID)
	addString(out, "team_key", req.Issue.TeamKey)
	addString(out, "team_name", req.Issue.TeamName)
	addString(out, "state_name", req.Issue.StateName)
	addString(out, "state_type", req.Issue.StateType)
	addString(out, "priority", req.Issue.PriorityName)
	addString(out, "issue_type", req.Issue.IssueType)
	if req.OccurredAt != nil {
		out["occurred_at"] = req.OccurredAt.Format(time.RFC3339)
	}
	return out
}

func linearEventGoalSnapshot(goal string, req LinearIssueEventTriggerRequest) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(goal))
	b.WriteString("\n\nLinear issue:\n")
	writeGoalLine(&b, "Identifier", req.Issue.Identifier)
	writeGoalLine(&b, "ID", req.Issue.ID)
	writeGoalLine(&b, "Event", string(req.EventType))
	writeGoalLine(&b, "Title", req.Issue.Title)
	writeGoalLine(&b, "URL", req.Issue.URL)
	writeGoalLine(&b, "Team", req.Issue.TeamKey)
	writeGoalLine(&b, "State", req.Issue.StateName)
	writeGoalLine(&b, "State type", req.Issue.StateType)
	writeGoalLine(&b, "Priority", req.Issue.PriorityName)
	writeGoalLine(&b, "Issue type", req.Issue.IssueType)
	if len(req.Issue.Labels) > 0 {
		writeGoalLine(&b, "Labels", strings.Join(req.Issue.Labels, ", "))
	}
	if strings.TrimSpace(req.Issue.Description) != "" {
		b.WriteString("Description:\n")
		b.WriteString(strings.TrimSpace(req.Issue.Description))
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func addString(target map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		target[key] = value
	}
}
