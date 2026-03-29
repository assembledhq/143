package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/adapters"
)

type issueStore interface {
	GetByID(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error)
	ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.IssueFilters) ([]models.Issue, error)
	UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status string) error
}

type sessionStore interface {
	CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error)
	Create(ctx context.Context, run *models.Session) error
	UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error
	UpdatePMPlanID(ctx context.Context, orgID, runID, planID uuid.UUID) error
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
	ListByOrgExcludeSourceType(ctx context.Context, orgID uuid.UUID, excludeSourceType string, limit int) ([]models.PMDocument, error)
	GetByOrgAndSourceType(ctx context.Context, orgID uuid.UUID, sourceType string) (models.PMDocument, error)
	ListByOrgAndSourceType(ctx context.Context, orgID uuid.UUID, sourceType string) ([]models.PMDocument, error)
	Create(ctx context.Context, doc *models.PMDocument) error
	Update(ctx context.Context, doc *models.PMDocument) error
	Delete(ctx context.Context, orgID, docID uuid.UUID) error
	DeleteByOrgAndSourceType(ctx context.Context, orgID uuid.UUID, sourceType string) error
	GetByID(ctx context.Context, orgID, docID uuid.UUID) (models.PMDocument, error)
	Begin(ctx context.Context) (pgx.Tx, error)
	WithTx(tx pgx.Tx) *db.PMDocumentStore
}

type integrationStore interface {
	ListByOrgAndProvider(ctx context.Context, orgID uuid.UUID, provider string) ([]models.Integration, error)
}

type credentialStore interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

type sessionLogStore interface {
	Create(ctx context.Context, log *models.SessionLog) error
}

// SkillsBuilder generates integration skills docs for agents running in sandboxes.
// This is satisfied by the Orchestrator which already builds skills docs from
// org integration credentials.
type SkillsBuilder interface {
	BuildIntegrationSkills(ctx context.Context, orgID uuid.UUID) string
}

// Service is the AI Product Manager. It runs the PM agent and delegates work.
type Service struct {
	issues            issueStore
	sessions         sessionStore
	sessionLogs       sessionLogStore   // nil-safe: if nil, PM session logs are not persisted
	pullRequests      prStore
	orgs              orgStore
	repos             repoStore
	jobs              jobStore
	plans             planStore
	decisionLog       decisionLogStore
	projects          projectStore      // nil-safe: projects feature disabled if nil
	projectTasks      projectTaskStore  // nil-safe
	projectCycles     projectCycleStore // nil-safe
	pmDocuments       pmDocumentStore   // nil-safe
	integrations      integrationStore  // nil-safe: Slack context disabled if nil
	credentials       credentialStore   // nil-safe
	skills            SkillsBuilder     // nil-safe: bootstrap disabled if nil
	sandbox           agent.SandboxProvider
	adapter           agent.AgentAdapter
	github            agent.GitHubTokenProvider
	internalAPIURL    string // base URL for internal API (e.g. "http://server:8080/api/v1/internal")
	internalAPISecret string // signing secret for internal API tokens
	logger            zerolog.Logger
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

// SetSessionLogStore injects the session log store for PM agent log persistence.
func (s *Service) SetSessionLogStore(store sessionLogStore) {
	s.sessionLogs = store
}

// SetInternalAPI configures the internal API URL and signing secret for sandbox-to-server
// communication (e.g. issue creation). If not set, the PM agent cannot create issues.
func (s *Service) SetInternalAPI(url, secret string) {
	s.internalAPIURL = url
	s.internalAPISecret = secret
}

// SetSkillsBuilder injects the skills builder for bootstrap/refresh agent prompts.
func (s *Service) SetSkillsBuilder(sb SkillsBuilder) {
	s.skills = sb
}

// selectRepo picks a target repository for an org. If repoID is provided, that
// specific repo is selected; otherwise it defaults to the first active repo.
// Note: for multi-repo orgs, this only returns one repo. Bootstrap/refresh will
// only cover that single repo's codebase.
func (s *Service) selectRepo(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID) (models.Repository, error) {
	repos, err := s.repos.ListByOrg(ctx, orgID)
	if err != nil {
		return models.Repository{}, fmt.Errorf("list repositories: %w", err)
	}
	if len(repos) == 0 {
		return models.Repository{}, fmt.Errorf("no repositories configured for org")
	}

	if repoID != nil {
		for _, candidate := range repos {
			if candidate.ID == *repoID {
				return candidate, nil
			}
		}
		return models.Repository{}, fmt.Errorf("repository %s not found in org", repoID)
	}

	repo := repos[0]
	for _, candidate := range repos {
		if candidate.Status == "active" {
			repo = candidate
			break
		}
	}
	return repo, nil
}

func (s *Service) Analyze(ctx context.Context, orgID uuid.UUID, trigger models.PMTrigger, repoID *uuid.UUID) (*Plan, error) {
	if s.adapter == nil || s.sandbox == nil {
		return nil, fmt.Errorf("pm adapter or sandbox not configured")
	}
	if err := trigger.Validate(); err != nil {
		return nil, fmt.Errorf("invalid trigger: %w", err)
	}

	repo, err := s.selectRepo(ctx, orgID, repoID)
	if err != nil {
		return nil, err
	}

	// Create a PM session record to anchor logs.
	pmSession := &models.Session{
		OrgID:         orgID,
		AgentType:     models.AgentTypePMAgent,
		Status:        "running",
		Title:         strPtr("PM Analysis"),
		RepositoryID:  &repo.ID,
		AutonomyLevel: "full",
		TokenMode:     "high",
	}
	if err := s.sessions.Create(ctx, pmSession); err != nil {
		s.logger.Error().Err(err).Msg("failed to create PM session — continuing without session logging")
		pmSession = nil
	}

	// Helper to mark PM session as failed on early return.
	failSession := func() {
		if pmSession != nil {
			if err := s.sessions.UpdateStatus(ctx, orgID, pmSession.ID, "failed"); err != nil {
				s.logger.Error().Err(err).Msg("failed to mark PM session as failed")
			}
		}
	}

	ctxBundle, err := s.gatherContext(ctx, orgID, &repo)
	if err != nil {
		failSession()
		return nil, fmt.Errorf("gather context: %w", err)
	}

	sbCfg := pmSandboxConfig()

	// Inject internal API credentials so the PM agent can create issues.
	if s.internalAPIURL != "" && s.internalAPISecret != "" {
		// Token TTL extends past sandbox timeout to avoid mid-execution expiry.
		tokenTTL := sbCfg.Timeout + 5*time.Minute
		internalToken, tokenErr := auth.GenerateInternalToken(s.internalAPISecret, orgID, tokenTTL)
		if tokenErr != nil {
			s.logger.Warn().Err(tokenErr).Msg("failed to generate internal API token — issue creation will be unavailable")
		} else {
			if sbCfg.Env == nil {
				sbCfg.Env = make(map[string]string)
			}
			sbCfg.Env["INTERNAL_API_TOKEN"] = internalToken
			sbCfg.Env["INTERNAL_API_URL"] = s.internalAPIURL
		}
	}

	sb, err := s.sandbox.Create(ctx, sbCfg)
	if err != nil {
		failSession()
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
			failSession()
			return nil, fmt.Errorf("get installation token: %w", err)
		}
		token = ghToken
	}
	if err := s.sandbox.CloneRepo(ctx, sb, repo.CloneURL, repo.DefaultBranch, token); err != nil {
		failSession()
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
		failSession()
		return nil, fmt.Errorf("marshal pm context: %w", err)
	}
	if err := s.sandbox.WriteFile(ctx, sb, "/workspace/.pm-context.json", contextJSON); err != nil {
		failSession()
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

	pmTokenLimit := ctxBundle.settings.ContextLimits.PMMaxTokens
	if pmTokenLimit <= 0 {
		pmTokenLimit = defaultPMMaxTokens
	}

	prompt := &agent.AgentPrompt{
		SystemPrompt: buildPMSystemPrompt(available, ctxBundle.pmContext.MaxConcurrentRuns, len(ctxBundle.pmContext.ActiveProjects)),
		UserPrompt:   string(contextJSON),
		MaxTokens:    pmTokenLimit,
	}

	// Stream PM agent logs into session_logs (same pattern as orchestrator.streamLogs).
	logCh := make(chan agent.LogEntry, 100)
	var logWg sync.WaitGroup
	logWg.Add(1)
	go func() {
		defer logWg.Done()
		s.streamPMLogs(ctx, pmSession, logCh)
	}()

	execCtx := adapters.WithSandboxProvider(ctx, s.sandbox)
	result, err := s.adapter.Execute(execCtx, sb, prompt, logCh)
	close(logCh)
	logWg.Wait()
	if err != nil {
		failSession()
		return nil, fmt.Errorf("pm agent execution: %w", err)
	}

	plan, err := parsePlan(result.Summary)
	if err != nil {
		failSession()
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
		failSession()
		return nil, fmt.Errorf("serialize plan: %w", err)
	}
	if err := s.plans.Create(ctx, planModel); err != nil {
		failSession()
		return nil, fmt.Errorf("persist plan: %w", err)
	}
	plan.ID = planModel.ID
	plan.CreatedAt = planModel.CreatedAt

	// Link PM session to the plan.
	if pmSession != nil {
		if err := s.sessions.UpdatePMPlanID(ctx, orgID, pmSession.ID, planModel.ID); err != nil {
			s.logger.Error().Err(err).Msg("failed to link PM session to plan")
		}
	}

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
		failSession()
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

	// Mark PM session as completed.
	if pmSession != nil {
		if err := s.sessions.UpdateStatus(ctx, orgID, pmSession.ID, "completed"); err != nil {
			s.logger.Error().Err(err).Msg("failed to mark PM session as completed")
		}
	}

	return plan, nil
}

// streamPMLogs persists PM agent log entries to session_logs. If the session log
// store is not configured or the PM session is nil, entries are drained silently.
func (s *Service) streamPMLogs(ctx context.Context, pmSession *models.Session, logCh <-chan agent.LogEntry) {
	for entry := range logCh {
		if s.sessionLogs == nil || pmSession == nil {
			continue
		}
		metadata, err := json.Marshal(entry.Metadata)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to marshal PM log entry metadata")
			metadata = nil
		}
		log := &models.SessionLog{
			SessionID: pmSession.ID,
			Level:     entry.Level,
			Message:   entry.Message,
			Metadata:  metadata,
		}
		if err := s.sessionLogs.Create(ctx, log); err != nil {
			s.logger.Error().Err(err).Msg("failed to persist PM log entry")
		}
	}
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

func strPtr(s string) *string { return &s }
