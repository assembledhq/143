-- Assessment identity is separate from the (possibly reused) review session.
-- Composite parent keys make tenant and metadata ownership enforceable by FKs.
CREATE UNIQUE INDEX code_review_policies_org_id ON code_review_policies(org_id, id);
CREATE UNIQUE INDEX code_review_metadata_identity ON code_review_session_metadata(org_id, id, session_id, repository_id, pull_request_id, policy_id);

CREATE TABLE code_review_revision_assessments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    repository_id uuid NOT NULL,
    repository_full_name text NOT NULL,
    pull_request_id uuid NOT NULL,
    metadata_id uuid NOT NULL,
    session_id uuid NOT NULL,
    policy_id uuid NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    conversation_id uuid,
    source_assessment_id uuid,
    previous_assessment_id uuid,
    previous_published_assessment_id uuid,
    superseded_by_assessment_id uuid,
    base_sha text NOT NULL CHECK (base_sha <> ''),
    base_ref text NOT NULL CHECK (base_ref <> ''),
    head_sha text NOT NULL CHECK (head_sha <> ''),
    input_version integer NOT NULL CHECK (input_version > 0),
    code_digest text NOT NULL CHECK (code_digest <> ''),
    contract_digest text NOT NULL CHECK (contract_digest <> ''),
    intent_digest text NOT NULL CHECK (intent_digest <> ''),
    visual_digest text NOT NULL CHECK (visual_digest <> ''),
    request_digest text NOT NULL CHECK (request_digest <> ''),
    gate_digest text NOT NULL CHECK (gate_digest <> ''),
    input_digest text NOT NULL CHECK (input_digest <> ''),
    input_manifest jsonb NOT NULL CHECK (jsonb_typeof(input_manifest) = 'object'),
    review_scope text NOT NULL CHECK (review_scope IN ('full','evidence_only')),
    route_reason text NOT NULL CHECK (route_reason IN ('initial_full','visual_changed','evidence_changed','checks_changed','no_evidence_change','code_changed','contract_changed','intent_changed','request_changed','gates_changed','no_complete_baseline','baseline_not_visual_only','evidence_validation_failed','inputs_unavailable','force_fresh','dispute')),
    result_origin text CHECK (result_origin IN ('executed','reused','evidence_only')),
    coverage_complete boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'reserved' CHECK (status IN ('reserved','running','publishing','completed','superseded','failed','cancelled')),
    decision text CHECK (decision IN ('approved','comment_only','needs_human_review','blocked')),
    acceptable boolean,
    risk_reason_details jsonb,
    structured_outcome jsonb,
    rendered_body text,
    failure_detail text,
    publication_key text NOT NULL CHECK (publication_key <> ''),
    publication_state text NOT NULL DEFAULT 'not_started' CHECK (publication_state IN ('not_started','reserved','uncertain','confirmed','not_required')),
    publication_receipt jsonb,
    github_review_id bigint,
    github_review_url text,
    submitted_commit_sha text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    superseded_at timestamptz,
    -- Names are immutable capture provenance; live repositories and PRs can be
    -- renamed. Tenant/id keys and the metadata relationship enforce ownership.
    CONSTRAINT code_review_assessment_repository_fk FOREIGN KEY (org_id,repository_id) REFERENCES repositories(org_id,id),
    CONSTRAINT code_review_assessment_pr_fk FOREIGN KEY (org_id,pull_request_id) REFERENCES pull_requests(org_id,id),
    CONSTRAINT code_review_assessment_metadata_fk FOREIGN KEY (org_id,metadata_id,session_id,repository_id,pull_request_id,policy_id) REFERENCES code_review_session_metadata(org_id,id,session_id,repository_id,pull_request_id,policy_id),
    CONSTRAINT code_review_assessment_scope_source CHECK ((review_scope='full' AND source_assessment_id IS NULL) OR (review_scope='evidence_only' AND source_assessment_id IS NOT NULL)),
    CONSTRAINT code_review_assessment_terminal_time CHECK ((status IN ('completed','superseded','failed','cancelled')) = (completed_at IS NOT NULL)),
    CONSTRAINT code_review_assessment_outcome CHECK (status <> 'completed' OR (result_origin IS NOT NULL AND decision IS NOT NULL AND acceptable IS NOT NULL AND structured_outcome IS NOT NULL)),
    UNIQUE (org_id,id),
    UNIQUE (org_id,session_id,id),
    UNIQUE (org_id,id,session_id,repository_id,pull_request_id,policy_id),
    UNIQUE (org_id,pull_request_id,id),
    UNIQUE (org_id,pull_request_id,generation),
    UNIQUE (org_id,publication_key)
);

ALTER TABLE code_review_revision_assessments ADD CONSTRAINT code_review_assessment_source_fk
    FOREIGN KEY (org_id,pull_request_id,source_assessment_id) REFERENCES code_review_revision_assessments(org_id,pull_request_id,id);
ALTER TABLE code_review_revision_assessments ADD CONSTRAINT code_review_assessment_previous_fk
    FOREIGN KEY (org_id,pull_request_id,previous_assessment_id) REFERENCES code_review_revision_assessments(org_id,pull_request_id,id);
ALTER TABLE code_review_revision_assessments ADD CONSTRAINT code_review_assessment_previous_published_fk
    FOREIGN KEY (org_id,pull_request_id,previous_published_assessment_id) REFERENCES code_review_revision_assessments(org_id,pull_request_id,id);
ALTER TABLE code_review_revision_assessments ADD CONSTRAINT code_review_assessment_superseded_by_fk
    FOREIGN KEY (org_id,pull_request_id,superseded_by_assessment_id) REFERENCES code_review_revision_assessments(org_id,pull_request_id,id);
CREATE UNIQUE INDEX code_review_assessments_active_pr ON code_review_revision_assessments(org_id,pull_request_id) WHERE status IN ('reserved','running','publishing');
CREATE INDEX code_review_assessments_pr_history ON code_review_revision_assessments(org_id,pull_request_id,created_at DESC,id);
CREATE INDEX code_review_assessments_session_full ON code_review_revision_assessments(org_id,session_id,created_at DESC,id) WHERE review_scope='full';
CREATE INDEX code_review_assessments_session_completed ON code_review_revision_assessments(org_id,session_id,generation DESC) WHERE status='completed';

ALTER TABLE code_review_session_metadata ADD COLUMN assessment_id uuid;
ALTER TABLE code_review_session_metadata ADD CONSTRAINT code_review_metadata_assessment_fk
    FOREIGN KEY (org_id,assessment_id,session_id,repository_id,pull_request_id,policy_id) REFERENCES code_review_revision_assessments(org_id,id,session_id,repository_id,pull_request_id,policy_id);
CREATE INDEX code_review_metadata_assessment ON code_review_session_metadata(org_id,assessment_id) WHERE assessment_id IS NOT NULL;

ALTER TABLE code_review_agent_results ADD COLUMN assessment_id uuid;
ALTER TABLE code_review_agent_results ADD CONSTRAINT code_review_agent_results_assessment_fk
    FOREIGN KEY (org_id,session_id,assessment_id) REFERENCES code_review_revision_assessments(org_id,session_id,id);
CREATE INDEX code_review_agent_results_assessment ON code_review_agent_results(org_id,assessment_id,created_at,id) WHERE assessment_id IS NOT NULL;

ALTER TABLE code_review_findings ADD COLUMN assessment_id uuid;
ALTER TABLE code_review_findings ADD CONSTRAINT code_review_findings_assessment_fk
    FOREIGN KEY (org_id,session_id,assessment_id) REFERENCES code_review_revision_assessments(org_id,session_id,id);
-- Retain the legacy unique index during the schema-first rollout. Existing
-- binaries still target it with ON CONFLICT; new assessment writers must use
-- assessment-qualified dedupe keys until the old index can be retired.
CREATE UNIQUE INDEX code_review_findings_assessment_dedupe ON code_review_findings(org_id,assessment_id,dedupe_key) WHERE assessment_id IS NOT NULL;

ALTER TABLE code_review_prompt_records ADD COLUMN assessment_id uuid;
ALTER TABLE code_review_prompt_records ADD CONSTRAINT code_review_prompt_records_assessment_fk
    FOREIGN KEY (org_id,session_id,assessment_id) REFERENCES code_review_revision_assessments(org_id,session_id,id);
CREATE INDEX code_review_prompt_records_assessment ON code_review_prompt_records(org_id,assessment_id,created_at,id) WHERE assessment_id IS NOT NULL;
