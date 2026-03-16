DROP FUNCTION IF EXISTS delete_expired_audit_logs(uuid, int);
DROP TRIGGER IF EXISTS audit_logs_immutable ON audit_logs;
DROP FUNCTION IF EXISTS prevent_audit_logs_modification();
DROP TABLE IF EXISTS audit_logs;
ALTER TABLE IF EXISTS audit_log_legacy RENAME TO audit_log;

-- Re-create the immutability function in case it was dropped.
CREATE OR REPLACE FUNCTION prevent_audit_log_modification()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % operations are not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_immutable
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_log_modification();
