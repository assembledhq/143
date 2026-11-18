-- Passive provenance only. Existing runtimes retain unknown admission epochs
-- and no recorded drain proof. Status and lease expiry do not prove process drain.
-- Canonical allocation, immutable receipts and drain observation are separate work.
SET LOCAL lock_timeout = '5s';

ALTER TABLE thread_runtimes
    ADD COLUMN admission_session_epoch bigint,
    ADD COLUMN admission_thread_epoch bigint,
    ADD COLUMN drain_proof jsonb;
