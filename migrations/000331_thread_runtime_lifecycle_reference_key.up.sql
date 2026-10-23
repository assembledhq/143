-- Establish exact tenant/session/lane/runtime identity for future foreign keys.
-- Optional runtime references also need a nonnull lane when the runtime is present.
-- Standalone concurrent build; a leftover invalid index must fail visibly.
CREATE UNIQUE INDEX CONCURRENTLY idx_thread_runtimes_org_session_thread_id
    ON thread_runtimes (org_id, session_id, thread_id, id);
