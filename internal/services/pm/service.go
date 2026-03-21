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

type sessionStore interface {
	CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error)
	Create(ctx context.Context, run *models.Session) error
	ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.SessionFilters) ([]models.Session, error)
	ListRecentByOrg(ctx context.Context, orgID uuid.UUID, statuses []string, limit int) ([]models.Session, error)
}

type prStore interface {
	ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.PullRequestFilters) ([]models.PullRequest, error)
}

type orgStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

type repoStore interface {
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Repository, error)
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
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

type projectStore interface {
	ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.ProjectFilters) ([]models.Project, error)
	GetByID(ctx context.Context, orgID, projectID uuid.UUID) (models.Project, error)
	Update(ctx context.Context, p *models.Project) error
	UpdateProgress(ctx context.Context, orgID, projectID uuid.UUID) error
	UpdateStatus(ctx context.Context, orgID, projectID uuid.UUID, status string) error
}

type projectTaskStore interface {
	Create(ctx context.Context, t *models.ProjectTask) error
	GetByID(ctx context.Context, orgID, taskID uuid.UUID) (models.ProjectTask, error)
	ListByProject(ctx context.Context, orgID, projectID uuid.UUID, filters db.ProjectTaskFilters) ([]models.ProjectTask, error)
	Update(ctx context.Context, t *models.ProjectTask) error
	CountByProjectAndStatus(ctx context.Context, orgID, projectID uuid.UUID, status string) (int, error)
	GetMaxBatchNumber(ctx context.Context, orgID, projectID uuid.UUID) (int, error)
}

type projectCycleStore interface {
	Create(ctx context.Context, c *models.ProjectCycle) error
	ListByProject(ctx context.Context, orgID, projectID uuid.UUID, limit int) ([]models.ProjectCycle, error)
}

type pmDocumentStore interface {
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.PMDocument, error)
}

type integrationStore interface {
	ListByOrgAndProvider(ctx context.Context, orgID uuid.UUID, provider string) ([]models.Integration, error)
}

type credentialStore interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

// Service is the AI Product Manager. It runs the PM agent and delegates work.
type Service struct {
	issues        issueStore
	sessions     sessionStore
	pullRequests  prStore
	orgs          orgStore
	repos         repoStore
	jobs          jobStore
	plans         planStore
	decisionLog   decisionLogStore
	projects      projectStore      // nil-safe: projects feature disabled if nil
	projectTasks  projectTaskStore  // nil-safe
	projectCycles projectCycleStore // nil-safe
	pmDocuments   pmDocumentStore   // nil-safe
	integrations  integrationStore  // nil-safe: Slack context disabled if nil
	credentials   credentialStore   // nil-safe
	sandbox       agent.SandboxProvider
	adapter       agent.AgentAdapter
	github        agent.GitHubTokenProvider
	logger        zerolog.Logger
}

func NewService(
	issues issueStore,
	sessions sessionStore,
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
		sessions:    sessions,
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

// SetProjectStores injects project store dependencies. Nil-safe: if not called, projects feature is disabled.
func (s *Service) SetProjectStores(projects projectStore, tasks projectTaskStore, cycles projectCycleStore) {
	s.projects = projects
	s.projectTasks = tasks
	s.projectCycles = cycles
}

// SetPMDocumentStore injects the PM document store. Nil-safe: if not called, PM documents are not included in context.
func (s *Service) SetPMDocumentStore(store pmDocumentStore) {
	s.pmDocuments = store
}

// SetSlackStores injects integration and credential stores for Slack context.
func (s *Service) SetSlackStores(integrations integrationStore, credentials credentialStore) {
	s.integrations = integrations
	s.credentials = credentials
}


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

	if len(ctxBundle.pmDocuments) > 0 {
		if err := s.writePMDocumentsToWorkspace(ctx, sb, ctxBundle.pmDocuments); err != nil {
			s.logger.Warn().Err(err).Msg("failed to write PM documents to workspace")
		}
	}

	contextJSON, err := json.Marshal(ctxBundle.pmContext)
	if err != nil {
		return nil, fmt.Errorf("marshal pm context: %w", err)
	}
	if err := s.sandbox.WriteFile(ctx, sb, "/workspace/.pm-context.json", contextJSON); err != nil {
		return nil, fmt.Errorf("write context: %w", err)
	}

	// Write full Slack thread files to the sandbox for PM drill-down.
	if ctxBundle.slackThreads != nil {
		if err := s.writeSlackThreadFiles(ctx, sb, ctxBundle.slackThreads); err != nil {
			s.logger.Warn().Err(err).Msg("failed to write slack thread files to sandbox")
		}
	}

	available := ctxBundle.pmContext.MaxConcurrentRuns - ctxBundle.pmContext.CurrentRunCount
	if available < 0 {
		available = 0
	}

	prompt := &agent.AgentPrompt{
		SystemPrompt: buildPMSystemPrompt(available, ctxBundle.pmContext.MaxConcurrentRuns, len(ctxBundle.pmContext.ActiveProjects)),
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
		tokenJSON, err := json.Marshal(result.TokenUsage)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to marshal token usage")
			tokenJSON = nil
		}
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

	// Execute project plans if projects feature is enabled.
	if s.projects != nil && s.projectTasks != nil && s.projectCycles != nil {
		for _, pp := range plan.ProjectPlans {
			if err := s.executeProjectPlan(ctx, orgID, &pp, ctxBundle.settings, plan.ID); err != nil {
				s.logger.Error().Err(err).Str("project_id", pp.ProjectID.String()).Msg("failed to execute project plan")
			}
		}
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
		var err error
		productSnapshot, err = json.Marshal(productContext)
		if err != nil {
			return nil, fmt.Errorf("marshal product context: %w", err)
		}
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
