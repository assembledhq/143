-- Validate the legacy status check restored by 352 down without holding that
-- installer's ACCESS EXCLUSIVE lock across a full jobs scan.
SET LOCAL lock_timeout = '5s';
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_status_legacy;
