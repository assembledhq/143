-- Contract the rolling-compatibility layer introduced by migration 000260.
-- Every application query now uses the neutral execution/reference-context
-- names, so the legacy objects and dual-write machinery can be removed.

DROP TRIGGER trg_reject_disabled_pm_jobs ON jobs;
DROP FUNCTION reject_disabled_pm_jobs();

DROP TRIGGER trg_eval_runs_sync_reference_context_set_pin ON eval_runs;
DROP TRIGGER trg_eval_tasks_sync_reference_context_set_pin ON eval_tasks;
DROP FUNCTION sync_reference_context_set_pin_columns();

ALTER TABLE eval_runs DROP COLUMN pm_document_set_pin_id;
ALTER TABLE eval_tasks DROP COLUMN pm_document_set_pin_id;

DROP VIEW project_cycle_archive;
DROP VIEW pm_decision_archive;
DROP VIEW pm_plan_archive;
DROP VIEW reference_context_set_pin_members;
DROP VIEW reference_context_set_pins;
DROP VIEW reference_documents;
DROP VIEW session_execution_context;

ALTER TABLE session_pm_context RENAME TO session_execution_context;
ALTER TABLE session_execution_context RENAME COLUMN pm_approach TO execution_brief;
ALTER TABLE session_execution_context RENAME COLUMN pm_reasoning TO planning_reasoning;
ALTER TABLE session_execution_context
    DROP CONSTRAINT chk_session_pm_context_has_data;
ALTER TABLE session_execution_context DROP COLUMN pm_plan_id;
DELETE FROM session_execution_context
WHERE execution_brief IS NULL
  AND planning_reasoning IS NULL
  AND project_task_id IS NULL;
ALTER TABLE session_execution_context
    ADD CONSTRAINT chk_session_execution_context_has_data CHECK (
        execution_brief IS NOT NULL
        OR planning_reasoning IS NOT NULL
        OR project_task_id IS NOT NULL
    );
ALTER INDEX idx_session_pm_context_org_session
    RENAME TO idx_session_execution_context_org_session;
ALTER INDEX idx_session_pm_context_project_task
    RENAME TO idx_session_execution_context_project_task;
ALTER TABLE session_execution_context
    RENAME CONSTRAINT session_pm_context_pkey TO session_execution_context_pkey;
ALTER TABLE session_execution_context
    RENAME CONSTRAINT session_pm_context_session_id_fkey TO session_execution_context_session_id_fkey;
ALTER TABLE session_execution_context
    RENAME CONSTRAINT session_pm_context_org_id_fkey TO session_execution_context_org_id_fkey;
ALTER TABLE session_execution_context
    RENAME CONSTRAINT session_pm_context_project_task_id_fkey TO session_execution_context_project_task_id_fkey;

ALTER TABLE pm_documents RENAME TO reference_documents;
ALTER TABLE pm_document_set_pins RENAME TO reference_context_set_pins;
ALTER TABLE pm_document_set_pin_members RENAME TO reference_context_set_pin_members;
ALTER TABLE reference_context_set_pin_members
    RENAME COLUMN document_id TO reference_document_id;
ALTER TABLE reference_documents
    RENAME CONSTRAINT pm_documents_pkey TO reference_documents_pkey;
ALTER INDEX idx_pm_documents_org RENAME TO idx_reference_documents_org;
ALTER INDEX idx_pm_documents_source RENAME TO idx_reference_documents_source;
ALTER INDEX idx_pm_documents_active_logical RENAME TO idx_reference_documents_active_logical;
ALTER TABLE reference_documents
    RENAME CONSTRAINT pm_documents_org_id_fkey TO reference_documents_org_id_fkey;
ALTER TABLE reference_documents
    RENAME CONSTRAINT pm_documents_created_by_fkey TO reference_documents_created_by_fkey;
ALTER TABLE reference_documents
    RENAME CONSTRAINT chk_pm_documents_doc_type TO chk_reference_documents_doc_type;
ALTER TABLE reference_documents
    RENAME CONSTRAINT chk_pm_documents_source_type TO chk_reference_documents_source_type;
ALTER TABLE reference_context_set_pins
    RENAME CONSTRAINT pm_document_set_pins_pkey TO reference_context_set_pins_pkey;
ALTER INDEX idx_pm_document_set_pins_org RENAME TO idx_reference_context_set_pins_org;
ALTER TABLE reference_context_set_pins
    RENAME CONSTRAINT pm_document_set_pins_org_id_fkey TO reference_context_set_pins_org_id_fkey;
ALTER TABLE reference_context_set_pin_members
    RENAME CONSTRAINT pm_document_set_pin_members_pkey TO reference_context_set_pin_members_pkey;
ALTER TABLE reference_context_set_pin_members
    RENAME CONSTRAINT pm_document_set_pin_members_pin_id_fkey TO reference_context_set_pin_members_pin_id_fkey;
ALTER TABLE reference_context_set_pin_members
    RENAME CONSTRAINT pm_document_set_pin_members_document_id_fkey TO reference_context_set_pin_members_reference_document_id_fkey;

-- Preserve obsolete planning data as archives. Renaming the tables keeps rows,
-- foreign keys, and audit history intact without exposing live PM contracts.
ALTER TABLE pm_plans RENAME TO pm_plan_archive;
ALTER TABLE pm_decision_log RENAME TO pm_decision_archive;
ALTER TABLE project_cycles RENAME TO project_cycle_archive;

-- PM proposal provenance is no longer part of user-authored Projects. The
-- archived Project rows themselves remain intact.
DROP TRIGGER trg_freeze_source_issue_ids ON projects;
DROP TABLE project_source_issues;
ALTER TABLE projects
    DROP COLUMN proposed_by_pm,
    DROP COLUMN source_issue_ids,
    DROP COLUMN proposal_reasoning;
