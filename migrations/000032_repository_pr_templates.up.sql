-- Cache discovered PR templates per repository to avoid repeated GitHub API lookups.
-- Templates are refreshed periodically (TTL-based) rather than on every PR creation.
CREATE TABLE repository_pr_templates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    org_id UUID NOT NULL REFERENCES organizations(id),
    template_content TEXT NOT NULL DEFAULT '',
    template_path TEXT NOT NULL DEFAULT '',
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repository_id)
);

CREATE INDEX idx_repository_pr_templates_repo ON repository_pr_templates(repository_id);
