ALTER TABLE github_installation_org_links
    ADD COLUMN auto_join_enabled boolean NOT NULL DEFAULT false;

CREATE UNIQUE INDEX idx_github_install_links_auto_join
    ON github_installation_org_links (installation_id)
    WHERE status = 'active' AND auto_join_enabled;

CREATE TABLE github_org_members (
    -- lint:no-org-id reason="global GitHub org roster keyed by installation, shared like github_installations"
    installation_id bigint      NOT NULL REFERENCES github_installations(installation_id) ON DELETE CASCADE,
    github_user_id  bigint      NOT NULL,
    github_login    text        NOT NULL,
    synced_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (installation_id, github_user_id)
);

CREATE INDEX idx_github_org_members_user
    ON github_org_members (github_user_id);

ALTER TABLE github_installations
    ADD COLUMN roster_synced_at timestamptz;
