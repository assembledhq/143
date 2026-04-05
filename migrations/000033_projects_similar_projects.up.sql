-- Add structured overlap metadata for PM project proposals.
-- Stores similarity analysis between proposed and existing projects.
ALTER TABLE projects ADD COLUMN similar_projects JSONB NOT NULL DEFAULT '[]'::jsonb;

-- GIN index for querying which proposals reference a given project.
CREATE INDEX idx_projects_similar_projects ON projects USING GIN (similar_projects);
