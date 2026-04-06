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
-- Filter out stale references (deleted tasks) and self-references, since the
-- old UUID[] column had no FK enforcement and could accumulate dangling IDs.
INSERT INTO project_task_dependencies (task_id, depends_on_id)
SELECT DISTINCT pt.id, dep_id
FROM project_tasks pt,
     unnest(pt.depends_on) AS dep_id
WHERE pt.depends_on IS NOT NULL
  AND array_length(pt.depends_on, 1) > 0
  AND pt.id != dep_id                              -- exclude self-references
  AND EXISTS (SELECT 1 FROM project_tasks WHERE id = dep_id);  -- exclude stale IDs

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
-- Filter out stale references (deleted issues), since the old UUID[] column
-- had no FK enforcement.
INSERT INTO project_source_issues (project_id, issue_id)
SELECT DISTINCT p.id, iss_id
FROM projects p,
     unnest(p.source_issue_ids) AS iss_id
WHERE p.source_issue_ids IS NOT NULL
  AND array_length(p.source_issue_ids, 1) > 0
  AND EXISTS (SELECT 1 FROM issues WHERE id = iss_id);  -- exclude stale IDs

-- Note: we do NOT drop the old UUID[] columns in this migration to allow
-- a phased rollout. The application code will be updated to use the join
-- tables, and a future migration will drop the legacy columns once verified.
-- This avoids data loss during the transition.
--
-- IMPORTANT: Application code MUST NOT write to both the old UUID[] columns
-- and the new join tables simultaneously, as they will diverge. The join
-- tables are the source of truth from this migration onward. The old columns
-- (projects.source_issue_ids, project_tasks.depends_on) are frozen and will
-- be dropped in a follow-up migration.
