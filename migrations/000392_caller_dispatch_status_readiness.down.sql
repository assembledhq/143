-- Highest-first preflight precedes every lower down. Once the new status is
-- retained, rollback refuses permanently; recovery is forward-fix only.
SET LOCAL lock_timeout = '5s';
LOCK TABLE code_review_session_metadata, caller_send_intent_control,
    code_review_agent_results, session_review_loops, session_review_loop_passes,
    review_loop_start_intents, caller_send_command_stamps,
    session_publications, automation_runs IN SHARE ROW EXCLUSIVE MODE;
SELECT caller143_assert_send_rollback_safe();
UPDATE caller_send_intent_control
SET dispatch_status_ready = false, intents_enabled = false, updated_at = now()
WHERE singleton;
