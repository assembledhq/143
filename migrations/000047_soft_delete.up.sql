-- Add soft-delete support to high-value entities. Hard deletes via CASCADE
-- can be catastrophic — deleting a session cascades to logs, messages,
-- threads, questions, validations, and review comments with no recovery.

-- =============================================================================
-- sessions: most critical — deletion cascades to 6+ child tables
-- =============================================================================
ALTER TABLE sessions ADD COLUMN deleted_at timestamptz;

-- Partial index so normal queries efficiently skip deleted rows.
CREATE INDEX idx_sessions_deleted ON sessions (org_id, created_at DESC) WHERE deleted_at IS NULL;

-- =============================================================================
-- projects: deletion cascades to tasks, cycles, attachments, specs
-- =============================================================================
ALTER TABLE projects ADD COLUMN deleted_at timestamptz;
CREATE INDEX idx_projects_deleted ON projects (org_id, status) WHERE deleted_at IS NULL;

-- =============================================================================
-- issues: deletion cascades to events, scores, estimates, and all downstream
-- =============================================================================
ALTER TABLE issues ADD COLUMN deleted_at timestamptz;
CREATE INDEX idx_issues_deleted ON issues (org_id, status) WHERE deleted_at IS NULL;
