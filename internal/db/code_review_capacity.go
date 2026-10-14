package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CodeReviewSandboxAvailable serializes review admission across tenants. The
// caller must create its session/assessment in this same transaction: that row
// is the durable reservation seen by the next admission. Local Docker admission
// remains authoritative for unrelated workloads and heartbeat races.
//
// Only aggregate cross-tenant occupancy is read; a warm-session exception is
// scoped to the caller's organization. No tenant data leaves this method.
func CodeReviewSandboxAvailable(ctx context.Context, tx pgx.Tx, orgID, warmSessionID uuid.UUID) (bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('code-review-sandbox-admission',0))`); err != nil {
		return false, fmt.Errorf("lock review sandbox admission: %w", err)
	}
	// Keep warm ownership stable through the caller's assessment reservation.
	// Review callers already hold the PR lock shared with every GC path.
	if warmSessionID != uuid.Nil {
		if _, err := tx.Exec(ctx, `SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE`, orgID, warmSessionID); err != nil {
			return false, fmt.Errorf("lock warm review workspace: %w", err)
		}
	}
	var available bool
	err := tx.QueryRow(ctx, `
WITH counts AS (
 SELECT n.*,NULLIF(metadata->>'sandbox_host_id','') AS sandbox_host_id,
   CASE WHEN metadata->>'max_active_sandboxes' ~ '^[0-9]{1,9}$' THEN (metadata->>'max_active_sandboxes')::bigint END AS capacity,
   CASE WHEN metadata->>'live_sandbox_count' ~ '^[0-9]{1,9}$' THEN (metadata->>'live_sandbox_count')::bigint END AS live,
   CASE WHEN metadata->>'reserved_sandbox_count' ~ '^[0-9]{1,9}$' THEN (metadata->>'reserved_sandbox_count')::bigint END AS reserved
 FROM nodes n WHERE mode IN ('worker','all')
), latest AS (
 SELECT DISTINCT ON (sandbox_host_id) * FROM counts WHERE sandbox_host_id IS NOT NULL
   AND status IN ('active','draining') AND last_heartbeat_at>=clock_timestamp()-interval '90 seconds'
 ORDER BY sandbox_host_id,started_at DESC,id DESC
), samples AS (
 SELECT sandbox_host_id,MAX(live) AS live,SUM(reserved) AS reserved FROM counts
 WHERE status IN ('active','draining') AND last_heartbeat_at >= clock_timestamp()-interval '90 seconds'
   AND COALESCE(metadata->>'live_sandbox_count_error','')='' AND live IS NOT NULL AND reserved IS NOT NULL
 GROUP BY sandbox_host_id
), missing_samples AS (
 SELECT n.sandbox_host_id,n.metadata->'missing_sandbox_references' AS references
 FROM counts n
 WHERE n.sandbox_host_id IS NOT NULL AND n.status IN ('active','draining')
   AND n.last_heartbeat_at>=clock_timestamp()-interval '90 seconds'
   AND COALESCE(n.metadata->>'live_sandbox_count_error','')=''
   AND n.live IS NOT NULL AND n.reserved IS NOT NULL
   AND COALESCE(n.metadata->>'missing_sandbox_reference_error','')=''
   AND jsonb_typeof(n.metadata->'missing_sandbox_references')='array'
   AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(CASE
     WHEN jsonb_typeof(n.metadata->'missing_sandbox_references')='array' THEN n.metadata->'missing_sandbox_references' ELSE '[]'::jsonb END) p
     WHERE jsonb_typeof(p)<>'object'
       OR jsonb_typeof(p->'container_id') IS DISTINCT FROM 'string'
       OR COALESCE(p->>'container_id','') !~ '^[a-f0-9]{64}$'
       OR jsonb_typeof(p->'owner_node_id') IS DISTINCT FROM 'string'
       OR COALESCE(p->>'owner_node_id','')=''
       OR p->>'owner_node_id'<>btrim(p->>'owner_node_id')
       OR CASE WHEN jsonb_typeof(p)='object' THEN p-'container_id'-'owner_node_id' ELSE '{}'::jsonb END<>'{}'::jsonb)
), missing AS (
 SELECT DISTINCT m.sandbox_host_id,p->>'container_id' AS container_id,p->>'owner_node_id' AS owner_node_id
 FROM missing_samples m CROSS JOIN LATERAL jsonb_array_elements(m.references) p
), verified AS (
 SELECT n.id,n.sandbox_host_id,n.capacity,
   GREATEST(samples.live,
     (SELECT COUNT(DISTINCT s.container_id) FROM sessions s JOIN counts owner ON owner.id=s.worker_node_id
      WHERE owner.sandbox_host_id=n.sandbox_host_id AND s.container_id IS NOT NULL
        AND NOT EXISTS(SELECT 1 FROM missing p WHERE p.sandbox_host_id=owner.sandbox_host_id
          AND p.container_id=s.container_id AND p.owner_node_id=s.worker_node_id)))+samples.reserved AS occupied
 FROM latest n JOIN samples USING(sandbox_host_id)
 WHERE n.status='active' AND n.drain_intent='none'
   AND n.last_heartbeat_at >= clock_timestamp()-interval '90 seconds'
   AND COALESCE(n.metadata->>'live_sandbox_count_error','')=''
   AND n.capacity>0 AND n.live IS NOT NULL AND n.reserved IS NOT NULL
   AND NOT EXISTS(SELECT 1 FROM counts generation
     WHERE generation.sandbox_host_id=n.sandbox_host_id AND generation.status IN ('active','draining')
       AND generation.last_heartbeat_at>=clock_timestamp()-interval '90 seconds'
       AND (COALESCE(generation.metadata->>'live_sandbox_count_error','')<>'' OR generation.live IS NULL OR generation.reserved IS NULL))
), runnable AS (
 SELECT * FROM jobs WHERE status='running' OR (status='pending' AND isfinite(run_at))
), commitments AS (
 SELECT m.org_id,m.session_id FROM code_review_session_metadata m
 WHERE m.status IN ('queued','running')
   AND EXISTS(SELECT 1 FROM runnable j WHERE j.org_id=m.org_id AND j.payload->>'session_id'=m.session_id::text
     AND (j.job_type IN ('prepare_code_review_workspace','run_agent','continue_session','fork_session_thread','deliver_thread_inbox')
       OR (j.job_type='run_code_review' AND j.payload->>'review_output_key'=m.review_output_key)))
   AND NOT EXISTS(SELECT 1 FROM code_review_revision_assessments a WHERE a.org_id=m.org_id AND a.session_id=m.session_id
                  AND (a.status='publishing' OR a.result_origin IS NOT NULL))
 UNION
 SELECT a.org_id,a.session_id FROM code_review_revision_assessments a
 WHERE a.status IN ('reserved','running') AND a.result_origin IS NULL
   AND (EXISTS(SELECT 1 FROM runnable j WHERE j.org_id=a.org_id AND j.job_type='run_code_review_recheck' AND j.payload->>'assessment_id'=a.id::text)
     OR EXISTS(SELECT 1 FROM code_review_recheck_dispatches d JOIN runnable j ON j.org_id=d.org_id AND j.id=d.job_id
               WHERE d.org_id=a.org_id AND d.assessment_id=a.id AND d.status IN ('pending','running')))
), cold AS (
 SELECT COUNT(*) AS count FROM commitments c JOIN sessions s ON s.org_id=c.org_id AND s.id=c.session_id
 WHERE s.container_id IS NULL OR NOT EXISTS(SELECT 1 FROM counts owner JOIN verified n USING(sandbox_host_id) WHERE owner.id=s.worker_node_id)
   OR EXISTS(SELECT 1 FROM missing p JOIN counts owner ON owner.id=s.worker_node_id
     WHERE p.sandbox_host_id=owner.sandbox_host_id AND p.container_id=s.container_id AND p.owner_node_id=s.worker_node_id)
)
SELECT EXISTS(SELECT 1 FROM verified WHERE capacity>0)
 AND NOT EXISTS(SELECT 1 FROM counts WHERE status IN ('active','draining')
                AND last_heartbeat_at >= clock_timestamp()-interval '90 seconds' AND sandbox_host_id IS NULL)
 AND (EXISTS(SELECT 1 FROM sessions s JOIN counts owner ON owner.id=s.worker_node_id JOIN verified n USING(sandbox_host_id)
              WHERE s.org_id=$1 AND s.id=$2 AND s.container_id IS NOT NULL
                AND NOT EXISTS(SELECT 1 FROM missing p WHERE p.sandbox_host_id=n.sandbox_host_id
                  AND p.container_id=s.container_id AND p.owner_node_id=s.worker_node_id))
      OR (SELECT COALESCE(SUM(GREATEST(capacity-occupied,0)),0) FROM verified) > (SELECT count FROM cold))`, orgID, warmSessionID).Scan(&available)
	if err != nil {
		return false, fmt.Errorf("read review sandbox admission: %w", err)
	}
	return available, nil
}
