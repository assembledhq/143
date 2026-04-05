-- Add a trigger to keep projects.total_tasks, completed_tasks, and failed_tasks
-- in sync with project_tasks. This prevents silent counter drift from application
-- bugs, missed code paths, or cascade deletes.

CREATE OR REPLACE FUNCTION update_project_task_counts()
RETURNS TRIGGER AS $$
DECLARE
    target_project_id uuid;
BEGIN
    -- Determine which project to update.
    IF TG_OP = 'DELETE' THEN
        target_project_id := OLD.project_id;
    ELSE
        target_project_id := NEW.project_id;
    END IF;

    -- Recompute all three counters in a single scan of project_tasks.
    UPDATE projects SET
        total_tasks     = agg.total,
        completed_tasks = agg.completed,
        failed_tasks    = agg.failed,
        updated_at      = now()
    FROM (
        SELECT
            count(*) AS total,
            count(*) FILTER (WHERE status = 'completed') AS completed,
            count(*) FILTER (WHERE status = 'failed') AS failed
        FROM project_tasks
        WHERE project_id = target_project_id
    ) agg
    WHERE id = target_project_id;

    -- For UPDATE, if project_id changed, also update the old project.
    IF TG_OP = 'UPDATE' AND OLD.project_id != NEW.project_id THEN
        UPDATE projects SET
            total_tasks     = agg.total,
            completed_tasks = agg.completed,
            failed_tasks    = agg.failed,
            updated_at      = now()
        FROM (
            SELECT
                count(*) AS total,
                count(*) FILTER (WHERE status = 'completed') AS completed,
                count(*) FILTER (WHERE status = 'failed') AS failed
            FROM project_tasks
            WHERE project_id = OLD.project_id
        ) agg
        WHERE id = OLD.project_id;
    END IF;

    RETURN NULL; -- AFTER trigger, return value is ignored
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_project_task_counts
    AFTER INSERT OR UPDATE OF status OR DELETE ON project_tasks
    FOR EACH ROW
    EXECUTE FUNCTION update_project_task_counts();
