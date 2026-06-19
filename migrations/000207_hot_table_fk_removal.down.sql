BEGIN;

-- session_logs: PostgreSQL does not support NOT VALID foreign keys on
-- partitioned tables, so these constraints are restored with full row
-- validation. Rolling back on a large session_logs table requires a
-- maintenance window; do not set a short lock_timeout here or the rollback
-- will abort mid-way and leave migration state inconsistent.
ALTER TABLE session_logs
    ADD CONSTRAINT fk_session_logs_session
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE;

ALTER TABLE session_logs
    ADD CONSTRAINT fk_session_logs_org
    FOREIGN KEY (org_id) REFERENCES organizations(id);

ALTER TABLE session_logs
    ADD CONSTRAINT fk_session_logs_thread
    FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE SET NULL;

-- preview_dependency_cache_locations: added NOT VALID to skip the full table
-- scan on rollback. Rows present when the up migration ran were inserted while
-- the FK was in force and are structurally valid. Rows inserted after the up
-- migration may reference deleted orgs/repos; leave those as NOT VALID rather
-- than blocking the rollback. Validate separately if strict enforcement is
-- required after rollback.
SET LOCAL lock_timeout = '5s';

ALTER TABLE preview_dependency_cache_locations
    ADD CONSTRAINT preview_dependency_cache_locations_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE CASCADE NOT VALID;

ALTER TABLE preview_dependency_cache_locations
    ADD CONSTRAINT preview_dependency_cache_locations_repo_id_fkey
    FOREIGN KEY (repo_id) REFERENCES repositories(id) ON DELETE CASCADE NOT VALID;

COMMIT;
