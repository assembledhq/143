-- One current preparation per job, not a second allocation/event history.
-- B/E own typed target verification, current authority, CAS and settlement.
-- Closing/revoking execution does not establish physical target-operation drain.
SET LOCAL lock_timeout = '5s';
CREATE TABLE session_runtime_start_preparations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL,
    thread_id uuid,
    job_id uuid NOT NULL,
    input_dispatch_ids uuid[] NOT NULL,
    dispatch_kind text NOT NULL,
    request_id uuid,
    session_epoch bigint NOT NULL,
    thread_epoch bigint,
    version bigint NOT NULL,
    token uuid NOT NULL,
    job_attempt_token uuid NOT NULL,
    expected_control_version bigint,
    state text NOT NULL,
    deadline_at timestamptz NOT NULL,
    next_attempt_at timestamptz NOT NULL,
    revoked_at timestamptz,
    target jsonb NOT NULL,
    resume_identity jsonb NOT NULL,
    check_disposition text,
    check_receipt jsonb,
    check_consumed_at timestamptz,
    physical_operation_id uuid,
    physical_operation_state text NOT NULL DEFAULT 'none',
    physical_operation_scope text,
    physical_operation jsonb,
    physical_source_key text,
    physical_settlement_receipt jsonb,
    reason text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT start_preparations_job_unique UNIQUE (org_id, job_id),
    CONSTRAINT start_preparations_binding_check CHECK (
        id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND org_id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND session_id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND (thread_id IS NULL OR thread_id <> '00000000-0000-0000-0000-000000000000'::uuid)
        AND job_id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND token <> '00000000-0000-0000-0000-000000000000'::uuid
        AND job_attempt_token <> '00000000-0000-0000-0000-000000000000'::uuid
        AND lifecycle143_valid_input_dispatch_set(input_dispatch_ids)
        AND dispatch_kind IN ('message','answer','denial','initial','retry','recovery')
        AND (dispatch_kind='message' OR cardinality(input_dispatch_ids)=1)
        AND ((dispatch_kind IN ('answer','denial') AND request_id IS NOT NULL
              AND request_id <> '00000000-0000-0000-0000-000000000000'::uuid)
             OR (dispatch_kind NOT IN ('answer','denial') AND request_id IS NULL))
        AND session_epoch>=0
        AND ((thread_id IS NULL AND thread_epoch IS NULL)
             OR (thread_id IS NOT NULL AND thread_epoch IS NOT NULL AND thread_epoch>=0))
    ),
    CONSTRAINT start_preparations_execution_check CHECK (
        version>0 AND state IN ('preparing','authorized','closed')
        AND (expected_control_version IS NULL OR expected_control_version>0)
        AND (state<>'closed' OR revoked_at IS NOT NULL)
        AND jsonb_typeof(target)='object' AND jsonb_typeof(resume_identity)='object'
        AND ((check_disposition IS NULL AND check_receipt IS NULL AND check_consumed_at IS NULL)
             OR (check_disposition IS NOT NULL
                 AND check_disposition IN ('verified','retryable','unavailable','unsupported')
                 AND check_receipt IS NOT NULL AND jsonb_typeof(check_receipt)='object'
                 AND (check_consumed_at IS NULL OR check_disposition='verified')))
    ),
    CONSTRAINT start_preparations_physical_check CHECK (
        (physical_operation_state='none'
         AND physical_operation_id IS NULL AND physical_operation_scope IS NULL
         AND physical_operation IS NULL AND physical_source_key IS NULL
         AND physical_settlement_receipt IS NULL)
        OR
        (physical_operation_state IN ('reserved','in_progress','unknown','settled','fenced_drained')
         AND physical_operation_id IS NOT NULL
         AND physical_operation_id <> '00000000-0000-0000-0000-000000000000'::uuid
         AND physical_operation_scope IS NOT NULL
         AND physical_operation_scope IN ('target_only','whole_session')
         AND physical_operation IS NOT NULL AND jsonb_typeof(physical_operation)='object'
         AND (physical_source_key IS NULL OR btrim(physical_source_key)<>'')
         AND ((physical_operation_state IN ('reserved','in_progress','unknown') AND physical_settlement_receipt IS NULL)
              OR (physical_operation_state IN ('settled','fenced_drained')
                  AND physical_settlement_receipt IS NOT NULL AND jsonb_typeof(physical_settlement_receipt)='object')))
    )
);

-- Complete child-side indexes retain efficient FK checks and reverse discovery
-- after logical closure, not only while the preparation is active.
CREATE INDEX idx_start_preparations_scope ON session_runtime_start_preparations(org_id,session_id,thread_id);
CREATE INDEX idx_start_preparations_thread ON session_runtime_start_preparations(org_id,thread_id);
CREATE INDEX idx_start_preparations_request ON session_runtime_start_preparations(org_id,session_id,request_id);
CREATE INDEX idx_start_preparations_due ON session_runtime_start_preparations(next_attempt_at,id)
    WHERE state='preparing' OR physical_operation_state IN ('reserved','in_progress','unknown');
CREATE INDEX idx_start_preparations_source ON session_runtime_start_preparations(physical_source_key,org_id,session_id)
    WHERE physical_operation_state IN ('reserved','in_progress','unknown');

-- Structural retention only. A nonempty JSON object is not physical proof:
-- B/E must validate the exact host/workspace/operation settlement before writing
-- a settled state, and bind any source projection to the full typed target.
CREATE FUNCTION lifecycle143_retain_preparation_operation()
RETURNS trigger LANGUAGE plpgsql VOLATILE SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP='TRUNCATE' THEN
        IF current_setting('transaction_isolation')<>'read committed' THEN
            RAISE EXCEPTION 'lifecycle preparation retention requires read committed' USING ERRCODE='55000';
        END IF;
        IF EXISTS (SELECT 1 FROM public.session_runtime_start_preparations) THEN
            RAISE EXCEPTION 'retained lifecycle preparation prevents truncate' USING ERRCODE='55000';
        END IF;
        RETURN NULL;
    END IF;
    IF OLD.physical_operation_state IN ('reserved','in_progress','unknown') THEN
        IF TG_OP='DELETE' THEN
            RAISE EXCEPTION 'unresolved lifecycle target operation prevents deletion' USING ERRCODE='55000';
        END IF;
        IF ROW(NEW.id,NEW.org_id,NEW.session_id,NEW.thread_id,NEW.job_id,NEW.input_dispatch_ids,
               NEW.dispatch_kind,NEW.request_id,NEW.session_epoch,NEW.thread_epoch,NEW.token,
               NEW.job_attempt_token,NEW.target,NEW.resume_identity,NEW.physical_operation_id,
               NEW.physical_operation_scope,NEW.physical_operation,NEW.physical_source_key)
           IS DISTINCT FROM
           ROW(OLD.id,OLD.org_id,OLD.session_id,OLD.thread_id,OLD.job_id,OLD.input_dispatch_ids,
               OLD.dispatch_kind,OLD.request_id,OLD.session_epoch,OLD.thread_epoch,OLD.token,
               OLD.job_attempt_token,OLD.target,OLD.resume_identity,OLD.physical_operation_id,
               OLD.physical_operation_scope,OLD.physical_operation,OLD.physical_source_key)
           OR NEW.target::text IS DISTINCT FROM OLD.target::text
           OR NEW.resume_identity::text IS DISTINCT FROM OLD.resume_identity::text
           OR NEW.physical_operation::text IS DISTINCT FROM OLD.physical_operation::text
           OR NEW.physical_operation_state='none' THEN
            RAISE EXCEPTION 'unresolved lifecycle target operation cannot be replaced' USING ERRCODE='55000';
        END IF;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_start_preparations_retain_operation
    BEFORE UPDATE OR DELETE ON session_runtime_start_preparations
    FOR EACH ROW EXECUTE FUNCTION lifecycle143_retain_preparation_operation();
CREATE TRIGGER trg_start_preparations_no_truncate
    BEFORE TRUNCATE ON session_runtime_start_preparations
    FOR EACH STATEMENT EXECUTE FUNCTION lifecycle143_retain_preparation_operation();
-- Scoped references and job retention follow before any producer can use this.
