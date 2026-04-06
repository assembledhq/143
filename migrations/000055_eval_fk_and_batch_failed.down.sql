ALTER TABLE eval_runs DROP CONSTRAINT IF EXISTS fk_eval_runs_pm_document_set_pin;
ALTER TABLE eval_tasks DROP CONSTRAINT IF EXISTS fk_eval_tasks_pm_document_set_pin;

-- Restore original constraint without 'failed'
ALTER TABLE eval_batches DROP CONSTRAINT IF EXISTS chk_eval_batches_status;
ALTER TABLE eval_batches ADD CONSTRAINT chk_eval_batches_status
    CHECK (status IN ('pending', 'running', 'completed'));
