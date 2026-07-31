DROP INDEX IF EXISTS idx_session_human_input_requests_activity_phase;
DROP INDEX IF EXISTS idx_session_messages_activity_phase;
DROP INDEX IF EXISTS idx_session_logs_activity_phase;

ALTER TABLE session_human_input_requests DROP COLUMN IF EXISTS activity_phase_id;
ALTER TABLE session_messages DROP COLUMN IF EXISTS activity_phase_id;
ALTER TABLE session_logs DROP COLUMN IF EXISTS activity_phase_id;
