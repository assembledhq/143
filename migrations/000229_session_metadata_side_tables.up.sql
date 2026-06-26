CREATE TABLE session_linear_context (
    session_id                   UUID PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    org_id                       UUID NOT NULL REFERENCES organizations(id),
    linear_private               BOOLEAN NOT NULL DEFAULT false,
    linear_state_sync_disabled   BOOLEAN NOT NULL DEFAULT false,
    linear_identifier_hint       TEXT,
    linear_prepare_state         TEXT NOT NULL DEFAULT 'none',
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_linear_context_prepare_state
        CHECK (linear_prepare_state IN ('none', 'pending', 'ready', 'failed'))
);

CREATE UNIQUE INDEX idx_session_linear_context_org_session
    ON session_linear_context (org_id, session_id);

CREATE INDEX idx_session_linear_context_identifier_hint
    ON session_linear_context (org_id, linear_identifier_hint)
    WHERE linear_identifier_hint IS NOT NULL;

CREATE OR REPLACE FUNCTION session_linear_context_flags_immutable()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.linear_private IS DISTINCT FROM NEW.linear_private THEN
        RAISE EXCEPTION 'session_linear_context.linear_private is immutable after create (session_id=%)', OLD.session_id
            USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.linear_state_sync_disabled IS DISTINCT FROM NEW.linear_state_sync_disabled THEN
        RAISE EXCEPTION 'session_linear_context.linear_state_sync_disabled is immutable after create (session_id=%)', OLD.session_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER session_linear_context_flags_immutable_trigger
    BEFORE UPDATE OF linear_private, linear_state_sync_disabled ON session_linear_context
    FOR EACH ROW
    EXECUTE FUNCTION session_linear_context_flags_immutable();

INSERT INTO session_linear_context (
    session_id, org_id, linear_private, linear_state_sync_disabled,
    linear_identifier_hint, linear_prepare_state, created_at, updated_at
)
SELECT
    id, org_id, linear_private, linear_state_sync_disabled,
    linear_identifier_hint, linear_prepare_state, created_at, created_at
FROM sessions
WHERE linear_private
   OR linear_state_sync_disabled
   OR linear_identifier_hint IS NOT NULL
   OR linear_prepare_state <> 'none';

CREATE TABLE session_publish_state (
    session_id             UUID PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    org_id                 UUID NOT NULL REFERENCES organizations(id),
    pr_creation_state      TEXT NOT NULL DEFAULT 'idle',
    pr_creation_error      TEXT,
    pr_push_state          TEXT NOT NULL DEFAULT 'idle',
    pr_push_error          TEXT,
    branch_creation_state  TEXT NOT NULL DEFAULT 'idle',
    branch_creation_error  TEXT,
    branch_url             TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_publish_state_pr_creation_state
        CHECK (pr_creation_state IN ('idle', 'queued', 'pushing', 'succeeded', 'failed')),
    CONSTRAINT chk_session_publish_state_pr_push_state
        CHECK (pr_push_state IN ('idle', 'queued', 'pushing', 'succeeded', 'failed')),
    CONSTRAINT chk_session_publish_state_branch_creation_state
        CHECK (branch_creation_state IN ('idle', 'queued', 'pushing', 'succeeded', 'failed'))
);

CREATE UNIQUE INDEX idx_session_publish_state_org_session
    ON session_publish_state (org_id, session_id);

CREATE INDEX idx_session_publish_state_in_flight
    ON session_publish_state (org_id, session_id)
    WHERE pr_creation_state IN ('queued', 'pushing')
       OR pr_push_state IN ('queued', 'pushing')
       OR branch_creation_state IN ('queued', 'pushing');

INSERT INTO session_publish_state (
    session_id, org_id,
    pr_creation_state, pr_creation_error,
    pr_push_state, pr_push_error,
    branch_creation_state, branch_creation_error, branch_url,
    created_at, updated_at
)
SELECT
    id, org_id,
    pr_creation_state, pr_creation_error,
    pr_push_state, pr_push_error,
    branch_creation_state, branch_creation_error, branch_url,
    created_at, created_at
FROM sessions
WHERE pr_creation_state <> 'idle'
   OR pr_creation_error IS NOT NULL
   OR pr_push_state <> 'idle'
   OR pr_push_error IS NOT NULL
   OR branch_creation_state <> 'idle'
   OR branch_creation_error IS NOT NULL
   OR branch_url IS NOT NULL;

CREATE TABLE session_execution_metadata (
    session_id            UUID PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    org_id                UUID NOT NULL REFERENCES organizations(id),
    capability_snapshot   JSONB NOT NULL DEFAULT '[]'::jsonb,
    git_identity_source   TEXT,
    git_identity_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_execution_metadata_capability_snapshot_array
        CHECK (jsonb_typeof(capability_snapshot) = 'array')
);

CREATE UNIQUE INDEX idx_session_execution_metadata_org_session
    ON session_execution_metadata (org_id, session_id);

CREATE INDEX idx_session_execution_metadata_git_identity_user
    ON session_execution_metadata (org_id, git_identity_user_id)
    WHERE git_identity_user_id IS NOT NULL;

INSERT INTO session_execution_metadata (
    session_id, org_id, capability_snapshot, git_identity_source, git_identity_user_id,
    created_at, updated_at
)
SELECT
    id, org_id, capability_snapshot, git_identity_source, git_identity_user_id,
    created_at, created_at
FROM sessions
WHERE capability_snapshot <> '[]'::jsonb
   OR git_identity_source IS NOT NULL
   OR git_identity_user_id IS NOT NULL;

DROP TRIGGER IF EXISTS sessions_linear_flags_immutable_trigger ON sessions;
DROP FUNCTION IF EXISTS sessions_linear_flags_immutable();

DROP INDEX IF EXISTS idx_sessions_linear_identifier_hint;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_linear_prepare_state,
    DROP CONSTRAINT IF EXISTS chk_sessions_pr_creation_state,
    DROP CONSTRAINT IF EXISTS chk_sessions_pr_push_state,
    DROP CONSTRAINT IF EXISTS chk_sessions_branch_creation_state,
    DROP CONSTRAINT IF EXISTS chk_sessions_capability_snapshot_array;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS linear_private,
    DROP COLUMN IF EXISTS linear_state_sync_disabled,
    DROP COLUMN IF EXISTS linear_identifier_hint,
    DROP COLUMN IF EXISTS linear_prepare_state,
    DROP COLUMN IF EXISTS pr_creation_state,
    DROP COLUMN IF EXISTS pr_creation_error,
    DROP COLUMN IF EXISTS pr_push_state,
    DROP COLUMN IF EXISTS pr_push_error,
    DROP COLUMN IF EXISTS branch_creation_state,
    DROP COLUMN IF EXISTS branch_creation_error,
    DROP COLUMN IF EXISTS branch_url,
    DROP COLUMN IF EXISTS capability_snapshot,
    DROP COLUMN IF EXISTS git_identity_source,
    DROP COLUMN IF EXISTS git_identity_user_id;
