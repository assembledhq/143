DROP INDEX IF EXISTS idx_worker_image_retention_active;
DROP TABLE IF EXISTS worker_image_retention;

DROP INDEX IF EXISTS idx_deploy_drain_extensions_node_active;
DROP INDEX IF EXISTS idx_deploy_drain_extensions_active_session;
DROP TABLE IF EXISTS deploy_drain_extensions;

DROP INDEX IF EXISTS idx_worker_deploy_wave_hosts_wave_status;
DROP TABLE IF EXISTS worker_deploy_wave_hosts;

DROP INDEX IF EXISTS idx_worker_deploy_waves_status_created;
DROP TABLE IF EXISTS worker_deploy_waves;

ALTER TABLE preview_runtimes
    DROP CONSTRAINT IF EXISTS chk_preview_runtimes_unavailable_reason,
    DROP COLUMN IF EXISTS unavailable_reason;

ALTER TABLE preview_instances
    DROP CONSTRAINT IF EXISTS chk_preview_instances_unavailable_reason,
    DROP COLUMN IF EXISTS unavailable_reason;

DROP INDEX IF EXISTS idx_session_threads_recovery_state;

ALTER TABLE session_threads
    DROP CONSTRAINT IF EXISTS chk_session_threads_recovery_state,
    DROP CONSTRAINT IF EXISTS chk_session_threads_runtime_stop_reason,
    DROP COLUMN IF EXISTS recovery_event_history,
    DROP COLUMN IF EXISTS recovery_reason,
    DROP COLUMN IF EXISTS recovery_state,
    DROP COLUMN IF EXISTS runtime_graceful_stop_at,
    DROP COLUMN IF EXISTS runtime_stop_reason;
