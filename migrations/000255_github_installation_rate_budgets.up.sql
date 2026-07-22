-- Repository-only legacy records predate integration.config.installation_id.
-- Preserve their ability to run reviews by materializing the shared
-- installation identity before rate-limit rows add their foreign keys.
INSERT INTO github_installations (installation_id, account_id, account_login, status)
SELECT
    repository.installation_id,
    0,
    COALESCE(NULLIF(split_part(MIN(repository.full_name), '/', 1), ''), 'unknown'),
    'active'
FROM repositories repository
WHERE repository.installation_id > 0
GROUP BY repository.installation_id
ON CONFLICT (installation_id) DO NOTHING;

CREATE TABLE github_installation_rate_limits (
    -- lint:no-org-id reason="GitHub rate limits are global per App installation and shared across linked 143 organizations"
    installation_id bigint      NOT NULL REFERENCES github_installations(installation_id) ON DELETE CASCADE,
    resource        text        NOT NULL,
    limit_count     integer,
    remaining_count integer,
    reset_at        timestamptz,
    blocked_until   timestamptz,
    observed_at     timestamptz NOT NULL,
    bootstrap_metadata_id uuid,
    bootstrap_reserved_at timestamptz,
    PRIMARY KEY (installation_id, resource),
    CONSTRAINT chk_github_installation_rate_limits_resource
        CHECK (resource IN ('core', 'graphql', 'search', 'unknown')),
    CONSTRAINT chk_github_installation_rate_limits_counts
        CHECK (
            (limit_count IS NULL AND remaining_count IS NULL AND reset_at IS NULL)
            OR
            (limit_count > 0 AND remaining_count >= 0 AND remaining_count <= limit_count AND reset_at IS NOT NULL)
        ),
    CONSTRAINT chk_github_installation_rate_limits_bootstrap
        CHECK (
            (bootstrap_metadata_id IS NULL AND bootstrap_reserved_at IS NULL)
            OR
            (bootstrap_metadata_id IS NOT NULL AND bootstrap_reserved_at IS NOT NULL)
        )
);

CREATE UNIQUE INDEX idx_code_review_metadata_org_id
    ON code_review_session_metadata (org_id, id);

CREATE TABLE github_installation_rate_reservations (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    installation_id    bigint      NOT NULL REFERENCES github_installations(installation_id) ON DELETE CASCADE,
    code_review_metadata_id uuid    NOT NULL,
    resource           text        NOT NULL,
    reserved_units     integer     NOT NULL CHECK (reserved_units > 0),
    created_at         timestamptz NOT NULL DEFAULT now(),
    released_at        timestamptz,
    CONSTRAINT fk_github_rate_reservation_metadata
        FOREIGN KEY (org_id, code_review_metadata_id)
        REFERENCES code_review_session_metadata (org_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_github_installation_rate_reservations_resource
        CHECK (resource IN ('core', 'graphql', 'search', 'unknown')),
    CONSTRAINT uq_github_installation_rate_reservation
        UNIQUE (installation_id, code_review_metadata_id, resource)
);

CREATE INDEX idx_github_installation_rate_reservations_active
    ON github_installation_rate_reservations (installation_id, resource, code_review_metadata_id)
    WHERE released_at IS NULL;
