-- Current whole-workspace reservation, not accepted/outcome recovery history.
-- F owns exact acknowledgements, host proofs and conditional release. No
-- admission, restore or activation writer is installed here.
SET LOCAL lock_timeout = '5s';
ALTER TABLE sessions
    ADD COLUMN workspace_recovery_id uuid,
    ADD COLUMN workspace_recovery_token uuid,
    ADD COLUMN workspace_recovery jsonb,
    ADD COLUMN workspace_recovery_next_attempt_at timestamptz,
    ADD CONSTRAINT sessions_workspace_recovery_shape_check CHECK (
        (workspace_recovery_id IS NULL AND workspace_recovery_token IS NULL
         AND workspace_recovery IS NULL AND workspace_recovery_next_attempt_at IS NULL)
        OR
        (workspace_recovery_id IS NOT NULL AND workspace_recovery_token IS NOT NULL
         AND workspace_recovery IS NOT NULL AND workspace_recovery_next_attempt_at IS NOT NULL
         AND org_id <> '00000000-0000-0000-0000-000000000000'::uuid
         AND id <> '00000000-0000-0000-0000-000000000000'::uuid
         AND workspace_recovery_id <> '00000000-0000-0000-0000-000000000000'::uuid
         AND workspace_recovery_token <> '00000000-0000-0000-0000-000000000000'::uuid
         AND jsonb_typeof(workspace_recovery)='object'
         AND workspace_recovery->>'schema_version' IS NOT NULL
         AND jsonb_typeof(workspace_recovery->'schema_version')='number' AND workspace_recovery->>'schema_version'='1'
         AND workspace_recovery->>'version' IS NOT NULL
         AND jsonb_typeof(workspace_recovery->'version')='number' AND workspace_recovery->>'version' ~ '^[1-9][0-9]*$'
         AND workspace_recovery->>'session_epoch' IS NOT NULL
         AND jsonb_typeof(workspace_recovery->'session_epoch')='number' AND workspace_recovery->>'session_epoch' ~ '^(0|[1-9][0-9]*)$'
         AND NOT workspace_recovery ? 'thread_id' AND NOT workspace_recovery ? 'thread_epoch'
         AND workspace_recovery->'reservation' IS NOT NULL AND jsonb_typeof(workspace_recovery->'reservation')='object'
         AND workspace_recovery->>'state' IS NOT NULL
         AND workspace_recovery->>'state' IN ('reserved','recovering','blocked','uncertain','settled')
         AND workspace_recovery->>'physical_operation_state' IS NOT NULL
         AND workspace_recovery->>'physical_operation_state' IN ('reserved','in_progress','unknown','settled','fenced_drained')
         AND workspace_recovery->'physical_operation' IS NOT NULL AND jsonb_typeof(workspace_recovery->'physical_operation')='object'
         AND ((workspace_recovery->>'physical_operation_state' IN ('reserved','in_progress','unknown')
               AND NOT workspace_recovery ? 'physical_settlement_receipt')
              OR (workspace_recovery->>'physical_operation_state' IN ('settled','fenced_drained')
                  AND workspace_recovery->'physical_settlement_receipt' IS NOT NULL
                  AND jsonb_typeof(workspace_recovery->'physical_settlement_receipt')='object')))
    ) NOT VALID;
-- The recovery-ID and due indexes are built concurrently in standalone
-- migrations, outside this ALTER TABLE transaction. No recovery writer is
-- installed here; its eventual owner must require both valid indexes.

-- Evidence retention only, not an authority check. The future F owner must
-- install the exact immutable accepted/outcome/isolation receipt check before
-- adding any conditional clear/replace path. Expiry and a parsed settlement
-- object cannot clear this current slot. Original tokens survive revocation.
CREATE FUNCTION lifecycle143_retain_workspace_recovery()
RETURNS trigger LANGUAGE plpgsql VOLATILE SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP='TRUNCATE' THEN
        IF current_setting('transaction_isolation')<>'read committed' THEN
            RAISE EXCEPTION 'lifecycle recovery retention requires read committed' USING ERRCODE='55000';
        END IF;
        -- TRUNCATE already holds the relation lock; this is a fresh RC read.
        IF EXISTS (SELECT 1 FROM public.sessions WHERE workspace_recovery_id IS NOT NULL
                   OR workspace_recovery_token IS NOT NULL OR workspace_recovery IS NOT NULL
                   OR workspace_recovery_next_attempt_at IS NOT NULL) THEN
            RAISE EXCEPTION 'retained lifecycle recovery prevents truncate' USING ERRCODE='55000';
        END IF;
        RETURN NULL;
    END IF;
    IF OLD.workspace_recovery_id IS NOT NULL OR OLD.workspace_recovery_token IS NOT NULL
       OR OLD.workspace_recovery IS NOT NULL OR OLD.workspace_recovery_next_attempt_at IS NOT NULL THEN
        IF TG_OP='DELETE' THEN
            RAISE EXCEPTION 'retained lifecycle recovery prevents deletion' USING ERRCODE='55000';
        END IF;
        IF NEW.org_id IS DISTINCT FROM OLD.org_id OR NEW.id IS DISTINCT FROM OLD.id
           OR NEW.workspace_recovery_id IS DISTINCT FROM OLD.workspace_recovery_id
           OR NEW.workspace_recovery_token IS DISTINCT FROM OLD.workspace_recovery_token
           OR NEW.workspace_recovery IS NULL
           OR (NEW.workspace_recovery->'reservation')::text IS DISTINCT FROM (OLD.workspace_recovery->'reservation')::text
           OR (NEW.workspace_recovery->'physical_operation')::text IS DISTINCT FROM (OLD.workspace_recovery->'physical_operation')::text
           OR NEW.workspace_recovery->'session_epoch' IS DISTINCT FROM OLD.workspace_recovery->'session_epoch' THEN
            RAISE EXCEPTION 'retained lifecycle recovery requires exact receipt release' USING ERRCODE='55000';
        END IF;
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_sessions_retain_workspace_recovery
    BEFORE UPDATE OF org_id,id,workspace_recovery_id,workspace_recovery_token,workspace_recovery,workspace_recovery_next_attempt_at
    OR DELETE ON sessions FOR EACH ROW EXECUTE FUNCTION lifecycle143_retain_workspace_recovery();
CREATE TRIGGER trg_sessions_recovery_no_truncate
    BEFORE TRUNCATE ON sessions FOR EACH STATEMENT EXECUTE FUNCTION lifecycle143_retain_workspace_recovery();
