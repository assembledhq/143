package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/adapters"
)

// AnalyzeProject runs a focused, project-scoped PM analysis for a single
// scheduled project. It gathers project-specific context, runs the PM agent
// with a project-focused prompt, and calls executeProjectPlan to create tasks
// and dispatch runs. This is the entry point for the project_cycle job.
func (s *Service) AnalyzeProject(ctx context.Context, orgID, projectID uuid.UUID) error {
	if s.sandbox == nil || s.env == nil {
		return fmt.Errorf("pm sandbox or env helper not configured")
	}
	if s.projects == nil || s.projectTasks == nil || s.projectCycles == nil {
		return fmt.Errorf("project stores not configured")
	}

	project, err := s.projects.GetByID(ctx, orgID, projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	if project.Status != models.ProjectStatusActive {
		s.logger.Info().Str("project_id", projectID.String()).Str("status", string(project.Status)).Msg("skipping project_cycle: project not active")
		return nil
	}

	org, err := s.orgs.GetByID(ctx, orgID)
	if err != nil {
		return fmt.Errorf("get org: %w", err)
	}
	settings, parseErr := models.ParseOrgSettings(org.Settings)
	if parseErr != nil {
		return fmt.Errorf("parse org settings: %w", parseErr)
	}

	// Fetch the repository for this project.
	if project.RepositoryID == nil {
		return fmt.Errorf("project %s has no repository_id", projectID)
	}
	repo, err := s.repos.GetByID(ctx, orgID, *project.RepositoryID)
	if err != nil {
		return fmt.Errorf("get repository %s: %w", *project.RepositoryID, err)
	}

	// Build project-specific context.
	projectSummary, err := s.buildProjectSummary(ctx, orgID, &project)
	if err != nil {
		return fmt.Errorf("build project summary: %w", err)
	}

	projectCtx := &ProjectCycleContext{
		Project: projectSummary,
	}
	contextJSON, err := json.Marshal(projectCtx)
	if err != nil {
		return fmt.Errorf("marshal project context: %w", err)
	}

	agentType := resolveAgentType(settings, nil)
	adapter, err := s.pickAdapter(agentType)
	if err != nil {
		return fmt.Errorf("pick adapter: %w", err)
	}

	// Create sandbox and clone repo.
	sbCfg := pmSandboxConfig()
	sbCfg.Env = s.env.Resolve(ctx, orgID, agentType, nil)
	if sbCfg.Env == nil {
		sbCfg.Env = make(map[string]string)
	}
	if err := s.finalizeSandboxEnv(agentType, sbCfg.Env); err != nil {
		return fmt.Errorf("agent auth preflight: %w", err)
	}
	sb, err := s.sandbox.Create(ctx, sbCfg)
	if err != nil {
		return fmt.Errorf("create sandbox: %w", err)
	}
	exitReason := "completed"
	containerStartedAt := time.Now()
	var usageEventID uuid.UUID
	if s.usageTracker != nil {
		usageEventID = s.usageTracker.ContainerStarted(ctx, orgID, uuid.Nil, sb, sbCfg, containerStartedAt)
	}
	defer func() {
		if s.usageTracker != nil {
			s.usageTracker.ContainerStopped(ctx, orgID, uuid.Nil, usageEventID, sb.ID, containerStartedAt, exitReason)
		}
		if destroyErr := s.sandbox.Destroy(ctx, sb); destroyErr != nil {
			s.logger.Warn().Err(destroyErr).Msg("failed to destroy project PM sandbox")
		}
	}()

	if err := s.injectRequiredAgentAuth(ctx, orgID, agentType, sb, sbCfg.Env); err != nil {
		exitReason = containerExitReason(ctx, err)
		return fmt.Errorf("inject codex auth: %w", err)
	}

	var token string
	if s.github != nil {
		ghToken, err := s.github.GetInstallationToken(ctx, repo.InstallationID)
		if err != nil {
			exitReason = containerExitReason(ctx, err)
			return fmt.Errorf("get installation token: %w", err)
		}
		token = ghToken
	}
	if err := s.sandbox.CloneRepo(ctx, sb, repo.CloneURL, repo.DefaultBranch, token); err != nil {
		exitReason = containerExitReason(ctx, err)
		return fmt.Errorf("clone repo: %w", err)
	}

	if err := s.sandbox.WriteFile(ctx, sb, "/workspace/.pm-project-context.json", contextJSON); err != nil {
		exitReason = containerExitReason(ctx, err)
		return fmt.Errorf("write project context: %w", err)
	}

	pmTokenLimit := settings.ContextLimits.PMMaxTokens
	if pmTokenLimit <= 0 {
		pmTokenLimit = defaultPMMaxTokens
	}

	prompt := &agent.AgentPrompt{
		SystemPrompt: buildProjectCycleSystemPrompt(&projectSummary),
		UserPrompt:   string(contextJSON),
		MaxTokens:    pmTokenLimit,
	}

	logCh := make(chan agent.LogEntry, 100)
	go func() {
		for range logCh {
		}
	}()
	defer close(logCh)

	execCtx := adapters.WithSandboxProvider(ctx, s.sandbox)
	result, err := adapter.Execute(execCtx, sb, prompt, logCh)
	if err != nil {
		exitReason = containerExitReason(ctx, err)
		return fmt.Errorf("project pm agent execution: %w", err)
	}

	// Parse the result as a ProjectPlan.
	pp, err := parseProjectPlan(result.Summary, projectID)
	if err != nil {
		exitReason = containerExitReason(ctx, err)
		return fmt.Errorf("parse project plan: %w", err)
	}

	// Create a lightweight PM plan record to link the cycle.
	plan := &models.PMPlan{
		OrgID:         orgID,
		Status:        models.PMPlanStatusCompleted,
		Analysis:      pp.CycleAnalysis,
		Tasks:         []byte("[]"),
		Clusters:      []byte("[]"),
		SkippedIssues: []byte("[]"),
		TriggeredBy:   models.PMTriggerCron,
	}
	if result.TokenUsage != (agent.TokenUsage{}) {
		tokenJSON, err := json.Marshal(result.TokenUsage)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to marshal token usage")
			tokenJSON = nil
		}
		plan.TokenUsage = tokenJSON
	}
	now := time.Now()
	plan.CompletedAt = &now
	if err := s.plans.Create(ctx, plan); err != nil {
		exitReason = containerExitReason(ctx, err)
		return fmt.Errorf("persist project cycle plan: %w", err)
	}

	// Execute the project plan using existing infrastructure.
	if err := s.executeProjectPlan(ctx, orgID, pp, settings, plan.ID); err != nil {
		exitReason = containerExitReason(ctx, err)
		return fmt.Errorf("execute project plan: %w", err)
	}

	return nil
}

// ProjectCycleContext is the focused context provided to the PM agent
// when running a project-scoped cycle.
type ProjectCycleContext struct {
	Project ProjectSummary `json:"project"`
}

// buildProjectCycleSystemPrompt creates a system prompt focused on a single project.
func buildProjectCycleSystemPrompt(project *ProjectSummary) string {
	return prompts.ProjectCycleSystemPrompt(prompts.ProjectCycleSystemPromptData{
		Title: project.Title,
		Goal:  project.Goal,
		ID:    project.ID,
	})
}

// parseProjectPlan parses the PM agent's output into a ProjectPlan.
func parseProjectPlan(summary string, projectID uuid.UUID) (*ProjectPlan, error) {
	var pp ProjectPlan
	if err := json.Unmarshal([]byte(summary), &pp); err != nil {
		return nil, fmt.Errorf("unmarshal project plan: %w", err)
	}
	pp.ProjectID = projectID
	return &pp, nil
}
