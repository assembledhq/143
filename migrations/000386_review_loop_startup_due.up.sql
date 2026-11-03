CREATE INDEX CONCURRENTLY idx_srl_startup_send_due ON session_review_loops(startup_send_due_at,id) WHERE startup_send_due_at IS NOT NULL;
