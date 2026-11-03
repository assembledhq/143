CREATE TABLE package_registry_credentials (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    registry_url text NOT NULL,
    package_scope text NOT NULL,
    revision uuid NOT NULL,
    token_encrypted bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT package_registry_credentials_scope_unique UNIQUE (org_id, repository_id, package_scope),
    CONSTRAINT package_registry_credentials_origin_unique UNIQUE (org_id, repository_id, registry_url),
    CONSTRAINT package_registry_credentials_scope_check CHECK (package_scope ~ '^@[a-z0-9][a-z0-9._-]*$'),
    CONSTRAINT package_registry_credentials_token_check CHECK (octet_length(token_encrypted) > 0)
);
