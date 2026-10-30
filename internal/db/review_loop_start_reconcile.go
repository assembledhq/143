package db

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReviewLoopStartCandidate carries only stable scoped identities from the
// bounded due read, never raw prompts.
type ReviewLoopStartCandidate struct {
	ID        uuid.UUID `db:"id"`
	SessionID uuid.UUID `db:"session_id"`
}

// ListDueReviewLoopStartsInTx selects each owner category independently. A
// blocked prepared owner cannot consume every slot needed by created startups.
// Each branch returns at most limit identities; the combined bound is 2*limit.
func ListDueReviewLoopStartsInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, limit int) ([]ReviewLoopStartCandidate, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil {
		return nil, ErrReviewLoopStartIdentity
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := tx.Query(ctx, `WITH prepared AS MATERIALIZED (
 SELECT id,session_id,due_at FROM review_loop_start_intents WHERE org_id=$1 AND due_at IS NOT NULL AND due_at<=statement_timestamp()
 ORDER BY due_at,id LIMIT $2
 ), startup AS MATERIALIZED (
 SELECT id,session_id,startup_send_due_at AS due_at FROM session_review_loops WHERE org_id=$1 AND startup_send_due_at IS NOT NULL AND startup_send_due_at<=statement_timestamp()
 ORDER BY startup_send_due_at,id LIMIT $2
 ) SELECT id,session_id FROM (SELECT * FROM prepared UNION ALL SELECT * FROM startup) candidates ORDER BY due_at,id`, orgID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ReviewLoopStartCandidate])
}
