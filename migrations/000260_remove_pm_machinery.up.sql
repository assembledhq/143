-- Rolling-compatible expansion for the neutral execution/reference-context
-- model. Deploys run migrations before rolling API containers, and workers
-- roll after the app, so the legacy schema must remain usable until every old
-- process has drained. A later contraction migration may remove these
-- compatibility objects after the fleet is fully on neutral code.

-- Neutral application-facing projection over the legacy session context table.
-- This simple view is automatically updatable for the columns used by new code.
CREATE VIEW session_execution_context AS
SELECT
    session_id,
    org_id,
    pm_approach AS execution_brief,
    pm_reasoning AS planning_reasoning,
    project_task_id,
    created_at,
    updated_at
FROM session_pm_context;

-- Neutral, automatically updatable projections over retained document tables.
CREATE VIEW reference_documents AS
SELECT * FROM pm_documents;

CREATE VIEW reference_context_set_pins AS
SELECT * FROM pm_document_set_pins;

CREATE VIEW reference_context_set_pin_members AS
SELECT
    pin_id,
    document_id AS reference_document_id
FROM pm_document_set_pin_members;

-- Neutral operator projections preserve archived planning data without
-- invalidating old binaries that still resolve the legacy table names.
CREATE VIEW pm_plan_archive AS
SELECT * FROM pm_plans;

CREATE VIEW pm_decision_archive AS
SELECT * FROM pm_decision_log;

CREATE VIEW project_cycle_archive AS
SELECT * FROM project_cycles;

-- Eval pin columns must support both old and new writers during the rolling
-- window. Keep both names synchronized until the legacy columns are contracted.
ALTER TABLE eval_tasks
    ADD COLUMN reference_context_set_pin_id uuid
        REFERENCES pm_document_set_pins(id);
ALTER TABLE eval_runs
    ADD COLUMN reference_context_set_pin_id uuid
        REFERENCES pm_document_set_pins(id);

UPDATE eval_tasks
SET reference_context_set_pin_id = pm_document_set_pin_id;
UPDATE eval_runs
SET reference_context_set_pin_id = pm_document_set_pin_id;

CREATE FUNCTION sync_reference_context_set_pin_columns()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.reference_context_set_pin_id IS NOT NULL
           AND NEW.pm_document_set_pin_id IS NOT NULL
           AND NEW.reference_context_set_pin_id IS DISTINCT FROM NEW.pm_document_set_pin_id THEN
            RAISE EXCEPTION 'conflicting reference-context pin IDs';
        END IF;
        NEW.reference_context_set_pin_id :=
            COALESCE(NEW.reference_context_set_pin_id, NEW.pm_document_set_pin_id);
        NEW.pm_document_set_pin_id :=
            COALESCE(NEW.pm_document_set_pin_id, NEW.reference_context_set_pin_id);
        RETURN NEW;
    END IF;

    IF NEW.reference_context_set_pin_id IS DISTINCT FROM OLD.reference_context_set_pin_id
       AND NEW.pm_document_set_pin_id IS NOT DISTINCT FROM OLD.pm_document_set_pin_id THEN
        NEW.pm_document_set_pin_id := NEW.reference_context_set_pin_id;
    ELSIF NEW.pm_document_set_pin_id IS DISTINCT FROM OLD.pm_document_set_pin_id
       AND NEW.reference_context_set_pin_id IS NOT DISTINCT FROM OLD.reference_context_set_pin_id THEN
        NEW.reference_context_set_pin_id := NEW.pm_document_set_pin_id;
    ELSIF NEW.reference_context_set_pin_id IS DISTINCT FROM NEW.pm_document_set_pin_id THEN
        RAISE EXCEPTION 'conflicting reference-context pin IDs';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_eval_tasks_sync_reference_context_set_pin
BEFORE INSERT OR UPDATE OF pm_document_set_pin_id, reference_context_set_pin_id
ON eval_tasks
FOR EACH ROW
EXECUTE FUNCTION sync_reference_context_set_pin_columns();

CREATE TRIGGER trg_eval_runs_sync_reference_context_set_pin
BEFORE INSERT OR UPDATE OF pm_document_set_pin_id, reference_context_set_pin_id
ON eval_runs
FOR EACH ROW
EXECUTE FUNCTION sync_reference_context_set_pin_columns();

-- Keep historical pm_agent issues readable while assigning new agent-created
-- issues a neutral source.
ALTER TABLE issues DROP CONSTRAINT chk_issues_source;
ALTER TABLE issues
    ADD CONSTRAINT chk_issues_source CHECK (source IN (
        'sentry', 'linear', 'pagerduty', 'manual', 'agent', 'pm_agent'
    ));

-- Legacy organization/repository JSON keys remain stored but have no defaults
-- or writers in application code. The disabled-PM-job trigger also remains
-- installed throughout the compatibility window so old processes cannot
-- reintroduce removed work.
