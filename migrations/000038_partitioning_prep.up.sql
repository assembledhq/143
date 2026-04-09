-- Partitioning preparation for high-growth append-only tables.
--
-- Strategy: range partition by time column (monthly) on the three highest-volume
-- append-only tables. We migrate each table by:
--   1. Creating a new partitioned table with the same schema
--   2. Copying existing data into it
--   3. Swapping the old table out and the new table in
--   4. Creating initial partitions covering past data + the next 3 months
--
-- IMPORTANT: MAINTENANCE-WINDOW-ONLY MIGRATION.
-- This migration copies data and swaps tables while holding an ACCESS EXCLUSIVE
-- lock for the entire duration (not just acquisition). The lock_timeout below
-- only guards *acquiring* the lock — once held, the copy can take minutes to
-- hours on large tables. For production, consider:
--   (a) create partitioned table,
--   (b) copy data in batches outside a transaction,
--   (c) swap with a brief lock.
-- Or use pg_partman / logical replication for zero-downtime migration.
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

-- Guard: use a short lock_timeout to prevent accidental execution outside
-- a maintenance window. If the lock cannot be acquired in 5s, the migration
-- fails fast rather than blocking production traffic indefinitely.
SET LOCAL lock_timeout = '5s';

-- Lock table to prevent concurrent writes during the swap.
LOCK TABLE session_logs IN ACCESS EXCLUSIVE MODE;

-- 1. Rename existing table, its indexes, and its backing sequence.
--    bigserial columns own a *_id_seq sequence; renaming the table does NOT
--    rename the sequence, so the subsequent CREATE TABLE would collide.
ALTER TABLE session_logs RENAME TO session_logs_old;
-- The sequence kept its original name (agent_run_logs_id_seq) when the table
-- was renamed from agent_run_logs to session_logs in migration 000015.
-- The sequence may be named agent_run_logs_id_seq (original) or
-- session_logs_id_seq (if already renamed). Handle both cases.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'agent_run_logs_id_seq' AND relkind = 'S') THEN
        ALTER SEQUENCE agent_run_logs_id_seq RENAME TO agent_run_logs_id_seq_old;
    ELSIF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'session_logs_id_seq' AND relkind = 'S') THEN
        ALTER SEQUENCE session_logs_id_seq RENAME TO agent_run_logs_id_seq_old;
    END IF;
END $$;
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
CREATE INDEX IF NOT EXISTS idx_session_logs_session ON session_logs (session_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_session_logs_thread ON session_logs (thread_id) WHERE thread_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_logs_timestamp ON session_logs (timestamp);
CREATE INDEX IF NOT EXISTS idx_session_logs_org_created ON session_logs (org_id, timestamp DESC);

-- 5. Add the CHECK constraint from migration 034.
ALTER TABLE session_logs
    ADD CONSTRAINT chk_session_logs_level CHECK (level IN ('debug', 'info', 'warn', 'error', 'output', 'tool_use', 'question'));

-- 6. Create partitions: from 2025-01 through 3 months from now.
SELECT create_monthly_partitions('session_logs',
    COALESCE((SELECT date_trunc('month', MIN(timestamp))::date FROM session_logs_old), '2025-01-01'::date),
    (date_trunc('month', now()::date) + interval '3 months')::date);

-- 7. Default partition for data outside defined ranges.
CREATE TABLE session_logs_default PARTITION OF session_logs DEFAULT;

-- 8. Copy data from old table, backfilling org_id from sessions.
--    Orphaned rows (session deleted) are excluded by the JOIN.
INSERT INTO session_logs (id, session_id, org_id, timestamp, level, message, metadata, turn_number, thread_id)
SELECT sl.id, sl.session_id, s.org_id, sl.timestamp, sl.level, sl.message, sl.metadata, sl.turn_number, sl.thread_id
FROM session_logs_old sl
JOIN sessions s ON s.id = sl.session_id;

-- 9. Verify row count. The JOIN intentionally excludes orphaned rows (where the
--    parent session was deleted), so we count with the same join to get the expected total.
DO $$ BEGIN
    IF (SELECT count(*) FROM session_logs) !=
       (SELECT count(*) FROM session_logs_old sl JOIN sessions s ON s.id = sl.session_id) THEN
        RAISE EXCEPTION 'session_logs row count mismatch after partition copy';
    END IF;
END $$;

-- 10. Sync the sequence.
SELECT setval('session_logs_id_seq', COALESCE((SELECT MAX(id) FROM session_logs), 1));

-- 11. Drop old table.
DROP TABLE session_logs_old;
