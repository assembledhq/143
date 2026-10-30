package db

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Prelock ordinary loop writers before any pass/loop DML. Discovery is
// unlocked; subsequent statements acquire caller -> start -> loop and recheck
// the linkage. This is also needed by legacy loops whose unique running slot
// can otherwise deadlock with T1 while holding loop -> publication.
func prelockReviewLoopWriter(ctx context.Context, orgID, loopID uuid.UUID, tx pgx.Tx) error {
	var scope ReviewLoopStartScope
	var retained bool
	scope.ID = loopID
	err := tx.QueryRow(ctx, `SELECT session_id,source,automation_run_id,changeset_id,startup_send_version IS NOT NULL
 FROM session_review_loops WHERE org_id=$1 AND id=$2`, orgID, loopID).Scan(&scope.SessionID, &scope.Source, &scope.AutomationRunID, &scope.ChangesetID, &retained)
	if err != nil {
		return err
	}
	current, err := lockReviewLoopStartCaller(ctx, orgID, tx, scope)
	if err != nil && (retained || !errors.Is(err, pgx.ErrNoRows)) {
		return err
	}
	if retained && scope.Source != models.ReviewLoopSourceManual && (current == nil || *current != loopID) {
		return ErrReviewLoopStartCaller
	}
	startID := loopID
	if current != nil {
		startID = *current
	}
	var locked uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR UPDATE`, orgID, scope.SessionID, startID).Scan(&locked)
	if err != nil && (!errors.Is(err, pgx.ErrNoRows) || retained || current != nil) {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3
 AND source=$4 AND automation_run_id IS NOT DISTINCT FROM $5::uuid AND changeset_id IS NOT DISTINCT FROM $6::uuid FOR UPDATE`,
		orgID, scope.SessionID, loopID, scope.Source, scope.AutomationRunID, scope.ChangesetID).Scan(&locked)
	if err != nil {
		return err
	}
	if retained && scope.Source == models.ReviewLoopSourcePublication {
		var matches bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_publications WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3
 AND review_start_id=$4 AND (review_loop_id IS NULL OR review_loop_id=$4))`, orgID, scope.SessionID, scope.ChangesetID, loopID).Scan(&matches)
		if err != nil {
			return err
		}
		if !matches {
			return ErrReviewLoopStartCaller
		}
	}
	return nil
}
