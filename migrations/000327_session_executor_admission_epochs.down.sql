-- Roll back metadata readers before removing these passive columns.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_executors
    DROP COLUMN admission_thread_epoch,
    DROP COLUMN admission_session_epoch;
