-- This preflight must finish before any subsequent standalone concurrent index drop.
LOCK TABLE caller_send_intent_control, code_review_agent_results, session_review_loops,
    session_review_loop_passes, review_loop_start_intents, caller_send_command_stamps,
    session_publications, automation_runs IN SHARE ROW EXCLUSIVE MODE;
SELECT caller143_assert_send_rollback_safe();
UPDATE caller_send_intent_control SET schema_ready = false, intents_enabled = false, updated_at = now()
WHERE singleton;
