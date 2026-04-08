DROP TRIGGER IF EXISTS trg_project_task_counts_insert ON project_tasks;
DROP TRIGGER IF EXISTS trg_project_task_counts_update ON project_tasks;
DROP TRIGGER IF EXISTS trg_project_task_counts_delete ON project_tasks;
DROP FUNCTION IF EXISTS update_project_task_counts_insert();
DROP FUNCTION IF EXISTS update_project_task_counts_update();
DROP FUNCTION IF EXISTS update_project_task_counts_delete();
