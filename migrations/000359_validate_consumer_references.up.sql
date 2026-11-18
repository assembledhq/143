-- Validation is separated from installation so its scan does not retain the
-- stronger installation locks on existing hot tables.
SET LOCAL lock_timeout = '5s';
ALTER TABLE thread_runtimes
    VALIDATE CONSTRAINT thread_runtimes_input_allocation_check,
    VALIDATE CONSTRAINT thread_runtimes_own_provider_result_check,
    VALIDATE CONSTRAINT thread_runtimes_input_receipt_shapes_check,
    VALIDATE CONSTRAINT thread_runtimes_consumer_job_fk;
ALTER TABLE session_input_dispatches
    VALIDATE CONSTRAINT session_input_dispatches_consumer_shape_check,
    VALIDATE CONSTRAINT session_input_dispatches_control_shape_check,
    VALIDATE CONSTRAINT session_input_dispatches_consumer_job_fk,
    VALIDATE CONSTRAINT session_input_dispatches_consumer_runtime_fk;
