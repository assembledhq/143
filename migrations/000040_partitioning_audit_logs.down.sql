-- Reverse audit_logs partitioning.
DROP TRIGGER IF EXISTS audit_logs_immutable ON audit_logs;

CREATE TABLE audit_logs_backup AS SELECT * FROM audit_logs;

DROP TABLE audit_logs CASCADE;

CREATE TABLE audit_logs (
    id              bigserial       PRIMARY KEY,
    org_id          uuid            NOT NULL REFERENCES organizations(id),
    actor_type      text            NOT NULL,
    actor_id        text            NOT NULL,
    user_id         uuid            REFERENCES users(id),
    action          text            NOT NULL,
    resource_type   text            NOT NULL,
    resource_id     text,
    details         jsonb,
    request_id      text,
    ip_address      inet,
    user_agent      text,
    session_id      uuid,
    project_id      uuid,
    created_at      timestamptz     NOT NULL DEFAULT now()
);

INSERT INTO audit_logs (id, org_id, actor_type, actor_id, user_id, action, resource_type, resource_id, details, request_id, ip_address, user_agent, session_id, project_id, created_at)
SELECT id, org_id, actor_type, actor_id, user_id, action, resource_type, resource_id, details, request_id, ip_address, user_agent, session_id, project_id, created_at
FROM audit_logs_backup;

DO $$ BEGIN
    IF (SELECT count(*) FROM audit_logs) != (SELECT count(*) FROM audit_logs_backup) THEN
        RAISE EXCEPTION 'audit_logs row count mismatch during partition rollback';
    END IF;
END $$;

SELECT setval('audit_logs_id_seq', COALESCE((SELECT MAX(id) FROM audit_logs), 1));

CREATE INDEX idx_audit_logs_org_created ON audit_logs (org_id, created_at DESC, id DESC);
CREATE INDEX idx_audit_logs_resource ON audit_logs (org_id, resource_type, resource_id, created_at DESC);
CREATE INDEX idx_audit_logs_user ON audit_logs (org_id, user_id, created_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX idx_audit_logs_action ON audit_logs (org_id, action, created_at DESC);
CREATE INDEX idx_audit_logs_session ON audit_logs (session_id, created_at DESC) WHERE session_id IS NOT NULL;
CREATE INDEX idx_audit_logs_project ON audit_logs (project_id, created_at DESC) WHERE project_id IS NOT NULL;

CREATE TRIGGER audit_logs_immutable
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_logs_modification();

DROP TABLE audit_logs_backup;

DROP FUNCTION IF EXISTS drop_expired_audit_log_partitions(int);
