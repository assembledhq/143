-- Passive representation only. NULL means no recorded classification or proof;
-- it does not classify an existing request as legacy, ready or version zero.
-- Canonical proof publication, immutability, enforcement and activation remain
-- separate work. No existing writer populates or interprets these columns.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_human_input_requests
    ADD COLUMN legacy_request boolean,
    ADD COLUMN proof_policy_version integer,
    ADD COLUMN enforcement_version integer,
    ADD COLUMN classification_version integer,
    ADD COLUMN readiness_state text,
    ADD COLUMN readiness_deadline_at timestamptz,
    ADD COLUMN readiness_proof jsonb;
