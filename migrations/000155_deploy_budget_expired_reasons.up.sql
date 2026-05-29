ALTER TABLE nodes
    DROP CONSTRAINT IF EXISTS chk_nodes_drain_intent;

ALTER TABLE nodes
    ADD CONSTRAINT chk_nodes_drain_intent CHECK (drain_intent IN (
        'none', 'planned_rollout', 'deploy_budget_expired', 'runtime_ceiling',
        'human_input_checkpoint', 'host_maintenance', 'emergency_force'
    ));

ALTER TABLE session_executors
    DROP CONSTRAINT IF EXISTS chk_session_executors_drain_intent;

ALTER TABLE session_executors
    ADD CONSTRAINT chk_session_executors_drain_intent CHECK (drain_intent IN (
        'none', 'planned_rollout', 'deploy_budget_expired', 'runtime_ceiling',
        'human_input_checkpoint', 'host_maintenance', 'emergency_force'
    ));

ALTER TABLE worker_deploy_events
    DROP CONSTRAINT IF EXISTS chk_worker_deploy_events_drain_intent;

ALTER TABLE worker_deploy_events
    ADD CONSTRAINT chk_worker_deploy_events_drain_intent CHECK (drain_intent IN (
        'none', 'planned_rollout', 'deploy_budget_expired', 'runtime_ceiling',
        'human_input_checkpoint', 'host_maintenance', 'emergency_force'
    ));

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_runtime_stop_reason_check;

ALTER TABLE sessions
    ADD CONSTRAINT sessions_runtime_stop_reason_check
        CHECK (runtime_stop_reason IN ('', 'user_cancel', 'soft_budget', 'no_progress', 'absolute_ceiling', 'force_kill', 'worker_recovery', 'worker_drain', 'deploy_budget_expired'));
