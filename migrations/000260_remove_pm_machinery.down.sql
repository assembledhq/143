UPDATE issues SET source = 'manual' WHERE source = 'agent';
ALTER TABLE issues DROP CONSTRAINT chk_issues_source;
ALTER TABLE issues
    ADD CONSTRAINT chk_issues_source CHECK (source IN (
        'sentry', 'linear', 'pagerduty', 'manual', 'pm_agent'
    ));

DROP TRIGGER trg_eval_runs_sync_reference_context_set_pin ON eval_runs;
DROP TRIGGER trg_eval_tasks_sync_reference_context_set_pin ON eval_tasks;
DROP FUNCTION sync_reference_context_set_pin_columns();

ALTER TABLE eval_runs DROP COLUMN reference_context_set_pin_id;
ALTER TABLE eval_tasks DROP COLUMN reference_context_set_pin_id;

DROP VIEW project_cycle_archive;
DROP VIEW pm_decision_archive;
DROP VIEW pm_plan_archive;
DROP VIEW reference_context_set_pin_members;
DROP VIEW reference_context_set_pins;
DROP VIEW reference_documents;
DROP VIEW session_execution_context;
