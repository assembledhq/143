ALTER TABLE projects ADD COLUMN archived_at timestamptz;

CREATE INDEX idx_projects_not_archived_priority
  ON projects(org_id, priority, created_at DESC, id DESC)
  WHERE deleted_at IS NULL AND archived_at IS NULL;

CREATE INDEX idx_projects_archived_priority
  ON projects(org_id, priority, created_at DESC, id DESC)
  WHERE deleted_at IS NULL AND archived_at IS NOT NULL;
