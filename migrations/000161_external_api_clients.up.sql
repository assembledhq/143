ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_origin;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_origin
        CHECK (origin IN ('issue_trigger', 'manual', 'project', 'automation', 'revision', 'slack', 'external_api'));

CREATE TABLE api_clients (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    name text NOT NULL,
    description text,
    status text NOT NULL DEFAULT 'enabled',
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    disabled_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_api_clients_status CHECK (status IN ('enabled', 'disabled'))
);

CREATE INDEX idx_api_clients_org_created
    ON api_clients (org_id, created_at DESC, id DESC);

CREATE TABLE api_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    api_client_id uuid NOT NULL REFERENCES api_clients(id) ON DELETE CASCADE,
    name text NOT NULL,
    token_hash text NOT NULL,
    token_prefix text NOT NULL,
    scopes text[] NOT NULL,
    repository_ids uuid[] NOT NULL DEFAULT '{}',
    expires_at timestamptz,
    last_used_at timestamptz,
    last_used_ip text,
    last_used_user_agent text,
    revoked_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    revoked_at timestamptz,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_api_tokens_hash_active
    ON api_tokens (token_hash)
    WHERE revoked_at IS NULL;

CREATE INDEX idx_api_tokens_client_created
    ON api_tokens (org_id, api_client_id, created_at DESC);

CREATE TABLE api_idempotency_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    api_client_id uuid NOT NULL REFERENCES api_clients(id) ON DELETE CASCADE,
    api_token_id uuid NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
    idempotency_key text NOT NULL,
    method text NOT NULL,
    path text NOT NULL,
    request_body_hash text NOT NULL,
    response_status int,
    response_body jsonb,
    locked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX idx_api_idempotency_keys_unique
    ON api_idempotency_keys (org_id, api_client_id, method, path, idempotency_key);

CREATE INDEX idx_api_idempotency_keys_expires
    ON api_idempotency_keys (expires_at);
