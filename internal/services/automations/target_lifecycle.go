package automations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
)

// TargetLifecycle applies a pull request's own lifecycle to its per-target
// automation targets (design doc 125, "Lifecycle Transitions"). PRService
// calls it from the branches that record a pull request closed, merged, or
// reopened, whether or not any automation subscribes to that event: a
// target must close even when its automation would never have been
// triggered by the close, or its waiters would sit until the wait timeout
// and its session would stay owned.
type TargetLifecycle struct {
	pool      db.TxStarter
	targets   *db.AutomationTargetStore
	completer *TurnCompleter
	logger    zerolog.Logger
	now       func() time.Time
}

// NewTargetLifecycle wires the notification over one pool. The completer
// supplies the wake outbox's job, so a freed target is picked up at once.
func NewTargetLifecycle(pool db.TxStarter, targets *db.AutomationTargetStore, completer *TurnCompleter, logger zerolog.Logger) *TargetLifecycle {
	return &TargetLifecycle{pool: pool, targets: targets, completer: completer, logger: logger, now: time.Now}
}

// OnPullRequestClosed records the pull request's terminal state on every
// target for it. Waiting runs are skipped as pr_closed, except a subscribed
// merged run on a merged pull request, which keeps its final turn; the
// generation is retired unless that turn is still to come, in which case
// its completion retires it. An executing turn is never interrupted.
func (l *TargetLifecycle) OnPullRequestClosed(ctx context.Context, orgID uuid.UUID, repoFullName string, number int, merged bool, observedAt time.Time) error {
	return l.forEachTarget(ctx, orgID, repoFullName, number, "closed", func(ctx context.Context, targetID uuid.UUID) error {
		tx, err := l.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		outcome, err := l.targets.ApplyPullRequestClosed(ctx, tx, orgID, targetID, merged, observedAt)
		if err != nil {
			return err
		}
		if outcome.Stale {
			l.logger.Info().
				Str("org_id", orgID.String()).
				Str("target_id", targetID.String()).
				Msg("dropped a pull request close that describes the target before its current state")
			return nil
		}
		if err := l.completer.EnqueueWakeJob(ctx, tx, orgID, targetID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		l.logger.Info().
			Str("org_id", orgID.String()).
			Str("target_id", targetID.String()).
			Bool("merged", merged).
			Int64("skipped_runs", outcome.SkippedRuns).
			Bool("generation_retired", outcome.GenerationRetired).
			Bool("final_turn_pending", outcome.FinalTurnPending).
			Msg("applied a closed pull request to its automation target")
		return nil
	})
}

// OnPullRequestReopened puts every target for the pull request back to
// open. The retired generation stays retired: the next trigger starts a new
// one, because the reopened pull request's history may have moved on.
func (l *TargetLifecycle) OnPullRequestReopened(ctx context.Context, orgID uuid.UUID, repoFullName string, number int, observedAt time.Time) error {
	return l.forEachTarget(ctx, orgID, repoFullName, number, "reopened", func(ctx context.Context, targetID uuid.UUID) error {
		tx, err := l.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		reopened, err := l.targets.ReopenTarget(ctx, tx, orgID, targetID, observedAt)
		if err != nil {
			return err
		}
		// Committed whether or not the state changed: an already-open target
		// still records this observation, and that recorded moment is what
		// keeps a close delivered after it, but timestamped before it, from
		// looking fresh and retiring a generation on an open pull request.
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if !reopened {
			return nil
		}
		l.logger.Info().
			Str("org_id", orgID.String()).
			Str("target_id", targetID.String()).
			Msg("reopened an automation target with its pull request")
		return nil
	})
}

// forEachTarget applies fn to each of the pull request's targets, one
// transaction each, and joins their errors: one target's failure does not
// hide another's work. A missing target is not an error, because targets
// exist only for automations that ran on this pull request.
func (l *TargetLifecycle) forEachTarget(ctx context.Context, orgID uuid.UUID, repoFullName string, number int, action string, fn func(context.Context, uuid.UUID) error) error {
	if l == nil || l.targets == nil {
		return nil
	}
	targetIDs, err := l.targets.ListTargetsForPullRequest(ctx, orgID, repoFullName, number)
	if err != nil {
		return fmt.Errorf("list automation targets for a %s pull request: %w", action, err)
	}
	var errs []error
	for _, targetID := range targetIDs {
		if err := fn(ctx, targetID); err != nil {
			if errors.Is(err, db.ErrAutomationTargetNotFound) {
				continue
			}
			errs = append(errs, fmt.Errorf("apply %s to automation target %s: %w", action, targetID, err))
		}
	}
	return errors.Join(errs...)
}
