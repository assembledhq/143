-- Reverse session_logs partitioning and drop helper functions.

-- Verify the source table exists and is partitioned before proceeding.
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'session_logs' AND relkind = 'p') THEN
        RAISE EXCEPTION 'session_logs is not a partitioned table — down migration may have already run';
    END IF;
END $$;

CREATE TABLE session_logs_backup AS SELECT * FROM session_logs;

DROP TABLE session_logs CASCADE;

CREATE TABLE session_logs (
    id           bigserial   PRIMARY KEY,
    session_id   uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    org_id       uuid        NOT NULL REFERENCES organizations(id),
    timestamp    timestamptz NOT NULL DEFAULT now(),
    level        text        NOT NULL DEFAULT 'info',
    message      text        NOT NULL,
    metadata     jsonb,
    turn_number  int         NOT NULL DEFAULT 0,
    thread_id    uuid        REFERENCES session_threads(id) ON DELETE CASCADE
);

INSERT INTO session_logs (id, session_id, org_id, timestamp, level, message, metadata, turn_number, thread_id)
SELECT id, session_id, org_id, timestamp, level, message, metadata, turn_number, thread_id
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

-- Drop helper functions created in this migration's up.
-- NOTE: drop_expired_audit_log_partitions is created in 000040, not here —
-- it is dropped in 000040's down migration.
DROP FUNCTION IF EXISTS ensure_future_partitions(text, int);
DROP FUNCTION IF EXISTS create_monthly_partitions(text, date, date);
