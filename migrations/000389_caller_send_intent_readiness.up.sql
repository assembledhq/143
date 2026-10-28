LOCK TABLE caller_send_intent_control IN SHARE ROW EXCLUSIVE MODE;
DO $$
DECLARE expected record;
BEGIN
    FOR expected IN SELECT * FROM (VALUES
        ('caller_send_intent_control','caller_send_intent_control_pkey'),
        ('caller_send_intent_control','caller_send_intent_control_singleton_check'),
        ('code_review_agent_results','chk_cra_dispatch_send'),
        ('code_review_agent_results','fk_cra_dispatch_thread'),
        ('session_review_loops','chk_srl_startup_send'),
        ('session_review_loops','fk_srl_startup_thread'),
        ('session_review_loops','fk_srl_startup_pass'),
        ('review_loop_start_intents','chk_review_loop_start_intent'),
        ('review_loop_start_intents','review_loop_start_intents_pkey'),
        ('review_loop_start_intents','review_loop_start_intents_org_id_fkey'),
        ('review_loop_start_intents','fk_review_loop_start_session'),
        ('review_loop_start_intents','fk_review_loop_start_automation'),
        ('review_loop_start_intents','fk_review_loop_start_changeset'),
        ('review_loop_start_intents','uq_review_loop_start_org_id'),
        ('review_loop_start_intents','uq_review_loop_start_org_session_id'),
        ('caller_send_command_stamps','chk_caller_send_stamp'),
        ('caller_send_command_stamps','caller_send_command_stamps_org_id_fkey'),
        ('caller_send_command_stamps','fk_caller_send_stamp_session'),
        ('caller_send_command_stamps','fk_caller_send_stamp_thread'),
        ('caller_send_command_stamps','uq_caller_send_stamp_thread_key'),
        ('caller_send_command_stamps','uq_caller_send_stamp_owner'),
        ('automation_runs','fk_automation_review_start'),
        ('session_publications','fk_publication_review_start')
    ) AS required(table_name,constraint_name) LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_constraint c
            WHERE c.conrelid = to_regclass(expected.table_name) AND c.conname = expected.constraint_name AND c.convalidated) THEN
            RAISE EXCEPTION 'caller_send_schema_constraint_not_ready' USING ERRCODE = '55000';
        END IF;
    END LOOP;
    FOR expected IN SELECT * FROM (VALUES
        ('code_review_agent_results','idx_cr_agent_results_dispatch_due'),
        ('session_review_loops','idx_srl_startup_send_due'),
        ('session_review_loop_passes','idx_srl_passes_org_session_loop_id'),
        ('review_loop_start_intents','idx_review_loop_start_due'),
        ('review_loop_start_intents','uq_review_loop_start_org_id'),
        ('review_loop_start_intents','uq_review_loop_start_org_session_id'),
        ('caller_send_command_stamps','uq_caller_send_stamp_thread_key'),
        ('caller_send_command_stamps','uq_caller_send_stamp_owner')
    ) AS required(table_name,index_name) LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_index i WHERE i.indexrelid = to_regclass(expected.index_name)
            AND i.indrelid = to_regclass(expected.table_name) AND i.indisvalid AND i.indisready) THEN
            RAISE EXCEPTION 'caller_send_schema_index_not_ready' USING ERRCODE = '55000';
        END IF;
    END LOOP;
    FOR expected IN SELECT * FROM (VALUES
        ('code_review_agent_results','trg_caller_dispatch_owner'),
        ('code_review_agent_results','trg_caller_dispatch_truncate'),
        ('code_review_agent_results','trg_caller_dispatch_payload'),
        ('session_review_loops','trg_caller_startup_owner'),
        ('session_review_loops','trg_caller_startup_truncate'),
        ('session_review_loops','trg_caller_startup_payload'),
        ('review_loop_start_intents','trg_caller_review_start'),
        ('review_loop_start_intents','trg_caller_review_start_truncate'),
        ('review_loop_start_intents','trg_caller_created_start'),
        ('review_loop_start_intents','trg_caller_review_start_payload'),
        ('automation_runs','trg_caller_automation_start'),
        ('automation_runs','trg_caller_automation_truncate'),
        ('session_publications','trg_caller_publication_start'),
        ('session_publications','trg_caller_publication_truncate'),
        ('caller_send_command_stamps','trg_caller_send_stamp'),
        ('caller_send_command_stamps','trg_caller_send_stamp_truncate')
    ) AS required(table_name,trigger_name) LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_trigger t WHERE t.tgrelid = to_regclass(expected.table_name)
            AND t.tgname = expected.trigger_name AND t.tgenabled IN ('O','A')) THEN
            RAISE EXCEPTION 'caller_send_schema_trigger_not_ready' USING ERRCODE = '55000';
        END IF;
    END LOOP;
END;
$$;
INSERT INTO caller_send_intent_control(singleton,schema_ready,intents_enabled,updated_at)
VALUES (true,true,false,now()) ON CONFLICT(singleton)
DO UPDATE SET schema_ready = true, updated_at = now();
