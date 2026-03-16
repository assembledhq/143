DROP INDEX IF EXISTS idx_sessions_snapshot_cleanup;
ALTER TABLE session_logs DROP COLUMN IF EXISTS turn_number;
DROP TABLE IF EXISTS session_messages;
ALTER TABLE sessions
    DROP COLUMN IF EXISTS agent_session_id,
    DROP COLUMN IF EXISTS current_turn,
    DROP COLUMN IF EXISTS last_activity_at,
    DROP COLUMN IF EXISTS sandbox_state,
    DROP COLUMN IF EXISTS snapshot_key;
