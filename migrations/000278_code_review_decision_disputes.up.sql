-- Composite (org_id, id) unique indexes are the referenced-key requirement for
-- the tenant-scoped foreign keys below. They are built on hot parent tables,
-- and a plain CREATE UNIQUE INDEX holds a SHARE lock for the whole build, which
-- blocks writes to sessions and pull_requests until it finishes.
-- NOTE: For production, create these four indexes with CONCURRENTLY before
-- running the migration; IF NOT EXISTS then makes this step a no-op. If such a
-- pre-build failed it leaves an INVALID index that IF NOT EXISTS will skip and
-- the composite foreign keys below will then fail with "no unique constraint
-- matching given keys" -- drop the invalid index before rerunning.
-- lock_timeout bounds how long each statement waits to ACQUIRE a lock. It does
-- not bound how long an acquired lock is held, so it makes this migration fail
-- fast against concurrent DDL rather than making the index builds cheap.
SET LOCAL lock_timeout = '5s';

CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_org_id_id_for_code_review_disputes
    ON sessions (org_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pull_requests_org_id_id_for_code_review_disputes
    ON pull_requests (org_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_repositories_org_id_id_for_code_review_disputes
    ON repositories (org_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_review_policies_org_id_id_for_disputes
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
    -- The provider's own last-edited timestamp for the source comment.
    -- source_version is a content hash and so carries no order, but deciding
    -- which of several disputes on one comment is the live one needs one:
    -- webhook redelivery can present an edit before the creation it replaced.
    source_updated_at timestamptz,
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
    -- Editing one GitHub comment files a new dispute, because source_version is
    -- content-derived. The replaced rows point here at the row that replaced
    -- them. This is deliberately its own column rather than a reply_status or
    -- intake_status value: those are lifecycle states that reassessment and
    -- triage transitions rewrite, and a retirement that a later transition can
    -- undo lets a stale objection republish over the live answer.
    superseded_by_dispute_id uuid CHECK (superseded_by_dispute_id <> id),
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

-- Added after the index rather than inline: a self-referencing composite key
-- needs its referenced unique index to already exist.
ALTER TABLE code_review_decision_disputes
    ADD CONSTRAINT fk_code_review_disputes_superseded_by
        FOREIGN KEY (org_id, superseded_by_dispute_id)
        REFERENCES code_review_decision_disputes(org_id, id)
        ON DELETE SET NULL (superseded_by_dispute_id);

CREATE UNIQUE INDEX idx_code_review_disputes_github_source
    ON code_review_decision_disputes (org_id, github_comment_id, source_version)
    WHERE github_comment_id IS NOT NULL;

CREATE INDEX idx_code_review_disputes_pending_intake
    ON code_review_decision_disputes (org_id, created_at, id)
    WHERE intake_status = 'pending';

CREATE INDEX idx_code_review_disputes_session
    ON code_review_decision_disputes (org_id, session_id, created_at DESC, id DESC);

-- The adjudication UI always asks for adjudication_status = 'pending', so that
-- stays a narrow partial index: resolved disputes accumulate forever and must
-- not sit in the hot path's index. repository_id is an optional filter and
-- cannot lead the key without breaking the ordering for the unfiltered case.
CREATE INDEX idx_code_review_disputes_queue_pending
    ON code_review_decision_disputes (org_id, queue_priority DESC, created_at DESC, id DESC)
    WHERE adjudication_status = 'pending';

-- Serves the API's repository-scoped adjudication list with the same ordering.
-- The unfiltered "any status" list still sorts: repository_id has to lead the
-- key to answer that filter, which leaves the ordering columns unreachable when
-- no repository is supplied.
CREATE INDEX idx_code_review_disputes_queue_repository
    ON code_review_decision_disputes (org_id, repository_id, queue_priority DESC, created_at DESC, id DESC)
    WHERE adjudication_status IS NOT NULL;

-- Backs the untrusted intake caps. Both ceilings are counted from one scan of
-- this pull request's window (the per-login figure is a FILTER over the same
-- rows, not a separate predicate), so the login does not belong in the key --
-- it could never serve as an access predicate and would only widen the index.
-- The per-pull-request ceiling bounds that scan.
CREATE INDEX idx_code_review_disputes_pull_request_intake
    ON code_review_decision_disputes (org_id, pull_request_id, created_at DESC)
    WHERE source = 'github_comment';

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

-- Adding the columns is metadata-only (non-volatile defaults), but validating
-- the CHECK scans every pull_requests row under ACCESS EXCLUSIVE.
-- ADD CONSTRAINT ... NOT VALID plus a separate VALIDATE would not help here:
-- golang-migrate's postgres driver executes this whole file as one statement,
-- so it runs in a single implicit transaction and the ACCESS EXCLUSIVE lock is
-- held until commit either way. Splitting the validation requires splitting the
-- migration, which is not worth it for a scan of a table this size.
ALTER TABLE pull_requests
    ADD COLUMN code_review_dispute_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN code_review_dispute_cycles_in_epoch integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT chk_pr_code_review_dispute_cycles
        CHECK (code_review_dispute_cycles_in_epoch >= 0);
