-- Separate validation avoids retaining the installation's stronger table lock.
SET LOCAL lock_timeout = '5s';
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_status;
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_parked_without_lease;
