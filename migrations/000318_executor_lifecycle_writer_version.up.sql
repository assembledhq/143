-- Passive last-writer metadata only; dispatcher allocation and executor refreshes
-- are observations, not process/build provenance or a compatible writer floor.
-- Legacy writers omitting the column can retain a stale value on existing rows.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_executors
    ADD COLUMN lifecycle_writer_version integer NOT NULL DEFAULT 0
        CONSTRAINT session_executors_lifecycle_writer_version_nonnegative CHECK (lifecycle_writer_version >= 0);
