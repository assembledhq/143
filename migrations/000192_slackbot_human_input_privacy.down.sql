DROP INDEX IF EXISTS idx_slack_inbound_events_payload_retention;
DROP INDEX IF EXISTS idx_session_human_input_requests_assigned_pending;

ALTER TABLE session_human_input_requests
    DROP CONSTRAINT IF EXISTS chk_session_human_input_requests_preferred_channel,
    DROP CONSTRAINT IF EXISTS chk_session_human_input_requests_sensitivity,
    DROP COLUMN IF EXISTS preferred_channel,
    DROP COLUMN IF EXISTS sensitivity,
    DROP COLUMN IF EXISTS assigned_user_id;
