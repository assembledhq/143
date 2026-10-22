-- A NULL lane remains explicitly unthreaded; no primary-lane inference.
-- One existing hot parent per migration transaction; dispatch has no producer.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_input_dispatches
    ADD CONSTRAINT session_input_dispatches_thread_scope_fk
    FOREIGN KEY (org_id, session_id, thread_id)
    REFERENCES session_threads (org_id, session_id, id);
