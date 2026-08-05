ALTER TABLE code_review_decision_disputes
    ADD COLUMN policy_owner_active_seconds integer
    CHECK (policy_owner_active_seconds IS NULL OR policy_owner_active_seconds BETWEEN 0 AND 3600);

ALTER TABLE review_comments
    ADD COLUMN reviewer_type text NOT NULL DEFAULT '';

CREATE INDEX idx_code_review_disputes_repeat_reason_window
    ON code_review_decision_disputes (org_id, repository_id, created_at DESC)
    WHERE adjudication_status IS NOT NULL;
CREATE INDEX idx_code_review_disputes_contested_reasons
    ON code_review_decision_disputes USING gin (contested_reason_codes);
CREATE INDEX idx_code_review_disputes_rank_stale
    ON code_review_decision_disputes (org_id, updated_at, id)
    WHERE adjudication_status = 'pending';

CREATE TABLE code_review_dispute_queue_snapshots (
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    snapshot_id uuid NOT NULL,
    position bigint NOT NULL CHECK (position > 0),
    dispute_id uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, snapshot_id, position),
    UNIQUE (org_id, snapshot_id, dispute_id),
    FOREIGN KEY (org_id, dispute_id)
        REFERENCES code_review_decision_disputes(org_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_code_review_dispute_queue_snapshots_expiry
    ON code_review_dispute_queue_snapshots (org_id, expires_at);

CREATE TABLE code_review_pull_request_lifecycle_observations (
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    pull_request_id uuid NOT NULL,
    merged boolean NOT NULL,
    merged_at timestamptz,
    terminal boolean NOT NULL,
    observed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, pull_request_id),
    FOREIGN KEY (org_id, pull_request_id) REFERENCES pull_requests(org_id, id) ON DELETE CASCADE
);

CREATE TABLE code_review_decision_outcomes (
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL,
    pull_request_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    policy_id uuid NOT NULL,
    decision text NOT NULL CHECK (decision IN ('approved', 'comment_only', 'needs_human_review', 'blocked')),
    reason_codes text[] NOT NULL DEFAULT '{}',
    merged boolean NOT NULL DEFAULT false,
    merged_at timestamptz,
    independent_approver_login text,
    independent_blocking_review_login text,
    human_review_comment_count integer NOT NULL DEFAULT 0 CHECK (human_review_comment_count >= 0),
    terminal boolean NOT NULL DEFAULT false,
    lifecycle_observed_at timestamptz,
    observed_until timestamptz NOT NULL DEFAULT now(),
    provider_reconcile_attempted_at timestamptz,
    projection_updated_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, session_id),
    FOREIGN KEY (org_id, session_id) REFERENCES sessions(org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, pull_request_id) REFERENCES pull_requests(org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, repository_id) REFERENCES repositories(org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, policy_id) REFERENCES code_review_policies(org_id, id) ON DELETE RESTRICT
);

CREATE INDEX idx_code_review_decision_outcomes_pull_request
    ON code_review_decision_outcomes (org_id, pull_request_id, projection_updated_at DESC);
CREATE INDEX idx_code_review_decision_outcomes_repository
    ON code_review_decision_outcomes (org_id, repository_id, created_at DESC);
CREATE INDEX idx_code_review_decision_outcomes_reason_codes
    ON code_review_decision_outcomes USING gin (reason_codes);
CREATE INDEX idx_code_review_decision_outcomes_reconcile
    ON code_review_decision_outcomes (org_id, provider_reconcile_attempted_at, observed_until, session_id)
    WHERE terminal = false;

CREATE TABLE code_review_human_review_observations (
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    pull_request_id uuid NOT NULL,
    github_review_id bigint NOT NULL,
    reviewer_login text NOT NULL,
    reviewer_type text NOT NULL DEFAULT '',
    author_association text NOT NULL DEFAULT '',
    state text NOT NULL CHECK (state IN ('approved', 'changes_requested', 'dismissed')),
    independent boolean NOT NULL,
    active boolean NOT NULL DEFAULT true,
    observed_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, pull_request_id, github_review_id),
    FOREIGN KEY (org_id, pull_request_id) REFERENCES pull_requests(org_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_code_review_human_review_observations_active
    ON code_review_human_review_observations (org_id, pull_request_id, state, observed_at DESC, github_review_id DESC)
    WHERE active = true AND independent = true;
