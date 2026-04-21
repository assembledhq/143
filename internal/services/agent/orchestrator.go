package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/assembledhq/143/internal/services/mcp"
	"github.com/assembledhq/143/internal/services/storage"
)

const (
	defaultMaxConcurrent = 10
)

// ErrConcurrencyLimit is returned when an org has reached its maximum
// number of concurrent agent runs. Callers can check for this with
// errors.Is to handle it as a transient/retryable condition.
var ErrConcurrencyLimit = fmt.Errorf("concurrency limit reached")

// ErrSessionTimedOut is returned from RunAgent / ContinueSession when the
// per-session wall-clock deadline fires. Callers can errors.Is against this
// to distinguish timeout failures from user cancellations and other errors
// without resorting to error-string matching.
var ErrSessionTimedOut = errors.New("session timed out")

// canonicalTimeoutLogMessage is the single log phrase emitted whenever a
// session hits its configured deadline. Kept deliberately narrow so
// Grafana alerts can key off one string across RunAgent and ContinueSession.
const canonicalTimeoutLogMessage = "session exceeded configured timeout"

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
	UpdateFailure(ctx context.Context, orgID, runID uuid.UUID, explanation, category string, nextSteps []string, retryAdvised bool) error
	UpdateTitle(ctx context.Context, orgID, sessionID uuid.UUID, title string) error
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	// AcquireTurnHold flips turn_holding_container=TRUE and publishes the
	// turn's proposed container_id via COALESCE. The returned
	// actualContainerID is the container_id now stored on the row: equal to
	// proposedContainerID when the caller won the race, different when a
	// concurrent preview-hydrate published first. In the latter case the
	// caller must destroy its just-created sandbox and attach to the
	// actualContainerID instead.
	AcquireTurnHold(ctx context.Context, orgID, sessionID uuid.UUID, proposedContainerID string) (actualContainerID string, err error)
	// ReleaseTurnHold flips turn_holding_container=FALSE and reports whether
	// the caller should destroy the container. destroyNow is true only when no
	// preview is holding the container; otherwise the preview keeps it alive
	// for the "iterate between turns" flow.
	ReleaseTurnHold(ctx context.Context, orgID, sessionID uuid.UUID) (destroyNow bool, containerID string, err error)
	// FinalizeContainerDestroy is the TOCTOU-safe companion to
	// ReleaseTurnHold: it atomically clears container_id and marks
	// sandbox_state='snapshotted' only when no holder has come back and the
	// container_id still matches expectedContainerID. Returns true when the
	// caller owns the destroy, false when a new holder acquired in the gap
	// (caller must leave the container alone).
	FinalizeContainerDestroy(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (cleared bool, err error)
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

// UsageRecorder tracks container lifecycle events for billing.
type UsageRecorder interface {
	ContainerStarted(ctx context.Context, orgID, sessionID uuid.UUID, sandbox *Sandbox, cfg SandboxConfig, startedAt time.Time) uuid.UUID
	ContainerStopped(ctx context.Context, orgID, sessionID uuid.UUID, eventID uuid.UUID, startedAt time.Time, exitReason string)
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

// AutomationRunUpdater is called after an agent run completes to bubble the
// session's terminal status back to the owning automation_runs row. Mirrors
// ProjectTaskUpdater: one hook per owning-entity kind, invoked at both the
// success and failure paths so the run's completed_at + result_summary stay
// consistent with whatever the orchestrator persisted to the session.
type AutomationRunUpdater interface {
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
	projectTasks      ProjectTaskUpdater   // can be nil
	automationRuns    AutomationRunUpdater // can be nil
	issues            IssueStore
	repositories      RepositoryStore
	orgs              OrgStore
	jobs              JobStore
	github            GitHubTokenProvider
	codexAuth         CodexAuthProvider // can be nil
	credentials       CredentialProvider
	memory            MemoryService          // can be nil
	userCredentials   UserCredentialProvider // can be nil
	snapshots         storage.SnapshotStore  // can be nil — multi-turn disabled if nil
	usageTracker      UsageRecorder          // can be nil — billing tracking disabled if nil
	orgSettingsCache  *OrgSettingsCache      // can be nil — disables Amp/Pi agent_config caching
	logger            zerolog.Logger
	maxConcurrent     int
	cancels           *CancelRegistry
}

// OrchestratorConfig holds the dependencies for creating an Orchestrator.
type OrchestratorConfig struct {
	Provider         SandboxProvider
	Adapters         map[models.AgentType]AgentAdapter
	Sessions         SessionStore
	SessionLogs      SessionLogStore
	SessionQuestions SessionQuestionStore
	SessionMessages  SessionMessageStore
	DecisionLog      DecisionLogStore
	ProjectTasks     ProjectTaskUpdater   // optional — updates project tasks on run completion
	AutomationRuns   AutomationRunUpdater // optional — updates automation_runs on session completion
	Issues           IssueStore
	Repositories     RepositoryStore
	Orgs             OrgStore
	Jobs             JobStore
	GitHub           GitHubTokenProvider
	CodexAuth        CodexAuthProvider // optional — enables ChatGPT OAuth for Codex agent
	Credentials      CredentialProvider
	Memory           MemoryService          // optional — injects learned memories into agent prompts
	UserCredentials  UserCredentialProvider // optional — enables personal/team credential resolution
	Snapshots        storage.SnapshotStore  // optional — enables multi-turn snapshot/restore
	UsageTracker     UsageRecorder          // optional — enables billing observability
	Cancels          *CancelRegistry        // optional — enables session cancellation from API
	OrgSettingsCache *OrgSettingsCache      // optional — caches Amp/Pi agent_config lookups across session starts
	Logger           zerolog.Logger
	MaxConcurrent    int
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
		automationRuns:    cfg.AutomationRuns,
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
		usageTracker:      cfg.UsageTracker,
		orgSettingsCache:  cfg.OrgSettingsCache,
		cancels:           cfg.Cancels,
		logger:            cfg.Logger,
		maxConcurrent:     maxConcurrent,
	}
}

// RunAgent is the main entry point. It executes an agent run end-to-end:
// concurrency check → sandbox creation → repo clone → agent execution →
// result handling → follow-up job enqueuing → sandbox cleanup.
func (o *Orchestrator) RunAgent(ctx context.Context, run *models.Session) error {
	// Create a cancellable context. The cancel registry is populated later
	// once the sandbox is available, so CancelSession can send SIGINT.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if o.cancels != nil {
		defer o.cancels.Deregister(run.ID)
	}

	log := o.logger.With().
		Str("session_id", run.ID.String()).
		Str("org_id", run.OrgID.String()).
		Str("issue_id", run.IssueID.String()).
		Logger()

	// 1. Concurrency check.
	if err := o.checkConcurrency(ctx, run.OrgID); err != nil {
		log.Info().Err(err).Msg("concurrency limit reached, run stays pending")
		return err
	}

	// 2. Update status to "running" (sets started_at in DB). We also capture
	// the start time locally so the timeout branch below can log a
	// meaningful elapsed duration regardless of whether run.StartedAt was
	// populated by the caller.
	if err := o.sessions.UpdateStatus(ctx, run.OrgID, run.ID, "running"); err != nil {
		return fmt.Errorf("update run status to running: %w", err)
	}
	// runStartedAt is captured AFTER UpdateStatus, so the elapsed reported on
	// a timeout excludes concurrency check + status write but includes
	// everything from issue fetch onward (sandbox create, credential inject,
	// agent execute, snapshot). An on-call reader seeing a sub-minute
	// elapsed on a 25-minute timeout should suspect the deadline fired
	// during sandbox creation, not the adapter.
	runStartedAt := time.Now()

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
	input.IntegrationSkills = o.BuildIntegrationSkills(ctx, run.OrgID)

	prompt, err := adapter.PreparePrompt(ctx, input)
	if err != nil {
		o.failRun(ctx, run, fmt.Sprintf("prepare prompt: %s", err))
		return fmt.Errorf("prepare prompt: %w", err)
	}

	// 7. Create sandbox with agent-specific env vars (API keys).
	// Start with server-level defaults, then overlay org-level overrides.
	sandboxCfg := DefaultSandboxConfig()
	sandboxCfg.SessionID = run.ID.String()
	sandboxCfg.OrgID = run.OrgID.String()
	sandboxCfg.Purpose = "agent_run"
	sandboxCfg.Env = o.resolveAgentEnv(ctx, run.OrgID, run.AgentType, run.TriggeredByUserID)
	if sandboxCfg.Env == nil {
		sandboxCfg.Env = make(map[string]string)
	}
	// Apply per-run model override before the auth pre-flight: for Pi the
	// required provider key is derived from the resolved model, so checking
	// auth against the agent_config default would let an OpenAI override
	// past the gate on an Anthropic-only org.
	if run.ModelOverride != nil && *run.ModelOverride != "" {
		if envVar := models.ModelEnvVarForAgentType(run.AgentType); envVar != "" {
			sandboxCfg.Env[envVar] = *run.ModelOverride
		}
	}
	// For Pi, drop any inherited provider keys that don't match the effective
	// model so the sandbox only sees the single credential Pi will actually
	// use. Runs after ModelOverride so a per-run switch shapes the env too.
	if run.AgentType == models.AgentTypePi {
		if unknownPrefix := narrowPiProviderKeys(sandboxCfg.Env); unknownPrefix != "" {
			log.Warn().
				Str("provider_prefix", unknownPrefix).
				Str("model", piResolvedModel(sandboxCfg.Env)).
				Msg("pi: unrecognized provider prefix, exporting all inherited provider keys to sandbox")
		}
	}
	if err := o.checkAgentAuth(run.AgentType, sandboxCfg.Env); err != nil {
		o.failRun(ctx, run, err.Error())
		return err
	}
	// Route the sandbox workdir into a repo-named subdir of HomeDir so the
	// agent's cwd reads like `/home/sandbox/<repo>` — matching what a human
	// would see after `git clone && cd <repo>`. Falls back to the default
	// (/workspace) when no repo is attached.
	if slug := SlugForRepo(repoFullName); slug != "" {
		sandboxCfg.WorkDir = sandboxCfg.HomeDir + "/" + slug
	}
	// Inject GitHub token and repo info for 143-tools CLI only when the agent
	// has integration skills (i.e. the prompt references CLI tools). This avoids
	// giving every agent session GitHub write access unnecessarily.
	if input.IntegrationSkills != "" && token != "" {
		sandboxCfg.Env["GITHUB_TOKEN"] = token
		if repoFullName != "" {
			parts := strings.SplitN(repoFullName, "/", 2)
			if len(parts) == 2 {
				sandboxCfg.Env["GITHUB_REPO_OWNER"] = parts[0]
				sandboxCfg.Env["GITHUB_REPO_NAME"] = parts[1]
			}
		}
	}
	if _, ok := sandboxCfg.Env["HOME"]; !ok {
		sandboxCfg.Env["HOME"] = sandboxCfg.HomeDir
	}
	sandbox, err := o.provider.Create(ctx, sandboxCfg)
	if err != nil {
		o.failRun(ctx, run, fmt.Sprintf("create sandbox: %s", err))
		return fmt.Errorf("create sandbox: %w", err)
	}
	containerStartedAt := time.Now()
	var usageEventID uuid.UUID
	if o.usageTracker != nil {
		usageEventID = o.usageTracker.ContainerStarted(ctx, run.OrgID, run.ID, sandbox, sandboxCfg, containerStartedAt)
	}
	// Record the turn hold so a concurrent StartPreview can attach to this
	// container (same ID, same filesystem) instead of hydrating a duplicate.
	// AcquireTurnHold uses COALESCE to publish only if no one has raced ahead;
	// if a concurrent preview hydrate won, actualID will differ from our
	// sandbox.ID and we must drop the local container we just created.
	// On DB error we degrade to a safe "reconciler will clean up" state rather
	// than failing the run.
	actualContainerID, holdErr := o.sessions.AcquireTurnHold(ctx, run.OrgID, run.ID, sandbox.ID)
	if holdErr != nil {
		log.Warn().Err(holdErr).Msg("failed to persist turn hold on sandbox; preview coexistence disabled for this turn")
	} else if actualContainerID != "" && actualContainerID != sandbox.ID {
		destroyCtx := context.Background()
		if destroyErr := o.provider.Destroy(destroyCtx, sandbox); destroyErr != nil {
			log.Error().Err(destroyErr).Str("losing_container_id", sandbox.ID).Msg("failed to destroy sandbox after losing hydrate race")
		}
		log.Warn().
			Str("winning_container_id", actualContainerID).
			Str("losing_container_id", sandbox.ID).
			Msg("another holder published container_id first; aborting turn so the user can retry against the winning container")
		o.failRun(ctx, run, "sandbox race: another holder attached first, please retry")
		return fmt.Errorf("sandbox race: actual container %s != created %s", actualContainerID, sandbox.ID)
	}
	defer func() {
		exitReason := containerExitReason(ctx, err)
		if o.usageTracker != nil {
			// Use a detached context so the billing write succeeds even if
			// the parent ctx was cancelled (timeout, shutdown).
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopCancel()
			o.usageTracker.ContainerStopped(stopCtx, run.OrgID, run.ID, usageEventID, containerStartedAt, exitReason)
		}
		// Use a background context for cleanup since the run context may be cancelled.
		destroyCtx := context.Background()
		destroyNow, releasedID, releaseErr := o.sessions.ReleaseTurnHold(destroyCtx, run.OrgID, run.ID)
		if releaseErr != nil {
			// Fall back to destroy to avoid leaking the container if we
			// can't read the holder state.
			log.Warn().Err(releaseErr).Msg("failed to release turn hold; destroying container anyway")
			destroyNow = true
		}
		if !destroyNow {
			log.Info().Str("container_id", sandbox.ID).Msg("preview is holding the sandbox container; leaving it alive")
			return
		}
		// FinalizeContainerDestroy re-checks holder state atomically and only
		// clears container_id when it still matches the expected ID with no
		// holder active. This closes the window between the release above and
		// the physical destroy below where a new holder could have acquired.
		// Order: clear container_id FIRST (via the CAS) then destroy, so new
		// reuse-path readers hydrate fresh rather than attach to a dying ID.
		expectedID := releasedID
		if expectedID == "" {
			expectedID = sandbox.ID
		}
		cleared, finalizeErr := o.sessions.FinalizeContainerDestroy(destroyCtx, run.OrgID, run.ID, expectedID)
		if finalizeErr != nil {
			log.Warn().Err(finalizeErr).Msg("failed to finalize container destroy; skipping destroy to avoid dropping a live holder's container")
			return
		}
		if !cleared {
			log.Info().Str("container_id", sandbox.ID).Msg("another holder acquired between release and destroy; leaving container alive")
			return
		}
		if destroyErr := o.provider.Destroy(destroyCtx, sandbox); destroyErr != nil {
			log.Error().Err(destroyErr).Msg("failed to destroy sandbox")
		}
	}()

	// Register sandbox with cancel registry so CancelSession can send SIGINT.
	if o.cancels != nil {
		o.cancels.Register(run.ID, sandbox, o.provider, cancel)
	}

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
		if err := o.ensureCodexAuth(ctx, run, sandbox); err != nil {
			return err
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

	// 10b. Retry once on token expiration for Codex agents.
	result, err = o.retryOnTokenExpired(ctx, run.AgentType, run.OrgID, run.ID, run.CurrentTurn, sandbox, adapter, execCtx, prompt, result, err, log)

	// 11. Handle result.
	wasCancelled := o.cancels != nil && o.cancels.WasCancelled(run.ID)

	if err != nil {
		// Distinguish three cases:
		//   1. User cancellation (wasCancelled or ctx.Err()==Canceled) —
		//      snapshot and return to idle so the session can be continued.
		//      Checked first so an explicit user cancel that races the
		//      deadline is classified as a cancel, not a timeout.
		//   2. context.DeadlineExceeded — session hit its wall-clock limit.
		//      Classify explicitly via failTimedOutSession so the category
		//      is set without relying on text-matching in classifyFailure.
		//   3. Any other error — fail with the underlying message and defer
		//      classification to the async analyze_failure job.
		if wasCancelled || errors.Is(ctx.Err(), context.Canceled) {
			log.Info().Msg("session cancelled by user")
			o.handleCancelledSession(run, sandbox, result, run.CurrentTurn+1, log)
			return fmt.Errorf("session cancelled: %w", ctx.Err())
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			elapsed := time.Since(runStartedAt).Round(time.Second)
			o.failTimedOutSession(run, elapsed, 0, err, log)
			return fmt.Errorf("%w after %s: %w", ErrSessionTimedOut, elapsed, err)
		}
		o.failRun(ctx, run, err.Error())
		o.enqueueJob(ctx, run.OrgID, "agent", "analyze_failure", map[string]interface{}{
			"session_id": run.ID.String(),
			"org_id":     run.OrgID.String(),
		})
		return fmt.Errorf("execute agent: %w", err)
	}

	// 11a. If the agent exited after receiving SIGINT (user cancel), snapshot
	// the workspace and return the session to idle so it can be continued.
	if wasCancelled {
		log.Info().Msg("agent exited after cancel, snapshotting and returning to idle")
		o.handleCancelledSession(run, sandbox, result, run.CurrentTurn+1, log)
		return nil
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

	// 14. Bubble session completion to the owning automation_run, if any.
	if run.AutomationRunID != nil && o.automationRuns != nil {
		if err := o.automationRuns.OnSessionComplete(ctx, run, status); err != nil {
			o.logger.Warn().Err(err).Str("run_id", run.ID.String()).Msg("failed to update automation run on session completion")
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
	// Create a cancellable context. The cancel registry is populated later
	// once the sandbox is available, so CancelSession can send SIGINT.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if o.cancels != nil {
		defer o.cancels.Deregister(session.ID)
	}

	log := o.logger.With().
		Str("session_id", session.ID.String()).
		Str("org_id", session.OrgID.String()).
		Int("turn", session.CurrentTurn).
		Logger()

	// Determine whether we can restore from a snapshot or need a fresh start.
	hasSnapshot := session.SnapshotKey != nil && *session.SnapshotKey != "" &&
		o.snapshots != nil &&
		session.SandboxState != string(models.SandboxStateDestroyed)

	// 1. Update status to running. Capture wall-clock start locally so the
	// timeout branch below can log a meaningful elapsed regardless of
	// whether session.StartedAt is populated (it's set from the first
	// turn; on a later turn this captures THIS turn's elapsed).
	if err := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, "running"); err != nil {
		return fmt.Errorf("update session status to running: %w", err)
	}
	// turnStartedAt scopes elapsed to THIS turn only — it excludes any time
	// the session spent idle between turns, and excludes the status write
	// above. It includes snapshot restore, sandbox create, and agent
	// execute. See runStartedAt in RunAgent for analogous semantics.
	turnStartedAt := time.Now()
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
	var planMode bool
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

	// Detect plan mode prefix and strip it, wrapping with plan instructions.
	const planModePrefix = "[PLAN_MODE]\n"
	if strings.HasPrefix(userMessage, planModePrefix) {
		planMode = true
		originalMessage := strings.TrimPrefix(userMessage, planModePrefix)
		userMessage = "You are in PLAN MODE. Instead of making changes directly, create a detailed implementation plan for the following request. Describe:\n" +
			"1. What files need to be changed and why\n" +
			"2. What specific changes are needed in each file\n" +
			"3. The order of operations\n" +
			"4. Any potential risks or considerations\n\n" +
			"Do NOT make any file changes or use any tools that modify files. Only output the plan as a structured markdown response. " +
			"The user will review the plan and either approve it or request adjustments before you proceed.\n\n" +
			"User's request:\n" + originalMessage
	}
	_ = planMode // used by adapters that support explicit plan mode

	// 4. Create sandbox.
	sandboxCfg := DefaultSandboxConfig()
	sandboxCfg.SessionID = session.ID.String()
	sandboxCfg.OrgID = session.OrgID.String()
	sandboxCfg.Purpose = "continue_session"
	sandboxCfg.Env = o.resolveAgentEnv(ctx, session.OrgID, session.AgentType, session.TriggeredByUserID)
	if sandboxCfg.Env == nil {
		sandboxCfg.Env = make(map[string]string)
	}
	// Apply the per-session model override before checkAgentAuth so the
	// pre-flight evaluates the *effective* model — see the matching block in
	// RunAgent for the Pi-specific reasoning.
	if session.ModelOverride != nil && *session.ModelOverride != "" {
		if envVar := models.ModelEnvVarForAgentType(session.AgentType); envVar != "" {
			sandboxCfg.Env[envVar] = *session.ModelOverride
		}
	}
	if session.AgentType == models.AgentTypePi {
		if unknownPrefix := narrowPiProviderKeys(sandboxCfg.Env); unknownPrefix != "" {
			log.Warn().
				Str("provider_prefix", unknownPrefix).
				Str("model", piResolvedModel(sandboxCfg.Env)).
				Msg("pi: unrecognized provider prefix, exporting all inherited provider keys to sandbox")
		}
	}
	if authErr := o.checkAgentAuth(session.AgentType, sandboxCfg.Env); authErr != nil {
		log.Error().Err(authErr).Msg("agent auth pre-flight failed during continue_session")
		if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
			log.Error().Err(revertErr).Msg("failed to revert session to idle after auth pre-flight failure")
		}
		if o.sessionMessages != nil {
			errMsg := &models.SessionMessage{
				SessionID:  session.ID,
				OrgID:      session.OrgID,
				TurnNumber: session.CurrentTurn + 1,
				Role:       models.MessageRoleAssistant,
				Content:    authErr.Error(),
			}
			if createErr := o.sessionMessages.Create(ctx, errMsg); createErr != nil {
				log.Error().Err(createErr).Msg("failed to create error message for auth pre-flight failure")
			}
		}
		return authErr
	}
	// Look up the session's repo to derive the same WorkDir used on the
	// initial run (see RunAgent). This must match the original: the container
	// WorkingDir and HOME are driven off WorkDir, and the snapshot tar restores
	// files at the original absolute paths, so drifting here leaves the agent
	// running in an empty /workspace while the checkout sits elsewhere.
	// Treat a lookup failure as fatal — revert to idle so the user can retry.
	slug, slugErr := o.sessionRepoSlug(ctx, session)
	if slugErr != nil {
		log.Error().Err(slugErr).Msg("sandbox workdir resolution failed during continue_session")
		if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
			log.Error().Err(revertErr).Msg("failed to revert session to idle after workdir resolution failure")
		}
		if revertErr := o.sessions.UpdateSandboxState(ctx, session.OrgID, session.ID, string(models.SandboxStateSnapshotted)); revertErr != nil {
			log.Warn().Err(revertErr).Msg("failed to revert sandbox state after workdir resolution failure")
		}
		o.registerSandboxFailureMessage(
			ctx,
			session,
			fmt.Sprintf("Failed to resolve the sandbox workspace: %s\n\nPlease try again in a moment.", slugErr),
			"workdir resolution",
		)
		return fmt.Errorf("resolve workdir: %w", slugErr)
	}
	if slug != "" {
		sandboxCfg.WorkDir = sandboxCfg.HomeDir + "/" + slug
	}
	if _, ok := sandboxCfg.Env["HOME"]; !ok {
		sandboxCfg.Env["HOME"] = sandboxCfg.HomeDir
	}

	// Determine sandbox strategy:
	//   - Reuse: a preview already hydrated the container; attach to it by ID
	//     and skip both Create and Restore.
	//   - Hydrate: a snapshot exists; create a new container and restore the
	//     snapshot via the shared HydrateSandboxFromSnapshot helper.
	//   - Fresh: no snapshot; create a clean container and clone fresh below.
	var sandbox *Sandbox
	reusedExisting := session.ContainerID != nil && *session.ContainerID != ""
	switch {
	case reusedExisting:
		sandbox = &Sandbox{
			ID:        *session.ContainerID,
			Provider:  "docker",
			WorkDir:   sandboxCfg.WorkDir,
			HomeDir:   sandboxCfg.HomeDir,
			SessionID: sandboxCfg.SessionID,
			OrgID:     sandboxCfg.OrgID,
			Purpose:   sandboxCfg.Purpose,
		}
		log.Info().Str("container_id", sandbox.ID).Msg("reusing existing sandbox container (preview is holding it)")
	case hasSnapshot:
		sandbox, err = HydrateSandboxFromSnapshot(ctx, o.provider, o.snapshots, *session.SnapshotKey, sandboxCfg)
		if err != nil {
			log.Error().Err(err).Msg("sandbox hydrate failed during continue_session")
			if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
				log.Error().Err(revertErr).Msg("failed to revert session to idle after hydrate failure")
			}
			if revertErr := o.sessions.UpdateSandboxState(ctx, session.OrgID, session.ID, string(models.SandboxStateSnapshotted)); revertErr != nil {
				log.Warn().Err(revertErr).Msg("failed to revert sandbox state after hydrate failure")
			}
			o.registerSandboxFailureMessage(
				ctx,
				session,
				fmt.Sprintf("Failed to restore the sandbox environment: %s\n\nPlease try again in a moment.", err),
				"sandbox hydrate",
			)
			return fmt.Errorf("hydrate sandbox: %w", err)
		}
	default:
		sandbox, err = o.provider.Create(ctx, sandboxCfg)
		if err != nil {
			log.Error().Err(err).Msg("sandbox creation failed during continue_session")
			if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
				log.Error().Err(revertErr).Msg("failed to revert session to idle after sandbox failure")
			}
			if revertErr := o.sessions.UpdateSandboxState(ctx, session.OrgID, session.ID, string(models.SandboxStateSnapshotted)); revertErr != nil {
				log.Warn().Err(revertErr).Msg("failed to revert sandbox state after sandbox failure")
			}
			o.registerSandboxFailureMessage(
				ctx,
				session,
				fmt.Sprintf("Failed to start the sandbox environment: %s\n\nPlease try again in a moment. If this persists, check that Docker is running.", err),
				"sandbox creation",
			)
			return fmt.Errorf("create sandbox: %w", err)
		}
	}
	// Record the turn hold. AcquireTurnHold uses COALESCE so it is idempotent
	// when we reused a container (the row's container_id already matches our
	// sandbox.ID). When we freshly hydrated, a concurrent preview hydrate may
	// have published first — in that case actualContainerID differs from
	// sandbox.ID and we must destroy our local container and abort so the
	// user's retry picks up the winner via the reuse path.
	// Log-and-continue on DB error so losing the hold degrades to a safe
	// "reconciler will clean up" state rather than failing the turn.
	actualContainerID, holdErr := o.sessions.AcquireTurnHold(ctx, session.OrgID, session.ID, sandbox.ID)
	if holdErr != nil {
		log.Warn().Err(holdErr).Msg("failed to persist turn hold on sandbox; preview coexistence disabled for this turn")
	} else if actualContainerID != "" && actualContainerID != sandbox.ID {
		destroyCtx := context.Background()
		// Only destroy the locally-created container — reused containers came
		// from the row's existing container_id and should never be torn down
		// here, but reusedExisting implies sandbox.ID == *session.ContainerID,
		// and if that differs from actualContainerID the row was rewritten
		// under us (e.g. a preview hydrate replaced the reused ID). Either
		// way, destroying the sandbox we hold is safe: for reuse the
		// "destroy" is really "release our handle" since we didn't create it.
		if !reusedExisting {
			if destroyErr := o.provider.Destroy(destroyCtx, sandbox); destroyErr != nil {
				log.Error().Err(destroyErr).Str("losing_container_id", sandbox.ID).Msg("failed to destroy sandbox after losing hydrate race")
			}
		}
		log.Warn().
			Str("winning_container_id", actualContainerID).
			Str("losing_container_id", sandbox.ID).
			Msg("another holder published container_id first; aborting turn so the user can retry against the winning container")
		if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
			log.Error().Err(revertErr).Msg("failed to revert session to idle after losing hydrate race")
		}
		o.registerSandboxFailureMessage(
			ctx,
			session,
			"Another session activity was starting at the same time. Please try again.",
			"sandbox race",
		)
		return fmt.Errorf("sandbox race: actual container %s != local %s", actualContainerID, sandbox.ID)
	}
	containerStartedAt := time.Now()
	var usageEventID uuid.UUID
	if o.usageTracker != nil {
		usageEventID = o.usageTracker.ContainerStarted(ctx, session.OrgID, session.ID, sandbox, sandboxCfg, containerStartedAt)
	}
	defer func() {
		exitReason := containerExitReason(ctx, err)
		if o.usageTracker != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopCancel()
			o.usageTracker.ContainerStopped(stopCtx, session.OrgID, session.ID, usageEventID, containerStartedAt, exitReason)
		}
		// Detached context so DB writes + destroy succeed even if ctx was
		// cancelled (user cancel, timeout, shutdown).
		destroyCtx := context.Background()
		destroyNow, releasedID, releaseErr := o.sessions.ReleaseTurnHold(destroyCtx, session.OrgID, session.ID)
		if releaseErr != nil {
			log.Warn().Err(releaseErr).Msg("failed to release turn hold; destroying container anyway")
			destroyNow = true
		}
		if !destroyNow {
			log.Info().Str("container_id", sandbox.ID).Msg("preview is holding the sandbox container; leaving it alive for the preview")
			return
		}
		// FinalizeContainerDestroy re-checks holder state atomically: if a
		// preview or another turn acquired between our release and this
		// destroy, the CAS matches zero rows and we skip destroy so the new
		// holder's container isn't ripped out from under it.
		expectedID := releasedID
		if expectedID == "" {
			expectedID = sandbox.ID
		}
		cleared, finalizeErr := o.sessions.FinalizeContainerDestroy(destroyCtx, session.OrgID, session.ID, expectedID)
		if finalizeErr != nil {
			log.Warn().Err(finalizeErr).Msg("failed to finalize container destroy; skipping destroy to avoid dropping a live holder's container")
			return
		}
		if !cleared {
			log.Info().Str("container_id", sandbox.ID).Msg("another holder acquired between release and destroy; leaving container alive")
			return
		}
		if destroyErr := o.provider.Destroy(destroyCtx, sandbox); destroyErr != nil {
			log.Error().Err(destroyErr).Msg("failed to destroy sandbox")
		}
	}()

	// Register sandbox with cancel registry so CancelSession can send SIGINT.
	if o.cancels != nil {
		o.cancels.Register(session.ID, sandbox, o.provider, cancel)
	}

	// 5. Set up the workspace. Three paths:
	//   - Reuse: the container is already live (preview hydrated it); just
	//     re-inject Codex auth and build the resume prompt.
	//   - Hydrate: HydrateSandboxFromSnapshot already did Create+Restore;
	//     re-inject Codex auth and build the resume prompt.
	//   - Fresh: no snapshot; clone repo fresh and build a reconstructed
	//     prompt from the conversation history + stored diff.
	var prompt *AgentPrompt
	if reusedExisting || hasSnapshot {
		// Re-inject Codex auth.json. Cheap, and catches the case where the
		// file was cleared or drifted while the container was idle (or where
		// the preview created the container without agent credentials).
		if session.AgentType == models.AgentTypeCodex {
			if err := o.ensureCodexAuth(ctx, session, sandbox); err != nil {
				return err
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

		if reusedExisting {
			log.Info().Msg("continuing session in reused sandbox (preview holds container)")
		} else {
			log.Info().Msg("continuing session with snapshot restore")
		}
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
		input.IntegrationSkills = o.BuildIntegrationSkills(ctx, session.OrgID)
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

	// 6b. Retry once on token expiration for Codex agents.
	result, err = o.retryOnTokenExpired(ctx, session.AgentType, session.OrgID, session.ID, turnNumber, sandbox, adapter, execCtx, prompt, result, err, log)

	wasCancelled := o.cancels != nil && o.cancels.WasCancelled(session.ID)

	if err != nil {
		// User cancel is checked first so an explicit cancel that races the
		// deadline is classified as a cancel, not a timeout.
		if wasCancelled || errors.Is(ctx.Err(), context.Canceled) {
			log.Info().Msg("session cancelled by user during continue")
			o.handleCancelledSession(session, sandbox, result, turnNumber, log)
			return fmt.Errorf("session cancelled: %w", ctx.Err())
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			elapsed := time.Since(turnStartedAt).Round(time.Second)
			o.failTimedOutSession(session, elapsed, turnNumber, err, log)
			return fmt.Errorf("%w on turn %d after %s: %w", ErrSessionTimedOut, turnNumber, elapsed, err)
		}
		o.failRun(ctx, session, err.Error())
		return fmt.Errorf("execute agent on continue: %w", err)
	}

	// 6c. If cancelled but agent exited gracefully, snapshot and return to idle.
	if wasCancelled {
		log.Info().Msg("agent exited after cancel during continue, returning to idle")
		o.handleCancelledSession(session, sandbox, result, turnNumber, log)
		return nil
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

// sessionRepoSlug returns the repo-name slug for the session's repository. The
// returned slug drives WorkDir selection on resume and MUST match what RunAgent
// chose originally, or the container's WorkingDir/HOME diverge from where the
// snapshot tar restored the repo checkout. An empty slug with nil error means
// "no repo is attached" (legitimate, falls back to default WorkDir). Any lookup
// failure is returned as an error so the caller can surface it rather than
// silently diverging.
func (o *Orchestrator) sessionRepoSlug(ctx context.Context, session *models.Session) (string, error) {
	repoID := session.RepositoryID
	if repoID == nil {
		issue, err := o.issues.GetByID(ctx, session.OrgID, session.IssueID)
		if err != nil {
			return "", fmt.Errorf("fetch issue for session repo lookup: %w", err)
		}
		if issue.RepositoryID == nil {
			return "", nil
		}
		repoID = issue.RepositoryID
	}
	repo, err := o.repositories.GetByID(ctx, session.OrgID, *repoID)
	if err != nil {
		return "", fmt.Errorf("fetch repo for session workdir: %w", err)
	}
	return SlugForRepo(repo.FullName), nil
}

// registerSandboxFailureMessage queues a user-visible assistant message
// to post only if the current continue_session job is dead-lettered —
// posting inline would fire once per retry. Direct callers without a
// jobctx registry drop the hook and must handle the returned error
// themselves. stage labels the warn log emitted if the DB insert fails.
func (o *Orchestrator) registerSandboxFailureMessage(ctx context.Context, session *models.Session, content, stage string) {
	if o.sessionMessages == nil {
		return
	}
	sessionMessages := o.sessionMessages
	sessionID := session.ID
	orgID := session.OrgID
	turnNumber := session.CurrentTurn + 1
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, _ error) {
		errMsg := &models.SessionMessage{
			SessionID:  sessionID,
			OrgID:      orgID,
			TurnNumber: turnNumber,
			Role:       models.MessageRoleAssistant,
			Content:    content,
		}
		// The retryable-timeout dead-letter path fires this hook with a
		// handler context whose deadline has just expired; detach so the
		// terminal-message write isn't racing the very timeout that
		// triggered it, but keep a short bound so we can't hang the poll
		// goroutine on a wedged DB.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 5*time.Second)
		defer cancel()
		if createErr := sessionMessages.Create(writeCtx, errMsg); createErr != nil {
			o.logger.Error().
				Err(createErr).
				Str("session_id", sessionID.String()).
				Str("org_id", orgID.String()).
				Str("stage", stage).
				Msg("failed to create dead-letter error message")
		}
	})
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
		return fmt.Errorf("%w: %d/%d runs active", ErrConcurrencyLimit, count, o.maxConcurrent)
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
			OrgID:      orgID,
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
	if run.AutomationRunID != nil && o.automationRuns != nil {
		if err := o.automationRuns.OnSessionComplete(ctx, run, "failed"); err != nil {
			o.logger.Warn().Err(err).Str("run_id", run.ID.String()).Msg("failed to update automation run on session failure")
		}
	}
}

// failRunWithCategory marks a run as failed with a structured failure category,
// explanation, and next steps. Used for well-known failure modes (e.g. auth expiry)
// where we can provide actionable guidance in the UI.
func (o *Orchestrator) failRunWithCategory(ctx context.Context, run *models.Session, errMsg, category, explanation string, nextSteps []string) {
	o.failRun(ctx, run, errMsg)
	if err := o.sessions.UpdateFailure(ctx, run.OrgID, run.ID, explanation, category, nextSteps, true); err != nil {
		o.logger.Error().Err(err).Str("run_id", run.ID.String()).Msg("failed to update run failure details")
	}
}

// failTimedOutSession handles the common bookkeeping for a session that hit
// its wall-clock deadline: structured failure persisted via
// failRunWithCategory (with FailureCategoryTimeout so no downstream text
// classifier is involved), and a canonical log line for Grafana alerts.
// Uses a fresh cleanup context because the caller's ctx has already
// expired. Pass turnNumber=0 for initial runs; any other value is treated
// as a continue-session turn and surfaced in the user-facing error text.
// underlyingErr is the error returned by the adapter/exec path (often
// context.DeadlineExceeded, sometimes a wrapped Docker/exec error) and is
// attached to the log event so on-call can tell at a glance whether the
// deadline tripped the adapter or something downstream.
// retryAdvised is hard-coded true inside failRunWithCategory; the default
// fits the "transient slowness" case and we accept the small false-positive
// rate where a session is structurally too large to ever fit.
func (o *Orchestrator) failTimedOutSession(run *models.Session, elapsed time.Duration, turnNumber int, underlyingErr error, log zerolog.Logger) {
	event := log.Error().Err(underlyingErr).Dur("elapsed", elapsed)
	if turnNumber > 0 {
		event = event.Int("turn", turnNumber)
	}
	event.Msg(canonicalTimeoutLogMessage)

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanupCancel()

	// Sub-second elapsed almost always means the handler ctx was already
	// expired on entry (e.g. a retry picked up a job whose budget had
	// already been spent). Saying "after 0s" reads as a bug report; frame
	// it as "before the run could start" so the user-visible text matches
	// reality.
	elapsedDesc := fmt.Sprintf("after %s of execution", elapsed)
	if elapsed < time.Second {
		elapsedDesc = "before the run could meaningfully start (the context deadline had already passed when the orchestrator picked up the job)"
	}

	var errMsg, explanation string
	if turnNumber > 0 {
		errMsg = fmt.Sprintf("Session timed out %s on turn %d. Raise max_session_duration_seconds in org settings or break the remaining work into a shorter follow-up turn.", elapsedDesc, turnNumber)
		// Continue-session may still have a prior snapshot that is usable
		// for a follow-up — don't claim the session's state is gone.
		explanation = "This turn hit the configured wall-clock limit before the agent finished. Work done during this turn was not snapshotted, but the prior turn's snapshot (if any) is still available for a follow-up."
	} else {
		errMsg = fmt.Sprintf("Session timed out %s. Raise max_session_duration_seconds in org settings or split the task into smaller sub-tasks.", elapsedDesc)
		explanation = "The session hit its configured wall-clock limit before the agent could finish. Any work committed inside the sandbox during this run was discarded — no snapshot was taken."
	}
	o.failRunWithCategory(cleanupCtx, run,
		errMsg,
		FailureCategoryTimeout,
		explanation,
		[]string{
			"Raise Max session duration on the Coding agents settings page if these runs legitimately need more time",
			"Split the task into smaller sub-tasks so each fits inside the limit",
			"Retry the session — transient slowness (LLM latency, git clone) may have pushed the run over the edge",
		},
	)
}

// ensureCodexAuth injects Codex auth credentials into the sandbox, failing the
// run with a codex_auth_expired category if injection fails or no creds exist.
func (o *Orchestrator) ensureCodexAuth(ctx context.Context, run *models.Session, sandbox *Sandbox) error {
	injected, err := o.injectCodexAuth(ctx, run.OrgID, sandbox)
	if err != nil {
		o.failRunWithCategory(ctx, run,
			fmt.Sprintf("codex auth injection failed: %s", err),
			FailureCategoryCodexAuth,
			"Your ChatGPT authentication has expired or was revoked. Please re-authenticate to continue using Codex.",
			[]string{"Re-authenticate with ChatGPT from the session page to sign in again"},
		)
		return fmt.Errorf("codex auth injection: %w", err)
	}
	if !injected {
		o.failRunWithCategory(ctx, run,
			"no credentials configured for codex: connect ChatGPT from the Overview page",
			FailureCategoryCodexAuth,
			"No ChatGPT credentials are configured. Please connect your ChatGPT account to use Codex.",
			[]string{"Re-authenticate with ChatGPT from the session page to sign in"},
		)
		return fmt.Errorf("no credentials for codex agent")
	}
	return nil
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

// integrationCredentials holds the resolved Sentry, Linear, and Notion configs for an org.
type integrationCredentials struct {
	Sentry *models.SentryConfig
	Linear *models.LinearConfig
	Notion *models.NotionConfig
}

// fetchIntegrationCredentials retrieves the Sentry, Linear, and Notion configs
// for an org from the credential provider. Returns nil configs if unavailable.
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
	if cred, err := o.credentials.Get(ctx, orgID, models.ProviderNotion); err == nil && cred != nil {
		if cfg, ok := cred.Config.(models.NotionConfig); ok {
			ic.Notion = &cfg
		}
	}
	return ic
}

// resolveAgentEnv builds the sandbox env vars for the given agent type.
// It checks credentials in order: user personal → team default → org credential.
// Codex CLI auth is handled via auth.json injection (injectCodexAuth), not env vars.
//
// Invariant: sandbox env must only come from org-scoped DB credentials. Do NOT
// fall back to server-level env vars (e.g. cfg.AnthropicAPIKey, cfg.OpenAIAPIKey)
// — those are 143.dev-level platform credentials and would leak across orgs in
// a multi-tenant deployment. Server-level LLM keys are reserved for 143's own
// internal LLM calls via Config.LLMConfig().
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
	case models.AgentTypeAmp:
		// Amp authenticates to Sourcegraph's API with a dedicated AMP_API_KEY
		// that lives in agent_config.amp.AMP_API_KEY — applied by the per-agent
		// override block below. No first-class credential store (no ProviderAmp)
		// exists today, so we intentionally do nothing here.
	case models.AgentTypePi:
		// Pi is a meta-agent: route to many providers via one CLI. Inherit
		// every provider key the org already configured for the other agents,
		// then let agent_config.pi.* override at the call site below.
		if cfg := o.resolveProviderConfig(ctx, orgID, userID, models.ProviderAnthropic); cfg != nil {
			if ac, ok := cfg.(models.AnthropicConfig); ok && ac.APIKey != "" {
				merged["ANTHROPIC_API_KEY"] = ac.APIKey
			}
		}
		if cfg := o.resolveProviderConfig(ctx, orgID, userID, models.ProviderOpenAI); cfg != nil {
			if oc, ok := cfg.(models.OpenAIConfig); ok && oc.APIKey != "" {
				merged["OPENAI_API_KEY"] = oc.APIKey
			}
		}
		if cfg := o.resolveProviderConfig(ctx, orgID, userID, models.ProviderGemini); cfg != nil {
			if gc, ok := cfg.(models.GeminiConfig); ok && gc.APIKey != "" {
				merged["GEMINI_API_KEY"] = gc.APIKey
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
	if ic.Notion != nil {
		if ic.Notion.AccessToken != "" {
			merged["NOTION_ACCESS_TOKEN"] = ic.Notion.AccessToken
		}
	}

	// Apply per-agent env overrides from org settings (agent_config.<type>.*).
	// Scoped to Amp and Pi only — these agents have no first-class provider
	// credential store (no ProviderAmp / ProviderPi), so agent_config is the
	// only channel for their auth (AMP_API_KEY) and routing (PI_MODEL,
	// PI_MODEL_CUSTOM). For claude_code/codex/gemini_cli we keep the legacy
	// behavior: provider creds come exclusively from resolveProviderConfig,
	// and agent_config is treated as model-default metadata (validated,
	// stored, but not injected here) — changing that would silently flip
	// existing orgs' active keys.
	if agentType == models.AgentTypeAmp || agentType == models.AgentTypePi {
		o.applyAgentConfigOverrides(ctx, orgID, agentType, merged)
	}

	if len(merged) == 0 {
		return nil
	}

	return merged
}

// checkAgentAuth returns a user-facing error when an agent type has no chance
// of authenticating against its upstream because the required credential is
// missing from the resolved sandbox env. This is a pre-flight check intended
// to beat the generic "CLI exited 1" failure with something the user can act
// on — "configure AMP_API_KEY" instead of "amp: invalid api key".
//
// Scoped to agent types whose auth lives exclusively in agent_config (Amp,
// Pi) — for the others, resolveProviderConfig already surfaces richer errors,
// and we don't want to duplicate those rules here.
//
// Invariant: callers must pass the already-merged sandbox env — i.e. after
// resolveAgentEnv has run (which layers agent_config overrides on top of
// inherited provider creds) and after any per-run ModelOverride + Pi-specific
// narrowing have been applied. checkAgentAuth reads credentials and the
// resolved Pi model directly from `env`, so invoking it on a partial env
// would either pass a misconfigured run or fail a valid one.
func (o *Orchestrator) checkAgentAuth(agentType models.AgentType, env map[string]string) error {
	switch agentType {
	case models.AgentTypeAmp:
		if env["AMP_API_KEY"] == "" {
			return fmt.Errorf("missing AMP_API_KEY: configure Amp under Settings → Default Agent → Amp before starting a session")
		}
	case models.AgentTypePi:
		return checkPiProviderKey(env)
	}
	return nil
}

// checkPiProviderKey asserts the provider key for Pi's *selected model* is
// present. PI_MODEL_CUSTOM wins over PI_MODEL, with a hardcoded fallback that
// matches piStreamingConfig.BuildCmd.
//
// For curated providers (anthropic/openai/google) we can pinpoint which key
// Pi will actually need and return a precise error — cheaper than letting the
// CLI fail with an opaque upstream 401 that doesn't say "you set PI_MODEL to
// openai/... but only Anthropic is configured." For unknown provider prefixes
// that users reach via PI_MODEL_CUSTOM (moonshot, etc.), we can't know which
// env var is required, so we fall back to the weaker "at least one inherited
// key" guarantee.
func checkPiProviderKey(env map[string]string) error {
	model := piResolvedModel(env)
	prefix, _, _ := strings.Cut(model, "/")
	switch strings.ToLower(prefix) {
	case "anthropic":
		if env["ANTHROPIC_API_KEY"] == "" {
			return fmt.Errorf("missing ANTHROPIC_API_KEY for Pi model %q: configure Claude Code under Settings → Default Agent so Pi can inherit its API key", model)
		}
	case "openai":
		if env["OPENAI_API_KEY"] == "" {
			return fmt.Errorf("missing OPENAI_API_KEY for Pi model %q: configure Codex under Settings → Default Agent so Pi can inherit its API key", model)
		}
	case "google", "gemini":
		if env["GEMINI_API_KEY"] == "" {
			return fmt.Errorf("missing GEMINI_API_KEY for Pi model %q: configure Gemini CLI under Settings → Default Agent so Pi can inherit its API key", model)
		}
	default:
		// Unknown provider (e.g. moonshot via PI_MODEL_CUSTOM). We can't tell
		// which env var Pi needs, so fall back to "at least one inherited key".
		if env["ANTHROPIC_API_KEY"] == "" && env["OPENAI_API_KEY"] == "" && env["GEMINI_API_KEY"] == "" {
			return fmt.Errorf("missing provider credentials for Pi: configure at least one of Claude Code, Codex, or Gemini CLI under Settings → Default Agent so Pi can inherit its API key")
		}
	}
	return nil
}

// piResolvedModel returns the Pi model string the CLI will run against, using
// the same precedence as piStreamingConfig.BuildCmd: PI_MODEL_CUSTOM > PI_MODEL
// > hardcoded default.
func piResolvedModel(env map[string]string) string {
	if m := env["PI_MODEL_CUSTOM"]; m != "" {
		return m
	}
	if m := env["PI_MODEL"]; m != "" {
		return m
	}
	return models.PiModelClaudeOpus47
}

// narrowPiProviderKeys strips inherited provider keys that don't match Pi's
// resolved model. Called after ModelOverride is applied, so the env reflects
// the *effective* model — a per-run override that flips Pi from Anthropic to
// OpenAI removes the Anthropic key from the sandbox env, even if the
// agent_config default pointed at Anthropic.
//
// Only narrows for known provider prefixes (anthropic/openai/google/gemini):
// for unknown prefixes (moonshot via PI_MODEL_CUSTOM, etc.) we can't tell
// which key Pi will use upstream, so the weaker "keep all inherited keys"
// posture is intentional. Returns the unknown prefix in that case (or "" when
// narrowing happened), so callers can log a warning that all inherited
// provider credentials were exported to the sandbox.
//
// Known limitation — inherited-key leak for unknown prefixes: a run with
// PI_MODEL_CUSTOM=moonshot/kimi-k2 (or any other non-curated provider) ships
// every configured provider key (Anthropic, OpenAI, Gemini) into the sandbox
// process env, even though Pi only needs Moonshot's. The keys never leave the
// container under normal operation, but a compromised Pi CLI / prompt-injection
// that tricked the agent into exfiltrating env vars would expose siblings too.
// To tighten this, add the new provider's prefix and env-var name to the switch
// above so narrowing kicks in — an explicit entry is intentionally required
// rather than a deny-by-default so operators adopting a new Pi-supported
// provider don't silently get auth failures.
func narrowPiProviderKeys(env map[string]string) string {
	model := piResolvedModel(env)
	prefix, _, _ := strings.Cut(model, "/")
	const (
		ak = "ANTHROPIC_API_KEY"
		ok = "OPENAI_API_KEY"
		gk = "GEMINI_API_KEY"
	)
	switch strings.ToLower(prefix) {
	case "anthropic":
		delete(env, ok)
		delete(env, gk)
	case "openai":
		delete(env, ak)
		delete(env, gk)
	case "google", "gemini":
		delete(env, ak)
		delete(env, ok)
	default:
		return prefix
	}
	return ""
}

// applyAgentConfigOverrides layers agent_config.<agentType>.* entries from org
// settings on top of the already-resolved provider credentials in `merged`.
// Only called for agent types (amp, pi) that lack a first-class provider
// credential store — for those, agent_config is the sole channel for auth
// and routing. Non-empty values win over inherited provider creds.
//
// Reads go through OrgSettingsCache when configured so a burst of Amp/Pi
// session starts for the same org amortizes to one DB lookup per TTL window.
// The settings update handler invalidates the cache after a write, so
// configuration changes take effect immediately rather than waiting for
// the TTL to expire.
func (o *Orchestrator) applyAgentConfigOverrides(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, merged map[string]string) {
	agentConfig, ok := o.loadAgentConfig(ctx, orgID, agentType)
	if !ok {
		return
	}
	for k, v := range agentConfig[string(agentType)] {
		if v != "" {
			merged[k] = v
		}
	}
}

// loadAgentConfig returns the org's AgentEnvConfig, using the orchestrator's
// OrgSettingsCache as a front when present. Returns (nil, false) and logs a
// warning if the org can't be loaded; callers should treat that as "no
// overrides available" rather than failing the session start.
func (o *Orchestrator) loadAgentConfig(ctx context.Context, orgID uuid.UUID, agentType models.AgentType) (models.AgentEnvConfig, bool) {
	if o.orgs == nil {
		o.logger.Warn().
			Str("agent_type", string(agentType)).
			Str("org_id", orgID.String()).
			Msg("orchestrator has no orgs store; skipping agent_config overrides (agent may run without auth)")
		return nil, false
	}

	if o.orgSettingsCache != nil {
		if cached, hit := o.orgSettingsCache.Get(orgID); hit {
			return cached, true
		}
	}

	org, err := o.orgs.GetByID(ctx, orgID)
	if err != nil {
		o.logger.Warn().
			Err(err).
			Str("agent_type", string(agentType)).
			Str("org_id", orgID.String()).
			Msg("failed to load org for agent_config overrides; agent may run without auth")
		return nil, false
	}
	orgSettings, parseErr := models.ParseOrgSettings(org.Settings)
	if parseErr != nil {
		o.logger.Warn().
			Err(parseErr).
			Str("agent_type", string(agentType)).
			Str("org_id", orgID.String()).
			Msg("failed to parse org settings for agent_config overrides; agent may run without auth")
		return nil, false
	}

	if o.orgSettingsCache != nil {
		// Store the (possibly nil) AgentConfig so a second hit doesn't re-fetch
		// just to discover the org has no agent_config.
		o.orgSettingsCache.Set(orgID, orgSettings.AgentConfig)
	}

	return orgSettings.AgentConfig, true
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

	// Use round-robin selection across all active subscriptions for this org.
	// GetValidToken claims the least-recently-used credential, refreshing it
	// in-band if it's near expiry. This is the canonical path; the legacy
	// single-credential RefreshToken would always pick the same row and bypass
	// round-robin entirely.
	cfg, err := o.codexAuth.GetValidToken(ctx, orgID)
	if err != nil {
		return false, fmt.Errorf("get codex auth token: %w", err)
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

	// Write auth.json under $HOME/.codex. The sandbox env sets HOME to the
	// sandbox user's home dir (see RunAgent step 7) so the Codex CLI
	// resolves ~/.codex/auth.json to this path.
	authDir := path.Join(sandbox.HomeDir, ".codex")
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

// BuildIntegrationSkills generates a CLI skills doc from the org's integration
// credentials. The doc is injected into the agent's system prompt so it knows
// what 143-tools commands are available in the sandbox.
func (o *Orchestrator) BuildIntegrationSkills(ctx context.Context, orgID uuid.UUID) string {
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
	if ic.Notion != nil && ic.Notion.AccessToken != "" {
		store := integration.NewNotionDocumentStore(integration.NotionDocumentStoreConfig{
			AuthToken: ic.Notion.AccessToken,
		})
		reg.RegisterDocumentStore(store)
	}

	// Register a stub GitHub code review source for skills doc generation.
	// This only describes available tools — actual API calls use real credentials
	// injected via sandbox env vars. The stub never makes HTTP requests.
	if o.github != nil {
		reg.RegisterCodeReviewSource(&integration.StubCodeReviewSource{ProviderName: "github"})
	}

	if !reg.HasAny() {
		return ""
	}

	tr := mcp.NewToolRegistry(reg)
	return mcp.GenerateSkillsDoc(tr)
}

// handleCancelledSession snapshots the workspace and returns the session to idle
// (if snapshot succeeds) or marks it as cancelled (if not). This is shared by
// both RunAgent and ContinueSession to avoid duplication.
//
// result may be nil (e.g. when the agent was force-killed and returned an error).
func (o *Orchestrator) handleCancelledSession(session *models.Session, sandbox *Sandbox, result *AgentResult, turnNumber int, log zerolog.Logger) {
	bgCtx := context.Background()

	// Attempt to snapshot so the session can be continued later.
	snapshotKey, snapshotErr := o.snapshotSession(bgCtx, session, sandbox, nil)
	if snapshotErr != nil {
		log.Warn().Err(snapshotErr).Msg("failed to snapshot cancelled session")
	}

	// Resolve the agent session ID — prefer the result if the agent exited
	// gracefully, otherwise fall back to whatever was on the session.
	agentSessionID := ""
	if result != nil && result.AgentSessionID != "" {
		agentSessionID = result.AgentSessionID
	} else if session.AgentSessionID != nil {
		agentSessionID = *session.AgentSessionID
	}

	// If we got a snapshot, return to idle via UpdateTurnComplete so the user
	// can continue the conversation. Otherwise, mark as cancelled (terminal).
	if snapshotKey != "" {
		if err := o.sessions.UpdateTurnComplete(bgCtx, session.OrgID, session.ID, turnNumber, nil, agentSessionID, snapshotKey); err != nil {
			log.Warn().Err(err).Msg("failed to return cancelled session to idle")
			_ = o.sessions.UpdateStatus(bgCtx, session.OrgID, session.ID, string(models.SessionStatusCancelled))
		} else {
			log.Info().Int("turn", turnNumber).Msg("cancelled session returned to idle")
		}
	} else {
		_ = o.sessions.UpdateStatus(bgCtx, session.OrgID, session.ID, string(models.SessionStatusCancelled))
	}
}

// snapshotSession snapshots the sandbox workspace to object storage for multi-turn support.
// If snapshots are not configured, this is a no-op. This only saves the snapshot
// and updates sandbox state — it does NOT change session status or call UpdateTurnComplete.
// result is unused but kept in the signature for future extensibility (e.g. metadata).
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

// ResolveSessionTimeout returns the per-session wall-clock timeout for the
// given org, reading OrgSettings.MaxSessionDurationSeconds.
// ParseOrgSettings always normalises the value (defaulting to
// DefaultMaxSessionDurationSeconds and clamping into [Min, Max]), so the
// only path that hits the DefaultSandboxTimeout fallback is the one where
// we cannot reach the org store at all — nil store, DB outage, or a
// malformed settings row. Safe to call with a nil OrgStore.
func (o *Orchestrator) ResolveSessionTimeout(ctx context.Context, orgID uuid.UUID) time.Duration {
	if o.orgs != nil {
		if org, err := o.orgs.GetByID(ctx, orgID); err == nil {
			if orgSettings, parseErr := models.ParseOrgSettings(org.Settings); parseErr == nil {
				return time.Duration(orgSettings.MaxSessionDurationSeconds) * time.Second
			}
		}
	}
	return DefaultSandboxTimeout
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

// isTokenExpiredError returns true if the error string indicates the Codex CLI
// received a 401 token_expired response from ChatGPT's backend API. This is
// used to trigger a single retry with a refreshed token.
//
// Note: these strings are intentionally NOT covered by isRefreshTokenError
// in the Codex adapter (which filters refresh_token_reused, invalid_grant, etc.).
// token_expired errors must flow through to result.Error so this retry can fire.
func isTokenExpiredError(errMsg string) bool {
	if errMsg == "" {
		return false
	}
	return strings.Contains(errMsg, "token_expired") ||
		strings.Contains(errMsg, "token is expired") ||
		strings.Contains(errMsg, "Provided authentication token is expired")
}

// retryOnTokenExpired checks whether the Codex CLI failed with a token_expired
// error and, if so, refreshes the token, re-injects auth.json, and retries
// execution once. Returns the (possibly updated) result and error.
func (o *Orchestrator) retryOnTokenExpired(
	ctx context.Context,
	agentType models.AgentType,
	orgID uuid.UUID,
	sessionID uuid.UUID,
	turnNumber int,
	sandbox *Sandbox,
	adapter AgentAdapter,
	execCtx context.Context,
	prompt *AgentPrompt,
	result *AgentResult,
	err error,
	log zerolog.Logger,
) (*AgentResult, error) {
	if agentType != models.AgentTypeCodex || result == nil || !isTokenExpiredError(result.Error) {
		return result, err
	}

	log.Warn().Msg("codex CLI hit token_expired, refreshing token and retrying")

	reinjected, reinjectErr := o.injectCodexAuth(ctx, orgID, sandbox)
	if reinjectErr != nil {
		log.Warn().Err(reinjectErr).Msg("failed to re-inject codex auth for retry")
		return result, err
	}
	if !reinjected {
		return result, err
	}

	retryLogCh := make(chan LogEntry, 100)
	var retryLogWg sync.WaitGroup
	retryLogWg.Add(1)
	go func() {
		defer retryLogWg.Done()
		o.streamLogs(ctx, sessionID, orgID, turnNumber, retryLogCh)
	}()

	result, err = adapter.Execute(execCtx, sandbox, prompt, retryLogCh)
	close(retryLogCh)
	retryLogWg.Wait()

	log.Info().Msg("codex CLI retry after token refresh completed")
	return result, err
}

// containerExitReason determines a granular exit reason for billing metadata
// based on the parent context state and the error returned from execution.
func containerExitReason(ctx context.Context, err error) string {
	if err == nil {
		return "completed"
	}
	// Check context first — a cancelled/timed-out context is the most
	// specific signal we have.
	if ctxErr := ctx.Err(); ctxErr != nil {
		if ctxErr == context.DeadlineExceeded {
			return "timeout"
		}
		return "cancelled"
	}
	return "failed"
}
