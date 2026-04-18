-- Teams: first-class organizational units within an org.
CREATE TABLE teams (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id),
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    description TEXT,
    github_team_id BIGINT,
    github_team_slug TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(org_id, slug)
);

CREATE INDEX idx_teams_org_id ON teams(org_id);

-- Partial unique index: only one row per (org_id, github_team_id) when github_team_id is set.
CREATE UNIQUE INDEX idx_teams_org_github_team
    ON teams(org_id, github_team_id)
    WHERE github_team_id IS NOT NULL;

-- Team memberships: many-to-many users <-> teams.
-- org_id is denormalized from teams/users so queries can enforce tenant isolation
-- without an extra join, and can be used as a filter in indexes.
CREATE TABLE team_memberships (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id),
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'member',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(team_id, user_id)
);

CREATE INDEX idx_team_memberships_user_id ON team_memberships(user_id);
CREATE INDEX idx_team_memberships_team_id ON team_memberships(team_id);
CREATE INDEX idx_team_memberships_org_user ON team_memberships(org_id, user_id);

-- Add team_id to sessions and projects for fast filtering.
ALTER TABLE sessions ADD COLUMN team_id UUID REFERENCES teams(id) ON DELETE SET NULL;
CREATE INDEX idx_sessions_team_id ON sessions(team_id) WHERE team_id IS NOT NULL;

ALTER TABLE projects ADD COLUMN team_id UUID REFERENCES teams(id) ON DELETE SET NULL;
CREATE INDEX idx_projects_team_id ON projects(team_id) WHERE team_id IS NOT NULL;
