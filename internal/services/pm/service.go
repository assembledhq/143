package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/adapters"
)

type issueStore interface {
	ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.IssueFilters) ([]models.Issue, error)
	UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status string) error
}

type agentRunStore interface {
	CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error)
	Create(ctx context.Context, run *models.AgentRun) error
	ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.AgentRunFilters) ([]models.AgentRun, error)
	ListRecentByOrg(ctx context.Context, orgID uuid.UUID, statuses []string, limit int) ([]models.AgentRun, error)
}

type prStore interface {
	ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.PullRequestFilters) ([]models.PullRequest, error)
}

type orgStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

type repoStore interface {
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Repository, error)
}

type jobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

type planStore interface {
	Create(ctx context.Context, plan *models.PMPlan) error
	Update(ctx context.Context, plan *models.PMPlan) error
}

type decisionLogStore interface {
	Create(ctx context.Context, entry *models.PMDecisionLogEntry) error
	ListRecentByOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]models.PMDecisionLogEntry, error)
	UpdateOutcome(ctx context.Context, orgID, planID, issueID uuid.UUID, outcome models.PMDecisionOutcome) error
}

// Service is the AI Product Manager. It runs the PM agent and delegates work.
type Service struct {
	issues       issueStore
	agentRuns    agentRunStore
	pullRequests prStore
	orgs         orgStore
	repos        repoStore
	jobs         jobStore
	plans        planStore
	decisionLog  decisionLogStore
	sandbox      agent.SandboxProvider
	adapter      agent.AgentAdapter
	github       agent.GitHubTokenProvider
	logger       zerolog.Logger
}

func NewService(
	issues issueStore,
	agentRuns agentRunStore,
	pullRequests prStore,
	orgs orgStore,
	repos repoStore,
	jobs jobStore,
	plans planStore,
	decisionLog decisionLogStore,
	sandbox agent.SandboxProvider,
	adapter agent.AgentAdapter,
	github agent.GitHubTokenProvider,
	logger zerolog.Logger,
) *Service {
	return &Service{
		issues:       issues,
		agentRuns:    agentRuns,
		pullRequests: pullRequests,
		orgs:         orgs,
		repos:        repos,
		jobs:         jobs,
		plans:        plans,
		decisionLog:  decisionLog,
		sandbox:      sandbox,
		adapter:      adapter,
		github:       github,
		logger:       logger,
	}
}

// Analyze runs PM analysis for an org. When repoID is non-nil, analysis is
// scoped to that repository and repo-level PM settings are applied.
func (s *Service) Analyze(ctx context.Context, orgID uuid.UUID, trigger models.PMTrigger, repoID *uuid.UUID) (*Plan, error) {
	if s.adapter == nil || s.sandbox == nil {
		return nil, fmt.Errorf("pm adapter or sandbox not configured")
	}
	if err := trigger.Validate(); err != nil {
		return nil, fmt.Errorf("invalid trigger: %w", err)
	}

	repos, err := s.repos.ListByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("no repositories configured for org")
	}

	// Select the target repository.
	var repo models.Repository
	if repoID != nil {
		found := false
		for _, candidate := range repos {
			if candidate.ID == *repoID {
				repo = candidate
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("repository %s not found in org", repoID)
		}
	} else {
		repo = repos[0]
		for _, candidate := range repos {
			if candidate.Status == "active" {
				repo = candidate
				break
			}
		}
	}

	ctxBundle, err := s.gatherContext(ctx, orgID, &repo)
	if err != nil {
		return nil, fmt.Errorf("gather context: %w", err)
	}

	sbCfg := pmSandboxConfig()
	sb, err := s.sandbox.Create(ctx, sbCfg)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	defer func() {
		if destroyErr := s.sandbox.Destroy(ctx, sb); destroyErr != nil {
			s.logger.Warn().Err(destroyErr).Msg("failed to destroy PM sandbox")
		}
	}()

	var token string
	if s.github != nil {
		ghToken, err := s.github.GetInstallationToken(ctx, repo.InstallationID)
		if err != nil {
			return nil, fmt.Errorf("get installation token: %w", err)
		}
		token = ghToken
	}
	if err := s.sandbox.CloneRepo(ctx, sb, repo.CloneURL, repo.DefaultBranch, token); err != nil {
		return nil, fmt.Errorf("clone repo: %w", err)
	}

	if ctxBundle.productContext != nil {
		if err := s.writeProductContextToAgentsMD(ctx, sb, ctxBundle.productContext); err != nil {
			s.logger.Warn().Err(err).Msg("failed to write product context to AGENTS.md")
		}
	}

	contextJSON, err := json.Marshal(ctxBundle.pmContext)
	if err != nil {
		return nil, fmt.Errorf("marshal pm context: %w", err)
	}
	if err := s.sandbox.WriteFile(ctx, sb, "/workspace/.pm-context.json", contextJSON); err != nil {
		return nil, fmt.Errorf("write context: %w", err)
	}

	available := ctxBundle.pmContext.MaxConcurrentRuns - ctxBundle.pmContext.CurrentRunCount
	if available < 0 {
		available = 0
	}

	prompt := &agent.AgentPrompt{
		SystemPrompt: buildPMSystemPrompt(available, ctxBundle.pmContext.MaxConcurrentRuns),
		UserPrompt:   string(contextJSON),
		MaxTokens:    pmMaxTokens,
	}

	logCh := make(chan agent.LogEntry, 100)
	go func() {
		for range logCh {
		}
	}()
	defer close(logCh)

	execCtx := adapters.WithSandboxProvider(ctx, s.sandbox)
	result, err := s.adapter.Execute(execCtx, sb, prompt, logCh)
	if err != nil {
		return nil, fmt.Errorf("pm agent execution: %w", err)
	}

	plan, err := parsePlan(result.Summary)
	if err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}

	plan.OrgID = orgID
	plan.Status = models.PMPlanStatusExecuting
	plan.TriggeredBy = trigger
	plan.IssuesReviewed = len(ctxBundle.pmContext.OpenIssues)
	if result.TokenUsage != (agent.TokenUsage{}) {
		tokenJSON, _ := json.Marshal(result.TokenUsage)
		plan.TokenUsage = tokenJSON
	}

	planModel, err := planToModel(plan, ctxBundle.productContext)
	if err != nil {
		return nil, fmt.Errorf("serialize plan: %w", err)
	}
	if err := s.plans.Create(ctx, planModel); err != nil {
		return nil, fmt.Errorf("persist plan: %w", err)
	}
	plan.ID = planModel.ID
	plan.CreatedAt = planModel.CreatedAt

	if s.decisionLog != nil {
		entries := planToDecisionLog(plan)
		for _, entry := range entries {
			entry.OrgID = orgID
			if err := s.decisionLog.Create(ctx, &entry); err != nil {
				s.logger.Error().Err(err).Msg("failed to write decision log entry")
			}
		}
	}

	if err := s.executePlan(ctx, orgID, plan, ctxBundle.settings, ctxBundle.productContext); err != nil {
		return nil, fmt.Errorf("execute plan: %w", err)
	}

	plan.Status = models.PMPlanStatusCompleted
	now := time.Now()
	plan.CompletedAt = &now

	updatedModel, err := planToModel(plan, ctxBundle.productContext)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to serialize completed plan")
		return plan, nil
	}
	if err := s.plans.Update(ctx, updatedModel); err != nil {
		s.logger.Error().Err(err).Msg("failed to persist completed plan status")
	}

	return plan, nil
}

func pmSandboxConfig() agent.SandboxConfig {
	cfg := agent.DefaultSandboxConfig()
	cfg.Timeout = 10 * time.Minute
	cfg.CPULimit = 1
	cfg.MemoryLimitMB = 2048
	cfg.NetworkPolicy = "restricted"
	return cfg
}

func planToModel(plan *Plan, productContext *models.ProductContext) (*models.PMPlan, error) {
	tasksJSON, err := json.Marshal(plan.Tasks)
	if err != nil {
		return nil, fmt.Errorf("marshal tasks: %w", err)
	}
	clustersJSON, err := json.Marshal(plan.Clusters)
	if err != nil {
		return nil, fmt.Errorf("marshal clusters: %w", err)
	}
	skipsJSON, err := json.Marshal(plan.SkippedIssues)
	if err != nil {
		return nil, fmt.Errorf("marshal skips: %w", err)
	}

	var productSnapshot json.RawMessage
	if productContext != nil {
		productSnapshot, _ = json.Marshal(productContext)
	}

	return &models.PMPlan{
		ID:                     plan.ID,
		OrgID:                  plan.OrgID,
		Status:                 plan.Status,
		Analysis:               plan.Analysis,
		Tasks:                  tasksJSON,
		Clusters:               clustersJSON,
		SkippedIssues:          skipsJSON,
		IssuesReviewed:         plan.IssuesReviewed,
		ProductContextSnapshot: productSnapshot,
		TokenUsage:             plan.TokenUsage,
		TriggeredBy:            plan.TriggeredBy,
		CreatedAt:              plan.CreatedAt,
		CompletedAt:            plan.CompletedAt,
	}, nil
}

func tokenModeFromComplexity(complexity models.PMTaskComplexity) string {
	switch complexity {
	case models.PMTaskComplexityModerate, models.PMTaskComplexityComplex:
		return "high"
	default:
		return "low"
	}
}
