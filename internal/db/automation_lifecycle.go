package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// Per-target continuity: what a pull request's own lifecycle does to its
// targets (design doc 125, "Lifecycle Transitions"). The notification
// arrives from PRService whether or not any automation is subscribed to the
// event, so a target closes even when its automation would never have been
// triggered by the close.

// ListTargetsForPullRequest returns the org's per-target rows for one pull
// request, across every automation. The target key of a pull request target
// is its number.
func (s *AutomationTargetStore) ListTargetsForPullRequest(ctx context.Context, orgID uuid.UUID, repoFullName string, number int) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.id
		FROM automation_targets t
		JOIN repositories r ON r.id = t.repository_id AND r.org_id = t.org_id
		WHERE t.org_id = @org_id AND r.full_name = @full_name
		  AND t.target_kind = @kind AND t.target_key = @key
		ORDER BY t.id`,
		pgx.NamedArgs{
			"org_id": orgID, "full_name": repoFullName,
			"kind": models.AutomationTargetKindGitHubPullRequest, "key": strconv.Itoa(number),
		})
	if err != nil {
		return nil, fmt.Errorf("list automation targets for pull request: %w", err)
	}
	defer rows.Close()
	return collectIDs(rows)
}

// AutomationTargetLifecycleOutcome reports what a lifecycle notification
// applied to one target.
type AutomationTargetLifecycleOutcome struct {
	TargetID uuid.UUID
	// SkippedRuns is how many waiting runs were skipped as pr_closed.
	SkippedRuns int64
	// GenerationRetired is set when the active generation was retired now.
	// It is false when a merged final turn is still to run: that turn's
	// completion retires the generation instead.
	GenerationRetired bool
	// FinalTurnPending is set when the subscribed merged run is still
	// waiting or executing, so the generation lives until it finishes.
	FinalTurnPending bool
}

// ApplyPullRequestClosed records a pull request's terminal state on one
// target and applies the lifecycle table: waiting runs are skipped as
// pr_closed, except a subscribed merged run on a merged pull request, which
// keeps its final turn; the active generation is retired unless that final
// turn is still to come. Executing runs are never touched here — they
// finish and their completion applies any pending ownership release. The
// target is woken. Takes the target lock, so it serializes with arrival and
// dispatch.
func (s *AutomationTargetStore) ApplyPullRequestClosed(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, merged bool, observedAt time.Time) (AutomationTargetLifecycleOutcome, error) {
	out := AutomationTargetLifecycleOutcome{TargetID: targetID}
	if err := lockAutomationTarget(ctx, tx, orgID, targetID); err != nil {
		return out, err
	}
	state := models.AutomationTargetLifecycleClosed
	retiredReason := models.AutomationTargetRetiredPRClosed
	if merged {
		state = models.AutomationTargetLifecycleMerged
		retiredReason = models.AutomationTargetRetiredPRMerged
	}
	if err := s.SetLifecycleObserved(ctx, tx, orgID, targetID, state, &observedAt); err != nil {
		return out, err
	}

	// A merged pull request's subscribed merged run is the final turn; on an
	// unmerged close nothing survives.
	tag, err := tx.Exec(ctx, `
		UPDATE automation_runs r
		SET status = 'skipped', dispatch_state = 'done', outcome_reason = @outcome,
		    completed_at = now(), result_summary = 'the pull request is no longer open', updated_at = now()
		WHERE r.org_id = @org_id AND r.target_id = @target_id
		  AND r.status = 'pending' AND (r.dispatch_state = 'waiting' OR r.dispatch_state IS NULL)
		  AND NOT (@merged::bool AND r.config_snapshot->>'github_event' = @merged_event)`,
		pgx.NamedArgs{
			"org_id": orgID, "target_id": targetID, "merged": merged,
			"outcome": models.AutomationRunOutcomePRClosed, "merged_event": models.AutomationGitHubEventPullRequestMerged,
		})
	if err != nil {
		return out, fmt.Errorf("skip waiting runs after pull request close: %w", err)
	}
	out.SkippedRuns = tag.RowsAffected()

	// Only a surviving merged run defers the retirement, and only on a
	// merged pull request: it is the generation's last turn, so the
	// generation lives until its completion retires it. An unmerged close
	// retires now even with a turn executing — RetireGeneration marks the
	// ownership release pending and that turn's completion applies it,
	// which is what keeps the session from staying owned when the turn ends
	// through a path that writes no result.
	if merged {
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM automation_runs r
				WHERE r.org_id = @org_id AND r.target_id = @target_id
				  AND (r.dispatch_state IN ('waiting', 'executing') OR r.dispatch_state IS NULL)
				  AND r.status IN ('pending', 'running')
				  AND r.config_snapshot->>'github_event' = @merged_event)`,
			pgx.NamedArgs{"org_id": orgID, "target_id": targetID, "merged_event": models.AutomationGitHubEventPullRequestMerged}).
			Scan(&out.FinalTurnPending); err != nil {
			return out, fmt.Errorf("check for a pending final turn: %w", err)
		}
	}

	if !out.FinalTurnPending {
		generation, err := s.GetActiveGeneration(ctx, tx, orgID, targetID)
		switch {
		case errors.Is(err, ErrAutomationTargetGenerationNotFound):
			// Nothing to retire: no generation was ever created.
		case err != nil:
			return out, err
		default:
			if _, err := s.RetireGeneration(ctx, tx, orgID, generation.ID, retiredReason); err != nil && !errors.Is(err, ErrAutomationTargetGenerationNotActive) {
				return out, err
			}
			out.GenerationRetired = true
		}
	}
	if _, err := s.RequestWake(ctx, tx, orgID, targetID); err != nil {
		return out, err
	}
	return out, nil
}

// ReopenTarget puts a closed or merged target back to open, under the
// target lock. The retired generation stays retired: the next trigger
// creates a new one. Returns false when the target was already open.
func (s *AutomationTargetStore) ReopenTarget(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID, observedAt time.Time) (bool, error) {
	if err := lockAutomationTarget(ctx, tx, orgID, targetID); err != nil {
		return false, err
	}
	var state models.AutomationTargetLifecycleState
	if err := tx.QueryRow(ctx, `
		SELECT lifecycle_state FROM automation_targets WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}).Scan(&state); err != nil {
		return false, fmt.Errorf("read automation target lifecycle: %w", err)
	}
	if state == models.AutomationTargetLifecycleOpen {
		return false, nil
	}
	if err := s.SetLifecycleObserved(ctx, tx, orgID, targetID, models.AutomationTargetLifecycleOpen, &observedAt); err != nil {
		return false, err
	}
	return true, nil
}

// TerminalLifecycleRetirement is the reason a target's lifecycle gives for
// retiring its generation now that the current turn has finished, or an
// empty reason when the target is still open or another turn is still to
// run on it. The completer asks for it so a merged pull request's final
// turn is the last one on its generation, and so a close that arrived
// while a turn was executing still ends the generation.
func (s *AutomationTargetStore) TerminalLifecycleRetirement(ctx context.Context, q DBTX, orgID, targetID uuid.UUID) (models.AutomationTargetRetiredReason, error) {
	if q == nil {
		q = s.db
	}
	var state models.AutomationTargetLifecycleState
	err := q.QueryRow(ctx, `
		SELECT lifecycle_state FROM automation_targets WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAutomationTargetNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read automation target lifecycle: %w", err)
	}
	var reason models.AutomationTargetRetiredReason
	switch state {
	case models.AutomationTargetLifecycleMerged:
		reason = models.AutomationTargetRetiredPRMerged
	case models.AutomationTargetLifecycleClosed:
		reason = models.AutomationTargetRetiredPRClosed
	default:
		return "", nil
	}
	// A merged pull request's subscribed merged run is the generation's last
	// turn, and it can still be waiting behind the turn that is completing
	// now. Retiring here would send that final turn to a new generation
	// without the conversation it is meant to finish.
	var pending bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM automation_runs r
			WHERE r.org_id = @org_id AND r.target_id = @target_id
			  AND (r.dispatch_state IN ('waiting', 'executing') OR r.dispatch_state IS NULL)
			  AND r.status IN ('pending', 'running'))`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID}).Scan(&pending); err != nil {
		return "", fmt.Errorf("check for a remaining turn on a terminal target: %w", err)
	}
	if pending {
		return "", nil
	}
	return reason, nil
}

// GetSessionAutomationOwner returns the generation that owns the session,
// or nil when it is an ordinary session. Every human-entry path consults it
// before writing: an owned session's thread belongs to the automation's
// next turn (design doc 125, "Automation-owned sessions").
func (s *SessionStore) GetSessionAutomationOwner(ctx context.Context, orgID, sessionID uuid.UUID) (*models.SessionAutomationOwner, error) {
	var owner models.SessionAutomationOwner
	err := s.db.QueryRow(ctx, `
		SELECT g.id, t.automation_id, g.target_id, g.ownership_release_pending
		FROM sessions s
		JOIN automation_target_sessions g ON g.id = s.automation_owner_generation_id AND g.org_id = s.org_id
		JOIN automation_targets t ON t.id = g.target_id AND t.org_id = g.org_id
		WHERE s.id = @session_id AND s.org_id = @org_id`,
		pgx.NamedArgs{"session_id": sessionID, "org_id": orgID}).
		Scan(&owner.GenerationID, &owner.AutomationID, &owner.TargetID, &owner.ReleasePending)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session automation owner: %w", err)
	}
	owner.ResetURL = models.SessionAutomationResetURL(owner.AutomationID, owner.TargetID)
	return &owner, nil
}

// RejectIfAutomationOwned returns a *models.SessionAutomationOwnedError
// when the session is automation-owned, and nil otherwise. It is the guard
// every human-entry path shares.
func (s *SessionStore) RejectIfAutomationOwned(ctx context.Context, orgID, sessionID uuid.UUID) error {
	owner, err := s.GetSessionAutomationOwner(ctx, orgID, sessionID)
	if err != nil {
		return err
	}
	if owner == nil {
		return nil
	}
	return &models.SessionAutomationOwnedError{Owner: *owner}
}
