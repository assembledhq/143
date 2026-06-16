ALTER TABLE session_human_input_requests
    ADD COLUMN IF NOT EXISTS assigned_user_id uuid REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE session_human_input_requests
    ADD COLUMN IF NOT EXISTS sensitivity text;

UPDATE session_human_input_requests
SET sensitivity = 'team'
WHERE sensitivity IS NULL;

ALTER TABLE session_human_input_requests
    ALTER COLUMN sensitivity SET DEFAULT 'team',
    ALTER COLUMN sensitivity SET NOT NULL;

ALTER TABLE session_human_input_requests
    ADD COLUMN IF NOT EXISTS preferred_channel text;

UPDATE session_human_input_requests
SET preferred_channel = 'slack_thread'
WHERE preferred_channel IS NULL;

ALTER TABLE session_human_input_requests
    ALTER COLUMN preferred_channel SET DEFAULT 'slack_thread',
    ALTER COLUMN preferred_channel SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_session_human_input_requests_sensitivity'
    ) THEN
        ALTER TABLE session_human_input_requests
            ADD CONSTRAINT chk_session_human_input_requests_sensitivity
            CHECK (sensitivity IN ('team', 'personal', 'sensitive'));
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_session_human_input_requests_preferred_channel'
    ) THEN
        ALTER TABLE session_human_input_requests
            ADD CONSTRAINT chk_session_human_input_requests_preferred_channel
            CHECK (preferred_channel IN ('slack_thread', 'slack_dm', 'web'));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_session_human_input_requests_assigned_pending
    ON session_human_input_requests (org_id, assigned_user_id, created_at)
    WHERE status = 'pending' AND assigned_user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_slack_inbound_events_payload_retention
    ON slack_inbound_events (received_at)
    WHERE payload <> '{}'::jsonb;
