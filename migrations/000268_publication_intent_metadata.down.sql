SET LOCAL lock_timeout = '5s';

ALTER TABLE session_publications
    DROP CONSTRAINT IF EXISTS session_publications_agent_ready_source_check,
    DROP CONSTRAINT IF EXISTS session_publications_review_policy_source_check,
    DROP CONSTRAINT IF EXISTS session_publications_automatic_policy_source_check,
    DROP CONSTRAINT IF EXISTS session_publications_handoff_mode_check,
    DROP CONSTRAINT IF EXISTS session_publications_trigger_kind_check,
    DROP CONSTRAINT IF EXISTS session_publications_initiator_scope_fkey,
    DROP COLUMN IF EXISTS review_policy_source,
    DROP COLUMN IF EXISTS automatic_pr_policy_source,
    DROP COLUMN IF EXISTS initiated_by_user_id,
    DROP COLUMN IF EXISTS handoff_mode,
    DROP COLUMN IF EXISTS trigger_kind;

-- users_id_org_id_key is owned by migration 000266 and dropped there.
