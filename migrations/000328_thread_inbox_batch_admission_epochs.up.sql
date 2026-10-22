-- Passive provenance only. Legacy batches retain unknown admission epochs.
-- Canonical dispatch binding, acknowledgement and epoch fencing are separate work.
SET LOCAL lock_timeout = '5s';

ALTER TABLE thread_inbox_delivery_batches
    ADD COLUMN admission_session_epoch bigint,
    ADD COLUMN admission_thread_epoch bigint;
