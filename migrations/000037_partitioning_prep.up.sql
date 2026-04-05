-- Partitioning preparation for high-growth append-only tables.
--
-- Strategy: range partition by time column (monthly) on the three highest-volume
-- append-only tables. We migrate each table by:
--   1. Creating a new partitioned table with the same schema
--   2. Copying existing data into it
--   3. Swapping the old table out and the new table in
--   4. Creating initial partitions covering past data + the next 3 months
--
-- IMPORTANT: This migration should be run during a maintenance window as it
-- copies data and swaps tables. For large datasets, consider running the
-- data copy steps manually in batches before deploying this migration.
--
-- FK NOTE: Partitioned tables retain FKs on session_id and thread_id for
-- session_logs and session_messages. For audit_logs, session_id and project_id
-- FKs are intentionally omitted so audit entries survive if the referenced
-- session or project is deleted (audit_logs comments explain further).

-- =============================================================================
-- Helper: create monthly partitions for a given table.
-- Creates partitions from start_date through end_date (exclusive).
-- =============================================================================
CREATE OR REPLACE FUNCTION create_monthly_partitions(
    parent_table text,
    start_date date,
    end_date date
) RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    partition_start date := date_trunc('month', start_date);
    partition_end date;
    partition_name text;
BEGIN
    WHILE partition_start < end_date LOOP
        partition_end := partition_start + interval '1 month';
        partition_name := parent_table || '_y' || to_char(partition_start, 'YYYY') || 'm' || to_char(partition_start, 'MM');

        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF %I FOR VALUES FROM (%L) TO (%L)',
            partition_name, parent_table, partition_start, partition_end
        );

        partition_start := partition_end;
    END LOOP;
END;
$$;

-- =============================================================================
-- Helper: ensure partitions exist for an upcoming period.
-- Designed to be called by a cron job or scheduled task to pre-create
-- partitions before they are needed.
-- =============================================================================
CREATE OR REPLACE FUNCTION ensure_future_partitions(
    parent_table text,
    months_ahead int DEFAULT 3
) RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM create_monthly_partitions(
        parent_table,
        date_trunc('month', now()::date),
        (date_trunc('month', now()::date) + make_interval(months => months_ahead))::date
    );
END;
$$;

-- =============================================================================
-- session_logs: partition by timestamp (the time column for this table)
-- =============================================================================

-- Lock table to prevent concurrent writes during the swap.
LOCK TABLE session_logs IN ACCESS EXCLUSIVE MODE;

-- 1. Rename existing table, its indexes, and its backing sequence.
--    bigserial columns own a *_id_seq sequence; renaming the table does NOT
--    rename the sequence, so the subsequent CREATE TABLE would collide.
ALTER TABLE session_logs RENAME TO session_logs_old;
-- The sequence kept its original name (agent_run_logs_id_seq) when the table
-- was renamed from agent_run_logs to session_logs in migration 000015.
ALTER SEQUENCE agent_run_logs_id_seq RENAME TO agent_run_logs_id_seq_old;
ALTER INDEX idx_session_logs_session RENAME TO idx_session_logs_session_old;
ALTER INDEX idx_session_logs_timestamp RENAME TO idx_session_logs_timestamp_old;
ALTER INDEX IF EXISTS idx_session_logs_thread RENAME TO idx_session_logs_thread_old;

-- 2. Create partitioned table with identical schema + org_id.
--    PK must include the partition key, so we use (id, timestamp).
--    org_id is included here (rather than in a later migration) to avoid a
--    deployment gap where Go code expects the column but it doesn't exist yet.
CREATE TABLE session_logs (
    id           bigserial   NOT NULL,
    session_id   uuid        NOT NULL,
    org_id       uuid        NOT NULL,
    timestamp    timestamptz NOT NULL DEFAULT now(),
    level        text        NOT NULL DEFAULT 'info',
    message      text        NOT NULL,
    metadata     jsonb,
    turn_number  int         NOT NULL DEFAULT 0,
    thread_id    uuid,
    PRIMARY KEY (id, timestamp)
) PARTITION BY RANGE (timestamp);

-- 3. Add FKs. CASCADE on session_id because logs are owned content.
ALTER TABLE session_logs
    ADD CONSTRAINT fk_session_logs_session FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    ADD CONSTRAINT fk_session_logs_org FOREIGN KEY (org_id) REFERENCES organizations(id),
    ADD CONSTRAINT fk_session_logs_thread FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE CASCADE;

-- 4. Recreate indexes (auto-propagate to partitions).
CREATE INDEX idx_session_logs_session ON session_logs (session_id, timestamp);
CREATE INDEX idx_session_logs_thread ON session_logs (thread_id) WHERE thread_id IS NOT NULL;
CREATE INDEX idx_session_logs_timestamp ON session_logs (timestamp);
CREATE INDEX idx_session_logs_org_created ON session_logs (org_id, timestamp DESC);

-- 5. Add the CHECK constraint from migration 034.
ALTER TABLE session_logs
    ADD CONSTRAINT chk_session_logs_level CHECK (level IN ('debug', 'info', 'warn', 'error'));

-- 6. Create partitions: from 2025-01 through 3 months from now.
SELECT create_monthly_partitions('session_logs', '2025-01-01'::date,
    (date_trunc('month', now()::date) + interval '3 months')::date);

-- 7. Default partition for data outside defined ranges.
CREATE TABLE session_logs_default PARTITION OF session_logs DEFAULT;

-- 8. Copy data from old table, backfilling org_id from sessions.
--    Orphaned rows (session deleted) are excluded by the JOIN.
INSERT INTO session_logs (id, session_id, org_id, timestamp, level, message, metadata, turn_number, thread_id)
SELECT sl.id, sl.session_id, s.org_id, sl.timestamp, sl.level, sl.message, sl.metadata, sl.turn_number, sl.thread_id
FROM session_logs_old sl
JOIN sessions s ON s.id = sl.session_id;

-- 9. Sync the sequence.
SELECT setval('session_logs_id_seq', COALESCE((SELECT MAX(id) FROM session_logs), 1));

-- 10. Drop old table.
DROP TABLE session_logs_old;

-- =============================================================================
-- session_messages: partition by created_at
-- =============================================================================

LOCK TABLE session_messages IN ACCESS EXCLUSIVE MODE;

ALTER TABLE session_messages RENAME TO session_messages_old;
ALTER SEQUENCE session_messages_id_seq RENAME TO session_messages_id_seq_old;
ALTER INDEX IF EXISTS idx_session_messages_session RENAME TO idx_session_messages_session_old;
ALTER INDEX IF EXISTS idx_session_messages_thread RENAME TO idx_session_messages_thread_old;

CREATE TABLE session_messages (
    id          bigserial   NOT NULL,
    session_id  uuid        NOT NULL,
    org_id      uuid        NOT NULL,
    user_id     uuid,
    turn_number int         NOT NULL,
    role        text        NOT NULL,
    content     text        NOT NULL,
    attachments text[],
    token_usage jsonb,
    thread_id   uuid,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

ALTER TABLE session_messages
    ADD CONSTRAINT fk_session_messages_session FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    ADD CONSTRAINT fk_session_messages_org FOREIGN KEY (org_id) REFERENCES organizations(id),
    ADD CONSTRAINT fk_session_messages_user FOREIGN KEY (user_id) REFERENCES users(id),
    ADD CONSTRAINT fk_session_messages_thread FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE CASCADE;

CREATE INDEX idx_session_messages_session ON session_messages (org_id, session_id, turn_number);
CREATE INDEX idx_session_messages_thread ON session_messages (thread_id) WHERE thread_id IS NOT NULL;

SELECT create_monthly_partitions('session_messages', '2025-01-01'::date,
    (date_trunc('month', now()::date) + interval '3 months')::date);

CREATE TABLE session_messages_default PARTITION OF session_messages DEFAULT;

INSERT INTO session_messages (id, session_id, org_id, user_id, turn_number, role, content, attachments, token_usage, thread_id, created_at)
SELECT id, session_id, org_id, user_id, turn_number, role, content, attachments, token_usage, thread_id, created_at
FROM session_messages_old;

DO $$ BEGIN
    IF (SELECT count(*) FROM session_messages) != (SELECT count(*) FROM session_messages_old) THEN
        RAISE EXCEPTION 'session_messages row count mismatch after partition copy';
    END IF;
END $$;

SELECT setval('session_messages_id_seq', COALESCE((SELECT MAX(id) FROM session_messages), 1));

DROP TABLE session_messages_old;

-- =============================================================================
-- audit_logs: partition by created_at
-- =============================================================================

LOCK TABLE audit_logs IN ACCESS EXCLUSIVE MODE;

-- Must drop immutability trigger before rename.
DROP TRIGGER IF EXISTS audit_logs_immutable ON audit_logs;

ALTER TABLE audit_logs RENAME TO audit_logs_old;
ALTER SEQUENCE audit_logs_id_seq RENAME TO audit_logs_id_seq_old;
ALTER INDEX IF EXISTS idx_audit_logs_org_created RENAME TO idx_audit_logs_org_created_old;
ALTER INDEX IF EXISTS idx_audit_logs_resource RENAME TO idx_audit_logs_resource_old;
ALTER INDEX IF EXISTS idx_audit_logs_user RENAME TO idx_audit_logs_user_old;
ALTER INDEX IF EXISTS idx_audit_logs_action RENAME TO idx_audit_logs_action_old;
ALTER INDEX IF EXISTS idx_audit_logs_session RENAME TO idx_audit_logs_session_old;
ALTER INDEX IF EXISTS idx_audit_logs_project RENAME TO idx_audit_logs_project_old;

CREATE TABLE audit_logs (
    id              bigserial       NOT NULL,
    org_id          uuid            NOT NULL,
    actor_type      text            NOT NULL,
    actor_id        text            NOT NULL,
    user_id         uuid,
    action          text            NOT NULL,
    resource_type   text            NOT NULL,
    resource_id     text,
    details         jsonb,
    request_id      text,
    ip_address      inet,
    user_agent      text,
    session_id      uuid,
    project_id      uuid,
    created_at      timestamptz     NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- FK on org_id since orgs should never be hard-deleted.
-- Intentionally no FK on session_id/project_id — audit entries must survive
-- if the referenced session or project is deleted.
ALTER TABLE audit_logs
    ADD CONSTRAINT fk_audit_logs_org FOREIGN KEY (org_id) REFERENCES organizations(id),
    ADD CONSTRAINT fk_audit_logs_user FOREIGN KEY (user_id) REFERENCES users(id);

CREATE INDEX idx_audit_logs_org_created ON audit_logs (org_id, created_at DESC, id DESC);
CREATE INDEX idx_audit_logs_resource ON audit_logs (org_id, resource_type, resource_id, created_at DESC);
CREATE INDEX idx_audit_logs_user ON audit_logs (org_id, user_id, created_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX idx_audit_logs_action ON audit_logs (org_id, action, created_at DESC);
CREATE INDEX idx_audit_logs_session ON audit_logs (session_id, created_at DESC) WHERE session_id IS NOT NULL;
CREATE INDEX idx_audit_logs_project ON audit_logs (project_id, created_at DESC) WHERE project_id IS NOT NULL;

SELECT create_monthly_partitions('audit_logs', '2025-01-01'::date,
    (date_trunc('month', now()::date) + interval '3 months')::date);

CREATE TABLE audit_logs_default PARTITION OF audit_logs DEFAULT;

INSERT INTO audit_logs (id, org_id, actor_type, actor_id, user_id, action, resource_type, resource_id, details, request_id, ip_address, user_agent, session_id, project_id, created_at)
SELECT id, org_id, actor_type, actor_id, user_id, action, resource_type, resource_id, details, request_id, ip_address, user_agent, session_id, project_id, created_at
FROM audit_logs_old;

DO $$ BEGIN
    IF (SELECT count(*) FROM audit_logs) != (SELECT count(*) FROM audit_logs_old) THEN
        RAISE EXCEPTION 'audit_logs row count mismatch after partition copy';
    END IF;
END $$;

SELECT setval('audit_logs_id_seq', COALESCE((SELECT MAX(id) FROM audit_logs), 1));

DROP TABLE audit_logs_old;

-- Recreate immutability trigger on the new partitioned table.
CREATE TRIGGER audit_logs_immutable
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_logs_modification();

-- =============================================================================
-- Partition-aware retention for audit_logs.
-- For partitioned tables, dropping old partitions is far more efficient than
-- row-level deletes. This function drops partitions whose entire range
-- falls before the retention cutoff.
-- =============================================================================
CREATE OR REPLACE FUNCTION drop_expired_audit_log_partitions(retention_days int)
RETURNS int
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    cutoff date;
    rec record;
    dropped int := 0;
BEGIN
    IF retention_days <= 0 THEN
        RAISE EXCEPTION 'retention_days must be positive, got %', retention_days;
    END IF;

    cutoff := (now() - make_interval(days => retention_days))::date;

    -- Find partitions whose upper bound is before the cutoff.
    -- Note: this drops data for ALL orgs in that partition. For per-org
    -- retention, continue using the row-level delete_expired_audit_logs function.
    FOR rec IN
        SELECT inhrelid::regclass::text AS partition_name,
               pg_get_expr(c.relpartbound, c.oid) AS bound_expr
        FROM pg_inherits
        JOIN pg_class c ON c.oid = inhrelid
        WHERE inhparent = 'audit_logs'::regclass
          AND c.relname != 'audit_logs_default'
        ORDER BY c.relname
    LOOP
        IF (regexp_match(rec.bound_expr, $re$TO \('([^']+)'\)$re$))[1]::date <= cutoff THEN
            EXECUTE format('DROP TABLE %s', rec.partition_name);
            dropped := dropped + 1;
        END IF;
    END LOOP;

    RETURN dropped;
END;
$$;
