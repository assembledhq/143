-- Passive representation only. Legacy writers omit these columns and remain at
-- version zero; these values do not authorize admission, start, end or projection.
-- Canonical mutations, epoch relationships and activation guards are separate work.
-- Session and thread columns use separate migration transactions so neither
-- transaction holds both hot-table locks at the same time.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_threads
    ADD COLUMN admission_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN end_epoch bigint,
    ADD COLUMN lifecycle_version smallint NOT NULL DEFAULT 0;
