-- Add additive publication-intent metadata.
--
-- Every constraint here is added NOT VALID, so this migration only takes the
-- brief catalog-update ACCESS EXCLUSIVE lock on session_publications and never
-- scans the existing rows. Migration 000268 validates them in its own
-- transaction, where the scan runs under a lock that does not block reads or
-- writes. The composite FK's referenced key is created in 000266, which is
-- deliberately a separate migration so its index build does not extend this
-- transaction's lock.
--
-- The new columns are NOT NULL DEFAULT, which is metadata-only in Postgres 11+
-- (no table rewrite).
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_publications
    ADD COLUMN trigger_kind text NOT NULL DEFAULT 'policy',
    ADD COLUMN handoff_mode text NOT NULL DEFAULT 'pre_publish',
    ADD COLUMN initiated_by_user_id uuid,
    ADD COLUMN automatic_pr_policy_source text NOT NULL DEFAULT 'product_default',
    ADD COLUMN review_policy_source text NOT NULL DEFAULT 'product_default';

ALTER TABLE session_publications
    ADD CONSTRAINT session_publications_initiator_scope_fkey
        FOREIGN KEY (initiated_by_user_id, org_id)
        REFERENCES users(id, org_id) NOT VALID,
    ADD CONSTRAINT session_publications_trigger_kind_check
        CHECK (trigger_kind IN ('agent_ready', 'explicit_action', 'policy')) NOT VALID,
    ADD CONSTRAINT session_publications_handoff_mode_check
        CHECK (handoff_mode IN ('pre_publish', 'draft_first')) NOT VALID,
    ADD CONSTRAINT session_publications_automatic_policy_source_check
        CHECK (automatic_pr_policy_source IN (
            'product_default', 'organization', 'personal', 'automation',
            'explicit_action'
        )) NOT VALID,
    ADD CONSTRAINT session_publications_review_policy_source_check
        CHECK (review_policy_source IN (
            'product_default', 'organization', 'personal', 'automation',
            'explicit_bypass'
        )) NOT VALID,
    ADD CONSTRAINT session_publications_agent_ready_source_check
        CHECK (trigger_kind <> 'agent_ready' OR source = 'agent_tool') NOT VALID;
