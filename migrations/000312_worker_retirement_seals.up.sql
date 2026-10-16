-- A retirement seal is permanent for one exact process generation. In
-- particular, deleting an old nodes row must not reopen a delayed writer.
-- Therefore node_id deliberately has no FK to the garbage-collected nodes.
CREATE TABLE worker_retirement_seals (
    -- lint:no-org-id reason="cluster-scoped retirement seals fence process generations across all tenants"
    node_id text PRIMARY KEY CHECK (length(trim(node_id)) > 0),
    deploy_id text NOT NULL CHECK (length(trim(deploy_id)) > 0),
    build_sha text NOT NULL CHECK (length(trim(build_sha)) > 0),
    sealed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Both sides use transaction-scoped locks. A sealer takes the exclusive
-- version in its own statement, then obtains a fresh READ COMMITTED snapshot.
-- Admissions already holding this shared lock commit first and are counted;
-- admissions arriving later observe the committed seal before doing work.
CREATE FUNCTION check_worker_retirement_admission(node_id text) RETURNS void
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    IF node_id IS NULL OR node_id = '' THEN RETURN; END IF;
    PERFORM pg_advisory_xact_lock_shared(hashtextextended('worker-retirement:' || node_id, 0));
    IF EXISTS (SELECT 1 FROM worker_retirement_seals s WHERE s.node_id = check_worker_retirement_admission.node_id) THEN
        RAISE EXCEPTION 'worker generation is sealed for retirement' USING ERRCODE = '55000';
    END IF;
END
$$;

CREATE FUNCTION guard_worker_retirement_job() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'running' THEN
        IF TG_OP = 'INSERT' THEN
            PERFORM check_worker_retirement_admission(NEW.locked_by_node_id);
        ELSIF OLD.status IS DISTINCT FROM 'running'
           OR NEW.locked_by_node_id IS DISTINCT FROM OLD.locked_by_node_id
           OR NEW.lock_token IS DISTINCT FROM OLD.lock_token
           OR NEW.run_owner_id IS DISTINCT FROM OLD.run_owner_id
           OR NEW.owner_kind IS DISTINCT FROM OLD.owner_kind THEN
            PERFORM check_worker_retirement_admission(NEW.locked_by_node_id);
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION guard_worker_retirement_preview() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status IN ('starting', 'ready', 'partially_ready', 'unhealthy') THEN
        IF TG_OP = 'INSERT' THEN
            PERFORM check_worker_retirement_admission(NEW.worker_node_id);
        ELSIF OLD.status NOT IN ('starting', 'ready', 'partially_ready', 'unhealthy')
           OR ROW(NEW.worker_node_id, NEW.org_id, NEW.session_id, NEW.preview_target_id,
                  NEW.preview_handle, NEW.port, NEW.runtime_workspace_revision)
              IS DISTINCT FROM
              ROW(OLD.worker_node_id, OLD.org_id, OLD.session_id, OLD.preview_target_id,
                  OLD.preview_handle, OLD.port, OLD.runtime_workspace_revision)
           OR (NEW.preview_holding_container AND NOT OLD.preview_holding_container) THEN
            PERFORM check_worker_retirement_admission(NEW.worker_node_id);
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION guard_worker_retirement_preview_runtime() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status IN ('starting', 'ready', 'draining') THEN
        IF TG_OP = 'INSERT' THEN
            PERFORM check_worker_retirement_admission(NEW.worker_node_id);
        ELSIF OLD.status NOT IN ('starting', 'ready', 'draining')
           OR ROW(NEW.worker_node_id, NEW.org_id, NEW.preview_instance_id, NEW.runtime_epoch,
                  NEW.preview_handle, NEW.endpoint_url, NEW.primary_port)
              IS DISTINCT FROM
              ROW(OLD.worker_node_id, OLD.org_id, OLD.preview_instance_id, OLD.runtime_epoch,
                  OLD.preview_handle, OLD.endpoint_url, OLD.primary_port) THEN
            PERFORM check_worker_retirement_admission(NEW.worker_node_id);
        END IF;
    END IF;
    RETURN NEW;
END
$$;

-- Old binaries may try resume or re-registration after cleanup. A seal never
-- grants that generation admission again; a replacement needs a new node ID.
CREATE FUNCTION guard_worker_retirement_node() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'active' OR NEW.drain_intent = 'none' THEN
        IF TG_OP = 'INSERT' THEN
            PERFORM check_worker_retirement_admission(NEW.id);
        ELSIF NEW.status IS DISTINCT FROM OLD.status
           OR NEW.drain_intent IS DISTINCT FROM OLD.drain_intent THEN
            PERFORM check_worker_retirement_admission(NEW.id);
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION guard_worker_retirement_seal_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'worker retirement seals are immutable' USING ERRCODE = '55000';
END
$$;

CREATE TRIGGER trg_worker_retirement_job BEFORE INSERT OR UPDATE ON jobs
    FOR EACH ROW EXECUTE FUNCTION guard_worker_retirement_job();
CREATE TRIGGER trg_worker_retirement_preview BEFORE INSERT OR UPDATE ON preview_instances
    FOR EACH ROW EXECUTE FUNCTION guard_worker_retirement_preview();
CREATE TRIGGER trg_worker_retirement_preview_runtime BEFORE INSERT OR UPDATE ON preview_runtimes
    FOR EACH ROW EXECUTE FUNCTION guard_worker_retirement_preview_runtime();
CREATE TRIGGER trg_worker_retirement_node BEFORE INSERT OR UPDATE ON nodes
    FOR EACH ROW EXECUTE FUNCTION guard_worker_retirement_node();
CREATE TRIGGER trg_worker_retirement_seal_immutable BEFORE UPDATE OR DELETE ON worker_retirement_seals
    FOR EACH ROW EXECUTE FUNCTION guard_worker_retirement_seal_immutable();
ALTER TABLE jobs ENABLE ALWAYS TRIGGER trg_worker_retirement_job;
ALTER TABLE preview_instances ENABLE ALWAYS TRIGGER trg_worker_retirement_preview;
ALTER TABLE preview_runtimes ENABLE ALWAYS TRIGGER trg_worker_retirement_preview_runtime;
ALTER TABLE nodes ENABLE ALWAYS TRIGGER trg_worker_retirement_node;
ALTER TABLE worker_retirement_seals ENABLE ALWAYS TRIGGER trg_worker_retirement_seal_immutable;
