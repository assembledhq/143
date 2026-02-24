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
)

const (
	defaultMaxConcurrent = 3

	// lowConfidenceThreshold is the threshold below which runs are paused
	// for human guidance instead of proceeding to validation.
	lowConfidenceThreshold = 0.5
)

// GitHubTokenProvider abstracts retrieving a GitHub App installation token.
type GitHubTokenProvider interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
}

// CodexAuthProvider abstracts retrieving valid ChatGPT OAuth tokens for Codex.
type CodexAuthProvider interface {
	GetValidToken(ctx context.Context, orgID uuid.UUID) (*models.OpenAIChatGPTConfig, error)
}

// AgentRunStore defines the agent run DB operations needed by the orchestrator.
type AgentRunStore interface {
	UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error
	UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.AgentRunResult) error
	CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error)
}

// AgentRunLogStore defines the log persistence operations.
type AgentRunLogStore interface {
	Create(ctx context.Context, log *models.AgentRunLog) error
}

// AgentRunQuestionStore defines the question persistence operations.
type AgentRunQuestionStore interface {
	Create(ctx context.Context, q *models.AgentRunQuestion) error
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

// Orchestrator coordinates end-to-end agent execution: sandbox lifecycle,
// agent invocation, log streaming, result handling, and follow-up job enqueuing.
type Orchestrator struct {
	provider          SandboxProvider
	adapters          map[string]AgentAdapter
	agentRuns         AgentRunStore
	agentRunLogs      AgentRunLogStore
	agentRunQuestions AgentRunQuestionStore
	issues            IssueStore
	repositories      RepositoryStore
	orgs              OrgStore
	jobs              JobStore
	github            GitHubTokenProvider
	codexAuth         CodexAuthProvider // can be nil
	logger            zerolog.Logger
	maxConcurrent     int
	agentEnv          map[string]map[string]string
}

// OrchestratorConfig holds the dependencies for creating an Orchestrator.
type OrchestratorConfig struct {
	Provider          SandboxProvider
	Adapters          map[string]AgentAdapter
	AgentRuns         AgentRunStore
	AgentRunLogs      AgentRunLogStore
	AgentRunQuestions AgentRunQuestionStore
	Issues            IssueStore
	Repositories      RepositoryStore
	Orgs              OrgStore
	Jobs              JobStore
	GitHub            GitHubTokenProvider
	CodexAuth         CodexAuthProvider // optional — enables ChatGPT OAuth for Codex agent
	Logger            zerolog.Logger
	MaxConcurrent     int

	// AgentEnv maps agent type names to the environment variables that should
	// be injected into their sandbox containers. These are server-level defaults
	// that can be overridden by org-level agent_config in org settings.
	AgentEnv map[string]map[string]string
}

// NewOrchestrator creates an Orchestrator with the given dependencies.
func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrent
	}

	agentEnv := cfg.AgentEnv
	if agentEnv == nil {
		agentEnv = make(map[string]map[string]string)
	}

	return &Orchestrator{
		provider:          cfg.Provider,
		adapters:          cfg.Adapters,
		agentRuns:         cfg.AgentRuns,
		agentRunLogs:      cfg.AgentRunLogs,
		agentRunQuestions: cfg.AgentRunQuestions,
		issues:            cfg.Issues,
		repositories:      cfg.Repositories,
		orgs:              cfg.Orgs,
		jobs:              cfg.Jobs,
		github:            cfg.GitHub,
		codexAuth:         cfg.CodexAuth,
		logger:            cfg.Logger,
		maxConcurrent:     maxConcurrent,
		agentEnv:          agentEnv,
	}
}

// RunAgent is the main entry point. It executes an agent run end-to-end:
// concurrency check → sandbox creation → repo clone → agent execution →
// result handling → follow-up job enqueuing → sandbox cleanup.
func (o *Orchestrator) RunAgent(ctx context.Context, run *models.AgentRun) error {
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
	if err := o.agentRuns.UpdateStatus(ctx, run.OrgID, run.ID, "running"); err != nil {
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
	if run.AgentType == "codex" {
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
			"agent_run_id": run.ID.String(),
			"org_id":       run.OrgID.String(),
		})
		return fmt.Errorf("execute agent: %w", err)
	}

	// Store the successful result.
	runResult := o.buildRunResult(result)
	status := "completed"

	// 11. Confidence gating.
	if result.ConfidenceScore < lowConfidenceThreshold {
		status = "needs_human_guidance"
	}

	if err := o.agentRuns.UpdateResult(ctx, run.OrgID, run.ID, status, runResult); err != nil {
		return fmt.Errorf("update run result: %w", err)
	}

	log.Info().
		Str("status", status).
		Float64("confidence", result.ConfidenceScore).
		Msg("agent run finished")

	// 12. Enqueue follow-up job based on confidence.
	if result.ConfidenceScore >= lowConfidenceThreshold {
		o.enqueueJob(ctx, run.OrgID, "agent", "validate", map[string]interface{}{
			"agent_run_id": run.ID.String(),
			"org_id":       run.OrgID.String(),
		})
	}

	return nil
}

// checkConcurrency verifies the org hasn't exceeded its concurrent run limit.
func (o *Orchestrator) checkConcurrency(ctx context.Context, orgID uuid.UUID) error {
	count, err := o.agentRuns.CountRunningByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("count running runs: %w", err)
	}
	if count >= o.maxConcurrent {
		return fmt.Errorf("concurrency limit reached: %d/%d runs active", count, o.maxConcurrent)
	}
	return nil
}

// streamLogs reads LogEntry values from the channel and persists them to the DB.
// It also detects question-level log entries and creates AgentRunQuestion records.
func (o *Orchestrator) streamLogs(ctx context.Context, runID, orgID uuid.UUID, logCh <-chan LogEntry) {
	for entry := range logCh {
		metadata, _ := json.Marshal(entry.Metadata)

		log := &models.AgentRunLog{
			AgentRunID: runID,
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

// handleQuestion creates an AgentRunQuestion and updates the run status to awaiting_input.
func (o *Orchestrator) handleQuestion(ctx context.Context, runID, orgID uuid.UUID, entry LogEntry) {
	q := &models.AgentRunQuestion{
		AgentRunID:   runID,
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

	if err := o.agentRuns.UpdateStatus(ctx, orgID, runID, "awaiting_input"); err != nil {
		o.logger.Error().Err(err).Str("run_id", runID.String()).Msg("failed to update run status to awaiting_input")
	}
}

// failRun marks a run as failed and records the error.
func (o *Orchestrator) failRun(ctx context.Context, run *models.AgentRun, errMsg string) {
	result := &models.AgentRunResult{
		Error: strPtr(errMsg),
	}
	if err := o.agentRuns.UpdateResult(ctx, run.OrgID, run.ID, "failed", result); err != nil {
		o.logger.Error().Err(err).Str("run_id", run.ID.String()).Msg("failed to update run to failed")
	}
}

// buildRunResult converts an AgentResult into the DB update struct.
func (o *Orchestrator) buildRunResult(result *AgentResult) *models.AgentRunResult {
	tokenUsage, _ := json.Marshal(result.TokenUsage)

	return &models.AgentRunResult{
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

// resolveAgentEnv merges server-level and org-level env vars for the given agent type.
// Org-level values override server-level defaults.
func (o *Orchestrator) resolveAgentEnv(ctx context.Context, orgID uuid.UUID, agentType string) map[string]string {
	merged := make(map[string]string)

	// Start with server-level defaults.
	if base, ok := o.agentEnv[agentType]; ok {
		for k, v := range base {
			merged[k] = v
		}
	}

	// Overlay org-level overrides from settings.
	if o.orgs != nil {
		org, err := o.orgs.GetByID(ctx, orgID)
		if err == nil {
			orgSettings := models.ParseOrgSettings(org.Settings)
			if orgOverrides, ok := orgSettings.AgentConfig[agentType]; ok {
				for k, v := range orgOverrides {
					if v != "" {
						merged[k] = v
					}
				}
			}
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

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
