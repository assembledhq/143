-- Roll back metadata readers before removing these passive columns.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_human_input_requests
    DROP COLUMN readiness_proof,
    DROP COLUMN readiness_deadline_at,
    DROP COLUMN readiness_state,
    DROP COLUMN classification_version,
    DROP COLUMN enforcement_version,
    DROP COLUMN proof_policy_version,
    DROP COLUMN legacy_request;
