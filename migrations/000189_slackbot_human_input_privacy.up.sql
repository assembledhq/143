ALTER TABLE session_human_input_requests
    ADD COLUMN assigned_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN sensitivity text NOT NULL DEFAULT 'team',
    ADD COLUMN preferred_channel text NOT NULL DEFAULT 'slack_thread',
    ADD CONSTRAINT chk_session_human_input_requests_sensitivity
        CHECK (sensitivity IN ('team', 'personal', 'sensitive')),
    ADD CONSTRAINT chk_session_human_input_requests_preferred_channel
        CHECK (preferred_channel IN ('slack_thread', 'slack_dm', 'web'));

CREATE INDEX idx_session_human_input_requests_assigned_pending
    ON session_human_input_requests (org_id, assigned_user_id, created_at)
    WHERE status = 'pending' AND assigned_user_id IS NOT NULL;

CREATE INDEX idx_slack_inbound_events_payload_retention
    ON slack_inbound_events (received_at)
    WHERE payload <> '{}'::jsonb;
