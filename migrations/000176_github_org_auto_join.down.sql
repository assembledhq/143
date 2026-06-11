ALTER TABLE github_installations
    DROP COLUMN IF EXISTS roster_synced_at;

DROP INDEX IF EXISTS idx_github_org_members_user;
DROP TABLE IF EXISTS github_org_members;

DROP INDEX IF EXISTS idx_github_install_links_auto_join;
ALTER TABLE github_installation_org_links
    DROP COLUMN IF EXISTS auto_join_enabled;
