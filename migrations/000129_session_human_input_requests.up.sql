CREATE TABLE session_human_input_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id uuid REFERENCES session_threads(id) ON DELETE SET NULL,
    turn_number int NOT NULL DEFAULT 0,
    agent_type text NOT NULL,
    provider_request_id text,
    request_kind text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    title text NOT NULL,
    body text NOT NULL,
    context text,
    blocks_phase text,
    choices jsonb NOT NULL DEFAULT '[]'::jsonb,
    response_schema jsonb,
    provider_payload jsonb,
    answer_text text,
    answer_payload jsonb,
    answered_by uuid REFERENCES users(id) ON DELETE SET NULL,
    answered_at timestamptz,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_human_input_requests_request_kind CHECK (
        request_kind IN (
            'free_text',
            'single_choice',
            'multi_choice',
            'tool_approval',
            'action_choice'
        )
    ),
    CONSTRAINT chk_session_human_input_requests_status CHECK (
        status IN (
            'pending',
            'answered',
            'cancelled',
            'expired',
            'superseded'
        )
    )
);

CREATE INDEX idx_session_human_input_requests_session_status
    ON session_human_input_requests (org_id, session_id, status, created_at);

CREATE INDEX idx_session_human_input_requests_thread_status
    ON session_human_input_requests (org_id, session_id, thread_id, status);

CREATE UNIQUE INDEX idx_session_human_input_requests_provider_request
    ON session_human_input_requests (org_id, session_id, provider_request_id)
    WHERE provider_request_id IS NOT NULL;
