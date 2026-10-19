-- Passive binary metadata only; this does not establish a compatible writer floor.
-- Old binaries omitting this column can retain a stale value on a reused node ID.
SET LOCAL lock_timeout = '5s';

ALTER TABLE nodes
    ADD COLUMN lifecycle_writer_version integer NOT NULL DEFAULT 0
        CONSTRAINT nodes_lifecycle_writer_version_nonnegative CHECK (lifecycle_writer_version >= 0);
