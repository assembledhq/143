SET LOCAL lock_timeout = '5s';

ALTER TABLE session_human_input_requests
    DROP COLUMN admission_thread_epoch,
    DROP COLUMN admission_session_epoch;
