-- Recovery proves terminal ownership for one tenant/session/thread at a time.
-- Build without blocking executor writes. Interrupted invalid indexes must be
-- removed before repairing/retrying this standalone migration.
CREATE INDEX CONCURRENTLY idx_session_executors_terminal_review_lookup
    ON session_executors (org_id, session_id, thread_id) INCLUDE (job_id)
    WHERE status IN ('failed', 'completed', 'lost');
