DROP INDEX IF EXISTS idx_worker_deploy_events_node_created;
DROP INDEX IF EXISTS idx_worker_deploy_events_deploy_created;
DROP TABLE IF EXISTS worker_deploy_events;

DROP INDEX IF EXISTS idx_session_executors_host_active_deadline;
ALTER TABLE session_executors
    DROP CONSTRAINT IF EXISTS chk_session_executors_drain_intent,
    DROP COLUMN IF EXISTS drain_deadline_at,
    DROP COLUMN IF EXISTS drain_requested_at,
    DROP COLUMN IF EXISTS drain_intent,
    DROP COLUMN IF EXISTS runtime_deadline_at;

DROP INDEX IF EXISTS idx_nodes_worker_drain;
ALTER TABLE nodes
    DROP CONSTRAINT IF EXISTS chk_nodes_drain_intent,
    DROP COLUMN IF EXISTS drain_reason,
    DROP COLUMN IF EXISTS drain_requested_by,
    DROP COLUMN IF EXISTS drain_budget_expires_at,
    DROP COLUMN IF EXISTS drain_requested_at,
    DROP COLUMN IF EXISTS drain_intent;
