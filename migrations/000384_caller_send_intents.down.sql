-- Recheck at the storage-removal boundary; a prior readiness downgrade is not evidence of settlement.
LOCK TABLE caller_send_intent_control, code_review_agent_results, session_review_loops,
    session_review_loop_passes, review_loop_start_intents, caller_send_command_stamps,
    session_publications, automation_runs IN ACCESS EXCLUSIVE MODE;
SELECT caller143_assert_send_rollback_safe();

DROP TRIGGER trg_caller_dispatch_owner ON code_review_agent_results;
DROP TRIGGER trg_caller_dispatch_truncate ON code_review_agent_results;
DROP TRIGGER trg_caller_dispatch_payload ON code_review_agent_results;
DROP TRIGGER trg_caller_startup_owner ON session_review_loops;
DROP TRIGGER trg_caller_startup_truncate ON session_review_loops;
DROP TRIGGER trg_caller_startup_payload ON session_review_loops;
DROP TRIGGER trg_caller_review_start ON review_loop_start_intents;
DROP TRIGGER trg_caller_review_start_truncate ON review_loop_start_intents;
DROP TRIGGER trg_caller_created_start ON review_loop_start_intents;
DROP TRIGGER trg_caller_review_start_payload ON review_loop_start_intents;
DROP TRIGGER trg_caller_automation_start ON automation_runs;
DROP TRIGGER trg_caller_publication_start ON session_publications;
DROP TRIGGER trg_caller_automation_truncate ON automation_runs;
DROP TRIGGER trg_caller_publication_truncate ON session_publications;
DROP TRIGGER trg_caller_send_stamp ON caller_send_command_stamps;
DROP TRIGGER trg_caller_send_stamp_truncate ON caller_send_command_stamps;

ALTER TABLE automation_runs DROP COLUMN review_start_id;
ALTER TABLE session_publications DROP COLUMN review_start_id;
ALTER TABLE code_review_agent_results
    DROP COLUMN dispatch_version, DROP COLUMN dispatch_key, DROP COLUMN dispatch_input,
    DROP COLUMN dispatch_digest, DROP COLUMN dispatch_thread_id, DROP COLUMN dispatch_state,
    DROP COLUMN dispatch_attempt_id, DROP COLUMN dispatch_attempt_seq, DROP COLUMN dispatch_attempted_at,
    DROP COLUMN dispatch_due_at, DROP COLUMN dispatch_observe_deadline, DROP COLUMN dispatch_attention_at,
    DROP COLUMN dispatch_observed_at, DROP COLUMN dispatch_message_id, DROP COLUMN dispatch_resolution,
    DROP COLUMN dispatch_detail, DROP COLUMN dispatch_writer_seq, DROP COLUMN dispatch_updated_at, DROP COLUMN dispatch_wake;
ALTER TABLE session_review_loops
    DROP COLUMN startup_send_version, DROP COLUMN startup_send_key, DROP COLUMN startup_send_input,
    DROP COLUMN startup_send_digest, DROP COLUMN startup_send_thread_id, DROP COLUMN startup_send_state,
    DROP COLUMN startup_send_attempt_id, DROP COLUMN startup_send_attempt_seq, DROP COLUMN startup_send_attempted_at,
    DROP COLUMN startup_send_due_at, DROP COLUMN startup_send_observe_deadline, DROP COLUMN startup_send_attention_at,
    DROP COLUMN startup_send_observed_at, DROP COLUMN startup_send_message_id, DROP COLUMN startup_send_resolution,
    DROP COLUMN startup_send_detail, DROP COLUMN startup_send_writer_seq, DROP COLUMN startup_send_updated_at, DROP COLUMN startup_send_pass_id;
DROP TABLE caller_send_command_stamps;
DROP TABLE review_loop_start_intents;
DROP TABLE caller_send_intent_control;
DROP FUNCTION caller143_assert_send_rollback_safe();
DROP FUNCTION caller143_guard_send_stamp();
DROP FUNCTION caller143_stamp_owner_unresolved(uuid,text,uuid);
DROP FUNCTION caller143_guard_review_caller();
DROP FUNCTION caller143_check_created_start();
DROP FUNCTION caller143_guard_send_payload_update();
DROP FUNCTION caller143_guard_review_start();
DROP FUNCTION caller143_guard_startup_owner();
DROP FUNCTION caller143_guard_dispatch_owner();
DROP FUNCTION caller143_start_unresolved(uuid,uuid);
DROP FUNCTION caller143_send_transition_valid(jsonb,jsonb);
DROP FUNCTION caller143_send_state_valid(text,uuid,integer,timestamptz,timestamptz,timestamptz,timestamptz,timestamptz,bigint,text,text,integer,timestamptz);
DROP FUNCTION caller143_send_input_valid(jsonb,text,uuid,uuid,uuid,uuid);
DROP FUNCTION caller143_send_digest_valid(text);
DROP FUNCTION caller143_require_send_intents_enabled();
