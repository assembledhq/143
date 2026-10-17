package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ListUnsettledAppliedMessageIDs identifies admitted trailing input whose owner
// ended without a provider outcome. These messages are prior task context for a
// new follow-up, never new seeds. Later provider outcomes bound the context;
// unphased startup notes cannot hide unfinished input.
func (s *ThreadInboxStore) ListUnsettledAppliedMessageIDs(ctx context.Context, orgID, sessionID, threadID uuid.UUID, requested []int64) ([]int64, error) {
	if len(requested) == 0 {
		return []int64{}, nil
	}
	rows, err := s.db.Query(ctx, `SELECT e.message_id
 FROM thread_inbox_entries e
 JOIN thread_runtimes r ON r.org_id=e.org_id AND r.session_id=e.session_id AND r.thread_id=e.thread_id AND r.id=e.runtime_id
 JOIN session_messages m ON m.org_id=e.org_id AND m.session_id=e.session_id AND m.id=e.message_id AND m.role='user'
 WHERE e.org_id=$1 AND e.session_id=$2 AND e.thread_id=$3 AND e.message_id=ANY($4::bigint[])
  AND e.delivery_state='acked' AND e.applied_at IS NOT NULL
  AND (m.thread_id=$3 OR (m.thread_id IS NULL AND $3=(
   SELECT t.id FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2 AND t.archived_at IS NULL ORDER BY t.created_at,t.id LIMIT 1
  )))
  AND (r.status IN ('closed','failed','lost') OR r.lease_expires_at <= clock_timestamp())
  AND NOT `+continuationRuntimeHasOutcomeSQL+`
  AND NOT EXISTS (
   SELECT 1 FROM session_activity_phases later
   WHERE later.org_id=e.org_id AND later.session_id=e.session_id AND later.thread_id=e.thread_id
    AND later.status <> 'running' AND later.completed_at >= e.applied_at
    AND later.boundary_reason IN ('final_response','human_input','approval','plan_approval','cancelled','stopped')
  )
 ORDER BY e.message_id`, orgID, sessionID, threadID, requested)
	if err != nil {
		return nil, fmt.Errorf("read unfinished continuation task context: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("collect unfinished continuation task context: %w", err)
	}
	return ids, nil
}
