-- Add triggers to keep projects.total_tasks, completed_tasks, and failed_tasks
-- in sync with project_tasks. This prevents silent counter drift from application
-- bugs, missed code paths, or cascade deletes.
--
-- Uses STATEMENT-level triggers with transition tables to avoid N+1 count queries
-- during bulk operations. Each trigger collects distinct affected project_ids and
-- updates each project once per statement.
--
-- PERFORMANCE NOTE: The UPDATE trigger fires on ALL column updates to project_tasks,
-- not just status changes, because PostgreSQL does not allow REFERENCING transition
-- tables with column-list triggers (e.g. AFTER UPDATE OF status). The recount is
-- idempotent so correctness is unaffected, but non-status updates (branch_name,
-- pr_url, etc.) will also trigger a recount. If this becomes a hot path, consider
-- adding an early-exit check inside the trigger function comparing old and new
-- counter-relevant columns.

CREATE OR REPLACE FUNCTION update_project_task_counts_insert()
RETURNS TRIGGER AS $$
DECLARE
    affected_project_id uuid;
BEGIN
    FOR affected_project_id IN
        SELECT DISTINCT project_id FROM new_table
    LOOP
        UPDATE projects SET
            total_tasks     = (SELECT count(*) FROM project_tasks WHERE project_id = affected_project_id),
            completed_tasks = (SELECT count(*) FROM project_tasks WHERE project_id = affected_project_id AND status = 'completed'),
            failed_tasks    = (SELECT count(*) FROM project_tasks WHERE project_id = affected_project_id AND status = 'failed'),
            updated_at      = now()
        WHERE id = affected_project_id;
    END LOOP;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION update_project_task_counts_update()
RETURNS TRIGGER AS $$
DECLARE
    affected_project_id uuid;
BEGIN
    FOR affected_project_id IN
        SELECT DISTINCT project_id FROM (
            SELECT project_id FROM new_table
            UNION
            SELECT project_id FROM old_table
        ) AS affected
    LOOP
        UPDATE projects SET
            total_tasks     = (SELECT count(*) FROM project_tasks WHERE project_id = affected_project_id),
            completed_tasks = (SELECT count(*) FROM project_tasks WHERE project_id = affected_project_id AND status = 'completed'),
            failed_tasks    = (SELECT count(*) FROM project_tasks WHERE project_id = affected_project_id AND status = 'failed'),
            updated_at      = now()
        WHERE id = affected_project_id;
    END LOOP;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION update_project_task_counts_delete()
RETURNS TRIGGER AS $$
DECLARE
    affected_project_id uuid;
BEGIN
    FOR affected_project_id IN
        SELECT DISTINCT project_id FROM old_table
    LOOP
        UPDATE projects SET
            total_tasks     = (SELECT count(*) FROM project_tasks WHERE project_id = affected_project_id),
            completed_tasks = (SELECT count(*) FROM project_tasks WHERE project_id = affected_project_id AND status = 'completed'),
            failed_tasks    = (SELECT count(*) FROM project_tasks WHERE project_id = affected_project_id AND status = 'failed'),
            updated_at      = now()
        WHERE id = affected_project_id;
    END LOOP;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_project_task_counts_insert
    AFTER INSERT ON project_tasks
    REFERENCING NEW TABLE AS new_table
    FOR EACH STATEMENT
    EXECUTE FUNCTION update_project_task_counts_insert();

-- Note: Cannot use UPDATE OF column with REFERENCING transition tables in
-- PostgreSQL, so this fires on any column update. The function recounts from
-- project_tasks, so extra firings are harmless (just slightly more work).
CREATE TRIGGER trg_project_task_counts_update
    AFTER UPDATE ON project_tasks
    REFERENCING NEW TABLE AS new_table OLD TABLE AS old_table
    FOR EACH STATEMENT
    EXECUTE FUNCTION update_project_task_counts_update();

CREATE TRIGGER trg_project_task_counts_delete
    AFTER DELETE ON project_tasks
    REFERENCING OLD TABLE AS old_table
    FOR EACH STATEMENT
    EXECUTE FUNCTION update_project_task_counts_delete();
