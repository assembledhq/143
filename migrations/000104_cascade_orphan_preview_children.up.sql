-- Backfill orphaned preview_services / preview_infrastructure rows whose parent
-- preview_instances row is already terminal. Without this, the frontend's
-- startup checklist (which reads child statuses to drive spinner state) keeps
-- spinning forever on previews where the parent flipped to stopped/failed/
-- expired but the per-child rows were never updated — e.g. when a rolling
-- deploy kills the API process between row reservation and the worker
-- launch RPC.
--
-- Mapping:
--   parent='failed'  -> still-trying children become 'failed' (carry the
--                       parent error if their own error is empty), already-
--                       ready/healthy children become 'stopped'.
--   parent='stopped' / 'expired' -> all non-terminal children become 'stopped'.

UPDATE preview_services ps
SET
    status = CASE
        WHEN ps.status = 'starting' AND pi.status = 'failed' THEN 'failed'
        WHEN ps.status = 'starting' THEN 'stopped'
        WHEN ps.status = 'ready' THEN 'stopped'
        ELSE ps.status
    END,
    error = CASE
        WHEN ps.status = 'starting' AND pi.status = 'failed'
             AND (ps.error IS NULL OR ps.error = '')
        THEN pi.error
        ELSE ps.error
    END
FROM preview_instances pi
WHERE ps.preview_instance_id = pi.id
  AND pi.status IN ('stopped', 'failed', 'expired')
  AND ps.status NOT IN ('stopped', 'failed');

UPDATE preview_infrastructure pinf
SET
    status = CASE
        WHEN pinf.status IN ('provisioning', 'unhealthy') AND pi.status = 'failed' THEN 'failed'
        WHEN pinf.status IN ('provisioning', 'unhealthy') THEN 'stopped'
        WHEN pinf.status = 'healthy' THEN 'stopped'
        ELSE pinf.status
    END,
    error = CASE
        WHEN pinf.status IN ('provisioning', 'unhealthy') AND pi.status = 'failed'
             AND (pinf.error IS NULL OR pinf.error = '')
        THEN pi.error
        ELSE pinf.error
    END
FROM preview_instances pi
WHERE pinf.preview_instance_id = pi.id
  AND pi.status IN ('stopped', 'failed', 'expired')
  AND pinf.status NOT IN ('stopped', 'failed');
