-- Drop FKs from hot/ephemeral tables; keep org_id/parent-id columns and indexes.
-- session_logs: SessionLogStore.Create now validates session/thread ownership via
--   SELECT before insert. ON DELETE CASCADE (session) and ON DELETE SET NULL (thread)
--   are no longer DB-enforced; sessions/threads are soft-deleted so this is safe.
--   Orphan retention handled by delete_expired_session_logs.
-- preview_dependency_cache_locations: ephemeral worker hints; stale rows acceptable;
--   cleanup handled by TTL-based DeleteExpiredDependencyCacheLocations.

SET LOCAL lock_timeout = '5s';

ALTER TABLE session_logs DROP CONSTRAINT IF EXISTS fk_session_logs_session;
ALTER TABLE session_logs DROP CONSTRAINT IF EXISTS fk_session_logs_org;
ALTER TABLE session_logs DROP CONSTRAINT IF EXISTS fk_session_logs_thread;

ALTER TABLE preview_dependency_cache_locations DROP CONSTRAINT IF EXISTS preview_dependency_cache_locations_org_id_fkey;
ALTER TABLE preview_dependency_cache_locations DROP CONSTRAINT IF EXISTS preview_dependency_cache_locations_repo_id_fkey;
