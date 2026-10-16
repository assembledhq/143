package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// An outcome must belong to the exact attempt runtime. Thread turn counters
// alone cannot identify completion: mid-turn queued messages can share the
// preceding turn's number. Legacy unphased final output is bounded by the
// attempt's creation time so that preceding responses cannot satisfy it.
const continuationRuntimeHasOutcomeSQL = `(
 EXISTS (
  SELECT 1 FROM session_activity_phases p
  WHERE p.org_id = r.org_id AND p.session_id = r.session_id AND p.thread_id = r.thread_id
   AND p.runtime_id = r.id AND p.status <> 'running'
   AND p.boundary_reason IN ('final_response','human_input','approval','plan_approval','cancelled','stopped')
 ) OR EXISTS (
  SELECT 1 FROM session_messages response
  WHERE response.org_id = r.org_id AND response.session_id = r.session_id
   AND response.thread_id = r.thread_id AND response.role = 'assistant'
   AND response.activity_phase_id IS NULL AND response.id > m.id
   AND response.created_at >= r.started_at
 )
)`

// Preserve every recorded identity, including malformed or foreign values:
// execution validates the entire set and fails rather than silently dropping
// unproved inputs. UNION deduplicates repeated owned retries without losing
// confirmed steering from any preceding attempt.
const continuationRecoveryMessageIDsSQL = `(
 SELECT COALESCE(jsonb_agg(ids.value ORDER BY ids.value),'[]'::jsonb) FROM (
  SELECT value FROM jsonb_array_elements(CASE
   WHEN j.payload->'continuation_task_message_ids' IS NULL THEN '[]'::jsonb
   WHEN jsonb_typeof(j.payload->'continuation_task_message_ids')='array' THEN j.payload->'continuation_task_message_ids'
   ELSE jsonb_build_array(j.payload->'continuation_task_message_ids') END)
  UNION
  SELECT value FROM jsonb_array_elements(CASE
   WHEN j.payload->'recovery_message_ids' IS NULL THEN '[]'::jsonb
   WHEN jsonb_typeof(j.payload->'recovery_message_ids')='array' THEN j.payload->'recovery_message_ids'
   ELSE jsonb_build_array(j.payload->'recovery_message_ids') END)
  UNION
  SELECT to_jsonb(input.message_id) FROM thread_inbox_entries input
  WHERE input.org_id=r.org_id AND input.session_id=r.session_id AND input.thread_id=r.thread_id
   AND input.runtime_id=r.id AND input.delivery_state='acked' AND input.applied_at IS NOT NULL
 ) ids
)`

// continuationRetryPayloadSQL is evaluated in the same lease-fenced write that
// requeues jobs (alias j). Only ordinary continuation attempts participate.
// A runtime created during that job lease and an admitted scoped user input
// establish the original instruction boundary; a later queued user cannot
// replace it. The marker retains that runtime identity for a fresh outcome
// check at execution time. Pre-admission retries leave their payload unchanged.
const continuationRetryPayloadSQL = `CASE
 WHEN (j.job_type = 'continue_session' OR (j.job_type = 'run_agent' AND COALESCE(j.payload->>'continuation_runtime_id','') <> ''))
  AND COALESCE(j.payload->>'execution_purpose','') IN ('','continue','checkpoint_retry','checkpoint_recovery')
  AND COALESCE(j.payload->>'command_type','') = ''
  AND COALESCE(j.payload->>'feedback_batch_id','') = ''
  AND COALESCE(j.payload->>'automation_run_id','') = ''
  AND COALESCE(j.payload->>'code_review_assessment_id','') = ''
  AND COALESCE(j.payload->>'human_input_request_id','') = ''
  AND COALESCE(j.payload->>'changeset_id','') = ''
  AND COALESCE(j.payload->>'post_success_action','') = ''
 THEN COALESCE((
  SELECT (j.payload - 'execution_purpose' - 'recovery_runtime_id')
   || jsonb_build_object('thread_id',r.thread_id::text,'queued_message_id',m.id::text)
   || jsonb_build_object('execution_purpose','checkpoint_recovery','recovery_runtime_id',r.id::text,'recovery_message_ids',` + continuationRecoveryMessageIDsSQL + `)
  FROM thread_runtimes r
  JOIN session_messages m ON m.org_id = r.org_id AND m.session_id = r.session_id AND m.role = 'user'
  JOIN thread_inbox_entries e ON e.org_id = r.org_id AND e.session_id = r.session_id
   AND e.thread_id = r.thread_id AND e.message_id = m.id
  WHERE r.org_id = j.org_id AND r.session_id::text = j.payload->>'session_id'
   AND r.started_at >= j.locked_at
   AND (r.id::text = NULLIF(j.payload->>'continuation_runtime_id','')
    OR (COALESCE(j.payload->>'continuation_runtime_id','') = '' AND COALESCE(j.payload->>'queued_message_id','') <> '' AND e.runtime_id = r.id))
   AND (m.id::text = NULLIF(j.payload->>'continuation_message_id','')
    OR COALESCE(j.payload->>'continuation_message_id','') = '')
   AND (r.thread_id::text = NULLIF(j.payload->>'thread_id','')
    OR (COALESCE(j.payload->>'thread_id','') = '' AND r.thread_id = (
     SELECT t.id FROM session_threads t
     WHERE t.org_id = j.org_id AND t.session_id = r.session_id AND t.archived_at IS NULL
     ORDER BY t.created_at,t.id LIMIT 1
    )))
   AND (m.thread_id = r.thread_id OR (m.thread_id IS NULL AND r.thread_id = (
    SELECT t.id FROM session_threads t
    WHERE t.org_id = j.org_id AND t.session_id = r.session_id AND t.archived_at IS NULL
    ORDER BY t.created_at,t.id LIMIT 1
   )))
   AND e.delivery_state = 'acked' AND e.applied_at IS NOT NULL
   AND (m.id::text = NULLIF(j.payload->>'queued_message_id','')
    OR (COALESCE(j.payload->>'queued_message_id','') = '' AND e.runtime_id = r.id AND m.created_at <= r.started_at))
  ORDER BY r.started_at DESC,r.id DESC,m.id DESC LIMIT 1
 ),CASE WHEN COALESCE(j.payload->>'continuation_runtime_id','') = '' AND COALESCE(j.payload->>'queued_message_id','') = '' AND EXISTS (
  SELECT 1 FROM thread_runtimes old
  JOIN thread_inbox_entries input ON input.org_id=old.org_id AND input.session_id=old.session_id AND input.thread_id=old.thread_id AND input.runtime_id=old.id
  WHERE old.org_id=j.org_id AND old.session_id::text=j.payload->>'session_id' AND old.started_at>=j.locked_at
   AND old.thread_id::text=j.payload->>'thread_id' AND input.delivery_state='acked' AND input.applied_at IS NOT NULL
 ) THEN j.payload || jsonb_build_object('execution_purpose','checkpoint_recovery','recovery_unproven',true) ELSE j.payload END)
 ELSE j.payload END`

// InspectContinuationRecovery checks the exact attempt named by a trusted
// recovery marker. Missing ownership proof is an error. A provider response is
// distinct from the subsequent session/thread lifecycle writes.
func (s *ThreadInboxStore) InspectContinuationRecovery(ctx context.Context, orgID, sessionID, threadID, runtimeID uuid.UUID, messageID int64) (bool, bool, error) {
	var completed, settled bool
	err := s.db.QueryRow(ctx, `SELECT `+continuationRuntimeHasOutcomeSQL+`, EXISTS (
   SELECT 1 FROM session_activity_phases p
   JOIN session_threads t ON t.org_id=p.org_id AND t.session_id=p.session_id AND t.id=p.thread_id
   JOIN sessions session ON session.org_id=p.org_id AND session.id=p.session_id
   WHERE p.org_id=r.org_id AND p.session_id=r.session_id AND p.thread_id=r.thread_id AND p.runtime_id=r.id
    AND p.status <> 'running' AND p.boundary_reason IN ('final_response','human_input','approval','plan_approval','cancelled','stopped')
    AND t.status NOT IN ('pending','running')
    AND t.current_turn >= p.turn_number AND t.last_activity_at >= p.completed_at
  )
  FROM thread_runtimes r
  JOIN session_messages m ON m.org_id = r.org_id AND m.session_id = r.session_id AND m.id = $5 AND m.role = 'user'
  JOIN thread_inbox_entries e ON e.org_id = r.org_id AND e.session_id = r.session_id AND e.thread_id = r.thread_id AND e.message_id = m.id
  WHERE r.org_id = $1 AND r.session_id = $2 AND r.thread_id = $3 AND r.id = $4
   AND e.delivery_state = 'acked' AND e.applied_at IS NOT NULL`, orgID, sessionID, threadID, runtimeID, messageID).Scan(&completed, &settled)
	if err != nil {
		return false, false, fmt.Errorf("verify continuation recovery receipt: %w", err)
	}
	return completed, settled, nil
}

// RecordContinuationAttemptWithLease binds the original instruction and exact
// runtime before provider admission. Both ownership generations are checked;
// a fenced record must stop execution instead of creating an untracked attempt.
func (s *JobStore) RecordContinuationAttemptWithLease(ctx context.Context, orgID, jobID, jobToken, sessionID, threadID, runtimeID, runtimeToken uuid.UUID, messageID int64, taskMessageIDs []int64) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE jobs j
  SET payload = j.payload || jsonb_build_object('continuation_runtime_id',$6::uuid::text,'continuation_message_id',$8::bigint::text,'queued_message_id',$8::bigint::text,'thread_id',$5::uuid::text,'continuation_task_message_ids',$9::bigint[]),updated_at=now()
  WHERE j.org_id=$1 AND j.id=$2 AND j.lock_token=$3 AND j.status='running' AND j.job_type IN ('continue_session','run_agent')
   AND j.payload->>'session_id'=$4::uuid::text AND $8=ANY($9::bigint[])
   AND (COALESCE(j.payload->>'thread_id','')='' OR j.payload->>'thread_id'=$5::uuid::text)
   AND (COALESCE(j.payload->>'queued_message_id','')='' OR j.payload->>'queued_message_id'=$8::bigint::text)
   AND EXISTS (
    SELECT 1 FROM thread_runtimes r
    WHERE r.org_id=$1 AND r.session_id=$4 AND r.thread_id=$5 AND r.id=$6 AND r.lease_token=$7
     AND r.status IN ('starting','live','paused') AND r.lease_expires_at > clock_timestamp()
   )
   AND EXISTS (
    SELECT 1 FROM session_messages m
    WHERE m.org_id=$1 AND m.session_id=$4 AND m.id=$8 AND m.role='user'
     AND (m.thread_id=$5 OR (m.thread_id IS NULL AND $5=(
      SELECT t.id FROM session_threads t
      WHERE t.org_id=$1 AND t.session_id=$4 AND t.archived_at IS NULL
      ORDER BY t.created_at,t.id LIMIT 1
     )))
   ) AND (SELECT count(*) FROM session_messages task
    WHERE task.org_id=$1 AND task.session_id=$4 AND task.id=ANY($9::bigint[]) AND task.role='user'
     AND (task.thread_id=$5 OR (task.thread_id IS NULL AND $5=(
      SELECT t.id FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$4 AND t.archived_at IS NULL
      ORDER BY t.created_at,t.id LIMIT 1
     )))
   ) = (SELECT count(DISTINCT requested) FROM unnest($9::bigint[]) requested)
`, orgID, jobID, jobToken, sessionID, threadID, runtimeID, runtimeToken, messageID, taskMessageIDs)
	if err != nil {
		return false, fmt.Errorf("record continuation attempt: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func continuationRecoveryQuery(query string) string {
	return strings.Replace(query, "{{continuation_retry_payload}}", continuationRetryPayloadSQL, 1)
}

// ListAppliedRecoveryMessageIDs validates every preserved task identity. With
// no preserved set, an explicit task retry recovers only the exact original
// input's admitted runtime (including its confirmed steering), not older
// unrelated applied conversation history.
func (s *ThreadInboxStore) ListAppliedRecoveryMessageIDs(ctx context.Context, orgID, sessionID, threadID uuid.UUID, messageID int64, requested []int64) ([]int64, error) {
	if requested == nil {
		requested = []int64{}
	}
	rows, err := s.db.Query(ctx, `WITH original AS (
  SELECT runtime_id FROM thread_inbox_entries
  WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND message_id=$4
   AND delivery_state='acked' AND applied_at IS NOT NULL
 )
 SELECT e.message_id FROM thread_inbox_entries e
 JOIN session_messages m ON m.org_id=e.org_id AND m.session_id=e.session_id AND m.id=e.message_id AND m.role='user'
 WHERE e.org_id=$1 AND e.session_id=$2 AND e.thread_id=$3 AND e.delivery_state='acked' AND e.applied_at IS NOT NULL
  AND (e.message_id=ANY($5::bigint[]) OR (cardinality($5::bigint[])=0 AND e.runtime_id=(SELECT runtime_id FROM original)))
  AND (m.thread_id=$3 OR (m.thread_id IS NULL AND $3=(
   SELECT t.id FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2 AND t.archived_at IS NULL ORDER BY t.created_at,t.id LIMIT 1
  )))
 ORDER BY e.message_id`, orgID, sessionID, threadID, messageID, requested)
	if err != nil {
		return nil, fmt.Errorf("read recovery task input: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("collect recovery task input: %w", err)
	}
	expected := make(map[int64]bool, len(requested))
	for _, id := range requested {
		expected[id] = true
	}
	foundOriginal := false
	for _, id := range ids {
		if id == messageID {
			foundOriginal = true
		}
	}
	if !foundOriginal || len(ids) == 0 || (len(expected) > 0 && len(ids) != len(expected)) {
		return nil, fmt.Errorf("recovery task input lacks complete applied scope proof: %w", ErrInboxBatchConflict)
	}
	return ids, nil
}
