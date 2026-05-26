CREATE TABLE preview_targets (
    id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                 UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id          UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    branch                 TEXT        NOT NULL,
    commit_sha             TEXT        NOT NULL,
    preview_config_name    TEXT        NOT NULL DEFAULT '',
    resolved_config_digest TEXT        NOT NULL DEFAULT '',
    source_type            TEXT        NOT NULL DEFAULT 'manual',
    source_id              TEXT        NOT NULL DEFAULT '',
    source_url             TEXT        NOT NULL DEFAULT '',
    created_by_user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT preview_targets_source_type_check
        CHECK (source_type IN ('session', 'pull_request', 'api', 'manual', 'automation')),
    CONSTRAINT preview_targets_branch_not_blank CHECK (length(trim(branch)) > 0),
    CONSTRAINT preview_targets_commit_sha_not_blank CHECK (length(trim(commit_sha)) > 0)
);

CREATE UNIQUE INDEX idx_preview_targets_identity
    ON preview_targets (org_id, repository_id, branch, commit_sha, preview_config_name);

CREATE INDEX idx_preview_targets_org_repo_created
    ON preview_targets (org_id, repository_id, created_at DESC);

ALTER TABLE preview_instances
    ALTER COLUMN session_id DROP NOT NULL,
    ADD COLUMN preview_target_id UUID REFERENCES preview_targets(id) ON DELETE SET NULL;

CREATE INDEX idx_preview_instances_org_target
    ON preview_instances (org_id, preview_target_id, created_at DESC)
    WHERE preview_target_id IS NOT NULL;

CREATE UNIQUE INDEX idx_preview_instances_active_target
    ON preview_instances (preview_target_id)
    WHERE preview_target_id IS NOT NULL
      AND status IN ('starting', 'ready', 'partially_ready', 'unhealthy');

CREATE TABLE preview_links (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    preview_target_id UUID        NOT NULL REFERENCES preview_targets(id) ON DELETE CASCADE,
    link_type         TEXT        NOT NULL,
    slug              TEXT        NOT NULL,
    repository_id     UUID        REFERENCES repositories(id) ON DELETE CASCADE,
    pr_number         INT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT preview_links_type_check CHECK (link_type IN ('target', 'pull_request')),
    CONSTRAINT preview_links_slug_not_blank CHECK (length(trim(slug)) > 0)
);

CREATE UNIQUE INDEX idx_preview_links_org_type_slug
    ON preview_links (org_id, link_type, slug);

CREATE UNIQUE INDEX idx_preview_links_org_pr
    ON preview_links (org_id, repository_id, pr_number)
    WHERE link_type = 'pull_request' AND repository_id IS NOT NULL AND pr_number IS NOT NULL;

CREATE TABLE preview_idempotency_keys (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    idempotency_key   TEXT        NOT NULL,
    preview_target_id UUID        NOT NULL REFERENCES preview_targets(id) ON DELETE CASCADE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT preview_idempotency_key_not_blank CHECK (length(trim(idempotency_key)) > 0)
);

CREATE UNIQUE INDEX idx_preview_idempotency_keys_org_key
    ON preview_idempotency_keys (org_id, idempotency_key);

CREATE TABLE preview_api_tokens (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name               TEXT        NOT NULL,
    token_hash         TEXT        NOT NULL,
    scopes             TEXT[]      NOT NULL,
    repository_ids     UUID[]      NOT NULL DEFAULT '{}',
    created_by_user_id UUID        NOT NULL REFERENCES users(id),
    last_used_at       TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT preview_api_tokens_name_not_blank CHECK (length(trim(name)) > 0),
    CONSTRAINT preview_api_tokens_hash_not_blank CHECK (length(trim(token_hash)) > 0),
    CONSTRAINT preview_api_tokens_scopes_not_empty CHECK (array_length(scopes, 1) > 0)
);

CREATE UNIQUE INDEX idx_preview_api_tokens_org_hash
    ON preview_api_tokens (org_id, token_hash)
    WHERE revoked_at IS NULL;

CREATE INDEX idx_preview_api_tokens_hash_active
    ON preview_api_tokens (token_hash)
    WHERE revoked_at IS NULL;
