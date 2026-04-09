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
-- tables with column-list triggers (e.g. AFTER UPDATE OF status). The trigger
-- function includes an early-exit check that compares old and new counter-relevant
-- columns (status, project_id) and returns immediately if they haven't changed,
-- so non-counter updates (branch_name, pr_url, etc.) are cheap no-ops.

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
    has_counter_change bool;
BEGIN
    -- Early exit: only recount if a counter-relevant column (status,
    -- project_id) actually changed. This avoids unnecessary recounts when
    -- only non-counter columns (branch_name, pr_url, etc.) are updated.
    SELECT EXISTS (
        SELECT 1 FROM new_table n
        JOIN old_table o ON n.id = o.id
        WHERE n.status IS DISTINCT FROM o.status
           OR n.project_id IS DISTINCT FROM o.project_id
    ) INTO has_counter_change;

    IF NOT has_counter_change THEN
        RETURN NULL;
    END IF;

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
