ALTER TABLE eval_bootstrap_runs
    ADD COLUMN IF NOT EXISTS candidates JSONB;
