-- Passive provenance only. Threaded and unthreaded legacy executors retain
-- unknown admission epochs; no current parent values are inferred.
-- Canonical allocation and epoch fencing are separate work.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_executors
    ADD COLUMN admission_session_epoch bigint,
    ADD COLUMN admission_thread_epoch bigint;
