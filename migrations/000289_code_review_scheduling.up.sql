ALTER TABLE code_review_policies ADD COLUMN scheduling_policy jsonb NOT NULL DEFAULT '{}';

CREATE TABLE code_review_pr_state (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    repository_id uuid NOT NULL REFERENCES repositories(id),
    pull_request_id uuid NOT NULL REFERENCES pull_requests(id),
    generation bigint NOT NULL DEFAULT 0,
    automatic_paused boolean NOT NULL DEFAULT false,
    head_sha text NOT NULL DEFAULT '',
    base_sha text NOT NULL DEFAULT '',
    base_ref text NOT NULL DEFAULT '',
    is_draft boolean NOT NULL DEFAULT false,
    snapshot_observed_at timestamptz,
    last_material_change_at timestamptz,
    first_pending_at timestamptz,
    last_agent_start_at timestamptz,
    eligible_at timestamptz,
    retry_at timestamptz,
    active_session_id uuid REFERENCES sessions(id),
    pending_request_id uuid,
    pending_input jsonb,
    state text NOT NULL DEFAULT 'idle' CHECK (state IN ('idle','waiting','running','covered','paused','closed')),
    wait_reason text NOT NULL DEFAULT '' CHECK (wait_reason IN ('','quiet_period','minimum_interval','draft','manual_pause','policy_disabled','already_approved','active_review','context_unavailable')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id,pull_request_id)
);
CREATE INDEX idx_code_review_pr_state_due ON code_review_pr_state(org_id,eligible_at) WHERE pending_input IS NOT NULL;

CREATE TABLE code_review_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    repository_id uuid NOT NULL REFERENCES repositories(id),
    pull_request_id uuid NOT NULL REFERENCES pull_requests(id),
    source_kind text NOT NULL,
    source_identity text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('ensure_current','review_now')),
    requester_id uuid REFERENCES users(id),
    input_hash text NOT NULL,
    target_generation bigint NOT NULL,
    session_id uuid REFERENCES sessions(id),
    retry_of_session_id uuid REFERENCES sessions(id),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','joined','satisfied','superseded','cancelled','failed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id,source_kind,source_identity)
);
ALTER TABLE code_review_pr_state ADD CONSTRAINT code_review_pr_pending_request_fk FOREIGN KEY (pending_request_id) REFERENCES code_review_requests(id);
CREATE INDEX idx_code_review_requests_generation ON code_review_requests(org_id,pull_request_id,target_generation);
