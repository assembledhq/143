CREATE TABLE code_review_github_trigger_settings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    installation_id bigint NOT NULL,
    active boolean NOT NULL DEFAULT true,
    version int NOT NULL,
    team_slug text NOT NULL,
    team_name text NOT NULL,
    team_id bigint NOT NULL,
    repo_permission text NOT NULL CONSTRAINT chk_code_review_github_trigger_settings_repo_permission CHECK (repo_permission IN ('pull')),
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_code_review_github_trigger_settings_active
    ON code_review_github_trigger_settings (org_id, repository_id)
    WHERE active = true;

CREATE UNIQUE INDEX idx_code_review_github_trigger_settings_scope_version
    ON code_review_github_trigger_settings (org_id, repository_id, version);

CREATE INDEX idx_code_review_github_trigger_settings_history
    ON code_review_github_trigger_settings (org_id, repository_id, created_at DESC);
