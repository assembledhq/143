-- Add missing foreign key constraints for pm_document_set_pin_id
ALTER TABLE eval_tasks ADD CONSTRAINT fk_eval_tasks_pm_document_set_pin
    FOREIGN KEY (pm_document_set_pin_id) REFERENCES pm_document_set_pins(id);

ALTER TABLE eval_runs ADD CONSTRAINT fk_eval_runs_pm_document_set_pin
    FOREIGN KEY (pm_document_set_pin_id) REFERENCES pm_document_set_pins(id);

-- Add 'failed' to eval_batches status constraint
ALTER TABLE eval_batches DROP CONSTRAINT IF EXISTS chk_eval_batches_status;
ALTER TABLE eval_batches ADD CONSTRAINT chk_eval_batches_status
    CHECK (status IN ('pending', 'running', 'completed', 'failed'));
