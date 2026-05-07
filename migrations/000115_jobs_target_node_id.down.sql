DROP INDEX IF EXISTS idx_jobs_target_dequeue;
ALTER TABLE jobs DROP COLUMN IF EXISTS target_node_id;
