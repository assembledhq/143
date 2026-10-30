-- Shared admission/consumer representation. Existing passive rows stay NULL;
-- no old input receives inferred epochs, a consumer or a runnable disposition.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_input_dispatches
    ADD COLUMN admission_state text,
    ADD COLUMN consumer_kind text,
    ADD COLUMN job_id uuid,
    ADD COLUMN runtime_id uuid,
    ADD COLUMN job_lock_token uuid,
    ADD COLUMN runtime_lease_token uuid,
    ADD COLUMN next_attempt_at timestamptz,
    ADD COLUMN deadline_at timestamptz,
    ADD COLUMN reason text,
    ADD COLUMN control_receipt_id uuid,
    ADD COLUMN control_version bigint,
    ADD COLUMN control_state text,
    ADD COLUMN control_snapshot jsonb,
    ADD COLUMN control_next_attempt_at timestamptz,
    ADD COLUMN control_refund_receipt_id uuid,
    ADD CONSTRAINT session_input_dispatches_consumer_shape_check CHECK (
        (admission_state IS NULL AND consumer_kind IS NULL AND job_id IS NULL
         AND runtime_id IS NULL AND job_lock_token IS NULL AND runtime_lease_token IS NULL
         AND next_attempt_at IS NULL AND deadline_at IS NULL AND reason IS NULL
         AND control_receipt_id IS NULL)
        OR
        (admission_state IS NOT NULL AND admission_state IN ('waiting','bound','terminal','historical')
         AND consumer_kind IS NOT NULL AND consumer_kind IN ('none','job','runtime')
         AND CASE consumer_kind
             WHEN 'none' THEN job_id IS NULL AND runtime_id IS NULL
                 AND job_lock_token IS NULL AND runtime_lease_token IS NULL
             WHEN 'job' THEN job_id IS NOT NULL AND runtime_id IS NULL
                 AND job_lock_token IS NULL AND runtime_lease_token IS NULL
             WHEN 'runtime' THEN job_id IS NOT NULL AND runtime_id IS NOT NULL AND thread_id IS NOT NULL
                 AND job_lock_token IS NOT NULL AND runtime_lease_token IS NOT NULL
                 AND session_epoch IS NOT NULL AND thread_epoch IS NOT NULL
             ELSE false END
         AND (job_id IS NULL OR job_id <> '00000000-0000-0000-0000-000000000000'::uuid)
         AND (runtime_id IS NULL OR runtime_id <> '00000000-0000-0000-0000-000000000000'::uuid)
         AND (job_lock_token IS NULL OR job_lock_token <> '00000000-0000-0000-0000-000000000000'::uuid)
         AND (runtime_lease_token IS NULL OR runtime_lease_token <> '00000000-0000-0000-0000-000000000000'::uuid)
         AND (admission_state<>'waiting' OR (consumer_kind='none' AND next_attempt_at IS NOT NULL))
         AND (admission_state<>'bound' OR (
             consumer_kind IN ('job','runtime') AND session_epoch IS NOT NULL
             AND ((thread_id IS NULL AND thread_epoch IS NULL) OR (thread_id IS NOT NULL AND thread_epoch IS NOT NULL))
             AND payload_digest ~ '^[0-9a-f]{64}$' AND payload_digest<>repeat('0',64)
         )))
    ) NOT VALID,
    ADD CONSTRAINT session_input_dispatches_control_shape_check CHECK (
        (control_receipt_id IS NULL AND control_version IS NULL AND control_state IS NULL
         AND control_snapshot IS NULL AND control_next_attempt_at IS NULL AND control_refund_receipt_id IS NULL)
        OR
        (control_receipt_id IS NOT NULL AND control_receipt_id <> '00000000-0000-0000-0000-000000000000'::uuid
         AND control_version IS NOT NULL AND control_version>0
         AND control_state IS NOT NULL
         AND control_state IN ('awaiting_control','retry_queued','abandoned','superseded','superseded_delivery_uncertain')
         AND control_snapshot IS NOT NULL AND jsonb_typeof(control_snapshot)='object'
         AND admission_state IS NOT NULL AND admission_state IN ('bound','terminal','historical')
         AND consumer_kind IS NOT NULL AND consumer_kind IN ('job','runtime')
         AND (control_refund_receipt_id IS NULL OR control_refund_receipt_id <> '00000000-0000-0000-0000-000000000000'::uuid)
         AND (control_state<>'awaiting_control' OR (admission_state='bound' AND control_next_attempt_at IS NOT NULL)))
    ) NOT VALID;

CREATE UNIQUE INDEX idx_session_input_dispatches_bound_request
    ON session_input_dispatches(org_id,request_id)
    WHERE request_id IS NOT NULL AND kind IN ('answer','denial') AND admission_state='bound';
CREATE INDEX idx_session_input_dispatches_waiting_due
    ON session_input_dispatches(next_attempt_at,id) WHERE admission_state='waiting';
CREATE INDEX idx_session_input_dispatches_consumer_lane
    ON session_input_dispatches(org_id,session_id,thread_id,admission_state);
CREATE INDEX idx_session_input_dispatches_consumer_job
    ON session_input_dispatches(org_id,job_id,id) WHERE job_id IS NOT NULL;
CREATE INDEX idx_session_input_dispatches_consumer_runtime
    ON session_input_dispatches(org_id,runtime_id) WHERE runtime_id IS NOT NULL;
CREATE INDEX idx_session_input_dispatches_bound_job
    ON session_input_dispatches(org_id,job_id,id) WHERE admission_state='bound';
CREATE INDEX idx_session_input_dispatches_control_due
    ON session_input_dispatches(control_next_attempt_at,id) WHERE control_state='awaiting_control';
-- Real scoped references and matching retention exclusions follow before use.
