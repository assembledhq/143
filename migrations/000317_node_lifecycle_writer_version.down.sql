-- Roll back applications using this column before removing the passive metadata.
SET LOCAL lock_timeout = '5s';

ALTER TABLE nodes DROP COLUMN lifecycle_writer_version;
