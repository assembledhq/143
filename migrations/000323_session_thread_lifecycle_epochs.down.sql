-- Roll back applications reading the passive fields before removing them.
-- No lifecycle authority or activation state is installed by this migration.
-- Session and thread columns use separate migration transactions so neither
-- transaction holds both hot-table locks at the same time.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_threads
    DROP COLUMN admission_epoch,
    DROP COLUMN end_epoch,
    DROP COLUMN lifecycle_version;
