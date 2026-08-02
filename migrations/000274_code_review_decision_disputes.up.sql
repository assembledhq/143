CREATE UNIQUE INDEX idx_sessions_org_id_id_for_code_review_disputes
    ON sessions (org_id, id);
CREATE UNIQUE INDEX idx_pull_requests_org_id_id_for_code_review_disputes
    ON pull_requests (org_id, id);
CREATE UNIQUE INDEX idx_repositories_org_id_id_for_code_review_disputes
    ON repositories (org_id, id);
CREATE UNIQUE INDEX idx_code_review_policies_org_id_id_for_disputes
    ON code_review_policies (org_id, id);

CREATE TABLE code_review_decision_disputes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL,
    pull_request_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    policy_id uuid NOT NULL,
    reviewed_head_sha text NOT NULL,
    decision text NOT NULL CHECK (decision IN ('approved', 'comment_only', 'needs_human_review', 'blocked')),
    direction text CHECK (direction IS NULL OR direction IN ('should_have_approved', 'should_not_have_approved')),
    filed_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    filed_by_login text NOT NULL DEFAULT '',
    author_association text NOT NULL DEFAULT '',
    author_is_pr_author boolean NOT NULL DEFAULT false,
    repository_visibility text NOT NULL DEFAULT 'unknown'
        CHECK (repository_visibility IN ('public', 'private', 'unknown')),
    membership_evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    trust_override boolean,
    source text NOT NULL CHECK (source IN ('github_comment', 'app_ui', 'api', 'spot_check')),
    github_comment_id bigint,
    github_thread_root_comment_id bigint,
    reply_comment_id bigint,
    source_body_hash text NOT NULL,
    source_version bigint NOT NULL DEFAULT 1 CHECK (source_version > 0),
    body text NOT NULL CHECK (length(btrim(body)) > 0 AND char_length(body) <= 8000),
    contested_reason_codes text[] NOT NULL DEFAULT '{}',
    dispute_kind text,
    asserts_new_information boolean,
    routing text CHECK (routing IS NULL OR routing IN ('reassess', 'policy_signal_only', 'answer_only', 'not_a_dispute')),
    intake_status text NOT NULL DEFAULT 'pending'
        CHECK (intake_status IN ('pending', 'triaged', 'discarded', 'failed')),
    intake_confidence numeric CHECK (intake_confidence IS NULL OR (intake_confidence >= 0 AND intake_confidence <= 1)),
    reassessment_session_id uuid,
    reassessment_decision text CHECK (reassessment_decision IS NULL OR reassessment_decision IN ('approved', 'comment_only', 'needs_human_review', 'blocked')),
    reassessment_flipped boolean,
    reassessment_status text NOT NULL DEFAULT 'not_requested'
        CHECK (reassessment_status IN ('not_requested', 'queued', 'running', 'completed', 'deduped', 'head_changed', 'failed')),
    semantic_input_hash_at_filing text NOT NULL,
    semantic_input_hash_at_rerun text,
    adjudication_status text
        CHECK (adjudication_status IS NULL OR adjudication_status IN ('pending', 'upheld', 'rejected', 'expired', 'needs_context')),
    adjudicated_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    adjudicated_at timestamptz,
    adjudication_note text,
    escalated_at timestamptz,
    escalated_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    queue_signals jsonb NOT NULL DEFAULT '{}'::jsonb,
    queue_priority numeric NOT NULL DEFAULT 0,
    reply_status text NOT NULL DEFAULT 'pending'
        CHECK (reply_status IN ('pending', 'not_applicable', 'published', 'failed')),
    reply_cycle_reserved boolean NOT NULL DEFAULT false,
    status_detail text,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (intake_status <> 'triaged' OR (direction IS NOT NULL AND routing IS NOT NULL AND asserts_new_information IS NOT NULL)),
    CHECK (adjudication_status IS NULL OR (
        intake_status = 'triaged'
        AND routing IN ('reassess', 'policy_signal_only')
        AND direction IS NOT NULL
    )),
    CHECK (adjudication_status NOT IN ('upheld', 'rejected', 'needs_context') OR (adjudicated_by_user_id IS NOT NULL AND adjudicated_at IS NOT NULL)),
    FOREIGN KEY (org_id, session_id)
        REFERENCES sessions(org_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (org_id, pull_request_id)
        REFERENCES pull_requests(org_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (org_id, repository_id)
        REFERENCES repositories(org_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (org_id, policy_id)
        REFERENCES code_review_policies(org_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (org_id, reassessment_session_id)
        REFERENCES sessions(org_id, id) ON DELETE SET NULL (reassessment_session_id)
);

CREATE UNIQUE INDEX idx_code_review_disputes_org_id_id
    ON code_review_decision_disputes (org_id, id);

CREATE UNIQUE INDEX idx_code_review_disputes_github_source
    ON code_review_decision_disputes (org_id, github_comment_id, source_version)
    WHERE github_comment_id IS NOT NULL;

CREATE INDEX idx_code_review_disputes_pending_intake
    ON code_review_decision_disputes (org_id, created_at, id)
    WHERE intake_status = 'pending';

CREATE INDEX idx_code_review_disputes_session
    ON code_review_decision_disputes (org_id, session_id, created_at DESC, id DESC);

CREATE INDEX idx_code_review_disputes_queue
    ON code_review_decision_disputes (org_id, repository_id, queue_priority DESC, id)
    WHERE adjudication_status = 'pending';

CREATE INDEX idx_code_review_disputes_upheld
    ON code_review_decision_disputes (org_id, adjudicated_at DESC)
    WHERE adjudication_status = 'upheld';

CREATE TABLE code_review_dispute_authorizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    dispute_id uuid NOT NULL,
    action text NOT NULL CHECK (action IN ('rerun', 'queue_influence', 'admin_promotion')),
    trusted boolean NOT NULL,
    observed_inputs jsonb NOT NULL DEFAULT '{}'::jsonb,
    policy_version integer,
    evaluator_version text NOT NULL,
    override_value boolean,
    override_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    decision_reason text NOT NULL,
    decided_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (org_id, dispute_id)
        REFERENCES code_review_decision_disputes(org_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_code_review_dispute_authorizations_dispute
    ON code_review_dispute_authorizations (org_id, dispute_id, decided_at, id);

CREATE UNIQUE INDEX idx_code_review_dispute_authorizations_machine_action
    ON code_review_dispute_authorizations (org_id, dispute_id, action, evaluator_version)
    WHERE action IN ('rerun', 'queue_influence');

CREATE TABLE code_review_dispute_escalations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    dispute_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    note text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (org_id, dispute_id)
        REFERENCES code_review_decision_disputes(org_id, id) ON DELETE CASCADE,
    UNIQUE (org_id, dispute_id, user_id)
);

CREATE INDEX idx_code_review_dispute_escalations_dispute
    ON code_review_dispute_escalations (org_id, dispute_id, created_at, id);

CREATE TABLE code_review_reassessment_admissions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    dispute_id uuid NOT NULL,
    pull_request_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    semantic_input_hash text NOT NULL,
    status text NOT NULL CHECK (status IN ('admitted', 'deduped', 'denied')),
    denial_reason text,
    admitted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (org_id, dispute_id)
        REFERENCES code_review_decision_disputes(org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, pull_request_id)
        REFERENCES pull_requests(org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, repository_id)
        REFERENCES repositories(org_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_code_review_reassessment_dispute_once
    ON code_review_reassessment_admissions (org_id, dispute_id);

CREATE INDEX idx_code_review_reassessment_semantic_input
    ON code_review_reassessment_admissions (org_id, pull_request_id, semantic_input_hash, created_at DESC);

CREATE INDEX idx_code_review_reassessment_dispute
    ON code_review_reassessment_admissions (org_id, dispute_id, created_at DESC);

ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT chk_code_review_session_metadata_trigger_source,
    ADD CONSTRAINT chk_code_review_session_metadata_trigger_source
        CHECK (trigger_source IN ('app_reviewer', 'alias_reviewer', 'team_reviewer',
                                  'slash_command', 'auto_policy', 'dispute_reassessment')),
    ADD COLUMN triggering_dispute_id uuid,
    ADD CONSTRAINT fk_code_review_metadata_triggering_dispute_org
        FOREIGN KEY (org_id, triggering_dispute_id)
        REFERENCES code_review_decision_disputes(org_id, id)
        ON DELETE SET NULL (triggering_dispute_id);

ALTER TABLE pull_requests
    ADD COLUMN code_review_dispute_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN code_review_dispute_cycles_in_epoch integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT chk_pr_code_review_dispute_cycles
        CHECK (code_review_dispute_cycles_in_epoch >= 0);
