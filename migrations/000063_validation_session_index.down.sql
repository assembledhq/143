DROP INDEX IF EXISTS idx_validations_session_org;
CREATE INDEX idx_validations_run ON validations (session_id);
