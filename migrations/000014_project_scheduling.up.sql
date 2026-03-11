-- Add scheduling fields to projects for recurring automation.
ALTER TABLE projects ADD COLUMN schedule_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE projects ADD COLUMN schedule_interval INT NOT NULL DEFAULT 1;
ALTER TABLE projects ADD COLUMN schedule_unit TEXT NOT NULL DEFAULT 'days';
ALTER TABLE projects ADD COLUMN next_run_at TIMESTAMPTZ;

-- Partial index for the scheduler to efficiently find due projects.
CREATE INDEX idx_projects_schedule_due
  ON projects (next_run_at)
  WHERE schedule_enabled = true AND status = 'active' AND next_run_at IS NOT NULL;
