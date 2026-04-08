-- !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
-- DESTRUCTIVE: This down migration permanently deletes all PRs with NULL
-- session_id (external/manual PRs created after the forward migration ran).
-- Cascade-deleted child rows: review_comments, deploys.
--
-- Before running in production, back up affected rows:
--   CREATE TABLE pull_requests_nullable_session_backup AS
--     SELECT * FROM pull_requests WHERE session_id IS NULL;
-- !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

-- Log what will be deleted before actually deleting.
DO $$
DECLARE
    del_count int;
BEGIN
    SELECT count(*) INTO del_count FROM pull_requests WHERE session_id IS NULL;
    IF del_count > 0 THEN
        RAISE WARNING '% pull_requests with NULL session_id will be deleted (review_comments and deploys will cascade)', del_count;
    END IF;
END $$;

DELETE FROM pull_requests WHERE session_id IS NULL;
ALTER TABLE pull_requests ALTER COLUMN session_id SET NOT NULL;

-- Restore session_logs.thread_id CASCADE.
ALTER TABLE session_logs DROP CONSTRAINT IF EXISTS fk_session_logs_thread;
ALTER TABLE session_logs
    ADD CONSTRAINT fk_session_logs_thread
    FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE CASCADE;

-- Restore session_messages.thread_id CASCADE.
ALTER TABLE session_messages DROP CONSTRAINT IF EXISTS fk_session_messages_thread;
ALTER TABLE session_messages
    ADD CONSTRAINT fk_session_messages_thread
    FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE CASCADE;
