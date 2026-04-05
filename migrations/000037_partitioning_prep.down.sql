-- Reverse partitioning: convert partitioned tables back to regular tables.
-- This copies all data back into non-partitioned tables.

-- =============================================================================
-- audit_logs
-- =============================================================================
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

-- =============================================================================
-- session_messages
-- =============================================================================
CREATE TABLE session_messages_backup AS SELECT * FROM session_messages;

DROP TABLE session_messages CASCADE;

CREATE TABLE session_messages (
    id          bigserial   PRIMARY KEY,
    session_id  uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    org_id      uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id     uuid        REFERENCES users(id),
    turn_number int         NOT NULL,
    role        text        NOT NULL,
    content     text        NOT NULL,
    attachments text[],
    token_usage jsonb,
    thread_id   uuid        REFERENCES session_threads(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO session_messages (id, session_id, org_id, user_id, turn_number, role, content, attachments, token_usage, thread_id, created_at)
SELECT id, session_id, org_id, user_id, turn_number, role, content, attachments, token_usage, thread_id, created_at
FROM session_messages_backup;

DO $$ BEGIN
    IF (SELECT count(*) FROM session_messages) != (SELECT count(*) FROM session_messages_backup) THEN
        RAISE EXCEPTION 'session_messages row count mismatch during partition rollback';
    END IF;
END $$;

SELECT setval('session_messages_id_seq', COALESCE((SELECT MAX(id) FROM session_messages), 1));

CREATE INDEX idx_session_messages_session ON session_messages (org_id, session_id, turn_number);
CREATE INDEX idx_session_messages_thread ON session_messages (thread_id) WHERE thread_id IS NOT NULL;

DROP TABLE session_messages_backup;

-- =============================================================================
-- session_logs
-- =============================================================================
CREATE TABLE session_logs_backup AS SELECT * FROM session_logs;

DROP TABLE session_logs CASCADE;

CREATE TABLE session_logs (
    id           bigserial   PRIMARY KEY,
    session_id   uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    timestamp    timestamptz NOT NULL DEFAULT now(),
    level        text        NOT NULL DEFAULT 'info',
    message      text        NOT NULL,
    metadata     jsonb,
    turn_number  int         NOT NULL DEFAULT 0,
    thread_id    uuid        REFERENCES session_threads(id) ON DELETE CASCADE
);

INSERT INTO session_logs (id, session_id, timestamp, level, message, metadata, turn_number, thread_id)
SELECT id, session_id, timestamp, level, message, metadata, turn_number, thread_id
FROM session_logs_backup;

DO $$ BEGIN
    IF (SELECT count(*) FROM session_logs) != (SELECT count(*) FROM session_logs_backup) THEN
        RAISE EXCEPTION 'session_logs row count mismatch during partition rollback';
    END IF;
END $$;

SELECT setval('session_logs_id_seq', COALESCE((SELECT MAX(id) FROM session_logs), 1));

CREATE INDEX idx_session_logs_session ON session_logs (session_id, timestamp);
CREATE INDEX idx_session_logs_thread ON session_logs (thread_id) WHERE thread_id IS NOT NULL;
CREATE INDEX idx_session_logs_timestamp ON session_logs (timestamp);

DROP TABLE session_logs_backup;

-- =============================================================================
-- Drop helper functions
-- =============================================================================
DROP FUNCTION IF EXISTS drop_expired_audit_log_partitions(int);
DROP FUNCTION IF EXISTS ensure_future_partitions(text, int);
DROP FUNCTION IF EXISTS create_monthly_partitions(text, date, date);
