package automations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// Per-target continuity: completion, wake-up, and the scheduler sweeps
// (design doc 125, "Completion", "Target Wake-up", "Clocks and the
// stuck-run reaper"). The completer is the only path that records a
// per-target run's terminal status from its result marker; every
// transaction here that frees a target writes the wake outbox and enqueues
// the wake job in the same transaction.

const (
	// AutomationTargetWakeDedupePrefix prefixes the wake job's dedupe key;
	// the target id completes it.
	AutomationTargetWakeDedupePrefix = "automation_target_wake:"
	// automationRunDedupePrefix prefixes the automation_run job's dedupe key.
	automationRunDedupePrefix = "automation_run:"
	// automationTargetWakeQueue is the queue wake and run jobs share.
	automationTargetWakeQueue = "default"
	// automationTargetWakePriority matches the automation_run job's priority.
	automationTargetWakePriority = 5

	// AutomationWaitTimeout is how long a run may wait for its target before
	// the wait sweep fails it with wait_timeout.
	AutomationWaitTimeout = 2 * time.Hour
	// AutomationWakeStaleAfter is how old an unconsumed wake request may be
	// before reconciliation enqueues another wake.
	AutomationWakeStaleAfter = 2 * time.Minute
	// AutomationAttemptStaleAfter is how long an executing attempt may hold
	// its session without a live lease before the recovery sweep settles it.
	// It matches the stuck-run reaper's threshold, which no longer touches
	// per-target runs because terminalizing one without releasing its
	// session, thread, and ownership would strand all three.
	AutomationAttemptStaleAfter = 1 * time.Hour
)

// AutomationTargetWakePayload is the automation_target_wake job's payload.
type AutomationTargetWakePayload struct {
	OrgID    string `json:"org_id"`
	TargetID string `json:"target_id"`
}

// automationRunJobPayload is the automation_run job's payload, as every
// producer of that job writes it.
type automationRunJobPayload struct {
	OrgID           string `json:"org_id"`
	AutomationID    string `json:"automation_id"`
	AutomationRunID string `json:"automation_run_id"`
}

// CompletionResult reports what Complete applied.
type CompletionResult struct {
	Applied           bool
	Outcome           models.AutomationRunOutcomeReason
	TargetID          uuid.UUID
	GenerationRetired bool
	RetiredReason     models.AutomationTargetRetiredReason
}

// WakeOutcome reports what Wake did for a target.
type WakeOutcome struct {
	// Nudged is the waiting run whose automation_run job was made runnable
	// now, or nil when no run was waiting.
	Nudged *uuid.UUID
	// Enqueued is set when the run's job was gone and a fresh one was
	// enqueued rather than rescheduled.
	Enqueued bool
	// Requeue is set when a newer wake request arrived while this wake ran:
	// the marker was left in place and the caller should run the wake
	// again.
	Requeue bool
}

// SweepReport counts what one org's sweep did.
type SweepReport struct {
	RecoveredCompletions int
	RetriesExhausted     int
	WaitTimeouts         int64
	AmbiguityDeadlines   int
	WakesReconciled      int
	OwnershipReleases    int
}

// TurnCompleter completes per-target runs from their result markers, wakes
// targets, and runs the per-org sweeps.
type TurnCompleter struct {
	pool    db.TxStarter
	runs    *db.AutomationRunStore
	targets *db.AutomationTargetStore
	jobs    *db.JobStore
	logger  zerolog.Logger
	now     func() time.Time
}

// NewTurnCompleter wires the completer over one pool.
func NewTurnCompleter(pool db.TxStarter, runs *db.AutomationRunStore, targets *db.AutomationTargetStore, jobs *db.JobStore, logger zerolog.Logger) *TurnCompleter {
	return &TurnCompleter{pool: pool, runs: runs, targets: targets, jobs: jobs, logger: logger, now: time.Now}
}

// Complete records the run's terminal state from the result marker of its
// current attempt under the caller's job: a live lease identified by
// lockToken, or a terminal job when lockToken is nil. An awaiting_input
// outcome also retires the generation so a person can answer in the
// session. The completion, the retirement, the wake outbox, and the wake
// job commit together. Applied is false when no marker exists for the
// current attempt, the run is not executing under jobID, or the completion
// was already recorded.
func (c *TurnCompleter) Complete(ctx context.Context, orgID, runID, jobID, lockToken uuid.UUID) (CompletionResult, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return CompletionResult{}, fmt.Errorf("begin automation turn completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	done, err := c.runs.CompleteFromMarker(ctx, tx, orgID, runID, jobID, lockToken, nil)
	if err != nil {
		return CompletionResult{}, err
	}
	if !done.Applied {
		return CompletionResult{}, nil
	}
	result := CompletionResult{Applied: true, Outcome: done.Outcome, TargetID: done.TargetID}
	// An awaiting_input turn hands the session to a person; a turn that ran
	// on a closed or merged pull request was the last one its generation
	// will have, and the close could not retire it while this turn held the
	// target (design doc 125, "Lifecycle Transitions": retired after the
	// final turn).
	retireReason := models.AutomationTargetRetiredReason("")
	if done.RetireGeneration {
		retireReason = models.AutomationTargetRetiredAwaitingInput
	} else {
		lifecycleReason, err := c.targets.TerminalLifecycleRetirement(ctx, tx, orgID, done.TargetID)
		if err != nil {
			return CompletionResult{}, err
		}
		retireReason = lifecycleReason
	}
	if retireReason != "" {
		retired, err := c.retireGeneration(ctx, tx, orgID, done.GenerationID, retireReason)
		if err != nil {
			return CompletionResult{}, err
		}
		if retired {
			result.GenerationRetired = true
			result.RetiredReason = retireReason
		}
	}
	if err := c.enqueueWake(ctx, tx, orgID, done.TargetID); err != nil {
		return CompletionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CompletionResult{}, fmt.Errorf("commit automation turn completion: %w", err)
	}
	c.logger.Info().
		Str("org_id", orgID.String()).
		Str("run_id", runID.String()).
		Str("target_id", done.TargetID.String()).
		Str("outcome", string(done.Outcome)).
		Bool("generation_retired", result.GenerationRetired).
		Str("retired_reason", string(result.RetiredReason)).
		Msg("completed per-target automation turn")
	return result, nil
}

// retireGeneration retires a generation, treating an already-retired one as
// success without a retirement: a lifecycle transition may have retired it
// first, and this transaction's completion still applied the release.
func (c *TurnCompleter) retireGeneration(ctx context.Context, tx pgx.Tx, orgID, generationID uuid.UUID, reason models.AutomationTargetRetiredReason) (bool, error) {
	_, err := c.targets.RetireGeneration(ctx, tx, orgID, generationID, reason)
	switch {
	case errors.Is(err, db.ErrAutomationTargetGenerationNotActive):
		return false, nil
	case err != nil:
		return false, err
	default:
		return true, nil
	}
}

// FailRetriesExhausted records retries_exhausted on an abandoned executing
// run (no marker for the current attempt, no live lease, and either a
// terminal job or an attempt older than staleBefore), releases the session
// and thread, and wakes the target. Returns whether it applied.
func (c *TurnCompleter) FailRetriesExhausted(ctx context.Context, orgID, runID uuid.UUID, staleBefore time.Time) (bool, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin retries exhausted: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	targetID, done, err := c.runs.FailRetriesExhausted(ctx, tx, orgID, runID, staleBefore)
	if err != nil {
		return false, err
	}
	if !done {
		return false, nil
	}
	retiredReason, err := c.targets.RetireTerminalTarget(ctx, tx, orgID, targetID)
	if err != nil {
		return false, err
	}
	if err := c.enqueueWake(ctx, tx, orgID, targetID); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit retries exhausted: %w", err)
	}
	if retiredReason != "" {
		c.logger.Info().
			Str("org_id", orgID.String()).
			Str("target_id", targetID.String()).
			Str("retired_reason", string(retiredReason)).
			Msg("retired the generation of a target whose pull request is gone")
	}
	c.logger.Warn().
		Str("org_id", orgID.String()).
		Str("run_id", runID.String()).
		Str("target_id", targetID.String()).
		Msg("per-target automation run failed: retries exhausted")
	return true, nil
}

// RecoverAbandonedRun settles an executing run that no worker can still be
// running: a marker for the current attempt completes the run, otherwise
// the run fails with retries_exhausted. Either way the session, the thread,
// and any pending ownership release are freed. Returns the outcome
// recorded, or empty when the run needed nothing.
func (c *TurnCompleter) RecoverAbandonedRun(ctx context.Context, orgID, runID, jobID uuid.UUID, staleBefore time.Time) (models.AutomationRunOutcomeReason, error) {
	completed, err := c.Complete(ctx, orgID, runID, jobID, uuid.Nil)
	if err != nil {
		return "", err
	}
	if completed.Applied {
		return completed.Outcome, nil
	}
	failed, err := c.FailRetriesExhausted(ctx, orgID, runID, staleBefore)
	if err != nil {
		return "", err
	}
	if failed {
		return models.AutomationRunOutcomeRetriesExhausted, nil
	}
	return "", nil
}

// EnqueueWakeJob enqueues the target's wake job in the caller's
// transaction, for a caller that has already written the wake outbox.
func (c *TurnCompleter) EnqueueWakeJob(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID) error {
	return c.enqueueWake(ctx, tx, orgID, targetID)
}

// EnqueueWake writes the wake outbox and enqueues the target's wake job in
// the caller's transaction.
func (c *TurnCompleter) EnqueueWake(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID) error {
	if _, err := c.targets.RequestWake(ctx, tx, orgID, targetID); err != nil {
		return err
	}
	return c.enqueueWake(ctx, tx, orgID, targetID)
}

func (c *TurnCompleter) enqueueWake(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID) error {
	dedupe := AutomationTargetWakeDedupePrefix + targetID.String()
	payload := AutomationTargetWakePayload{OrgID: orgID.String(), TargetID: targetID.String()}
	if _, err := c.jobs.EnqueueInTx(ctx, tx, orgID, automationTargetWakeQueue, models.JobTypeAutomationTargetWake, payload, automationTargetWakePriority, &dedupe); err != nil {
		return fmt.Errorf("enqueue automation target wake: %w", err)
	}
	return nil
}

// Wake makes the target's next waiter re-enter dispatch: its automation_run
// job is made runnable now, or a fresh one is enqueued when the job is
// gone. With a pending head resolution and no dispatchable waiter, an
// ambiguous candidate's job is nudged so the dispatch-time lookup runs
// again. The wake marker is cleared only if it still carries the request
// this wake observed; a newer request reports Requeue.
func (c *TurnCompleter) Wake(ctx context.Context, orgID, targetID uuid.UUID) (WakeOutcome, error) {
	target, err := c.targets.GetByID(ctx, orgID, targetID)
	if err != nil {
		return WakeOutcome{}, err
	}
	observed := target.WakeRequestedAt
	var out WakeOutcome
	next, err := c.runs.NextWaiting(ctx, nil, orgID, targetID)
	switch {
	case err == nil:
		enqueued, err := c.nudge(ctx, orgID, next.AutomationID, next.ID)
		if err != nil {
			return WakeOutcome{}, err
		}
		id := next.ID
		out.Nudged = &id
		out.Enqueued = enqueued
	case errors.Is(err, db.ErrAutomationRunNotFound):
		if target.HeadResolutionPending {
			candidates, err := c.runs.ListAmbiguousPushCandidates(ctx, nil, orgID, targetID)
			if err != nil {
				return WakeOutcome{}, err
			}
			if len(candidates) > 0 {
				run, err := c.runs.GetByRunID(ctx, orgID, candidates[0].RunID)
				if err != nil {
					return WakeOutcome{}, err
				}
				enqueued, err := c.nudge(ctx, orgID, run.AutomationID, run.ID)
				if err != nil {
					return WakeOutcome{}, err
				}
				id := run.ID
				out.Nudged = &id
				out.Enqueued = enqueued
			}
		}
	default:
		return WakeOutcome{}, err
	}
	if observed != nil {
		cleared, err := c.targets.ClearWake(ctx, nil, orgID, targetID, *observed)
		if err != nil {
			return WakeOutcome{}, err
		}
		out.Requeue = !cleared
	}
	return out, nil
}

// nudge makes the run's automation_run job runnable now, enqueuing a fresh
// job when none is active. Returns whether a fresh job was enqueued.
func (c *TurnCompleter) nudge(ctx context.Context, orgID, automationID, runID uuid.UUID) (bool, error) {
	dedupe := automationRunDedupePrefix + runID.String()
	rescheduled, err := c.jobs.RescheduleActiveByDedupeKey(ctx, orgID, dedupe)
	if err != nil {
		return false, err
	}
	if rescheduled {
		return false, nil
	}
	active, err := c.jobs.HasActiveByDedupeKey(ctx, orgID, automationTargetWakeQueue, dedupe)
	if err != nil {
		return false, err
	}
	if active {
		// Runnable already, or running right now: it re-enters dispatch on
		// its own.
		return false, nil
	}
	payload := automationRunJobPayload{OrgID: orgID.String(), AutomationID: automationID.String(), AutomationRunID: runID.String()}
	if _, err := c.jobs.Enqueue(ctx, orgID, automationTargetWakeQueue, models.JobTypeAutomationRun, payload, automationTargetWakePriority, &dedupe); err != nil {
		return false, fmt.Errorf("enqueue automation run for wake: %w", err)
	}
	return true, nil
}

// ListOrgsWithWork returns the orgs the sweep should visit.
func (c *TurnCompleter) ListOrgsWithWork(ctx context.Context) ([]uuid.UUID, error) {
	return c.targets.ListOrgsWithPerTargetWork(ctx)
}

// Sweep runs the org's per-target sweeps: recover runs whose job died,
// fail timed-out waits, resolve expired ambiguity deadlines, and reconcile
// missing wakes. Each target-level step runs in its own transaction under
// the target lock; a failure in one step is logged and the others still
// run.
func (c *TurnCompleter) Sweep(ctx context.Context, orgID uuid.UUID) (SweepReport, error) {
	var report SweepReport
	var errs []error
	now := c.now()
	log := c.logger.With().Str("org_id", orgID.String()).Logger()

	staleBefore := now.Add(-AutomationAttemptStaleAfter)
	runIDs, err := c.runs.ListAbandonedExecutingRuns(ctx, orgID, staleBefore)
	if err != nil {
		errs = append(errs, err)
	}
	for _, runID := range runIDs {
		run, err := c.runs.GetByRunID(ctx, orgID, runID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if run.JobID == nil {
			continue
		}
		outcome, err := c.RecoverAbandonedRun(ctx, orgID, runID, *run.JobID, staleBefore)
		if err != nil {
			errs = append(errs, fmt.Errorf("recover run %s: %w", runID, err))
			continue
		}
		switch outcome {
		case "":
		case models.AutomationRunOutcomeRetriesExhausted:
			report.RetriesExhausted++
		default:
			report.RecoveredCompletions++
		}
	}

	targetIDs, err := c.targets.ListTargetsWithTimedOutWaits(ctx, orgID, now.Add(-AutomationWaitTimeout))
	if err != nil {
		errs = append(errs, err)
	}
	for _, targetID := range targetIDs {
		failed, err := c.failTimedOutWaits(ctx, orgID, targetID, now.Add(-AutomationWaitTimeout))
		if err != nil {
			errs = append(errs, fmt.Errorf("wait timeout for target %s: %w", targetID, err))
			continue
		}
		report.WaitTimeouts += failed
	}

	targetIDs, err = c.targets.ListTargetsWithExpiredAmbiguity(ctx, orgID, now)
	if err != nil {
		errs = append(errs, err)
	}
	for _, targetID := range targetIDs {
		resolved, converted, err := c.resolveAmbiguityDeadline(ctx, orgID, targetID, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("ambiguity deadline for target %s: %w", targetID, err))
			continue
		}
		if resolved {
			report.AmbiguityDeadlines++
			log.Debug().Str("target_id", targetID.String()).Int64("converted", converted).Msg("resolved an expired ambiguity deadline")
		}
	}

	stranded, err := c.targets.ListStrandedOwnershipReleases(ctx, orgID)
	if err != nil {
		errs = append(errs, err)
	}
	for _, row := range stranded {
		released, err := c.releaseStrandedOwnership(ctx, orgID, row)
		if err != nil {
			errs = append(errs, fmt.Errorf("release stranded ownership on target %s: %w", row.TargetID, err))
			continue
		}
		if released {
			report.OwnershipReleases++
		}
	}

	targetIDs, err = c.targets.ListTargetsNeedingWake(ctx, orgID, now.Add(-AutomationWakeStaleAfter), AutomationTargetWakeDedupePrefix)
	if err != nil {
		errs = append(errs, err)
	}
	for _, targetID := range targetIDs {
		if err := c.reconcileWake(ctx, orgID, targetID); err != nil {
			errs = append(errs, fmt.Errorf("reconcile wake for target %s: %w", targetID, err))
			continue
		}
		report.WakesReconciled++
	}

	if report != (SweepReport{}) {
		log.Info().
			Int("recovered_completions", report.RecoveredCompletions).
			Int("retries_exhausted", report.RetriesExhausted).
			Int64("wait_timeouts", report.WaitTimeouts).
			Int("ambiguity_deadlines", report.AmbiguityDeadlines).
			Int("ownership_releases", report.OwnershipReleases).
			Int("wakes_reconciled", report.WakesReconciled).
			Msg("per-target automation sweep")
	}
	return report, errors.Join(errs...)
}

func (c *TurnCompleter) failTimedOutWaits(ctx context.Context, orgID, targetID uuid.UUID, cutoff time.Time) (int64, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	failed, err := c.runs.FailTimedOutWaits(ctx, tx, orgID, targetID, cutoff)
	if err != nil {
		return 0, err
	}
	if failed == 0 {
		return 0, nil
	}
	// The timed-out run can have been the last thing a closed or merged
	// target had left to do.
	if _, err := c.targets.RetireTerminalTarget(ctx, tx, orgID, targetID); err != nil {
		return 0, err
	}
	if err := c.enqueueWake(ctx, tx, orgID, targetID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return failed, nil
}

// resolveAmbiguityDeadline commits the deadline transition whenever it
// applied, including when it converted no candidate: the flag and the
// deadline still have to be cleared, or a tie whose candidates left through
// the per-run fallback would block every later push.
func (c *TurnCompleter) resolveAmbiguityDeadline(ctx context.Context, orgID, targetID uuid.UUID, now time.Time) (bool, int64, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return false, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	resolved, converted, err := c.targets.ResolveAmbiguityDeadline(ctx, tx, orgID, targetID, now)
	if err != nil {
		return false, 0, err
	}
	if !resolved {
		return false, 0, nil
	}
	if err := c.enqueueWake(ctx, tx, orgID, targetID); err != nil {
		return false, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, 0, err
	}
	return true, converted, nil
}

// releaseStrandedOwnership clears an ownership release that no executing
// run remains to apply, so a session whose worker died between its result
// and its cleanup becomes an ordinary session again.
func (c *TurnCompleter) releaseStrandedOwnership(ctx context.Context, orgID uuid.UUID, row db.AutomationStrandedOwnership) (bool, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	released, err := c.targets.ReleaseStrandedOwnership(ctx, tx, orgID, row.GenerationID, row.TargetID)
	if err != nil {
		return false, err
	}
	if !released {
		return false, nil
	}
	if err := c.enqueueWake(ctx, tx, orgID, row.TargetID); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (c *TurnCompleter) reconcileWake(ctx context.Context, orgID, targetID uuid.UUID) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := c.EnqueueWake(ctx, tx, orgID, targetID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
