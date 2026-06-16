package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

type githubAutomationStore interface {
	ListEnabledByGitHubEvent(ctx context.Context, orgID, repositoryID uuid.UUID, event models.AutomationGitHubEvent) ([]models.Automation, error)
}

type githubAutomationRunStore interface {
	CreateRun(ctx context.Context, run *models.AutomationRun) (bool, error)
}

type githubAutomationJobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	Notify(ctx context.Context, jobID uuid.UUID)
}

type GitHubEventTriggerRequest struct {
	OrgID             uuid.UUID
	RepositoryID      uuid.UUID
	Event             models.AutomationGitHubEvent
	Repository        string
	PullRequestNumber int
	PullRequestURL    string
	Actor             string
	Body              string
}

type GitHubEventTriggerService struct {
	automations githubAutomationStore
	runs        githubAutomationRunStore
	jobs        githubAutomationJobStore
	logger      zerolog.Logger
}

func NewGitHubEventTriggerService(automations githubAutomationStore, runs githubAutomationRunStore, jobs githubAutomationJobStore, logger zerolog.Logger) *GitHubEventTriggerService {
	return &GitHubEventTriggerService{
		automations: automations,
		runs:        runs,
		jobs:        jobs,
		logger:      logger,
	}
}

func (s *GitHubEventTriggerService) TriggerGitHubEvent(ctx context.Context, req GitHubEventTriggerRequest) error {
	if s == nil || s.automations == nil || s.runs == nil || s.jobs == nil {
		return nil
	}
	if err := req.Event.Validate(); err != nil {
		return err
	}
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
	configSnapshot, err := automation.BuildConfigSnapshot()
	if err != nil {
		return fmt.Errorf("build config snapshot: %w", err)
	}
	configSnapshot, err = withGitHubEventSnapshot(configSnapshot, req)
	if err != nil {
		return err
	}

	run := models.AutomationRun{
		AutomationID:   automation.ID,
		OrgID:          automation.OrgID,
		TriggeredBy:    models.AutomationTriggeredByGitHub,
		GoalSnapshot:   githubEventGoalSnapshot(automation.Goal, req),
		ConfigSnapshot: configSnapshot,
		Status:         models.AutomationRunStatusPending,
	}
	created, err := s.runs.CreateRun(ctx, &run)
	if err != nil {
		return fmt.Errorf("create github-triggered automation run: %w", err)
	}
	if !created {
		return nil
	}
	dedupeKey := fmt.Sprintf("automation_run:%s", run.ID.String())
	payload := map[string]string{
		"org_id":            automation.OrgID.String(),
		"automation_id":     automation.ID.String(),
		"automation_run_id": run.ID.String(),
	}
	jobID, err := s.jobs.Enqueue(ctx, automation.OrgID, "default", models.JobTypeAutomationRun, payload, 5, &dedupeKey)
	if err != nil {
		return fmt.Errorf("enqueue github-triggered automation run: %w", err)
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
	decoded["github"] = map[string]any{
		"repository":          req.Repository,
		"pull_request_number": req.PullRequestNumber,
		"pull_request_url":    req.PullRequestURL,
		"actor":               req.Actor,
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
	b.WriteString("- Event: ")
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
	if req.Actor != "" {
		b.WriteString("\n- Actor: ")
		b.WriteString(req.Actor)
	}
	if strings.TrimSpace(req.Body) != "" {
		b.WriteString("\n\nEvent text:\n")
		b.WriteString(strings.TrimSpace(req.Body))
	}
	return b.String()
}
