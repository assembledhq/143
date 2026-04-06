DROP TRIGGER IF EXISTS trg_freeze_source_issue_ids ON projects;
DROP TRIGGER IF EXISTS trg_freeze_depends_on ON project_tasks;
DROP FUNCTION IF EXISTS reject_legacy_array_write();

DROP TABLE IF EXISTS project_source_issues;
DROP TABLE IF EXISTS project_task_dependencies;
