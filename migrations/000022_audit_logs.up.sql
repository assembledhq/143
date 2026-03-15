-- =============================================================================
-- Rename legacy audit_log table (preserve existing data)
-- =============================================================================
ALTER TABLE audit_log RENAME TO audit_log_legacy;
DROP TRIGGER IF EXISTS audit_log_immutable ON audit_log_legacy;

-- =============================================================================
-- audit_logs (new)
-- =============================================================================
CREATE TABLE audit_logs (
    id              bigserial       PRIMARY KEY,
    org_id          uuid            NOT NULL REFERENCES organizations(id),

    -- Actor identification
    actor_type      text            NOT NULL,
    actor_id        text            NOT NULL,
    user_id         uuid            REFERENCES users(id),

    -- What happened
    action          text            NOT NULL,
    resource_type   text            NOT NULL,
    resource_id     text,

    -- Context
    details         jsonb,
    request_id      text,
    ip_address      inet,
    user_agent      text,

    -- Correlation (no FK intentionally — audit entries must survive if the
    -- referenced session or project is deleted)
    session_id      uuid,
    project_id      uuid,

    created_at      timestamptz     NOT NULL DEFAULT now()
);

-- -----------------------------------------------------------------------
-- Indexes (designed for the query patterns in the API)
-- -----------------------------------------------------------------------

-- Primary listing: "show me the audit trail for this org, newest first"
CREATE INDEX idx_audit_logs_org_created ON audit_logs (org_id, created_at DESC, id DESC);

-- Resource drill-down: "show me all actions on this specific resource"
CREATE INDEX idx_audit_logs_resource ON audit_logs (org_id, resource_type, resource_id, created_at DESC);

-- Actor drill-down: "show me everything this user did"
CREATE INDEX idx_audit_logs_user ON audit_logs (org_id, user_id, created_at DESC) WHERE user_id IS NOT NULL;

-- Action filtering: "show me all session.created events"
CREATE INDEX idx_audit_logs_action ON audit_logs (org_id, action, created_at DESC);

-- Correlation: "show me all audit entries for this session/project"
CREATE INDEX idx_audit_logs_session ON audit_logs (session_id, created_at DESC) WHERE session_id IS NOT NULL;
CREATE INDEX idx_audit_logs_project ON audit_logs (project_id, created_at DESC) WHERE project_id IS NOT NULL;

-- -----------------------------------------------------------------------
-- Immutability trigger
-- -----------------------------------------------------------------------
CREATE OR REPLACE FUNCTION prevent_audit_logs_modification()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs is append-only: % operations are not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_logs_immutable
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_logs_modification();

-- -----------------------------------------------------------------------
-- Retention cleanup function (SECURITY DEFINER to bypass immutability trigger)
-- IMPORTANT: This function's owner must have ALTER TABLE privileges on
-- audit_logs because it uses DISABLE/ENABLE TRIGGER. The migration user
-- (typically the DB superuser/owner) becomes the owner. Do not reassign
-- ownership to a lesser-privileged role.
-- -----------------------------------------------------------------------
CREATE OR REPLACE FUNCTION delete_expired_audit_logs(target_org_id uuid, retention_days int)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    deleted bigint;
BEGIN
    IF retention_days <= 0 THEN
        RAISE EXCEPTION 'retention_days must be positive, got %', retention_days;
    END IF;

    ALTER TABLE audit_logs DISABLE TRIGGER audit_logs_immutable;

    BEGIN
        DELETE FROM audit_logs
        WHERE org_id = target_org_id
          AND created_at < now() - make_interval(days => retention_days);

        GET DIAGNOSTICS deleted = ROW_COUNT;
    EXCEPTION WHEN OTHERS THEN
        ALTER TABLE audit_logs ENABLE TRIGGER audit_logs_immutable;
        RAISE;
    END;

    ALTER TABLE audit_logs ENABLE TRIGGER audit_logs_immutable;

    RETURN deleted;
END;
$$;
