package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DeferPreparedReviewLoopStartInTx moves only an exact still-prepared owner's
// next recovery check. A concurrent created/refused state is a no-op. This is
// notification scheduling, never evidence that creation or a send aborted.
func DeferPreparedReviewLoopStartInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, expected ReviewLoopStartIntent) error {
	if ctx == nil || nilLifecycleDependency(tx) || expected.orgID != orgID || !expected.prepared.Scope.valid() {
		return ErrReviewLoopStartIdentity
	}
	_, err := tx.Exec(ctx, `UPDATE review_loop_start_intents SET due_at=statement_timestamp()+interval '5 seconds',updated_at=statement_timestamp()
 WHERE org_id=$1 AND session_id=$2 AND id=$3 AND envelope_digest=$4 AND state='prepared'`, orgID,
		expected.prepared.Scope.SessionID, expected.prepared.Scope.ID, expected.prepared.EnvelopeDigest)
	return err
}
