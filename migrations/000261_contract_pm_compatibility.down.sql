-- Restore the expand-phase compatibility schema. Dropped PM provenance values
-- cannot be reconstructed; restored columns are intentionally empty/defaulted.

ALTER TABLE projects
    ADD COLUMN proposed_by_pm boolean NOT NULL DEFAULT false,
    ADD COLUMN source_issue_ids uuid[],
    ADD COLUMN proposal_reasoning text;

CREATE TABLE project_source_issues (
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    issue_id uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, issue_id)
);
CREATE INDEX idx_project_source_issues_issue ON project_source_issues (issue_id);

CREATE TRIGGER trg_freeze_source_issue_ids
BEFORE UPDATE OF source_issue_ids ON projects
FOR EACH ROW
WHEN (OLD.source_issue_ids IS DISTINCT FROM NEW.source_issue_ids)
EXECUTE FUNCTION reject_legacy_array_write();

ALTER TABLE project_cycle_archive RENAME TO project_cycles;
ALTER TABLE pm_decision_archive RENAME TO pm_decision_log;
ALTER TABLE pm_plan_archive RENAME TO pm_plans;

ALTER TABLE reference_context_set_pin_members
    RENAME COLUMN reference_document_id TO document_id;
ALTER TABLE reference_context_set_pin_members
    RENAME CONSTRAINT reference_context_set_pin_members_reference_document_id_fkey TO pm_document_set_pin_members_document_id_fkey;
ALTER TABLE reference_context_set_pin_members
    RENAME CONSTRAINT reference_context_set_pin_members_pin_id_fkey TO pm_document_set_pin_members_pin_id_fkey;
ALTER TABLE reference_context_set_pin_members
    RENAME CONSTRAINT reference_context_set_pin_members_pkey TO pm_document_set_pin_members_pkey;
ALTER TABLE reference_context_set_pins
    RENAME CONSTRAINT reference_context_set_pins_org_id_fkey TO pm_document_set_pins_org_id_fkey;
ALTER INDEX idx_reference_context_set_pins_org RENAME TO idx_pm_document_set_pins_org;
ALTER TABLE reference_context_set_pins
    RENAME CONSTRAINT reference_context_set_pins_pkey TO pm_document_set_pins_pkey;
ALTER TABLE reference_documents
    RENAME CONSTRAINT chk_reference_documents_source_type TO chk_pm_documents_source_type;
ALTER TABLE reference_documents
    RENAME CONSTRAINT chk_reference_documents_doc_type TO chk_pm_documents_doc_type;
ALTER TABLE reference_documents
    RENAME CONSTRAINT reference_documents_created_by_fkey TO pm_documents_created_by_fkey;
ALTER TABLE reference_documents
    RENAME CONSTRAINT reference_documents_org_id_fkey TO pm_documents_org_id_fkey;
ALTER INDEX idx_reference_documents_active_logical RENAME TO idx_pm_documents_active_logical;
ALTER INDEX idx_reference_documents_source RENAME TO idx_pm_documents_source;
ALTER INDEX idx_reference_documents_org RENAME TO idx_pm_documents_org;
ALTER TABLE reference_documents
    RENAME CONSTRAINT reference_documents_pkey TO pm_documents_pkey;
ALTER TABLE reference_context_set_pin_members RENAME TO pm_document_set_pin_members;
ALTER TABLE reference_context_set_pins RENAME TO pm_document_set_pins;
ALTER TABLE reference_documents RENAME TO pm_documents;

ALTER TABLE session_execution_context ADD COLUMN pm_plan_id uuid REFERENCES pm_plans(id);
ALTER TABLE session_execution_context
    DROP CONSTRAINT chk_session_execution_context_has_data;
ALTER TABLE session_execution_context RENAME COLUMN planning_reasoning TO pm_reasoning;
ALTER TABLE session_execution_context RENAME COLUMN execution_brief TO pm_approach;
ALTER TABLE session_execution_context RENAME TO session_pm_context;
ALTER TABLE session_pm_context
    ADD CONSTRAINT chk_session_pm_context_has_data CHECK (
        pm_plan_id IS NOT NULL
        OR pm_approach IS NOT NULL
        OR pm_reasoning IS NOT NULL
        OR project_task_id IS NOT NULL
    );
ALTER INDEX idx_session_execution_context_org_session
    RENAME TO idx_session_pm_context_org_session;
ALTER INDEX idx_session_execution_context_project_task
    RENAME TO idx_session_pm_context_project_task;
ALTER TABLE session_pm_context
    RENAME CONSTRAINT session_execution_context_project_task_id_fkey TO session_pm_context_project_task_id_fkey;
ALTER TABLE session_pm_context
    RENAME CONSTRAINT session_execution_context_org_id_fkey TO session_pm_context_org_id_fkey;
ALTER TABLE session_pm_context
    RENAME CONSTRAINT session_execution_context_session_id_fkey TO session_pm_context_session_id_fkey;
ALTER TABLE session_pm_context
    RENAME CONSTRAINT session_execution_context_pkey TO session_pm_context_pkey;
ALTER TABLE session_pm_context
    RENAME CONSTRAINT session_execution_context_pm_plan_id_fkey TO session_pm_context_pm_plan_id_fkey;

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

CREATE VIEW reference_documents AS SELECT * FROM pm_documents;
CREATE VIEW reference_context_set_pins AS SELECT * FROM pm_document_set_pins;
CREATE VIEW reference_context_set_pin_members AS
SELECT pin_id, document_id AS reference_document_id
FROM pm_document_set_pin_members;
CREATE VIEW pm_plan_archive AS SELECT * FROM pm_plans;
CREATE VIEW pm_decision_archive AS SELECT * FROM pm_decision_log;
CREATE VIEW project_cycle_archive AS SELECT * FROM project_cycles;

ALTER TABLE eval_tasks
    ADD COLUMN pm_document_set_pin_id uuid REFERENCES pm_document_set_pins(id);
ALTER TABLE eval_runs
    ADD COLUMN pm_document_set_pin_id uuid REFERENCES pm_document_set_pins(id);
UPDATE eval_tasks SET pm_document_set_pin_id = reference_context_set_pin_id;
UPDATE eval_runs SET pm_document_set_pin_id = reference_context_set_pin_id;

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

CREATE FUNCTION reject_disabled_pm_jobs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.job_type IN ('pm_analyze', 'pm_bootstrap', 'pm_context_refresh', 'project_cycle') THEN
        RAISE EXCEPTION 'job type % is disabled by the PM shutdown', NEW.job_type
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_reject_disabled_pm_jobs
BEFORE INSERT OR UPDATE OF job_type ON jobs
FOR EACH ROW
EXECUTE FUNCTION reject_disabled_pm_jobs();
