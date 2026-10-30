SET LOCAL lock_timeout = '5s';
CREATE TRIGGER trg_cutovers_retain_lifecycle_protocol
BEFORE INSERT OR UPDATE OR DELETE ON session_lifecycle_cutovers
FOR EACH ROW EXECUTE FUNCTION lifecycle143_retain_cutover_protocol();
CREATE TRIGGER trg_cutovers_retain_lifecycle_protocol_truncate
BEFORE TRUNCATE ON session_lifecycle_cutovers
FOR EACH STATEMENT EXECUTE FUNCTION lifecycle143_reject_active_protocol_truncate();
