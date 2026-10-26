SET LOCAL lock_timeout = '5s';
ALTER TABLE jobs DROP CONSTRAINT chk_jobs_parked_without_lease;
ALTER TABLE jobs DROP CONSTRAINT chk_jobs_status;
ALTER TABLE jobs RENAME CONSTRAINT chk_jobs_status_legacy TO chk_jobs_status;
