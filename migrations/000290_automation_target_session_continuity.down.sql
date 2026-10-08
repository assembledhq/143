DROP INDEX IF EXISTS idx_session_messages_automation_run;
ALTER TABLE session_messages DROP COLUMN automation_run_id;

DROP TABLE automation_run_results;

DROP INDEX IF EXISTS idx_automation_runs_waiting_target;
DROP INDEX IF EXISTS idx_automation_runs_session;
DROP INDEX IF EXISTS idx_automation_runs_one_executing_per_target;

ALTER TABLE automation_runs
    DROP CONSTRAINT chk_automation_runs_executing,
    DROP CONSTRAINT chk_automation_runs_outcome_reason,
    DROP CONSTRAINT chk_automation_runs_wait_reason,
    DROP CONSTRAINT chk_automation_runs_dispatch_state,
    DROP CONSTRAINT chk_automation_runs_continuation_reason,
    DROP CONSTRAINT chk_automation_runs_continuation_mode,
    DROP CONSTRAINT chk_automation_runs_head_resolution,
    DROP COLUMN turn_duration_ms,
    DROP COLUMN restore_duration_ms,
    DROP COLUMN restore_snapshot_bytes,
    DROP COLUMN worker_node_id,
    DROP COLUMN head_lookup_degraded,
    DROP COLUMN outcome_reason,
    DROP COLUMN superseded_by_run_id,
    DROP COLUMN attempt_started_at,
    DROP COLUMN attempt_lock_token,
    DROP COLUMN attempt,
    DROP COLUMN job_id,
    DROP COLUMN execution_started_at,
    DROP COLUMN wait_started_at,
    DROP COLUMN wait_reason,
    DROP COLUMN dispatch_state,
    DROP COLUMN base_sha,
    DROP COLUMN previous_head_sha,
    DROP COLUMN native_context,
    DROP COLUMN continuation_reason,
    DROP COLUMN continuation_mode,
    DROP COLUMN head_resolution,
    DROP COLUMN head_epoch,
    DROP COLUMN pull_request_updated_at,
    DROP COLUMN github_action,
    DROP COLUMN turn_number,
    DROP COLUMN thread_id,
    DROP COLUMN session_id,
    DROP COLUMN target_generation,
    DROP COLUMN target_id;

DROP INDEX IF EXISTS idx_automation_target_sessions_session;
DROP INDEX IF EXISTS idx_automation_target_sessions_active;
DROP TABLE automation_target_sessions;

DROP INDEX IF EXISTS idx_automation_targets_wake;
DROP TABLE automation_targets;

DROP INDEX IF EXISTS idx_sessions_automation_owner;
ALTER TABLE sessions DROP COLUMN automation_owner_generation_id;

ALTER TABLE automations
    DROP CONSTRAINT chk_automations_session_continuity,
    DROP COLUMN session_continuity;
