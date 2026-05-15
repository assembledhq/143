-- Global GitHub App installation records. These are intentionally not scoped
-- to a single 143 org: one GitHub org installation can be linked to multiple
-- 143 organizations, while individual repositories have exclusive active
-- ownership via repositories.
CREATE TABLE github_installations (
    -- lint:no-org-id reason="global GitHub App installation identity shared by multiple 143 organizations"
    installation_id      bigint      PRIMARY KEY,
    account_id           bigint      NOT NULL,
    account_login        text        NOT NULL,
    account_type         text,
    repository_selection text,
    status               text        NOT NULL DEFAULT 'active',
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE github_installation_org_links (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    integration_id     uuid        REFERENCES integrations(id) ON DELETE SET NULL,
    installation_id    bigint      NOT NULL REFERENCES github_installations(installation_id) ON DELETE CASCADE,
    account_login      text        NOT NULL,
    linked_by_user_id  uuid        REFERENCES users(id) ON DELETE SET NULL,
    status             text        NOT NULL DEFAULT 'active',
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_github_installation_org_links_active
    ON github_installation_org_links (org_id, installation_id)
    WHERE status = 'active';
CREATE INDEX idx_github_installation_org_links_org
    ON github_installation_org_links (org_id, status, created_at DESC);
CREATE INDEX idx_github_installation_org_links_installation
    ON github_installation_org_links (installation_id, status);

INSERT INTO github_installations (installation_id, account_id, account_login, status, created_at, updated_at)
SELECT DISTINCT
    (i.config->>'installation_id')::bigint AS installation_id,
    COALESCE(NULLIF(i.config->>'account_id', '')::bigint, 0) AS account_id,
    COALESCE(NULLIF(i.config->>'account_login', ''), split_part(r.full_name, '/', 1), 'unknown') AS account_login,
    'active',
    min(i.created_at),
    now()
FROM integrations i
LEFT JOIN repositories r
  ON r.integration_id = i.id
WHERE i.provider = 'github'
  AND i.config ? 'installation_id'
  AND NULLIF(i.config->>'installation_id', '') IS NOT NULL
GROUP BY (i.config->>'installation_id')::bigint,
         COALESCE(NULLIF(i.config->>'account_id', '')::bigint, 0),
         COALESCE(NULLIF(i.config->>'account_login', ''), split_part(r.full_name, '/', 1), 'unknown')
ON CONFLICT (installation_id) DO UPDATE
SET account_id = EXCLUDED.account_id,
    account_login = EXCLUDED.account_login,
    updated_at = now();

INSERT INTO github_installation_org_links (org_id, integration_id, installation_id, account_login, status, created_at, updated_at)
SELECT DISTINCT
    i.org_id,
    i.id,
    (i.config->>'installation_id')::bigint,
    gi.account_login,
    i.status,
    i.created_at,
    now()
FROM integrations i
JOIN github_installations gi
  ON gi.installation_id = (i.config->>'installation_id')::bigint
WHERE i.provider = 'github'
  AND i.config ? 'installation_id'
  AND NULLIF(i.config->>'installation_id', '') IS NOT NULL
ON CONFLICT DO NOTHING;

DO $$
BEGIN
    IF EXISTS (
        SELECT github_id
        FROM repositories
        WHERE status = 'active'
        GROUP BY github_id
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot add exclusive active GitHub repo ownership index: duplicate active github_id rows exist';
    END IF;
END $$;

CREATE UNIQUE INDEX idx_repositories_active_github_id
    ON repositories (github_id)
    WHERE status = 'active';
