-- One hot-table trigger installation per transaction. No backfill/activation.
SET LOCAL lock_timeout = '5s';
CREATE TRIGGER trg_sessions_retain_lifecycle_protocol
BEFORE INSERT OR UPDATE OR DELETE ON sessions
FOR EACH ROW EXECUTE FUNCTION lifecycle143_retain_session_protocol();
CREATE TRIGGER trg_sessions_retain_lifecycle_protocol_truncate
BEFORE TRUNCATE ON sessions
FOR EACH STATEMENT EXECUTE FUNCTION lifecycle143_reject_active_protocol_truncate();
