CREATE TABLE preview_secret_bundles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    repository_id uuid NOT NULL REFERENCES repositories(id),
    name text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    source_type text NOT NULL,
    source_config_encrypted jsonb NOT NULL,
    outputs_config_encrypted jsonb NOT NULL,
    exposure_policy text NOT NULL DEFAULT 'preview_runtime',
    created_by_user_id uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT preview_secret_bundles_source_type_check CHECK (source_type IN ('managed')),
    CONSTRAINT preview_secret_bundles_exposure_policy_check CHECK (exposure_policy IN ('preview_runtime')),
    CONSTRAINT preview_secret_bundles_name_check CHECK (name <> '')
);

CREATE UNIQUE INDEX preview_secret_bundles_active_name_idx
    ON preview_secret_bundles (org_id, repository_id, name)
    WHERE active = true;

CREATE INDEX preview_secret_bundles_repo_created_idx
    ON preview_secret_bundles (org_id, repository_id, created_at DESC);
