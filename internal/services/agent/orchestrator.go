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

// ClaudeCodeAuthProvider abstracts Claude Code subscription OAuth: the
// existence check drives the resolveAgentEnv branch (so we know whether to
// set ANTHROPIC_API_KEY or leave it empty for the file path to win), while
// GetValidToken drives the sandbox file injection.
type ClaudeCodeAuthProvider interface {
	HasActiveSubscription(ctx context.Context, orgID uuid.UUID) (bool, error)
	GetValidToken(ctx context.Context, orgID uuid.UUID) (*models.AnthropicSubscription, *uuid.UUID, error)
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
	claudeCodeAuth    ClaudeCodeAuthProvider // can be nil
	credentials       CredentialProvider     // can be nil — disables integration-skills doc generation
	memory            MemoryService          // can be nil
	snapshots         storage.SnapshotStore  // can be nil — multi-turn disabled if nil
	usageTracker      UsageRecorder          // can be nil — billing tracking disabled if nil
	env               *AgentEnv              // owns env resolution, auth pre-flight, Codex auth injection
	logger            zerolog.Logger
	maxConcurrent     int
	cancels           *CancelRegistry
}

// DurableCheckpoint is the latest fully committed resume boundary for a
// session. Recovery may resume from this checkpoint, but never from newer
// in-memory state that was not durably persisted.
type DurableCheckpoint struct {
	Turn           int
	SnapshotKey    string
	AgentSessionID string
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
	CodexAuth        CodexAuthProvider      // optional — enables ChatGPT OAuth for Codex agent
	ClaudeCodeAuth   ClaudeCodeAuthProvider // optional — enables Claude subscription OAuth for Claude Code agent
	Credentials      CredentialProvider
	Memory           MemoryService          // optional — injects learned memories into agent prompts
	UserCredentials  UserCredentialProvider // optional — enables personal/team credential resolution
	Snapshots        storage.SnapshotStore  // optional — enables multi-turn snapshot/restore
	UsageTracker     UsageRecorder          // optional — enables billing observability
	Cancels          *CancelRegistry        // optional — enables session cancellation from API
	OrgSettingsCache *OrgSettingsCache      // optional — caches Amp/Pi agent_config lookups across session starts
	// Env owns env resolution + auth pre-flight + Codex auth injection,
	// shared with the PM service. Optional: when nil, NewOrchestrator
	// constructs an AgentEnv from the other OrchestratorConfig fields so
	// existing call sites (notably tests) don't have to change.
	Env           *AgentEnv
	Logger        zerolog.Logger
	MaxConcurrent int
}

// NewOrchestrator creates an Orchestrator with the given dependencies.
func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrent
	}

	// Fall back to constructing an AgentEnv from the inlined config fields when
	// the caller didn't pass one. main.go passes Env explicitly so PM can share
	// it; tests leave it nil and get a functionally equivalent AgentEnv for free.
	env := cfg.Env
	if env == nil {
		env = NewAgentEnv(AgentEnvDeps{
			Credentials:      cfg.Credentials,
			UserCredentials:  cfg.UserCredentials,
			Orgs:             cfg.Orgs,
			OrgSettingsCache: cfg.OrgSettingsCache,
			CodexAuth:        cfg.CodexAuth,
			Provider:         cfg.Provider,
			Logger:           cfg.Logger,
		})
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
		claudeCodeAuth:    cfg.ClaudeCodeAuth,
		credentials:       cfg.Credentials,
		memory:            cfg.Memory,
		snapshots:         cfg.Snapshots,
		usageTracker:      cfg.UsageTracker,
		env:               env,
		cancels:           cfg.Cancels,
		logger:            cfg.Logger,
		maxConcurrent:     maxConcurrent,
	}
}

// RecoverSession resumes an interrupted session from its latest committed
// checkpoint when one exists. If the session has no durable checkpoint yet,
// it restarts the run from scratch.
func (o *Orchestrator) RecoverSession(ctx context.Context, session *models.Session) error {
	log := o.logger.With().
		Str("session_id", session.ID.String()).
		Str("org_id", session.OrgID.String()).
		Logger()

	checkpoint, ok := latestDurableCheckpoint(session)
	if !ok {
		log.Info().Msg("recovery requested with no durable checkpoint; restarting session from scratch")
		return o.RunAgent(ctx, session)
	}

	event := log.Info().Int("checkpoint_turn", checkpoint.Turn)
	if checkpoint.SnapshotKey != "" {
		event = event.Str("snapshot_key", checkpoint.SnapshotKey)
	}
	if checkpoint.AgentSessionID != "" {
		event = event.Str("agent_session_id", checkpoint.AgentSessionID)
	}
	event.Msg("recovering session from latest durable checkpoint")

	return o.ContinueSession(ctx, session)
}

func latestDurableCheckpoint(session *models.Session) (DurableCheckpoint, bool) {
	checkpoint := DurableCheckpoint{Turn: session.CurrentTurn}
	if session.SnapshotKey != nil {
		checkpoint.SnapshotKey = *session.SnapshotKey
	}
	if session.AgentSessionID != nil {
		checkpoint.AgentSessionID = *session.AgentSessionID
	}
	if checkpoint.Turn <= 0 && checkpoint.SnapshotKey == "" && checkpoint.AgentSessionID == "" {
		return DurableCheckpoint{}, false
	}
	return checkpoint, true
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
	if err := o.checkConcurrency(ctx, run.OrgID, models.SessionStatus(run.Status) == models.SessionStatusRunning); err != nil {
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
	sandboxCfg.Env = o.env.Resolve(ctx, run.OrgID, run.AgentType, run.TriggeredByUserID)
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
	if err := o.env.CheckAuth(run.AgentType, sandboxCfg.Env); err != nil {
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
	// On DB error we destroy the local container and fail the run — if we
	// left it alive the reconciler couldn't find it (no container_id row
	// reference) and it would leak until server restart.
	actualContainerID, holdErr := o.sessions.AcquireTurnHold(ctx, run.OrgID, run.ID, sandbox.ID)
	if holdErr != nil {
		destroyCtx := context.Background()
		if destroyErr := o.provider.Destroy(destroyCtx, sandbox); destroyErr != nil {
			log.Error().Err(destroyErr).Str("container_id", sandbox.ID).Msg("failed to destroy sandbox after turn hold DB error")
		}
		o.failRun(ctx, run, fmt.Sprintf("acquire turn hold: %s", holdErr))
		return fmt.Errorf("acquire turn hold: %w", holdErr)
	}
	if actualContainerID != "" && actualContainerID != sandbox.ID {
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

	// 9. Inject auth credentials into the sandbox. Done after clone so the
	//    workspace is available.
	//    - Codex: auth.json is the primary (and only) auth mechanism.
	//    - Claude Code: subscription credentials file is preferred, with
	//      ANTHROPIC_API_KEY env var as the fallback.
	switch run.AgentType {
	case models.AgentTypeCodex:
		if err := o.ensureCodexAuth(ctx, run, sandbox); err != nil {
			return err
		}
	case models.AgentTypeClaudeCode:
		if err := o.ensureClaudeCodeAuth(ctx, run, sandbox); err != nil {
			return err
		}
	}

	// 9b. Integration tools (143-tools CLI) are pre-installed in the container
	// image. Credentials are injected via env vars (AgentEnv.Resolve), and the
	// skills doc is injected into the prompt (BuildIntegrationSkills). No
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
	sandboxCfg.Env = o.env.Resolve(ctx, session.OrgID, session.AgentType, session.TriggeredByUserID)
	if sandboxCfg.Env == nil {
		sandboxCfg.Env = make(map[string]string)
	}
	// Apply the per-session model override before the auth pre-flight so
	// AgentEnv.CheckAuth evaluates the *effective* model — see the matching
	// block in RunAgent for the Pi-specific reasoning.
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
	if authErr := o.env.CheckAuth(session.AgentType, sandboxCfg.Env); authErr != nil {
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
	// On DB error we destroy any locally-created sandbox and fail the turn —
	// if we left it alive the reconciler couldn't find it (no container_id
	// row reference) and it would leak.
	actualContainerID, holdErr := o.sessions.AcquireTurnHold(ctx, session.OrgID, session.ID, sandbox.ID)
	if holdErr != nil {
		destroyCtx := context.Background()
		if !reusedExisting {
			if destroyErr := o.provider.Destroy(destroyCtx, sandbox); destroyErr != nil {
				log.Error().Err(destroyErr).Str("container_id", sandbox.ID).Msg("failed to destroy sandbox after turn hold DB error")
			}
		}
		if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
			log.Error().Err(revertErr).Msg("failed to revert session to idle after turn hold DB error")
		}
		o.registerSandboxFailureMessage(
			ctx,
			session,
			fmt.Sprintf("Failed to acquire sandbox lease: %s\n\nPlease try again in a moment.", holdErr),
			"turn hold",
		)
		return fmt.Errorf("acquire turn hold: %w", holdErr)
	}
	if actualContainerID != "" && actualContainerID != sandbox.ID {
		destroyCtx := context.Background()
		// Only destroy the locally-created container. When reusedExisting is
		// true, sandbox.ID came from the row's existing container_id and
		// belongs to whoever set it — tearing it down here would kill a
		// container another holder is still using. On the losing-race path
		// we instead leave it alone; the caller's retry will attach to
		// actualContainerID via the reuse path.
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
		// Re-inject agent auth (Codex auth.json or Claude Code credentials.json).
		// Cheap, and catches the case where the file was cleared or drifted
		// while the container was idle (or where the preview created the
		// container without agent credentials).
		switch session.AgentType {
		case models.AgentTypeCodex:
			if err := o.ensureCodexAuth(ctx, session, sandbox); err != nil {
				return err
			}
		case models.AgentTypeClaudeCode:
			if err := o.ensureClaudeCodeAuth(ctx, session, sandbox); err != nil {
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

	// Inject auth credentials into the sandbox.
	switch session.AgentType {
	case models.AgentTypeCodex:
		injected, err := o.env.InjectCodexAuth(ctx, session.OrgID, sandbox)
		if err != nil {
			return models.Issue{}, "", fmt.Errorf("codex auth injection: %w", err)
		}
		if !injected {
			return models.Issue{}, "", fmt.Errorf("no credentials for codex agent")
		}
	case models.AgentTypeClaudeCode:
		if err := o.ensureClaudeCodeAuth(ctx, session, sandbox); err != nil {
			return models.Issue{}, "", fmt.Errorf("claude code auth injection: %w", err)
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
// excludeCurrentRunning should be true when re-entering RunAgent for a session
// that is already marked running, so recovery does not deadlock on its own slot.
func (o *Orchestrator) checkConcurrency(ctx context.Context, orgID uuid.UUID, excludeCurrentRunning bool) error {
	count, err := o.sessions.CountRunningByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("count running runs: %w", err)
	}
	if excludeCurrentRunning && count > 0 {
		count--
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
	injected, err := o.env.InjectCodexAuth(ctx, run.OrgID, sandbox)
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

// injectClaudeCodeAuth writes a ~/.claude/.credentials.json file into the
// sandbox if an active Claude Code subscription exists for this org. The
// Claude Code CLI prefers the credentials file over ANTHROPIC_API_KEY env
// vars, so when a subscription is present the file path wins even though
// resolveAgentEnv still sets ANTHROPIC_API_KEY as a fallback. Returns (true, nil) when the file
// was written, (false, nil) when no subscription exists so the API-key
// fallback should be used, or (false, err) on failure.
//
// Credentials file schema: the shape (claudeAiOauth.{accessToken,
// refreshToken, expiresAt, scopes, subscriptionType, rateLimitTier}) mirrors
// what the Claude Code CLI writes itself when a user runs `claude login`.
// expiresAt is milliseconds-since-epoch. Scopes is the array form the CLI
// uses; we translate from the space-separated `scope` response string when
// the tokens are issued. If Anthropic ever changes this format, update this
// marshal block and the AnthropicSubscription struct together.
func (o *Orchestrator) injectClaudeCodeAuth(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox) (bool, error) {
	if o.claudeCodeAuth == nil {
		return false, nil
	}

	sub, _, err := o.claudeCodeAuth.GetValidToken(ctx, orgID)
	if err != nil {
		return false, fmt.Errorf("get claude code subscription token: %w", err)
	}
	if sub == nil {
		return false, nil
	}

	oauthPayload := map[string]interface{}{
		"accessToken":  sub.AccessToken,
		"refreshToken": sub.RefreshToken,
		"expiresAt":    sub.ExpiresAt.UnixMilli(),
	}
	if len(sub.Scopes) > 0 {
		oauthPayload["scopes"] = sub.Scopes
	}
	if sub.AccountType != "" {
		oauthPayload["subscriptionType"] = sub.AccountType
	}
	if sub.RateLimitTier != "" {
		oauthPayload["rateLimitTier"] = sub.RateLimitTier
	}
	credsJSON, err := json.Marshal(map[string]interface{}{"claudeAiOauth": oauthPayload})
	if err != nil {
		return false, fmt.Errorf("marshal claude credentials: %w", err)
	}

	authDir := path.Join(sandbox.HomeDir, ".claude")
	credsPath := authDir + "/.credentials.json"

	// Single-quote each path defensively even though HomeDir is orchestrator-
	// controlled today; a future refactor could start threading user input
	// through HomeDir and we don't want a silent shell-injection footgun.
	// We combine mkdir + pre-create-with-0600 into one Exec so there is no
	// window where the credentials file exists at the shell's default umask
	// (typically 0644) before being locked down. `install -m 600 /dev/null`
	// creates an empty file with mode 0600 atomically; the subsequent
	// WriteFile uses POSIX `>` truncation, which preserves the existing
	// file's mode rather than re-applying the umask.
	prepCmd := fmt.Sprintf(
		"mkdir -p '%s' && install -m 600 /dev/null '%s'",
		shellEscapeSingleQuote(authDir),
		shellEscapeSingleQuote(credsPath),
	)

	var prepOut, prepErr bytes.Buffer
	exitCode, err := o.provider.Exec(ctx, sandbox, prepCmd, &prepOut, &prepErr)
	if err != nil {
		return false, fmt.Errorf("prepare claude credentials file: %w", err)
	}
	if exitCode != 0 {
		return false, fmt.Errorf("prepare claude credentials file: exited with code %d: %s", exitCode, prepErr.String())
	}

	if err := o.provider.WriteFile(ctx, sandbox, credsPath, credsJSON); err != nil {
		return false, fmt.Errorf("write claude credentials: %w", err)
	}

	o.logger.Debug().
		Str("org_id", orgID.String()).
		Msg("injected claude subscription credentials into sandbox")

	return true, nil
}

// ensureClaudeCodeAuth guarantees that the Claude Code agent has at least one
// credential path available in the sandbox. Priority is subscription file >
// ANTHROPIC_API_KEY env var; the run only fails when neither is configured.
func (o *Orchestrator) ensureClaudeCodeAuth(ctx context.Context, run *models.Session, sandbox *Sandbox) error {
	injected, err := o.injectClaudeCodeAuth(ctx, run.OrgID, sandbox)
	if err != nil {
		if fallbackErr := o.prepareClaudeCodeAPIKeyFallback(ctx, run, sandbox); fallbackErr == nil {
			o.logger.Warn().
				Err(err).
				Str("org_id", run.OrgID.String()).
				Str("session_id", run.ID.String()).
				Msg("claude subscription injection failed; continuing with Anthropic API-key fallback")
			return nil
		} else if !errors.Is(fallbackErr, errClaudeCodeFallbackUnavailable) {
			o.failRunWithCategory(ctx, run,
				fmt.Sprintf("claude subscription injection failed and API-key fallback could not be prepared: %s", fallbackErr),
				FailureCategoryClaudeCodeAuth,
				"Your Claude subscription token could not be injected, and the sandbox could not be prepared to use the Anthropic API key fallback.",
				[]string{"Retry the session after reconnecting your Claude subscription or verifying Anthropic credentials"},
			)
			return fmt.Errorf("prepare claude code API-key fallback: %w", fallbackErr)
		}
		o.failRunWithCategory(ctx, run,
			fmt.Sprintf("claude subscription injection failed: %s", err),
			FailureCategoryClaudeCodeAuth,
			"Your Claude subscription token could not be injected into the sandbox. The token may have been revoked or the refresh failed.",
			[]string{"Re-connect your Claude subscription from the Agent settings page"},
		)
		return fmt.Errorf("claude code auth injection: %w", err)
	}
	if injected {
		return nil
	}

	// No subscription — check for an Anthropic API-key fallback. The env var
	// was already baked into sandboxCfg.Env by resolveAgentEnv, so if the
	// credential exists the sandbox is already configured.
	if fallbackErr := o.prepareClaudeCodeAPIKeyFallback(ctx, run, sandbox); fallbackErr == nil {
		return nil
	} else if !errors.Is(fallbackErr, errClaudeCodeFallbackUnavailable) {
		o.failRunWithCategory(ctx, run,
			fmt.Sprintf("claude API-key fallback could not be prepared: %s", fallbackErr),
			FailureCategoryClaudeCodeAuth,
			"The Anthropic API key fallback is configured, but the sandbox could not be prepared to use it because stale Claude credentials could not be cleared.",
			[]string{"Retry the session after reconnecting your Claude subscription or verifying sandbox access"},
		)
		return fmt.Errorf("prepare claude code API-key fallback: %w", fallbackErr)
	}

	o.failRunWithCategory(ctx, run,
		"no credentials configured for Claude Code: connect a Claude subscription or add an Anthropic API key",
		FailureCategoryClaudeCodeAuth,
		"No Claude Code credentials are configured. Connect your Claude subscription (recommended) or add an Anthropic API key from the Agent settings page.",
		[]string{
			"Connect a Claude subscription from the Agent settings page",
			"Or add an Anthropic API key under Credentials",
		},
	)
	return fmt.Errorf("no credentials for claude code agent")
}

var errClaudeCodeFallbackUnavailable = errors.New("claude code API-key fallback unavailable")

func (o *Orchestrator) prepareClaudeCodeAPIKeyFallback(ctx context.Context, run *models.Session, sandbox *Sandbox) error {
	cfg := o.env.resolveProviderConfig(ctx, run.OrgID, run.TriggeredByUserID, models.ProviderAnthropic)
	ac, ok := cfg.(models.AnthropicConfig)
	if !ok || ac.APIKey == "" {
		return errClaudeCodeFallbackUnavailable
	}

	if err := o.removeClaudeCodeCredentialsFile(ctx, sandbox); err != nil {
		return err
	}
	return nil
}

func (o *Orchestrator) removeClaudeCodeCredentialsFile(ctx context.Context, sandbox *Sandbox) error {
	credsPath := path.Join(sandbox.HomeDir, ".claude", ".credentials.json")
	if _, err := o.provider.ReadFile(ctx, sandbox, credsPath); err != nil {
		if isSandboxFileMissing(err) {
			return nil
		}
		return fmt.Errorf("check stale claude credentials: %w", err)
	}

	cmd := fmt.Sprintf("rm -f '%s'", shellEscapeSingleQuote(credsPath))

	var stdout, stderr bytes.Buffer
	exitCode, err := o.provider.Exec(ctx, sandbox, cmd, &stdout, &stderr)
	if err != nil {
		return fmt.Errorf("remove stale claude credentials: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("remove stale claude credentials: exited with code %d: %s", exitCode, stderr.String())
	}
	return nil
}

func isSandboxFileMissing(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "file not found") || strings.Contains(msg, "no such file")
}

// BuildIntegrationSkills generates a CLI skills doc from the org's integration
// credentials. The doc is injected into the agent's system prompt so it knows
// what 143-tools commands are available in the sandbox.
func (o *Orchestrator) BuildIntegrationSkills(ctx context.Context, orgID uuid.UUID) string {
	if o.credentials == nil {
		return ""
	}

	ic := o.env.fetchIntegrationCredentials(ctx, orgID)
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

	reinjected, reinjectErr := o.env.InjectCodexAuth(ctx, orgID, sandbox)
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
