-- Passive shared consumer/allocation slots. B owns allocation/settlement/launch
-- writers. No existing runtime is inferred to have version-1 input authority.
SET LOCAL lock_timeout = '5s';
CREATE FUNCTION lifecycle143_valid_input_dispatch_set(p_ids uuid[])
RETURNS boolean LANGUAGE sql IMMUTABLE STRICT
SET search_path = pg_catalog AS $$
    SELECT CASE WHEN array_ndims(p_ids) IS DISTINCT FROM 1 OR array_lower(p_ids,1)<>1 THEN false
        ELSE cardinality(p_ids)>0 AND array_position(p_ids,NULL) IS NULL
         AND array_position(p_ids,'00000000-0000-0000-0000-000000000000'::uuid) IS NULL
         AND cardinality(p_ids)=(SELECT count(DISTINCT id) FROM unnest(p_ids) AS element(id)) END;
$$;
ALTER TABLE thread_runtimes
    ADD COLUMN job_id uuid,
    ADD COLUMN job_attempt_token uuid,
    ADD COLUMN allocation_digest text,
    ADD COLUMN allocation_expected_turn integer,
    ADD COLUMN input_dispatch_ids uuid[],
    ADD COLUMN provider_terminal_result jsonb,
    ADD COLUMN provider_terminal_result_digest text,
    ADD COLUMN provider_terminal_settled_at timestamptz,
    ADD COLUMN lease_loss_receipt jsonb,
    ADD COLUMN aborted_allocation_receipt jsonb,
    ADD COLUMN first_provider_launch_marker jsonb,
    ADD COLUMN provider_invocation_ordinal bigint,
    ADD COLUMN provider_invocation_nonce uuid,
    ADD COLUMN provider_invocation_purpose text,
    ADD CONSTRAINT thread_runtimes_input_allocation_check CHECK (
        (job_id IS NULL AND job_attempt_token IS NULL AND allocation_digest IS NULL
         AND allocation_expected_turn IS NULL
         AND input_dispatch_ids IS NULL AND provider_invocation_ordinal IS NULL
         AND provider_invocation_nonce IS NULL AND provider_invocation_purpose IS NULL
         AND first_provider_launch_marker IS NULL AND provider_terminal_result IS NULL
         AND provider_terminal_result_digest IS NULL AND provider_terminal_settled_at IS NULL
         AND lease_loss_receipt IS NULL AND aborted_allocation_receipt IS NULL)
        OR
        (job_id IS NOT NULL AND job_id <> '00000000-0000-0000-0000-000000000000'::uuid
         AND job_attempt_token IS NOT NULL AND job_attempt_token <> '00000000-0000-0000-0000-000000000000'::uuid
         AND allocation_digest IS NOT NULL AND allocation_digest ~ '^[0-9a-f]{64}$'
         AND allocation_digest <> repeat('0',64)
         AND allocation_expected_turn IS NOT NULL AND allocation_expected_turn >= 0
         AND input_dispatch_ids IS NOT NULL AND lifecycle143_valid_input_dispatch_set(input_dispatch_ids)
         AND admission_session_epoch IS NOT NULL AND admission_session_epoch >= 0
         AND admission_thread_epoch IS NOT NULL AND admission_thread_epoch >= 0
         AND lease_token <> '00000000-0000-0000-0000-000000000000'::uuid
         AND provider_invocation_ordinal IS NOT NULL AND provider_invocation_ordinal >= 0
         AND ((provider_invocation_nonce IS NULL AND provider_invocation_purpose IS NULL)
              OR (provider_invocation_nonce IS NOT NULL AND provider_invocation_purpose IS NOT NULL
                  AND provider_invocation_nonce <> '00000000-0000-0000-0000-000000000000'::uuid
                  AND btrim(provider_invocation_purpose) <> '')))
    ) NOT VALID,
    ADD CONSTRAINT thread_runtimes_own_provider_result_check CHECK (
        (provider_terminal_result IS NULL AND provider_terminal_result_digest IS NULL AND provider_terminal_settled_at IS NULL)
        OR (provider_terminal_result IS NOT NULL AND jsonb_typeof(provider_terminal_result)='object'
            AND provider_terminal_result_digest IS NOT NULL AND provider_terminal_result_digest ~ '^[0-9a-f]{64}$'
            AND provider_terminal_result_digest <> repeat('0',64)
            AND provider_terminal_settled_at IS NOT NULL)
    ) NOT VALID,
    ADD CONSTRAINT thread_runtimes_input_receipt_shapes_check CHECK (
        (first_provider_launch_marker IS NULL OR jsonb_typeof(first_provider_launch_marker)='object')
        AND (lease_loss_receipt IS NULL OR jsonb_typeof(lease_loss_receipt)='object')
        AND (aborted_allocation_receipt IS NULL OR jsonb_typeof(aborted_allocation_receipt)='object')
    ) NOT VALID;
-- Scoped references, reverse indexes and retention exclusions are installed by
-- following migrations before this component can be used by a producer.
