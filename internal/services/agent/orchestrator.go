package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sync"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/assembledhq/143/internal/services/mcp"
)

const (
	defaultMaxConcurrent = 3
)

// GitHubTokenProvider abstracts retrieving a GitHub App installation token.
type GitHubTokenProvider interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
}

// CodexAuthProvider abstracts retrieving valid ChatGPT OAuth tokens for Codex.
type CodexAuthProvider interface {
	GetValidToken(ctx context.Context, orgID uuid.UUID) (*models.OpenAIChatGPTConfig, error)
}

// CredentialProvider abstracts retrieving org-scoped provider credentials.
type CredentialProvider interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

// SessionStore defines the agent run DB operations needed by the orchestrator.
type SessionStore interface {
	UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error
	UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.SessionResult) error
	CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error)
}

// SessionLogStore defines the log persistence operations.
type SessionLogStore interface {
	Create(ctx context.Context, log *models.SessionLog) error
}

// SessionQuestionStore defines the question persistence operations.
type SessionQuestionStore interface {
	Create(ctx context.Context, q *models.SessionQuestion) error
}

// DecisionLogStore updates PM decision log outcomes.
type DecisionLogStore interface {
	UpdateOutcome(ctx context.Context, orgID, planID, issueID uuid.UUID, outcome models.PMDecisionOutcome) error
}

// OrgStore defines the organization read operations needed for org-level config.
type OrgStore interface {
	GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error)
}

// IssueStore defines the issue read operations.
type IssueStore interface {
	GetByID(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error)
}

// RepositoryStore defines the repository read operations.
type RepositoryStore interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

// JobStore defines the job enqueue operations.
type JobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

// ProjectTaskUpdater is called after an agent run completes to update
// the associated project task, if any.
type ProjectTaskUpdater interface {
	OnSessionComplete(ctx context.Context, run *models.Session, status string) error
}

// Orchestrator coordinates end-to-end agent execution: sandbox lifecycle,
// agent invocation, log streaming, result handling, and follow-up job enqueuing.
type Orchestrator struct {
	provider          SandboxProvider
	adapters          map[models.AgentType]AgentAdapter
	sessions         SessionStore
	agentRunLogs      SessionLogStore
	agentRunQuestions SessionQuestionStore
	decisionLog       DecisionLogStore
	projectTasks      ProjectTaskUpdater // can be nil
	issues            IssueStore
	repositories      RepositoryStore
	orgs              OrgStore
	jobs              JobStore
	github            GitHubTokenProvider
	codexAuth         CodexAuthProvider // can be nil
	credentials       CredentialProvider
	logger            zerolog.Logger
	maxConcurrent     int
}

// OrchestratorConfig holds the dependencies for creating an Orchestrator.
type OrchestratorConfig struct {
	Provider          SandboxProvider
	Adapters          map[models.AgentType]AgentAdapter
	Sessions         SessionStore
	SessionLogs      SessionLogStore
	SessionQuestions SessionQuestionStore
	DecisionLog       DecisionLogStore
	ProjectTasks      ProjectTaskUpdater // optional — updates project tasks on run completion
	Issues            IssueStore
	Repositories      RepositoryStore
	Orgs              OrgStore
	Jobs              JobStore
	GitHub            GitHubTokenProvider
	CodexAuth         CodexAuthProvider // optional — enables ChatGPT OAuth for Codex agent
	Credentials       CredentialProvider
	Logger            zerolog.Logger
	MaxConcurrent     int
}

// NewOrchestrator creates an Orchestrator with the given dependencies.
func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrent
	}

	return &Orchestrator{
		provider:          cfg.Provider,
		adapters:          cfg.Adapters,
		sessions:         cfg.Sessions,
		agentRunLogs:      cfg.SessionLogs,
		agentRunQuestions: cfg.SessionQuestions,
		decisionLog:       cfg.DecisionLog,
		projectTasks:      cfg.ProjectTasks,
		issues:            cfg.Issues,
		repositories:      cfg.Repositories,
		orgs:              cfg.Orgs,
		jobs:              cfg.Jobs,
		github:            cfg.GitHub,
		codexAuth:         cfg.CodexAuth,
		credentials:       cfg.Credentials,
		logger:            cfg.Logger,
		maxConcurrent:     maxConcurrent,
	}
}

// RunAgent is the main entry point. It executes an agent run end-to-end:
// concurrency check → sandbox creation → repo clone → agent execution →
// result handling → follow-up job enqueuing → sandbox cleanup.
func (o *Orchestrator) RunAgent(ctx context.Context, run *models.Session) error {
	log := o.logger.With().
		Str("run_id", run.ID.String()).
		Str("org_id", run.OrgID.String()).
		Str("issue_id", run.IssueID.String()).
		Logger()

	// 1. Concurrency check.
	if err := o.checkConcurrency(ctx, run.OrgID); err != nil {
		log.Info().Err(err).Msg("concurrency limit reached, run stays pending")
		return err
	}

	// 2. Update status to "running" (sets started_at).
	if err := o.sessions.UpdateStatus(ctx, run.OrgID, run.ID, "running"); err != nil {
		return fmt.Errorf("update run status to running: %w", err)
	}

	// 3. Fetch the issue.
	issue, err := o.issues.GetByID(ctx, run.OrgID, run.IssueID)
	if err != nil {
		o.failRun(ctx, run, fmt.Sprintf("fetch issue: %s", err))
		return fmt.Errorf("fetch issue: %w", err)
	}

	// 4. Look up the repository for clone URL and branch.
	var repoURL, branch, token string
	if issue.RepositoryID != nil {
		repo, err := o.repositories.GetByID(ctx, run.OrgID, *issue.RepositoryID)
		if err != nil {
			o.failRun(ctx, run, fmt.Sprintf("fetch repository: %s", err))
			return fmt.Errorf("fetch repository: %w", err)
		}
		repoURL = repo.CloneURL
		branch = repo.DefaultBranch

		// Get GitHub installation token for cloning.
		ghToken, err := o.github.GetInstallationToken(ctx, repo.InstallationID)
		if err != nil {
			o.failRun(ctx, run, fmt.Sprintf("get installation token: %s", err))
			return fmt.Errorf("get installation token: %w", err)
		}
		token = ghToken
	}

	// 5. Get the adapter for this agent type.
	adapter, ok := o.adapters[run.AgentType]
	if !ok {
		o.failRun(ctx, run, fmt.Sprintf("unknown agent type: %s", run.AgentType))
		return fmt.Errorf("unknown agent type: %s", run.AgentType)
	}

	// 6. Prepare the prompt.
	input := &AgentInput{
		Issue:      &issue,
		RepoURL:    repoURL,
		RepoBranch: branch,
		TokenMode:  run.TokenMode,
	}
	if run.ComplexityTier != nil {
		input.ComplexityEstimate = &ComplexityEstimate{
			Tier: *run.ComplexityTier,
		}
	}
	if run.PMApproach != nil || run.PMReasoning != nil {
		pmCtx := &PMTaskContext{}
		if run.PMApproach != nil {
			pmCtx.Approach = *run.PMApproach
		}
		if run.PMReasoning != nil {
			pmCtx.Reasoning = *run.PMReasoning
		}
		input.PMContext = pmCtx
	}

	// 6b. Generate integration skills doc from org credentials.
	// This tells the agent what CLI tools are available in the sandbox.
	input.IntegrationSkills = o.buildIntegrationSkills(ctx, run.OrgID)

	prompt, err := adapter.PreparePrompt(ctx, input)
	if err != nil {
		o.failRun(ctx, run, fmt.Sprintf("prepare prompt: %s", err))
		return fmt.Errorf("prepare prompt: %w", err)
	}

	// 7. Create sandbox with agent-specific env vars (API keys).
	// Start with server-level defaults, then overlay org-level overrides.
	sandboxCfg := DefaultSandboxConfig()
	sandboxCfg.Env = o.resolveAgentEnv(ctx, run.OrgID, run.AgentType)
	// Ensure HOME points to the sandbox workdir so CLI tools (e.g. Codex
	// resolving ~/.codex/auth.json) find files written to the workdir.
	if sandboxCfg.Env == nil {
		sandboxCfg.Env = make(map[string]string)
	}
	// Apply per-run model override if set.
	if run.ModelOverride != nil && *run.ModelOverride != "" {
		if envVar := models.ModelEnvVarForAgentType(run.AgentType); envVar != "" {
			sandboxCfg.Env[envVar] = *run.ModelOverride
		}
	}
	if _, ok := sandboxCfg.Env["HOME"]; !ok {
		sandboxCfg.Env["HOME"] = sandboxCfg.WorkDir
	}
	sandbox, err := o.provider.Create(ctx, sandboxCfg)
	if err != nil {
		o.failRun(ctx, run, fmt.Sprintf("create sandbox: %s", err))
		return fmt.Errorf("create sandbox: %w", err)
	}
	defer func() {
		if destroyErr := o.provider.Destroy(ctx, sandbox); destroyErr != nil {
			log.Error().Err(destroyErr).Msg("failed to destroy sandbox")
		}
	}()

	// 8. Inject Codex auth file if this is a codex agent run.
	if run.AgentType == models.AgentTypeCodex {
		injected, err := o.injectCodexAuth(ctx, run.OrgID, sandbox)
		if err != nil {
			log.Warn().Err(err).Msg("codex auth injection failed, falling back to API key")
		}
		// Fail early if neither OAuth token nor API key is available.
		hasAPIKey := sandboxCfg.Env["OPENAI_API_KEY"] != ""
		if !hasAPIKey && !injected {
			o.failRun(ctx, run, "no credentials configured for codex: connect ChatGPT or set an OPENAI_API_KEY in Settings")
			return fmt.Errorf("no credentials for codex agent")
		}
	}

	// 8b. Integration tools (143-tools CLI) are pre-installed in the container
	// image. Credentials are injected via env vars (resolveAgentEnv), and the
	// skills doc is injected into the prompt (buildIntegrationSkills). No
	// per-CLI config file injection needed — all agents can shell out directly.

	// 9. Clone repo into sandbox.
	if repoURL != "" {
		if err := o.provider.CloneRepo(ctx, sandbox, repoURL, branch, token); err != nil {
			o.failRun(ctx, run, fmt.Sprintf("clone repo: %s", err))
			return fmt.Errorf("clone repo: %w", err)
		}
	}

	// 10. Execute agent with log streaming.
	logCh := make(chan LogEntry, 100)
	var logWg sync.WaitGroup
	logWg.Add(1)
	go func() {
		defer logWg.Done()
		o.streamLogs(ctx, run.ID, run.OrgID, logCh)
	}()

	result, err := adapter.Execute(ctx, sandbox, prompt, logCh)
	close(logCh)
	logWg.Wait()

	// 11. Handle result.
	if err != nil {
		o.failRun(ctx, run, err.Error())
		o.enqueueJob(ctx, run.OrgID, "agent", "analyze_failure", map[string]interface{}{
			"session_id": run.ID.String(),
			"org_id":       run.OrgID.String(),
		})
		return fmt.Errorf("execute agent: %w", err)
	}

	// Fetch org settings for confidence thresholds.
	confidenceThresholds := models.ConfidenceThresholdsForAutonomy(models.DefaultAgentAutonomy)
	if o.orgs != nil {
		if org, orgErr := o.orgs.GetByID(ctx, run.OrgID); orgErr == nil {
			orgSettings := models.ParseOrgSettings(org.Settings)
			confidenceThresholds = orgSettings.ConfidenceThresholds
		}
	}

	// Store the successful result.
	runResult := o.buildRunResult(result)
	status := "completed"

	// 11. Confidence gating: use org-configured auto-proceed threshold.
	if result.ConfidenceScore < confidenceThresholds.AutoProceed {
		status = "needs_human_guidance"
	}

	if err := o.sessions.UpdateResult(ctx, run.OrgID, run.ID, status, runResult); err != nil {
		return fmt.Errorf("update run result: %w", err)
	}

	log.Info().
		Str("status", status).
		Float64("confidence", result.ConfidenceScore).
		Float64("threshold", confidenceThresholds.AutoProceed).
		Msg("agent run finished")

	// 12. Enqueue follow-up job based on confidence.
	if result.ConfidenceScore >= confidenceThresholds.AutoProceed {
		o.enqueueJob(ctx, run.OrgID, "agent", "validate", map[string]interface{}{
			"session_id": run.ID.String(),
			"org_id":       run.OrgID.String(),
		})
	}

	if run.PMPlanID != nil && o.decisionLog != nil {
		outcome := outcomeFromRunStatus(status)
		if outcome != "" {
			if run.IssueID != uuid.Nil {
				if err := o.decisionLog.UpdateOutcome(ctx, run.OrgID, *run.PMPlanID, run.IssueID, outcome); err != nil {
					o.logger.Warn().Err(err).Str("run_id", run.ID.String()).Msg("failed to update PM decision log outcome")
				}
			} else {
				o.logger.Debug().Str("run_id", run.ID.String()).Msg("skipping PM decision log outcome update because run has no issue_id")
			}
		}
	}

	// 13. Update project task status if this run is part of a project.
	if run.ProjectTaskID != nil && o.projectTasks != nil {
		if err := o.projectTasks.OnSessionComplete(ctx, run, status); err != nil {
			o.logger.Warn().Err(err).Str("run_id", run.ID.String()).Msg("failed to update project task on run completion")
		}
	}

	return nil
}

func outcomeFromRunStatus(status string) models.PMDecisionOutcome {
	switch status {
	case "completed":
		return models.PMDecisionOutcomeSucceeded
	case "failed":
		return models.PMDecisionOutcomeFailed
	case "needs_human_guidance":
		return models.PMDecisionOutcomeStillOpen
	default:
		return ""
	}
}

// checkConcurrency verifies the org hasn't exceeded its concurrent run limit.
func (o *Orchestrator) checkConcurrency(ctx context.Context, orgID uuid.UUID) error {
	count, err := o.sessions.CountRunningByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("count running runs: %w", err)
	}
	if count >= o.maxConcurrent {
		return fmt.Errorf("concurrency limit reached: %d/%d runs active", count, o.maxConcurrent)
	}
	return nil
}

// streamLogs reads LogEntry values from the channel and persists them to the DB.
// It also detects question-level log entries and creates SessionQuestion records.
func (o *Orchestrator) streamLogs(ctx context.Context, runID, orgID uuid.UUID, logCh <-chan LogEntry) {
	for entry := range logCh {
		metadata, _ := json.Marshal(entry.Metadata)

		log := &models.SessionLog{
			SessionID: runID,
			Level:      entry.Level,
			Message:    entry.Message,
			Metadata:   metadata,
		}
		if err := o.agentRunLogs.Create(ctx, log); err != nil {
			o.logger.Error().Err(err).Str("run_id", runID.String()).Msg("failed to persist log entry")
		}

		// Detect question-level entries.
		if entry.Level == "question" {
			o.handleQuestion(ctx, runID, orgID, entry)
		}
	}
}

// handleQuestion creates an SessionQuestion and updates the run status to awaiting_input.
func (o *Orchestrator) handleQuestion(ctx context.Context, runID, orgID uuid.UUID, entry LogEntry) {
	q := &models.SessionQuestion{
		SessionID:   runID,
		OrgID:        orgID,
		QuestionText: entry.Message,
		Status:       "pending",
	}

	// Extract structured question fields from metadata if present.
	if opts, ok := entry.Metadata["options"]; ok {
		if optSlice, ok := opts.([]interface{}); ok {
			for _, opt := range optSlice {
				if s, ok := opt.(string); ok {
					q.Options = append(q.Options, s)
				}
			}
		}
	}
	if ctxVal, ok := entry.Metadata["context"]; ok {
		if s, ok := ctxVal.(string); ok {
			q.Context = &s
		}
	}
	if phase, ok := entry.Metadata["blocks_phase"]; ok {
		if s, ok := phase.(string); ok {
			q.BlocksPhase = &s
		}
	}

	if err := o.agentRunQuestions.Create(ctx, q); err != nil {
		o.logger.Error().Err(err).Str("run_id", runID.String()).Msg("failed to create agent run question")
		return
	}

	if err := o.sessions.UpdateStatus(ctx, orgID, runID, "awaiting_input"); err != nil {
		o.logger.Error().Err(err).Str("run_id", runID.String()).Msg("failed to update run status to awaiting_input")
	}
}

// failRun marks a run as failed and records the error.
func (o *Orchestrator) failRun(ctx context.Context, run *models.Session, errMsg string) {
	result := &models.SessionResult{
		Error: strPtr(errMsg),
	}
	if err := o.sessions.UpdateResult(ctx, run.OrgID, run.ID, "failed", result); err != nil {
		o.logger.Error().Err(err).Str("run_id", run.ID.String()).Msg("failed to update run to failed")
	}
	if run.ProjectTaskID != nil && o.projectTasks != nil {
		if err := o.projectTasks.OnSessionComplete(ctx, run, "failed"); err != nil {
			o.logger.Warn().Err(err).Str("run_id", run.ID.String()).Msg("failed to update project task on run failure")
		}
	}
}

// buildRunResult converts an AgentResult into the DB update struct.
func (o *Orchestrator) buildRunResult(result *AgentResult) *models.SessionResult {
	tokenUsage, _ := json.Marshal(result.TokenUsage)

	return &models.SessionResult{
		ConfidenceScore:     &result.ConfidenceScore,
		ConfidenceReasoning: strPtr(result.ConfidenceReasoning),
		RiskFactors:         result.RiskFactors,
		TokenUsage:          tokenUsage,
		ResultSummary:       strPtr(result.Summary),
		Diff:                strPtr(result.Diff),
		Error:               strPtr(result.Error),
	}
}

// enqueueJob is a helper that enqueues a job and logs errors without failing the caller.
func (o *Orchestrator) enqueueJob(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload map[string]interface{}) {
	_, err := o.jobs.Enqueue(ctx, orgID, queue, jobType, payload, 0, nil)
	if err != nil {
		o.logger.Error().Err(err).
			Str("job_type", jobType).
			Msg("failed to enqueue follow-up job")
	}
}

// integrationCredentials holds the resolved Sentry and Linear configs for an org.
type integrationCredentials struct {
	Sentry *models.SentryConfig
	Linear *models.LinearConfig
}

// fetchIntegrationCredentials retrieves the Sentry and Linear configs for
// an org from the credential provider. Returns nil configs if unavailable.
func (o *Orchestrator) fetchIntegrationCredentials(ctx context.Context, orgID uuid.UUID) integrationCredentials {
	var ic integrationCredentials
	if o.credentials == nil {
		return ic
	}

	if cred, err := o.credentials.Get(ctx, orgID, models.ProviderSentry); err == nil && cred != nil {
		if cfg, ok := cred.Config.(models.SentryConfig); ok {
			ic.Sentry = &cfg
		}
	}
	if cred, err := o.credentials.Get(ctx, orgID, models.ProviderLinear); err == nil && cred != nil {
		if cfg, ok := cred.Config.(models.LinearConfig); ok {
			ic.Linear = &cfg
		}
	}
	return ic
}

// resolveAgentEnv builds the sandbox env vars for the given agent type from
// org-scoped credentials in org_credentials.
func (o *Orchestrator) resolveAgentEnv(ctx context.Context, orgID uuid.UUID, agentType models.AgentType) map[string]string {
	if o.credentials == nil {
		return nil
	}

	merged := make(map[string]string)

	switch agentType {
	case models.AgentTypeClaudeCode:
		cred, err := o.credentials.Get(ctx, orgID, models.ProviderAnthropic)
		if err == nil && cred != nil {
			if cfg, ok := cred.Config.(models.AnthropicConfig); ok {
				if cfg.APIKey != "" {
					merged["ANTHROPIC_API_KEY"] = cfg.APIKey
				}
				if cfg.BaseURL != "" {
					merged["ANTHROPIC_BASE_URL"] = cfg.BaseURL
				}
			}
		}
	case models.AgentTypeCodex:
		cred, err := o.credentials.Get(ctx, orgID, models.ProviderOpenAI)
		if err == nil && cred != nil {
			if cfg, ok := cred.Config.(models.OpenAIConfig); ok {
				if cfg.APIKey != "" {
					merged["OPENAI_API_KEY"] = cfg.APIKey
				}
				if cfg.BaseURL != "" {
					merged["OPENAI_BASE_URL"] = cfg.BaseURL
				}
			}
		}
	case models.AgentTypeGeminiCLI:
		cred, err := o.credentials.Get(ctx, orgID, models.ProviderGemini)
		if err == nil && cred != nil {
			if cfg, ok := cred.Config.(models.GeminiConfig); ok {
				if cfg.APIKey != "" {
					merged["GEMINI_API_KEY"] = cfg.APIKey
				}
				if cfg.Model != "" {
					merged["GEMINI_MODEL"] = cfg.Model
				}
			}
		}
	}

	// Integration credentials — consumed by the 143-mcp binary inside the sandbox.
	// These are injected for all agent types since the MCP server is agent-agnostic.
	ic := o.fetchIntegrationCredentials(ctx, orgID)
	if ic.Sentry != nil {
		if ic.Sentry.AccessToken != "" {
			merged["SENTRY_AUTH_TOKEN"] = ic.Sentry.AccessToken
		}
		if ic.Sentry.OrgSlug != "" {
			merged["SENTRY_ORG_SLUG"] = ic.Sentry.OrgSlug
		}
	}
	if ic.Linear != nil {
		if ic.Linear.AccessToken != "" {
			merged["LINEAR_ACCESS_TOKEN"] = ic.Linear.AccessToken
		}
	}

	if len(merged) == 0 {
		return nil
	}

	return merged
}

// injectCodexAuth writes a ~/.codex/auth.json file into the sandbox if
// a ChatGPT OAuth token exists for this org. Returns (true, nil) if auth
// was injected, (false, nil) if no OAuth token is available (allowing
// fallback to OPENAI_API_KEY env var), or (false, err) on failure.
func (o *Orchestrator) injectCodexAuth(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox) (bool, error) {
	if o.codexAuth == nil {
		return false, nil
	}

	cfg, err := o.codexAuth.GetValidToken(ctx, orgID)
	if err != nil {
		return false, fmt.Errorf("get codex auth token: %w", err)
	}
	if cfg == nil {
		// No OAuth token — not an error, agent will use API key.
		return false, nil
	}

	authJSON, err := json.Marshal(map[string]interface{}{
		"access_token":  cfg.AccessToken,
		"refresh_token": cfg.RefreshToken,
		"expires_at":    cfg.ExpiresAt.Format("2006-01-02T15:04:05Z"),
	})
	if err != nil {
		return false, fmt.Errorf("marshal auth.json: %w", err)
	}

	// Write auth.json under the sandbox workdir/.codex. The sandbox env
	// sets HOME to the workdir (see RunAgent step 7) so the Codex CLI
	// resolves ~/.codex/auth.json to this path.
	authDir := path.Join(sandbox.WorkDir, ".codex")
	mkdirCmd := fmt.Sprintf("mkdir -p %s", authDir)

	var mkdirOut, mkdirErr bytes.Buffer
	if _, err := o.provider.Exec(ctx, sandbox, mkdirCmd, &mkdirOut, &mkdirErr); err != nil {
		return false, fmt.Errorf("create .codex dir: %w", err)
	}

	authPath := authDir + "/auth.json"
	if err := o.provider.WriteFile(ctx, sandbox, authPath, authJSON); err != nil {
		return false, fmt.Errorf("write auth.json: %w", err)
	}

	o.logger.Debug().
		Str("org_id", orgID.String()).
		Msg("injected codex auth.json into sandbox")

	return true, nil
}

// buildIntegrationSkills generates a CLI skills doc from the org's integration
// credentials. The doc is injected into the agent's system prompt so it knows
// what 143-tools commands are available in the sandbox.
func (o *Orchestrator) buildIntegrationSkills(ctx context.Context, orgID uuid.UUID) string {
	if o.credentials == nil {
		return ""
	}

	ic := o.fetchIntegrationCredentials(ctx, orgID)
	reg := integration.NewRegistry()

	// Register integrations based on available credentials.
	if ic.Sentry != nil && ic.Sentry.AccessToken != "" {
		tracker := integration.NewSentryErrorTracker(integration.SentryTrackerConfig{
			AuthToken: ic.Sentry.AccessToken,
			OrgSlug:   ic.Sentry.OrgSlug,
		})
		reg.RegisterErrorTracker(tracker)
	}
	if ic.Linear != nil && ic.Linear.AccessToken != "" {
		manager := integration.NewLinearTaskManager(integration.LinearManagerConfig{
			AuthToken: ic.Linear.AccessToken,
		})
		reg.RegisterTaskManager(manager)
	}

	if !reg.HasAny() {
		return ""
	}

	tr := mcp.NewToolRegistry(reg)
	return mcp.GenerateSkillsDoc(tr)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
