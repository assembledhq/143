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
	// automationHeadLookupRetry is the initial backoff when an ambiguous
	// push cannot be resolved because the lookup failed.
	automationHeadLookupRetry = 30 * time.Second

	// AutomationTurnJobQueue is the queue per-target turns run on.
	AutomationTurnJobQueue = "agent"
)

// AutomationTurnDedupeKey scopes the turn's job to the run, not the thread:
// the previous turn's job may still be running while its completion
// executes, so a thread-scoped key would drop the follow-up enqueue.
func AutomationTurnDedupeKey(runID uuid.UUID) string {
	return "automation_turn:" + runID.String()
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
	// DispatchWaiting means the target is busy and the run waits for a wake.
	DispatchWaiting DispatchKind = "waiting"
	// DispatchTerminalized means the run ended without executing.
	DispatchTerminalized DispatchKind = "terminalized"
	// DispatchRetry means the decision could not be made yet (a pending
	// snapshot, an unresolved head); the caller retries after RetryAfter.
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
	Note               string
}

// DispatchInput is what the worker hands the dispatcher: the run and its
// automation as loaded, plus the session template it would create for a
// fresh per-run session (agent type, identity, repository, branch, brief,
// capability snapshot). The dispatcher inserts that template for a fresh
// generation, so both paths create sessions the same way.
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
	txStarter db.TxStarter
	targets   *db.AutomationTargetStore
	runs      *db.AutomationRunStore
	sessions  *db.SessionStore
	threads   *db.SessionThreadStore
	jobs      *db.JobStore
	heads     PullRequestHeadResolver
	logger    zerolog.Logger
	now       func() time.Time

	maxSnapshotAge      time.Duration
	checkpointSizeLimit int64
}

func NewTargetDispatcher(txStarter db.TxStarter, targets *db.AutomationTargetStore, runs *db.AutomationRunStore, sessions *db.SessionStore, threads *db.SessionThreadStore, jobs *db.JobStore, logger zerolog.Logger) *TargetDispatcher {
	return &TargetDispatcher{
		txStarter:           txStarter,
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
// push runs review their delivered head and ambiguous candidates wait for
// the ambiguity deadline.
func (d *TargetDispatcher) SetHeadResolver(resolver PullRequestHeadResolver) {
	d.heads = resolver
}

// SetMaxSnapshotAge bounds how old a checkpoint may be to count as ready;
// zero disables the bound.
func (d *TargetDispatcher) SetMaxSnapshotAge(age time.Duration) {
	d.maxSnapshotAge = age
}

// automationRunGitHubContext is the pull request context captured in a
// run's config_snapshot at trigger time.
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
// must have been attached to a target at arrival.
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

	tx, err := d.txStarter.Begin(ctx)
	if err != nil {
		return DispatchOutcome{}, fmt.Errorf("begin automation dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	target, err := d.targets.LockOrCreate(ctx, tx, orgID, in.Automation.ID, *targetRepository(in), models.AutomationTargetKindGitHubPullRequest, targetKeyForRun(in.Run, github))
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

	// Lifecycle revalidation at dispatch: a delivery that queued while the
	// PR was open but dispatches after it closed is skipped, unless it is
	// the merged run on a merged target.
	if target.LifecycleState != models.AutomationTargetLifecycleOpen &&
		!(target.LifecycleState == models.AutomationTargetLifecycleMerged && runEvent(run) == models.AutomationGitHubEventPullRequestMerged) {
		return d.terminalize(ctx, tx, orgID, run.ID, models.AutomationRunOutcomePRClosed, "pull request is no longer open")
	}

	// Head authority at dispatch for push runs.
	headSHA := github.HeadSHA
	headEpoch := run.HeadEpoch
	headResolution := run.HeadResolution
	lookupDegraded := false
	isPush := run.GitHubAction != nil && *run.GitHubAction == githubActionSynchronize
	if isPush {
		resolved, err := d.resolvePushHead(ctx, tx, orgID, target, run, github)
		if err != nil {
			return DispatchOutcome{}, err
		}
		if resolved.retryAfter > 0 {
			if err := tx.Commit(ctx); err != nil {
				return DispatchOutcome{}, fmt.Errorf("commit automation dispatch wait: %w", err)
			}
			return DispatchOutcome{Kind: DispatchRetry, RetryAfter: resolved.retryAfter, Note: resolved.note}, nil
		}
		if resolved.terminal != "" {
			return d.terminalize(ctx, tx, orgID, run.ID, resolved.terminal, resolved.note)
		}
		headSHA = resolved.headSHA
		headEpoch = resolved.epoch
		headResolution = resolved.resolution
		lookupDegraded = resolved.degraded
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
	decision := d.decide(in, github, target, generation, hasGeneration, session, isPush)
	switch decision.kind {
	case decisionWait:
		return d.wait(ctx, tx, orgID, run.ID, decision.note)
	case decisionRetry:
		if err := tx.Commit(ctx); err != nil {
			return DispatchOutcome{}, fmt.Errorf("commit automation dispatch retry: %w", err)
		}
		return DispatchOutcome{Kind: DispatchRetry, RetryAfter: decision.retryAfter, Note: decision.note}, nil
	case decisionSkip:
		return d.terminalize(ctx, tx, orgID, run.ID, decision.outcome, decision.note)
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
		payload    map[string]string
	)
	previousHead := (*string)(nil)
	if hasGeneration && decision.mode != models.AutomationRunContinuationFresh {
		previousHead = baselineHead(generation, decision.mode)
	}
	switch decision.mode {
	case models.AutomationRunContinuationFresh:
		template := *in.SessionTemplate
		template.OrgID = orgID
		template.AutomationRunID = &in.Run.ID
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
		payload = db.RunAgentPayload(&template)
	default:
		claimed, err := d.sessions.ClaimForAutomationTurn(ctx, tx, orgID, generation.SessionID, decision.mode == models.AutomationRunContinuationReconstructed)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// The session changed under us (a human turn cannot, but a
				// status write from a finishing turn can). Wait for the wake.
				return d.wait(ctx, tx, orgID, run.ID, "session claim contended")
			}
			return DispatchOutcome{}, fmt.Errorf("claim session for automation turn: %w", err)
		}
		thread, err := d.threads.ClaimPrimaryForAutomationTurn(ctx, tx, orgID, claimed.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return d.wait(ctx, tx, orgID, run.ID, "primary thread claim contended")
			}
			return DispatchOutcome{}, fmt.Errorf("claim primary thread for automation turn: %w", err)
		}
		sessionID = claimed.ID
		threadID = thread.ID
		turnNumber = claimed.CurrentTurn + 1
		prompt := AutomationTurnPrompt(AutomationTurnPromptInput{
			Goal:            in.Run.GoalSnapshot,
			TurnNumber:      turnNumber,
			Mode:            decision.mode,
			HeadSHA:         headSHA,
			PreviousHeadSHA: previousHead,
			BaseBranch:      github.BaseBranch,
		})
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
		jobType = "continue_session"
		payload = map[string]string{
			"session_id":          sessionID.String(),
			"org_id":              orgID.String(),
			"thread_id":           threadID.String(),
			"structured_prompt":   prompt,
			"head_sha":            headSHA,
			"pull_request_number": fmt.Sprint(github.PullRequestNumber),
			"automation_run_id":   in.Run.ID.String(),
			"target_generation":   fmt.Sprint(generation.Generation),
			"continuation_mode":   string(decision.mode),
		}
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
		if !payloadCarriesRun(existingPayload, in.Run.ID, sessionID) {
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
		PreviousHeadSHA:    previousHead,
		HeadEpoch:          headEpoch,
		HeadResolution:     headResolution,
		HeadLookupDegraded: lookupDegraded,
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

func (d *TargetDispatcher) terminalize(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID, outcome models.AutomationRunOutcomeReason, note string) (DispatchOutcome, error) {
	if _, err := d.runs.TerminalizeUnstarted(ctx, tx, orgID, runID, outcome, nil, note); err != nil {
		return DispatchOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DispatchOutcome{}, fmt.Errorf("commit automation dispatch terminalization: %w", err)
	}
	return DispatchOutcome{Kind: DispatchTerminalized, OutcomeReason: outcome, Note: note}, nil
}

// pushHeadResolution is the result of dispatch-time head authority.
type pushHeadResolution struct {
	headSHA    string
	epoch      *int
	resolution *models.AutomationRunHeadResolution
	degraded   bool
	terminal   models.AutomationRunOutcomeReason
	retryAfter time.Duration
	note       string
}

// resolvePushHead applies the dispatch-time rules for push runs: look up
// the current head; adopt a missed newer head; resolve ambiguous candidates
// by the current head; degrade to the delivered head when the lookup fails
// without ambiguity; and hold ambiguous runs when it fails with ambiguity.
func (d *TargetDispatcher) resolvePushHead(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, target models.AutomationTarget, run models.AutomationRun, github automationRunGitHubContext) (pushHeadResolution, error) {
	authoritative := models.AutomationRunHeadAuthoritative
	ambiguousRun := run.HeadResolution != nil && *run.HeadResolution == models.AutomationRunHeadAmbiguous
	out := pushHeadResolution{headSHA: github.HeadSHA, epoch: run.HeadEpoch, resolution: run.HeadResolution}

	var current *PullRequestHeadInfo
	if d.heads != nil && github.PullRequestNumber > 0 {
		info, err := d.heads.ResolvePullRequestHead(ctx, orgID, target.RepositoryID, github.PullRequestNumber)
		if err != nil {
			d.logger.Warn().Err(err).Str("run_id", run.ID.String()).Msg("automation dispatch head lookup failed")
		} else {
			current = &info
		}
	}
	if current == nil {
		if ambiguousRun || target.HeadResolutionPending {
			out.retryAfter = automationHeadLookupRetry
			out.note = "pull request head is ambiguous and the lookup is unavailable"
			return out, nil
		}
		out.degraded = true
		return out, nil
	}
	if current.State != "" && current.State != "open" {
		out.terminal = models.AutomationRunOutcomePRClosed
		out.note = "pull request is " + current.State
		return out, nil
	}

	// The current head decides: a missed webhook is adopted with a new
	// epoch; ambiguous candidates resolve to the one at the current head.
	epoch := target.HeadEpoch
	if target.ObservedHeadSHA == nil || *target.ObservedHeadSHA != current.SHA {
		adopted, err := d.targets.AdoptHead(ctx, tx, orgID, target.ID, current.SHA, current.UpdatedAt)
		if err != nil {
			return out, err
		}
		epoch = adopted
	}
	if target.HeadResolutionPending {
		candidates, err := d.runs.ListAmbiguousPushCandidates(ctx, tx, orgID, target.ID)
		if err != nil {
			return out, err
		}
		for _, c := range candidates {
			if c.RunID == run.ID {
				continue
			}
			if c.HeadSHA == current.SHA {
				if err := d.runs.StampHeadResolution(ctx, tx, orgID, c.RunID, &epoch, authoritative); err != nil {
					return out, err
				}
				continue
			}
			if _, err := d.runs.TerminalizeUnstarted(ctx, tx, orgID, c.RunID, models.AutomationRunOutcomeSuperseded, &run.ID, "superseded by the pull request's current head"); err != nil {
				return out, err
			}
		}
		if err := d.targets.ClearHeadResolutionPending(ctx, tx, orgID, target.ID); err != nil {
			return out, err
		}
	}
	if ambiguousRun && github.HeadSHA != current.SHA {
		// Another candidate holds the current head; this one loses.
		out.terminal = models.AutomationRunOutcomeSuperseded
		out.note = "superseded by the pull request's current head"
		return out, nil
	}
	out.headSHA = current.SHA
	out.epoch = &epoch
	out.resolution = &authoritative
	return out, nil
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
	note         string
}

func freshDecision(reason models.AutomationRunContinuationReason, retire *models.AutomationTargetRetiredReason) continuationDecision {
	return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationFresh, reason: &reason, retireReason: retire}
}

func retireDecision(reason models.AutomationTargetRetiredReason) continuationDecision {
	continuation := models.ContinuationReasonForRetirement(reason)
	return freshDecision(continuation, &reason)
}

// decide evaluates compatibility and readiness (design doc 125,
// "Continuation Decision") for a run against the target's active generation.
func (d *TargetDispatcher) decide(in DispatchInput, github automationRunGitHubContext, target models.AutomationTarget, generation models.AutomationTargetSession, hasGeneration bool, session models.Session, isPush bool) continuationDecision {
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
	if !agentConfigMatches(in.Automation, in.SessionTemplate, session) {
		return retireDecision(models.AutomationTargetRetiredAgentConfigChanged)
	}
	if !identityMatches(in.SessionTemplate, session) {
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

	// Readiness of the stored session.
	if session.PendingSnapshotKey != nil && *session.PendingSnapshotKey != "" {
		pendingSince := d.now()
		if session.PendingSnapshotSetAt != nil {
			pendingSince = *session.PendingSnapshotSetAt
		}
		if d.now().Sub(pendingSince) < automationPendingSnapshotGrace {
			return continuationDecision{kind: decisionRetry, retryAfter: automationPendingSnapshotRetry, note: "snapshot upload in flight"}
		}
		if session.SnapshotKey == nil || *session.SnapshotKey == "" {
			// Never race a live publisher: the stranded-pending reaper
			// clears the key, after which the next attempt rebuilds.
			return continuationDecision{kind: decisionRetry, retryAfter: automationPendingSnapshotRetry, note: "snapshot upload stranded; waiting for the reaper"}
		}
	}
	live := session.ContainerID != nil && *session.ContainerID != "" && session.WorkerNodeID != nil && *session.WorkerNodeID != "" &&
		session.SandboxState == models.SandboxStateRunning
	if live {
		return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationContinued}
	}
	if session.SandboxState == models.SandboxStateDestroyed {
		reason := models.AutomationRunContinuationReasonSandboxDestroyed
		return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationReconstructed, reason: &reason}
	}
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		reason := models.AutomationRunContinuationReasonSnapshotMissing
		return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationReconstructed, reason: &reason}
	}
	switch session.CheckpointKind {
	case models.CheckpointKindTurnComplete, models.CheckpointKindGracefulStop, models.CheckpointKindBootstrap:
	default:
		reason := models.AutomationRunContinuationReasonSnapshotMissing
		return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationReconstructed, reason: &reason}
	}
	if d.maxSnapshotAge > 0 && session.CheckpointedAt != nil && d.now().Sub(*session.CheckpointedAt) > d.maxSnapshotAge {
		reason := models.AutomationRunContinuationReasonSnapshotMissing
		return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationReconstructed, reason: &reason}
	}
	return continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationContinued}
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

// identityMatches compares the executing user the automation would use now
// against the one the generation's session runs as.
func identityMatches(template *models.Session, session models.Session) bool {
	if template == nil {
		return true
	}
	if template.TriggeredByUserID == nil && session.TriggeredByUserID == nil {
		return true
	}
	if template.TriggeredByUserID == nil || session.TriggeredByUserID == nil {
		return false
	}
	return *template.TriggeredByUserID == *session.TriggeredByUserID
}

// baselineHead is the delta baseline for a continued or reconstructed turn:
// the checkpoint's head when native context is restored from a coherent
// checkpoint, otherwise the last completed review.
func baselineHead(generation models.AutomationTargetSession, mode models.AutomationRunContinuationMode) *string {
	if mode == models.AutomationRunContinuationContinued && generation.CheckpointHeadSHA != nil && generation.CheckpointSnapshotKey != nil {
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

func payloadCarriesRun(payload json.RawMessage, runID, sessionID uuid.UUID) bool {
	var decoded map[string]string
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return false
	}
	if decoded["automation_run_id"] == runID.String() {
		return true
	}
	// A fresh run's run_agent payload is session-scoped; the session was
	// created for exactly this run.
	return decoded["session_id"] == sessionID.String() && decoded["automation_run_id"] == ""
}

func derefUUID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
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

// AutomationTurnPrompt renders the visible user message for a continued or
// reconstructed turn.
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
	case models.AutomationRunContinuationReconstructed:
		b.WriteString("\nYour earlier context for this pull request is unavailable. Review the full pull request against the goal.")
	default:
		b.WriteString("\nReview the changes since the baseline head against the goal. Do not repeat findings for unchanged code unless the change invalidates them, and state which earlier findings the new push resolved. If the baseline is none, review the full pull request.")
	}
	return b.String()
}
