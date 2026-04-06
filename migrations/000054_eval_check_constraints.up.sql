-- Add CHECK constraints for enum-like columns that were missing database-level validation.
ALTER TABLE eval_tasks ADD CONSTRAINT chk_eval_tasks_source
    CHECK (source IN ('manual', 'pr_bootstrap', 'failure_derived'));

ALTER TABLE eval_tasks ADD CONSTRAINT chk_eval_tasks_complexity
    CHECK (complexity IN ('trivial', 'simple', 'moderate', 'complex'));

ALTER TABLE eval_batches ADD CONSTRAINT chk_eval_batches_status
    CHECK (status IN ('pending', 'running', 'completed'));

ALTER TABLE eval_runs ADD CONSTRAINT chk_eval_runs_status
    CHECK (status IN ('pending', 'running', 'completed', 'failed'));
