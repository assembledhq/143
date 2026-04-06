-- Partition audit_logs by created_at.
-- IMPORTANT: Run during maintenance window. See 000037 for helper function definitions.

-- Guard: use a short lock_timeout to prevent accidental execution outside
-- a maintenance window. If the lock cannot be acquired in 5s, the migration
-- fails fast rather than blocking production traffic indefinitely.
SET LOCAL lock_timeout = '5s';

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

CREATE INDEX IF NOT EXISTS idx_audit_logs_org_created ON audit_logs (org_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_resource ON audit_logs (org_id, resource_type, resource_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_user ON audit_logs (org_id, user_id, created_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_audit_logs_action ON audit_logs (org_id, action, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_session ON audit_logs (session_id, created_at DESC) WHERE session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_audit_logs_project ON audit_logs (project_id, created_at DESC) WHERE project_id IS NOT NULL;

SELECT create_monthly_partitions('audit_logs',
    COALESCE((SELECT date_trunc('month', MIN(created_at))::date FROM audit_logs_old), '2025-01-01'::date),
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

-- Partition-aware retention for audit_logs.
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

    FOR rec IN
        SELECT c.oid,
               inhrelid::regclass::text AS partition_name,
               pg_get_expr(c.relpartbound, c.oid) AS bound_expr
        FROM pg_inherits
        JOIN pg_class c ON c.oid = inhrelid
        WHERE inhparent = 'audit_logs'::regclass
          AND c.relname != 'audit_logs_default'
        ORDER BY c.relname
    LOOP
        -- Extract the upper bound date from the partition range expression.
        -- Use a sub-select to safely handle unexpected expression formats.
        DECLARE
            upper_bound date;
        BEGIN
            upper_bound := (regexp_match(rec.bound_expr, $re$TO \('([^']+)'\)$re$))[1]::date;
            IF upper_bound <= cutoff THEN
                EXECUTE format('DROP TABLE %s', rec.partition_name);
                dropped := dropped + 1;
            END IF;
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'Skipping partition % — could not parse bound: %', rec.partition_name, rec.bound_expr;
        END;
    END LOOP;

    RETURN dropped;
END;
$$;
