-- Roll back metadata readers before removing these passive columns.
SET LOCAL lock_timeout = '5s';

ALTER TABLE thread_inbox_delivery_batches
    DROP COLUMN admission_thread_epoch,
    DROP COLUMN admission_session_epoch;
