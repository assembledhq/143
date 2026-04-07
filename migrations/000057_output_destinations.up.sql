-- Output destinations allow scheduled projects to send results to multiple systems
-- (Slack channels, Notion pages, email addresses, webhooks) without local MCP servers.

CREATE TABLE IF NOT EXISTS project_output_destinations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    destination_type TEXT NOT NULL CHECK (destination_type IN ('slack', 'email', 'notion', 'webhook')),
    label TEXT NOT NULL DEFAULT '',
    config JSONB NOT NULL DEFAULT '{}',
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_project_output_destinations_project ON project_output_destinations(project_id) WHERE enabled = true;
CREATE INDEX idx_project_output_destinations_org ON project_output_destinations(org_id);

-- Add reviewer_strategy to projects for auto-reviewer assignment on PRs.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS reviewer_strategy TEXT NOT NULL DEFAULT 'codeowners';

COMMENT ON COLUMN projects.reviewer_strategy IS 'How to assign PR reviewers: codeowners (parse CODEOWNERS), or none';
