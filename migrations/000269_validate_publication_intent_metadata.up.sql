-- Validate in a separate transaction so table scans do not extend the
-- ACCESS EXCLUSIVE lock lifetime of migration 000267.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_initiator_scope_fkey;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_trigger_kind_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_handoff_mode_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_automatic_policy_source_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_review_policy_source_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_agent_ready_source_check;
