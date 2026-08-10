DROP INDEX IF EXISTS idx_jobs_active_workload;
DROP INDEX IF EXISTS idx_jobs_sandbox_routing;

ALTER TABLE jobs DROP CONSTRAINT IF EXISTS chk_jobs_workload_class;
ALTER TABLE jobs DROP COLUMN IF EXISTS sandbox_slot_reserved_until;
ALTER TABLE jobs DROP COLUMN IF EXISTS workload_class;
