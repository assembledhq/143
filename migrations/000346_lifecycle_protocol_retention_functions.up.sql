-- G.1 protocol core only: retain activation provenance before any router uses
-- it. Full minimum-writer and OLD/NEW child-table coverage is a later G gate.
-- No session is stamped or activated by this migration.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION lifecycle143_protocol_shared_interlock(p_org_id uuid, p_session_id uuid)
RETURNS void LANGUAGE plpgsql VOLATILE
SET search_path = pg_catalog, public AS $$
BEGIN
    IF p_org_id IS NULL OR p_session_id IS NULL THEN
        RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'LIFECYCLE_GUARD_PROVENANCE_REQUIRED';
    END IF;
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'LIFECYCLE_READ_COMMITTED_REQUIRED';
    END IF;
    IF NOT pg_try_advisory_xact_lock_shared(hashtextextended(
        'session_lifecycle_cutover:' || p_org_id::text || ':' || p_session_id::text, 0)) THEN
        -- An actual try-lock false is the only cause of this retryable error.
        RAISE EXCEPTION USING ERRCODE = '55P03', MESSAGE = 'LIFECYCLE_CUTOVER_INTERLOCK_BUSY';
    END IF;
END;
$$;

CREATE FUNCTION lifecycle143_retain_session_protocol()
RETURNS trigger LANGUAGE plpgsql VOLATILE
SET search_path = pg_catalog, public AS $$
BEGIN
    IF TG_OP <> 'INSERT' AND OLD.lifecycle_version > 0 THEN
        PERFORM public.lifecycle143_protocol_shared_interlock(OLD.org_id, OLD.id);
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'LIFECYCLE_GUARD_PROVENANCE_REQUIRED';
        END IF;
        IF (NEW.org_id, NEW.id) IS DISTINCT FROM (OLD.org_id, OLD.id)
           OR NEW.lifecycle_version < OLD.lifecycle_version THEN
            RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'LIFECYCLE_GUARD_PROVENANCE_REQUIRED';
        END IF;
    ELSIF TG_OP <> 'DELETE' AND NEW.lifecycle_version > 0 THEN
        PERFORM public.lifecycle143_protocol_shared_interlock(NEW.org_id, NEW.id);
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION lifecycle143_retain_cutover_protocol()
RETURNS trigger LANGUAGE plpgsql VOLATILE
SET search_path = pg_catalog, public AS $$
DECLARE
    old_org uuid;
    old_session uuid;
    new_org uuid;
    new_session uuid;
    old_protocol smallint;
    new_protocol smallint;
    scope record;
    retained_old boolean := false;
BEGIN
    IF TG_OP <> 'INSERT' THEN old_org := OLD.org_id; old_session := OLD.session_id; END IF;
    IF TG_OP <> 'DELETE' THEN new_org := NEW.org_id; new_session := NEW.session_id; END IF;
    -- Late-discovered OLD/NEW scopes share stable ordering and never wait for
    -- an activator. Failure aborts the entire caller transaction.
    FOR scope IN
        SELECT DISTINCT s.org_id, s.session_id
        FROM (VALUES (old_org, old_session), (new_org, new_session)) AS s(org_id, session_id)
        WHERE s.org_id IS NOT NULL AND s.session_id IS NOT NULL
        ORDER BY s.org_id, s.session_id
    LOOP
        PERFORM public.lifecycle143_protocol_shared_interlock(scope.org_id, scope.session_id);
    END LOOP;
    -- These are separate fresh lookups after interlock acquisition. VOLATILE
    -- is intentional; the caller statement's earlier snapshot is not authority.
    IF old_session IS NOT NULL THEN
        SELECT lifecycle_version INTO old_protocol FROM public.sessions
        WHERE org_id = old_org AND id = old_session;
        retained_old := OLD.state = 'active' OR COALESCE(old_protocol, 0) > 0;
    END IF;
    IF new_session IS NOT NULL THEN
        SELECT lifecycle_version INTO new_protocol FROM public.sessions
        WHERE org_id = new_org AND id = new_session;
    END IF;
    IF retained_old THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'LIFECYCLE_GUARD_PROVENANCE_REQUIRED';
        END IF;
        IF (new_org, new_session) IS DISTINCT FROM (old_org, old_session)
           OR NEW.state <> 'active' OR NEW.version < OLD.version
           OR (OLD.activated_at IS NOT NULL AND NEW.activated_at IS DISTINCT FROM OLD.activated_at)
           OR (OLD.legacy_input_cutoff_id IS NOT NULL AND NEW.legacy_input_cutoff_id IS DISTINCT FROM OLD.legacy_input_cutoff_id)
           OR (OLD.normalization IS NOT NULL AND NEW.normalization IS NULL)
           OR (OLD.state = 'active' AND NEW.normalization IS DISTINCT FROM OLD.normalization)
           OR (OLD.resumability_capability IS NOT NULL AND NEW.resumability_capability IS NULL)
           OR (OLD.minimum_writer_version IS NOT NULL AND
               (NEW.minimum_writer_version IS NULL OR NEW.minimum_writer_version < OLD.minimum_writer_version)) THEN
            RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'LIFECYCLE_GUARD_PROVENANCE_REQUIRED';
        END IF;
    END IF;
    IF COALESCE(new_protocol, 0) > 0 AND
       (NEW.state <> 'active' OR NEW.activated_at IS NULL OR NEW.legacy_input_cutoff_id IS NULL
        OR NEW.minimum_writer_version IS NULL OR NEW.minimum_writer_version < new_protocol
        OR NEW.normalization IS NULL OR jsonb_typeof(NEW.normalization) <> 'object'
        OR NEW.resumability_capability IS NULL OR jsonb_typeof(NEW.resumability_capability) <> 'object') THEN
        RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'LIFECYCLE_GUARD_PROVENANCE_REQUIRED';
    END IF;
    -- Coarse retention checks cannot certify JSON release/provider evidence.
    -- The strict mode resolver and eventual activation owner still validate it.
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION lifecycle143_assert_protocol_rollback_safe()
RETURNS void LANGUAGE plpgsql VOLATILE
SET search_path = pg_catalog, public AS $$
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'LIFECYCLE_READ_COMMITTED_REQUIRED';
    END IF;
    IF EXISTS (SELECT 1 FROM public.sessions WHERE lifecycle_version > 0)
       OR EXISTS (SELECT 1 FROM public.session_lifecycle_cutovers WHERE state = 'active') THEN
        RAISE EXCEPTION USING ERRCODE = '55000', MESSAGE = 'LIFECYCLE_GUARD_PROVENANCE_REQUIRED';
    END IF;
END;
$$;

CREATE FUNCTION lifecycle143_reject_active_protocol_truncate()
RETURNS trigger LANGUAGE plpgsql VOLATILE
SET search_path = pg_catalog, public AS $$
BEGIN
    -- The target relation is already exclusively locked by TRUNCATE. Lock the
    -- other source before the fresh assertion too, without a late inverse wait.
    -- A relation-lock refusal is not an advisory interlock-busy signal.
    LOCK TABLE public.sessions, public.session_lifecycle_cutovers IN SHARE MODE NOWAIT;
    PERFORM public.lifecycle143_assert_protocol_rollback_safe();
    RETURN NULL;
END;
$$;
