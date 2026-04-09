-- Add snapshot_broken flag to eval_tasks for tracking unreachable commits.
ALTER TABLE eval_tasks ADD COLUMN snapshot_broken boolean NOT NULL DEFAULT false;
