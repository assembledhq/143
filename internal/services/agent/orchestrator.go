package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/observability"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/assembledhq/143/internal/services/mcp"
	"github.com/assembledhq/143/internal/services/sandboxauth"
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

// ErrSnapshotPending is returned from ContinueSession when the session has a
// non-empty pending_snapshot_key — i.e. a post-PR snapshot upload is still
// in flight. Hydrating from the previous SnapshotKey would restore the stale
// pre-PR state (uncommitted edits at the original BaseCommitSHA), so we
// prefer to wait. The worker handler wraps this in a RetryableError so the
// job is requeued without consuming an attempt.
var ErrSnapshotPending = errors.New("snapshot upload pending")

// ErrStalePullRequestHead is returned by PR repair reconstruction when the
// fetched pull-request ref does not match the health snapshot's expected head.
// The worker uses it to refresh GitHub state instead of running the agent
// against the wrong checkout.
var ErrStalePullRequestHead = errors.New("stale pull request head")

// ErrSandboxRaceLoser is returned from RunAgent / ContinueSession when
// AcquireTurnHold's COALESCE reveals that another holder published a
// container_id first AND that container is alive — i.e. a duplicate
// run_agent / continue_session job is concurrently running the same turn.
// The "winner" owns the session row and will update its result; the loser
// must NOT call failRun (that would race the winner's terminal write and
// corrupt the row) and must NOT have its job retried (every retry would
// lose the same race). Worker handlers recognize this error and convert it
// to a FatalError so the duplicate job dead-letters silently without
// surfacing a user-visible failure.
var ErrSandboxRaceLoser = errors.New("sandbox race: another holder attached first")

// ErrStaleSandboxIDCleared is returned from RunAgent / ContinueSession when
// AcquireTurnHold reports a different container_id but an IsAlive probe
// reveals that the "winner" container is dead — a stale orphan from a
// crashed worker that the startup reconciler hasn't reaped yet. The loser
// CAS-clears container_id via ClearContainerID and signals retry: the next
// attempt will see a clean row and create a fresh sandbox. Worker handlers
// convert this to a RetryableError with a short backoff so the retry happens
// without consuming an attempt counter.
var ErrStaleSandboxIDCleared = errors.New("sandbox race: cleared stale orphan container_id, retry")

// ErrSandboxPreviewRace is returned from RunAgent / ContinueSession when
// AcquireTurnHold reports a different live container_id, but holder-state
// inspection shows the live container belongs to a preview hydrate rather
// than another agent turn. There is no "winning" agent job to publish the
// user's turn result, so the worker must retry instead of silently
// dead-lettering as a duplicate.
var ErrSandboxPreviewRace = errors.New("sandbox race: preview holder attached first, retry")

// ErrSandboxOnDifferentNode is returned from ContinueSession's reuse path
// when the session's recorded worker_node_id points at a different worker
// than the one running this job. Container ids are local to a docker
// daemon, so we cannot exec into a sandbox owned by a sibling node — and
// an IsAlive probe on the wrong daemon false-reports "not alive", which
// would CAS-clear the row and orphan the live container on its real host.
//
// Worker handlers convert this to a RetryableError so the job is released
// back to the queue with a short delay, giving the correctly-pinned
// worker a chance to claim it. Once node-affinity routing is rolled out
// (target_node_id on the jobs table) this branch becomes a defense-in-
// depth safety net for any job enqueued before pinning landed.
var ErrSandboxOnDifferentNode = errors.New("sandbox race: session sandbox lives on a different worker node, retry")

// canonicalTimeoutLogMessage is the single log phrase emitted whenever a
// session hits its configured deadline. Kept deliberately narrow so
// Grafana alerts can key off one string across RunAgent and ContinueSession.
const canonicalTimeoutLogMessage = "session exceeded configured timeout"

// sandboxRaceProbeTimeout bounds the IsAlive probe at the AcquireTurnHold
// race-loss diagnosis site. Short enough that a docker-daemon hiccup can't
// stall the loser's cleanup, long enough that a healthy IsAlive (typically
// sub-100ms) succeeds with margin.
const sandboxRaceProbeTimeout = 3 * time.Second

func logAgentRunFinished(log zerolog.Logger, run *models.Session, outcome string, runStartedAt time.Time, addFields func(*zerolog.Event)) {
	event := log.Info().
		Str("agent_type", string(run.AgentType)).
		Str("outcome", outcome).
		Float64("duration_ms", observability.DurationMillis(time.Since(runStartedAt)))
	if addFields != nil {
		addFields(event)
	}
	event.Msg("agent run finished")
}

func logAgentRunFailed(log zerolog.Logger, run *models.Session, err error, outcome string, runStartedAt time.Time, addFields func(*zerolog.Event)) {
	event := log.Error().Err(err).
		Str("agent_type", string(run.AgentType)).
		Str("outcome", outcome).
		Float64("duration_ms", observability.DurationMillis(time.Since(runStartedAt)))
	if addFields != nil {
		addFields(event)
	}
	event.Msg("agent run failed")
}

// diagnoseAcquireHoldRaceLoss decides whether a lost AcquireTurnHold is a
// genuine duplicate run (winner is alive) or a stale orphan from a crashed
// prior worker (winner is dead, never released). It probes IsAlive on
// actualContainerID and, if dead, CAS-clears the row so the caller's retry
// re-enters against a clean session row. Returns ErrSandboxRaceLoser for
// genuine duplicates (worker dead-letters silently) or ErrStaleSandboxIDCleared
// for stale orphans (worker requeues without consuming an attempt). Probe
// or clear errors are intentionally conservative — they fall back to
// ErrSandboxRaceLoser to avoid clobbering a real active turn on a transient
// docker / DB hiccup; the startup reconciler remains the safety net.
func (o *Orchestrator) diagnoseAcquireHoldRaceLoss(
	ctx context.Context,
	orgID, sessionID uuid.UUID,
	actualContainerID string,
	log zerolog.Logger,
) error {
	aliveCtx, cancel := context.WithTimeout(ctx, sandboxRaceProbeTimeout)
	defer cancel()
	alive, aliveErr := o.provider.IsAlive(aliveCtx, &Sandbox{ID: actualContainerID, Provider: "docker"})
	if aliveErr != nil {
		log.Warn().Err(aliveErr).
			Str("winning_container_id", actualContainerID).
			Msg("IsAlive probe failed during sandbox race-loss diagnosis; assuming alive winner and dead-lettering this duplicate")
		return ErrSandboxRaceLoser
	}
	if alive {
		turnHolds, previewHolds, stateErr := o.sessions.ContainerHoldState(ctx, orgID, sessionID, actualContainerID)
		if stateErr != nil {
			log.Warn().Err(stateErr).
				Str("winning_container_id", actualContainerID).
				Msg("holder-state probe failed during sandbox race-loss diagnosis; assuming alive turn winner and dead-lettering this duplicate")
			return ErrSandboxRaceLoser
		}
		if turnHolds {
			return ErrSandboxRaceLoser
		}
		if previewHolds {
			return ErrSandboxPreviewRace
		}
		log.Warn().
			Str("winning_container_id", actualContainerID).
			Msg("live container has no recorded turn or preview holder during race-loss diagnosis; assuming alive winner and dead-lettering this duplicate")
		return ErrSandboxRaceLoser
	}
	cleared, clearErr := o.sessions.ClearContainerID(ctx, orgID, sessionID, actualContainerID)
	if clearErr != nil {
		log.Warn().Err(clearErr).
			Str("winning_container_id", actualContainerID).
			Msg("ClearContainerID failed during sandbox race-loss diagnosis; falling back to silent dead-letter so a transient DB error doesn't trigger a tight retry loop")
		return ErrSandboxRaceLoser
	}
	if !cleared {
		log.Info().
			Str("winning_container_id", actualContainerID).
			Msg("ClearContainerID CAS lost during race-loss diagnosis (a new holder acquired between IsAlive and Clear); dead-lettering this duplicate")
		return ErrSandboxRaceLoser
	}
	log.Warn().
		Str("stale_container_id", actualContainerID).
		Msg("cleared stale orphan container_id from a crashed prior turn; signaling retry to re-enter against the clean row")
	return ErrStaleSandboxIDCleared
}

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

// ClaudeCodeAuthRefresher rotates an expired Claude subscription token by
// credential id. The scope must match the credential's owner: personal
// subscriptions live in coding_credentials with user_id set, and the
// underlying lookup filters on (org_id, user_id) — passing org scope for a
// personal credential would mis-route the lookup and surface as
// "credential not found", silently dropping personal subscriptions back
// to the org fallback after their first 8h of token life.
type ClaudeCodeAuthRefresher interface {
	RefreshTokenByID(ctx context.Context, scope models.Scope, credID uuid.UUID) (*models.AnthropicSubscription, error)
}

// CredentialProvider abstracts retrieving org-scoped provider credentials.
type CredentialProvider interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
	ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error)
}

// UserCredentialProvider abstracts retrieving user-scoped provider credentials.
type UserCredentialProvider interface {
	GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
	GetTeamDefault(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
}

// CodingCredentialProvider abstracts the unified coding-credentials resolver.
// Returns the ordered (personal-then-org, priority-within-scope) list of
// runnable credentials for a (orgID, userID, provider) triple. The unified
// store is the source of truth post-migration; AgentEnv.resolveProviderConfig
// prefers this when wired, falling back to the legacy 3-step cascade only if
// nothing comes back.
type CodingCredentialProvider interface {
	ListResolvable(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error)
}

// UserLookup fetches a user record. Used by the orchestrator to materialize
// the triggering user for the Co-authored-by trailer when the resolver
// returns the App-token fallback. Defined as an interface so the
// orchestrator stays a step removed from db package import constraints.
type UserLookup interface {
	GetByID(ctx context.Context, orgID, userID uuid.UUID) (models.User, error)
}

// SandboxAuthServer is the host-side socket server that issues fresh GitHub
// credentials to the in-sandbox 143-tools helper. Defined as an interface
// so tests can stub it without spinning real Unix sockets.
//
// Listen returns just the socket path; teardown goes through Close(sessionID).
// Listen and Close are paired by sessionID so the Server stays the single
// owner of "what's currently bound" — callers don't keep per-call closers.
type SandboxAuthServer interface {
	Listen(ctx context.Context, sessionID uuid.UUID, run *models.Session, repo *models.Repository, orgSettings models.OrgSettings) (socketPath string, err error)
	Close(sessionID uuid.UUID)
}

// SessionStore defines the agent run DB operations needed by the orchestrator.
type SessionStore interface {
	UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error
	UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.SessionResult) error
	CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error)
	UpdateTurnComplete(ctx context.Context, orgID, sessionID uuid.UUID, turn int, result *models.SessionResult, agentSessionID, snapshotKey string) error
	UpdateSnapshotInfo(ctx context.Context, orgID, sessionID uuid.UUID, agentSessionID, snapshotKey string) error
	BeginRuntime(ctx context.Context, orgID, sessionID uuid.UUID, capability models.CheckpointCapability, softDeadline, hardDeadline, observedAt time.Time) error
	RecordRuntimeProgress(ctx context.Context, orgID, sessionID uuid.UUID, progressType models.RuntimeProgressType, strength models.RuntimeProgressStrength, observedAt time.Time) error
	GrantRuntimeExtension(ctx context.Context, orgID, sessionID uuid.UUID, lockToken uuid.UUID, expectedSoftDeadline, newSoftDeadline, hardDeadline time.Time, extensionSeconds int) (bool, error)
	PublishCheckpoint(ctx context.Context, orgID, sessionID uuid.UUID, lockToken uuid.UUID, agentSessionID, snapshotKey string, kind models.CheckpointKind, capability models.CheckpointCapability, sizeBytes int64, checkpointedAt time.Time, checkpointErr *string, stopReason models.RuntimeStopReason) (bool, error)
	UpdateRecoveryState(ctx context.Context, orgID, sessionID uuid.UUID, state models.RecoveryState, queuedAt, startedAt *time.Time, incrementAttempt bool) error
	UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state string) error
	UpdateWorkingBranch(ctx context.Context, orgID, sessionID uuid.UUID, branch string) error
	UpdateBaseCommitSHA(ctx context.Context, orgID, sessionID uuid.UUID, baseCommitSHA string) error
	// SetGitIdentity records which credential authority issued the session's
	// git pushes (user OAuth vs App installation token). Persisted for audit.
	SetGitIdentity(ctx context.Context, orgID, sessionID uuid.UUID, source string, userID *uuid.UUID) error
	UpdateFailure(ctx context.Context, orgID, runID uuid.UUID, explanation, category string, nextSteps []string, retryAdvised bool) error
	UpdateTitle(ctx context.Context, orgID, sessionID uuid.UUID, title string) error
	// UpdateRevisionContext rewrites sessions.revision_context.
	UpdateRevisionContext(ctx context.Context, orgID, sessionID uuid.UUID, revisionContext []byte) error
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	// AcquireTurnHold flips turn_holding_container=TRUE and publishes the
	// turn's proposed container_id via COALESCE. The returned
	// actualContainerID is the container_id now stored on the row: equal to
	// proposedContainerID when the caller won the race, different when a
	// concurrent preview-hydrate published first. In the latter case the
	// caller must destroy its just-created sandbox and attach to the
	// actualContainerID instead.
	AcquireTurnHold(ctx context.Context, orgID, sessionID uuid.UUID, proposedContainerID string) (actualContainerID string, err error)
	// SetWorkerNodeIDForContainer records which worker currently owns the live
	// container referenced by container_id.
	SetWorkerNodeIDForContainer(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID, workerNodeID string) error
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
	// ClearContainerID is the CAS-safe orphan reset: it nulls container_id
	// and clears the stuck turn_holding_container flag, but only when the
	// expected ID still matches AND no preview hold has appeared. Used by
	// the AcquireTurnHold race-loss path to recover from a stale orphan
	// container_id (worker crashed mid-turn, never released): the loser
	// probes IsAlive on actualContainerID, and if dead, calls ClearContainerID
	// so the retry can re-enter against a clean row.
	ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (cleared bool, err error)
	// ContainerHoldState returns whether the expected live container is held
	// by an agent turn, by a preview, or both. Used after an AcquireTurnHold
	// COALESCE loss so an alive preview hydrate is retried rather than
	// misclassified as a duplicate agent job.
	ContainerHoldState(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (turnHolds bool, previewHolds bool, err error)
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

type SessionThreadStore interface {
	UpdateStatus(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus) error
	CompleteTurn(ctx context.Context, orgID, threadID uuid.UUID, turnNumber int, agentSessionID string) error
	UpdateResult(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus, result *models.SessionResult) error
	// ClearPendingMessages resets the queued-message counter once the
	// orchestrator has re-enqueued a continue_session to drain the queue.
	// Called from drainQueuedMessages after the in-flight turn completes.
	ClearPendingMessages(ctx context.Context, orgID, threadID uuid.UUID) error
}

type SessionIssueLinkStore interface {
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionIssueLink, error)
}

type SessionIssueSnapshotStore interface {
	Create(ctx context.Context, snapshot *models.SessionTurnIssueSnapshot) error
	GetByTurn(ctx context.Context, orgID, sessionID uuid.UUID, turnNumber int) (models.SessionTurnIssueSnapshot, error)
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
	UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status string) error
}

// RepositoryStore defines the repository read operations.
type RepositoryStore interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

// JobStore defines the job enqueue operations. EnqueueWithTarget is the
// node-affinity variant: when targetNodeID is non-nil, only the matching
// worker (or workers picking up jobs released by a dead node) can claim
// the row. Used for sandbox-bound jobs where the work must run on the
// same docker daemon as the session's recorded container_id. The plain
// Enqueue maps to NULL target — any worker can claim — which is correct
// for unbound jobs (linear_milestone, sync_*, post-PR housekeeping).
type JobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	EnqueueWithTarget(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string, targetNodeID *string) (uuid.UUID, error)
	OldestPendingSessionJobAge(ctx context.Context) (time.Duration, bool, error)
}

// UsageRecorder tracks container lifecycle events for billing.
type UsageRecorder interface {
	ContainerStarted(ctx context.Context, orgID, sessionID uuid.UUID, sandbox *Sandbox, cfg SandboxConfig, startedAt time.Time) uuid.UUID
	ContainerStopped(ctx context.Context, orgID, sessionID uuid.UUID, eventID uuid.UUID, containerID string, startedAt time.Time, exitReason string)
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
	sessionThreads    SessionThreadStore
	sessionIssueLinks SessionIssueLinkStore
	issueSnapshots    SessionIssueSnapshotStore
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
	identityResolver  *identity.Resolver     // can be nil — falls back to legacy GITHUB_TOKEN env injection
	sandboxAuth       SandboxAuthServer      // can be nil — paired with identityResolver
	users             UserLookup             // can be nil — needed for App-token Co-authored-by trailer
	logger            zerolog.Logger
	maxConcurrent     int
	cancels           *CancelRegistry
	threadCancels     *ThreadCancelRegistry // optional — enables per-tab SIGINT
	nodeID            string
	isDraining        func() bool
}

// CancelThreadByID asks the thread-scoped cancel registry to SIGINT the
// in-flight agent for the given thread. Returns false when no live entry
// exists (the run already finished). Safe to call without a registry; just
// returns false.
func (o *Orchestrator) CancelThreadByID(threadID uuid.UUID) bool {
	if o.threadCancels == nil {
		return false
	}
	return o.threadCancels.CancelThread(threadID)
}

type workspaceSnapshotUpdater interface {
	UpdateWorkspaceSnapshot(ctx context.Context, orgID, sessionID uuid.UUID, snapshotKey string, result *models.SessionResult) error
}

// RevertThread applies a thread's stored diff in reverse against the latest
// durable session snapshot, then snapshots the resulting workspace and updates
// the session diff metadata so the UI reflects the reverted state.
func (o *Orchestrator) RevertThread(ctx context.Context, session *models.Session, thread *models.SessionThread) error {
	if session == nil {
		return fmt.Errorf("revert thread: session is nil")
	}
	if thread == nil {
		return fmt.Errorf("revert thread: thread is nil")
	}
	if thread.Diff == nil || strings.TrimSpace(*thread.Diff) == "" {
		return fmt.Errorf("revert thread: thread diff is empty")
	}
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		return fmt.Errorf("revert thread: session snapshot is unavailable")
	}
	if o.snapshots == nil {
		return fmt.Errorf("revert thread: snapshot store is unavailable")
	}

	updater, ok := o.sessions.(workspaceSnapshotUpdater)
	if !ok {
		return fmt.Errorf("revert thread: session store does not support workspace snapshot updates")
	}

	sandboxCfg := DefaultSandboxConfig()
	sandboxCfg.OrgID = session.OrgID.String()
	sandboxCfg.SessionID = session.ID.String()
	if slug, err := o.sessionRepoSlug(ctx, session); err != nil {
		return fmt.Errorf("revert thread: resolve workdir: %w", err)
	} else if slug != "" {
		sandboxCfg.WorkDir = sandboxCfg.HomeDir + "/" + slug
	}

	sandbox, err := HydrateSandboxFromSnapshot(ctx, o.provider, o.snapshots, *session.SnapshotKey, sandboxCfg)
	if err != nil {
		return fmt.Errorf("revert thread: hydrate sandbox: %w", err)
	}
	defer func() {
		if destroyErr := o.provider.Destroy(context.Background(), sandbox); destroyErr != nil {
			o.logger.Warn().Err(destroyErr).Str("session_id", session.ID.String()).Msg("failed to destroy revert sandbox")
		}
	}()

	patchPath := path.Join(sandbox.HomeDir, ".143-thread-revert.patch")
	if err := o.provider.WriteFile(ctx, sandbox, patchPath, []byte(*thread.Diff)); err != nil {
		return fmt.Errorf("revert thread: write patch: %w", err)
	}

	var stdout, stderr bytes.Buffer
	applyCmd := fmt.Sprintf("git apply -R '%s'", shellEscapeSingleQuote(patchPath))
	exitCode, err := o.provider.Exec(ctx, sandbox, applyCmd, &stdout, &stderr)
	if err != nil {
		return fmt.Errorf("revert thread: apply reverse patch: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("revert thread: apply reverse patch exited with code %d: %s", exitCode, strings.TrimSpace(stderr.String()))
	}

	targetBranch := ""
	if session.TargetBranch != nil {
		targetBranch = *session.TargetBranch
	}
	diff, diffErr := o.collectWorkspaceDiff(ctx, sandbox, derefString(session.BaseCommitSHA), targetBranch)
	if diffErr != nil && !errors.Is(diffErr, errNoBaseCommitSHA) {
		return fmt.Errorf("revert thread: collect session diff: %w", diffErr)
	}

	newSnapshotKey, _, err := o.snapshotSession(ctx, session, sandbox, nil)
	if err != nil {
		return fmt.Errorf("revert thread: snapshot updated workspace: %w", err)
	}
	if newSnapshotKey == "" && session.SnapshotKey != nil {
		newSnapshotKey = *session.SnapshotKey
	}

	result := &models.SessionResult{
		DiffBaseCommitSHA: session.BaseCommitSHA,
		DiffCollectedAt:   timePtr(time.Now().UTC()),
	}
	if diff != "" {
		result.Diff = &diff
	}
	if err := updater.UpdateWorkspaceSnapshot(ctx, session.OrgID, session.ID, newSnapshotKey, result); err != nil {
		return fmt.Errorf("revert thread: persist workspace snapshot: %w", err)
	}
	return nil
}

// DurableCheckpoint is the latest fully committed resume boundary for a
// session. Recovery may resume from this checkpoint, but never from newer
// in-memory state that was not durably persisted.
type DurableCheckpoint struct {
	Turn           int
	SnapshotKey    string
	AgentSessionID string
}

// ContinueSessionOptions carries execution-scoped overrides for a follow-up
// turn. Threaded sessions use this to run a tab with its selected agent/model
// while keeping the parent session row as the shared sandbox/session identity.
type ContinueSessionOptions struct {
	AgentType            models.AgentType
	ModelOverride        *string
	ThreadAgentSessionID *string
	ResultAgentSessionID *string
	PRRepair             *PRRepairContinueOptions

	// ThreadID, when set, identifies the agent tab this turn belongs to.
	// The orchestrator passes it to the thread cancel registry so a
	// per-tab Cancel can SIGINT only the matching agent process. nil
	// disables thread-scoped cancel and falls back to the legacy
	// session-level CancelRegistry behavior.
	ThreadID *uuid.UUID

	// OnTurnComplete fires after a successful turn with the agent's full
	// result. The thread continuation handler uses this both to emit file-
	// attribution events (from result.Diff) and to persist per-tab turn
	// metadata (result.Summary, result.ConfidenceScore, result.Diff) onto
	// the thread row so revert and the summary panel have data. Errors are
	// swallowed by the orchestrator: per-tab bookkeeping is operational,
	// not critical to the turn itself.
	OnTurnComplete func(result *AgentResult)
}

type PRRepairContinueOptions struct {
	PullRequestID     uuid.UUID
	RepairRunID       uuid.UUID
	PullRequestNumber int
	CommandType       models.PullRequestRepairActionType
	HealthVersion     int64
	HeadSHA           string
	WorkspaceMode     models.PullRequestRepairWorkspaceMode
}

// OrchestratorConfig holds the dependencies for creating an Orchestrator.
type OrchestratorConfig struct {
	Provider          SandboxProvider
	Adapters          map[models.AgentType]AgentAdapter
	Sessions          SessionStore
	SessionLogs       SessionLogStore
	SessionQuestions  SessionQuestionStore
	SessionMessages   SessionMessageStore
	SessionThreads    SessionThreadStore
	SessionIssueLinks SessionIssueLinkStore
	IssueSnapshots    SessionIssueSnapshotStore
	DecisionLog       DecisionLogStore
	ProjectTasks      ProjectTaskUpdater   // optional — updates project tasks on run completion
	AutomationRuns    AutomationRunUpdater // optional — updates automation_runs on session completion
	Issues            IssueStore
	Repositories      RepositoryStore
	Orgs              OrgStore
	Jobs              JobStore
	GitHub            GitHubTokenProvider
	CodexAuth         CodexAuthProvider      // optional — enables ChatGPT OAuth for Codex agent
	ClaudeCodeAuth    ClaudeCodeAuthProvider // optional — enables Claude subscription OAuth for Claude Code agent
	Credentials       CredentialProvider
	Memory            MemoryService            // optional — injects learned memories into agent prompts
	UserCredentials   UserCredentialProvider   // optional — enables legacy personal/team credential resolution
	CodingCredentials CodingCredentialProvider // optional — preferred unified resolver; consulted before the legacy cascade
	Snapshots         storage.SnapshotStore    // optional — enables multi-turn snapshot/restore
	UsageTracker      UsageRecorder            // optional — enables billing observability
	Cancels           *CancelRegistry          // optional — enables session cancellation from API
	ThreadCancels     *ThreadCancelRegistry    // optional — enables per-tab cancellation from API
	OrgSettingsCache  *OrgSettingsCache        // optional — caches Amp/Pi agent_config lookups across session starts
	// Env owns env resolution + auth pre-flight + Codex auth injection,
	// shared with the PM service. Optional: when nil, NewOrchestrator
	// constructs an AgentEnv from the other OrchestratorConfig fields so
	// existing call sites (notably tests) don't have to change.
	Env *AgentEnv
	// IdentityResolver picks a fresh GitHub token for each session (user
	// OAuth → App installation token fallback). Optional — when nil, the
	// orchestrator falls back to the legacy GITHUB_TOKEN env-var injection
	// path with no credential-helper. Pair with SandboxAuth and Users.
	IdentityResolver *identity.Resolver
	// SandboxAuth is the host-side socket server that the in-sandbox
	// 143-tools helper dials for fresh credentials. Required when
	// IdentityResolver is set.
	SandboxAuth SandboxAuthServer
	// Users looks up the triggering user record for the App-token
	// Co-authored-by trailer. Required when IdentityResolver is set and
	// the org has any user-triggered sessions.
	Users         UserLookup
	NodeID        string
	IsDraining    func() bool
	Logger        zerolog.Logger
	MaxConcurrent int
}

type sandboxGitHubAuthState struct {
	source string
	userID *uuid.UUID
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
			Credentials:       cfg.Credentials,
			UserCredentials:   cfg.UserCredentials,
			CodingCredentials: cfg.CodingCredentials,
			Orgs:              cfg.Orgs,
			OrgSettingsCache:  cfg.OrgSettingsCache,
			CodexAuth:         cfg.CodexAuth,
			Provider:          cfg.Provider,
			Logger:            cfg.Logger,
		})
	}

	return &Orchestrator{
		provider:          cfg.Provider,
		adapters:          cfg.Adapters,
		sessions:          cfg.Sessions,
		agentRunLogs:      cfg.SessionLogs,
		agentRunQuestions: cfg.SessionQuestions,
		sessionMessages:   cfg.SessionMessages,
		sessionThreads:    cfg.SessionThreads,
		sessionIssueLinks: cfg.SessionIssueLinks,
		issueSnapshots:    cfg.IssueSnapshots,
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
		identityResolver:  cfg.IdentityResolver,
		sandboxAuth:       cfg.SandboxAuth,
		users:             cfg.Users,
		cancels:           cfg.Cancels,
		threadCancels:     cfg.ThreadCancels,
		logger:            cfg.Logger,
		maxConcurrent:     maxConcurrent,
		nodeID:            cfg.NodeID,
		isDraining:        cfg.IsDraining,
	}
}

func defaultSandboxPATH() string {
	return "/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
}

func prependSandboxBinDir(env map[string]string, homeDir string) {
	if env == nil || homeDir == "" {
		return
	}
	// Use path (not path/filepath) intentionally: this builds an in-container
	// Linux path, and the orchestrator may run on a darwin host where
	// path/filepath would emit OS-native separators.
	binDir := path.Join(homeDir, ".local", "bin")
	current := env["PATH"]
	if current == "" {
		env["PATH"] = binDir + ":" + defaultSandboxPATH()
		return
	}
	for _, segment := range strings.Split(current, ":") {
		if segment == binDir {
			return
		}
	}
	env["PATH"] = binDir + ":" + current
}

// sandboxAuthOrgSettings loads the org's PR-authorship policy for the
// resolver. We surface lookup/parse errors rather than silently defaulting
// to UserPreferred: an org configured for app_only or user_required would
// otherwise issue a token under the wrong policy on a transient DB blip,
// which can leak the wrong identity onto a commit. With no orgs store
// wired (a few test paths) we still default — there's no source of truth
// to consult.
func (o *Orchestrator) sandboxAuthOrgSettings(ctx context.Context, orgID uuid.UUID) (models.OrgSettings, error) {
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	if o.orgs == nil {
		return settings, nil
	}
	org, err := o.orgs.GetByID(ctx, orgID)
	if err != nil {
		return settings, fmt.Errorf("load org settings for sandbox auth: %w", err)
	}
	parsed, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return settings, fmt.Errorf("parse org settings for sandbox auth: %w", err)
	}
	return parsed, nil
}

func (o *Orchestrator) prepareSandboxGitHubAuth(
	ctx context.Context,
	run *models.Session,
	repo *models.Repository,
	fallbackToken string,
	sandboxCfg *SandboxConfig,
	log zerolog.Logger,
) (*sandboxGitHubAuthState, error) {
	if repo == nil || sandboxCfg == nil {
		return nil, nil
	}
	if repo.FullName != "" {
		parts := strings.SplitN(repo.FullName, "/", 2)
		if len(parts) == 2 {
			sandboxCfg.Env["GITHUB_REPO_OWNER"] = parts[0]
			sandboxCfg.Env["GITHUB_REPO_NAME"] = parts[1]
		}
	}
	if o.identityResolver == nil || o.sandboxAuth == nil {
		log.Warn().
			Bool("identity_resolver_nil", o.identityResolver == nil).
			Bool("sandbox_auth_nil", o.sandboxAuth == nil).
			Bool("fallback_token_present", fallbackToken != "").
			Str("session_id", run.ID.String()).
			Msg("sandbox auth socket bridge unavailable; falling back to env token (legacy path)")
		if fallbackToken != "" {
			sandboxCfg.Env["GITHUB_TOKEN"] = fallbackToken
		}
		name, email := identity.CommitIdentity(nil)
		sandboxCfg.Env[sandboxauth.GitNameEnvVar] = name
		sandboxCfg.Env[sandboxauth.GitEmailEnvVar] = email
		prependSandboxBinDir(sandboxCfg.Env, sandboxCfg.HomeDir)
		if run.TriggeredByUserID != nil && o.users != nil {
			if user, userErr := o.users.GetByID(ctx, run.OrgID, *run.TriggeredByUserID); userErr == nil {
				if trailer := identity.CoAuthorTrailer(&user); trailer != "" {
					sandboxCfg.Env[sandboxauth.CoAuthorEnvVar] = trailer
				}
			} else {
				log.Warn().Err(userErr).Str("user_id", run.TriggeredByUserID.String()).Msg("failed to load triggering user for legacy co-author trailer")
			}
		}
		return nil, nil
	}

	// We Resolve once here to capture the bootstrap-time commit identity
	// (used to populate user.name/user.email and the Co-authored-by env)
	// and to stamp the audit row with which authority issued the *initial*
	// token. The host socket re-runs Resolve on every credential request,
	// so the per-push token may legitimately differ from this snapshot if
	// the user's OAuth grant or repo access changes mid-turn — that's
	// fine for security (each push gets fresh state) but means git author
	// metadata committed inside the sandbox can lag a mid-turn identity
	// flip until the next bootstrap.
	orgSettings, err := o.sandboxAuthOrgSettings(ctx, run.OrgID)
	if err != nil {
		return nil, err
	}
	resolveCtx, resolveCancel := context.WithTimeout(ctx, 30*time.Second)
	res, err := o.identityResolver.Resolve(resolveCtx, run, repo, orgSettings, "")
	resolveCancel()
	if err != nil {
		return nil, fmt.Errorf("resolve github identity: %w", err)
	}
	// The orchestrator's deferred cleanup goes through Server.Close(sessionID),
	// which mirrors how the listener is keyed inside the Server. Listen
	// no longer returns a per-call closer — the single-owner model means
	// every error path here just runs closeSandboxAuth(run.ID, ...) and
	// the Server figures out which entry is current.
	socketPath, err := o.sandboxAuth.Listen(ctx, run.ID, run, repo, orgSettings)
	if err != nil {
		log.Error().
			Err(err).
			Str("session_id", run.ID.String()).
			Msg("sandbox auth: failed to open per-session credential socket; sandbox push will fail with ECONNREFUSED until next turn or restart")
		return nil, fmt.Errorf("open sandbox auth socket: %w", err)
	}
	log.Info().
		Str("session_id", run.ID.String()).
		Str("socket_path", socketPath).
		Str("identity_source", string(res.AuthoredBy())).
		Bool("user_token", res.IsUserToken()).
		Msg("sandbox auth: per-session credential socket wired into sandbox")
	sandboxCfg.AuthSocketPath = socketPath
	name, email := identity.CommitIdentity(res)
	sandboxCfg.Env[sandboxauth.SocketEnvVar] = sandboxauth.SandboxSocketPath
	sandboxCfg.Env[sandboxauth.GitNameEnvVar] = name
	sandboxCfg.Env[sandboxauth.GitEmailEnvVar] = email
	prependSandboxBinDir(sandboxCfg.Env, sandboxCfg.HomeDir)
	if !res.IsUserToken() && run.TriggeredByUserID != nil && o.users != nil {
		if user, userErr := o.users.GetByID(ctx, run.OrgID, *run.TriggeredByUserID); userErr == nil {
			if trailer := identity.CoAuthorTrailer(&user); trailer != "" {
				sandboxCfg.Env[sandboxauth.CoAuthorEnvVar] = trailer
			}
		} else {
			log.Warn().Err(userErr).Str("user_id", run.TriggeredByUserID.String()).Msg("failed to load triggering user for co-author trailer")
		}
	}
	authState := &sandboxGitHubAuthState{source: res.AuthoredBy()}
	if res.IsUserToken() && run.TriggeredByUserID != nil {
		authState.userID = run.TriggeredByUserID
	}
	return authState, nil
}

func (o *Orchestrator) closeSandboxAuth(sessionID uuid.UUID, log zerolog.Logger) {
	if o.sandboxAuth == nil {
		return
	}
	o.sandboxAuth.Close(sessionID)
	log.Debug().Msg("closed sandbox auth socket")
}

func (o *Orchestrator) runSandboxGitBootstrap(ctx context.Context, sandbox *Sandbox, workDir string, log zerolog.Logger) {
	if sandbox == nil || workDir == "" {
		return
	}
	bootstrapCmd := fmt.Sprintf("143-tools git-bootstrap --workdir=%s", shellEscapeSingleQuote(workDir))
	var bootOut, bootErr bytes.Buffer
	if code, err := o.provider.Exec(ctx, sandbox, bootstrapCmd, &bootOut, &bootErr); err != nil || code != 0 {
		log.Warn().
			Err(err).
			Int("exit_code", code).
			Str("stderr", bootErr.String()).
			Msg("git-bootstrap failed; agent will lack github push credentials")
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
	startedAt := time.Now().UTC()
	if err := o.sessions.UpdateRecoveryState(ctx, session.OrgID, session.ID, models.RecoveryStateRecovering, nil, &startedAt, true); err != nil {
		log.Warn().Err(err).Msg("failed to mark session recovery as in progress")
	}
	defer func() {
		if err := o.sessions.UpdateRecoveryState(context.Background(), session.OrgID, session.ID, models.RecoveryStateNone, nil, nil, false); err != nil {
			log.Warn().Err(err).Msg("failed to clear session recovery state")
		}
	}()

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

	return o.ContinueSession(ctx, session, nil)
}

func (o *Orchestrator) beginRuntimeControl(ctx context.Context, controller *runtimeController, orgID, sessionID uuid.UUID, fallbackStatus string, capability models.CheckpointCapability, startedAt time.Time, log zerolog.Logger) error {
	if err := controller.Begin(ctx, startedAt, capability); err != nil {
		if rollbackErr := o.sessions.UpdateStatus(ctx, orgID, sessionID, fallbackStatus); rollbackErr != nil {
			log.Warn().Err(rollbackErr).Str("fallback_status", fallbackStatus).Msg("failed to roll back session status after runtime initialization error")
		}
		return fmt.Errorf("begin runtime control: %w", err)
	}
	go controller.Run(ctx)
	return nil
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

func latestUserMessage(messages []models.SessionMessage) *models.SessionMessage {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == models.MessageRoleUser {
			return &messages[i]
		}
	}
	return nil
}

// latestUserMessageInScope returns the most recent user message bound to the
// requested scope. When threadID is nil the scope is the session-level
// conversation (messages with no thread); when threadID is non-nil the scope
// is that specific thread.
//
// The scoped form exists because session_messages.ListBySession orders rows by
// (turn_number, id) — and per-thread turn counters are independent — so the
// globally-last user row in the slice is not necessarily the last message the
// user actually sent. ContinueSession must scope to the thread the worker
// payload identified, otherwise a sibling thread with a higher turn_number
// will steal the run.
func latestUserMessageInScope(messages []models.SessionMessage, threadID *uuid.UUID) *models.SessionMessage {
	matchesScope := func(m models.SessionMessage) bool {
		if threadID == nil {
			return m.ThreadID == nil
		}
		return m.ThreadID != nil && *m.ThreadID == *threadID
	}
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != models.MessageRoleUser {
			continue
		}
		if !matchesScope(m) {
			continue
		}
		return &messages[i]
	}
	return nil
}

// unprocessedUserMessages returns the consecutive trailing user messages for
// a given thread scope — i.e. all user messages after the most recent
// in-scope assistant message. When threadID is nil the scope is the
// session-level conversation (messages with no thread); when threadID is
// non-nil the scope is that specific thread.
//
// Used by ContinueSession to bundle rapid-fire mid-turn user sends into a
// single agent invocation. Without this, the orchestrator processes only
// the very latest user message and silently drops earlier queued ones.
// Returned messages are in oldest-first order.
func unprocessedUserMessages(messages []models.SessionMessage, threadID *uuid.UUID) []models.SessionMessage {
	return unprocessedUserMessagesThrough(messages, threadID, 1<<63-1)
}

// unprocessedUserMessagesThrough is the boundary-aware form used when a later
// assistant message may already exist in the timeline. Mid-turn queued user
// rows are inserted before the in-flight turn's assistant row; when the drain
// job starts, that assistant must not hide the queued users.
func unprocessedUserMessagesThrough(messages []models.SessionMessage, threadID *uuid.UUID, latestUserMessageID int64) []models.SessionMessage {
	matchesScope := func(m models.SessionMessage) bool {
		if threadID == nil {
			return m.ThreadID == nil
		}
		return m.ThreadID != nil && *m.ThreadID == *threadID
	}
	var pending []models.SessionMessage
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.ID > latestUserMessageID {
			continue
		}
		if !matchesScope(m) {
			continue
		}
		if m.Role == models.MessageRoleAssistant {
			break
		}
		if m.Role == models.MessageRoleUser {
			pending = append(pending, m)
		}
	}
	for i, j := 0, len(pending)-1; i < j; i, j = i+1, j-1 {
		pending[i], pending[j] = pending[j], pending[i]
	}
	return pending
}

func canonicalReferences(message *models.SessionMessage) []models.SessionInputReference {
	if message == nil || len(message.References) == 0 {
		return nil
	}
	references := make([]models.SessionInputReference, 0, len(message.References))
	for _, reference := range message.References {
		if err := reference.Validate(); err != nil {
			continue
		}
		references = append(references, reference)
	}
	return references
}

func isLegacySyntheticManualSession(session *models.Session, issue *models.Issue) bool {
	if session == nil || issue == nil || issue.Source != models.IssueSourceManual {
		return false
	}
	if session.ProjectTaskID != nil || session.AutomationRunID != nil || session.ParentSessionID != nil {
		return false
	}
	return session.Origin == "" || session.Origin == models.SessionOriginIssueTrigger
}

// canonicalCommands filters a message's commands[] to those that pass
// validation and that target the session's agent. Commands targeting another
// agent are silently dropped here: the API layer rejects them at submit time,
// so the only way they reach this code path is a stale row in storage where
// the user later switched the session's agent — surfacing the orphan to the
// adapter would only confuse the prompt.
func canonicalCommands(message *models.SessionMessage, agentType models.AgentType) []models.SessionInputCommand {
	if message == nil || len(message.Commands) == 0 {
		return nil
	}
	commands := make([]models.SessionInputCommand, 0, len(message.Commands))
	for _, command := range message.Commands {
		if err := command.Validate(); err != nil {
			continue
		}
		if agentType != "" && command.AgentType != agentType {
			continue
		}
		commands = append(commands, command)
	}
	return commands
}

func hydrateSessionPolicyForExecution(session *models.Session, issue *models.Issue) {
	if session == nil {
		return
	}
	legacyManual := isLegacySyntheticManualSession(session, issue)
	if legacyManual {
		session.Origin = models.SessionOriginManual
	}
	if session.Origin == "" {
		switch {
		case issue != nil && issue.Source == models.IssueSourceManual:
			session.Origin = models.SessionOriginManual
		case session.TriggeredByUserID != nil:
			session.Origin = models.SessionOriginManual
		case session.ProjectTaskID != nil:
			session.Origin = models.SessionOriginProject
		case session.AutomationRunID != nil:
			session.Origin = models.SessionOriginAutomation
		case session.ParentSessionID != nil:
			session.Origin = models.SessionOriginRevision
		default:
			session.Origin = models.SessionOriginIssueTrigger
		}
	}
	if legacyManual && (session.InteractionMode == "" || session.InteractionMode == models.SessionInteractionModeSingleRun) {
		session.InteractionMode = models.SessionInteractionModeInteractive
	} else if session.InteractionMode == "" {
		if session.Origin == models.SessionOriginManual {
			session.InteractionMode = models.SessionInteractionModeInteractive
		} else {
			session.InteractionMode = models.SessionInteractionModeSingleRun
		}
	}
	if legacyManual && (session.ValidationPolicy == "" || session.ValidationPolicy == models.SessionValidationPolicyOnTurnComplete) {
		session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
	} else if session.ValidationPolicy == "" {
		if session.Origin == models.SessionOriginManual {
			session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
		} else {
			session.ValidationPolicy = models.SessionValidationPolicyOnTurnComplete
		}
	}
}

func primaryLinkedIssue(links []models.SessionIssueLink) *models.SessionIssueLink {
	for i := range links {
		if links[i].Role == models.SessionIssueLinkRolePrimary {
			return &links[i]
		}
	}
	return nil
}

func snapshotEntriesFromLinks(links []models.SessionIssueLink) ([]models.SessionIssueSnapshotEntry, error) {
	entries := make([]models.SessionIssueSnapshotEntry, 0, len(links))
	for _, link := range links {
		entry := models.SessionIssueSnapshotEntry{
			IssueID:      link.IssueID,
			Role:         link.Role,
			Position:     link.Position,
			Source:       models.IssueSourcePMAgent,
			RepositoryID: link.RepositoryID,
		}
		if link.IssueTitle != nil {
			entry.Title = *link.IssueTitle
		}
		if link.ExternalID != nil {
			entry.ExternalID = *link.ExternalID
		}
		if link.Description != nil {
			entry.Description = *link.Description
		}
		if link.IssueStatus != nil {
			entry.Status = *link.IssueStatus
		}
		if link.IssueSource != nil {
			entry.Source = *link.IssueSource
		}
		if err := applyLinearPrimarySnapshot(&entry, link.RawLinearPrimarySnapshot); err != nil {
			return nil, fmt.Errorf("decode linear primary snapshot for link %s: %w", link.ID, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func applyLinearPrimarySnapshot(entry *models.SessionIssueSnapshotEntry, raw json.RawMessage) error {
	if entry == nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var snapshot linear.LinearTurnContext
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	if snapshot.Identifier != "" {
		entry.ExternalID = snapshot.Identifier
	}
	if snapshot.Title != "" {
		entry.Title = snapshot.Title
	}
	if snapshot.Description != "" {
		entry.Description = snapshot.Description
	}
	entry.StateName = snapshot.StateName
	entry.StateType = snapshot.StateType
	entry.Priority = snapshot.Priority
	entry.AssigneeName = snapshot.AssigneeName
	entry.TeamKey = snapshot.TeamKey
	entry.TeamName = snapshot.TeamName
	entry.URL = snapshot.URL
	if len(snapshot.Attachments) > 0 {
		entry.Attachments = make([]models.SessionIssueSnapshotAttachment, 0, len(snapshot.Attachments))
		for _, attachment := range snapshot.Attachments {
			entry.Attachments = append(entry.Attachments, models.SessionIssueSnapshotAttachment{
				Title:  attachment.Title,
				URL:    attachment.URL,
				Source: attachment.Source,
			})
		}
	}
	if len(snapshot.Comments) > 0 {
		entry.Comments = make([]models.SessionIssueSnapshotComment, 0, len(snapshot.Comments))
		for _, comment := range snapshot.Comments {
			entry.Comments = append(entry.Comments, models.SessionIssueSnapshotComment{
				Author:    comment.Author,
				Body:      comment.Body,
				CreatedAt: comment.CreatedAt,
			})
		}
	}
	return nil
}

func (o *Orchestrator) createIssueSnapshotForTurn(ctx context.Context, session *models.Session, turnNumber int) (*models.SessionTurnIssueSnapshot, error) {
	if o.issueSnapshots == nil || o.sessionIssueLinks == nil {
		return nil, nil
	}
	links, err := o.sessionIssueLinks.ListBySession(ctx, session.OrgID, session.ID)
	if err != nil {
		return nil, fmt.Errorf("list session issue links: %w", err)
	}
	if len(links) > 0 && primaryLinkedIssue(links) == nil {
		return nil, fmt.Errorf("execution requires exactly one primary issue when links are present")
	}
	linkedIssues, err := snapshotEntriesFromLinks(links)
	if err != nil {
		return nil, err
	}
	snapshot := &models.SessionTurnIssueSnapshot{
		OrgID:        session.OrgID,
		SessionID:    session.ID,
		TurnNumber:   turnNumber,
		LinkedIssues: linkedIssues,
	}
	if err := o.issueSnapshots.Create(ctx, snapshot); err != nil {
		return nil, fmt.Errorf("create issue snapshot: %w", err)
	}
	return snapshot, nil
}

func issueFromSnapshotEntry(entry *models.SessionIssueSnapshotEntry) *models.Issue {
	if entry == nil {
		return nil
	}
	issue := &models.Issue{
		ID:           entry.IssueID,
		Source:       entry.Source,
		RepositoryID: entry.RepositoryID,
		Title:        entry.Title,
	}
	if entry.ExternalID != "" {
		issue.ExternalID = entry.ExternalID
	}
	if entry.Description != "" {
		description := entry.Description
		issue.Description = &description
	}
	if entry.Status != "" {
		issue.Status = entry.Status
	}
	return issue
}

func (o *Orchestrator) promptSeedForSession(session *models.Session, latestMessage *models.SessionMessage, snapshot *models.SessionTurnIssueSnapshot) (*models.Issue, []models.SessionIssueSnapshotEntry) {
	var linkedIssues []models.SessionIssueSnapshotEntry
	if snapshot != nil {
		linkedIssues = snapshot.LinkedIssues
	}
	var primary *models.SessionIssueSnapshotEntry
	for i := range linkedIssues {
		if linkedIssues[i].Role == models.SessionIssueLinkRolePrimary {
			primary = &linkedIssues[i]
			break
		}
	}
	if primary != nil {
		return issueFromSnapshotEntry(primary), linkedIssues
	}
	if session.Origin == models.SessionOriginManual {
		message := ""
		if latestMessage != nil {
			message = latestMessage.Content
		}
		title := "Manual Session"
		if session.Title != nil && strings.TrimSpace(*session.Title) != "" {
			title = *session.Title
		}
		issue := &models.Issue{
			Source: models.IssueSourceManual,
			Title:  title,
		}
		if strings.TrimSpace(message) != "" {
			issue.Description = &message
		}
		return issue, linkedIssues
	}

	title := syntheticIssueTitleForSession(session, latestMessage)
	var descriptionParts []string
	if session.PMApproach != nil && strings.TrimSpace(*session.PMApproach) != "" {
		descriptionParts = append(descriptionParts, *session.PMApproach)
	}
	if session.PMReasoning != nil && strings.TrimSpace(*session.PMReasoning) != "" {
		descriptionParts = append(descriptionParts, *session.PMReasoning)
	}
	issue := &models.Issue{
		Source:       models.IssueSourcePMAgent,
		RepositoryID: session.RepositoryID,
		Title:        title,
	}
	if len(descriptionParts) > 0 {
		description := strings.Join(descriptionParts, "\n\n")
		issue.Description = &description
	}
	return issue, linkedIssues
}

func syntheticIssueTitleForSession(session *models.Session, latestMessage *models.SessionMessage) string {
	if session.Title != nil && strings.TrimSpace(*session.Title) != "" {
		return strings.TrimSpace(*session.Title)
	}
	if latestMessage != nil {
		if title := syntheticIssueTitleFragment(latestMessage.Content); title != "" {
			return title
		}
	}
	if session.PMApproach != nil {
		if title := syntheticIssueTitleFragment(*session.PMApproach); title != "" {
			return title
		}
	}
	if session.PMReasoning != nil {
		if title := syntheticIssueTitleFragment(*session.PMReasoning); title != "" {
			return title
		}
	}
	return "Session"
}

func syntheticIssueTitleFragment(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if idx := strings.Index(trimmed, "\n"); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	if len(trimmed) <= 120 {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:120]) + "..."
}

func issueSnapshotEntriesFromIssue(issue *models.Issue) []models.SessionIssueSnapshotEntry {
	if issue == nil {
		return nil
	}
	entry := models.SessionIssueSnapshotEntry{
		IssueID:      issue.ID,
		Role:         models.SessionIssueLinkRolePrimary,
		Position:     0,
		Source:       issue.Source,
		RepositoryID: issue.RepositoryID,
		Title:        issue.Title,
		ExternalID:   issue.ExternalID,
		Status:       issue.Status,
	}
	if issue.Description != nil {
		entry.Description = *issue.Description
	}
	return []models.SessionIssueSnapshotEntry{entry}
}

// resolvePromptSeed resolves the *models.Issue used to seed prompt construction.
//
// Priority:
//  1. A snapshot- or synthetic-derived seed produced by promptSeedForSession.
//     For a session with a primary linked issue, this comes straight from the
//     per-turn snapshot. For a manual session, it is an in-memory placeholder
//     carrying the session title/message — not a persisted issue row.
//  2. If the synthetic seed carries no identifying context and the session has
//     a primary issue, fetch that issue from the DB as a defensive fallback for
//     edge cases where snapshot hydration returned a placeholder.
//
// The returned []SessionIssueSnapshotEntry is the linked-issue context that
// should flow into the agent prompt (primary first, then related).
func (o *Orchestrator) resolvePromptSeed(ctx context.Context, session *models.Session, latestMessage *models.SessionMessage, snapshot *models.SessionTurnIssueSnapshot) (*models.Issue, []models.SessionIssueSnapshotEntry, error) {
	issue, linkedIssues := o.promptSeedForSession(session, latestMessage, snapshot)
	if issue != nil && (issue.ID != uuid.Nil || session.Origin == models.SessionOriginManual) {
		hydrateSessionPolicyForExecution(session, issue)
		return issue, linkedIssues, nil
	}

	if session.PrimaryIssueID == nil || o.issues == nil {
		hydrateSessionPolicyForExecution(session, issue)
		return issue, linkedIssues, nil
	}

	// Defensive fallback: after phase 2 of the session/issue decoupling,
	// createIssueSnapshotForTurn should have populated a snapshot with the
	// primary issue hydrated from session_issue_links. If we get here, the
	// snapshot was empty or missing — log so ops can detect silent snapshot
	// regressions rather than having them masked by a live join-table read.
	o.logger.Warn().
		Str("session_id", session.ID.String()).
		Str("org_id", session.OrgID.String()).
		Str("primary_issue_id", session.PrimaryIssueID.String()).
		Msg("resolvePromptSeed falling back to live issue lookup; expected per-turn snapshot")
	fallbackIssue, err := o.issues.GetByID(ctx, session.OrgID, *session.PrimaryIssueID)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch primary issue: %w", err)
	}
	hydrateSessionPolicyForExecution(session, &fallbackIssue)
	return &fallbackIssue, issueSnapshotEntriesFromIssue(&fallbackIssue), nil
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
	var primaryThreadID *uuid.UUID
	if run.PrimaryThreadID != nil && *run.PrimaryThreadID != uuid.Nil {
		threadID := *run.PrimaryThreadID
		primaryThreadID = &threadID
		if o.sessionThreads != nil {
			if err := o.sessionThreads.UpdateStatus(ctx, run.OrgID, threadID, models.ThreadStatusRunning); err != nil {
				log.Warn().Err(err).Str("thread_id", threadID.String()).Msg("failed to mark primary thread running")
			}
		}
	}
	if run.PrimaryIssueID != nil && o.issues != nil {
		if err := o.issues.UpdateStatus(ctx, run.OrgID, *run.PrimaryIssueID, "in_progress"); err != nil {
			log.Warn().Err(err).Str("issue_id", run.PrimaryIssueID.String()).Msg("failed to update primary issue status to in_progress")
		}
	}
	// Fire the Linear state-transition signal exactly once per session, when
	// the session actually starts running. Linking alone (paste a `ACS-1234`
	// into the textarea) no longer moves the issue — the issue only flips to
	// "in progress" once the orchestrator picks the session up and runs it.
	// The fire-once unique constraint on (session_id, issue_id, "started")
	// makes a re-entry from a resumed/retried run a no-op, so this call is
	// safe on every RunAgent invocation.
	o.enqueueLinearMilestone(ctx, run, string(linear.MilestoneStarted))
	// runStartedAt is captured AFTER UpdateStatus, so the elapsed reported on
	// a timeout excludes concurrency check + status write but includes
	// everything from issue fetch onward (sandbox create, credential inject,
	// agent execute, snapshot). An on-call reader seeing a sub-minute
	// elapsed on a 25-minute timeout should suspect the deadline fired
	// during sandbox creation, not the adapter.
	runStartedAt := time.Now()
	runtimeCfg := o.resolveRuntimeConfig(ctx, run.OrgID)
	runtimeTracker := newRuntimeProgressTracker(runStartedAt)
	runtimeController := newRuntimeController(runtimeCfg, o.sessions, o.jobs, o.cancels, log, run.OrgID, run.ID, o.maxConcurrent, o.isDraining, runtimeTracker)
	if err := o.beginRuntimeControl(ctx, runtimeController, run.OrgID, run.ID, string(models.SessionStatusPending), checkpointCapabilityForAgent(run.AgentType), runStartedAt, log); err != nil {
		return err
	}

	turnNumber := run.CurrentTurn + 1
	issueSnapshot, err := o.createIssueSnapshotForTurn(ctx, run, turnNumber)
	if err != nil {
		o.failRun(ctx, run, fmt.Sprintf("resolve issue context: %s", err))
		return fmt.Errorf("resolve issue context: %w", err)
	}
	var messages []models.SessionMessage
	if o.sessionMessages != nil {
		messages, err = o.sessionMessages.ListBySession(ctx, run.OrgID, run.ID)
		if err != nil {
			o.failRun(ctx, run, fmt.Sprintf("fetch session messages: %s", err))
			return fmt.Errorf("fetch session messages: %w", err)
		}
	}
	issue, linkedIssues, err := o.resolvePromptSeed(ctx, run, latestUserMessage(messages), issueSnapshot)
	if err != nil {
		o.failRun(ctx, run, fmt.Sprintf("resolve prompt seed: %s", err))
		return fmt.Errorf("resolve prompt seed: %w", err)
	}
	if issue != nil && issue.ID != uuid.Nil {
		run.PrimaryIssueID = &issue.ID
	}
	hydrateSessionPolicyForExecution(run, issue)

	// 4. Resolve which repository to clone. sessions.repository_id is the
	// canonical source of truth — session creation copies issue.repository_id
	// into it up front, so execution never needs to re-derive repo from the
	// primary issue.
	var resolvedRepoID *uuid.UUID
	if run.RepositoryID != nil {
		resolvedRepoID = run.RepositoryID
	} else if issue != nil && issue.RepositoryID != nil {
		resolvedRepoID = issue.RepositoryID
	}

	var (
		repoURL, branch, token, repoFullName string
		authRepo                             *models.Repository
	)
	var designatedWorkingBranch string
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
		repoCopy := repo
		authRepo = &repoCopy
		designatedWorkingBranch = sessionWorkingBranch(run, issue)
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
		Issue:        issue,
		LinkedIssues: linkedIssues,
		Manual:       run.Origin == models.SessionOriginManual,
		PromptStyle: func() PromptStyle {
			if run.Origin == models.SessionOriginManual || run.AutomationRunID != nil {
				return PromptStyleRawTask
			}
			return PromptStyleIssueContext
		}(),
		UserMessage: func() string {
			if msg := latestUserMessage(messages); msg != nil {
				return msg.Content
			}
			if run.AutomationRunID != nil && run.PMApproach != nil {
				return strings.TrimSpace(*run.PMApproach)
			}
			return ""
		}(),
		RepoURL:    repoURL,
		RepoBranch: branch,
		References: func() []models.SessionInputReference {
			refs := canonicalReferences(latestUserMessage(messages))
			if len(refs) > 0 {
				return refs
			}
			return manualSessionReferences(issue)
		}(),
		Commands: canonicalCommands(latestUserMessage(messages), run.AgentType),
		ReasoningEffort: func() models.ReasoningEffort {
			if run.ReasoningEffort == nil {
				return ""
			}
			return *run.ReasoningEffort
		}(),
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

	if run.AutomationRunID == nil && (run.PMApproach != nil || run.PMReasoning != nil) {
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
	if revisionContext, revErr := ParseRevisionContext(run.RevisionContext); revErr != nil {
		log.Warn().Err(revErr).Msg("failed to parse session revision context")
	} else {
		input.RevisionContext = revisionContext
	}

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
	// Apply per-run model override before the auth pre-flight so the sandbox
	// sees the effective model/mode selection rather than only the org default.
	if run.ModelOverride != nil && *run.ModelOverride != "" {
		if envVar := models.ModelEnvVarForAgentType(run.AgentType); envVar != "" {
			sandboxCfg.Env[envVar] = *run.ModelOverride
		}
	}
	if designatedWorkingBranch != "" {
		sandboxCfg.Env[sandboxauth.WorkingBranchEnvVar] = designatedWorkingBranch
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
	authState, authErr := o.prepareSandboxGitHubAuth(ctx, run, authRepo, token, &sandboxCfg, log)
	if authErr != nil {
		o.failRun(ctx, run, authErr.Error())
		return authErr
	}
	if _, ok := sandboxCfg.Env["HOME"]; !ok {
		sandboxCfg.Env["HOME"] = sandboxCfg.HomeDir
	}
	sandbox, err := o.provider.Create(ctx, sandboxCfg)
	if err != nil {
		if sandboxCfg.AuthSocketPath != "" {
			o.closeSandboxAuth(run.ID, log)
		}
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
		if sandboxCfg.AuthSocketPath != "" {
			o.closeSandboxAuth(run.ID, log)
		}
		o.failRun(ctx, run, fmt.Sprintf("acquire turn hold: %s", holdErr))
		return fmt.Errorf("acquire turn hold: %w", holdErr)
	}
	if actualContainerID != "" && actualContainerID != sandbox.ID {
		destroyCtx := context.Background()
		if destroyErr := o.provider.Destroy(destroyCtx, sandbox); destroyErr != nil {
			log.Error().Err(destroyErr).Str("losing_container_id", sandbox.ID).Msg("failed to destroy sandbox after losing hydrate race")
		}
		if sandboxCfg.AuthSocketPath != "" {
			o.closeSandboxAuth(run.ID, log)
		}
		// Self-heal: ask the diagnosis helper whether the "winner" is alive
		// (real duplicate → dead-letter via ErrSandboxRaceLoser), an alive
		// preview holder (retry), or a stale orphan (worker crashed mid-turn
		// → CAS-clear and signal retry via ErrStaleSandboxIDCleared). Either
		// way, do NOT failRun — the winner (if any) owns the session row, and
		// the orphan path leaves the row pending for the retry to re-enter
		// cleanly.
		log.Warn().
			Str("winning_container_id", actualContainerID).
			Str("losing_container_id", sandbox.ID).
			Msg("another holder published container_id first; diagnosing whether the winner is alive or a stale orphan")
		diagErr := o.diagnoseAcquireHoldRaceLoss(ctx, run.OrgID, run.ID, actualContainerID, log)
		if errors.Is(diagErr, ErrSandboxPreviewRace) {
			if revertErr := o.sessions.UpdateStatus(ctx, run.OrgID, run.ID, string(models.SessionStatusPending)); revertErr != nil {
				log.Error().Err(revertErr).Msg("failed to revert session to pending after preview won sandbox race")
			}
		}
		return fmt.Errorf("%w: actual container %s != created %s", diagErr, actualContainerID, sandbox.ID)
	}
	defer func() {
		exitReason := containerExitReason(ctx, err)
		if o.usageTracker != nil {
			// Use a detached context so the billing write succeeds even if
			// the parent ctx was cancelled (timeout, shutdown).
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopCancel()
			o.usageTracker.ContainerStopped(stopCtx, run.OrgID, run.ID, usageEventID, sandbox.ID, containerStartedAt, exitReason)
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
		if sandboxCfg.AuthSocketPath != "" {
			o.closeSandboxAuth(run.ID, log)
		}
	}()
	if o.nodeID != "" {
		if err := o.sessions.SetWorkerNodeIDForContainer(ctx, run.OrgID, run.ID, sandbox.ID, o.nodeID); err != nil {
			log.Error().Err(err).
				Str("container_id", sandbox.ID).
				Str("worker_node_id", o.nodeID).
				Msg("persist session worker ownership: CAS failed (container_id moved or worker_node_id held by another worker)")
			o.failRun(ctx, run, fmt.Sprintf("persist session worker ownership: %s", err))
			return fmt.Errorf("persist session worker ownership: %w", err)
		}
	}

	// Register the session with the cancel registry so RequestStop can
	// deliver a graceful interrupt. The actual interactive command handle
	// is attached lazily once the adapter starts it (see runInteractiveAgent
	// in the adapters package); until that point CancelSession falls back
	// to context cancellation.
	if o.cancels != nil {
		o.cancels.Register(run.ID, cancel, ResolveCancellationSpec(adapter))
	}

	// 8. Clone repo into sandbox. This must happen before auth injection
	// so that /workspace is empty when git clone runs (git clone fails on
	// non-empty directories).
	if repoURL != "" {
		if err := o.provider.CloneRepo(ctx, sandbox, repoURL, branch, token); err != nil {
			o.failRun(ctx, run, fmt.Sprintf("clone repo: %s", err))
			return fmt.Errorf("clone repo: %w", err)
		}

		baseCommitSHA, err := o.captureBaseCommitSHA(ctx, sandbox)
		if err != nil {
			log.Warn().Err(err).Msg("failed to capture base commit sha")
		} else if baseCommitSHA != "" {
			if sandbox.Metadata == nil {
				sandbox.Metadata = make(map[string]string)
			}
			sandbox.Metadata[SandboxMetadataBaseCommitSHA] = baseCommitSHA
			run.BaseCommitSHA = &baseCommitSHA
			if dbErr := o.sessions.UpdateBaseCommitSHA(ctx, run.OrgID, run.ID, baseCommitSHA); dbErr != nil {
				log.Warn().Err(dbErr).Str("base_commit_sha", baseCommitSHA).Msg("failed to persist base commit sha")
			}
		}

		// Stamp the resolved target branch (the branch we just cloned from —
		// repo default unless the session overrode it) onto sandbox.Metadata
		// so sessiondiff.Collect can compute a merge-base diff against
		// origin/<branch>. Without this the diff is taken against the frozen
		// baseCommitSHA, which inflates by the entire delta from base to HEAD
		// whenever the user integrates the target branch back into the working
		// branch (e.g. `git pull origin main` or merging main to resolve PR
		// conflicts). Empty branch is unexpected in this path (we just cloned
		// from it) but we guard anyway — Collect treats empty target branch
		// as "fall back to baseCommitSHA".
		if branch != "" {
			if sandbox.Metadata == nil {
				sandbox.Metadata = make(map[string]string)
			}
			sandbox.Metadata[SandboxMetadataTargetBranch] = branch
		}

		// 8b. Create a working branch so the agent operates on a separate
		// branch from the start, keeping the base branch clean.
		workingBranch := designatedWorkingBranch
		checkoutCmd := fmt.Sprintf("git checkout -b '%s'", shellEscapeSingleQuote(workingBranch))
		var checkoutOut, checkoutErr bytes.Buffer
		exitCode, execErr := o.provider.Exec(ctx, sandbox, checkoutCmd, &checkoutOut, &checkoutErr)
		if execErr != nil || exitCode != 0 {
			o.failRun(ctx, run, fmt.Sprintf("create working branch: exit=%d err=%v stderr=%s", exitCode, execErr, checkoutErr.String()))
			if execErr != nil {
				return fmt.Errorf("create working branch %s: exit=%d err=%w stderr=%s", workingBranch, exitCode, execErr, checkoutErr.String())
			}
			return fmt.Errorf("create working branch %s: exit=%d stderr=%s", workingBranch, exitCode, checkoutErr.String())
		}
		run.WorkingBranch = &workingBranch
		if dbErr := o.sessions.UpdateWorkingBranch(ctx, run.OrgID, run.ID, workingBranch); dbErr != nil {
			log.Warn().Err(dbErr).Str("branch", workingBranch).Msg("failed to persist working branch")
		}

		// 8c. Wire git/gh inside the sandbox up to the per-session credential
		// helper. Runs *after* CloneRepo and the working-branch checkout so
		// the .git directory exists; idempotent so it's safe to re-run on
		// session resume. Skipped when the auth socket isn't bound (legacy
		// or non-integration path).
		o.runSandboxGitBootstrap(ctx, sandbox, sandboxCfg.WorkDir, log)
		// 8d. Stamp git identity on the session row for audit. Best-effort —
		// a failure here only affects post-hoc reporting, not the run.
		if authState != nil && authState.source != "" {
			if dbErr := o.sessions.SetGitIdentity(ctx, run.OrgID, run.ID, authState.source, authState.userID); dbErr != nil {
				log.Warn().Err(dbErr).Str("source", authState.source).Msg("failed to persist git identity audit")
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
		mode, err := o.ensureCodexAuth(ctx, run, sandbox, sandboxCfg.Env)
		if err != nil {
			return err
		}
		prompt.UsageHint.BillingMode = mode
	case models.AgentTypeClaudeCode:
		mode, err := o.ensureClaudeCodeAuth(ctx, run, sandbox, sandboxCfg.Env)
		if err != nil {
			return err
		}
		prompt.UsageHint.BillingMode = mode
	}

	// 9b. Integration tools (143-tools CLI) are pre-installed in the container
	// image. Credentials are injected via env vars (AgentEnv.Resolve), and the
	// skills doc is injected into the prompt (BuildIntegrationSkills). No
	// per-CLI config file injection needed — all agents can shell out directly.
	prompt.UsageHint = o.buildTokenUsageHint(ctx, run.AgentType, run.OrgID, run.TriggeredByUserID, sandboxCfg.Env, prompt.UsageHint)

	// 10. Execute agent with log streaming.
	logCh := make(chan LogEntry, 100)
	var logWg sync.WaitGroup
	logWg.Add(1)
	go func() {
		defer logWg.Done()
		o.streamLogs(ctx, run.ID, run.OrgID, primaryThreadID, turnNumber, logCh, runtimeTracker)
	}()

	execCtx := WithSandboxProvider(ctx, o.provider)
	if o.cancels != nil {
		execCtx = WithInteractiveHandleAttacher(execCtx, o.cancels.HandleAttacher(run.ID))
	}
	result, err := adapter.Execute(execCtx, sandbox, prompt, logCh)
	close(logCh)
	logWg.Wait()

	// 10b. Retry once on token expiration for Codex agents.
	result, err = o.retryOnTokenExpired(ctx, run.AgentType, run.OrgID, run.TriggeredByUserID, run.ID, primaryThreadID, turnNumber, sandbox, adapter, execCtx, prompt, result, err, log)

	// 10c. Shed the just-picked credential's in-process health-cache slot if
	// the (possibly retried) result indicates a credential-level failure.
	// No-ops cleanly for agent types whose auth flows do not pass through
	// the unified resolver (e.g. Codex subscription via codexauth.Service).
	o.shedOnRunResult(run.AgentType, run.OrgID, run.TriggeredByUserID, result, err, log)

	// 11. Handle result.
	stopReason := StopReasonNone
	if o.cancels != nil {
		stopReason = o.cancels.StopReason(run.ID)
	}
	wasCancelled := stopReason == StopReasonUserCancel

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
		if wasCancelled || (stopReason == StopReasonNone && errors.Is(ctx.Err(), context.Canceled)) {
			log.Info().Msg("session cancelled by user")
			o.handleCancelledSession(ctx, run, sandbox, result, run.CurrentTurn+1, log)
			logAgentRunFinished(log, run, "cancelled", runStartedAt, func(event *zerolog.Event) {
				event.Str("stop_reason", string(models.RuntimeStopReasonUserCancel))
			})
			return fmt.Errorf("session cancelled: %w", ctx.Err())
		}
		if stopReason != StopReasonNone {
			log.Info().Str("stop_reason", string(stopReason)).Msg("session stopped by runtime policy")
			o.handlePolicyStoppedSession(ctx, run, sandbox, result, run.CurrentTurn+1, stopReason, log)
			logAgentRunFinished(log, run, "runtime_policy_stopped", runStartedAt, func(event *zerolog.Event) {
				event.Str("stop_reason", string(stopReason))
			})
			return nil
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			elapsed := time.Since(runStartedAt).Round(time.Second)
			o.failTimedOutSession(run, elapsed, 0, err, log)
			return fmt.Errorf("%w after %s: %w", ErrSessionTimedOut, elapsed, err)
		}
		o.failRun(ctx, run, err.Error())
		logAgentRunFailed(log, run, err, "failed", runStartedAt, nil)
		o.updatePrimaryThreadTerminal(ctx, run, models.ThreadStatusFailed, &models.SessionResult{
			Error: strPtr(err.Error()),
		}, log)
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
		o.handleCancelledSession(ctx, run, sandbox, result, run.CurrentTurn+1, log)
		logAgentRunFinished(log, run, "cancelled", runStartedAt, func(event *zerolog.Event) {
			event.Str("stop_reason", string(models.RuntimeStopReasonUserCancel))
		})
		return nil
	}
	if stopReason != StopReasonNone {
		log.Info().Str("stop_reason", string(stopReason)).Msg("agent exited after runtime policy stop")
		o.handlePolicyStoppedSession(ctx, run, sandbox, result, run.CurrentTurn+1, stopReason, log)
		logAgentRunFinished(log, run, "runtime_policy_stopped", runStartedAt, func(event *zerolog.Event) {
			event.Str("stop_reason", string(stopReason))
		})
		return nil
	}

	// 11b. Snapshot workspace for multi-turn support (does not change session status).
	snapshotKey, snapshotSize, snapshotErr := o.snapshotSessionOnTurnSuccess(ctx, run, sandbox, result, log)
	if snapshotErr != nil {
		log.Warn().Err(snapshotErr).Msg("failed to snapshot session, session will not support follow-up turns")
	} else if snapshotKey != "" {
		runtimeTracker.Record(models.RuntimeProgressTypeCheckpoint, models.RuntimeProgressStrengthStrong, time.Now().UTC())
		lockToken, _ := jobctx.LockTokenFromContext(ctx)
		if _, err := o.sessions.PublishCheckpoint(ctx, run.OrgID, run.ID, lockToken, result.AgentSessionID, snapshotKey, models.CheckpointKindTurnComplete, checkpointCapabilityForAgent(run.AgentType), snapshotSize, time.Now().UTC(), nil, models.RuntimeStopReasonNone); err != nil {
			log.Warn().Err(err).Msg("failed to publish checkpoint metadata")
		}
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
	runResult := o.buildRunResult(ctx, run, sandbox, result)
	status := "completed"
	isInteractive := run.IsInteractive() && snapshotKey != ""

	// 11. Confidence gating: use org-configured auto-proceed threshold.
	if result.ConfidenceScore < confidenceThresholds.AutoProceed {
		status = "needs_human_guidance"
	}

	if isInteractive {
		turnNumber := run.CurrentTurn + 1
		if err := o.createAssistantMessage(ctx, run.ID, run.OrgID, primaryThreadID, turnNumber, result); err != nil {
			log.Warn().Err(err).Msg("failed to persist assistant message for interactive turn")
		}

		agentSessionID := result.AgentSessionID
		if agentSessionID == "" && run.AgentSessionID != nil {
			agentSessionID = *run.AgentSessionID
		}
		if err := o.sessions.UpdateTurnComplete(ctx, run.OrgID, run.ID, turnNumber, runResult, agentSessionID, snapshotKey); err != nil {
			return fmt.Errorf("update interactive turn result: %w", err)
		}
		if primaryThreadID != nil && o.sessionThreads != nil {
			if err := o.sessionThreads.CompleteTurn(ctx, run.OrgID, *primaryThreadID, turnNumber, agentSessionID); err != nil {
				log.Warn().Err(err).Str("thread_id", primaryThreadID.String()).Msg("failed to mark primary thread turn complete")
			}
		}

		log.Info().
			Int("turn", turnNumber).
			Msg("interactive session turn completed and returned to idle")
		logAgentRunFinished(log, run, "idle", runStartedAt, func(event *zerolog.Event) {
			event.
				Str("status", "idle").
				Float64("confidence", result.ConfidenceScore).
				Float64("threshold", confidenceThresholds.AutoProceed)
		})
		return nil
	}

	if err := o.sessions.UpdateResult(ctx, run.OrgID, run.ID, status, runResult); err != nil {
		return fmt.Errorf("update run result: %w", err)
	}
	if primaryThreadID != nil && o.sessionThreads != nil {
		if err := o.sessionThreads.UpdateResult(ctx, run.OrgID, *primaryThreadID, models.ThreadStatusCompleted, runResult); err != nil {
			log.Warn().Err(err).Str("thread_id", primaryThreadID.String()).Msg("failed to persist primary thread result")
		}
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

	logAgentRunFinished(log, run, status, runStartedAt, func(event *zerolog.Event) {
		event.
			Str("status", status).
			Float64("confidence", result.ConfidenceScore).
			Float64("threshold", confidenceThresholds.AutoProceed)
	})

	// 12. Enqueue follow-up job based on confidence.
	if result.ConfidenceScore >= confidenceThresholds.AutoProceed {
		payload := map[string]interface{}{
			"session_id": run.ID.String(),
			"org_id":     run.OrgID.String(),
		}
		if issueSnapshot != nil {
			payload["issue_snapshot_id"] = issueSnapshot.ID.String()
		}
		o.enqueueJob(ctx, run.OrgID, "default", "open_pr", payload)
	}

	if run.PMPlanID != nil && o.decisionLog != nil {
		outcome := outcomeFromRunStatus(status)
		if outcome != "" {
			if run.PrimaryIssueID != nil {
				if err := o.decisionLog.UpdateOutcome(ctx, run.OrgID, *run.PMPlanID, *run.PrimaryIssueID, outcome); err != nil {
					o.logger.Warn().Err(err).Str("run_id", run.ID.String()).Msg("failed to update PM decision log outcome")
				}
			} else {
				o.logger.Debug().Str("run_id", run.ID.String()).Msg("skipping PM decision log outcome update because run has no primary issue")
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
// scoping and ClaimIdleForSession atomicity.
func (o *Orchestrator) ContinueSession(ctx context.Context, session *models.Session, opts *ContinueSessionOptions) error {
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

	// Gate: if a post-PR snapshot upload is still in flight, hydrating from
	// the prior SnapshotKey would restore stale pre-PR state. Bail out early
	// with ErrSnapshotPending so the worker requeues the job. No state has
	// been mutated yet — this is the cleanest place for the gate. The status
	// stays where the user left it (typically `pr_created`); no failure
	// message is registered because this is a transient wait, not an error.
	prRepairOpts := (*PRRepairContinueOptions)(nil)
	if opts != nil && opts.PRRepair != nil {
		prRepairOpts = opts.PRRepair
	}
	prHeadReconstruction := prRepairOpts != nil && prRepairOpts.WorkspaceMode == models.PullRequestRepairWorkspaceModePRHeadReconstruction

	if !prHeadReconstruction && session.PendingSnapshotKey != nil && *session.PendingSnapshotKey != "" {
		log.Info().Str("pending_snapshot_key", *session.PendingSnapshotKey).Msg("continue_session waiting for post-PR snapshot upload to land")
		return ErrSnapshotPending
	}

	parentAgentSessionID := ""
	if session.AgentSessionID != nil {
		parentAgentSessionID = *session.AgentSessionID
	}
	threadScopedExecution := opts != nil && opts.AgentType != ""
	if threadScopedExecution {
		executionSession := *session
		executionSession.AgentType = opts.AgentType
		executionSession.ModelOverride = opts.ModelOverride
		executionSession.AgentSessionID = opts.ThreadAgentSessionID
		session = &executionSession
	}

	// Determine whether we can restore from a snapshot or need a fresh start.
	hasSnapshot := !prHeadReconstruction &&
		session.SnapshotKey != nil && *session.SnapshotKey != "" &&
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
	runtimeCfg := o.resolveRuntimeConfig(ctx, session.OrgID)
	runtimeTracker := newRuntimeProgressTracker(turnStartedAt)
	runtimeController := newRuntimeController(runtimeCfg, o.sessions, o.jobs, o.cancels, log, session.OrgID, session.ID, o.maxConcurrent, o.isDraining, runtimeTracker)
	fallbackStatus := session.Status
	if fallbackStatus == "" {
		fallbackStatus = string(models.SessionStatusIdle)
	}
	if err := o.beginRuntimeControl(ctx, runtimeController, session.OrgID, session.ID, fallbackStatus, checkpointCapabilityForAgent(session.AgentType), turnStartedAt, log); err != nil {
		return err
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

	// Scope the latest-user-message lookup to the thread the worker enqueued
	// for. ListBySession orders rows by (turn_number, id) and per-thread turn
	// counters are independent, so the globally-last user row in the slice is
	// not necessarily the most recently sent message — a sibling thread that's
	// further along in turns can sit "after" a brand-new user message in the
	// returned ordering. Without this scoping, ContinueSession would re-run
	// the wrong (already-answered) thread and orphan the new message.
	//
	// When opts is nil (RecoverSession path: worker crash mid-turn rehydrates
	// without a thread hint) we preserve the legacy global lookup so a
	// threaded session can still recover its in-flight turn.
	var latestMsg *models.SessionMessage
	if opts != nil && opts.ThreadID != nil {
		latestMsg = latestUserMessageInScope(messages, opts.ThreadID)
	} else {
		latestMsg = latestUserMessage(messages)
	}
	if latestMsg == nil || strings.TrimSpace(latestMsg.Content) == "" {
		o.failRun(ctx, session, "no user message found for continue_session")
		return fmt.Errorf("no user message found")
	}
	threadID := latestMsg.ThreadID
	// Bundle every trailing user message in scope into the prompt seed so
	// rapid mid-turn sends are all delivered to the agent. The latest
	// message remains the "carrier" for thread_id, references, and
	// plan-mode detection — earlier queued messages contribute only their
	// text content. When only a single user message is unprocessed, this
	// reduces to the legacy single-message behavior.
	const planModePrefix = "[PLAN_MODE]\n"
	pendingMsgs := unprocessedUserMessagesThrough(messages, threadID, latestMsg.ID)
	planMode := strings.HasPrefix(latestMsg.Content, planModePrefix)
	var userMessage string
	if len(pendingMsgs) <= 1 {
		userMessage = strings.TrimPrefix(latestMsg.Content, planModePrefix)
	} else {
		var sb strings.Builder
		for i, m := range pendingMsgs {
			if i > 0 {
				sb.WriteString("\n\n---\n\n")
			}
			sb.WriteString(strings.TrimPrefix(m.Content, planModePrefix))
		}
		userMessage = sb.String()
	}
	if planMode {
		userMessage = "You are in PLAN MODE. Instead of making changes directly, create a detailed implementation plan for the following request. Describe:\n" +
			"1. What files need to be changed and why\n" +
			"2. What specific changes are needed in each file\n" +
			"3. The order of operations\n" +
			"4. Any potential risks or considerations\n\n" +
			"Do NOT make any file changes or use any tools that modify files. Only output the plan as a structured markdown response. " +
			"The user will review the plan and either approve it or request adjustments before you proceed.\n\n" +
			"User's request:\n" + userMessage
	}
	_ = planMode // used by adapters that support explicit plan mode
	revisionContext, revErr := ParseRevisionContext(session.RevisionContext)
	if revErr != nil {
		log.Warn().Err(revErr).Msg("failed to parse session revision context during continue_session")
		revisionContext = nil
	}
	if formatted := FormatRevisionContextForContinuation(revisionContext); formatted != "" {
		userMessage = strings.TrimSpace(userMessage + "\n\n" + formatted)
	}

	turnNumber := session.CurrentTurn + 1
	issueSnapshot, err := o.createIssueSnapshotForTurn(ctx, session, turnNumber)
	if err != nil {
		o.failRun(ctx, session, fmt.Sprintf("resolve issue context: %s", err))
		return fmt.Errorf("resolve issue context: %w", err)
	}
	promptIssue, linkedIssues := o.promptSeedForSession(session, latestMsg, issueSnapshot)
	if promptIssue != nil && promptIssue.ID != uuid.Nil {
		session.PrimaryIssueID = &promptIssue.ID
	}
	hydrateSessionPolicyForExecution(session, promptIssue)

	// 4. Create sandbox.
	sandboxCfg := DefaultSandboxConfig()
	sandboxCfg.SessionID = session.ID.String()
	sandboxCfg.OrgID = session.OrgID.String()
	sandboxCfg.Purpose = "continue_session"
	sandboxCfg.Env = o.env.Resolve(ctx, session.OrgID, session.AgentType, session.TriggeredByUserID)
	if sandboxCfg.Env == nil {
		sandboxCfg.Env = make(map[string]string)
	}
	// Apply the per-session model override before the auth pre-flight so the
	// sandbox sees the effective model/mode selection.
	if session.ModelOverride != nil && *session.ModelOverride != "" {
		if envVar := models.ModelEnvVarForAgentType(session.AgentType); envVar != "" {
			sandboxCfg.Env[envVar] = *session.ModelOverride
		}
	}
	if branch := sessionWorkingBranch(session, promptIssue); branch != "" {
		sandboxCfg.Env[sandboxauth.WorkingBranchEnvVar] = branch
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
				ThreadID:   threadID,
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
	reusedExisting := session.ContainerID != nil && *session.ContainerID != ""
	// Liveness / cross-node check on reuse. A recorded container_id only
	// means "someone (preview, prior turn) wrote it" — it does NOT prove
	// the container is alive AND on this node's docker daemon. Two cases:
	//
	//   (1) Cross-node claim. session.WorkerNodeID points at a different
	//       worker. Container ids are local to a docker daemon, so any
	//       Exec we issue here would fail "No such container" (historically
	//       misclassified as Codex auth expiry). An IsAlive probe on the
	//       wrong daemon also false-reports dead — running ClearContainerID
	//       in that state would orphan the live container on its real host.
	//       Bail with ErrSandboxOnDifferentNode so the worker re-enqueues;
	//       the correctly-pinned worker (or a future retry post-rollout of
	//       target_node_id job affinity) picks it up. This branch is
	//       defense-in-depth once that affinity lands.
	//
	//   (2) Stale orphan. WorkerNodeID matches us (or is unset, e.g. a
	//       legacy row from before SetWorkerNodeIDForContainer existed),
	//       and the recorded container is gone (rollover, OOM, daemon
	//       eviction). Probe IsAlive; if dead, CAS-clear via
	//       ClearContainerID and return ErrStaleSandboxIDCleared so the
	//       worker retries against a clean row.
	//
	// Probe-or-clear is gated on node match because the IsAlive signal is
	// only authoritative on the daemon that created the container.
	if reusedExisting {
		recordedNode := ""
		if session.WorkerNodeID != nil {
			recordedNode = *session.WorkerNodeID
		}
		switch {
		case recordedNode != "" && recordedNode != o.nodeID:
			if deadTargetNode, ok := jobctx.DeadTargetNodeFromContext(ctx); ok && deadTargetNode == recordedNode {
				cleared, clearErr := o.sessions.ClearContainerID(ctx, session.OrgID, session.ID, *session.ContainerID)
				if clearErr != nil {
					log.Warn().Err(clearErr).
						Str("stale_container_id", *session.ContainerID).
						Str("dead_target_node", deadTargetNode).
						Msg("ClearContainerID failed during dead-node continue_session recovery; retrying for another recovery attempt")
					if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusPending)); revertErr != nil {
						log.Error().Err(revertErr).Msg("failed to revert session to pending after dead-node recovery clear failure")
					}
					return ErrSandboxOnDifferentNode
				}
				if !cleared {
					log.Info().
						Str("stale_container_id", *session.ContainerID).
						Str("dead_target_node", deadTargetNode).
						Msg("ClearContainerID CAS lost during dead-node continue_session recovery; retrying against the current row")
					if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusPending)); revertErr != nil {
						log.Error().Err(revertErr).Msg("failed to revert session to pending after dead-node recovery CAS miss")
					}
					return ErrStaleSandboxIDCleared
				}
				log.Warn().
					Str("stale_container_id", *session.ContainerID).
					Str("dead_target_node", deadTargetNode).
					Msg("cleared container_id from dead-node continue_session recovery; signaling retry to hydrate on this worker")
				if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusPending)); revertErr != nil {
					log.Error().Err(revertErr).Msg("failed to revert session to pending after dead-node recovery clear")
				}
				return ErrStaleSandboxIDCleared
			}
			log.Info().
				Str("container_id", *session.ContainerID).
				Str("recorded_node", recordedNode).
				Str("this_node", o.nodeID).
				Msg("continue_session claimed on the wrong worker; recorded container_id belongs to a sibling node — releasing for the correct worker to pick up")
			if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusPending)); revertErr != nil {
				log.Error().Err(revertErr).Msg("failed to revert session to pending after wrong-node detection")
			}
			return ErrSandboxOnDifferentNode
		default:
			probeCtx, cancel := context.WithTimeout(ctx, sandboxRaceProbeTimeout)
			alive, aliveErr := o.provider.IsAlive(probeCtx, &Sandbox{ID: *session.ContainerID, Provider: "docker"})
			cancel()
			switch {
			case aliveErr != nil:
				// The probe was inconclusive: we can't tell whether the
				// recorded container is alive on this daemon or genuinely
				// gone (a docker daemon hiccup, a probe timeout against an
				// unreachable container, etc.). Falling through to docker
				// exec on a stale id surfaces a user-visible "No such
				// container" failure, while clearing on an inconclusive
				// signal would orphan a healthy live container if the
				// daemon merely hiccuped — both are worse than retrying.
				// Bounded by maxRetryableDuration so a permanently broken
				// daemon still dead-letters through the normal path.
				log.Warn().Err(aliveErr).
					Str("container_id", *session.ContainerID).
					Msg("IsAlive probe on recorded container_id failed during continue_session reuse; abandoning attempt so the worker retries instead of attaching to an indeterminate container")
				return o.abandonReuseForRetry(ctx, session, log, "isalive probe error")
			case !alive:
				cleared, clearErr := o.sessions.ClearContainerID(ctx, session.OrgID, session.ID, *session.ContainerID)
				if clearErr != nil {
					// DB error during the clear: container_id state is
					// indeterminate. Abandon the attempt rather than
					// proceeding to docker exec on a known-dead
					// container, which would always surface a user-visible
					// "No such container" failure.
					log.Warn().Err(clearErr).
						Str("stale_container_id", *session.ContainerID).
						Msg("ClearContainerID failed during continue_session reuse liveness check; abandoning attempt so the worker retries against the unchanged row")
					return o.abandonReuseForRetry(ctx, session, log, "clear container_id db error")
				}
				if !cleared {
					// CAS-lost: a peer (typically a preview's
					// FinalizeContainerDestroy on a launch-failed
					// instance) cleared or replaced container_id between
					// our probe and clear. The in-memory
					// session.ContainerID is now stale, so reusing it
					// would attach to a no-longer-current container.
					// Abandon so the next attempt re-fetches a fresh
					// session row.
					log.Info().
						Str("stale_container_id", *session.ContainerID).
						Msg("ClearContainerID CAS lost during continue_session reuse liveness check (a new holder acquired between probe and clear); abandoning attempt so the worker re-fetches the now-active row")
					return o.abandonReuseForRetry(ctx, session, log, "clear container_id cas lost")
				}
				log.Warn().
					Str("stale_container_id", *session.ContainerID).
					Msg("cleared stale orphan container_id during continue_session reuse liveness check; signaling retry to re-enter against the clean row")
				return o.abandonReuseForRetry(ctx, session, log, "stale container_id cleared")
			}
		}
	}
	integrationSkills := o.BuildIntegrationSkills(ctx, session.OrgID)
	var authState *sandboxGitHubAuthState
	var authErr error
	// continueTargetBranch is the resolved target branch (repo default,
	// overridden by session.TargetBranch) — captured during the repo lookup
	// below and stamped onto sandbox.Metadata after sandbox setup so
	// sessiondiff.Collect can compute a merge-base diff against
	// origin/<branch>. Mirrors the branch resolved in RunAgent.
	var continueTargetBranch string

	// Wire the per-session GitHub credential helper for both fresh and
	// reused containers. For reused containers (preview is holding the
	// previously-created container alive across turns), we still need to
	// (re)open the host listener: an orchestrator restart between turns
	// would have killed the original listener while leaving the container
	// running, so the agent's next push would dial a dead socket. The
	// directory bind-mount (see providers.docker.go) means a fresh listener
	// at the deterministic per-session path is always reachable from the
	// alive container, so the recreate is transparent to the agent.
	//
	// For fresh containers we additionally need an installation token to
	// pass into the legacy GITHUB_TOKEN env path that prepareSandboxGitHubAuth
	// falls back to when the resolver isn't wired. Reused containers don't
	// need it: their env was set at create time, prepareSandboxGitHubAuth's
	// env mutations are wasted (but harmless), and we never reach the
	// legacy path with a still-running container anyway.
	repoID := session.RepositoryID
	if repoID == nil && promptIssue != nil {
		repoID = promptIssue.RepositoryID
	}
	if repoID != nil {
		repo, repoErr := o.repositories.GetByID(ctx, session.OrgID, *repoID)
		if repoErr != nil {
			log.Error().Err(repoErr).Msg("failed to fetch repository for continue-session auth wiring")
			if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
				log.Error().Err(revertErr).Msg("failed to revert session to idle after auth wiring repo lookup failure")
			}
			if revertErr := o.sessions.UpdateSandboxState(ctx, session.OrgID, session.ID, string(models.SandboxStateSnapshotted)); revertErr != nil {
				log.Warn().Err(revertErr).Msg("failed to revert sandbox state after auth wiring repo lookup failure")
			}
			o.registerSandboxFailureMessage(
				ctx,
				session,
				fmt.Sprintf("Failed to prepare GitHub access for the sandbox: %s\n\nPlease try again in a moment.", repoErr),
				"sandbox github auth",
			)
			return fmt.Errorf("fetch repository for auth: %w", repoErr)
		}
		continueTargetBranch = repo.DefaultBranch
		if session.TargetBranch != nil && *session.TargetBranch != "" {
			continueTargetBranch = *session.TargetBranch
		}
		var fallbackToken string
		if !reusedExisting {
			token, tokenErr := o.github.GetInstallationToken(ctx, repo.InstallationID)
			if tokenErr != nil {
				log.Error().Err(tokenErr).Msg("failed to get installation token for continue-session auth wiring")
				if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
					log.Error().Err(revertErr).Msg("failed to revert session to idle after auth wiring token failure")
				}
				if revertErr := o.sessions.UpdateSandboxState(ctx, session.OrgID, session.ID, string(models.SandboxStateSnapshotted)); revertErr != nil {
					log.Warn().Err(revertErr).Msg("failed to revert sandbox state after auth wiring token failure")
				}
				o.registerSandboxFailureMessage(
					ctx,
					session,
					fmt.Sprintf("Failed to prepare GitHub access for the sandbox: %s\n\nPlease try again in a moment.", tokenErr),
					"sandbox github auth",
				)
				return fmt.Errorf("get installation token for auth: %w", tokenErr)
			}
			fallbackToken = token
		}
		repoCopy := repo
		authState, authErr = o.prepareSandboxGitHubAuth(ctx, session, &repoCopy, fallbackToken, &sandboxCfg, log)
		if authErr != nil {
			log.Error().Err(authErr).Msg("failed to wire GitHub auth for continue-session sandbox")
			if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
				log.Error().Err(revertErr).Msg("failed to revert session to idle after auth wiring failure")
			}
			if revertErr := o.sessions.UpdateSandboxState(ctx, session.OrgID, session.ID, string(models.SandboxStateSnapshotted)); revertErr != nil {
				log.Warn().Err(revertErr).Msg("failed to revert sandbox state after auth wiring failure")
			}
			o.registerSandboxFailureMessage(
				ctx,
				session,
				fmt.Sprintf("Failed to prepare GitHub access for the sandbox: %s\n\nPlease try again in a moment.", authErr),
				"sandbox github auth",
			)
			return fmt.Errorf("prepare sandbox github auth: %w", authErr)
		}
	}

	// Determine sandbox strategy:
	//   - Reuse: a preview already hydrated the container; attach to it by ID
	//     and skip both Create and Restore.
	//   - Hydrate: a snapshot exists; create a new container and restore the
	//     snapshot via the shared HydrateSandboxFromSnapshot helper.
	//   - Fresh: no snapshot; create a clean container and clone fresh below.
	var sandbox *Sandbox
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
			o.closeSandboxAuth(session.ID, log)
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
			o.closeSandboxAuth(session.ID, log)
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
	// Re-populate sandbox.Metadata["base_commit_sha"] from the DB so that
	// sessiondiff.Collect can compute `git diff <base> -- .` (the cumulative
	// session diff against the immutable base) instead of falling back to a
	// plain `git diff` against the index. Without this, any continue turn
	// run on a clean working tree (post-PR-push, post-merge) would collect
	// an empty diff and overwrite the persisted authoritative diff, blanking
	// out the Changes tab on the session page even though the PR clearly
	// has changes. RunAgent sets this on the initial clone; the three setup
	// branches above (reuse, hydrate, fresh-clone-on-continue) all leave
	// Metadata empty, so we fix it here once for every continue path.
	//
	// Also re-stamp the resolved target branch so Collect can compute a
	// merge-base diff against origin/<branch>. Without this the post-merge
	// diff includes every commit pulled in from the target branch, inflating
	// the Changes tab from the actual PR delta to the full base..HEAD range.
	if session.BaseCommitSHA != nil && *session.BaseCommitSHA != "" {
		if sandbox.Metadata == nil {
			sandbox.Metadata = make(map[string]string)
		}
		sandbox.Metadata[SandboxMetadataBaseCommitSHA] = *session.BaseCommitSHA
	}
	if continueTargetBranch != "" {
		if sandbox.Metadata == nil {
			sandbox.Metadata = make(map[string]string)
		}
		sandbox.Metadata[SandboxMetadataTargetBranch] = continueTargetBranch
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
		// Close the listener regardless of reuse: we opened it during this
		// turn's auth wiring, so it's our responsibility to release it.
		// Server.Close is idempotent and a no-op when no listener exists.
		o.closeSandboxAuth(session.ID, log)
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
		// we instead leave it alone; the winner is using actualContainerID.
		if !reusedExisting {
			if destroyErr := o.provider.Destroy(destroyCtx, sandbox); destroyErr != nil {
				log.Error().Err(destroyErr).Str("losing_container_id", sandbox.ID).Msg("failed to destroy sandbox after losing hydrate race")
			}
		}
		// Close the listener regardless of reuse: we opened it during this
		// turn's auth wiring, so it's our responsibility to release it.
		o.closeSandboxAuth(session.ID, log)
		log.Warn().
			Str("winning_container_id", actualContainerID).
			Str("losing_container_id", sandbox.ID).
			Msg("another holder published container_id first; diagnosing whether the winner is alive or a stale orphan")
		// Self-heal: see RunAgent's symmetrical site for rationale. The
		// diagnosis helper either dead-letters this duplicate (real winner
		// active) or CAS-clears a stale orphan and signals retry.
		diagErr := o.diagnoseAcquireHoldRaceLoss(ctx, session.OrgID, session.ID, actualContainerID, log)
		if errors.Is(diagErr, ErrSandboxPreviewRace) {
			if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
				log.Error().Err(revertErr).Msg("failed to revert session to idle after preview won sandbox race")
			}
		}
		return fmt.Errorf("%w: actual container %s != local %s", diagErr, actualContainerID, sandbox.ID)
	}
	containerStartedAt := time.Now()
	var usageEventID uuid.UUID
	if o.usageTracker != nil {
		usageEventID = o.usageTracker.ContainerStarted(ctx, session.OrgID, session.ID, sandbox, sandboxCfg, containerStartedAt)
	}
	drainAfterRelease := false
	defer func() {
		if !drainAfterRelease {
			return
		}
		drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer drainCancel()
		o.drainQueuedMessages(drainCtx, session, latestMsg, threadID, log)
	}()
	defer func() {
		exitReason := containerExitReason(ctx, err)
		if o.usageTracker != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopCancel()
			o.usageTracker.ContainerStopped(stopCtx, session.OrgID, session.ID, usageEventID, sandbox.ID, containerStartedAt, exitReason)
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
		o.closeSandboxAuth(session.ID, log)
	}()
	if o.nodeID != "" {
		if err := o.sessions.SetWorkerNodeIDForContainer(ctx, session.OrgID, session.ID, sandbox.ID, o.nodeID); err != nil {
			log.Error().Err(err).
				Str("container_id", sandbox.ID).
				Str("worker_node_id", o.nodeID).
				Msg("persist session worker ownership: CAS failed (container_id moved or worker_node_id held by another worker)")
			// Detached context for the cleanup writes: this site fires when
			// a CAS conflict means another worker already owns the row, and
			// also during rolling-deploy ctx cancellation. Both cases need
			// the revert to land. Without WithoutCancel, a cancelled ctx
			// silently fails the UpdateStatus and leaves session.status =
			// 'running' / thread.status = 'running' permanently — that's
			// the orphan that produces "Session is not active" +
			// "Agent is working..." in the UI at the same time.
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cleanupCancel()
			if revertErr := o.sessions.UpdateStatus(cleanupCtx, session.OrgID, session.ID, string(models.SessionStatusIdle)); revertErr != nil {
				log.Error().Err(revertErr).Msg("failed to revert session to idle after worker ownership persistence failure")
			}
			// Mirror the session revert onto the active thread. The handler
			// also resets thread.status on error, but it can miss when its
			// own ctx is cancelled mid-shutdown (the exact scenario this
			// failure path tends to fire in). Belt-and-suspenders here is
			// what unblocks the UI for the user that just sent a message.
			if opts != nil && opts.ThreadID != nil && o.sessionThreads != nil {
				if revertErr := o.sessionThreads.UpdateStatus(cleanupCtx, session.OrgID, *opts.ThreadID, models.ThreadStatusIdle); revertErr != nil {
					log.Error().Err(revertErr).
						Str("thread_id", opts.ThreadID.String()).
						Msg("failed to revert thread to idle after worker ownership persistence failure")
				}
			}
			o.registerSandboxFailureMessage(
				ctx,
				session,
				fmt.Sprintf("Failed to persist sandbox worker ownership: %s\n\nPlease try again in a moment.", err),
				"sandbox ownership",
			)
			return fmt.Errorf("persist session worker ownership: %w", err)
		}
	}

	// Register the session with the cancel registry. The interactive
	// command handle is attached lazily by the adapter's runtime helper.
	if o.cancels != nil {
		o.cancels.Register(session.ID, cancel, ResolveCancellationSpec(adapter))
	}
	// Mirror the registration on the thread-scoped registry so a per-tab
	// cancel can unwind just this thread's run context. The session-level
	// registry remains the legacy path for whole-sandbox cancels.
	if o.threadCancels != nil && opts != nil && opts.ThreadID != nil {
		o.threadCancels.Register(*opts.ThreadID, cancel)
		defer o.threadCancels.Deregister(*opts.ThreadID)
	}

	// 5. Set up the workspace. Three paths:
	//   - Reuse: the container is already live (preview hydrated it); just
	//     re-inject Codex auth and build the resume prompt.
	//   - Hydrate: HydrateSandboxFromSnapshot already did Create+Restore;
	//     re-inject Codex auth and build the resume prompt.
	//   - Fresh: no snapshot; clone repo fresh and build a reconstructed
	//     prompt from the conversation history + stored diff.
	var prompt *AgentPrompt
	authBillingMode := TokenBillingModeUnknown
	if reusedExisting || hasSnapshot {
		// Re-inject agent auth (Codex auth.json or Claude Code credentials.json).
		// Cheap, and catches the case where the file was cleared or drifted
		// while the container was idle (or where the preview created the
		// container without agent credentials).
		switch session.AgentType {
		case models.AgentTypeCodex:
			mode, err := o.ensureCodexAuth(ctx, session, sandbox, sandboxCfg.Env)
			if err != nil {
				return err
			}
			authBillingMode = mode
		case models.AgentTypeClaudeCode:
			mode, err := o.ensureClaudeCodeAuth(ctx, session, sandbox, sandboxCfg.Env)
			if err != nil {
				return err
			}
			authBillingMode = mode
		}
		if !reusedExisting {
			o.runSandboxGitBootstrap(ctx, sandbox, sandboxCfg.WorkDir, log)
		}

		commands := canonicalCommands(latestMsg, session.AgentType)
		hasThreadAgentSessionID := session.AgentSessionID != nil && *session.AgentSessionID != ""
		resumeMode := adapter.ResumeMode()
		// missingResumeID covers the case where the adapter resumes by
		// session id but no id was captured from a prior turn — e.g. the
		// session predates session-id capture, or capture failed. Without
		// the id, the adapter falls back to a fresh exec, so we must embed
		// the conversation history into the prompt or the agent loses
		// context. Threaded first turns are handled by the next branch.
		missingResumeID := resumeMode == ResumeBySessionID && !hasThreadAgentSessionID && !threadScopedExecution
		// Both snapshot-path branches that go through PreparePrompt feed it
		// the same context; capture once here so a future field addition
		// can't drift between the two callers.
		buildSnapshotContinueInput := func() *AgentInput {
			return &AgentInput{
				Issue:        promptIssue,
				LinkedIssues: linkedIssues,
				Manual:       session.Origin == models.SessionOriginManual,
				PromptStyle: func() PromptStyle {
					if session.Origin == models.SessionOriginManual || session.AutomationRunID != nil {
						return PromptStyleRawTask
					}
					return PromptStyleIssueContext
				}(),
				UserMessage: userMessage,
				References: func() []models.SessionInputReference {
					refs := canonicalReferences(latestMsg)
					if len(refs) > 0 {
						return refs
					}
					if promptIssue != nil {
						return manualSessionReferences(promptIssue)
					}
					return nil
				}(),
				Commands: commands,
				ReasoningEffort: func() models.ReasoningEffort {
					if session.ReasoningEffort == nil {
						return ""
					}
					return *session.ReasoningEffort
				}(),
				TokenMode:         session.TokenMode,
				RevisionContext:   revisionContext,
				IntegrationSkills: integrationSkills,
			}
		}
		if threadScopedExecution && !hasThreadAgentSessionID {
			prompt, err = adapter.PreparePrompt(ctx, buildSnapshotContinueInput())
			if err != nil {
				o.failRun(ctx, session, fmt.Sprintf("prepare prompt for thread: %s", err))
				return fmt.Errorf("prepare prompt for thread: %w", err)
			}
		} else if missingResumeID {
			basePrompt, err := adapter.PreparePrompt(ctx, buildSnapshotContinueInput())
			if err != nil {
				o.failRun(ctx, session, fmt.Sprintf("prepare prompt for resume fallback: %s", err))
				return fmt.Errorf("prepare prompt for resume fallback: %w", err)
			}
			// Override UserPrompt with conversation history so the agent has
			// prior context when running a fresh exec. The snapshot already
			// restored the workspace, so do not ask the agent to re-apply the
			// stored diff.
			basePrompt.UserPrompt = o.buildRestoredWorkspaceResumeContext(session, promptIssue, messages, userMessage)
			basePrompt.Continuation = false
			basePrompt.RevisionContext = revisionContext
			prompt = basePrompt
			log.Info().Msg("continuing session with embedded history (no captured agent session id)")
		} else {
			var resumeSessionID string
			if hasThreadAgentSessionID {
				resumeSessionID = *session.AgentSessionID
			}
			// UserMessage carries the user's textarea content verbatim, including
			// any visible /command tokens. Run it through the same slash-command
			// repair helper used by adapter.PreparePrompt so reused/snapshot-backed
			// continuation turns cannot silently drop stored commands when the
			// textarea and commands[] payload disagree.
			prompt = &AgentPrompt{
				Continuation:    true,
				ResumeSessionID: resumeSessionID,
				UserMessage:     EnsureSlashCommandsInPrompt(userMessage, commands),
				MaxTokens:       tokenLimitForMode(session.TokenMode),
				ReasoningEffort: func() models.ReasoningEffort {
					if session.ReasoningEffort == nil {
						return ""
					}
					return *session.ReasoningEffort
				}(),
				RevisionContext: revisionContext,
			}
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

		issue, repoFullName, authMode, err := o.setupFreshSandbox(ctx, session, sandbox, sandboxCfg.Env, prRepairOpts)
		if err != nil {
			if errors.Is(err, ErrStalePullRequestHead) {
				if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, fallbackStatus); revertErr != nil {
					log.Error().Err(revertErr).Msg("failed to restore session status after stale PR head")
				}
				if revertErr := o.sessions.UpdateSandboxState(ctx, session.OrgID, session.ID, string(models.SandboxStateSnapshotted)); revertErr != nil {
					log.Warn().Err(revertErr).Msg("failed to restore sandbox state after stale PR head")
				}
				return err
			}
			o.failRun(ctx, session, fmt.Sprintf("setup fresh sandbox: %s", err))
			return fmt.Errorf("setup fresh sandbox: %w", err)
		}
		authBillingMode = authMode
		o.runSandboxGitBootstrap(ctx, sandbox, sandboxCfg.WorkDir, log)

		// Build a full prompt via PreparePrompt so the agent gets the system
		// prompt with integration skills, memory, and repo conventions.
		input := &AgentInput{
			Issue:        &issue,
			LinkedIssues: linkedIssues,
			Manual:       session.Origin == models.SessionOriginManual,
			PromptStyle: func() PromptStyle {
				if session.Origin == models.SessionOriginManual || session.AutomationRunID != nil {
					return PromptStyleRawTask
				}
				return PromptStyleIssueContext
			}(),
			UserMessage: latestMsg.Content,
			References: func() []models.SessionInputReference {
				refs := canonicalReferences(latestMsg)
				if len(refs) > 0 {
					return refs
				}
				return manualSessionReferences(&issue)
			}(),
			Commands: canonicalCommands(latestMsg, session.AgentType),
			ReasoningEffort: func() models.ReasoningEffort {
				if session.ReasoningEffort == nil {
					return ""
				}
				return *session.ReasoningEffort
			}(),
			TokenMode:       session.TokenMode,
			RevisionContext: revisionContext,
		}
		input.IntegrationSkills = integrationSkills
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
		basePrompt.RevisionContext = revisionContext
		prompt = basePrompt
	}
	prompt.UsageHint.BillingMode = authBillingMode
	prompt.UsageHint = o.buildTokenUsageHint(ctx, session.AgentType, session.OrgID, session.TriggeredByUserID, sandboxCfg.Env, prompt.UsageHint)
	if authState != nil && authState.source != "" {
		if dbErr := o.sessions.SetGitIdentity(ctx, session.OrgID, session.ID, authState.source, authState.userID); dbErr != nil {
			log.Warn().Err(dbErr).Str("source", authState.source).Msg("failed to persist git identity audit during continue_session")
		}
	}

	// 6. Execute agent with log streaming.
	logCh := make(chan LogEntry, 100)
	var logWg sync.WaitGroup
	logWg.Add(1)
	go func() {
		defer logWg.Done()
		o.streamLogs(ctx, session.ID, session.OrgID, threadID, turnNumber, logCh, runtimeTracker)
	}()

	execCtx := WithSandboxProvider(ctx, o.provider)
	if o.cancels != nil {
		execCtx = WithInteractiveHandleAttacher(execCtx, o.cancels.HandleAttacher(session.ID))
	}
	result, err := adapter.Execute(execCtx, sandbox, prompt, logCh)
	close(logCh)
	logWg.Wait()

	// 6b. Retry once on token expiration for Codex agents.
	result, err = o.retryOnTokenExpired(ctx, session.AgentType, session.OrgID, session.TriggeredByUserID, session.ID, threadID, turnNumber, sandbox, adapter, execCtx, prompt, result, err, log)

	// 6c. Shed the just-picked credential when the (post-retry) result shows
	// rate-limit or auth-rejected signals. Same semantics as the entry-turn
	// path above; see shedOnRunResult.
	o.shedOnRunResult(session.AgentType, session.OrgID, session.TriggeredByUserID, result, err, log)

	stopReason := StopReasonNone
	if o.cancels != nil {
		stopReason = o.cancels.StopReason(session.ID)
	}
	wasCancelled := stopReason == StopReasonUserCancel

	if err != nil {
		// User cancel is checked first so an explicit cancel that races the
		// deadline is classified as a cancel, not a timeout.
		if wasCancelled || (stopReason == StopReasonNone && errors.Is(ctx.Err(), context.Canceled)) {
			log.Info().Msg("session cancelled by user during continue")
			o.handleCancelledSession(ctx, session, sandbox, result, turnNumber, log)
			drainAfterRelease = true
			return fmt.Errorf("session cancelled: %w", ctx.Err())
		}
		if stopReason != StopReasonNone {
			log.Info().Str("stop_reason", string(stopReason)).Msg("session stopped by runtime policy during continue")
			o.handlePolicyStoppedSession(ctx, session, sandbox, result, turnNumber, stopReason, log)
			return nil
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
		o.handleCancelledSession(ctx, session, sandbox, result, turnNumber, log)
		drainAfterRelease = true
		return nil
	}
	if stopReason != StopReasonNone {
		log.Info().Str("stop_reason", string(stopReason)).Msg("agent exited after runtime policy stop during continue")
		o.handlePolicyStoppedSession(ctx, session, sandbox, result, turnNumber, stopReason, log)
		return nil
	}

	// 7. Create assistant message with result summary.
	if err := o.createAssistantMessage(ctx, session.ID, session.OrgID, threadID, turnNumber, result); err != nil {
		log.Warn().Err(err).Msg("failed to create assistant message")
	}
	if opts != nil && opts.ResultAgentSessionID != nil {
		threadAgentSessionID := result.AgentSessionID
		if threadAgentSessionID == "" && opts.ThreadAgentSessionID != nil {
			threadAgentSessionID = *opts.ThreadAgentSessionID
		}
		*opts.ResultAgentSessionID = threadAgentSessionID
	}
	// Fire the thread-scoped turn-complete hook before snapshotting so the
	// caller's bookkeeping (file attribution, cost accumulation) lands in
	// one logical transaction with the assistant message and turn-complete
	// row update. Hook is intentionally fire-and-forget from the
	// orchestrator's perspective; failures inside the callback must not
	// abort the turn. Diff is taken straight from the agent result so we
	// never re-shell into the sandbox.
	if opts != nil && opts.OnTurnComplete != nil && result != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Warn().Interface("panic", r).Msg("OnTurnComplete callback panicked")
				}
			}()
			opts.OnTurnComplete(result)
		}()
	}

	// 8. Snapshot again.
	newSnapshotKey, snapshotSize, snapshotErr := o.snapshotSessionOnTurnSuccess(ctx, session, sandbox, result, log)
	if snapshotErr != nil {
		log.Warn().Err(snapshotErr).Msg("failed to snapshot session after continue")
	} else if newSnapshotKey != "" {
		runtimeTracker.Record(models.RuntimeProgressTypeCheckpoint, models.RuntimeProgressStrengthStrong, time.Now().UTC())
		lockToken, _ := jobctx.LockTokenFromContext(ctx)
		if _, err := o.sessions.PublishCheckpoint(ctx, session.OrgID, session.ID, lockToken, result.AgentSessionID, newSnapshotKey, models.CheckpointKindTurnComplete, checkpointCapabilityForAgent(session.AgentType), snapshotSize, time.Now().UTC(), nil, models.RuntimeStopReasonNone); err != nil {
			log.Warn().Err(err).Msg("failed to publish checkpoint metadata after continue")
		}
	}

	// 9. Update turn complete — sets status to idle.
	agentSessionID := result.AgentSessionID
	if threadScopedExecution {
		agentSessionID = parentAgentSessionID
	} else if agentSessionID == "" && session.AgentSessionID != nil {
		agentSessionID = *session.AgentSessionID
	}
	snapshotKey := newSnapshotKey
	if snapshotKey == "" && session.SnapshotKey != nil {
		snapshotKey = *session.SnapshotKey
	}
	if err := o.sessions.UpdateTurnComplete(ctx, session.OrgID, session.ID, turnNumber, o.buildRunResult(ctx, session, sandbox, result), agentSessionID, snapshotKey); err != nil {
		return fmt.Errorf("update turn complete: %w", err)
	}

	drainAfterRelease = true

	log.Info().Int("turn", turnNumber).Msg("session turn completed, now idle")
	return nil
}

// drainQueuedMessages re-enqueues a continue_session when a user message
// arrived during the just-completed turn. Mid-turn sends are accepted by
// both the session-level fast path (sessions.go) and the thread service's
// queue-only path (thread/service.go); without this drain those messages
// would sit in the database with no agent picking them up.
//
// Called from both the success path and the cancel-back-to-idle path of
// ContinueSession. The session's current status is re-fetched so a drain
// from the cancel path that ended up terminal (snapshot failed → cancelled)
// does not enqueue a job the worker would just refuse. All failure modes
// are logged-only because the next user-triggered send will pick up the
// queued message anyway.
func (o *Orchestrator) drainQueuedMessages(ctx context.Context, session *models.Session, processed *models.SessionMessage, threadID *uuid.UUID, log zerolog.Logger) {
	if processed == nil || o.jobs == nil || o.sessionMessages == nil {
		return
	}
	messages, err := o.sessionMessages.ListBySession(ctx, session.OrgID, session.ID)
	if err != nil {
		log.Warn().Err(err).Msg("failed to fetch messages for post-turn queue drain")
		return
	}
	hasQueued := false
	for _, m := range messages {
		if m.Role != models.MessageRoleUser || m.ID <= processed.ID {
			continue
		}
		// On the thread path, a queued message is bound to the same thread —
		// we drain only the matching thread's queue. On the session path
		// threadID is nil and we accept any user message that matches the
		// session-level scope (no thread).
		if threadID != nil {
			if m.ThreadID == nil || *m.ThreadID != *threadID {
				continue
			}
		} else if m.ThreadID != nil {
			continue
		}
		hasQueued = true
		break
	}
	if !hasQueued {
		return
	}

	// Confirm the session is in a state that can accept another turn before
	// enqueueing. Skipping the GetByID would still be safe (the worker would
	// refuse a job for a terminal session), but it would create a noisy
	// dead-letter trail on every cancelled run that left the session marked
	// failed/cancelled.
	current, getErr := o.sessions.GetByID(ctx, session.OrgID, session.ID)
	if getErr != nil {
		log.Warn().Err(getErr).Msg("failed to fetch session for queue drain status check")
		return
	}
	if !drainAcceptableStatus(current.Status) {
		return
	}

	if threadID != nil && o.sessionThreads != nil {
		if err := o.sessionThreads.ClearPendingMessages(ctx, session.OrgID, *threadID); err != nil {
			log.Warn().Err(err).Str("thread_id", threadID.String()).Msg("failed to clear pending_message_count after drain")
		}
	}
	payload := map[string]string{
		"session_id": session.ID.String(),
		"org_id":     session.OrgID.String(),
	}
	if threadID != nil {
		payload["thread_id"] = threadID.String()
	}
	dedupeKey := continueSessionDrainDedupeKey(session.ID, processed.ID)
	if _, err := o.jobs.EnqueueWithTarget(ctx, session.OrgID, "agent", "continue_session", payload, 5, &dedupeKey, models.SessionWorkerTarget(session)); err != nil {
		log.Warn().Err(err).Msg("failed to enqueue continue_session for queued messages")
	}
}

// drainAcceptableStatus returns true for session states that can absorb
// another continue_session turn: running (the in-flight job will pick the
// queued message up via latest-message reads) and idle (a fresh
// continue_session can claim it). Terminal states are skipped — a queued
// message after a failed/cancelled session waits for the user to take an
// explicit action.
func drainAcceptableStatus(status string) bool {
	switch status {
	case string(models.SessionStatusIdle),
		string(models.SessionStatusRunning),
		string(models.SessionStatusAwaitingInput),
		string(models.SessionStatusNeedsHumanGuidance):
		return true
	}
	return false
}

// continueSessionDedupeKey mirrors db.ContinueSessionDedupeKey. The agent
// package deliberately avoids importing the db package; this helper keeps
// the dedupe-key shape in lockstep without taking on the dependency. Keep
// these two definitions identical or the dedupe will silently break.
func continueSessionDedupeKey(sessionID uuid.UUID) string {
	return "continue_session:" + sessionID.String()
}

// continueSessionDrainDedupeKey intentionally differs from
// continueSessionDedupeKey because drainQueuedMessages runs while the current
// continue_session job is still in status='running'. Reusing the active key
// would hit the jobs dedupe index and turn the enqueue into a no-op.
func continueSessionDrainDedupeKey(sessionID uuid.UUID, processedMessageID int64) string {
	return fmt.Sprintf("continue_session_drain:%s:%d", sessionID.String(), processedMessageID)
}

// setupFreshSandbox clones the session's repository into the sandbox when no
// snapshot is available. Returns the issue (for prompt building) and the repo
// full name (for memory lookup). Handles sessions with or without a repository.
// The resolved env is passed in from the caller so auth injection honors the
// exact credential selection already baked into SandboxConfig.
func (o *Orchestrator) setupFreshSandbox(ctx context.Context, session *models.Session, sandbox *Sandbox, env map[string]string, repairOpts *PRRepairContinueOptions) (models.Issue, string, TokenBillingMode, error) {
	var issue models.Issue
	if session.PrimaryIssueID != nil {
		fetched, err := o.issues.GetByID(ctx, session.OrgID, *session.PrimaryIssueID)
		if err != nil {
			return models.Issue{}, "", TokenBillingModeUnknown, fmt.Errorf("fetch issue: %w", err)
		}
		issue = fetched
	} else if session.Title != nil {
		issue = models.Issue{
			Source:       models.IssueSourcePMAgent,
			RepositoryID: session.RepositoryID,
			Title:        *session.Title,
		}
	}

	// Clone repo if the session has one. sessions.repository_id is the
	// canonical source of truth — session creation copies issue.repository_id
	// into it up front, so execution never needs to re-derive repo from the
	// primary issue.
	var repoFullName string
	if session.RepositoryID != nil {
		repo, err := o.repositories.GetByID(ctx, session.OrgID, *session.RepositoryID)
		if err != nil {
			return models.Issue{}, "", TokenBillingModeUnknown, fmt.Errorf("fetch repository: %w", err)
		}
		repoFullName = repo.FullName
		branch := repo.DefaultBranch
		if session.TargetBranch != nil && *session.TargetBranch != "" {
			branch = *session.TargetBranch
		}
		token, err := o.github.GetInstallationToken(ctx, repo.InstallationID)
		if err != nil {
			return models.Issue{}, "", TokenBillingModeUnknown, fmt.Errorf("get installation token: %w", err)
		}
		if err := o.provider.CloneRepo(ctx, sandbox, repo.CloneURL, branch, token); err != nil {
			return models.Issue{}, "", TokenBillingModeUnknown, fmt.Errorf("clone repo: %w", err)
		}
		workingBranch := sessionWorkingBranch(session, &issue)
		if repairOpts != nil && repairOpts.WorkspaceMode == models.PullRequestRepairWorkspaceModePRHeadReconstruction {
			if err := o.checkoutPullRequestHead(ctx, sandbox, workingBranch, repairOpts); err != nil {
				return models.Issue{}, "", TokenBillingModeUnknown, err
			}
			session.WorkingBranch = &workingBranch
		} else if workingBranch != "" {
			var checkoutOut, checkoutErr bytes.Buffer
			exitCode, execErr := o.provider.Exec(ctx, sandbox, fmt.Sprintf("git checkout -b '%s'", shellEscapeSingleQuote(workingBranch)), &checkoutOut, &checkoutErr)
			if execErr != nil {
				return models.Issue{}, "", TokenBillingModeUnknown, fmt.Errorf("create working branch %s: %w", workingBranch, execErr)
			}
			if exitCode != 0 {
				return models.Issue{}, "", TokenBillingModeUnknown, fmt.Errorf("create working branch %s: exit=%d stderr=%s", workingBranch, exitCode, checkoutErr.String())
			}
			session.WorkingBranch = &workingBranch
		}
	}

	// Inject auth credentials into the sandbox.
	authBillingMode := TokenBillingModeUnknown
	switch session.AgentType {
	case models.AgentTypeCodex:
		mode, err := o.ensureCodexAuth(ctx, session, sandbox, env)
		if err != nil {
			return models.Issue{}, "", TokenBillingModeUnknown, fmt.Errorf("codex auth injection: %w", err)
		}
		authBillingMode = mode
	case models.AgentTypeClaudeCode:
		mode, err := o.ensureClaudeCodeAuth(ctx, session, sandbox, env)
		if err != nil {
			return models.Issue{}, "", TokenBillingModeUnknown, fmt.Errorf("claude code auth injection: %w", err)
		}
		authBillingMode = mode
	}

	return issue, repoFullName, authBillingMode, nil
}

func (o *Orchestrator) checkoutPullRequestHead(ctx context.Context, sandbox *Sandbox, workingBranch string, repairOpts *PRRepairContinueOptions) error {
	if repairOpts == nil {
		return nil
	}
	if repairOpts.HeadSHA == "" {
		return fmt.Errorf("pull request repair reconstruction missing expected head SHA")
	}
	if workingBranch == "" {
		return fmt.Errorf("pull request repair reconstruction missing working branch")
	}

	if repairOpts.PullRequestNumber <= 0 {
		return fmt.Errorf("pull request repair reconstruction missing pull request number")
	}
	return o.checkoutExpectedPullRequestHead(ctx, sandbox, repairOpts.PullRequestNumber, workingBranch, repairOpts.HeadSHA)
}

func (o *Orchestrator) checkoutExpectedPullRequestHead(ctx context.Context, sandbox *Sandbox, pullRequestNumber int, workingBranch, expectedHeadSHA string) error {
	fetchCmd := fmt.Sprintf("git fetch --quiet --no-tags origin 'pull/%d/head'", pullRequestNumber)
	var fetchErr bytes.Buffer
	fetchExit, fetchExecErr := o.provider.Exec(ctx, sandbox, fetchCmd, io.Discard, &fetchErr)
	if fetchExecErr != nil {
		return fmt.Errorf("fetch pull request head: %w", fetchExecErr)
	}
	if fetchExit != 0 {
		return fmt.Errorf("fetch pull request head: exit=%d stderr=%s", fetchExit, fetchErr.String())
	}

	checkoutCmd := fmt.Sprintf("git checkout -B '%s' FETCH_HEAD", shellEscapeSingleQuote(workingBranch))
	var checkoutErr bytes.Buffer
	checkoutExit, checkoutExecErr := o.provider.Exec(ctx, sandbox, checkoutCmd, io.Discard, &checkoutErr)
	if checkoutExecErr != nil {
		return fmt.Errorf("checkout pull request head: %w", checkoutExecErr)
	}
	if checkoutExit != 0 {
		return fmt.Errorf("checkout pull request head: exit=%d stderr=%s", checkoutExit, checkoutErr.String())
	}

	var headOut, headErr bytes.Buffer
	headExit, headExecErr := o.provider.Exec(ctx, sandbox, "git rev-parse HEAD", &headOut, &headErr)
	if headExecErr != nil {
		return fmt.Errorf("verify pull request head: %w", headExecErr)
	}
	if headExit != 0 {
		return fmt.Errorf("verify pull request head: exit=%d stderr=%s", headExit, headErr.String())
	}
	actual := strings.TrimSpace(headOut.String())
	if actual != expectedHeadSHA {
		return fmt.Errorf("%w: expected %s, got %s", ErrStalePullRequestHead, expectedHeadSHA, actual)
	}
	return nil
}

// sessionRepoSlug returns the repo-name slug for the session's repository. The
// returned slug drives WorkDir selection on resume and MUST match what RunAgent
// chose originally, or the container's WorkingDir/HOME diverge from where the
// snapshot tar restored the repo checkout. An empty slug with nil error means
// "no repo is attached" (legitimate, falls back to default WorkDir). Any lookup
// failure is returned as an error so the caller can surface it rather than
// silently diverging.
func (o *Orchestrator) sessionRepoSlug(ctx context.Context, session *models.Session) (string, error) {
	if session.RepositoryID == nil {
		return "", nil
	}
	repo, err := o.repositories.GetByID(ctx, session.OrgID, *session.RepositoryID)
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

// registerSandboxInfraFailure is the deferred companion to
// failRunWithCategory: it queues the same failure bookkeeping
// (status='failed', failure metadata + Linear milestone, user-visible
// assistant message) on the dead-letter hook so transient retries don't
// churn user state. Use this for failure modes the worker can recover
// from on retry (docker hiccups, exec/file-write errors, sandbox
// destroy/recreate races) — pairing the immediate failRunWithCategory
// with retry produces a flicker (status="failed" → "running" → "failed"
// …) and emits a Linear "failed" milestone on every attempt, even when
// a later retry succeeds.
//
// Direct callers without a job registry get a no-op hook, mirroring
// registerSandboxFailureMessage's existing semantics: such callers
// should handle the returned error path themselves. Tests that want to
// observe the deferred behavior must wrap the context with
// jobctx.WithDeadLetterHooks and explicitly invoke RunDeadLetterHooks.
//
// session is captured by value at registration time so the hook reads
// the same snapshot the caller just observed (turn number, container
// id, etc.), regardless of any later mutations to the underlying
// struct as the orchestrator advances through subsequent setup.
func (o *Orchestrator) registerSandboxInfraFailure(
	ctx context.Context,
	session *models.Session,
	errMsg, category, explanation string,
	nextSteps []string,
	stage string,
) {
	sessionCopy := *session
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, _ error) {
		// Detached + bounded: dead-letter hooks fire on the worker poll
		// goroutine after the handler ctx has been cancelled (worker
		// drain, retryable timeout). A fresh bounded context lets the
		// terminal write land without racing the very cancellation
		// that triggered the hook. 10s leaves room for both the failRun
		// updates and the assistant message insert.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 10*time.Second)
		defer cancel()
		o.failRunWithCategory(writeCtx, &sessionCopy, errMsg, category, explanation, nextSteps)
		if o.sessionMessages != nil {
			errEntry := &models.SessionMessage{
				SessionID:  sessionCopy.ID,
				OrgID:      sessionCopy.OrgID,
				TurnNumber: sessionCopy.CurrentTurn + 1,
				Role:       models.MessageRoleAssistant,
				Content:    explanation,
			}
			if createErr := o.sessionMessages.Create(writeCtx, errEntry); createErr != nil {
				o.logger.Error().Err(createErr).
					Str("session_id", sessionCopy.ID.String()).
					Str("org_id", sessionCopy.OrgID.String()).
					Str("stage", stage).
					Msg("failed to create dead-letter error message after sandbox infra failure")
			}
		}
	})
}

// abandonReuseForRetry reverts the session row's transient state and
// returns ErrStaleSandboxIDCleared so the worker re-enqueues the job
// without consuming an attempt counter. Used by the continue_session
// reuse-path liveness check whenever the IsAlive probe or its CAS-clear
// is inconclusive: the safe move is to let the next attempt re-fetch the
// session row and re-probe rather than attaching to a possibly-stale
// container_id and surfacing a user-visible "No such container" error.
//
// The status revert mirrors the existing successful stale-clear path
// (and the GitHub-auth retry paths above): pending lets a fresh worker
// claim re-enter cleanly without tripping the "session is already
// running" branches in the orchestrator's status checks. reason labels
// the log line so on-call can tell the three abandon paths apart.
//
// Deliberately does NOT revert sandbox_state. Sibling failure paths
// (auth wiring, hydrate, sandbox-create around line ~2532 / ~2575 /
// ~2615) revert it to "snapshotted" because they fire AFTER the
// orchestrator has begun creating its own sandbox — there's a fresh
// container to mark as gone. The reuse-path abandon fires BEFORE any
// sandbox creation: container_id either still points at a peer holder
// (aliveErr / CAS-lost) or was just CAS-cleared by us via
// ClearContainerID. In the holder cases sandbox_state legitimately
// remains "running" because someone else's container is still alive;
// in the CAS-cleared case the next attempt's hydrate/create path will
// transition sandbox_state correctly when it republishes container_id.
// Setting "snapshotted" here would either lie about peer-held
// containers or race the next attempt's own state writes.
func (o *Orchestrator) abandonReuseForRetry(ctx context.Context, session *models.Session, log zerolog.Logger, reason string) error {
	if revertErr := o.sessions.UpdateStatus(ctx, session.OrgID, session.ID, string(models.SessionStatusPending)); revertErr != nil {
		log.Error().Err(revertErr).
			Str("reason", reason).
			Msg("failed to revert session to pending after reuse-path abandon; the reaper will re-sync the row")
	}
	return ErrStaleSandboxIDCleared
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

	writeResumeIssueAndHistory(&b, issue, messages)

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

// buildRestoredWorkspaceResumeContext constructs the user prompt for a
// snapshot-backed continuation where the agent CLI cannot resume by session ID.
// The workspace already contains the previous turn's files, so it includes
// conversation context without asking the agent to re-apply the stored diff.
func (o *Orchestrator) buildRestoredWorkspaceResumeContext(session *models.Session, issue *models.Issue, messages []models.SessionMessage, latestUserMessage string) string {
	var b bytes.Buffer

	b.WriteString("This is a continuation of a previous session. The previous workspace state has been restored from the last saved snapshot, so treat the files on disk as the current state.\n\n")

	writeResumeIssueAndHistory(&b, issue, messages)

	if session.ResultSummary != nil && *session.ResultSummary != "" {
		b.WriteString("## Previous session summary\n\n")
		b.WriteString(*session.ResultSummary)
		b.WriteString("\n\n")
	}

	b.WriteString("## New message\n\n")
	b.WriteString(latestUserMessage)

	return b.String()
}

func writeResumeIssueAndHistory(b *bytes.Buffer, issue *models.Issue, messages []models.SessionMessage) {
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
}

func manualSessionReferences(issue *models.Issue) []models.SessionInputReference {
	if issue == nil || issue.Source != models.IssueSourceManual || len(issue.RawData) == 0 {
		return nil
	}

	var payload struct {
		References []models.SessionInputReference `json:"references"`
	}
	if err := json.Unmarshal(issue.RawData, &payload); err != nil {
		return nil
	}

	references := make([]models.SessionInputReference, 0, len(payload.References))
	for _, reference := range payload.References {
		if err := reference.Validate(); err != nil {
			continue
		}
		references = append(references, reference)
	}
	return references
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
func (o *Orchestrator) streamLogs(ctx context.Context, runID, orgID uuid.UUID, threadID *uuid.UUID, turnNumber int, logCh <-chan LogEntry, tracker *runtimeProgressTracker) {
	for entry := range logCh {
		if tracker != nil {
			if progressType, strength, ok := runtimeProgressFromLog(entry); ok {
				tracker.Record(progressType, strength, entry.Timestamp)
			}
		}
		var metadata json.RawMessage
		if entry.Metadata != nil {
			var err error
			metadata, err = json.Marshal(entry.Metadata)
			if err != nil {
				o.logger.Warn().Err(err).Str("run_id", runID.String()).Msg("failed to marshal log entry metadata")
				metadata = nil
			}
		}

		effectiveThreadID := threadID
		if effectiveThreadID == nil {
			effectiveThreadID = entry.ThreadID
		}

		log := &models.SessionLog{
			SessionID:  runID,
			OrgID:      orgID,
			ThreadID:   effectiveThreadID,
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
	o.enqueueLinearMilestone(ctx, run, "failed")
}

// enqueueLinearMilestone schedules a linear_milestone job for the terminal
// session lifecycle states ("failed", "ended_no_pr"). The Linear linker is
// the single owner of the attachment subtitle / rolling-comment / state
// transition writes for these events; the orchestrator only fires the
// signal. Best effort — a failed enqueue logs and moves on so terminal
// session bookkeeping isn't held hostage by Linear-side hiccups.
//
// Routes through linear.EnqueueMilestone so the queue/priority/dedupe-key
// shape stays consistent with the PR-event and no-changes paths.
func (o *Orchestrator) enqueueLinearMilestone(ctx context.Context, run *models.Session, event string) {
	if o == nil || run == nil {
		return
	}
	linear.EnqueueMilestone(ctx, o.jobs, o.logger, run.OrgID, run.ID, event, 0)
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
	// Single canonical log per timeout: includes the canonical message that
	// SessionTimeoutBurst alerts key off, plus the platform-health fields
	// (agent_type, outcome, duration_ms) that the platform-health dashboard
	// uses. Emitting only one event keeps the alert and dashboard counts in
	// sync — a separate "agent run failed" event would double-count timeouts.
	event := log.Error().Err(underlyingErr).
		Str("agent_type", string(run.AgentType)).
		Str("outcome", "timeout").
		Float64("duration_ms", observability.DurationMillis(elapsed)).
		Dur("elapsed", elapsed)
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
	o.updatePrimaryThreadTerminal(cleanupCtx, run, models.ThreadStatusFailed, &models.SessionResult{
		Error:           &explanation,
		FailureCategory: strPtr(FailureCategoryTimeout),
	}, log)
}

// ensureCodexAuth injects Codex auth credentials into the sandbox. On failure
// it distinguishes three buckets:
//
//  1. Genuine auth invalidity (refresh-token revoked) — tagged at the source
//     via wrapCodexAuthInvalid in env.go and surfaced as codex_auth_expired
//     with a re-authenticate CTA. Permanent: marked failed inline since no
//     retry will help.
//  2. Sandbox/transport errors (Docker exec or file-write failure against a
//     container that's gone or a daemon that's hiccuping) — surfaced as
//     codex_auth_inject_failed with a retry CTA. Transient: deferred to the
//     dead-letter hook so the failure bookkeeping (status='failed' + Linear
//     "failed" milestone + user-visible assistant message) only fires when
//     retries are exhausted. Otherwise every retry would emit a stale Linear
//     "failed" ping and flicker session.status to "failed" mid-flight.
//  3. No credential configured — permanent, marked failed inline.
//
// It returns the resolved billing mode so downstream usage normalization can
// distinguish direct provider-reported USD from derived subscription credits.
func (o *Orchestrator) ensureCodexAuth(ctx context.Context, run *models.Session, sandbox *Sandbox, env map[string]string) (TokenBillingMode, error) {
	// For Codex, OPENAI_API_KEY is only populated when AgentEnv resolved an
	// OpenAI API-key credential. Subscription-backed Codex auth uses auth.json
	// instead and does not inject OPENAI_API_KEY. That makes the env var itself
	// authoritative across both unified and legacy fallback resolution paths.
	if env["OPENAI_API_KEY"] != "" {
		return TokenBillingModeAPIKey, nil
	}

	injected, err := o.env.InjectCodexAuthForUser(ctx, run.OrgID, run.TriggeredByUserID, sandbox)
	if err != nil {
		if errors.Is(err, ErrCodexAuthInvalid) {
			o.failRunWithCategory(ctx, run,
				fmt.Sprintf("codex auth injection failed: %s", err),
				FailureCategoryCodexAuth,
				"Your ChatGPT authentication has expired or was revoked. Please re-authenticate to continue using Codex.",
				[]string{"Re-authenticate with ChatGPT from the session page to sign in again"},
			)
			return TokenBillingModeUnknown, fmt.Errorf("codex auth injection: %w", err)
		}
		// Transient infra failure: defer the user-facing failure to the
		// dead-letter hook so retries don't churn session.status, emit
		// false Linear "failed" milestones, or flash a "failed" banner
		// in the UI that snaps back when a later attempt succeeds.
		o.registerSandboxInfraFailure(ctx, run,
			fmt.Sprintf("codex auth injection failed: %s", err),
			FailureCategoryCodexAuthInject,
			"Could not inject ChatGPT credentials into the sandbox because of an infrastructure error (Docker exec or file write failed). Your ChatGPT authentication itself is still valid — retry the session.",
			[]string{"Retry the session — the underlying sandbox error is usually transient", "If retries keep failing, check the worker logs for Docker errors"},
			"codex auth injection",
		)
		return TokenBillingModeUnknown, fmt.Errorf("codex auth injection: %w", err)
	}
	if !injected {
		o.failRunWithCategory(ctx, run,
			"no credentials configured for codex: connect ChatGPT from the Overview page",
			FailureCategoryCodexAuth,
			"No ChatGPT credentials are configured. Please connect your ChatGPT account to use Codex.",
			[]string{"Re-authenticate with ChatGPT from the session page to sign in"},
		)
		return TokenBillingModeUnknown, fmt.Errorf("no credentials for codex agent")
	}
	return TokenBillingModeSubscription, nil
}

func (o *Orchestrator) buildTokenUsageHint(
	ctx context.Context,
	agentType models.AgentType,
	orgID uuid.UUID,
	userID *uuid.UUID,
	env map[string]string,
	fallback TokenUsageHint,
) TokenUsageHint {
	hint := fallback
	hint.AgentType = agentType
	hint.EffectiveModel = o.effectiveAgentModel(ctx, orgID, agentType, env, fallback.EffectiveModel)
	hint.BillingMode = o.billingModeForAgent(ctx, agentType, orgID, userID, env, fallback.BillingMode)
	return hint
}

func (o *Orchestrator) effectiveAgentModel(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, env map[string]string, fallback string) string {
	if env == nil {
		env = map[string]string{}
	}
	switch agentType {
	case models.AgentTypePi:
		if env["PI_MODEL_CUSTOM"] != "" {
			return env["PI_MODEL_CUSTOM"]
		}
	case models.AgentTypeAmp:
		if env["AMP_MODE"] == "" {
			if fallback != "" {
				return fallback
			}
			return models.AmpModeSmart
		}
	}
	if envVar := models.ModelEnvVarForAgentType(agentType); envVar != "" && env[envVar] != "" {
		return env[envVar]
	}
	if o.env != nil {
		if agentConfig, ok := o.env.loadAgentConfig(ctx, orgID, agentType); ok {
			if envVar := models.ModelEnvVarForAgentType(agentType); envVar != "" {
				if cfg := agentConfig[string(agentType)]; cfg != nil && cfg[envVar] != "" {
					return cfg[envVar]
				}
			}
		}
	}
	return fallback
}

func (o *Orchestrator) billingModeForAgent(
	ctx context.Context,
	agentType models.AgentType,
	orgID uuid.UUID,
	userID *uuid.UUID,
	env map[string]string,
	fallback TokenBillingMode,
) TokenBillingMode {
	if fallback != "" && fallback != TokenBillingModeUnknown {
		return fallback
	}
	switch agentType {
	case models.AgentTypeCodex:
		if env["OPENAI_API_KEY"] != "" {
			return TokenBillingModeAPIKey
		}
		return TokenBillingModeSubscription
	case models.AgentTypeClaudeCode:
		if o.env != nil && o.env.unifiedCodingCredentialIsAPIKey(ctx, orgID, userID, models.ProviderAnthropic) {
			return TokenBillingModeAPIKey
		}
		if o.claudeCodeAuth != nil {
			active, err := o.claudeCodeAuth.HasActiveSubscription(ctx, orgID)
			if err == nil && active {
				return TokenBillingModeSubscription
			}
		}
		if env["ANTHROPIC_API_KEY"] != "" {
			return TokenBillingModeAPIKey
		}
		return TokenBillingModeUnknown
	case models.AgentTypeGeminiCLI, models.AgentTypeAmp, models.AgentTypePi:
		return TokenBillingModeAPIKey
	default:
		return TokenBillingModeUnknown
	}
}

// buildRunResult converts an AgentResult into the DB update struct.
func (o *Orchestrator) buildRunResult(ctx context.Context, run *models.Session, sandbox *Sandbox, result *AgentResult) *models.SessionResult {
	var tokenUsage []byte
	if HasPersistableTokenUsage(result.TokenUsage) {
		marshaled, err := json.Marshal(result.TokenUsage)
		if err != nil {
			o.logger.Warn().Err(err).Msg("failed to marshal token usage")
		} else {
			tokenUsage = marshaled
		}
	}

	headSHA := o.captureCurrentHeadSHA(ctx, sandbox)
	workspaceDirty := o.captureWorkspaceDirty(ctx, sandbox)

	return &models.SessionResult{
		ConfidenceScore:     &result.ConfidenceScore,
		ConfidenceReasoning: strPtr(result.ConfidenceReasoning),
		RiskFactors:         result.RiskFactors,
		TokenUsage:          tokenUsage,
		ResultSummary:       strPtr(result.Summary),
		Diff:                strPtr(result.Diff),
		Error:               strPtr(result.Error),
		DiffBaseCommitSHA:   run.BaseCommitSHA,
		DiffHeadCommitSHA:   headSHA,
		DiffWorkspaceDirty:  workspaceDirty,
		DiffCollectedAt:     timePtr(time.Now().UTC()),
		DiffSource:          "turn_complete",
	}
}

func (o *Orchestrator) captureCurrentHeadSHA(ctx context.Context, sandbox *Sandbox) *string {
	if sandbox == nil {
		return nil
	}
	var stdout, stderr bytes.Buffer
	exitCode, err := o.provider.Exec(ctx, sandbox, "git rev-parse HEAD", &stdout, &stderr)
	if err != nil {
		o.logger.Warn().Err(err).Msg("failed to capture current head sha")
		return nil
	}
	if exitCode != 0 {
		o.logger.Warn().Int("exit_code", exitCode).Str("stderr", stderr.String()).Msg("failed to capture current head sha")
		return nil
	}
	headSHA := strings.TrimSpace(stdout.String())
	if headSHA == "" {
		return nil
	}
	return &headSHA
}

func (o *Orchestrator) captureWorkspaceDirty(ctx context.Context, sandbox *Sandbox) bool {
	if sandbox == nil {
		return false
	}
	var stdout, stderr bytes.Buffer
	exitCode, err := o.provider.Exec(ctx, sandbox, "git status --porcelain --untracked-files=all -- .", &stdout, &stderr)
	if err != nil {
		o.logger.Warn().Err(err).Msg("failed to capture workspace dirty state")
		return false
	}
	if exitCode != 0 {
		o.logger.Warn().Int("exit_code", exitCode).Str("stderr", stderr.String()).Msg("failed to capture workspace dirty state")
		return false
	}
	return strings.TrimSpace(stdout.String()) != ""
}

func (o *Orchestrator) captureBaseCommitSHA(ctx context.Context, sandbox *Sandbox) (string, error) {
	var stdout, stderr bytes.Buffer
	exitCode, err := o.provider.Exec(ctx, sandbox, "git rev-parse HEAD", &stdout, &stderr)
	if err != nil {
		return "", fmt.Errorf("capture base commit sha: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("capture base commit sha: exit=%d stderr=%s", exitCode, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
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

	return o.writeClaudeCodeAuth(ctx, orgID, sandbox, *sub)
}

func (o *Orchestrator) writeClaudeCodeAuth(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox, sub models.AnthropicSubscription) (bool, error) {
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

func (o *Orchestrator) injectUnifiedClaudeCodeAuth(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, sandbox *Sandbox) (bool, error) {
	if o.env == nil || o.env.codingCredentials == nil {
		return false, nil
	}

	if picked, ok := o.env.lookupRecentCredential(orgID, userID, models.ProviderAnthropic); ok {
		return o.injectPickedUnifiedClaudeCodeAuth(ctx, orgID, sandbox, picked)
	}

	_, picked, handled := o.env.pickFromCodingProviderSet(ctx, orgID, userID, models.ProviderAnthropic, []models.ProviderName{
		models.ProviderAnthropic,
		models.ProviderAnthropicSubscription,
	})
	if !handled || picked == nil {
		return false, nil
	}
	return o.injectPickedUnifiedClaudeCodeAuth(ctx, orgID, sandbox, *picked)
}

func (o *Orchestrator) injectPickedUnifiedClaudeCodeAuth(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox, picked models.DecryptedCodingCredential) (bool, error) {
	if picked.Provider != models.ProviderAnthropicSubscription {
		return false, nil
	}
	cfg, ok := picked.Config.(models.AnthropicSubscriptionConfig)
	if !ok || cfg.AccessToken == "" || cfg.RefreshToken == "" {
		return false, nil
	}
	sub := models.AnthropicSubscription{
		AccessToken:   cfg.AccessToken,
		RefreshToken:  cfg.RefreshToken,
		ExpiresAt:     cfg.ExpiresAt,
		AccountType:   cfg.AccountType,
		RateLimitTier: cfg.RateLimitTier,
		Scopes:        cfg.Scopes,
	}
	if sub.NeedsRefresh(codexSubscriptionRefreshWindow) {
		refresher, ok := o.claudeCodeAuth.(ClaudeCodeAuthRefresher)
		if ok {
			// Build scope from the picked row's UserID. Personal credentials
			// (UserID != nil) require their scope on the lookup; passing
			// org scope would miss them in coding_credentials and surface
			// as "credential not found".
			scope := models.Scope{OrgID: orgID, UserID: picked.UserID}
			refreshed, err := refresher.RefreshTokenByID(ctx, scope, picked.ID)
			if err == nil && refreshed != nil {
				sub = *refreshed
			} else if sub.IsExpired() {
				if err != nil {
					return false, fmt.Errorf("refresh unified claude subscription %s: %w", picked.ID, err)
				}
				return false, fmt.Errorf("refresh unified claude subscription %s returned no token", picked.ID)
			} else if err != nil {
				o.logger.Warn().
					Err(err).
					Str("cred_id", picked.ID.String()).
					Msg("unified claude subscription refresh failed; using cached token")
			}
		} else if sub.IsExpired() {
			return false, fmt.Errorf("unified claude subscription %s is expired and no refresh provider is configured", picked.ID)
		}
	}
	return o.writeClaudeCodeAuth(ctx, orgID, sandbox, sub)
}

// ensureClaudeCodeAuth guarantees that the Claude Code agent has at least one
// credential path available in the sandbox. When the unified resolver selected
// an API key, that key wins; otherwise subscription file injection is preferred
// over the legacy ANTHROPIC_API_KEY fallback. The run only fails when neither
// path is configured.
func (o *Orchestrator) ensureClaudeCodeAuth(ctx context.Context, run *models.Session, sandbox *Sandbox, env map[string]string) (TokenBillingMode, error) {
	if env["ANTHROPIC_API_KEY"] != "" && o.env != nil && o.env.unifiedCodingCredentialIsAPIKey(ctx, run.OrgID, run.TriggeredByUserID, models.ProviderAnthropic) {
		return TokenBillingModeAPIKey, nil
	}

	injected, err := o.injectUnifiedClaudeCodeAuth(ctx, run.OrgID, run.TriggeredByUserID, sandbox)
	if err != nil {
		if fallbackErr := o.prepareClaudeCodeAPIKeyFallback(ctx, run, sandbox, env); fallbackErr == nil {
			o.logger.Warn().
				Err(err).
				Str("org_id", run.OrgID.String()).
				Str("session_id", run.ID.String()).
				Msg("unified claude subscription injection failed; continuing with Anthropic API-key fallback")
			return TokenBillingModeAPIKey, nil
		}
		o.failRunWithCategory(ctx, run,
			fmt.Sprintf("unified claude subscription injection failed: %s", err),
			FailureCategoryClaudeCodeAuth,
			"Your Claude subscription token could not be injected into the sandbox. The token may have been revoked or the refresh failed.",
			[]string{"Re-connect your Claude subscription from the Agent settings page"},
		)
		return TokenBillingModeUnknown, fmt.Errorf("unified claude code auth injection: %w", err)
	}
	if injected {
		return TokenBillingModeSubscription, nil
	}

	injected, err = o.injectClaudeCodeAuth(ctx, run.OrgID, sandbox)
	if err != nil {
		if fallbackErr := o.prepareClaudeCodeAPIKeyFallback(ctx, run, sandbox, env); fallbackErr == nil {
			o.logger.Warn().
				Err(err).
				Str("org_id", run.OrgID.String()).
				Str("session_id", run.ID.String()).
				Msg("claude subscription injection failed; continuing with Anthropic API-key fallback")
			return TokenBillingModeAPIKey, nil
		} else if !errors.Is(fallbackErr, errClaudeCodeFallbackUnavailable) {
			o.failRunWithCategory(ctx, run,
				fmt.Sprintf("claude subscription injection failed and API-key fallback could not be prepared: %s", fallbackErr),
				FailureCategoryClaudeCodeAuth,
				"Your Claude subscription token could not be injected, and the sandbox could not be prepared to use the Anthropic API key fallback.",
				[]string{"Retry the session after reconnecting your Claude subscription or verifying Anthropic credentials"},
			)
			return TokenBillingModeUnknown, fmt.Errorf("prepare claude code API-key fallback: %w", fallbackErr)
		}
		o.failRunWithCategory(ctx, run,
			fmt.Sprintf("claude subscription injection failed: %s", err),
			FailureCategoryClaudeCodeAuth,
			"Your Claude subscription token could not be injected into the sandbox. The token may have been revoked or the refresh failed.",
			[]string{"Re-connect your Claude subscription from the Agent settings page"},
		)
		return TokenBillingModeUnknown, fmt.Errorf("claude code auth injection: %w", err)
	}
	if injected {
		return TokenBillingModeSubscription, nil
	}

	// No subscription — check for an Anthropic API-key fallback. The env var
	// was already baked into sandboxCfg.Env by resolveAgentEnv, so if the
	// credential exists the sandbox is already configured.
	if fallbackErr := o.prepareClaudeCodeAPIKeyFallback(ctx, run, sandbox, env); fallbackErr == nil {
		return TokenBillingModeAPIKey, nil
	} else if !errors.Is(fallbackErr, errClaudeCodeFallbackUnavailable) {
		o.failRunWithCategory(ctx, run,
			fmt.Sprintf("claude API-key fallback could not be prepared: %s", fallbackErr),
			FailureCategoryClaudeCodeAuth,
			"The Anthropic API key fallback is configured, but the sandbox could not be prepared to use it because stale Claude credentials could not be cleared.",
			[]string{"Retry the session after reconnecting your Claude subscription or verifying sandbox access"},
		)
		return TokenBillingModeUnknown, fmt.Errorf("prepare claude code API-key fallback: %w", fallbackErr)
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
	return TokenBillingModeUnknown, fmt.Errorf("no credentials for claude code agent")
}

var errClaudeCodeFallbackUnavailable = errors.New("claude code API-key fallback unavailable")

func (o *Orchestrator) prepareClaudeCodeAPIKeyFallback(ctx context.Context, run *models.Session, sandbox *Sandbox, env map[string]string) error {
	if env["ANTHROPIC_API_KEY"] == "" {
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
func (o *Orchestrator) handleCancelledSession(ctx context.Context, session *models.Session, sandbox *Sandbox, result *AgentResult, turnNumber int, log zerolog.Logger) {
	bgCtx := context.Background()
	lockToken, _ := jobctx.LockTokenFromContext(ctx)

	// Attempt to snapshot so the session can be continued later.
	snapshotKey, snapshotSize, snapshotErr := o.snapshotSession(bgCtx, session, sandbox, nil)
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
		checkpointedAt := time.Now().UTC()
		if _, err := o.sessions.PublishCheckpoint(bgCtx, session.OrgID, session.ID, lockToken, agentSessionID, snapshotKey, models.CheckpointKindGracefulStop, checkpointCapabilityForAgent(session.AgentType), snapshotSize, checkpointedAt, nil, models.RuntimeStopReasonUserCancel); err != nil {
			log.Warn().Err(err).Msg("failed to publish cancelled-session checkpoint metadata")
		}
		if err := o.sessions.UpdateTurnComplete(bgCtx, session.OrgID, session.ID, turnNumber, nil, agentSessionID, snapshotKey); err != nil {
			log.Warn().Err(err).Msg("failed to return cancelled session to idle")
			_ = o.sessions.UpdateStatus(bgCtx, session.OrgID, session.ID, string(models.SessionStatusCancelled))
			o.updatePrimaryThreadTerminal(bgCtx, session, models.ThreadStatusCancelled, nil, log)
		} else {
			log.Info().Int("turn", turnNumber).Msg("cancelled session returned to idle")
			if o.sessionThreads != nil && session.PrimaryThreadID != nil && *session.PrimaryThreadID != uuid.Nil {
				if threadErr := o.sessionThreads.CompleteTurn(bgCtx, session.OrgID, *session.PrimaryThreadID, turnNumber, agentSessionID); threadErr != nil {
					log.Warn().Err(threadErr).Str("thread_id", session.PrimaryThreadID.String()).Msg("failed to return primary thread to idle after cancel")
				}
			}
		}
	} else {
		_ = o.sessions.UpdateStatus(bgCtx, session.OrgID, session.ID, string(models.SessionStatusCancelled))
		o.updatePrimaryThreadTerminal(bgCtx, session, models.ThreadStatusCancelled, nil, log)
	}
}

// snapshotSessionOnTurnSuccess wraps snapshotSession with the guard the
// "normal completion" paths (RunAgent / ContinueSession success branches)
// need: skip the snapshot when result.ExitCode != 0. That's the signal that
// the agent CLI — and likely the sandbox runtime under it — crashed mid-turn,
// leaving the workspace incoherent.
//
// The cancel and policy-stop paths intentionally do NOT use this wrapper:
// their non-zero exits just mean the agent caught the signal and shut down
// cleanly, so the workspace state is still valid. Calling this only on the
// success path keeps both invariants:
//   - graceful stops still produce a resumable checkpoint
//   - a sandbox crash never overwrites a known-good prior snapshot with a
//     truncated archive (incident: a 298-byte garbage upload bricked an
//     active session for the rest of its lifetime)
func (o *Orchestrator) snapshotSessionOnTurnSuccess(ctx context.Context, session *models.Session, sandbox *Sandbox, result *AgentResult, log zerolog.Logger) (string, int64, error) {
	if result != nil && result.ExitCode != 0 {
		log.Warn().
			Int("exit_code", result.ExitCode).
			Str("agent_error", truncateForLog(result.Error, 256)).
			Msg("agent exited non-zero on the success path; skipping snapshot to preserve any prior good checkpoint")
		return "", 0, nil
	}
	return o.snapshotSession(ctx, session, sandbox, result)
}

// snapshotSession snapshots the sandbox workspace to object storage for multi-turn support.
// If snapshots are not configured, this is a no-op. This only saves the snapshot
// and updates sandbox state — it does NOT change session status or call UpdateTurnComplete.
// result is unused but kept in the signature for future extensibility (e.g. metadata).
//
// Most callers should use snapshotSessionOnTurnSuccess; only the cancel and
// policy-stop paths legitimately bypass the exit-code guard because they
// know the non-zero exit was a graceful shutdown.
func (o *Orchestrator) snapshotSession(ctx context.Context, session *models.Session, sandbox *Sandbox, result *AgentResult) (string, int64, error) {
	if o.snapshots == nil {
		return "", 0, nil
	}

	snapshotKey := fmt.Sprintf("snapshots/%s/%s/workspace.tar.zst", session.OrgID, session.ID)

	reader, err := o.provider.Snapshot(ctx, sandbox)
	if err != nil {
		return "", 0, fmt.Errorf("snapshot sandbox: %w", err)
	}
	defer reader.Close()

	countingReader := &countingReadCloser{ReadCloser: reader}

	if err := o.snapshots.Save(ctx, snapshotKey, countingReader); err != nil {
		return "", 0, fmt.Errorf("save snapshot to storage: %w", err)
	}

	// Store the snapshot key on the session for subsequent use.
	session.SnapshotKey = &snapshotKey

	return snapshotKey, countingReader.n, nil
}

type countingReadCloser struct {
	io.ReadCloser
	n int64
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.n += int64(n)
	return n, err
}

// truncateForLog clips s to at most max bytes (rune-safe), appending "…"
// when truncation occurs. Used when an unbounded user/CLI string is included
// in a structured log field.
func truncateForLog(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Trim back to the previous rune boundary so we don't split a UTF-8
	// codepoint when the cutoff lands mid-encoding.
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func (o *Orchestrator) handlePolicyStoppedSession(ctx context.Context, session *models.Session, sandbox *Sandbox, result *AgentResult, turnNumber int, reason StopReason, log zerolog.Logger) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lockToken, _ := jobctx.LockTokenFromContext(ctx)
	checkpointedAt := time.Now().UTC()
	agentSessionID := ""
	if result != nil && result.AgentSessionID != "" {
		agentSessionID = result.AgentSessionID
	} else if session.AgentSessionID != nil {
		agentSessionID = *session.AgentSessionID
	}

	snapshotKey, snapshotSize, snapshotErr := o.snapshotSession(bgCtx, session, sandbox, result)
	var checkpointErrText *string
	if snapshotErr != nil {
		errMsg := snapshotErr.Error()
		checkpointErrText = &errMsg
	}
	if snapshotKey != "" || checkpointErrText != nil {
		if _, err := o.sessions.PublishCheckpoint(bgCtx, session.OrgID, session.ID, lockToken, agentSessionID, snapshotKey, models.CheckpointKindGracefulStop, checkpointCapabilityForAgent(session.AgentType), snapshotSize, checkpointedAt, checkpointErrText, stopReasonToRuntime(reason)); err != nil {
			log.Warn().Err(err).Msg("failed to publish graceful-stop checkpoint metadata")
		}
	}

	errMsg, explanation, nextSteps := gracefulStopFailure(reason, snapshotKey != "", session.SnapshotKey != nil && *session.SnapshotKey != "")
	o.failRunWithCategory(bgCtx, session, errMsg, FailureCategoryTimeout, explanation, nextSteps)
	o.updatePrimaryThreadTerminal(bgCtx, session, models.ThreadStatusFailed, &models.SessionResult{
		Error:           &explanation,
		FailureCategory: strPtr(FailureCategoryTimeout),
	}, log)
}

func gracefulStopFailure(reason StopReason, checkpointedThisTurn, hadPriorCheckpoint bool) (string, string, []string) {
	reasonText := "The session reached its runtime budget."
	switch reason {
	case StopReasonNoProgress:
		reasonText = "The session stopped after going too long without meaningful progress."
	case StopReasonAbsoluteCeiling:
		reasonText = "The session hit the absolute runtime ceiling."
	}

	if checkpointedThisTurn {
		return reasonText + " The latest state was saved.", reasonText + " We stopped the run cleanly and saved a resumable checkpoint for the latest state.", []string{
			"Resume the session from the saved checkpoint",
			"Provide narrower follow-up guidance if the run was heading into a large search space",
			"Retry later if the repository or toolchain was unusually slow",
		}
	}
	if hadPriorCheckpoint {
		return reasonText + " The latest turn could not be checkpointed, but the previous checkpoint is still available.", reasonText + " We rolled the session back to the previously committed checkpoint because the current turn could not be saved cleanly.", []string{
			"Resume from the previous checkpoint",
			"Split the remaining work into a smaller follow-up turn",
			"Retry later if the stop happened during a large tool invocation",
		}
	}
	return reasonText + " The latest in-flight work could not be saved.", reasonText + " The run had to stop before any durable checkpoint was available for this turn.", []string{
		"Retry the session from scratch",
		"Split the task into smaller pieces so checkpoints publish sooner",
		"Reduce broad repository scans or long-running commands in the prompt",
	}
}

func (o *Orchestrator) createAssistantMessage(ctx context.Context, sessionID, orgID uuid.UUID, threadID *uuid.UUID, turnNumber int, result *AgentResult) error {
	if o.sessionMessages == nil {
		return nil
	}

	assistantMsg := &models.SessionMessage{
		SessionID:  sessionID,
		OrgID:      orgID,
		ThreadID:   threadID,
		TurnNumber: turnNumber,
		Role:       models.MessageRoleAssistant,
		Content:    result.Summary,
	}
	if HasPersistableTokenUsage(result.TokenUsage) {
		tokenJSON, err := json.Marshal(result.TokenUsage)
		if err != nil {
			return fmt.Errorf("marshal token usage: %w", err)
		}
		assistantMsg.TokenUsage = tokenJSON
	}
	if err := o.sessionMessages.Create(ctx, assistantMsg); err != nil {
		return err
	}
	if marker, ok := o.agentRunLogs.(interface {
		MarkAssistantTranscriptDuplicate(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID, turnNumber int, message string) error
	}); ok && result.Summary != "" {
		if err := marker.MarkAssistantTranscriptDuplicate(ctx, orgID, sessionID, threadID, turnNumber, result.Summary); err != nil {
			o.logger.Warn().
				Err(err).
				Str("session_id", sessionID.String()).
				Str("org_id", orgID.String()).
				Int("turn_number", turnNumber).
				Msg("failed to mark assistant output log as transcript duplicate")
		}
	}
	return nil
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

// updatePrimaryThreadTerminal mirrors a session-level terminal transition onto
// the seeded primary thread row. When result is non-nil, it persists the same
// failure_explanation / failure_category / result_summary / diff the session
// got so per-thread review surfaces (Phase 2) have data to display; when nil,
// it just transitions the status (e.g. cancel without a result payload). All
// errors are logged best-effort because this is bookkeeping — a failure here
// must not abort the surrounding session-level cleanup.
func (o *Orchestrator) updatePrimaryThreadTerminal(ctx context.Context, run *models.Session, status models.ThreadStatus, result *models.SessionResult, log zerolog.Logger) {
	if o.sessionThreads == nil || run == nil || run.PrimaryThreadID == nil || *run.PrimaryThreadID == uuid.Nil {
		return
	}
	threadID := *run.PrimaryThreadID
	var err error
	if result != nil {
		err = o.sessionThreads.UpdateResult(ctx, run.OrgID, threadID, status, result)
	} else {
		err = o.sessionThreads.UpdateStatus(ctx, run.OrgID, threadID, status)
	}
	if err != nil {
		log.Warn().Err(err).
			Str("thread_id", threadID.String()).
			Str("status", string(status)).
			Msg("failed to update primary thread terminal status")
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
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

func (o *Orchestrator) ResolveAbsoluteRuntimeCeiling(ctx context.Context, orgID uuid.UUID) time.Duration {
	return o.resolveRuntimeConfig(ctx, orgID).AbsoluteRuntimeCeiling
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

func sessionWorkingBranch(run *models.Session, issue *models.Issue) string {
	if run != nil && run.WorkingBranch != nil && *run.WorkingBranch != "" {
		return *run.WorkingBranch
	}
	return formatWorkingBranch(run, issue)
}

// formatWorkingBranch generates a branch name for an agent session.
// Format: 143/<short-id>/<slug> so the local working branch and the PR push
// branch stay identical across fresh runs, resumes, and PR creation.
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

	return fmt.Sprintf("143/%s/%s", short, slug)
}

// shellEscapeSingleQuote escapes single quotes for safe use in shell commands.
func shellEscapeSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var errNoBaseCommitSHA = errors.New("base commit sha is required")

func (o *Orchestrator) collectWorkspaceDiff(ctx context.Context, sandbox *Sandbox, baseCommitSHA, targetBranch string) (string, error) {
	var checkStdout, checkStderr bytes.Buffer
	checkExit, err := o.provider.Exec(ctx, sandbox, "git rev-parse --is-inside-work-tree", &checkStdout, &checkStderr)
	if err != nil {
		return "", fmt.Errorf("check git repo: %w", err)
	}
	if checkExit != 0 {
		return "", nil
	}
	if baseCommitSHA == "" {
		return "", errNoBaseCommitSHA
	}

	diffBase := o.resolveWorkspaceDiffBase(ctx, sandbox, baseCommitSHA, targetBranch)
	diffCmd := fmt.Sprintf("git diff --find-renames --binary %s -- .", shellEscapeSingleQuote(diffBase))

	var stdout, stderr bytes.Buffer
	exitCode, err := o.provider.Exec(ctx, sandbox, diffCmd, &stdout, &stderr)
	if err != nil {
		return "", fmt.Errorf("exec git diff: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("git diff exited with code %d: %s", exitCode, stderr.String())
	}

	untrackedDiff, err := o.collectUntrackedDiffs(ctx, sandbox)
	if err != nil {
		return "", err
	}
	return stdout.String() + untrackedDiff, nil
}

func (o *Orchestrator) resolveWorkspaceDiffBase(ctx context.Context, sandbox *Sandbox, baseCommitSHA, targetBranch string) string {
	if targetBranch == "" {
		return baseCommitSHA
	}

	escapedBranch := shellEscapeSingleQuote(targetBranch)
	var fetchErr bytes.Buffer
	fetchCmd := fmt.Sprintf("git fetch --quiet --no-tags origin '%s'", escapedBranch)
	fetchExit, fetchExecErr := o.provider.Exec(ctx, sandbox, fetchCmd, io.Discard, &fetchErr)

	var mbOut, mbErr bytes.Buffer
	mbCmd := fmt.Sprintf("git merge-base 'origin/%s' HEAD", escapedBranch)
	exitCode, err := o.provider.Exec(ctx, sandbox, mbCmd, &mbOut, &mbErr)
	if err != nil || exitCode != 0 {
		o.logger.Debug().
			Str("target_branch", targetBranch).
			Str("fallback_base_commit_sha", baseCommitSHA).
			Int("fetch_exit", fetchExit).
			AnErr("fetch_exec_err", fetchExecErr).
			Str("fetch_stderr", strings.TrimSpace(fetchErr.String())).
			Int("merge_base_exit", exitCode).
			AnErr("merge_base_exec_err", err).
			Str("merge_base_stderr", strings.TrimSpace(mbErr.String())).
			Msg("workspace diff: merge-base unavailable, falling back to base commit sha")
		return baseCommitSHA
	}
	mb := strings.TrimSpace(mbOut.String())
	if mb == "" {
		return baseCommitSHA
	}
	return mb
}

func (o *Orchestrator) collectUntrackedDiffs(ctx context.Context, sandbox *Sandbox) (string, error) {
	var stdout, stderr bytes.Buffer
	exitCode, err := o.provider.Exec(ctx, sandbox, "git ls-files --others --exclude-standard -- .", &stdout, &stderr)
	if err != nil {
		return "", fmt.Errorf("list untracked files: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("git ls-files exited with code %d: %s", exitCode, stderr.String())
	}

	var builder strings.Builder
	for _, filePath := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		filePath = strings.TrimSpace(filePath)
		if filePath == "" {
			continue
		}

		var fileStdout, fileStderr bytes.Buffer
		cmd := fmt.Sprintf("git diff --find-renames --binary --no-index -- /dev/null '%s'", shellEscapeSingleQuote(filePath))
		exitCode, err = o.provider.Exec(ctx, sandbox, cmd, &fileStdout, &fileStderr)
		if err != nil {
			return "", fmt.Errorf("diff untracked file %s: %w", filePath, err)
		}
		if exitCode != 0 && exitCode != 1 {
			return "", fmt.Errorf("git diff for untracked file %s exited with code %d: %s", filePath, exitCode, fileStderr.String())
		}
		builder.WriteString(fileStdout.String())
	}
	return builder.String(), nil
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
	userID *uuid.UUID,
	sessionID uuid.UUID,
	threadID *uuid.UUID,
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

	reinjected, reinjectErr := o.env.InjectCodexAuthForUser(ctx, orgID, userID, sandbox)
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
		o.streamLogs(ctx, sessionID, orgID, threadID, turnNumber, retryLogCh, nil)
	}()

	result, err = adapter.Execute(execCtx, sandbox, prompt, retryLogCh)
	close(retryLogCh)
	retryLogWg.Wait()

	log.Info().Msg("codex CLI retry after token refresh completed")
	return result, err
}

// shedOnRunResult forwards rate-limit / auth-rejected signals from a finished
// run back into the unified credential store's in-process health cache so the
// next pickFromCodingProvider walk for the same (orgID, userID, provider)
// skips the just-failed credential until its TTL expires. The OAuth services
// independently flip persisted credential status to "invalid" on hard auth
// failures; this is the fast-path hint that prevents repeat picks before the
// resolver cache refreshes.
//
// Provider mapping is intentionally limited to agent types whose API-key auth
// flows go through AgentEnv.pickFromCodingProvider. Codex subscription auth
// uses codexauth.Service's own selector; because that path records no
// ProviderOpenAI pick, the shed call below remains a no-op for subscription
// runs.
func (o *Orchestrator) shedOnRunResult(agentType models.AgentType, orgID uuid.UUID, userID *uuid.UUID, result *AgentResult, runErr error, log zerolog.Logger) {
	if o.env == nil || result == nil {
		return
	}
	provider := codingProviderForAgent(agentType)
	if provider == "" {
		return
	}
	errMsg := strings.ToLower(result.Error)
	switch {
	case isAuthRejectedError(errMsg):
		log.Warn().
			Str("agent_type", string(agentType)).
			Str("provider", string(provider)).
			Msg("shedding credential after auth-rejected signal in run result")
		o.env.ShedAuthRejected(orgID, userID, provider)
	case isRateLimitedError(errMsg):
		log.Warn().
			Str("agent_type", string(agentType)).
			Str("provider", string(provider)).
			Msg("shedding credential after rate-limit signal in run result")
		o.env.ShedRateLimited(orgID, userID, provider)
	}
	_ = runErr // accepted for symmetry with the call sites; the result.Error string is the canonical signal today
}

// codingProviderForAgent maps an agent type to the unified provider name its
// API-key auth uses. Subscription picks are intentionally not represented
// here; they do not write the recentPicks tracker under these providers.
func codingProviderForAgent(agentType models.AgentType) models.ProviderName {
	switch agentType {
	case models.AgentTypeClaudeCode:
		return models.ProviderAnthropic
	case models.AgentTypeCodex:
		return models.ProviderOpenAI
	case models.AgentTypeGeminiCLI:
		return models.ProviderGemini
	case models.AgentTypeAmp:
		return models.ProviderAmp
	case models.AgentTypePi:
		return models.ProviderPi
	default:
		return ""
	}
}

// isRateLimitedError matches the same surface as the failure classifier so
// shedding stays consistent with the user-facing api_error category.
func isRateLimitedError(errMsg string) bool {
	if errMsg == "" {
		return false
	}
	return strings.Contains(errMsg, "rate limit") ||
		strings.Contains(errMsg, "rate_limit") ||
		strings.Contains(errMsg, "429") ||
		strings.Contains(errMsg, "too many requests")
}

// isAuthRejectedError detects 401-class signals indicating the credential is
// structurally bad (expired refresh, revoked, invalid key). Token-expired
// transients are excluded because retryOnTokenExpired already refreshed and
// retried; if that retry succeeded the result is clean, and if it failed the
// error text shifts to the strings matched here.
func isAuthRejectedError(errMsg string) bool {
	if errMsg == "" {
		return false
	}
	return strings.Contains(errMsg, "refresh_token_reused") ||
		strings.Contains(errMsg, "invalid_grant") ||
		strings.Contains(errMsg, "invalid api key") ||
		strings.Contains(errMsg, "invalid_api_key") ||
		strings.Contains(errMsg, "401 unauthorized") ||
		strings.Contains(errMsg, "401 unauthenticated") ||
		strings.Contains(errMsg, "authentication_error")
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
