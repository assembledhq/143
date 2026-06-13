package pm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
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

// resolveAgentType returns the agent type the PM should run under.
// Priority: explicit override (per-run) → settings.DefaultAgentType → platform default.
func resolveAgentType(settings models.OrgSettings, override *models.AgentType) models.AgentType {
	if override != nil && *override != "" {
		return *override
	}
	if settings.DefaultAgentType != "" {
		return settings.DefaultAgentType
	}
	return models.DefaultDefaultAgentType
}

type issueStore interface {
	GetByID(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error)
	ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.IssueFilters) ([]models.Issue, error)
	UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status models.IssueStatus) error
}

type sessionStore interface {
	CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error)
	Create(ctx context.Context, run *models.Session) error
	UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status models.SessionStatus, result *models.SessionResult) error
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
	ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.RepositoryFilters) ([]models.Repository, error)
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
	UpdateStatus(ctx context.Context, orgID, projectID uuid.UUID, status models.ProjectStatus) error
}

type projectTaskStore interface {
	Create(ctx context.Context, t *models.ProjectTask) error
	GetByID(ctx context.Context, orgID, taskID uuid.UUID) (models.ProjectTask, error)
	ListByProject(ctx context.Context, orgID, projectID uuid.UUID, filters db.ProjectTaskFilters) ([]models.ProjectTask, error)
	Update(ctx context.Context, t *models.ProjectTask) error
	CountByProjectAndStatus(ctx context.Context, orgID, projectID uuid.UUID, status models.ProjectTaskStatus) (int, error)
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
	ListByOrgAndProvider(ctx context.Context, orgID uuid.UUID, provider models.IntegrationProvider) ([]models.Integration, error)
}

type credentialStore interface {
	GetAllIntegrations(ctx context.Context, orgID uuid.UUID, providers []models.ProviderName) (map[models.ProviderName]*models.DecryptedCredential, error)
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
	sessions          sessionStore
	sessionLogs       sessionLogStore // nil-safe: if nil, PM session logs are not persisted
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
	adapters          map[models.AgentType]agent.AgentAdapter
	env               *agent.AgentEnv
	usageTracker      agent.UsageRecorder // nil-safe: container billing disabled if nil
	github            agent.GitHubTokenProvider
	internalAPIURL    string // base URL for internal API (e.g. "http://server:8080/api/v1/internal")
	internalAPISecret string // signing secret for internal API tokens
	logger            zerolog.Logger
}

// pickAdapter returns the adapter for the org's default agent type. Returns an
// error when the adapter map is missing the requested type so callers fail fast
// with a readable message instead of a nil-dereference mid-run.
func (s *Service) pickAdapter(agentType models.AgentType) (agent.AgentAdapter, error) {
	if s.adapters == nil {
		return nil, fmt.Errorf("pm adapters not configured")
	}
	adapter, ok := s.adapters[agentType]
	if !ok || adapter == nil {
		return nil, fmt.Errorf("no adapter registered for agent type %q", agentType)
	}
	return adapter, nil
}

func (s *Service) finalizeSandboxEnv(agentType models.AgentType, env map[string]string) error {
	return s.env.CheckAuth(agentType, env)
}

func (s *Service) injectRequiredAgentAuth(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, sb *agent.Sandbox, env map[string]string) (agent.TokenBillingMode, error) {
	switch agentType {
	case models.AgentTypeCodex:
		if env["OPENAI_API_KEY"] != "" {
			return agent.TokenBillingModeAPIKey, nil
		}

		injected, err := s.env.InjectCodexAuth(ctx, orgID, sb)
		if err != nil {
			return agent.TokenBillingModeUnknown, &agent.AuthError{
				AgentType: agentType,
				Detail:    fmt.Sprintf("failed to prepare ChatGPT authentication for Codex: %v", err),
			}
		}
		if !injected {
			return agent.TokenBillingModeUnknown, &agent.AuthError{
				AgentType: agentType,
				Detail:    "No ChatGPT credentials are configured for Codex. Connect ChatGPT before running PM with Codex.",
			}
		}
		return agent.TokenBillingModeSubscription, nil
	case models.AgentTypeClaudeCode:
		injected, err := s.env.InjectClaudeCodeAuthWithEnv(ctx, orgID, sb, env)
		if err != nil {
			if fallbackErr := s.env.PrepareClaudeCodeAPIKeyFallback(ctx, sb, env); fallbackErr == nil {
				s.logger.Warn().
					Err(err).
					Str("org_id", orgID.String()).
					Msg("PM Claude subscription injection failed; continuing with Anthropic API-key fallback")
				return agent.TokenBillingModeAPIKey, nil
			}
			return agent.TokenBillingModeUnknown, &agent.AuthError{
				AgentType: agentType,
				Detail:    fmt.Sprintf("failed to prepare Claude subscription authentication: %v", err),
			}
		}
		if injected {
			return agent.TokenBillingModeSubscription, nil
		}
		if err := s.env.PrepareClaudeCodeAPIKeyFallback(ctx, sb, env); err == nil {
			return agent.TokenBillingModeAPIKey, nil
		} else if agent.IsClaudeCodeFallbackUnavailable(err) {
			return agent.TokenBillingModeUnknown, nil
		} else {
			return agent.TokenBillingModeUnknown, &agent.AuthError{
				AgentType: agentType,
				Detail:    fmt.Sprintf("failed to prepare Anthropic API-key fallback: %v", err),
			}
		}
	case models.AgentTypeGeminiCLI, models.AgentTypeAmp, models.AgentTypePi, models.AgentTypeOpenCode:
		return agent.TokenBillingModeAPIKey, nil
	default:
		return agent.TokenBillingModeUnknown, nil
	}
}

func (s *Service) executeAgentWithCredentialFallback(
	ctx context.Context,
	orgID uuid.UUID,
	agentType models.AgentType,
	settings models.OrgSettings,
	sbCfg agent.SandboxConfig,
	sb *agent.Sandbox,
	adapter agent.AgentAdapter,
	execCtx context.Context,
	prompt *agent.AgentPrompt,
	logCh chan<- agent.LogEntry,
) (*agent.AgentResult, error) {
	result, err := adapter.Execute(execCtx, sb, prompt, logCh)
	signal := agent.CredentialFailureSignalFromResult(result, time.Now())
	if !signal.RateLimited {
		s.shedCredentialFailure(ctx, orgID, agentType, result)
		return result, err
	}

	s.shedCredentialFailure(ctx, orgID, agentType, result)
	refreshedEnv := s.refreshPMCredentialEnv(ctx, orgID, agentType, sbCfg)
	if authErr := s.finalizeSandboxEnv(agentType, refreshedEnv); authErr != nil {
		return result, authErr
	}
	sb.Env = refreshedEnv
	authBillingMode, authErr := s.prepareAgentAuthForRetry(ctx, orgID, agentType, sb, refreshedEnv)
	if authErr != nil {
		return result, authErr
	}
	prompt.UsageHint = buildPMTokenUsageHint(settings, agentType, refreshedEnv, authBillingMode)

	s.logger.Info().
		Str("org_id", orgID.String()).
		Str("agent_type", string(agentType)).
		Msg("retrying PM agent with fallback credential after rate-limit signal")

	retryResult, retryErr := adapter.Execute(execCtx, sb, prompt, logCh)
	if agent.CredentialFailureSignalFromResult(retryResult, time.Now()).RateLimited {
		s.shedCredentialFailure(ctx, orgID, agentType, retryResult)
		refreshedEnv = s.refreshPMCredentialEnv(ctx, orgID, agentType, sbCfg)
		if authErr := s.finalizeSandboxEnv(agentType, refreshedEnv); authErr != nil {
			return retryResult, authErr
		}
	}
	return retryResult, retryErr
}

func (s *Service) refreshPMCredentialEnv(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, sbCfg agent.SandboxConfig) map[string]string {
	refreshedEnv := agent.RefreshAgentCredentialEnv(sbCfg.Env, s.env.Resolve(ctx, orgID, agentType, nil), agentType)
	if refreshedEnv == nil {
		refreshedEnv = make(map[string]string)
	}
	if _, ok := refreshedEnv["HOME"]; !ok {
		refreshedEnv["HOME"] = sbCfg.HomeDir
	}
	return refreshedEnv
}

func (s *Service) shedCredentialFailure(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, result *agent.AgentResult) {
	if s.env == nil || result == nil {
		return
	}
	provider := agent.CodingProviderForAgent(agentType)
	if provider == "" {
		return
	}
	signal := agent.CredentialFailureSignalFromResult(result, time.Now())
	switch {
	case signal.AuthRejected:
		s.env.ShedAuthRejectedWithContext(ctx, orgID, nil, provider)
	case signal.RateLimited:
		s.env.ShedRateLimitedWithSignal(ctx, orgID, nil, provider, signal)
	}
}

func (s *Service) prepareAgentAuthForRetry(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, sb *agent.Sandbox, env map[string]string) (agent.TokenBillingMode, error) {
	if agentType == models.AgentTypeCodex && env["OPENAI_API_KEY"] != "" {
		if err := s.removeCodexAuthFile(ctx, sb); err != nil {
			return agent.TokenBillingModeUnknown, err
		}
	}
	return s.injectRequiredAgentAuth(ctx, orgID, agentType, sb, env)
}

func (s *Service) removeCodexAuthFile(ctx context.Context, sb *agent.Sandbox) error {
	authPath := path.Join(sb.HomeDir, ".codex", "auth.json")
	cmd := fmt.Sprintf("rm -f '%s'", pmShellEscapeSingleQuote(authPath))
	var stdout, stderr bytes.Buffer
	exitCode, err := s.sandbox.Exec(ctx, sb, cmd, &stdout, &stderr)
	if err != nil {
		return fmt.Errorf("remove stale codex auth: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("remove stale codex auth: exited with code %d: %s", exitCode, stderr.String())
	}
	return nil
}

func pmShellEscapeSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}

func buildPMTokenUsageHint(settings models.OrgSettings, agentType models.AgentType, env map[string]string, billingMode agent.TokenBillingMode) agent.TokenUsageHint {
	return agent.TokenUsageHint{
		AgentType:      agentType,
		EffectiveModel: effectivePMAgentModel(settings, agentType, env),
		BillingMode:    effectivePMBillingMode(agentType, env, billingMode),
	}
}

func effectivePMAgentModel(settings models.OrgSettings, agentType models.AgentType, env map[string]string) string {
	if env == nil {
		env = map[string]string{}
	}
	switch agentType {
	case models.AgentTypeOpenCode:
		if env["OPENCODE_MODEL_CUSTOM"] != "" {
			return env["OPENCODE_MODEL_CUSTOM"]
		}
	case models.AgentTypePi:
		if env["PI_MODEL_CUSTOM"] != "" {
			return env["PI_MODEL_CUSTOM"]
		}
	case models.AgentTypeAmp:
		if env["AMP_MODE"] == "" {
			if cfg := settings.AgentConfig[string(agentType)]; cfg != nil && cfg["AMP_MODE"] != "" {
				return cfg["AMP_MODE"]
			}
			return models.AmpModeSmart
		}
	}
	if envVar := models.ModelEnvVarForAgentType(agentType); envVar != "" && env[envVar] != "" {
		return env[envVar]
	}
	if envVar := models.ModelEnvVarForAgentType(agentType); envVar != "" {
		if cfg := settings.AgentConfig[string(agentType)]; cfg != nil {
			return cfg[envVar]
		}
	}
	return ""
}

func effectivePMBillingMode(agentType models.AgentType, env map[string]string, fallback agent.TokenBillingMode) agent.TokenBillingMode {
	if fallback != "" && fallback != agent.TokenBillingModeUnknown {
		return fallback
	}
	switch agentType {
	case models.AgentTypeCodex:
		if env["OPENAI_API_KEY"] != "" {
			return agent.TokenBillingModeAPIKey
		}
		return agent.TokenBillingModeSubscription
	case models.AgentTypeClaudeCode:
		if env["ANTHROPIC_API_KEY"] != "" {
			return agent.TokenBillingModeAPIKey
		}
		return agent.TokenBillingModeUnknown
	case models.AgentTypeGeminiCLI, models.AgentTypeAmp, models.AgentTypePi, models.AgentTypeOpenCode:
		return agent.TokenBillingModeAPIKey
	default:
		return agent.TokenBillingModeUnknown
	}
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
	adapters map[models.AgentType]agent.AgentAdapter,
	env *agent.AgentEnv,
	github agent.GitHubTokenProvider,
	logger zerolog.Logger,
) *Service {
	return &Service{
		issues:       issues,
		sessions:     sessions,
		pullRequests: pullRequests,
		orgs:         orgs,
		repos:        repos,
		jobs:         jobs,
		plans:        plans,
		decisionLog:  decisionLog,
		sandbox:      sandbox,
		adapters:     adapters,
		env:          env,
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

// SetUsageTracker injects the container usage recorder for billing. Nil-safe:
// if not called, PM sandbox usage is not tracked.
func (s *Service) SetUsageTracker(ut agent.UsageRecorder) {
	s.usageTracker = ut
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
	repos, err := s.repos.ListByOrg(ctx, orgID, db.RepositoryFilters{})
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

func (s *Service) Analyze(ctx context.Context, orgID uuid.UUID, trigger models.PMTrigger, repoID *uuid.UUID, agentTypeOverride *models.AgentType) (*Plan, error) {
	if s.sandbox == nil || s.env == nil {
		return nil, fmt.Errorf("pm sandbox or env helper not configured")
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
		Status:        models.SessionStatusRunning,
		Title:         strPtr("PM Analysis"),
		RepositoryID:  &repo.ID,
		AutonomyLevel: models.SessionAutonomyFull,
		TokenMode:     models.SessionTokenModeHigh,
	}
	if err := s.sessions.Create(ctx, pmSession); err != nil {
		s.logger.Error().Err(err).Msg("failed to create PM session — continuing without session logging")
		pmSession = nil
	}

	// failSession marks the PM session as failed, capturing the error message on
	// the session record and writing a session log entry (matching the manual-session
	// pattern so failures are visible in the UI). It returns a wrapped error for the
	// caller to return directly, avoiding duplicate format strings at each call site.
	//
	// When the cause is an AuthError, a failed plan record is also persisted so
	// the UI can show an actionable message in the plan history rather than only
	// surfacing the error via the job's last_error column.
	failSession := func(stage string, cause error) error {
		errMsg := fmt.Sprintf("%s: %v", stage, cause)
		if pmSession != nil {
			result := &models.SessionResult{
				Error: strPtr(errMsg),
			}
			if err := s.sessions.UpdateResult(ctx, orgID, pmSession.ID, models.SessionStatusFailed, result); err != nil {
				s.logger.Error().Err(err).Msg("failed to mark PM session as failed")
			}
			if s.sessionLogs != nil {
				log := &models.SessionLog{
					SessionID: pmSession.ID,
					OrgID:     orgID,
					Level:     "error",
					Message:   errMsg,
				}
				if err := s.sessionLogs.Create(ctx, log); err != nil {
					s.logger.Error().Err(err).Msg("failed to write PM failure session log")
				}
			}
		}
		var authErr *agent.AuthError
		if errors.As(cause, &authErr) {
			s.persistFailedPlan(ctx, orgID, trigger, errMsg)
		}
		return fmt.Errorf("%s: %w", stage, cause)
	}

	ctxBundle, err := s.gatherContext(ctx, orgID, &repo)
	if err != nil {
		return nil, failSession("gather context", err)
	}

	agentType := resolveAgentType(ctxBundle.settings, agentTypeOverride)
	adapter, err := s.pickAdapter(agentType)
	if err != nil {
		return nil, failSession("pick adapter", err)
	}

	sbCfg := pmSandboxConfig()
	sbCfg.Env = s.env.Resolve(ctx, orgID, agentType, nil)
	if sbCfg.Env == nil {
		sbCfg.Env = make(map[string]string)
	}

	// Inject internal API credentials so the PM agent can create issues.
	if s.internalAPIURL != "" && s.internalAPISecret != "" {
		// Token TTL extends past sandbox timeout to avoid mid-execution expiry.
		tokenTTL := sbCfg.Timeout + 5*time.Minute
		internalToken, tokenErr := auth.GenerateInternalToken(s.internalAPISecret, orgID, repo.ID, tokenTTL)
		if tokenErr != nil {
			s.logger.Warn().Err(tokenErr).Msg("failed to generate internal API token — issue creation will be unavailable")
		} else {
			sbCfg.Env["INTERNAL_API_TOKEN"] = internalToken
			sbCfg.Env["INTERNAL_API_URL"] = s.internalAPIURL
		}
	}

	if err := s.finalizeSandboxEnv(agentType, sbCfg.Env); err != nil {
		return nil, failSession("agent auth preflight", err)
	}

	sb, err := s.sandbox.Create(ctx, sbCfg)
	if err != nil {
		return nil, failSession("create sandbox", err)
	}
	exitReason := "completed"
	containerStartedAt := time.Now()
	var usageEventID uuid.UUID
	sessionID := uuid.Nil
	if pmSession != nil {
		sessionID = pmSession.ID
	}
	if s.usageTracker != nil {
		usageEventID = s.usageTracker.ContainerStarted(ctx, orgID, sessionID, sb, sbCfg, containerStartedAt)
	}
	defer func() {
		if s.usageTracker != nil {
			s.usageTracker.ContainerStopped(ctx, orgID, sessionID, usageEventID, sb.ID, containerStartedAt, exitReason)
		}
		if destroyErr := s.sandbox.Destroy(ctx, sb); destroyErr != nil {
			s.logger.Warn().Err(destroyErr).Msg("failed to destroy PM sandbox")
		}
	}()

	authBillingMode, err := s.injectRequiredAgentAuth(ctx, orgID, agentType, sb, sbCfg.Env)
	if err != nil {
		exitReason = containerExitReason(ctx, err)
		return nil, failSession("inject codex auth", err)
	}

	var token string
	if s.github != nil {
		ghToken, err := s.github.GetInstallationToken(ctx, repo.InstallationID)
		if err != nil {
			exitReason = containerExitReason(ctx, err)
			return nil, failSession("get installation token", err)
		}
		token = ghToken
	}
	if err := s.sandbox.CloneRepo(ctx, sb, repo.CloneURL, repo.DefaultBranch, token); err != nil {
		exitReason = containerExitReason(ctx, err)
		return nil, failSession("clone repo", err)
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
		exitReason = containerExitReason(ctx, err)
		return nil, failSession("marshal pm context", err)
	}
	if err := s.sandbox.WriteFile(ctx, sb, "/workspace/.pm-context.json", contextJSON); err != nil {
		exitReason = containerExitReason(ctx, err)
		return nil, failSession("write context to sandbox", err)
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
		UsageHint:    buildPMTokenUsageHint(ctxBundle.settings, agentType, sbCfg.Env, authBillingMode),
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
	result, err := s.executeAgentWithCredentialFallback(ctx, orgID, agentType, ctxBundle.settings, sbCfg, sb, adapter, execCtx, prompt, logCh)
	close(logCh)
	logWg.Wait()
	if err != nil {
		exitReason = containerExitReason(ctx, err)
		return nil, failSession("pm agent execution", err)
	}

	plan, err := parsePlan(result.Summary)
	if err != nil {
		exitReason = containerExitReason(ctx, err)
		sessionErr := failSession("parse plan", err)
		logOutput := excerpt(result.Summary, 2000)
		sessionID := ""
		if pmSession != nil {
			sessionID = pmSession.ID.String()
		}
		s.logger.Error().
			Str("session_id", sessionID).
			Str("agent_output", logOutput).
			Err(err).
			Msg("failed to parse PM plan from agent output")
		if sessionID != "" {
			return nil, fmt.Errorf("parse plan [session_id=%s]: %w", sessionID, err)
		}
		return nil, sessionErr
	}

	plan.OrgID = orgID
	plan.Status = models.PMPlanStatusExecuting
	plan.TriggeredBy = trigger
	plan.IssuesReviewed = len(ctxBundle.pmContext.OpenIssues)
	if agent.HasPersistableTokenUsage(result.TokenUsage) {
		tokenJSON, err := json.Marshal(result.TokenUsage)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to marshal token usage")
			tokenJSON = nil
		}
		plan.TokenUsage = tokenJSON
	}

	planModel, err := planToModel(plan, ctxBundle.productContext)
	if err != nil {
		exitReason = containerExitReason(ctx, err)
		return nil, failSession("serialize plan", err)
	}
	if err := s.plans.Create(ctx, planModel); err != nil {
		exitReason = containerExitReason(ctx, err)
		return nil, failSession("persist plan", err)
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
		exitReason = containerExitReason(ctx, err)
		return nil, failSession("execute plan", err)
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

	// Mark PM session as completed, persisting token usage on the session record.
	if pmSession != nil {
		sessionResult := &models.SessionResult{
			TokenUsage: plan.TokenUsage,
		}
		if err := s.sessions.UpdateResult(ctx, orgID, pmSession.ID, models.SessionStatusCompleted, sessionResult); err != nil {
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
			OrgID:     pmSession.OrgID,
			Level:     models.SessionLogLevel(entry.Level),
			Message:   entry.Message,
			Metadata:  metadata,
		}
		if err := s.sessionLogs.Create(ctx, log); err != nil {
			s.logger.Error().Err(err).Msg("failed to persist PM log entry")
		}
	}
}

// persistFailedPlan creates a minimal plan record with status=failed so auth
// and other early failures appear in the plan history. The Analysis field
// carries the error message, giving the UI something actionable to show. Called
// from failSession when the error chain contains an AuthError. Returns the
// persisted plan ID for log correlation, or uuid.Nil on failure.
func (s *Service) persistFailedPlan(ctx context.Context, orgID uuid.UUID, trigger models.PMTrigger, errMsg string) uuid.UUID {
	if s.plans == nil {
		s.logger.Error().Msg("failed to persist failed plan record: plan store not configured")
		return uuid.Nil
	}

	now := time.Now()
	plan := &models.PMPlan{
		OrgID:         orgID,
		Status:        models.PMPlanStatusFailed,
		Analysis:      errMsg,
		Tasks:         []byte("[]"),
		Clusters:      []byte("[]"),
		SkippedIssues: []byte("[]"),
		TriggeredBy:   trigger,
		CompletedAt:   &now,
	}
	if err := s.plans.Create(ctx, plan); err != nil {
		s.logger.Error().Err(err).Msg("failed to persist failed plan record")
		return uuid.Nil
	}
	return plan.ID
}

func containerExitReason(ctx context.Context, err error) string {
	if err == nil {
		return "completed"
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if ctxErr == context.DeadlineExceeded {
			return "timeout"
		}
		return "cancelled"
	}
	return "failed"
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
