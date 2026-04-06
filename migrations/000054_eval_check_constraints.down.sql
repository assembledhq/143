ALTER TABLE eval_tasks DROP CONSTRAINT IF EXISTS chk_eval_tasks_source;
ALTER TABLE eval_tasks DROP CONSTRAINT IF EXISTS chk_eval_tasks_complexity;
ALTER TABLE eval_batches DROP CONSTRAINT IF EXISTS chk_eval_batches_status;
ALTER TABLE eval_runs DROP CONSTRAINT IF EXISTS chk_eval_runs_status;
