-- Validate separately from the jobs-table shape change so the deployment that
-- installs the columns and indexes does not retain its table lock while the
-- existing rows are scanned.
SET LOCAL lock_timeout = '5s';

ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_workload_class;
