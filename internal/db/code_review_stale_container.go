package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FinalizeStaleTerminalCodeReviewContainer repairs abandoned turn/recovery
// flags only after durable terminal state and a quiet period prove them stale.
// Container identity and all execution/publication/retention owners are fenced
// in the same update. A true result transfers physical destruction to host GC.
func (s *SessionStore) FinalizeStaleTerminalCodeReviewContainer(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (bool, error) {
	return s.finalizeCodeReviewContainer(ctx, orgID, sessionID, expectedContainerID, true)
}

func (s *SessionStore) finalizeCodeReviewContainer(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string, stale bool) (bool, error) {
	starter, ok := s.db.(TxStarter)
	if !ok {
		return false, fmt.Errorf("stale review ownership reconciliation requires a transaction")
	}
	cleared := false
	err := pgx.BeginFunc(ctx, starter, func(tx pgx.Tx) error {
		var prID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT pull_request_id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2`, orgID, sessionID).Scan(&prID); err != nil {
			if err == pgx.ErrNoRows {
				return nil
			}
			return err
		}
		// Match review/recheck admission's PR lock before locking the session.
		// A fresh statement after this lock sees any successor admitted first.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+orgID.String()+":"+prID.String()); err != nil {
			return err
		}
		var lockedID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM sessions WHERE org_id=$1 AND id=$2 AND container_id=$3 FOR UPDATE`, orgID, sessionID, expectedContainerID).Scan(&lockedID); err != nil {
			if err == pgx.ErrNoRows {
				return nil
			}
			return err
		}
		// Publication retries carry session_id but do not use its sandbox.
		// Fence sandbox work only; a controller must still match the review's
		// output identity. Assessments and dispatches retain publication owners.
		// A reserved/running evidence assessment without any dispatch cannot yet
		// use this sandbox: its first dispatch requires a cleared container_id.
		// Let GC clear the predecessor workspace before that dispatch. The PR
		// lock serializes this proof with admission and dispatch; any dispatch
		// history or publication ownership still keeps the assessment fenced.
		tag, err := tx.Exec(ctx, `UPDATE sessions s
 SET container_id=NULL,worker_node_id=NULL,turn_holding_container=false,
     recovery_state='',recovery_queued_at=NULL,
     sandbox_state=CASE WHEN COALESCE(snapshot_key,'')='' THEN 'none' ELSE 'snapshotted' END
 WHERE s.org_id=$1 AND s.id=$2 AND s.container_id=$3 AND s.origin='code_review'
   AND EXISTS(SELECT 1 FROM code_review_session_metadata m WHERE m.org_id=s.org_id AND m.session_id=s.id)
   AND ((NOT $4 AND NOT s.turn_holding_container AND COALESCE(s.recovery_state,'') NOT IN ('queued','recovering'))
     OR ($4 AND s.status IN ('idle','completed','failed','cancelled')
       AND COALESCE(s.recovery_state,'') IN ('','queued','unavailable')
       AND GREATEST(COALESCE(s.last_activity_at,s.created_at),COALESCE(s.recovery_queued_at,s.created_at)) < clock_timestamp()-interval '10 minutes'
       AND EXISTS(SELECT 1 FROM code_review_session_metadata m WHERE m.org_id=s.org_id AND m.session_id=s.id
         AND m.status IN ('completed','failed','cancelled','stale')
         AND GREATEST(m.created_at,COALESCE(m.completed_at,m.created_at)) < clock_timestamp()-interval '10 minutes')))
   AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=s.org_id AND j.payload->>'session_id'=s.id::text AND j.status IN ('pending','running')
     AND (j.job_type IN ('prepare_code_review_workspace','run_agent','continue_session','deliver_thread_inbox')
       OR (j.job_type='run_code_review' AND EXISTS(SELECT 1 FROM code_review_session_metadata m
         WHERE m.org_id=s.org_id AND m.session_id=s.id AND m.review_output_key=j.payload->>'review_output_key'))))
   AND NOT EXISTS(SELECT 1 FROM session_threads t WHERE t.org_id=s.org_id AND t.session_id=s.id AND t.status IN ('pending','running','awaiting_input'))
   AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining'))
   AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
   AND NOT EXISTS(SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container)
   AND NOT EXISTS(SELECT 1 FROM session_sandbox_holders h WHERE h.org_id=s.org_id AND h.session_id=s.id AND h.status IN ('active','draining') AND h.expires_at>clock_timestamp())
   AND NOT EXISTS(SELECT 1 FROM code_review_revision_assessments a WHERE a.org_id=s.org_id AND a.session_id=s.id
                  AND (a.status='publishing' OR a.publication_state='uncertain'
                    OR (a.status IN ('reserved','running')
                      AND (a.review_scope IS DISTINCT FROM 'evidence_only'
                        OR a.publication_state IS DISTINCT FROM 'not_started'
                        OR EXISTS(SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=a.org_id AND d.assessment_id=a.id)))))
   AND NOT EXISTS(SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=s.org_id AND d.session_id=s.id AND d.status IN ('pending','running'))`, orgID, sessionID, expectedContainerID, stale)
		if err != nil {
			return err
		}
		cleared = tag.RowsAffected() == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("finalize stale terminal review container: %w", err)
	}
	return cleared, nil
}
