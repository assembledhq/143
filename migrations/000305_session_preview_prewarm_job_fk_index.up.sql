-- Job retention's existing SET NULL FK action keeps prewarm-run history but
-- must find the optional job link without scanning every retained run.
-- Standalone CONCURRENTLY avoids blocking writes during the build. An invalid
-- interrupted index must be removed before repairing/retrying this migration.
CREATE INDEX CONCURRENTLY idx_session_preview_prewarm_runs_retention_job
    ON session_preview_prewarm_runs (job_id)
    WHERE job_id IS NOT NULL;
