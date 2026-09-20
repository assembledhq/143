// Package automations contains service-layer glue for the automations feature.
// It lives alongside other service packages (pm, validation, etc.) so the
// orchestrator's completion hooks can depend on it without pulling the db
// layer into the agent package directly.
package automations

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// automationRunStore is the minimal surface AutomationHooks needs. Defined as
// an interface here (rather than taking *db.AutomationRunStore) so hook tests
// don't have to stand up a Postgres pool.
type automationRunStore interface {
	TransitionStatusIf(ctx context.Context, orgID, runID uuid.UUID, fromStatus, toStatus models.AutomationRunStatus, completedAt *time.Time, resultSummary *string) (bool, error)
	GetByRunID(ctx context.Context, orgID, runID uuid.UUID) (models.AutomationRun, error)
}

type pagerDutyAutomationWritebacker interface {
	OnAutomationSessionComplete(ctx context.Context, session models.Session, automationRun models.AutomationRun, status models.SessionStatus, summary string) error
}

// automationFallbackRunStore and automationFallbackJobStore are the extra
// surface on-failure model promotion needs. They stay off the required
// automationRunStore interface so every existing caller of NewAutomationHooks
// keeps compiling — a hooks value without a promoter simply fails a run on the
// first capacity error. They are split in two because the attempt history lives
// on the run store and the enqueue on the job store, which lets wiring pass
// both concrete stores straight through without an adapter.
type automationFallbackRunStore interface {
	ListSessionAttempts(ctx context.Context, orgID, runID uuid.UUID) ([]models.AutomationRunAttempt, error)
}

const (
	// automationRunExecutionBudget mirrors stuckAutomationRunThreshold in
	// internal/cluster/scheduler.go: how long a pending/running automation_run
	// may sit before the reaper marks it failed. Every attempt in a fallback
	// chain shares it, because promotion preserves the run's triggered_at.
	automationRunExecutionBudget = time.Hour
	// automationAttemptBudgetReserve is the slice of that budget one more
	// session needs, matching models.DefaultMaxSessionDurationSeconds.
	automationAttemptBudgetReserve = 20 * time.Minute
)

type automationFallbackJobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	Notify(ctx context.Context, jobID uuid.UUID)
}

// automationFallbackPromoter bundles the two optional dependencies so the
// promotion path has a single nil check.
type automationFallbackPromoter struct {
	runs automationFallbackRunStore
	jobs automationFallbackJobStore
}

// AutomationHooks implements agent.AutomationRunUpdater by mapping a session's
// terminal status back onto its owning automation_runs row.
type AutomationHooks struct {
	runs                 automationRunStore
	pagerDutyWritebacker pagerDutyAutomationWritebacker
	fallback             *automationFallbackPromoter
	logger               zerolog.Logger
}

func NewAutomationHooks(runs automationRunStore, logger zerolog.Logger) *AutomationHooks {
	return &AutomationHooks{runs: runs, logger: logger}
}

func (h *AutomationHooks) SetPagerDutyWritebacker(writebacker pagerDutyAutomationWritebacker) {
	h.pagerDutyWritebacker = writebacker
}

// SetFallbackPromoter enables on-failure model promotion. Optional in the same
// way as SetPagerDutyWritebacker: both stores must be supplied for the chain to
// run, and without them a capacity failure lands the run as failed.
func (h *AutomationHooks) SetFallbackPromoter(runs automationFallbackRunStore, jobs automationFallbackJobStore) {
	if runs == nil || jobs == nil {
		return
	}
	h.fallback = &automationFallbackPromoter{runs: runs, jobs: jobs}
}

// AutomaticPublishPolicy returns the immutable policy captured when the
// automation run was created. Reading the run snapshot instead of the live
// automation keeps an in-flight run's publication behavior deterministic.
func (h *AutomationHooks) AutomaticPublishPolicy(ctx context.Context, orgID, runID uuid.UUID) (models.AutomationPublishPolicy, error) {
	automationRun, err := h.runs.GetByRunID(ctx, orgID, runID)
	if err != nil {
		return "", fmt.Errorf("load automation run publish policy: %w", err)
	}
	policy, err := models.AutomationPublishPolicyFromConfigSnapshot(automationRun.ConfigSnapshot)
	if err != nil {
		return "", fmt.Errorf("resolve automation run publish policy: %w", err)
	}
	return policy, nil
}

// OnSessionComplete maps a session's terminal status to the automation_run
// row. Non-terminal statuses (awaiting_input, cancelled, etc.) are ignored —
// the automation_run stays "running" until the session reaches a terminal
// state, matching the reaper contract (stuck pending/running rows get failed
// after the threshold).
//
// Transition is conditional on the current automation_run status being
// "running" so that if both the orchestrator's success path and failRun fire
// for the same session (or the reaper has already written a terminal status),
// a second call here cannot overwrite an already-terminal row.
func (h *AutomationHooks) OnSessionComplete(ctx context.Context, run *models.Session, status models.SessionStatus) error {
	if run.AutomationRunID == nil {
		return nil
	}

	var runStatus models.AutomationRunStatus
	switch status {
	case models.SessionStatusCompleted:
		if run.Diff == nil || strings.TrimSpace(*run.Diff) == "" {
			runStatus = models.AutomationRunStatusCompletedNoop
		} else {
			runStatus = models.AutomationRunStatusCompleted
		}
	case models.SessionStatusFailed, models.SessionStatusNeedsHumanGuidance:
		// needs_human_guidance is terminal from the orchestrator's
		// perspective — a human response starts a fresh session rather than
		// re-entering this hook — so map it onto failed here too, matching
		// pm.ProjectHooks. Without this the automation_run would stay
		// "running" until the 1-hour reaper swept it.
		runStatus = models.AutomationRunStatusFailed
	default:
		// Ignore non-terminal updates; the automation_run will be updated
		// when the session eventually lands in a terminal status.
		return nil
	}

	// A model being at capacity says nothing about the work, so give the run
	// the next model in its fallback chain before writing a terminal status.
	// Only a plain failure qualifies: needs_human_guidance is a verdict on the
	// task, and a different model would reach the same place.
	if status == models.SessionStatusFailed {
		promoted, err := h.promoteToNextModelRank(ctx, run)
		if err != nil {
			return err
		}
		if promoted {
			return nil
		}
	}

	now := time.Now().UTC()
	summary := deriveSummary(run, status)
	transitioned, err := h.runs.TransitionStatusIf(ctx, run.OrgID, *run.AutomationRunID, models.AutomationRunStatusRunning, runStatus, &now, summary)
	if err != nil {
		return fmt.Errorf("update automation run status: %w", err)
	}
	if !transitioned {
		// Row was already non-running (terminal, or never claimed by the
		// worker handler). No-op — the earlier writer's status stands.
		h.logger.Debug().
			Str("automation_run_id", run.AutomationRunID.String()).
			Str("attempted_status", string(runStatus)).
			Msg("automation run already non-running; hook update skipped")
	}
	if transitioned && h.pagerDutyWritebacker != nil {
		automationRun, lookupErr := h.runs.GetByRunID(ctx, run.OrgID, *run.AutomationRunID)
		if lookupErr != nil {
			h.logger.Warn().
				Err(lookupErr).
				Str("automation_run_id", run.AutomationRunID.String()).
				Msg("failed to load automation run for PagerDuty writeback")
		} else if writeErr := h.pagerDutyWritebacker.OnAutomationSessionComplete(ctx, *run, automationRun, status, valueOrEmpty(summary)); writeErr != nil {
			h.logger.Warn().
				Err(writeErr).
				Str("automation_run_id", run.AutomationRunID.String()).
				Msg("failed to write PagerDuty automation session completion note")
		}
	}
	return nil
}

// promoteToNextModelRank re-dispatches a run whose session died of model
// capacity onto the next rank in the automation's chain. It reports whether
// the run was handled here — either promoted, or written terminal by the
// compensating path below — in which case the caller must not apply its own
// running->failed transition.
//
// The run stays in flight for CountInFlightRuns/max_concurrent for the whole
// chain: it is still one logical run, and that count covers pending as well as
// running. ReapStuckRuns keys off triggered_at rather than the current status,
// so re-entering pending cannot extend the run's 1-hour budget.
//
// Everything that is not a promotion (no promoter wired, legacy run, exhausted
// chain, non-capacity failure, work already produced) returns false and lets
// the caller fail the run. Lookup errors are logged rather than returned for
// the same reason — a broken fallback read must not stop the run from landing
// on a terminal status.
func (h *AutomationHooks) promoteToNextModelRank(ctx context.Context, session *models.Session) (bool, error) {
	if h.fallback == nil || session.AutomationRunID == nil {
		return false, nil
	}
	orgID := session.OrgID
	runID := *session.AutomationRunID

	automationRun, err := h.runs.GetByRunID(ctx, orgID, runID)
	if err != nil {
		// Logged, not returned: a transient read on the fallback path must not
		// stop the caller from writing the run terminal, or one failed lookup
		// strands the row as running until the reaper sweeps it an hour later.
		h.logger.Warn().
			Err(err).
			Str("automation_run_id", runID.String()).
			Msg("failed to load automation run for model fallback; failing run")
		return false, nil
	}
	// Read the chain the run was dispatched under, not the live automation —
	// editing the automation mid-run must not re-point an in-flight chain.
	ranks, ok, err := models.AutomationModelRanksFromConfigSnapshot(automationRun.ConfigSnapshot)
	if err != nil {
		h.logger.Warn().
			Err(err).
			Str("automation_run_id", runID.String()).
			Msg("failed to read automation run model ranks; failing run")
		return false, nil
	}
	if !ok {
		// Snapshot predates fallback models: there is no chain to promote.
		return false, nil
	}

	attempts, err := h.fallback.runs.ListSessionAttempts(ctx, orgID, runID)
	if err != nil {
		h.logger.Warn().
			Err(err).
			Str("automation_run_id", runID.String()).
			Msg("failed to read automation run attempts for model fallback; failing run")
		return false, nil
	}
	// Only the run's newest session may promote it. Without this a duplicate
	// hook delivery for an already-superseded session would CAS a run that is
	// legitimately running its next attempt back to pending and enqueue a
	// second dispatch, leaving two agent sessions doing the same work. The
	// status CAS alone cannot catch it: "running" is true for the successor too.
	if len(attempts) > 0 && attempts[0].SessionID != session.ID {
		return false, nil
	}
	// Whether anything is left to try is decided the same way the worker
	// decides it — by dropping the (agent, model) pairs already spent — so the
	// hook cannot promote a run the worker will immediately fail as exhausted.
	if len(models.AutomationModelRanksRemaining(ranks, attempts)) == 0 {
		return false, nil
	}
	// A promoted run keeps its original triggered_at, so every attempt shares
	// one reaper budget. Promoting with less than a session's worth of it left
	// starts work that outlives its own run: the reaper marks the run failed
	// while that session keeps going, and its completion hook can no longer
	// update the terminal row — so a diff or a pull request can land with
	// nothing recording it.
	if !automationRunBudgetFitsAnotherAttempt(automationRun.TriggeredAt, time.Now()) {
		h.logger.Info().
			Str("automation_run_id", runID.String()).
			Time("triggered_at", automationRun.TriggeredAt).
			Msg("automation run has too little execution budget left to try another model; failing run")
		return false, nil
	}
	if !sessionModelUnavailable(session) {
		return false, nil
	}
	// The persisted attempt rows are authoritative about side effects: the
	// in-memory session's publish state is whatever it was at load time, so
	// reading it here would let a run that already opened a pull request be
	// retried on another model and open a second one.
	if attemptsProducedWork(attempts) {
		return false, nil
	}

	// Transition before enqueueing. The CAS guarantees exactly one promoter
	// wins when this hook fires twice for the same session, and pending is
	// what the unchanged worker guard (run.Status != pending -> skip) needs to
	// accept the re-dispatch. Enqueueing first would let the worker read a
	// still-running row and early-exit, dropping the run on the floor.
	transitioned, err := h.runs.TransitionStatusIf(ctx, orgID, runID, models.AutomationRunStatusRunning, models.AutomationRunStatusPending, nil, nil)
	if err != nil {
		h.logger.Warn().
			Err(err).
			Str("automation_run_id", runID.String()).
			Msg("failed to promote automation run to its next model; failing run")
		return false, nil
	}
	if !transitioned {
		return false, nil
	}

	// Per-attempt dedupe key so duplicate hook invocations that both got here
	// collapse into one job, while a later rank still gets its own.
	dedupeKey := fmt.Sprintf("automation_run_fallback:%s:%d", runID.String(), len(attempts))
	payload := map[string]string{
		"org_id":            orgID.String(),
		"automation_id":     automationRun.AutomationID.String(),
		"automation_run_id": runID.String(),
	}
	jobID, err := h.fallback.jobs.Enqueue(ctx, orgID, "default", models.JobTypeAutomationRun, payload, 5, &dedupeKey)
	if err != nil {
		// The row is pending with nothing coming to claim it. Fail it now:
		// otherwise a clean failure turns into a silent hour-long hang until
		// the reaper sweeps it.
		h.logger.Warn().
			Err(err).
			Str("automation_run_id", runID.String()).
			Int("attempt", len(attempts)).
			Msg("failed to enqueue automation model fallback run; failing run")
		now := time.Now().UTC()
		if _, failErr := h.runs.TransitionStatusIf(ctx, orgID, runID, models.AutomationRunStatusPending, models.AutomationRunStatusFailed, &now, deriveSummary(session, models.SessionStatusFailed)); failErr != nil {
			return false, fmt.Errorf("fail automation run after fallback enqueue error: %w", failErr)
		}
		return true, nil
	}
	// Enqueue reports a dedupe collision as (uuid.Nil, nil): an equivalent job
	// is already pending or running, which satisfies the intent, but there is
	// nothing new to wake.
	if jobID != uuid.Nil {
		h.fallback.jobs.Notify(ctx, jobID)
	}
	h.logger.Info().
		Str("automation_run_id", runID.String()).
		Str("job_id", jobID.String()).
		Int("attempt", len(attempts)).
		Int("ranks", len(ranks)).
		Msg("promoted automation run to next model rank after a capacity failure")
	return true, nil
}

// sessionModelUnavailable reports whether a failed session died of model
// capacity rather than of the work itself. It reads the structured failure
// explanation first and then the raw error, the same structured-then-raw order
// as the code reviewer's codeReviewAgentModelUnavailable — but consults both,
// because the explanation is a generated human summary that frequently drops
// the upstream marker the classifier matches on.
//
// This is deliberately the orchestrator's capacity-only rule and not the
// reviewer's error-agnostic one. An automation does real work with side
// effects and cost, so burning five models on a deterministic prompt bug is
// worse than one honest failure.
func sessionModelUnavailable(session *models.Session) bool {
	if session.FailureExplanation != nil && agent.ModelUnavailableForRetry(*session.FailureExplanation) {
		return true
	}
	return session.Error != nil && agent.ModelUnavailableForRetry(*session.Error)
}

// automationRunBudgetFitsAnotherAttempt reports whether a run still has room
// for one more session inside the window the reaper allows it.
//
// Both bounds are deliberately the conservative defaults rather than the org's
// configured session timeout: this only has to stop the chain from starting
// work it cannot finish, and reading per-org settings here would put a second
// lookup on every failed automation session.
func automationRunBudgetFitsAnotherAttempt(triggeredAt, now time.Time) bool {
	return now.Add(automationAttemptBudgetReserve).Before(triggeredAt.Add(automationRunExecutionBudget))
}

// attemptsProducedWork reports whether any session this run has already spawned
// left work behind. A capacity error hit *after* the agent produced a diff or
// opened a pull request must fail the run rather than redo it on a fresh
// session — re-running would duplicate the PR.
//
// This reads the persisted attempt rows rather than the in-memory session
// because the session struct the hook receives carries publish state from when
// the worker loaded it, not from what the run went on to do.
func attemptsProducedWork(attempts []models.AutomationRunAttempt) bool {
	for _, attempt := range attempts {
		if attempt.ProducedWork() {
			return true
		}
	}
	return false
}

// deriveSummary picks the most useful single-line summary from the session's
// terminal fields. Prefers the orchestrator's result summary; falls back to
// the error string on failure; finally to a generic status label so the row
// never lands with an empty result_summary.
func deriveSummary(run *models.Session, status models.SessionStatus) *string {
	if run.ResultSummary != nil && *run.ResultSummary != "" {
		s := *run.ResultSummary
		return &s
	}
	sessionStatus := models.SessionStatus(status)
	if sessionStatus == models.SessionStatusFailed && run.Error != nil && *run.Error != "" {
		s := *run.Error
		return &s
	}
	var s string
	switch sessionStatus {
	case models.SessionStatusCompleted:
		s = "Agent session completed."
	case models.SessionStatusFailed:
		s = "Agent session failed."
	case models.SessionStatusNeedsHumanGuidance:
		s = "Agent run needs human guidance."
	default:
		s = fmt.Sprintf("Agent session ended with status %q.", status)
	}
	return &s
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
