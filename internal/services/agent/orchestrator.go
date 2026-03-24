package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/assembledhq/143/internal/services/mcp"
	"github.com/assembledhq/143/internal/services/storage"
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
	RefreshToken(ctx context.Context, orgID uuid.UUID) (*models.OpenAIChatGPTConfig, error)
}

// CredentialProvider abstracts retrieving org-scoped provider credentials.
type CredentialProvider interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

// UserCredentialProvider abstracts retrieving user-scoped provider credentials.
type UserCredentialProvider interface {
	GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
	GetTeamDefault(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
}

// SessionStore defines the agent run DB operations needed by the orchestrator.
type SessionStore interface {
	UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error
	UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.SessionResult) error
	CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error)
	UpdateTurnComplete(ctx context.Context, orgID, sessionID uuid.UUID, turn int, result *models.SessionResult, agentSessionID, snapshotKey string) error
	UpdateSnapshotInfo(ctx context.Context, orgID, sessionID uuid.UUID, agentSessionID, snapshotKey string) error
	UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state string) error
	UpdateWorkingBranch(ctx context.Context, orgID, sessionID uuid.UUID, branch string) error
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
}

// SessionLogStore defines the log persistence operations.
type SessionLogStore interface {
	Create(ctx context.Context, log *models.SessionLog) error
}

// SessionQuestionStore defines the question persistence operations.
type SessionQuestionStore interface {
	Create(ctx context.Context, q *models.SessionQuestion) error
}

// SessionMessageStore defines the message persistence operations for multi-turn sessions.
type SessionMessageStore interface {
	Create(ctx context.Context, msg *models.SessionMessage) error
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionMessage, error)
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

// MemoryService provides scored memory context for agent prompts.
type MemoryService interface {
	GetContextMemories(ctx context.Context, req MemoryContextRequest) (*MemoryContextResult, error)
}

// MemoryContextRequest describes the context for memory selection.
// Mirrors memory.ContextRequest but avoids a circular import.
type MemoryContextRequest struct {
	OrgID     uuid.UUID
	Repo      string
	FilePaths []string
}

// MemoryContextResult contains the selected memory context.
type MemoryContextResult struct {
	Formatted string
	MemoryIDs []uuid.UUID
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
	sessions          SessionStore
	agentRunLogs      SessionLogStore
	agentRunQuestions SessionQuestionStore
	sessionMessages   SessionMessageStore
	decisionLog       DecisionLogStore
	projectTasks      ProjectTaskUpdater // can be nil
	issues            IssueStore
	repositories      RepositoryStore
	orgs              OrgStore
	jobs              JobStore
	github            GitHubTokenProvider
	codexAuth         CodexAuthProvider // can be nil
	credentials       CredentialProvider
	memory            MemoryService // can be nil
	userCredentials   UserCredentialProvider // can be nil
	snapshots         storage.SnapshotStore // can be nil — multi-turn disabled if nil
	logger            zerolog.Logger
	maxConcurrent     int
}

// OrchestratorConfig holds the dependencies for creating an Orchestrator.
type OrchestratorConfig struct {
	Provider         SandboxProvider
	Adapters         map[models.AgentType]AgentAdapter
	Sessions         SessionStore
	SessionLogs      SessionLogStore
	SessionQuestions SessionQuestionStore
	SessionMessages   SessionMessageStore
	DecisionLog       DecisionLogStore
	ProjectTasks      ProjectTaskUpdater // optional — updates project tasks on run completion
	Issues            IssueStore
	Repositories      RepositoryStore
	Orgs              OrgStore
	Jobs              JobStore
	GitHub            GitHubTokenProvider
	CodexAuth         CodexAuthProvider      // optional — enables ChatGPT OAuth for Codex agent
	Credentials       CredentialProvider
	Memory            MemoryService // optional — injects learned memories into agent prompts
	UserCredentials   UserCredentialProvider // optional — enables personal/team credential resolution
	Snapshots         storage.SnapshotStore // optional — enables multi-turn snapshot/restore
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
		sessions:          cfg.Sessions,
		agentRunLogs:      cfg.SessionLogs,
		agentRunQuestions: cfg.SessionQuestions,
		sessionMessages:   cfg.SessionMessages,
		decisionLog:       cfg.DecisionLog,
		projectTasks:      cfg.ProjectTasks,
		issues:            cfg.Issues,
		repositories:      cfg.Repositories,
		orgs:              cfg.Orgs,
		jobs:              cfg.Jobs,
		github:            cfg.GitHub,
		codexAuth:         cfg.CodexAuth,
		credentials:       cfg.Credentials,
		memory:            cfg.Memory,
		userCredentials:   cfg.UserCredentials,
		snapshots:         cfg.Snapshots,
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

	// 3. Fetch the issue (non-fatal when IssueID is nil/zero for project-dispatched sessions).
	var issue *models.Issue
	if run.IssueID != uuid.Nil {
		fetched, err := o.issues.GetByID(ctx, run.OrgID, run.IssueID)
		if err != nil {
			o.failRun(ctx, run, fmt.Sprintf("fetch issue: %s", err))
			return fmt.Errorf("fetch issue: %w", err)
		}
		issue = &fetched
	}

	// 4. Resolve which repository to clone.
	// Priority: session.RepositoryID → issue.RepositoryID (backwards compat).
	var resolvedRepoID *uuid.UUID
	if run.RepositoryID != nil {
		resolvedRepoID = run.RepositoryID
	} else if issue != nil && issue.RepositoryID != nil {
		resolvedRepoID = issue.RepositoryID
	}

	var repoURL, branch, token, repoFullName string
	if resolvedRepoID != nil {
		repo, err := o.repositories.GetByID(ctx, run.OrgID, *resolvedRepoID)
		if err != nil {
			o.failRun(ctx, run, fmt.Sprintf("fetch repository: %s", err))
			return fmt.Errorf("fetch repository: %w", err)
		}
		repoURL = repo.CloneURL
		branch = repo.DefaultBranch
		repoFullName = repo.FullName

		// Override with session-specific branch if set.
		if run.TargetBranch != nil && *run.TargetBranch != "" {
			branch = *run.TargetBranch
		}

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

	// 5b. Resolve org-specific context limits for adaptive token budgets.
	var contextLimits *models.ContextLimits
	if o.orgs != nil {
		if org, orgErr := o.orgs.GetByID(ctx, run.OrgID); orgErr == nil {
			if orgSettings, parseErr := models.ParseOrgSettings(org.Settings); parseErr == nil {
				contextLimits = &orgSettings.ContextLimits
			}
		}
	}

	// 6. Prepare the prompt.
	input := &AgentInput{
		Issue:         issue,
		RepoURL:       repoURL,
		RepoBranch:    branch,
		TokenMode:     run.TokenMode,
		ContextLimits: contextLimits,
	}
	if run.ComplexityTier != nil {
		input.ComplexityEstimate = &ComplexityEstimate{
			Tier: *run.ComplexityTier,
		}
	}
	// Inject learned memories into agent context.
	if o.memory != nil && repoFullName != "" {
		memResult, memErr := o.memory.GetContextMemories(ctx, MemoryContextRequest{
			OrgID: run.OrgID,
			Repo:  repoFullName,
		})
		if memErr != nil {
			log.Warn().Err(memErr).Str("repo", repoFullName).Msg("failed to retrieve memories for agent context")
		} else if memResult != nil && memResult.Formatted != "" {
			input.ContextDocs = append(input.ContextDocs, memResult.Formatted)
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
	sandboxCfg.Env = o.resolveAgentEnv(ctx, run.OrgID, run.AgentType, run.TriggeredByUserID)
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

	// 8. Clone repo into sandbox. This must happen before auth injection
	// so that /workspace is empty when git clone runs (git clone fails on
	// non-empty directories).
	if repoURL != "" {
		if err := o.provider.CloneRepo(ctx, sandbox, repoURL, branch, token); err != nil {
			o.failRun(ctx, run, fmt.Sprintf("clone repo: %s", err))
			return fmt.Errorf("clone repo: %w", err)
		}

		// 8b. Create a working branch so the agent operates on a separate
		// branch from the start, keeping the base branch clean.
		workingBranch := formatWorkingBranch(run, issue)
		checkoutCmd := fmt.Sprintf("git checkout -b '%s'", shellEscapeSingleQuote(workingBranch))
		var checkoutOut, checkoutErr bytes.Buffer
		exitCode, execErr := o.provider.Exec(ctx, sandbox, checkoutCmd, &checkoutOut, &checkoutErr)
		if execErr != nil || exitCode != 0 {
			log.Warn().
				Err(execErr).
				Int("exit_code", exitCode).
				Str("stderr", checkoutErr.String()).
				Msg("failed to create working branch, agent will work on base branch")
		} else {
			run.WorkingBranch = &workingBranch
			if dbErr := o.sessions.UpdateWorkingBranch(ctx, run.OrgID, run.ID, workingBranch); dbErr != nil {
				log.Warn().Err(dbErr).Str("branch", workingBranch).Msg("failed to persist working branch")
			}
		}
	}

	// 9. Inject Codex auth.json if this is a codex agent run.
	//    auth.json is the primary auth mechanism (uses ChatGPT backend).
	//    Done after clone so the workspace is available.
	if run.AgentType == models.AgentTypeCodex {
		injected, err := o.injectCodexAuth(ctx, run.OrgID, sandbox)
		if err != nil {
			o.failRun(ctx, run, fmt.Sprintf("codex auth injection failed: %s", err))
			return fmt.Errorf("codex auth injection: %w", err)
		}
		if !injected {
			o.failRun(ctx, run, "no credentials configured for codex: connect ChatGPT from the Overview page")
			return fmt.Errorf("no credentials for codex agent")
		}
	}

	// 9b. Integration tools (143-tools CLI) are pre-installed in the container
	// image. Credentials are injected via env vars (resolveAgentEnv), and the
	// skills doc is injected into the prompt (buildIntegrationSkills). No
	// per-CLI config file injection needed — all agents can shell out directly.

	// 10. Execute agent with log streaming.
	logCh := make(chan LogEntry, 100)
	var logWg sync.WaitGroup
	logWg.Add(1)
	go func() {
		defer logWg.Done()
		o.streamLogs(ctx, run.ID, run.OrgID, run.CurrentTurn, logCh)
	}()

	execCtx := WithSandboxProvider(ctx, o.provider)
	result, err := adapter.Execute(execCtx, sandbox, prompt, logCh)
	close(logCh)
	logWg.Wait()

	// 11. Handle result.
	if err != nil {
		o.failRun(ctx, run, err.Error())
		o.enqueueJob(ctx, run.OrgID, "agent", "analyze_failure", map[string]interface{}{
			"session_id": run.ID.String(),
			"org_id":     run.OrgID.String(),
		})
		return fmt.Errorf("execute agent: %w", err)
	}

	// 11b. Snapshot workspace for multi-turn support (does not change session status).
	snapshotKey, snapshotErr := o.snapshotSession(ctx, run, sandbox, result)
	if snapshotErr != nil {
		log.Warn().Err(snapshotErr).Msg("failed to snapshot session, session will not support follow-up turns")
	}

	// Fetch org settings for confidence thresholds.
	confidenceThresholds := models.ConfidenceThresholdsForAutonomy(models.DefaultAgentAutonomy)
	if o.orgs != nil {
		if org, orgErr := o.orgs.GetByID(ctx, run.OrgID); orgErr == nil {
			orgSettings, parseErr := models.ParseOrgSettings(org.Settings)
			if parseErr != nil {
				o.logger.Warn().Err(parseErr).Str("org_id", run.OrgID.String()).Msg("failed to parse org settings, using defaults")
			} else {
				confidenceThresholds = orgSettings.ConfidenceThresholds
			}
		}
	}

	// Store the successful result.
	runResult := o.buildRunResult(result)
	status := "completed"
	isInteractive := o.isInteractiveSession(issue) && snapshotKey != ""

	// 11. Confidence gating: use org-configured auto-proceed threshold.
	if result.ConfidenceScore < confidenceThresholds.AutoProceed {
		status = "needs_human_guidance"
	}

	if isInteractive {
		turnNumber := run.CurrentTurn + 1
		if err := o.createAssistantMessage(ctx, run.ID, run.OrgID, turnNumber, result); err != nil {
			log.Warn().Err(err).Msg("failed to persist assistant message for interactive turn")
		}

		agentSessionID := result.AgentSessionID
		if agentSessionID == "" && run.AgentSessionID != nil {
			agentSessionID = *run.AgentSessionID
		}
		if err := o.sessions.UpdateTurnComplete(ctx, run.OrgID, run.ID, turnNumber, runResult, agentSessionID, snapshotKey); err != nil {
			return fmt.Errorf("update interactive turn result: %w", err)
		}

		log.Info().
			Int("turn", turnNumber).
			Msg("interactive manual session turn completed and returned to idle")
		return nil
	}

	if err := o.sessions.UpdateResult(ctx, run.OrgID, run.ID, status, runResult); err != nil {
		return fmt.Errorf("update run result: %w", err)
	}

	// Persist snapshot metadata so the session can be continued later.
	// Uses UpdateSnapshotInfo (not UpdateTurnComplete) to avoid overwriting
	// the status set by UpdateResult above.
	if snapshotKey != "" {
		agentSessionID := result.AgentSessionID
		if agentSessionID == "" && run.AgentSessionID != nil {
			agentSessionID = *run.AgentSessionID
		}
		if err := o.sessions.UpdateSnapshotInfo(ctx, run.OrgID, run.ID, agentSessionID, snapshotKey); err != nil {
			log.Warn().Err(err).Msg("failed to persist snapshot metadata")
		}
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
			"org_id":     run.OrgID.String(),
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

// ContinueSession handles a follow-up turn in a multi-turn session.
// It creates a fresh sandbox, restores the snapshot from the previous turn,
// re-injects credentials, runs the agent with --resume, and snapshots again.
//
// Authorization: callers must verify the requesting user is authorized before
// invoking this method. The SendMessage HTTP handler enforces this via org_id
// scoping and ClaimIdle atomicity.
func (o *Orchestrator) ContinueSession(ctx context.Context, session *models.Session) error {
	log := o.logger.With().
		Str("session_id", session.ID.String()).
		Str("org_id", session.OrgID.String()).
		Int("turn", session.CurrentTurn).
		Logger()

	// Determine whether we can restore from a snapshot or need a fresh start.
	hasSnapshot := session.SnapshotKey != nil && *session.SnapshotKey != "" &&
		o.snapshots != nil &&
		session.SandboxState != string(models.SandboxStateDestroyed)

	// 1. Update status to running.
	if err := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, "running"); err != nil {
		return fmt.Errorf("update session status to running: %w", err)
	}
	if err := o.sessions.UpdateSandboxState(ctx, session.OrgID, session.ID, string(models.SandboxStateRunning)); err != nil {
		log.Warn().Err(err).Msg("failed to update sandbox state to running")
	}

	// 2. Get the adapter.
	adapter, ok := o.adapters[session.AgentType]
	if !ok {
		o.failRun(ctx, session, fmt.Sprintf("unknown agent type: %s", session.AgentType))
		return fmt.Errorf("unknown agent type: %s", session.AgentType)
	}

	// 3. Get the latest user message for this turn.
	messages, err := o.sessionMessages.ListBySession(ctx, session.OrgID, session.ID)
	if err != nil {
		o.failRun(ctx, session, fmt.Sprintf("fetch session messages: %s", err))
		return fmt.Errorf("fetch session messages: %w", err)
	}

	var userMessage string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == models.MessageRoleUser {
			userMessage = messages[i].Content
			break
		}
	}
	if userMessage == "" {
		o.failRun(ctx, session, "no user message found for continue_session")
		return fmt.Errorf("no user message found")
	}

	// 4. Create sandbox.
	sandboxCfg := DefaultSandboxConfig()
	sandboxCfg.Env = o.resolveAgentEnv(ctx, session.OrgID, session.AgentType, session.TriggeredByUserID)
	if sandboxCfg.Env == nil {
		sandboxCfg.Env = make(map[string]string)
	}
	if session.ModelOverride != nil && *session.ModelOverride != "" {
		if envVar := models.ModelEnvVarForAgentType(session.AgentType); envVar != "" {
			sandboxCfg.Env[envVar] = *session.ModelOverride
		}
	}
	if _, ok := sandboxCfg.Env["HOME"]; !ok {
		sandboxCfg.Env["HOME"] = sandboxCfg.WorkDir
	}

	sandbox, err := o.provider.Create(ctx, sandboxCfg)
	if err != nil {
		o.failRun(ctx, session, fmt.Sprintf("create sandbox: %s", err))
		return fmt.Errorf("create sandbox: %w", err)
	}
	defer func() {
		if destroyErr := o.provider.Destroy(ctx, sandbox); destroyErr != nil {
			log.Error().Err(destroyErr).Msg("failed to destroy sandbox")
		}
	}()

	// 5. Set up the workspace — either restore snapshot or clone fresh.
	var prompt *AgentPrompt
	if hasSnapshot {
		// Path A: Restore snapshot from storage — preserves all git changes.
		snapshotReader, snapshotWriter := io.Pipe()
		var restoreErr error
		var restoreWg sync.WaitGroup
		restoreWg.Add(1)
		go func() {
			defer restoreWg.Done()
			restoreErr = o.snapshots.Load(ctx, *session.SnapshotKey, snapshotWriter)
			_ = snapshotWriter.Close() // Intentionally ignored: pipe close error is not actionable here; restoreErr captures the real failure.
		}()

		if err := o.provider.Restore(ctx, sandbox, snapshotReader); err != nil {
			_ = snapshotReader.Close() // Intentionally ignored: we already have the restore error.
			restoreWg.Wait()
			o.failRun(ctx, session, fmt.Sprintf("restore snapshot: %s", err))
			return fmt.Errorf("restore snapshot: %w", err)
		}
		restoreWg.Wait()
		if restoreErr != nil {
			o.failRun(ctx, session, fmt.Sprintf("load snapshot from storage: %s", restoreErr))
			return fmt.Errorf("load snapshot: %w", restoreErr)
		}

		// Re-inject Codex auth.json if needed.
		if session.AgentType == models.AgentTypeCodex {
			injected, err := o.injectCodexAuth(ctx, session.OrgID, sandbox)
			if err != nil {
				o.failRun(ctx, session, fmt.Sprintf("codex auth injection failed: %s", err))
				return fmt.Errorf("codex auth injection: %w", err)
			}
			if !injected {
				o.failRun(ctx, session, "no credentials configured for codex: connect ChatGPT from the Overview page")
				return fmt.Errorf("no credentials for codex agent")
			}
		}

		var resumeSessionID string
		if session.AgentSessionID != nil {
			resumeSessionID = *session.AgentSessionID
		}
		prompt = &AgentPrompt{
			Continuation:    true,
			ResumeSessionID: resumeSessionID,
			UserMessage:     userMessage,
			MaxTokens:       tokenLimitForMode(session.TokenMode),
		}

		log.Info().Msg("continuing session with snapshot restore")
	} else {
		// Path B: No snapshot available — clone repo fresh and provide
		// conversation history + stored diff as context so the agent can
		// reconstruct the prior state.
		log.Info().Msg("continuing session without snapshot, starting fresh")

		issue, repoFullName, err := o.setupFreshSandbox(ctx, session, sandbox)
		if err != nil {
			o.failRun(ctx, session, fmt.Sprintf("setup fresh sandbox: %s", err))
			return fmt.Errorf("setup fresh sandbox: %w", err)
		}

		// Build a full prompt via PreparePrompt so the agent gets the system
		// prompt with integration skills, memory, and repo conventions.
		input := &AgentInput{
			Issue:     &issue,
			TokenMode: session.TokenMode,
		}
		input.IntegrationSkills = o.buildIntegrationSkills(ctx, session.OrgID)
		if o.memory != nil && repoFullName != "" {
			memResult, memErr := o.memory.GetContextMemories(ctx, MemoryContextRequest{
				OrgID: session.OrgID,
				Repo:  repoFullName,
			})
			if memErr != nil {
				log.Warn().Err(memErr).Str("repo", repoFullName).Msg("failed to retrieve memories for resume context")
			} else if memResult != nil && memResult.Formatted != "" {
				input.ContextDocs = append(input.ContextDocs, memResult.Formatted)
			}
		}

		basePrompt, err := adapter.PreparePrompt(ctx, input)
		if err != nil {
			o.failRun(ctx, session, fmt.Sprintf("prepare prompt for resume: %s", err))
			return fmt.Errorf("prepare prompt for resume: %w", err)
		}

		// Override UserPrompt with resume context (conversation history + diff).
		basePrompt.UserPrompt = o.buildResumeContext(session, &issue, messages, userMessage)
		basePrompt.Continuation = false
		prompt = basePrompt
	}

	// 6. Execute agent with log streaming.
	turnNumber := session.CurrentTurn + 1
	logCh := make(chan LogEntry, 100)
	var logWg sync.WaitGroup
	logWg.Add(1)
	go func() {
		defer logWg.Done()
		o.streamLogs(ctx, session.ID, session.OrgID, turnNumber, logCh)
	}()

	execCtx := WithSandboxProvider(ctx, o.provider)
	result, err := adapter.Execute(execCtx, sandbox, prompt, logCh)
	close(logCh)
	logWg.Wait()

	if err != nil {
		o.failRun(ctx, session, err.Error())
		return fmt.Errorf("execute agent on continue: %w", err)
	}

	// 7. Create assistant message with result summary.
	if err := o.createAssistantMessage(ctx, session.ID, session.OrgID, turnNumber, result); err != nil {
		log.Warn().Err(err).Msg("failed to create assistant message")
	}

	// 8. Snapshot again.
	newSnapshotKey, snapshotErr := o.snapshotSession(ctx, session, sandbox, result)
	if snapshotErr != nil {
		log.Warn().Err(snapshotErr).Msg("failed to snapshot session after continue")
	}

	// 9. Update turn complete — sets status to idle.
	agentSessionID := result.AgentSessionID
	if agentSessionID == "" && session.AgentSessionID != nil {
		agentSessionID = *session.AgentSessionID
	}
	snapshotKey := newSnapshotKey
	if snapshotKey == "" && session.SnapshotKey != nil {
		snapshotKey = *session.SnapshotKey
	}
	if err := o.sessions.UpdateTurnComplete(ctx, session.OrgID, session.ID, turnNumber, o.buildRunResult(result), agentSessionID, snapshotKey); err != nil {
		return fmt.Errorf("update turn complete: %w", err)
	}

	log.Info().Int("turn", turnNumber).Msg("session turn completed, now idle")
	return nil
}

// setupFreshSandbox clones the session's repository into the sandbox when no
// snapshot is available. Returns the issue (for prompt building) and the repo
// full name (for memory lookup). Handles sessions with or without a repository.
func (o *Orchestrator) setupFreshSandbox(ctx context.Context, session *models.Session, sandbox *Sandbox) (models.Issue, string, error) {
	// Fetch the issue to get repository info.
	issue, err := o.issues.GetByID(ctx, session.OrgID, session.IssueID)
	if err != nil {
		return models.Issue{}, "", fmt.Errorf("fetch issue: %w", err)
	}

	// Clone repo if the session has one.
	var repoFullName string
	if issue.RepositoryID != nil {
		repo, err := o.repositories.GetByID(ctx, session.OrgID, *issue.RepositoryID)
		if err != nil {
			return models.Issue{}, "", fmt.Errorf("fetch repository: %w", err)
		}
		repoFullName = repo.FullName
		branch := repo.DefaultBranch
		if session.TargetBranch != nil && *session.TargetBranch != "" {
			branch = *session.TargetBranch
		}
		token, err := o.github.GetInstallationToken(ctx, repo.InstallationID)
		if err != nil {
			return models.Issue{}, "", fmt.Errorf("get installation token: %w", err)
		}
		if err := o.provider.CloneRepo(ctx, sandbox, repo.CloneURL, branch, token); err != nil {
			return models.Issue{}, "", fmt.Errorf("clone repo: %w", err)
		}
	}

	// Inject Codex auth if needed.
	if session.AgentType == models.AgentTypeCodex {
		injected, err := o.injectCodexAuth(ctx, session.OrgID, sandbox)
		if err != nil {
			return models.Issue{}, "", fmt.Errorf("codex auth injection: %w", err)
		}
		if !injected {
			return models.Issue{}, "", fmt.Errorf("no credentials for codex agent")
		}
	}

	return issue, repoFullName, nil
}

// maxDiffCharsInPrompt is the maximum number of characters from a stored diff
// to include in a resume prompt. Larger diffs are truncated to avoid blowing
// up the agent's token budget.
const maxDiffCharsInPrompt = 50000

// buildResumeContext constructs the user prompt for a resumed session that has
// no snapshot. It includes the issue description, conversation history, and
// the stored diff so the agent understands the prior state and can re-apply
// changes.
func (o *Orchestrator) buildResumeContext(session *models.Session, issue *models.Issue, messages []models.SessionMessage, latestUserMessage string) string {
	var b bytes.Buffer

	b.WriteString("This is a continuation of a previous session. The previous workspace state is not available, so you are starting from a fresh clone.\n\n")

	// Include the original issue description for context (especially
	// important for non-manual sessions that may have no prior messages).
	if issue != nil && issue.Description != nil && *issue.Description != "" {
		b.WriteString("## Original issue\n\n**")
		b.WriteString(issue.Title)
		b.WriteString("**\n\n")
		b.WriteString(*issue.Description)
		b.WriteString("\n\n")
	}

	// Include conversation history if available.
	if len(messages) > 1 { // >1 because the latest user message is always present
		b.WriteString("## Previous conversation history\n\n")
		for _, msg := range messages[:len(messages)-1] {
			role := "User"
			if msg.Role == models.MessageRoleAssistant {
				role = "Assistant"
			}
			b.WriteString(fmt.Sprintf("**%s:** %s\n\n", role, msg.Content))
		}
	}

	// Include the stored diff if available, truncating if very large.
	if session.Diff != nil && *session.Diff != "" {
		diff := *session.Diff
		truncated := false
		if len(diff) > maxDiffCharsInPrompt {
			diff = diff[:maxDiffCharsInPrompt]
			truncated = true
		}
		b.WriteString("## Previous code changes (git diff)\n\nThe following diff was produced in the previous session. Please re-apply these changes as needed:\n\n```diff\n")
		b.WriteString(diff)
		b.WriteString("\n```\n")
		if truncated {
			b.WriteString("\n**Note:** The diff was truncated due to size. The full diff had additional changes not shown above.\n")
		}
		b.WriteString("\n")
	}

	// Include the result summary if available.
	if session.ResultSummary != nil && *session.ResultSummary != "" {
		b.WriteString("## Previous session summary\n\n")
		b.WriteString(*session.ResultSummary)
		b.WriteString("\n\n")
	}

	b.WriteString("## New message\n\n")
	b.WriteString(latestUserMessage)

	return b.String()
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
func (o *Orchestrator) streamLogs(ctx context.Context, runID, orgID uuid.UUID, turnNumber int, logCh <-chan LogEntry) {
	for entry := range logCh {
		metadata, err := json.Marshal(entry.Metadata)
		if err != nil {
			o.logger.Warn().Err(err).Str("run_id", runID.String()).Msg("failed to marshal log entry metadata")
			metadata = nil
		}

		log := &models.SessionLog{
			SessionID:  runID,
			Level:      entry.Level,
			Message:    entry.Message,
			Metadata:   metadata,
			TurnNumber: turnNumber,
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
		SessionID:    runID,
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
	tokenUsage, err := json.Marshal(result.TokenUsage)
	if err != nil {
		o.logger.Warn().Err(err).Msg("failed to marshal token usage")
		tokenUsage = nil
	}

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

// resolveAgentEnv builds the sandbox env vars for the given agent type.
// It checks credentials in order: user personal → team default → org credential.
// Codex CLI auth is handled via auth.json injection (injectCodexAuth), not env vars.
func (o *Orchestrator) resolveAgentEnv(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, userID *uuid.UUID) map[string]string {
	merged := make(map[string]string)

	switch agentType {
	case models.AgentTypeClaudeCode:
		cfg := o.resolveProviderConfig(ctx, orgID, userID, models.ProviderAnthropic)
		if ac, ok := cfg.(models.AnthropicConfig); ok {
			if ac.APIKey != "" {
				merged["ANTHROPIC_API_KEY"] = ac.APIKey
			}
			if ac.BaseURL != "" {
				merged["ANTHROPIC_BASE_URL"] = ac.BaseURL
			}
		}
	case models.AgentTypeCodex:
		// Codex CLI authenticates via ~/.codex/auth.json (injected by
		// injectCodexAuth), NOT via the CODEX_API_KEY env var. The env
		// var makes Codex call api.openai.com/v1/responses which requires
		// the api.responses.write scope — a scope the ChatGPT OAuth token
		// does not carry. The auth.json path uses the ChatGPT backend
		// instead, which accepts the OAuth token as-is.
		//
		// Inject the general OpenAI API key as OPENAI_API_KEY for other
		// tools in the sandbox (not used by Codex CLI itself).
		cfg := o.resolveProviderConfig(ctx, orgID, userID, models.ProviderOpenAI)
		if oc, ok := cfg.(models.OpenAIConfig); ok {
			if oc.APIKey != "" {
				merged["OPENAI_API_KEY"] = oc.APIKey
			}
			if oc.BaseURL != "" {
				merged["OPENAI_BASE_URL"] = oc.BaseURL
			}
		}
		// Skip Codex CLI's internal bwrap (bubblewrap) sandboxing. The
		// container is already isolated by Docker + gVisor (dropped caps,
		// read-only rootfs, non-root user, PID limits), so bwrap is
		// redundant and fails because gVisor doesn't support the
		// unprivileged user namespaces that bwrap requires.
		merged["CODEX_UNSAFE_ALLOW_NO_SANDBOX"] = "1"
	case models.AgentTypeGeminiCLI:
		cfg := o.resolveProviderConfig(ctx, orgID, userID, models.ProviderGemini)
		if gc, ok := cfg.(models.GeminiConfig); ok {
			if gc.APIKey != "" {
				merged["GEMINI_API_KEY"] = gc.APIKey
			}
			if gc.Model != "" {
				merged["GEMINI_MODEL"] = gc.Model
			}
		}
	}

	// Integration credentials — consumed by the 143-tools CLI (preferred) and
	// 143-mcp binary inside the sandbox. Agents use the CLI via shell commands;
	// the MCP server is only for IDE integrations. See internal/services/mcp/AGENTS.md.
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

// resolveProviderConfig returns the best ProviderConfig for a provider,
// checking in order: user personal → team default → org credential.
func (o *Orchestrator) resolveProviderConfig(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) models.ProviderConfig {
	// 1. Check user's personal credential.
	if userID != nil && o.userCredentials != nil {
		if cred, err := o.userCredentials.GetForUser(ctx, orgID, *userID, provider); err == nil && cred != nil {
			return cred.Config
		}
	}

	// 2. Check team default credential.
	if o.userCredentials != nil {
		if cred, err := o.userCredentials.GetTeamDefault(ctx, orgID, provider); err == nil && cred != nil {
			return cred.Config
		}
	}

	// 3. Fall back to org credential.
	if o.credentials != nil {
		if cred, err := o.credentials.Get(ctx, orgID, provider); err == nil && cred != nil {
			return cred.Config
		}
	}

	return nil
}

// injectCodexAuth writes a ~/.codex/auth.json file into the sandbox if
// a ChatGPT OAuth token exists for this org. This is the primary Codex
// auth mechanism — auth.json tells the CLI to use the ChatGPT backend
// which accepts the OAuth token without needing api.responses.write scope.
// Returns (true, nil) if auth was injected, (false, nil) if no OAuth
// token is available, or (false, err) on failure.
func (o *Orchestrator) injectCodexAuth(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox) (bool, error) {
	if o.codexAuth == nil {
		return false, nil
	}

	// Force-refresh the token to ensure a fresh access_token is injected
	// into the sandbox. If the refresh fails (e.g. token already consumed),
	// fall back to GetValidToken which returns any cached valid token.
	cfg, err := o.codexAuth.RefreshToken(ctx, orgID)
	if err != nil || cfg == nil {
		if err != nil {
			o.logger.Debug().Err(err).Str("org_id", orgID.String()).Msg("forced token refresh failed, falling back to GetValidToken")
		}
		cfg, err = o.codexAuth.GetValidToken(ctx, orgID)
		if err != nil {
			return false, fmt.Errorf("get codex auth token: %w", err)
		}
	}
	if cfg == nil {
		// No OAuth token — not an error, agent will use API key.
		return false, nil
	}

	// Omit the refresh_token from auth.json so the Codex CLI never
	// attempts to refresh the token itself. If the CLI refreshes the
	// token inside the sandbox, it consumes the refresh_token on
	// OpenAI's servers, but the sandbox-side token is lost when the
	// container is destroyed. Our DB then holds a stale refresh_token,
	// and the next turn's RefreshToken() call gets refresh_token_reused.
	// By omitting refresh_token, the CLI uses the fresh access_token
	// (15-min TTL) as-is, and our server retains sole ownership of
	// the refresh_token for future turns.
	authJSON, err := json.Marshal(map[string]interface{}{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"access_token":  cfg.AccessToken,
			"refresh_token": "",
			"id_token":      cfg.IDToken,
		},
		"last_refresh": time.Now().Format(time.RFC3339),
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
	exitCode, err := o.provider.Exec(ctx, sandbox, mkdirCmd, &mkdirOut, &mkdirErr)
	if err != nil {
		return false, fmt.Errorf("create .codex dir: %w", err)
	}
	if exitCode != 0 {
		return false, fmt.Errorf("create .codex dir: mkdir exited with code %d: %s", exitCode, mkdirErr.String())
	}

	authPath := authDir + "/auth.json"
	if err := o.provider.WriteFile(ctx, sandbox, authPath, authJSON); err != nil {
		return false, fmt.Errorf("write auth.json: %w", err)
	}

	// Write config.toml to disable Codex's internal bwrap sandboxing.
	// The container is already isolated by Docker + gVisor so bwrap is
	// redundant and fails because gVisor doesn't support the unprivileged
	// user namespaces that bwrap requires.
	configTOML := []byte("sandbox_mode = \"danger-full-access\"\n")
	configPath := authDir + "/config.toml"
	if err := o.provider.WriteFile(ctx, sandbox, configPath, configTOML); err != nil {
		return false, fmt.Errorf("write config.toml: %w", err)
	}

	o.logger.Debug().
		Str("org_id", orgID.String()).
		Msg("injected codex auth.json and config.toml into sandbox")

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

// snapshotSession snapshots the sandbox workspace to object storage for multi-turn support.
// If snapshots are not configured, this is a no-op. This only saves the snapshot
// and updates sandbox state — it does NOT change session status or call UpdateTurnComplete.
func (o *Orchestrator) snapshotSession(ctx context.Context, session *models.Session, sandbox *Sandbox, result *AgentResult) (string, error) {
	if o.snapshots == nil {
		return "", nil
	}

	snapshotKey := fmt.Sprintf("snapshots/%s/%s/workspace.tar.zst", session.OrgID, session.ID)

	reader, err := o.provider.Snapshot(ctx, sandbox)
	if err != nil {
		return "", fmt.Errorf("snapshot sandbox: %w", err)
	}
	defer reader.Close()

	if err := o.snapshots.Save(ctx, snapshotKey, reader); err != nil {
		return "", fmt.Errorf("save snapshot to storage: %w", err)
	}

	// Store the snapshot key on the session for subsequent use.
	session.SnapshotKey = &snapshotKey

	return snapshotKey, nil
}

func (o *Orchestrator) isInteractiveSession(issue *models.Issue) bool {
	return issue != nil && issue.Source == models.IssueSourceManual && o.sessionMessages != nil && o.snapshots != nil
}

func (o *Orchestrator) createAssistantMessage(ctx context.Context, sessionID, orgID uuid.UUID, turnNumber int, result *AgentResult) error {
	if o.sessionMessages == nil {
		return nil
	}

	assistantMsg := &models.SessionMessage{
		SessionID:  sessionID,
		OrgID:      orgID,
		TurnNumber: turnNumber,
		Role:       models.MessageRoleAssistant,
		Content:    result.Summary,
	}
	if result.TokenUsage.InputTokens > 0 || result.TokenUsage.OutputTokens > 0 {
		tokenJSON, err := json.Marshal(result.TokenUsage)
		if err != nil {
			return fmt.Errorf("marshal token usage: %w", err)
		}
		assistantMsg.TokenUsage = tokenJSON
	}
	return o.sessionMessages.Create(ctx, assistantMsg)
}

// tokenLimitForMode returns the max token limit based on the session's token mode.
// Optional context limits from org settings override the defaults when provided.
func tokenLimitForMode(mode string, limits ...models.ContextLimits) int {
	var lowMax, highMax int
	if len(limits) > 0 && limits[0].AgentLowTokenMax > 0 {
		lowMax = limits[0].AgentLowTokenMax
	} else {
		lowMax = 50000
	}
	if len(limits) > 0 && limits[0].AgentHighTokenMax > 0 {
		highMax = limits[0].AgentHighTokenMax
	} else {
		highMax = 200000
	}

	switch mode {
	case "high":
		return highMax
	default:
		return lowMax
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

const maxBranchSlugLen = 60

var nonAlphanumRegexp = regexp.MustCompile(`[^a-z0-9]+`)

// slugifyForBranch converts a title into a lowercase, hyphen-separated slug
// suitable for use in a git branch name.
func slugifyForBranch(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumRegexp.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxBranchSlugLen {
		s = s[:maxBranchSlugLen]
		if i := strings.LastIndex(s, "-"); i > 0 {
			s = s[:i]
		}
	}
	return s
}

// formatWorkingBranch generates a branch name for an agent session.
// Format: 143-<short-id>-<slug> — short, flat, and descriptive.
func formatWorkingBranch(run *models.Session, issue *models.Issue) string {
	short := run.ID.String()[:8]

	// Prefer the session title (set for manual sessions) over the issue title.
	title := issue.Title
	if run.Title != nil && *run.Title != "" {
		title = *run.Title
	}

	slug := slugifyForBranch(title)
	if slug == "" {
		slug = "session"
	}

	return fmt.Sprintf("143-%s-%s", short, slug)
}

// shellEscapeSingleQuote escapes single quotes for safe use in shell commands.
func shellEscapeSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
