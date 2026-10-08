package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// This file holds the per-target dispatch surface of AutomationRunStore,
// AutomationTargetStore, SessionStore, SessionThreadStore, and JobStore:
// the arrival bookkeeping, the ownership transaction's reservation, waiting
// and superseding, the attempt claim, and the fenced terminal transitions
// for runs that never started. See design doc 125, "Turn Ownership and
// Dispatch", "Head Authority", and "Waiting and Coalescing".

// ErrAutomationRunNotFound is returned when a run row does not exist in the org.
var ErrAutomationRunNotFound = errors.New("automation run not found")

// AutomationRunArrival is what the target-locked arrival transaction records
// on a run: its target, the GitHub action it was delivered with, the PR
// timestamp GitHub sent, and the head ordering decided at arrival.
type AutomationRunArrival struct {
	TargetID             uuid.UUID
	GitHubAction         string
	PullRequestUpdatedAt *time.Time
	HeadEpoch            *int
	HeadResolution       *models.AutomationRunHeadResolution
}

// RecordArrival stores the arrival bookkeeping on a pending run inside the
// caller's target-locked transaction.
func (s *AutomationRunStore) RecordArrival(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID, arrival AutomationRunArrival) error {
	if arrival.HeadResolution != nil {
		if err := arrival.HeadResolution.Validate(); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE automation_runs
		SET target_id = @target_id,
		    github_action = NULLIF(@github_action, ''),
		    pull_request_updated_at = @pull_request_updated_at,
		    head_epoch = @head_epoch,
		    head_resolution = @head_resolution,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{
			"id":                      runID,
			"org_id":                  orgID,
			"target_id":               arrival.TargetID,
			"github_action":           arrival.GitHubAction,
			"pull_request_updated_at": arrival.PullRequestUpdatedAt,
			"head_epoch":              arrival.HeadEpoch,
			"head_resolution":         arrival.HeadResolution,
		})
	if err != nil {
		return fmt.Errorf("record automation run arrival: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAutomationRunNotFound
	}
	return nil
}

// GetByRunIDForUpdate loads a run and locks its row for the rest of tx.
func (s *AutomationRunStore) GetByRunIDForUpdate(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID) (models.AutomationRun, error) {
	query := fmt.Sprintf(`SELECT %s FROM automation_runs
		WHERE id = @id AND org_id = @org_id
		FOR UPDATE`, automationRunColumns)
	run, err := scanAutomationRun(tx.QueryRow(ctx, query, pgx.NamedArgs{"id": runID, "org_id": orgID}))
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationRun{}, ErrAutomationRunNotFound
	}
	if err != nil {
		return models.AutomationRun{}, fmt.Errorf("lock automation run: %w", err)
	}
	return run, nil
}

// TerminalizeUnstarted finishes a run that never started executing: its
// predicate is status = pending with dispatch_state waiting or unset, so it
// can never touch an executing run (those go through the preflight and
// completion paths). The run status follows models.AutomationRunOutcomeReason
// .RunStatus. Returns whether the row was updated.
func (s *AutomationRunStore) TerminalizeUnstarted(ctx context.Context, q DBTX, orgID, runID uuid.UUID, outcome models.AutomationRunOutcomeReason, supersededBy *uuid.UUID, summary string) (bool, error) {
	if err := outcome.Validate(); err != nil {
		return false, err
	}
	if q == nil {
		q = s.db
	}
	tag, err := q.Exec(ctx, `
		UPDATE automation_runs
		SET status = @status,
		    dispatch_state = 'done',
		    outcome_reason = @outcome,
		    superseded_by_run_id = @superseded_by,
		    completed_at = now(),
		    result_summary = NULLIF(@summary, ''),
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id
		  AND status = 'pending'
		  AND (dispatch_state = 'waiting' OR dispatch_state IS NULL)`,
		pgx.NamedArgs{
			"id":            runID,
			"org_id":        orgID,
			"status":        outcome.RunStatus(),
			"outcome":       outcome,
			"superseded_by": supersededBy,
			"summary":       summary,
		})
	if err != nil {
		return false, fmt.Errorf("terminalize unstarted automation run: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SupersedeWaitingPush skips every unstarted push run of the target whose
// epoch is strictly older than newEpoch, recording newRunID as the
// superseder. Ambiguous candidates carry a null epoch and are left alone.
// Returns the number of runs superseded.
func (s *AutomationRunStore) SupersedeWaitingPush(ctx context.Context, tx pgx.Tx, orgID, targetID, newRunID uuid.UUID, newEpoch int) (int64, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE automation_runs
		SET status = 'skipped',
		    dispatch_state = 'done',
		    outcome_reason = 'superseded',
		    superseded_by_run_id = @new_run_id,
		    completed_at = now(),
		    result_summary = 'superseded by a newer push',
		    updated_at = now()
		WHERE org_id = @org_id AND target_id = @target_id
		  AND id <> @new_run_id
		  AND github_action = 'synchronize'
		  AND status = 'pending'
		  AND (dispatch_state = 'waiting' OR dispatch_state IS NULL)
		  AND head_epoch IS NOT NULL AND head_epoch < @new_epoch`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"target_id":  targetID,
			"new_run_id": newRunID,
			"new_epoch":  newEpoch,
		})
	if err != nil {
		return 0, fmt.Errorf("supersede waiting push runs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountWaiting returns how many admitted runs have not started executing on
// the target: runs recorded as waiting plus runs whose automation_run job
// has not dispatched yet. Both count toward the waiting cap, otherwise a
// burst of arrivals could pass the cap and become more than the cap's
// worth of waiters once dispatched.
func (s *AutomationRunStore) CountWaiting(ctx context.Context, q DBTX, orgID, targetID uuid.UUID) (int, error) {
	if q == nil {
		q = s.db
	}
	var count int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM automation_runs
		WHERE org_id = @org_id AND target_id = @target_id
		  AND status = 'pending'
		  AND (dispatch_state = 'waiting' OR dispatch_state IS NULL)`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID}).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count waiting automation runs: %w", err)
	}
	return count, nil
}

// ExecutingRunForTarget returns the id of the run currently executing for
// the target, if any. The partial unique index guarantees at most one.
func (s *AutomationRunStore) ExecutingRunForTarget(ctx context.Context, q DBTX, orgID, targetID uuid.UUID) (*uuid.UUID, error) {
	if q == nil {
		q = s.db
	}
	var id uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT id FROM automation_runs
		WHERE org_id = @org_id AND target_id = @target_id AND dispatch_state = 'executing'`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID}).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find executing automation run: %w", err)
	}
	return &id, nil
}

// MarkWaiting records that the run is waiting for the target. A run that
// was already waiting keeps its original wait_started_at. Returns whether
// the row was updated.
func (s *AutomationRunStore) MarkWaiting(ctx context.Context, q DBTX, orgID, runID uuid.UUID) (bool, error) {
	if q == nil {
		q = s.db
	}
	tag, err := q.Exec(ctx, `
		UPDATE automation_runs
		SET dispatch_state = 'waiting',
		    wait_reason = 'target_busy',
		    wait_started_at = COALESCE(wait_started_at, now()),
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id
		  AND status = 'pending'
		  AND (dispatch_state = 'waiting' OR dispatch_state IS NULL)`,
		pgx.NamedArgs{"id": runID, "org_id": orgID})
	if err != nil {
		return false, fmt.Errorf("mark automation run waiting: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimPendingForPerRun is the per-run path's pending-to-running claim. A
// run that waited for a target and then fell back to per-run execution
// (kill switch, continuity switched to per_run) carries dispatch_state
// 'waiting'; the claim clears that bookkeeping in the same statement so the
// turn's attempt fence sees an ordinary per-run run. A reserved run is
// never pending, so genuine reservations keep their fencing. Returns
// whether this caller won the claim.
func (s *AutomationRunStore) ClaimPendingForPerRun(ctx context.Context, orgID, runID uuid.UUID) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE automation_runs
		SET status = 'running',
		    dispatch_state = NULL,
		    wait_reason = NULL,
		    wait_started_at = NULL,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id
		  AND status = 'pending'
		  AND dispatch_state IS DISTINCT FROM 'executing'`,
		pgx.NamedArgs{"id": runID, "org_id": orgID})
	if err != nil {
		return false, fmt.Errorf("claim automation run for per-run execution: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// AutomationRunReservation is the single-statement reservation that turns a
// pending run into the target's executing turn. Every field the executing
// CHECK requires is installed by the same UPDATE.
type AutomationRunReservation struct {
	TargetID           uuid.UUID
	TargetGeneration   int
	SessionID          uuid.UUID
	ThreadID           uuid.UUID
	TurnNumber         int
	JobID              uuid.UUID
	ContinuationMode   models.AutomationRunContinuationMode
	ContinuationReason *models.AutomationRunContinuationReason
	PreviousHeadSHA    *string
	HeadEpoch          *int
	HeadResolution     *models.AutomationRunHeadResolution
	// ResolvedHeadSHA is the head the turn reviews; nil keeps the stored
	// value (the delivered head when nothing was resolved).
	ResolvedHeadSHA    *string
	HeadLookupDegraded bool
}

// ReserveForExecution flips a pending, unstarted-or-waiting run to
// executing and running in one statement. Returns false when the run is no
// longer reservable (already reserved, terminalized, or superseded).
func (s *AutomationRunStore) ReserveForExecution(ctx context.Context, tx pgx.Tx, orgID, runID uuid.UUID, r AutomationRunReservation) (bool, error) {
	if err := r.ContinuationMode.Validate(); err != nil {
		return false, err
	}
	if r.ContinuationReason != nil {
		if err := r.ContinuationReason.Validate(); err != nil {
			return false, err
		}
	}
	if r.HeadResolution != nil {
		if err := r.HeadResolution.Validate(); err != nil {
			return false, err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE automation_runs
		SET status = 'running',
		    dispatch_state = 'executing',
		    execution_started_at = now(),
		    wait_reason = NULL,
		    target_id = @target_id,
		    target_generation = @target_generation,
		    session_id = @session_id,
		    thread_id = @thread_id,
		    turn_number = @turn_number,
		    job_id = @job_id,
		    continuation_mode = @continuation_mode,
		    continuation_reason = @continuation_reason,
		    previous_head_sha = @previous_head_sha,
		    head_epoch = COALESCE(@head_epoch, head_epoch),
		    head_resolution = COALESCE(@head_resolution, head_resolution),
		    resolved_head_sha = COALESCE(@resolved_head_sha, resolved_head_sha),
		    head_lookup_degraded = @head_lookup_degraded,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id
		  AND status = 'pending'
		  AND (dispatch_state = 'waiting' OR dispatch_state IS NULL)`,
		pgx.NamedArgs{
			"id":                   runID,
			"org_id":               orgID,
			"target_id":            r.TargetID,
			"target_generation":    r.TargetGeneration,
			"session_id":           r.SessionID,
			"thread_id":            r.ThreadID,
			"turn_number":          r.TurnNumber,
			"job_id":               r.JobID,
			"continuation_mode":    r.ContinuationMode,
			"continuation_reason":  r.ContinuationReason,
			"previous_head_sha":    r.PreviousHeadSHA,
			"head_epoch":           r.HeadEpoch,
			"head_resolution":      r.HeadResolution,
			"resolved_head_sha":    r.ResolvedHeadSHA,
			"head_lookup_degraded": r.HeadLookupDegraded,
		})
	if err != nil {
		return false, fmt.Errorf("reserve automation run for execution: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimAttempt starts a new attempt of an executing run under the job lease
// identified by jobID and lockToken. Ownership is validated against the
// jobs row inside the same transaction that locks it, so a worker whose job
// was reclaimed cannot claim an attempt even before the next worker claims
// the job. Returns the new attempt number and false when the caller is not
// the owner.
func (s *AutomationRunStore) ClaimAttempt(ctx context.Context, tx pgx.Tx, orgID, runID, jobID, lockToken uuid.UUID) (int, bool, error) {
	// The run row is locked before the job row. Every attempt write locks
	// them in that order (the fenced statements take FOR UPDATE OF r, j),
	// and so does recovery, so no pair of writers can hold one and wait for
	// the other.
	var runID2 uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM automation_runs
		WHERE id = @run_id AND org_id = @org_id AND dispatch_state = 'executing' AND job_id = @job_id
		FOR UPDATE`,
		pgx.NamedArgs{"run_id": runID, "org_id": orgID, "job_id": jobID}).Scan(&runID2)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("lock run for attempt claim: %w", err)
	}
	var owned int
	err = tx.QueryRow(ctx, `
		SELECT 1 FROM jobs
		WHERE id = @job_id AND org_id = @org_id AND status = 'running'
		  AND lock_token = @lock_token AND lease_expires_at > now()
		FOR UPDATE`,
		pgx.NamedArgs{"job_id": jobID, "org_id": orgID, "lock_token": lockToken}).Scan(&owned)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("verify job lease for attempt claim: %w", err)
	}
	var attempt int
	err = tx.QueryRow(ctx, `
		UPDATE automation_runs
		SET attempt = attempt + 1,
		    attempt_lock_token = @lock_token,
		    attempt_started_at = now(),
		    updated_at = now()
		WHERE id = @run_id AND org_id = @org_id
		  AND dispatch_state = 'executing' AND job_id = @job_id
		RETURNING attempt`,
		pgx.NamedArgs{"run_id": runID, "org_id": orgID, "job_id": jobID, "lock_token": lockToken}).Scan(&attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("claim automation run attempt: %w", err)
	}
	return attempt, true, nil
}

// NextWaiting returns the run that should dispatch next for the target
// among every admitted run that has not started: the authoritative push
// run first, then unresolved push candidates and every other event in
// arrival order. Ambiguous candidates are never returned; they wait for the
// lookup or the ambiguity deadline. Dispatch consults it under the target
// lock so an older comment cannot beat a newer push and later arrivals
// cannot bypass earlier ones.
func (s *AutomationRunStore) NextWaiting(ctx context.Context, q DBTX, orgID, targetID uuid.UUID) (models.AutomationRun, error) {
	if q == nil {
		q = s.db
	}
	query := fmt.Sprintf(`SELECT %s FROM automation_runs
		WHERE org_id = @org_id AND target_id = @target_id
		  AND status = 'pending'
		  AND (dispatch_state = 'waiting' OR dispatch_state IS NULL)
		  AND head_resolution IS DISTINCT FROM 'ambiguous'
		ORDER BY
		  CASE WHEN github_action = 'synchronize' AND head_resolution = 'authoritative' THEN 0 ELSE 1 END,
		  triggered_at, id
		LIMIT 1`, automationRunColumns)
	run, err := scanAutomationRun(q.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID, "target_id": targetID}))
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationRun{}, ErrAutomationRunNotFound
	}
	if err != nil {
		return models.AutomationRun{}, fmt.Errorf("next waiting automation run: %w", err)
	}
	return run, nil
}

// AmbiguousPushCandidate is a waiting push run whose head ordering could
// not be decided at arrival, together with the head it was delivered with.
type AmbiguousPushCandidate struct {
	RunID   uuid.UUID
	HeadSHA string
}

// ListAmbiguousPushCandidates returns the target's ambiguous push runs with
// their delivered heads, oldest first.
func (s *AutomationRunStore) ListAmbiguousPushCandidates(ctx context.Context, q DBTX, orgID, targetID uuid.UUID) ([]AmbiguousPushCandidate, error) {
	if q == nil {
		q = s.db
	}
	rows, err := q.Query(ctx, `
		SELECT id, COALESCE(config_snapshot #>> '{github,head_sha}', '')
		FROM automation_runs
		WHERE org_id = @org_id AND target_id = @target_id
		  AND status = 'pending'
		  AND (dispatch_state = 'waiting' OR dispatch_state IS NULL)
		  AND head_resolution = 'ambiguous'
		ORDER BY triggered_at, id`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID})
	if err != nil {
		return nil, fmt.Errorf("list ambiguous push candidates: %w", err)
	}
	defer rows.Close()
	var out []AmbiguousPushCandidate
	for rows.Next() {
		var c AmbiguousPushCandidate
		if err := rows.Scan(&c.RunID, &c.HeadSHA); err != nil {
			return nil, fmt.Errorf("scan ambiguous push candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StampHeadResolution records the ordering decided for a run after arrival
// (a resolved ambiguity or a dispatch-time lookup).
func (s *AutomationRunStore) StampHeadResolution(ctx context.Context, q DBTX, orgID, runID uuid.UUID, epoch *int, resolution models.AutomationRunHeadResolution, headSHA string) error {
	if err := resolution.Validate(); err != nil {
		return err
	}
	if q == nil {
		q = s.db
	}
	tag, err := q.Exec(ctx, `
		UPDATE automation_runs
		SET head_epoch = @epoch, head_resolution = @resolution,
		    resolved_head_sha = NULLIF(@head_sha, ''),
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": runID, "org_id": orgID, "epoch": epoch, "resolution": resolution, "head_sha": headSHA})
	if err != nil {
		return fmt.Errorf("stamp automation run head resolution: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAutomationRunNotFound
	}
	return nil
}

// AdoptHead records a strictly newer authoritative head on the target and
// returns the new epoch. updatedAt may be nil for a dispatch-time lookup
// whose response omitted the timestamp; the observed timestamp is then left
// unchanged so later deliveries still order against it.
func (s *AutomationTargetStore) AdoptHead(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, headSHA string, updatedAt *time.Time) (int, error) {
	if headSHA == "" {
		return 0, fmt.Errorf("automation target head sha is required")
	}
	var epoch int
	err := tx.QueryRow(ctx, `
		UPDATE automation_targets
		SET observed_head_sha = @head_sha,
		    observed_head_updated_at = COALESCE(@updated_at, observed_head_updated_at),
		    head_epoch = head_epoch + 1,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id
		RETURNING head_epoch`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID, "head_sha": headSHA, "updated_at": updatedAt}).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrAutomationTargetNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("adopt automation target head: %w", err)
	}
	return epoch, nil
}

// MarkHeadResolutionPending flags the target as holding ambiguous push
// candidates and sets the ambiguity deadline if none is pending yet, so
// consecutive ties share one window and a later tie after a resolution
// gets a fresh one.
func (s *AutomationTargetStore) MarkHeadResolutionPending(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, deadline time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE automation_targets
		SET head_resolution_pending = true,
		    head_resolution_deadline_at = COALESCE(head_resolution_deadline_at, @deadline),
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID, "deadline": deadline})
	if err != nil {
		return fmt.Errorf("mark automation target head resolution pending: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAutomationTargetNotFound
	}
	return nil
}

// ClearHeadResolutionPending clears both the pending flag and the deadline;
// called by every transition that leaves no ambiguous candidate on the
// target.
func (s *AutomationTargetStore) ClearHeadResolutionPending(ctx context.Context, q DBTX, orgID, targetID uuid.UUID) error {
	if q == nil {
		q = s.db
	}
	tag, err := q.Exec(ctx, `
		UPDATE automation_targets
		SET head_resolution_pending = false,
		    head_resolution_deadline_at = NULL,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID})
	if err != nil {
		return fmt.Errorf("clear automation target head resolution: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAutomationTargetNotFound
	}
	return nil
}

// ClaimForAutomationTurn claims a session for a per-target automation turn
// inside the ownership transaction. It accepts idle plus every resumable
// status, applies the same runtime reset assignments as ClaimForResume, and
// permits a destroyed sandbox only when the caller is reconstructing the
// workspace. Existing ClaimForResume is not reused because it excludes idle
// and rejects destroyed sandboxes. On an owned session the claim cannot
// contend with a human turn.
func (s *SessionStore) ClaimForAutomationTurn(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, allowDestroyed bool) (models.Session, error) {
	statuses := append([]models.SessionStatus{models.SessionStatusIdle}, models.ResumableSessionStatuses...)
	query := fmt.Sprintf(`
		UPDATE sessions
		SET status = 'running', started_at = now(), completed_at = NULL,
		    %s,
		    last_activity_at = now()
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		  AND status = ANY(@statuses)
		  AND (@allow_destroyed OR sandbox_state != 'destroyed')
		RETURNING `+sessionSelectColumns, sessionResumeRuntimeResetAssignments)
	rows, err := tx.Query(ctx, query, pgx.NamedArgs{
		"id":              sessionID,
		"org_id":          orgID,
		"statuses":        sessionStatusStrings(statuses),
		"allow_destroyed": allowDestroyed,
	})
	if err != nil {
		return models.Session{}, fmt.Errorf("claim session for automation turn: %w", err)
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return models.Session{}, err
	}
	hydrateSessionPolicy(&session)
	return session, nil
}

// ClaimPrimaryForAutomationTurn claims the session's primary thread (the
// oldest unarchived thread) for a per-target automation turn inside the
// ownership transaction: idle threads take a fresh claim and resumable
// threads take a resume claim. Returns the claimed thread.
func (s *SessionThreadStore) ClaimPrimaryForAutomationTurn(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.SessionThread, error) {
	claimable := append([]string{string(models.ThreadStatusIdle)}, threadStatusStrings(models.ResumableThreadStatuses)...)
	rows, err := tx.Query(ctx, `
		WITH primary_thread AS (
			SELECT id FROM session_threads
			WHERE org_id = @org_id AND session_id = @session_id AND archived_at IS NULL
			ORDER BY created_at, id
			LIMIT 1
			FOR UPDATE
		)
		UPDATE session_threads
		SET status = 'running',
		    started_at = now(),
		    completed_at = NULL,
		    last_activity_at = now(),
		    cancel_requested_at = NULL
		WHERE id = (SELECT id FROM primary_thread)
		  AND org_id = @org_id
		  AND status = ANY(@claimable)
		RETURNING `+sessionThreadSelectColumns,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "claimable": claimable})
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("claim primary thread for automation turn: %w", err)
	}
	thread, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.SessionThread])
	if err != nil {
		return models.SessionThread{}, err
	}
	// The runtime event is published by the caller after commit
	// (PublishRuntime); publishing here would announce a claim that a later
	// rollback undoes.
	return thread, nil
}

// PublishRuntime emits the thread's current runtime state to the session
// stream. Callers that claim a thread inside a transaction call it after
// the commit.
func (s *SessionThreadStore) PublishRuntime(ctx context.Context, orgID, threadID uuid.UUID) {
	s.publishThreadRuntimeByID(ctx, orgID, threadID)
}

// ActiveJobPayloadByDedupeKeyInTx returns the pending or running job that
// holds dedupeKey on queue, with its payload, inside tx. Used by the
// ownership transaction's conflict lookup when EnqueueInTx reports a
// dedupe conflict.
func (s *JobStore) ActiveJobPayloadByDedupeKeyInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, queue, dedupeKey string) (ActiveJobRef, json.RawMessage, error) {
	var active ActiveJobRef
	var payload json.RawMessage
	err := tx.QueryRow(ctx, `
		SELECT id, status, payload
		FROM jobs
		WHERE org_id = @org_id
		  AND queue = @queue
		  AND dedupe_key = @dedupe_key
		  AND status IN ('pending', 'running')`, pgx.NamedArgs{
		"org_id":     orgID,
		"queue":      queue,
		"dedupe_key": dedupeKey,
	}).Scan(&active.ID, &active.Status, &payload)
	if err != nil {
		return ActiveJobRef{}, nil, err
	}
	return active, payload, nil
}

// TouchObservedHead advances the observed head's timestamp for a delivery
// of the same head without opening a new epoch, so a force-push back to a
// previously observed head still outranks a delayed older delivery.
func (s *AutomationTargetStore) TouchObservedHead(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, headSHA string, updatedAt time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE automation_targets
		SET observed_head_updated_at = GREATEST(COALESCE(observed_head_updated_at, @updated_at), @updated_at),
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id AND observed_head_sha = @head_sha`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID, "head_sha": headSHA, "updated_at": updatedAt})
	if err != nil {
		return fmt.Errorf("touch automation target head: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAutomationTargetNotFound
	}
	return nil
}

// SetAttemptedHead records the head a reserved turn is about to review on
// its generation, so the turn's workspace preparation and later recovery
// read a durable value rather than the delivered head in the audit snapshot.
func (s *AutomationTargetStore) SetAttemptedHead(ctx context.Context, tx pgx.Tx, orgID, generationID uuid.UUID, headSHA string, baseRef string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE automation_target_sessions
		SET last_attempted_head_sha = NULLIF(@head_sha, ''),
		    last_base_ref = COALESCE(NULLIF(@base_ref, ''), last_base_ref),
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status = 'active'`,
		pgx.NamedArgs{"id": generationID, "org_id": orgID, "head_sha": headSHA, "base_ref": baseRef})
	if err != nil {
		return fmt.Errorf("record attempted head: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAutomationTargetGenerationNotActive
	}
	return nil
}

// UpsertCapabilitySnapshotInTx replaces the session's capability snapshot
// with the current run's, inside the ownership transaction, so credential
// and tool resolution during a continued turn sees the current grant.
func (s *SessionStore) UpsertCapabilitySnapshotInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, snapshot []models.AgentCapabilitySnapshotItem) error {
	if snapshot == nil {
		snapshot = []models.AgentCapabilitySnapshotItem{}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal capability snapshot: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO session_execution_metadata (session_id, org_id, capability_snapshot)
		SELECT id, org_id, @snapshot::jsonb FROM sessions WHERE id = @session_id AND org_id = @org_id
		ON CONFLICT (session_id) DO UPDATE SET capability_snapshot = EXCLUDED.capability_snapshot`,
		pgx.NamedArgs{"session_id": sessionID, "org_id": orgID, "snapshot": encoded})
	if err != nil {
		return fmt.Errorf("upsert session capability snapshot: %w", err)
	}
	return nil
}
