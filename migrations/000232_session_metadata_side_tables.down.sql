ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS linear_private BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS linear_state_sync_disabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS linear_identifier_hint TEXT,
    ADD COLUMN IF NOT EXISTS linear_prepare_state TEXT NOT NULL DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS pr_creation_state TEXT NOT NULL DEFAULT 'idle',
    ADD COLUMN IF NOT EXISTS pr_creation_error TEXT,
    ADD COLUMN IF NOT EXISTS pr_push_state TEXT NOT NULL DEFAULT 'idle',
    ADD COLUMN IF NOT EXISTS pr_push_error TEXT,
    ADD COLUMN IF NOT EXISTS pr_push_error_code TEXT,
    ADD COLUMN IF NOT EXISTS branch_creation_state TEXT NOT NULL DEFAULT 'idle',
    ADD COLUMN IF NOT EXISTS branch_creation_error TEXT,
    ADD COLUMN IF NOT EXISTS branch_url TEXT,
    ADD COLUMN IF NOT EXISTS capability_snapshot JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS git_identity_source TEXT,
    ADD COLUMN IF NOT EXISTS git_identity_user_id UUID REFERENCES users(id) ON DELETE SET NULL;

UPDATE sessions s
SET linear_private = slc.linear_private,
    linear_state_sync_disabled = slc.linear_state_sync_disabled,
    linear_identifier_hint = slc.linear_identifier_hint,
    linear_prepare_state = slc.linear_prepare_state
FROM session_linear_context slc
WHERE slc.org_id = s.org_id
  AND slc.session_id = s.id;

UPDATE sessions s
SET pr_creation_state = sps.pr_creation_state,
    pr_creation_error = sps.pr_creation_error,
    pr_push_state = sps.pr_push_state,
    pr_push_error = sps.pr_push_error,
    pr_push_error_code = sps.pr_push_error_code,
    branch_creation_state = sps.branch_creation_state,
    branch_creation_error = sps.branch_creation_error,
    branch_url = sps.branch_url
FROM session_publish_state sps
WHERE sps.org_id = s.org_id
  AND sps.session_id = s.id;

UPDATE sessions s
SET capability_snapshot = sem.capability_snapshot,
    git_identity_source = sem.git_identity_source,
    git_identity_user_id = sem.git_identity_user_id
FROM session_execution_metadata sem
WHERE sem.org_id = s.org_id
  AND sem.session_id = s.id;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_linear_prepare_state
        CHECK (linear_prepare_state IN ('none', 'pending', 'ready', 'failed')) NOT VALID,
    ADD CONSTRAINT chk_sessions_pr_creation_state
        CHECK (pr_creation_state IN ('idle', 'queued', 'pushing', 'succeeded', 'failed')) NOT VALID,
    ADD CONSTRAINT chk_sessions_pr_push_state
        CHECK (pr_push_state IN ('idle', 'queued', 'pushing', 'succeeded', 'failed')) NOT VALID,
    ADD CONSTRAINT chk_sessions_pr_push_error_code
        CHECK (pr_push_error_code IS NULL OR pr_push_error_code IN (
            'branch_diverged',
            'push_rejected',
            'sandbox_auth_unavailable',
            'generic'
        )) NOT VALID,
    ADD CONSTRAINT chk_sessions_branch_creation_state
        CHECK (branch_creation_state IN ('idle', 'queued', 'pushing', 'succeeded', 'failed')) NOT VALID,
    ADD CONSTRAINT chk_sessions_capability_snapshot_array
        CHECK (jsonb_typeof(capability_snapshot) = 'array') NOT VALID;

ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_linear_prepare_state;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_pr_creation_state;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_pr_push_state;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_pr_push_error_code;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_branch_creation_state;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_capability_snapshot_array;

CREATE INDEX IF NOT EXISTS idx_sessions_linear_identifier_hint
    ON sessions (org_id, linear_identifier_hint)
    WHERE linear_identifier_hint IS NOT NULL;

CREATE OR REPLACE FUNCTION sessions_linear_flags_immutable()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.linear_private IS DISTINCT FROM NEW.linear_private THEN
        RAISE EXCEPTION 'sessions.linear_private is immutable after create (session_id=%)', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.linear_state_sync_disabled IS DISTINCT FROM NEW.linear_state_sync_disabled THEN
        RAISE EXCEPTION 'sessions.linear_state_sync_disabled is immutable after create (session_id=%)', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER sessions_linear_flags_immutable_trigger
    BEFORE UPDATE OF linear_private, linear_state_sync_disabled ON sessions
    FOR EACH ROW
    EXECUTE FUNCTION sessions_linear_flags_immutable();

DROP TRIGGER IF EXISTS session_linear_context_flags_immutable_trigger ON session_linear_context;
DROP FUNCTION IF EXISTS session_linear_context_flags_immutable();

DROP TABLE IF EXISTS session_execution_metadata;
DROP TABLE IF EXISTS session_publish_state;
DROP TABLE IF EXISTS session_linear_context;
