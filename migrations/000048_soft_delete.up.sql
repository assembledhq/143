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

-- =============================================================================
-- Guard: prevent accidental hard deletes on soft-delete tables.
-- A DELETE on these tables is redirected to a soft delete (sets deleted_at).
-- To truly hard-delete, SET LOCAL app.allow_hard_delete = 'true' first.
-- =============================================================================
CREATE OR REPLACE FUNCTION prevent_hard_delete()
RETURNS TRIGGER AS $$
BEGIN
    IF current_setting('app.allow_hard_delete', true) = 'true' THEN
        RETURN OLD;
    END IF;
    EXECUTE format(
        'UPDATE %I.%I SET deleted_at = now() WHERE id = $1',
        TG_TABLE_SCHEMA, TG_TABLE_NAME
    ) USING OLD.id;
    RETURN NULL; -- suppress the DELETE
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_sessions_soft_delete
    BEFORE DELETE ON sessions FOR EACH ROW
    EXECUTE FUNCTION prevent_hard_delete();

CREATE TRIGGER trg_projects_soft_delete
    BEFORE DELETE ON projects FOR EACH ROW
    EXECUTE FUNCTION prevent_hard_delete();

CREATE TRIGGER trg_issues_soft_delete
    BEFORE DELETE ON issues FOR EACH ROW
    EXECUTE FUNCTION prevent_hard_delete();
