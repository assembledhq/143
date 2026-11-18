SET LOCAL lock_timeout = '5s';

ALTER TABLE code_review_agent_results VALIDATE CONSTRAINT chk_cra_dispatch_send;
ALTER TABLE code_review_agent_results VALIDATE CONSTRAINT fk_cra_dispatch_thread;
ALTER TABLE session_review_loops VALIDATE CONSTRAINT chk_srl_startup_send;
ALTER TABLE session_review_loops VALIDATE CONSTRAINT fk_srl_startup_thread;
ALTER TABLE review_loop_start_intents VALIDATE CONSTRAINT chk_review_loop_start_intent;
ALTER TABLE caller_send_command_stamps VALIDATE CONSTRAINT chk_caller_send_stamp;
ALTER TABLE automation_runs VALIDATE CONSTRAINT fk_automation_review_start;
ALTER TABLE session_publications VALIDATE CONSTRAINT fk_publication_review_start;
ALTER TABLE session_review_loops ADD CONSTRAINT fk_srl_startup_pass
    FOREIGN KEY (org_id,session_id,id,startup_send_pass_id)
    REFERENCES session_review_loop_passes(org_id,session_id,loop_id,id)
    DEFERRABLE INITIALLY DEFERRED NOT VALID;
ALTER TABLE session_review_loops VALIDATE CONSTRAINT fk_srl_startup_pass;
