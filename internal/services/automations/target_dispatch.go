package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// Per-target continuity: the ownership transaction (design doc 125, "Turn
// Ownership and Dispatch", "Continuation Decision"). TargetDispatcher turns a
// pending run of a per_target automation into the target's executing turn,
// or records it as waiting, in one database transaction with a fixed lock
// order: automation-scoped advisory lock, target row, run row, session.

const (
	// AutomationTurnLimit is the number of turns a generation may run before
	// it is retired and the next trigger starts fresh.
	AutomationTurnLimit = 25
	// AutomationCheckpointSizeLimit bounds the snapshot a generation may
	// restore from; a larger checkpoint retires the generation.
	AutomationCheckpointSizeLimit int64 = 2 << 30
	// automationPendingSnapshotRetry is how long dispatch waits before
	// re-checking a session whose snapshot upload is still in flight.
	automationPendingSnapshotRetry = 15 * time.Second
	// automationPendingSnapshotGrace is how long a pending upload is trusted
	// before dispatch treats a published key as the checkpoint.
	automationPendingSnapshotGrace = 3 * time.Minute
	// automationHeadLookupRetryMin and Max bound the backoff when an
	// ambiguous push cannot be resolved because the lookup failed: 30 s
	// doubling to 10 min.
	automationHeadLookupRetryMin = 30 * time.Second
	automationHeadLookupRetryMax = 10 * time.Minute
	// automationLifecycleStaleAfter is how old a stored lifecycle state may
	// be before creating a new generation revalidates it against GitHub.
	automationLifecycleStaleAfter = time.Hour
	// automationConfigChangeRetry is the delay before a run re-enters
	// dispatch after its automation changed underneath it.
	automationConfigChangeRetry = time.Second

	// AutomationTurnJobQueue is the queue per-target turns run on.
	AutomationTurnJobQueue = "agent"
)

// AutomationTurnDedupeKey scopes the turn's job to the run, not the thread:
// the previous turn's job may still be running while its completion
// executes, so a thread-scoped key would drop the follow-up enqueue.
func AutomationTurnDedupeKey(runID uuid.UUID) string {
	return "automation_turn:" + runID.String()
}

// AutomationTurnJobPayload is the payload of the job that runs a per-target
// turn. The same shape is used for a fresh generation's run_agent job and a
// continued generation's continue_session job; the worker decodes it with
// the field types declared here.
type AutomationTurnJobPayload struct {
	SessionID         string `json:"session_id"`
	OrgID             string `json:"org_id"`
	ThreadID          string `json:"thread_id"`
	AutomationRunID   string `json:"automation_run_id"`
	TargetGeneration  int    `json:"target_generation"`
	ContinuationMode  string `json:"continuation_mode"`
	HeadSHA           string `json:"head_sha"`
	PullRequestNumber int    `json:"pull_request_number"`
	StructuredPrompt  string `json:"structured_prompt,omitempty"`
}

// PullRequestHeadInfo is what the dispatch-time lookup returns.
type PullRequestHeadInfo struct {
	SHA        string
	UpdatedAt  *time.Time
	State      string
	BaseBranch string
}

// PullRequestHeadResolver fetches a pull request's current head from GitHub
// with the repository's installation token.
type PullRequestHeadResolver interface {
	ResolvePullRequestHead(ctx context.Context, orgID, repositoryID uuid.UUID, pullRequestNumber int) (PullRequestHeadInfo, error)
}

// DispatchKind is the outcome of one ownership transaction.
type DispatchKind string

const (
	// DispatchReserved means the run now executes; a job was enqueued.
	DispatchReserved DispatchKind = "reserved"
	// DispatchAlreadyReserved means an earlier attempt reserved the run; the
	// existing job carries it.
	DispatchAlreadyReserved DispatchKind = "already_reserved"
	// DispatchWaiting means the target is busy and the run waits.
	DispatchWaiting DispatchKind = "waiting"
	// DispatchTerminalized means the run ended without executing.
	DispatchTerminalized DispatchKind = "terminalized"
	// DispatchRetry means the decision could not be made yet (a pending
	// snapshot, an unresolved head, a changed automation); the caller retries
	// after RetryAfter without spending the job's attempt budget, for at
	// most MaxWait.
	DispatchRetry DispatchKind = "retry"
	// DispatchNotApplicable means the run is not a per-target run; the
	// caller dispatches it as an ordinary per-run session.
	DispatchNotApplicable DispatchKind = "not_applicable"
)

// DispatchOutcome reports what the ownership transaction did.
type DispatchOutcome struct {
	Kind               DispatchKind
	JobID              uuid.UUID
	SessionID          uuid.UUID
	ThreadID           uuid.UUID
	ContinuationMode   models.AutomationRunContinuationMode
	ContinuationReason *models.AutomationRunContinuationReason
	OutcomeReason      models.AutomationRunOutcomeReason
	RetryAfter         time.Duration
	MaxWait            time.Duration
	Note               string
}

// DispatchInput is what the worker hands the dispatcher: the run and its
// automation as loaded, plus the session template it would create for a
// fresh per-run session (agent type, repository, branch, brief, capability
// snapshot). The dispatcher rereads the automation under the lock and
// derives the executing identity from the current row.
type DispatchInput struct {
	Run             models.AutomationRun
	Automation      models.Automation
	SessionTemplate *models.Session
	// KillSwitch mirrors AUTOMATION_SESSION_CONTINUITY_DISABLED on the
	// worker: forces fresh per-run sessions and leaves target rows untouched.
	KillSwitch bool
}

// TargetDispatcher runs the ownership transaction.
type TargetDispatcher struct {
	txStarter   db.TxStarter
	automations *db.AutomationStore
	targets     *db.AutomationTargetStore
	runs        *db.AutomationRunStore
	sessions    *db.SessionStore
	threads     *db.SessionThreadStore
	jobs        *db.JobStore
	heads       PullRequestHeadResolver
	logger      zerolog.Logger
	now         func() time.Time

	maxSnapshotAge      time.Duration
	checkpointSizeLimit int64
}

func NewTargetDispatcher(txStarter db.TxStarter, automations *db.AutomationStore, targets *db.AutomationTargetStore, runs *db.AutomationRunStore, sessions *db.SessionStore, threads *db.SessionThreadStore, jobs *db.JobStore, logger zerolog.Logger) *TargetDispatcher {
	return &TargetDispatcher{
		txStarter:           txStarter,
		automations:         automations,
		targets:             targets,
		runs:                runs,
		sessions:            sessions,
		threads:             threads,
		jobs:                jobs,
		logger:              logger,
		now:                 time.Now,
		checkpointSizeLimit: AutomationCheckpointSizeLimit,
	}
}

// SetHeadResolver enables the dispatch-time GitHub head lookup. Without it
// push runs review their delivered head in degraded mode and ambiguous
// candidates wait for the ambiguity deadline.
func (d *TargetDispatcher) SetHeadResolver(resolver PullRequestHeadResolver) {
	d.heads = resolver
}

// SetMaxSnapshotAge bounds how old a checkpoint may be to count as ready;
// zero disables the bound.
func (d *TargetDispatcher) SetMaxSnapshotAge(age time.Duration) {
	d.maxSnapshotAge = age
}

// automationRunGitHubContext is the pull request context captured in a
// run's config_snapshot at trigger time. HeadSHA and BaseBranch are the
// delivered values; dispatch may resolve newer ones.
type automationRunGitHubContext struct {
	Repository        string `json:"repository"`
	PullRequestNumber int    `json:"pull_request_number"`
	PullRequestURL    string `json:"pull_request_url"`
	HeadSHA           string `json:"head_sha"`
	BaseBranch        string `json:"base_branch"`
}

func githubContextFromRun(run models.AutomationRun) (automationRunGitHubContext, error) {
	var snapshot struct {
		GitHub automationRunGitHubContext `json:"github"`
	}
	if len(run.ConfigSnapshot) == 0 {
		return automationRunGitHubContext{}, nil
	}
	if err := json.Unmarshal(run.ConfigSnapshot, &snapshot); err != nil {
		return automationRunGitHubContext{}, fmt.Errorf("parse automation run github context: %w", err)
	}
	return snapshot.GitHub, nil
}

// Applies reports whether the run should go through the ownership
// transaction: continuity is read from the automation row now (the
// snapshot keeps it for audit), the worker kill switch wins, and the run
// must have been attached to a target at arrival. Dispatch rereads the
// automation under the lock and returns DispatchNotApplicable if the mode
// changed in between.
func (d *TargetDispatcher) Applies(in DispatchInput) bool {
	if in.KillSwitch {
		return false
	}
	if in.Automation.SessionContinuity.OrDefault() != models.AutomationSessionContinuityPerTarget {
		return false
	}
	return in.Run.TargetID != nil
}

// Dispatch runs the ownership transaction for the run and returns what
// happened. Any failure rolls back every step and leaves the run pending
// for the job's retry.
func (d *TargetDispatcher) Dispatch(ctx context.Context, in DispatchInput) (DispatchOutcome, error) {
	if !d.Applies(in) {
		return DispatchOutcome{Kind: DispatchNotApplicable}, nil
	}
	if in.SessionTemplate == nil {
		return DispatchOutcome{}, errors.New("automation dispatch requires a session template")
	}
	orgID := in.Run.OrgID
	github, err := githubContextFromRun(in.Run)
	if err != nil {
		return DispatchOutcome{}, err
	}
	repositoryID := targetRepository(in)
	if repositoryID == nil {
		return DispatchOutcome{}, errors.New("automation dispatch requires a repository")
	}

	tx, err := d.txStarter.Begin(ctx)
	if err != nil {
		return DispatchOutcome{}, fmt.Errorf("begin automation dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	target, err := d.targets.LockOrCreate(ctx, tx, orgID, in.Automation.ID, *repositoryID, models.AutomationTargetKindGitHubPullRequest, targetKeyForRun(in.Run, github))
	if err != nil {
		return DispatchOutcome{}, err
	}

	// The automation row is reread under the advisory lock: a continuity
	// switch or a configuration change that committed while this run
	// waited for the lock must not be dispatched from stale input.
	automation, err := d.automations.GetByID(ctx, orgID, in.Automation.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DispatchOutcome{Kind: DispatchNotApplicable, Note: "automation deleted"}, nil
		}
		return DispatchOutcome{}, fmt.Errorf("reread automation: %w", err)
	}
	if automation.SessionContinuity.OrDefault() != models.AutomationSessionContinuityPerTarget {
		return DispatchOutcome{Kind: DispatchNotApplicable, Note: "automation is per_run"}, nil
	}
	if !automation.UpdatedAt.Equal(in.Automation.UpdatedAt) {
		return DispatchOutcome{Kind: DispatchRetry, RetryAfter: automationConfigChangeRetry, MaxWait: time.Minute, Note: "automation changed since the run was loaded"}, nil
	}
	expectedUser, err := automationExecutingUser(automation)
	if err != nil {
		return DispatchOutcome{}, err
	}

	run, err := d.runs.GetByRunIDForUpdate(ctx, tx, orgID, in.Run.ID)
	if err != nil {
		return DispatchOutcome{}, err
	}
	if run.DispatchState != nil && *run.DispatchState == models.AutomationRunDispatchExecuting {
		// A retry of the automation_run job after the reservation committed:
		// the turn's own job carries it from here.
		return DispatchOutcome{Kind: DispatchAlreadyReserved, SessionID: derefUUID(run.SessionID), ThreadID: derefUUID(run.ThreadID)}, nil
	}
	if run.Status != models.AutomationRunStatusPending {
		return DispatchOutcome{Kind: DispatchTerminalized, Note: "run is no longer pending"}, nil
	}

	// Lifecycle revalidation from the stored state; a stale state is
	// revalidated against GitHub below before a new generation is created.
	event := runEvent(run)
	if !lifecycleAllowsRun(target.LifecycleState, event) {
		return d.terminalize(ctx, tx, orgID, run.ID, models.AutomationRunOutcomePRClosed, "pull request is no longer open")
	}

	lookup := d.headLookup(ctx, orgID, target.RepositoryID, github.PullRequestNumber)
	resolved, err := d.resolveHead(ctx, tx, orgID, &target, run, github, lookup)
	if err != nil {
		return DispatchOutcome{}, err
	}
	switch {
	case resolved.retryAfter > 0:
		if _, err := d.runs.MarkWaiting(ctx, tx, orgID, run.ID); err != nil {
			return DispatchOutcome{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return DispatchOutcome{}, fmt.Errorf("commit automation dispatch wait: %w", err)
		}
		return DispatchOutcome{Kind: DispatchRetry, RetryAfter: resolved.retryAfter, MaxWait: automationHeadAmbiguityWindow + automationHeadLookupRetryMax, Note: resolved.note}, nil
	case resolved.terminal != "":
		return d.terminalizeWith(ctx, tx, orgID, run.ID, resolved.terminal, resolved.supersededBy, resolved.note)
	}
	github.HeadSHA = resolved.headSHA
	if resolved.baseBranch != "" {
		github.BaseBranch = resolved.baseBranch
	}

	// At most one turn executes per target at a time.
	executing, err := d.runs.ExecutingRunForTarget(ctx, tx, orgID, target.ID)
	if err != nil {
		return DispatchOutcome{}, err
	}
	if executing != nil {
		return d.wait(ctx, tx, orgID, run.ID, fmt.Sprintf("run %s is executing", executing))
	}

	// Continuation decision.
	generation, err := d.targets.GetActiveGeneration(ctx, tx, orgID, target.ID)
	hasGeneration := err == nil
	if err != nil && !errors.Is(err, db.ErrAutomationTargetGenerationNotFound) {
		return DispatchOutcome{}, err
	}
	var session models.Session
	if hasGeneration {
		session, err = d.sessions.GetByIDInTx(ctx, tx, orgID, generation.SessionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return DispatchOutcome{}, fmt.Errorf("load generation session: %w", err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			session = models.Session{}
		}
	}
	decision := d.decide(automation, in.SessionTemplate, expectedUser, github, generation, hasGeneration, session, resolved.isPush)
	switch decision.kind {
	case decisionWait:
		return d.wait(ctx, tx, orgID, run.ID, decision.note)
	case decisionRetry:
		if err := tx.Commit(ctx); err != nil {
			return DispatchOutcome{}, fmt.Errorf("commit automation dispatch retry: %w", err)
		}
		return DispatchOutcome{Kind: DispatchRetry, RetryAfter: decision.retryAfter, MaxWait: decision.maxWait, Note: decision.note}, nil
	case decisionSkip:
		return d.terminalize(ctx, tx, orgID, run.ID, decision.outcome, decision.note)
	}

	// A new generation on a target whose lifecycle is unknown or stale is
	// revalidated against GitHub first.
	if decision.mode == models.AutomationRunContinuationFresh && d.lifecycleStale(target) {
		if info, ok := lookup(); ok && info.State != "" && info.State != "open" {
			if err := d.targets.SetLifecycle(ctx, tx, orgID, target.ID, models.AutomationTargetLifecycleClosed); err != nil {
				return DispatchOutcome{}, err
			}
			return d.terminalize(ctx, tx, orgID, run.ID, models.AutomationRunOutcomePRClosed, "pull request is "+info.State)
		}
	}

	// Retire an incompatible generation before creating the next one.
	if hasGeneration && decision.retireReason != nil {
		if _, err := d.targets.RetireGeneration(ctx, tx, orgID, generation.ID, *decision.retireReason); err != nil {
			return DispatchOutcome{}, fmt.Errorf("retire incompatible generation: %w", err)
		}
		hasGeneration = false
	}

	var (
		sessionID  uuid.UUID
		threadID   uuid.UUID
		turnNumber int
		jobType    string
		previous   *string
	)
	switch decision.mode {
	case models.AutomationRunContinuationFresh:
		template := *in.SessionTemplate
		template.OrgID = orgID
		template.AutomationRunID = &in.Run.ID
		template.TriggeredByUserID = expectedUser
		template.ModelOverride = automation.ModelOverride
		template.ReasoningEffort = automation.ReasoningEffort
		if err := d.sessions.CreateInTx(ctx, tx, &template); err != nil {
			return DispatchOutcome{}, fmt.Errorf("create per-target session: %w", err)
		}
		if template.PrimaryThreadID == nil {
			return DispatchOutcome{}, errors.New("per-target session was created without a primary thread")
		}
		if _, err := d.targets.InsertGeneration(ctx, tx, orgID, target.ID, template.ID); err != nil {
			return DispatchOutcome{}, fmt.Errorf("insert generation: %w", err)
		}
		generation, err = d.targets.GetActiveGeneration(ctx, tx, orgID, target.ID)
		if err != nil {
			return DispatchOutcome{}, err
		}
		sessionID = template.ID
		threadID = *template.PrimaryThreadID
		turnNumber = 1
		jobType = "run_agent"
	default:
		claimed, err := d.sessions.ClaimForAutomationTurn(ctx, tx, orgID, generation.SessionID, decision.mode == models.AutomationRunContinuationReconstructed)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// The session changed under us (a finishing turn's status
				// write). Nothing was written yet; wait for the wake.
				return d.wait(ctx, tx, orgID, run.ID, "session claim contended")
			}
			return DispatchOutcome{}, fmt.Errorf("claim session for automation turn: %w", err)
		}
		thread, err := d.threads.ClaimPrimaryForAutomationTurn(ctx, tx, orgID, claimed.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// The session claim already ran inside tx; roll it back
				// rather than commit a running session with no turn.
				return d.waitAfterRollback(ctx, tx, orgID, run.ID, "primary thread claim contended")
			}
			return DispatchOutcome{}, fmt.Errorf("claim primary thread for automation turn: %w", err)
		}
		if err := d.sessions.UpsertCapabilitySnapshotInTx(ctx, tx, orgID, claimed.ID, in.Run.CapabilitySnapshot); err != nil {
			return DispatchOutcome{}, err
		}
		sessionID = claimed.ID
		threadID = thread.ID
		turnNumber = claimed.CurrentTurn + 1
		jobType = "continue_session"
		previous = baselineHead(generation, claimed, thread, decision.mode)
	}

	prompt := AutomationTurnPrompt(AutomationTurnPromptInput{
		Goal:            in.Run.GoalSnapshot,
		TurnNumber:      turnNumber,
		Mode:            decision.mode,
		HeadSHA:         github.HeadSHA,
		PreviousHeadSHA: previous,
		BaseBranch:      github.BaseBranch,
	})
	// The visible user message is the transcript's copy of the turn's
	// prompt. A fresh turn gets one too, so a recovery that continues from
	// a bootstrap checkpoint finds a user message.
	message := &models.SessionMessage{
		SessionID:  sessionID,
		OrgID:      orgID,
		ThreadID:   &threadID,
		TurnNumber: turnNumber,
		Role:       models.MessageRoleUser,
		Content:    prompt,
		Source:     models.SessionMessageSourceAutomationTurn,
	}
	if err := db.NewSessionMessageStore(tx).CreateWithSource(ctx, message); err != nil {
		return DispatchOutcome{}, fmt.Errorf("insert automation turn message: %w", err)
	}
	if err := d.targets.SetAttemptedHead(ctx, tx, orgID, generation.ID, github.HeadSHA, github.BaseBranch); err != nil {
		return DispatchOutcome{}, err
	}
	payload := AutomationTurnJobPayload{
		SessionID:         sessionID.String(),
		OrgID:             orgID.String(),
		ThreadID:          threadID.String(),
		AutomationRunID:   in.Run.ID.String(),
		TargetGeneration:  generation.Generation,
		ContinuationMode:  string(decision.mode),
		HeadSHA:           github.HeadSHA,
		PullRequestNumber: github.PullRequestNumber,
	}
	if jobType == "continue_session" {
		payload.StructuredPrompt = prompt
	}

	// Dispatch identity: the job is run-scoped. On a dedupe conflict the
	// existing job must carry this run, otherwise the transaction fails.
	dedupeKey := AutomationTurnDedupeKey(in.Run.ID)
	jobID, err := d.jobs.EnqueueInTx(ctx, tx, orgID, AutomationTurnJobQueue, jobType, payload, 5, &dedupeKey)
	if err != nil {
		return DispatchOutcome{}, fmt.Errorf("enqueue automation turn: %w", err)
	}
	if jobID == uuid.Nil {
		existing, existingPayload, err := d.jobs.ActiveJobPayloadByDedupeKeyInTx(ctx, tx, orgID, AutomationTurnJobQueue, dedupeKey)
		if err != nil {
			return DispatchOutcome{}, fmt.Errorf("look up conflicting automation turn job: %w", err)
		}
		if !payloadCarriesRun(existingPayload, in.Run.ID) {
			return DispatchOutcome{}, fmt.Errorf("automation turn dedupe key %s is held by job %s for a different run", dedupeKey, existing.ID)
		}
		jobID = existing.ID
	}

	reserved, err := d.runs.ReserveForExecution(ctx, tx, orgID, in.Run.ID, db.AutomationRunReservation{
		TargetID:           target.ID,
		TargetGeneration:   generation.Generation,
		SessionID:          sessionID,
		ThreadID:           threadID,
		TurnNumber:         turnNumber,
		JobID:              jobID,
		ContinuationMode:   decision.mode,
		ContinuationReason: decision.reason,
		PreviousHeadSHA:    previous,
		HeadEpoch:          resolved.epoch,
		HeadResolution:     resolved.resolution,
		HeadLookupDegraded: resolved.degraded,
	})
	if err != nil {
		return DispatchOutcome{}, err
	}
	if !reserved {
		return DispatchOutcome{}, errors.New("automation run was not reservable inside the ownership transaction")
	}
	if err := tx.Commit(ctx); err != nil {
		return DispatchOutcome{}, fmt.Errorf("commit automation dispatch: %w", err)
	}
	if jobType == "continue_session" {
		d.threads.PublishRuntime(ctx, orgID, threadID)
	}
	d.jobs.Notify(ctx, jobID)
	return DispatchOutcome{
		Kind:               DispatchReserved,
		JobID:              jobID,
		SessionID:          sessionID,
		ThreadID:           threadID,
		ContinuationMode:   decision.mode,
		ContinuationReason: decision.reason,
	}, nil
}

func (d *TargetDispatcher) wait(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID, note string) (DispatchOutcome, error) {
	if _, err := d.runs.MarkWaiting(ctx, tx, orgID, runID); err != nil {
		return DispatchOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DispatchOutcome{}, fmt.Errorf("commit automation dispatch wait: %w", err)
	}
	return DispatchOutcome{Kind: DispatchWaiting, Note: note}, nil
}

// waitAfterRollback discards the ownership transaction's writes and records
// the run as waiting in its own statement.
func (d *TargetDispatcher) waitAfterRollback(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID, note string) (DispatchOutcome, error) {
	if err := tx.Rollback(ctx); err != nil {
		return DispatchOutcome{}, fmt.Errorf("rollback automation dispatch: %w", err)
	}
	if _, err := d.runs.MarkWaiting(ctx, nil, orgID, runID); err != nil {
		return DispatchOutcome{}, err
	}
	return DispatchOutcome{Kind: DispatchWaiting, Note: note}, nil
}

func (d *TargetDispatcher) terminalize(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID, outcome models.AutomationRunOutcomeReason, note string) (DispatchOutcome, error) {
	return d.terminalizeWith(ctx, tx, orgID, runID, outcome, nil, note)
}

func (d *TargetDispatcher) terminalizeWith(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID, outcome models.AutomationRunOutcomeReason, supersededBy *uuid.UUID, note string) (DispatchOutcome, error) {
	if _, err := d.runs.TerminalizeUnstarted(ctx, tx, orgID, runID, outcome, supersededBy, note); err != nil {
		return DispatchOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DispatchOutcome{}, fmt.Errorf("commit automation dispatch terminalization: %w", err)
	}
	return DispatchOutcome{Kind: DispatchTerminalized, OutcomeReason: outcome, Note: note}, nil
}

// lifecycleAllowsRun applies the stored lifecycle gate: open targets run
// everything; a merged target runs only its merged event.
func lifecycleAllowsRun(state models.AutomationTargetLifecycleState, event models.AutomationGitHubEvent) bool {
	switch state {
	case models.AutomationTargetLifecycleOpen:
		return true
	case models.AutomationTargetLifecycleMerged:
		return event == models.AutomationGitHubEventPullRequestMerged
	default:
		return false
	}
}

func (d *TargetDispatcher) lifecycleStale(target models.AutomationTarget) bool {
	return target.LifecycleUpdatedAt == nil || d.now().Sub(*target.LifecycleUpdatedAt) > automationLifecycleStaleAfter
}

// headLookup returns a memoized dispatch-time lookup of the pull request's
// current head; ok is false when no resolver is configured or the lookup
// failed, and the failure is logged once.
func (d *TargetDispatcher) headLookup(ctx context.Context, orgID, repositoryID uuid.UUID, number int) func() (PullRequestHeadInfo, bool) {
	var (
		done bool
		info PullRequestHeadInfo
		ok   bool
	)
	return func() (PullRequestHeadInfo, bool) {
		if done {
			return info, ok
		}
		done = true
		if d.heads == nil || number <= 0 {
			return info, false
		}
		resolved, err := d.heads.ResolvePullRequestHead(ctx, orgID, repositoryID, number)
		if err != nil {
			d.logger.Warn().Err(err).Str("org_id", orgID.String()).Int("pull_request_number", number).Msg("automation dispatch head lookup failed")
			return info, false
		}
		info, ok = resolved, resolved.SHA != ""
		return info, ok
	}
}

// headResolution is the result of dispatch-time head authority.
type headResolution struct {
	headSHA      string
	baseBranch   string
	epoch        *int
	resolution   *models.AutomationRunHeadResolution
	degraded     bool
	isPush       bool
	terminal     models.AutomationRunOutcomeReason
	supersededBy *uuid.UUID
	retryAfter   time.Duration
	note         string
}

// resolveHead applies the dispatch-time head rules. Push runs look up the
// current head: a missed newer head is adopted with a new epoch and
// reviewed; ambiguous candidates resolve to the one at the current head,
// and when none holds it the dispatching candidate becomes the survivor
// that reviews it; a failed lookup degrades to the delivered head unless
// ambiguity is pending, in which case the run waits with backoff.
// Unresolved candidates (converted by the ambiguity deadline) keep their
// delivered head and null epoch. Non-push runs keep their delivered head,
// and one delivered without a head is enriched by the lookup.
func (d *TargetDispatcher) resolveHead(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, target *models.AutomationTarget, run models.AutomationRun, github automationRunGitHubContext, lookup func() (PullRequestHeadInfo, bool)) (headResolution, error) {
	authoritative := models.AutomationRunHeadAuthoritative
	isPush := run.GitHubAction != nil && *run.GitHubAction == githubActionSynchronize
	out := headResolution{headSHA: github.HeadSHA, epoch: run.HeadEpoch, resolution: run.HeadResolution, isPush: isPush}
	if run.HeadResolution != nil && *run.HeadResolution == models.AutomationRunHeadUnresolved {
		// The deadline promised each unresolved candidate its own review at
		// its delivered head.
		return out, nil
	}
	ambiguousRun := run.HeadResolution != nil && *run.HeadResolution == models.AutomationRunHeadAmbiguous
	needsLookup := isPush || github.HeadSHA == ""
	if !needsLookup {
		return out, nil
	}

	current, ok := lookup()
	if !ok {
		if ambiguousRun || target.HeadResolutionPending {
			out.retryAfter = headLookupBackoff(d.now(), run)
			out.note = "pull request head is ambiguous and the lookup is unavailable"
			return out, nil
		}
		if isPush {
			out.degraded = true
		}
		return out, nil
	}
	if current.State != "" && current.State != "open" {
		out.terminal = models.AutomationRunOutcomePRClosed
		out.note = "pull request is " + current.State
		return out, nil
	}
	out.baseBranch = current.BaseBranch

	// The current head decides: a missed webhook is adopted with a new
	// epoch; the same head refreshes the watermark; ambiguous candidates
	// resolve to the one at the current head.
	epoch := target.HeadEpoch
	switch {
	case target.ObservedHeadSHA == nil || *target.ObservedHeadSHA != current.SHA:
		adopted, err := d.targets.AdoptHead(ctx, tx, orgID, target.ID, current.SHA, current.UpdatedAt)
		if err != nil {
			return out, err
		}
		epoch = adopted
		target.ObservedHeadSHA = &current.SHA
		target.HeadEpoch = adopted
		// A missed newer head supersedes older waiting pushes exactly as a
		// newer delivery would have at arrival.
		if _, err := d.runs.SupersedeWaitingPush(ctx, tx, orgID, target.ID, run.ID, adopted); err != nil {
			return out, err
		}
	case current.UpdatedAt != nil && (target.ObservedHeadUpdatedAt == nil || current.UpdatedAt.After(*target.ObservedHeadUpdatedAt)):
		if err := d.targets.TouchObservedHead(ctx, tx, orgID, target.ID, current.SHA, *current.UpdatedAt); err != nil {
			return out, err
		}
	}
	if !isPush {
		// A non-push run delivered without a head reviews the current head
		// and joins its epoch.
		out.headSHA = current.SHA
		out.epoch = &epoch
		out.resolution = &authoritative
		return out, nil
	}
	if target.HeadResolutionPending {
		candidates, err := d.runs.ListAmbiguousPushCandidates(ctx, tx, orgID, target.ID)
		if err != nil {
			return out, err
		}
		survivor := run.ID
		for _, c := range candidates {
			if c.HeadSHA == current.SHA {
				survivor = c.RunID
				break
			}
		}
		for _, c := range candidates {
			switch {
			case c.RunID == survivor:
				if c.RunID != run.ID {
					if err := d.runs.StampHeadResolution(ctx, tx, orgID, c.RunID, &epoch, authoritative); err != nil {
						return out, err
					}
				}
			default:
				if _, err := d.runs.TerminalizeUnstarted(ctx, tx, orgID, c.RunID, models.AutomationRunOutcomeSuperseded, &survivor, "superseded by the pull request's current head"); err != nil {
					return out, err
				}
			}
		}
		if err := d.targets.ClearHeadResolutionPending(ctx, tx, orgID, target.ID); err != nil {
			return out, err
		}
		target.HeadResolutionPending = false
		if survivor != run.ID {
			out.terminal = models.AutomationRunOutcomeSuperseded
			out.supersededBy = &survivor
			out.note = "superseded by the pull request's current head"
			return out, nil
		}
	}
	out.headSHA = current.SHA
	out.epoch = &epoch
	out.resolution = &authoritative
	return out, nil
}

// headLookupBackoff doubles from 30 s to 10 min over the time the run has
// been waiting, without spending the job's attempt budget.
func headLookupBackoff(now time.Time, run models.AutomationRun) time.Duration {
	since := run.TriggeredAt
	if run.WaitStartedAt != nil {
		since = *run.WaitStartedAt
	}
	elapsed := now.Sub(since)
	delay := automationHeadLookupRetryMin
	for delay < automationHeadLookupRetryMax && delay*2 <= elapsed {
		delay *= 2
	}
	if delay > automationHeadLookupRetryMax {
		delay = automationHeadLookupRetryMax
	}
	return delay
}

type decisionKind int

const (
	decisionProceed decisionKind = iota
	decisionWait
	decisionRetry
	decisionSkip
)

type continuationDecision struct {
	kind         decisionKind
	mode         models.AutomationRunContinuationMode
	reason       *models.AutomationRunContinuationReason
	retireReason *models.AutomationTargetRetiredReason
	outcome      models.AutomationRunOutcomeReason
	retryAfter   time.Duration
	maxWait      time.Duration
	note         string
}

func freshDecision(reason models.AutomationRunContinuationReason, retire *models.AutomationTargetRetiredReason) continuationDecision {
	return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationFresh, reason: &reason, retireReason: retire}
}

func retireDecision(reason models.AutomationTargetRetiredReason) continuationDecision {
	continuation := models.ContinuationReasonForRetirement(reason)
	return freshDecision(continuation, &reason)
}

func reconstructDecision(reason models.AutomationRunContinuationReason) continuationDecision {
	return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationReconstructed, reason: &reason}
}

// decide evaluates compatibility and readiness (design doc 125,
// "Continuation Decision") for a run against the target's active
// generation. automation is the current row; expectedUser is the executing
// identity it resolves to now.
func (d *TargetDispatcher) decide(automation models.Automation, template *models.Session, expectedUser *uuid.UUID, github automationRunGitHubContext, generation models.AutomationTargetSession, hasGeneration bool, session models.Session, isPush bool) continuationDecision {
	if !hasGeneration {
		return freshDecision(models.AutomationRunContinuationReasonNoGeneration, nil)
	}
	// Duplicates: a push at the already-reviewed head has nothing to do.
	if isPush && generation.LastReviewedHeadSHA != nil && github.HeadSHA != "" && github.HeadSHA == *generation.LastReviewedHeadSHA {
		return continuationDecision{kind: decisionSkip, outcome: models.AutomationRunOutcomeDuplicateHead, note: "head already reviewed by this conversation"}
	}
	if session.ID == uuid.Nil || session.ArchivedAt != nil || session.DeletedAt != nil {
		return retireDecision(models.AutomationTargetRetiredSessionUnavailable)
	}
	switch {
	case session.Status == models.SessionStatusRunning:
		return continuationDecision{kind: decisionWait, note: "generation session is running"}
	case session.Status == models.SessionStatusIdle, session.Status.IsResumable():
	default:
		return retireDecision(models.AutomationTargetRetiredNotResumable)
	}
	if !agentConfigMatches(automation, template, session) {
		return retireDecision(models.AutomationTargetRetiredAgentConfigChanged)
	}
	if !uuidPtrEqual(expectedUser, session.TriggeredByUserID) {
		return retireDecision(models.AutomationTargetRetiredIdentityChanged)
	}
	if generation.LastBaseRef != nil && github.BaseBranch != "" && *generation.LastBaseRef != github.BaseBranch {
		return retireDecision(models.AutomationTargetRetiredBaseRetargeted)
	}
	if generation.TurnCount >= AutomationTurnLimit {
		return retireDecision(models.AutomationTargetRetiredTurnLimit)
	}
	if d.checkpointSizeLimit > 0 && session.CheckpointSizeBytes > d.checkpointSizeLimit {
		return retireDecision(models.AutomationTargetRetiredSnapshotTooLarge)
	}

	// Readiness of the stored session, from its runtime fields rather than
	// sandbox_state alone.
	if session.PendingSnapshotKey != nil && *session.PendingSnapshotKey != "" {
		pendingSince := d.now()
		if session.PendingSnapshotSetAt != nil {
			pendingSince = *session.PendingSnapshotSetAt
		}
		if d.now().Sub(pendingSince) < automationPendingSnapshotGrace {
			return continuationDecision{kind: decisionRetry, retryAfter: automationPendingSnapshotRetry, maxWait: automationPendingSnapshotGrace + automationPendingSnapshotRetry, note: "snapshot upload in flight"}
		}
		if session.SnapshotKey == nil || *session.SnapshotKey == "" {
			// Never race a live publisher: the stranded-pending reaper
			// clears the key, after which the next attempt rebuilds.
			return continuationDecision{kind: decisionRetry, retryAfter: automationPendingSnapshotRetry, maxWait: 2 * automationPendingSnapshotGrace, note: "snapshot upload stranded; waiting for the reaper"}
		}
	}
	live := session.ContainerID != nil && *session.ContainerID != "" && session.WorkerNodeID != nil && *session.WorkerNodeID != "" &&
		session.SandboxState == models.SandboxStateRunning
	if live {
		return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationContinued}
	}
	if d.checkpointUsable(session) {
		return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationContinued}
	}
	if session.SandboxState == models.SandboxStateDestroyed {
		return reconstructDecision(models.AutomationRunContinuationReasonSandboxDestroyed)
	}
	return reconstructDecision(models.AutomationRunContinuationReasonSnapshotMissing)
}

// checkpointUsable is the readiness table's checkpoint row: a published
// key from a turn_complete, graceful_stop, or bootstrap checkpoint within
// the age bound.
func (d *TargetDispatcher) checkpointUsable(session models.Session) bool {
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		return false
	}
	switch session.CheckpointKind {
	case models.CheckpointKindTurnComplete, models.CheckpointKindGracefulStop, models.CheckpointKindBootstrap:
	default:
		return false
	}
	if d.maxSnapshotAge > 0 {
		if session.CheckpointedAt == nil || d.now().Sub(*session.CheckpointedAt) > d.maxSnapshotAge {
			return false
		}
	}
	return true
}

// automationExecutingUser resolves the identity a per-target turn runs as
// from the automation's current identity scope.
func automationExecutingUser(automation models.Automation) (*uuid.UUID, error) {
	switch automation.IdentityScope.OrDefault() {
	case models.AutomationIdentityScopeOrg:
		return nil, nil
	case models.AutomationIdentityScopePersonal:
		if automation.CreatedBy == nil {
			return nil, errors.New("personal automation is missing created_by; cannot resolve execution identity")
		}
		id := *automation.CreatedBy
		return &id, nil
	default:
		return nil, fmt.Errorf("invalid identity_scope %q", automation.IdentityScope)
	}
}

// agentConfigMatches compares the automation's current agent type, model
// override, and reasoning effort against the generation's session.
func agentConfigMatches(automation models.Automation, template *models.Session, session models.Session) bool {
	if template != nil && template.AgentType != "" && template.AgentType != session.AgentType {
		return false
	}
	if !stringPtrEqual(automation.ModelOverride, session.ModelOverride) {
		return false
	}
	var want, have *string
	if automation.ReasoningEffort != nil {
		v := string(*automation.ReasoningEffort)
		want = &v
	}
	if session.ReasoningEffort != nil {
		v := string(*session.ReasoningEffort)
		have = &v
	}
	return stringPtrEqual(want, have)
}

// baselineHead is the delta baseline for a continued or reconstructed turn:
// the checkpoint's head when the restored checkpoint is the one the
// generation's provenance describes and native context can resume from it,
// otherwise the last completed review.
func baselineHead(generation models.AutomationTargetSession, session models.Session, thread models.SessionThread, mode models.AutomationRunContinuationMode) *string {
	if mode != models.AutomationRunContinuationContinued {
		return generation.LastReviewedHeadSHA
	}
	coherent := generation.CheckpointSnapshotKey != nil && session.SnapshotKey != nil && *generation.CheckpointSnapshotKey == *session.SnapshotKey
	nativeResume := (thread.AgentSessionID != nil && *thread.AgentSessionID != "") || (session.AgentSessionID != nil && *session.AgentSessionID != "")
	if coherent && nativeResume && generation.CheckpointHeadSHA != nil {
		return generation.CheckpointHeadSHA
	}
	return generation.LastReviewedHeadSHA
}

func runEvent(run models.AutomationRun) models.AutomationGitHubEvent {
	var snapshot struct {
		Event models.AutomationGitHubEvent `json:"github_event"`
	}
	if len(run.ConfigSnapshot) == 0 {
		return ""
	}
	if err := json.Unmarshal(run.ConfigSnapshot, &snapshot); err != nil {
		return ""
	}
	return snapshot.Event
}

func targetRepository(in DispatchInput) *uuid.UUID {
	if in.SessionTemplate != nil && in.SessionTemplate.RepositoryID != nil {
		return in.SessionTemplate.RepositoryID
	}
	return in.Automation.RepositoryID
}

func targetKeyForRun(run models.AutomationRun, github automationRunGitHubContext) string {
	if github.PullRequestNumber > 0 {
		return fmt.Sprint(github.PullRequestNumber)
	}
	return run.ID.String()
}

// payloadCarriesRun reports whether a job payload names runID; every
// per-target turn payload carries automation_run_id.
func payloadCarriesRun(payload json.RawMessage, runID uuid.UUID) bool {
	var decoded struct {
		AutomationRunID string `json:"automation_run_id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return false
	}
	return decoded.AutomationRunID == runID.String()
}

func derefUUID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func uuidPtrEqual(a, b *uuid.UUID) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

func stringPtrEqual(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return strings.TrimSpace(*a) == strings.TrimSpace(*b)
	}
}

// AutomationTurnPromptInput feeds the interim per-target turn prompt. The
// full template with the workspace delta and untrusted-data blocks lands
// with the orchestrator turn path; until then the prompt states the turn's
// identity so the agent knows it is continuing a review.
type AutomationTurnPromptInput struct {
	Goal            string
	TurnNumber      int
	Mode            models.AutomationRunContinuationMode
	HeadSHA         string
	PreviousHeadSHA *string
	BaseBranch      string
}

// AutomationTurnPrompt renders the visible user message for a per-target
// turn.
func AutomationTurnPrompt(in AutomationTurnPromptInput) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(in.Goal))
	b.WriteString("\n\nContinuation context:\n")
	fmt.Fprintf(&b, "- Turn: %d\n", in.TurnNumber)
	fmt.Fprintf(&b, "- Continuation: %s\n", in.Mode)
	baseline := "none"
	if in.PreviousHeadSHA != nil && *in.PreviousHeadSHA != "" {
		baseline = *in.PreviousHeadSHA
	}
	fmt.Fprintf(&b, "- Baseline head: %s\n", baseline)
	fmt.Fprintf(&b, "- Head: %s\n", in.HeadSHA)
	if in.BaseBranch != "" {
		fmt.Fprintf(&b, "- Base branch: %s\n", in.BaseBranch)
	}
	switch in.Mode {
	case models.AutomationRunContinuationFresh:
		b.WriteString("\nThis is the first turn of this pull request's review conversation. Review the full pull request against the goal.")
	case models.AutomationRunContinuationReconstructed:
		b.WriteString("\nYour earlier context for this pull request is unavailable. Review the full pull request against the goal.")
	default:
		b.WriteString("\nReview the changes since the baseline head against the goal. Do not repeat findings for unchanged code unless the change invalidates them, and state which earlier findings the new push resolved. If the baseline is none, review the full pull request.")
	}
	return b.String()
}
