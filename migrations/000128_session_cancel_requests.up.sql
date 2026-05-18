CREATE TABLE session_cancel_requests (
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    requested_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    PRIMARY KEY (org_id, session_id)
);

CREATE INDEX idx_session_cancel_requests_pending
    ON session_cancel_requests (org_id, session_id)
    WHERE delivered_at IS NULL;
