-- Replace UUID[] columns with proper join tables for referential integrity.
-- Arrays of UUIDs have no FK enforcement, no reverse-lookup indexes, and
-- silently accumulate dangling references when referenced rows are deleted.

-- =============================================================================
-- project_task_dependencies: replaces project_tasks.depends_on UUID[]
-- =============================================================================
CREATE TABLE project_task_dependencies (
    task_id       uuid NOT NULL REFERENCES project_tasks(id) ON DELETE CASCADE,
    depends_on_id uuid NOT NULL REFERENCES project_tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, depends_on_id),
    CONSTRAINT chk_no_self_dependency CHECK (task_id != depends_on_id)
);

-- Reverse lookup: "which tasks depend on task X?"
CREATE INDEX idx_task_deps_depends_on ON project_task_dependencies (depends_on_id);

-- Migrate existing data from the UUID[] column.
INSERT INTO project_task_dependencies (task_id, depends_on_id)
SELECT pt.id, unnest(pt.depends_on)
FROM project_tasks pt
WHERE pt.depends_on IS NOT NULL AND array_length(pt.depends_on, 1) > 0;

-- =============================================================================
-- project_source_issues: replaces projects.source_issue_ids UUID[]
-- =============================================================================
CREATE TABLE project_source_issues (
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    issue_id   uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, issue_id)
);

-- Reverse lookup: "which projects reference issue X?"
CREATE INDEX idx_project_source_issues_issue ON project_source_issues (issue_id);

-- Migrate existing data from the UUID[] column.
INSERT INTO project_source_issues (project_id, issue_id)
SELECT p.id, unnest(p.source_issue_ids)
FROM projects p
WHERE p.source_issue_ids IS NOT NULL AND array_length(p.source_issue_ids, 1) > 0;

-- Note: we do NOT drop the old UUID[] columns in this migration to allow
-- a phased rollout. The application code will be updated to use the join
-- tables, and a future migration will drop the legacy columns once verified.
-- This avoids data loss during the transition.
