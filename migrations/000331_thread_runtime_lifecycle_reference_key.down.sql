-- Remove dependent foreign keys before rolling back this prerequisite; no CASCADE.
DROP INDEX CONCURRENTLY idx_thread_runtimes_org_session_thread_id;
