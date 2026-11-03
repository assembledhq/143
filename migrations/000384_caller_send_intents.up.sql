-- Passive caller-send storage. Readiness is not enablement; no row is enabled here.
SET LOCAL lock_timeout = '5s';

CREATE TABLE caller_send_intent_control ( -- lint:no-org-id reason="global caller-send schema readiness and explicit enablement interlock"
    singleton boolean PRIMARY KEY CHECK (singleton),
    schema_ready boolean NOT NULL,
    intents_enabled boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL
);

CREATE FUNCTION caller143_require_send_intents_enabled() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM caller_send_intent_control
    WHERE singleton AND schema_ready AND intents_enabled FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'caller_send_intents_disabled' USING ERRCODE = '55000';
    END IF;
END;
$$;

CREATE FUNCTION caller143_send_digest_valid(value text) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (octet_length(value) = 64 AND value COLLATE "C" ~ '^[0-9a-f]{64}$'
        AND value <> repeat('0',64)) IS TRUE
$$;

CREATE FUNCTION caller143_send_input_valid(value jsonb, purpose text, owner_id uuid,
    org_id uuid, session_id uuid, thread_id uuid) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (jsonb_typeof(value) = 'object' AND octet_length(value::text) <= 8388608
        AND value->'version' = '1'::jsonb AND value->>'purpose' = purpose
        AND value->>'org_id' = org_id::text AND value->>'session_id' = session_id::text
        AND value->>'thread_id' = thread_id::text
        AND value->>'client_message_id' = CASE purpose
            WHEN 'code-review-reviewer-dispatch' THEN 'cr-dispatch:v1:' || owner_id::text
            WHEN 'review-loop-initial-send' THEN 'rl-startup:v1:' || owner_id::text END) IS TRUE
$$;

-- This finite state shape is shared by the two caller operations, not a general workflow engine.
CREATE FUNCTION caller143_send_state_valid(state text, attempt_id uuid, attempt_seq integer,
    attempted_at timestamptz, due_at timestamptz, observe_deadline timestamptz,
    attention_at timestamptz, observed_at timestamptz, message_id bigint,
    resolution text, detail text, writer_seq integer, updated_at timestamptz) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (num_nulls(state, attempt_seq, writer_seq, updated_at) = 0
        AND attempt_seq >= 0 AND writer_seq >= 0
        AND (attempt_seq = 0) = (attempt_id IS NULL)
        AND (attempt_seq = 0) = (attempted_at IS NULL)
        AND (attempt_id IS NULL OR attempt_id <> '00000000-0000-0000-0000-000000000000'::uuid)
        AND (state IN ('pending','attempting','unknown','attention')) = (due_at IS NOT NULL)
        AND (state = 'accepted') = (message_id IS NOT NULL)
        AND (message_id IS NULL OR message_id > 0)
        AND (state IN ('accepted','refused','cancelled')) = (resolution IS NOT NULL)
        AND CASE state
            WHEN 'pending' THEN observe_deadline IS NULL AND attention_at IS NULL AND observed_at IS NULL
                AND CASE WHEN attempt_seq = 0 THEN detail IS NULL
                    ELSE detail IN ('contention','normalizing','mode_changed','replay_observation_needed') END
            WHEN 'attempting' THEN attempt_seq > 0 AND detail IS NULL
                AND observe_deadline IS NULL AND attention_at IS NULL AND observed_at IS NULL
            WHEN 'unknown' THEN attempt_seq > 0 AND detail IN ('unknown_ack','stale_attempt')
                AND observe_deadline IS NOT NULL AND attention_at IS NULL
            WHEN 'attention' THEN attempt_seq > 0 AND detail IN ('unknown_ack','stale_attempt','pass_pointer_conflict')
                AND observe_deadline IS NOT NULL AND attention_at IS NOT NULL
            WHEN 'accepted' THEN attempt_seq > 0 AND resolution IN ('send_ack','observed_auto','observed_manual')
                AND (resolution <> 'send_ack') = (observed_at IS NOT NULL)
            WHEN 'refused' THEN attempt_seq > 0 AND detail IS NOT NULL AND resolution = 'send_refusal'
            WHEN 'cancelled' THEN resolution IN ('owner_cancelled','owner_ineligible','deadline_before_attempt')
            ELSE false END) IS TRUE
$$;

ALTER TABLE code_review_agent_results
    ADD COLUMN dispatch_version smallint,
    ADD COLUMN dispatch_key text,
    ADD COLUMN dispatch_input jsonb,
    ADD COLUMN dispatch_digest text,
    ADD COLUMN dispatch_thread_id uuid,
    ADD COLUMN dispatch_state text,
    ADD COLUMN dispatch_attempt_id uuid,
    ADD COLUMN dispatch_attempt_seq integer,
    ADD COLUMN dispatch_attempted_at timestamptz,
    ADD COLUMN dispatch_due_at timestamptz,
    ADD COLUMN dispatch_observe_deadline timestamptz,
    ADD COLUMN dispatch_attention_at timestamptz,
    ADD COLUMN dispatch_observed_at timestamptz,
    ADD COLUMN dispatch_message_id bigint,
    ADD COLUMN dispatch_resolution text,
    ADD COLUMN dispatch_detail text,
    ADD COLUMN dispatch_writer_seq integer,
    ADD COLUMN dispatch_updated_at timestamptz,
    ADD COLUMN dispatch_wake jsonb;

ALTER TABLE code_review_agent_results ADD CONSTRAINT chk_cra_dispatch_send CHECK ((
    CASE WHEN dispatch_version IS NULL THEN num_nonnulls(dispatch_version,dispatch_key,dispatch_input,
        dispatch_digest,dispatch_thread_id,dispatch_state,dispatch_attempt_id,dispatch_attempt_seq,
        dispatch_attempted_at,dispatch_due_at,dispatch_observe_deadline,dispatch_attention_at,
        dispatch_observed_at,dispatch_message_id,dispatch_resolution,dispatch_detail,
        dispatch_writer_seq,dispatch_updated_at,dispatch_wake) = 0
    ELSE dispatch_version = 1 AND role = 'reviewer'
        AND num_nulls(dispatch_key,dispatch_input,dispatch_digest,dispatch_thread_id,dispatch_wake) = 0
        AND id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND dispatch_thread_id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND dispatch_key = 'cr-dispatch:v1:' || id::text
        AND caller143_send_digest_valid(dispatch_digest)
        AND dispatch_detail IS DISTINCT FROM 'pass_pointer_conflict'
        AND caller143_send_state_valid(dispatch_state,dispatch_attempt_id,dispatch_attempt_seq,
            dispatch_attempted_at,dispatch_due_at,dispatch_observe_deadline,dispatch_attention_at,
            dispatch_observed_at,dispatch_message_id,dispatch_resolution,dispatch_detail,
            dispatch_writer_seq,dispatch_updated_at) END
) IS TRUE) NOT VALID;
ALTER TABLE code_review_agent_results ADD CONSTRAINT fk_cra_dispatch_thread
    FOREIGN KEY (org_id,session_id,dispatch_thread_id)
    REFERENCES session_threads(org_id,session_id,id) NOT VALID;

ALTER TABLE session_review_loops
    ADD COLUMN startup_send_version smallint,
    ADD COLUMN startup_send_key text,
    ADD COLUMN startup_send_input jsonb,
    ADD COLUMN startup_send_digest text,
    ADD COLUMN startup_send_thread_id uuid,
    ADD COLUMN startup_send_state text,
    ADD COLUMN startup_send_attempt_id uuid,
    ADD COLUMN startup_send_attempt_seq integer,
    ADD COLUMN startup_send_attempted_at timestamptz,
    ADD COLUMN startup_send_due_at timestamptz,
    ADD COLUMN startup_send_observe_deadline timestamptz,
    ADD COLUMN startup_send_attention_at timestamptz,
    ADD COLUMN startup_send_observed_at timestamptz,
    ADD COLUMN startup_send_message_id bigint,
    ADD COLUMN startup_send_resolution text,
    ADD COLUMN startup_send_detail text,
    ADD COLUMN startup_send_writer_seq integer,
    ADD COLUMN startup_send_updated_at timestamptz,
    ADD COLUMN startup_send_pass_id uuid;

ALTER TABLE session_review_loops ADD CONSTRAINT chk_srl_startup_send CHECK ((
    CASE WHEN startup_send_version IS NULL THEN num_nonnulls(startup_send_version,startup_send_key,startup_send_input,
        startup_send_digest,startup_send_thread_id,startup_send_state,startup_send_attempt_id,startup_send_attempt_seq,
        startup_send_attempted_at,startup_send_due_at,startup_send_observe_deadline,startup_send_attention_at,
        startup_send_observed_at,startup_send_message_id,startup_send_resolution,startup_send_detail,
        startup_send_writer_seq,startup_send_updated_at,startup_send_pass_id) = 0
    ELSE startup_send_version = 1
        AND num_nulls(startup_send_key,startup_send_input,startup_send_digest,startup_send_thread_id,startup_send_pass_id) = 0
        AND id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND startup_send_thread_id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND startup_send_pass_id <> '00000000-0000-0000-0000-000000000000'::uuid
        AND thread_id = startup_send_thread_id AND startup_send_key = 'rl-startup:v1:' || id::text
        AND caller143_send_digest_valid(startup_send_digest)
        AND caller143_send_state_valid(startup_send_state,startup_send_attempt_id,startup_send_attempt_seq,
            startup_send_attempted_at,startup_send_due_at,startup_send_observe_deadline,startup_send_attention_at,
            startup_send_observed_at,startup_send_message_id,startup_send_resolution,startup_send_detail,
            startup_send_writer_seq,startup_send_updated_at) END
) IS TRUE) NOT VALID;
ALTER TABLE session_review_loops ADD CONSTRAINT fk_srl_startup_thread
    FOREIGN KEY (org_id,session_id,startup_send_thread_id)
    REFERENCES session_threads(org_id,session_id,id) NOT VALID;

CREATE TABLE review_loop_start_intents (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL,
    caller_kind text NOT NULL,
    automation_run_id uuid,
    publication_changeset_id uuid,
    envelope jsonb NOT NULL,
    envelope_digest text NOT NULL,
    pass_id uuid NOT NULL,
    thread_id uuid NOT NULL,
    startup_input jsonb NOT NULL,
    startup_digest text NOT NULL,
    state text NOT NULL,
    due_at timestamptz,
    cancel_requested_at timestamptz,
    detail text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT uq_review_loop_start_org_id UNIQUE (org_id,id),
    CONSTRAINT uq_review_loop_start_org_session_id UNIQUE (org_id,session_id,id),
    CONSTRAINT fk_review_loop_start_session FOREIGN KEY (org_id,session_id)
        REFERENCES sessions(org_id,id) ON DELETE CASCADE,
    CONSTRAINT fk_review_loop_start_automation FOREIGN KEY (automation_run_id,org_id)
        REFERENCES automation_runs(id,org_id),
    CONSTRAINT fk_review_loop_start_changeset FOREIGN KEY (publication_changeset_id,org_id,session_id)
        REFERENCES session_changesets(id,org_id,session_id)
);
ALTER TABLE review_loop_start_intents ADD CONSTRAINT chk_review_loop_start_intent CHECK ((
    id <> '00000000-0000-0000-0000-000000000000'::uuid
    AND thread_id <> '00000000-0000-0000-0000-000000000000'::uuid
    AND pass_id <> '00000000-0000-0000-0000-000000000000'::uuid
    AND caller_kind IN ('manual','automation','publication')
    AND CASE caller_kind WHEN 'manual' THEN automation_run_id IS NULL AND publication_changeset_id IS NULL
        WHEN 'automation' THEN automation_run_id IS NOT NULL
            AND automation_run_id <> '00000000-0000-0000-0000-000000000000'::uuid AND publication_changeset_id IS NULL
        WHEN 'publication' THEN automation_run_id IS NULL AND publication_changeset_id IS NOT NULL
            AND publication_changeset_id <> '00000000-0000-0000-0000-000000000000'::uuid ELSE false END
    AND caller143_send_digest_valid(envelope_digest) AND caller143_send_digest_valid(startup_digest)
    AND state IN ('prepared','created','refused') AND (state = 'prepared') = (due_at IS NOT NULL)
    AND (state <> 'refused' OR detail IS NOT NULL)
) IS TRUE) NOT VALID;
CREATE INDEX idx_review_loop_start_due ON review_loop_start_intents(due_at,id) WHERE due_at IS NOT NULL;

CREATE TABLE caller_send_command_stamps (
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL,
    thread_id uuid NOT NULL,
    client_message_id text NOT NULL,
    purpose text NOT NULL,
    owner_id uuid NOT NULL,
    version smallint NOT NULL,
    digest text NOT NULL,
    message_id bigint NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT uq_caller_send_stamp_thread_key UNIQUE (org_id,thread_id,client_message_id),
    CONSTRAINT uq_caller_send_stamp_owner UNIQUE (org_id,purpose,owner_id),
    CONSTRAINT fk_caller_send_stamp_session FOREIGN KEY (org_id,session_id) REFERENCES sessions(org_id,id) ON DELETE CASCADE,
    CONSTRAINT fk_caller_send_stamp_thread FOREIGN KEY (org_id,session_id,thread_id)
        REFERENCES session_threads(org_id,session_id,id) ON DELETE CASCADE
);
ALTER TABLE caller_send_command_stamps ADD CONSTRAINT chk_caller_send_stamp CHECK ((
    version = 1 AND owner_id <> '00000000-0000-0000-0000-000000000000'::uuid
    AND purpose IN ('code-review-reviewer-dispatch','review-loop-initial-send')
    AND client_message_id = CASE purpose
        WHEN 'code-review-reviewer-dispatch' THEN 'cr-dispatch:v1:' || owner_id::text
        WHEN 'review-loop-initial-send' THEN 'rl-startup:v1:' || owner_id::text END
    AND caller143_send_digest_valid(digest) AND message_id > 0
) IS TRUE) NOT VALID;

ALTER TABLE automation_runs ADD COLUMN review_start_id uuid;
ALTER TABLE session_publications ADD COLUMN review_start_id uuid;
ALTER TABLE automation_runs ADD CONSTRAINT fk_automation_review_start FOREIGN KEY (org_id,review_start_id)
    REFERENCES review_loop_start_intents(org_id,id) NOT VALID;
ALTER TABLE session_publications ADD CONSTRAINT fk_publication_review_start FOREIGN KEY (org_id,session_id,review_start_id)
    REFERENCES review_loop_start_intents(org_id,session_id,id) NOT VALID;

-- Deliberately small state-only JSON values; retained inputs/wakes are never copied here.
CREATE FUNCTION caller143_send_transition_valid(old_value jsonb, new_value jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    old_state text := old_value->>'state';
    new_state text := new_value->>'state';
BEGIN
    IF (new_value->>'writer_seq')::integer NOT IN ((old_value->>'writer_seq')::integer,
        (old_value->>'writer_seq')::integer + 1) THEN RETURN false; END IF;
    IF old_value - ARRAY['writer_seq','updated_at'] = new_value - ARRAY['writer_seq','updated_at'] THEN
        RETURN true;
    END IF;
    IF (new_value->>'writer_seq')::integer <> (old_value->>'writer_seq')::integer + 1 THEN RETURN false; END IF;
    IF old_state = 'pending' AND new_state = 'attempting' THEN
        RETURN (new_value->>'attempt_seq')::integer = (old_value->>'attempt_seq')::integer + 1
            AND new_value->'attempt_id' IS DISTINCT FROM old_value->'attempt_id'
            AND (new_value->>'attempted_at')::timestamptz >= COALESCE((old_value->>'attempted_at')::timestamptz,'-infinity'::timestamptz);
    END IF;
    IF new_value->'attempt_id' IS DISTINCT FROM old_value->'attempt_id'
        OR new_value->'attempt_seq' IS DISTINCT FROM old_value->'attempt_seq'
        OR new_value->'attempted_at' IS DISTINCT FROM old_value->'attempted_at' THEN RETURN false; END IF;
    IF old_state IN ('unknown','attention') THEN
        IF new_value->'observe_deadline' IS DISTINCT FROM old_value->'observe_deadline' THEN RETURN false; END IF;
        IF old_value->>'observed_at' IS NOT NULL AND (
            new_value->>'observed_at' IS NOT NULL
            AND (new_value->>'observed_at')::timestamptz >= (old_value->>'observed_at')::timestamptz
        ) IS NOT TRUE THEN RETURN false; END IF;
        IF old_state = 'attention' AND new_value->'attention_at' IS DISTINCT FROM old_value->'attention_at' THEN RETURN false; END IF;
        IF new_state = 'attention' THEN
            IF ((new_value->>'due_at')::timestamptz >= (old_value->>'due_at')::timestamptz) IS NOT TRUE THEN RETURN false; END IF;
            IF new_value->'detail' IS DISTINCT FROM old_value->'detail' AND (
                new_value->>'detail' = 'pass_pointer_conflict'
                AND old_value->>'detail' IS DISTINCT FROM 'pass_pointer_conflict'
            ) IS NOT TRUE THEN RETURN false; END IF;
        END IF;
    END IF;
    IF old_state = 'attempting' AND (new_state IN ('pending','refused','unknown')
        OR new_state = 'accepted' AND new_value->>'resolution' = 'send_ack') THEN RETURN true; END IF;
    IF old_state IN ('unknown','attention') AND new_state = 'accepted'
        AND new_value->>'resolution' IN ('observed_auto','observed_manual') THEN RETURN true; END IF;
    IF old_state = 'unknown' AND new_state = 'attention' THEN RETURN true; END IF;
    IF old_state IN ('attempting','unknown','attention') AND new_state = 'attention'
        AND new_value->>'detail' = 'pass_pointer_conflict'
        AND (old_state <> 'attention' OR old_value->>'detail' IS DISTINCT FROM 'pass_pointer_conflict') THEN RETURN true; END IF;
    IF old_state = 'pending' AND new_state = 'cancelled' THEN RETURN true; END IF;
    IF old_state IN ('unknown','attention') AND new_state = old_state THEN
        RETURN old_value - ARRAY['due_at','observed_at','writer_seq','updated_at']
            = new_value - ARRAY['due_at','observed_at','writer_seq','updated_at']
            AND (new_value->>'due_at')::timestamptz >= (old_value->>'due_at')::timestamptz
            AND (old_value->>'observed_at' IS NULL OR new_value->>'observed_at' IS NOT NULL
                AND (new_value->>'observed_at')::timestamptz >= (old_value->>'observed_at')::timestamptz);
    END IF;
    RETURN false;
END;
$$;

CREATE FUNCTION caller143_start_unresolved(scope_org uuid, start_id uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT 1 FROM review_loop_start_intents s WHERE s.org_id = scope_org AND s.id = start_id
        AND (s.state = 'prepared' OR s.state = 'created' AND EXISTS (
            SELECT 1 FROM session_review_loops l WHERE l.org_id = s.org_id AND l.session_id = s.session_id
                AND l.id = s.id AND l.startup_send_due_at IS NOT NULL)))
$$;

CREATE FUNCTION caller143_guard_dispatch_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_state jsonb; new_state jsonb;
BEGIN
    IF TG_OP = 'TRUNCATE' THEN
        IF EXISTS (SELECT 1 FROM code_review_agent_results WHERE dispatch_due_at IS NOT NULL) THEN
            RAISE EXCEPTION 'unresolved_caller_send' USING ERRCODE = '55000'; END IF;
        RETURN NULL;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF OLD.dispatch_due_at IS NOT NULL THEN RAISE EXCEPTION 'unresolved_caller_send' USING ERRCODE = '55000'; END IF;
        RETURN OLD;
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.dispatch_version IS NOT NULL THEN
            PERFORM caller143_require_send_intents_enabled();
            IF NOT caller143_send_input_valid(NEW.dispatch_input,'code-review-reviewer-dispatch',NEW.id,
                NEW.org_id,NEW.session_id,NEW.dispatch_thread_id)
                OR jsonb_typeof(NEW.dispatch_wake) IS DISTINCT FROM 'object'
                OR octet_length(NEW.dispatch_wake::text) > 8388608 THEN
                RAISE EXCEPTION 'invalid_caller_send_input' USING ERRCODE = '23514'; END IF;
            IF NEW.dispatch_state IS DISTINCT FROM 'pending' OR NEW.dispatch_attempt_seq IS DISTINCT FROM 0
                OR NEW.dispatch_writer_seq IS DISTINCT FROM 0 OR NEW.status IS DISTINCT FROM 'queued' THEN
                RAISE EXCEPTION 'caller_send_requires_fresh_pending' USING ERRCODE = '55000'; END IF;
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.dispatch_version IS NULL THEN
        IF num_nonnulls(NEW.dispatch_version,NEW.dispatch_key,NEW.dispatch_input,NEW.dispatch_digest,
            NEW.dispatch_thread_id,NEW.dispatch_state,NEW.dispatch_attempt_id,NEW.dispatch_attempt_seq,
            NEW.dispatch_attempted_at,NEW.dispatch_due_at,NEW.dispatch_observe_deadline,NEW.dispatch_attention_at,
            NEW.dispatch_observed_at,NEW.dispatch_message_id,NEW.dispatch_resolution,NEW.dispatch_detail,
            NEW.dispatch_writer_seq,NEW.dispatch_updated_at,NEW.dispatch_wake) <> 0 THEN
            RAISE EXCEPTION 'legacy_caller_send_upgrade_forbidden' USING ERRCODE = '55000'; END IF;
        RETURN NEW;
    END IF;
    IF ROW(NEW.id,NEW.org_id,NEW.session_id,NEW.role,NEW.agent_provider,NEW.agent_model,
        NEW.dispatch_version,NEW.dispatch_key,NEW.dispatch_digest,NEW.dispatch_thread_id)
        IS DISTINCT FROM ROW(OLD.id,OLD.org_id,OLD.session_id,OLD.role,OLD.agent_provider,OLD.agent_model,
        OLD.dispatch_version,OLD.dispatch_key,OLD.dispatch_digest,OLD.dispatch_thread_id) THEN
        RAISE EXCEPTION 'immutable_caller_send_identity' USING ERRCODE = '55000'; END IF;
    IF NEW.dispatch_detail = 'pass_pointer_conflict' THEN
        RAISE EXCEPTION 'caller_send_pass_conflict_requires_loop' USING ERRCODE = '55000'; END IF;
    old_state := jsonb_build_object('state',OLD.dispatch_state,'attempt_id',OLD.dispatch_attempt_id,
        'attempt_seq',OLD.dispatch_attempt_seq,'attempted_at',OLD.dispatch_attempted_at,'due_at',OLD.dispatch_due_at,
        'observe_deadline',OLD.dispatch_observe_deadline,'attention_at',OLD.dispatch_attention_at,
        'observed_at',OLD.dispatch_observed_at,'message_id',OLD.dispatch_message_id,'resolution',OLD.dispatch_resolution,
        'detail',OLD.dispatch_detail,'writer_seq',OLD.dispatch_writer_seq,'updated_at',OLD.dispatch_updated_at);
    new_state := jsonb_build_object('state',NEW.dispatch_state,'attempt_id',NEW.dispatch_attempt_id,
        'attempt_seq',NEW.dispatch_attempt_seq,'attempted_at',NEW.dispatch_attempted_at,'due_at',NEW.dispatch_due_at,
        'observe_deadline',NEW.dispatch_observe_deadline,'attention_at',NEW.dispatch_attention_at,
        'observed_at',NEW.dispatch_observed_at,'message_id',NEW.dispatch_message_id,'resolution',NEW.dispatch_resolution,
        'detail',NEW.dispatch_detail,'writer_seq',NEW.dispatch_writer_seq,'updated_at',NEW.dispatch_updated_at);
    IF caller143_send_transition_valid(old_state,new_state) IS NOT TRUE OR OLD.dispatch_due_at IS NOT NULL
        AND ROW(NEW.status,NEW.raw_output,NEW.structured_result) IS DISTINCT FROM ROW(OLD.status,OLD.raw_output,OLD.structured_result)
        AND NEW.dispatch_writer_seq IS DISTINCT FROM OLD.dispatch_writer_seq + 1 THEN
        RAISE EXCEPTION 'invalid_caller_send_transition' USING ERRCODE = '55000'; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_caller_dispatch_owner BEFORE INSERT OR UPDATE OR DELETE ON code_review_agent_results
    FOR EACH ROW EXECUTE FUNCTION caller143_guard_dispatch_owner();
CREATE TRIGGER trg_caller_dispatch_truncate BEFORE TRUNCATE ON code_review_agent_results
    FOR EACH STATEMENT EXECUTE FUNCTION caller143_guard_dispatch_owner();

CREATE FUNCTION caller143_guard_startup_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_state jsonb; new_state jsonb;
BEGIN
    IF TG_OP = 'TRUNCATE' THEN
        IF EXISTS (SELECT 1 FROM session_review_loops WHERE startup_send_due_at IS NOT NULL) THEN
            RAISE EXCEPTION 'unresolved_caller_send' USING ERRCODE = '55000'; END IF;
        RETURN NULL;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF OLD.startup_send_due_at IS NOT NULL THEN RAISE EXCEPTION 'unresolved_caller_send' USING ERRCODE = '55000'; END IF;
        RETURN OLD;
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.startup_send_version IS NOT NULL THEN
            PERFORM caller143_require_send_intents_enabled();
            IF NOT caller143_send_input_valid(NEW.startup_send_input,'review-loop-initial-send',NEW.id,
                NEW.org_id,NEW.session_id,NEW.startup_send_thread_id) THEN
                RAISE EXCEPTION 'invalid_caller_send_input' USING ERRCODE = '23514'; END IF;
            IF NEW.startup_send_state IS DISTINCT FROM 'pending' OR NEW.startup_send_attempt_seq IS DISTINCT FROM 0
                OR NEW.startup_send_writer_seq IS DISTINCT FROM 0 OR NEW.status IS DISTINCT FROM 'running'
                OR NOT EXISTS (SELECT 1 FROM review_loop_start_intents s WHERE s.org_id = NEW.org_id
                    AND s.session_id = NEW.session_id AND s.id = NEW.id AND s.state = 'created'
                    AND s.thread_id = NEW.startup_send_thread_id AND s.pass_id = NEW.startup_send_pass_id
                    AND s.startup_input = NEW.startup_send_input AND s.startup_digest = NEW.startup_send_digest) THEN
                RAISE EXCEPTION 'caller_send_requires_retained_start' USING ERRCODE = '55000'; END IF;
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.startup_send_version IS NULL THEN
        IF num_nonnulls(NEW.startup_send_version,NEW.startup_send_key,NEW.startup_send_input,NEW.startup_send_digest,
            NEW.startup_send_thread_id,NEW.startup_send_state,NEW.startup_send_attempt_id,NEW.startup_send_attempt_seq,
            NEW.startup_send_attempted_at,NEW.startup_send_due_at,NEW.startup_send_observe_deadline,NEW.startup_send_attention_at,
            NEW.startup_send_observed_at,NEW.startup_send_message_id,NEW.startup_send_resolution,NEW.startup_send_detail,
            NEW.startup_send_writer_seq,NEW.startup_send_updated_at,NEW.startup_send_pass_id) <> 0 THEN
            RAISE EXCEPTION 'legacy_caller_send_upgrade_forbidden' USING ERRCODE = '55000'; END IF;
        RETURN NEW;
    END IF;
    IF ROW(NEW.id,NEW.org_id,NEW.session_id,NEW.thread_id,NEW.source,NEW.agent_type,NEW.max_passes,
        NEW.fix_mode,NEW.started_by_user_id,NEW.automation_run_id,NEW.changeset_id,
        NEW.loop_start_checkpoint_key,NEW.startup_send_version,NEW.startup_send_key,
        NEW.startup_send_digest,NEW.startup_send_thread_id,NEW.startup_send_pass_id)
        IS DISTINCT FROM ROW(OLD.id,OLD.org_id,OLD.session_id,OLD.thread_id,OLD.source,OLD.agent_type,OLD.max_passes,
        OLD.fix_mode,OLD.started_by_user_id,OLD.automation_run_id,OLD.changeset_id,
        OLD.loop_start_checkpoint_key,OLD.startup_send_version,OLD.startup_send_key,
        OLD.startup_send_digest,OLD.startup_send_thread_id,OLD.startup_send_pass_id) THEN
        RAISE EXCEPTION 'immutable_caller_send_identity' USING ERRCODE = '55000'; END IF;
    -- Existing explicit review bypass cancels the current loop and clears its
    -- operational requirement. The retained original envelope never changes.
    IF NEW.review_required IS DISTINCT FROM OLD.review_required AND NOT (
        OLD.status = 'running' AND NEW.status = 'cancelled'
        AND OLD.review_required AND NOT NEW.review_required) THEN
        RAISE EXCEPTION 'immutable_caller_send_identity' USING ERRCODE = '55000'; END IF;
    old_state := jsonb_build_object('state',OLD.startup_send_state,'attempt_id',OLD.startup_send_attempt_id,
        'attempt_seq',OLD.startup_send_attempt_seq,'attempted_at',OLD.startup_send_attempted_at,'due_at',OLD.startup_send_due_at,
        'observe_deadline',OLD.startup_send_observe_deadline,'attention_at',OLD.startup_send_attention_at,
        'observed_at',OLD.startup_send_observed_at,'message_id',OLD.startup_send_message_id,'resolution',OLD.startup_send_resolution,
        'detail',OLD.startup_send_detail,'writer_seq',OLD.startup_send_writer_seq,'updated_at',OLD.startup_send_updated_at);
    new_state := jsonb_build_object('state',NEW.startup_send_state,'attempt_id',NEW.startup_send_attempt_id,
        'attempt_seq',NEW.startup_send_attempt_seq,'attempted_at',NEW.startup_send_attempted_at,'due_at',NEW.startup_send_due_at,
        'observe_deadline',NEW.startup_send_observe_deadline,'attention_at',NEW.startup_send_attention_at,
        'observed_at',NEW.startup_send_observed_at,'message_id',NEW.startup_send_message_id,'resolution',NEW.startup_send_resolution,
        'detail',NEW.startup_send_detail,'writer_seq',NEW.startup_send_writer_seq,'updated_at',NEW.startup_send_updated_at);
    IF caller143_send_transition_valid(old_state,new_state) IS NOT TRUE OR OLD.startup_send_due_at IS NOT NULL
        AND NEW.status IS DISTINCT FROM OLD.status AND NEW.status IS DISTINCT FROM 'cancelled'
        AND NEW.startup_send_writer_seq IS DISTINCT FROM OLD.startup_send_writer_seq + 1 THEN
        RAISE EXCEPTION 'invalid_caller_send_transition' USING ERRCODE = '55000'; END IF;
    IF NEW.startup_send_state = 'attention' AND NEW.startup_send_detail = 'pass_pointer_conflict'
        AND old_state - ARRAY['writer_seq','updated_at'] IS DISTINCT FROM new_state - ARRAY['writer_seq','updated_at']
        AND NOT EXISTS (SELECT 1 FROM session_review_loop_passes p
            WHERE p.org_id = NEW.org_id AND p.session_id = NEW.session_id AND p.loop_id = NEW.id
                AND p.id = NEW.startup_send_pass_id AND p.review_message_id IS NOT NULL) THEN
        RAISE EXCEPTION 'caller_send_pass_conflict_requires_pointer' USING ERRCODE = '55000'; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_caller_startup_owner BEFORE INSERT OR UPDATE OR DELETE ON session_review_loops
    FOR EACH ROW EXECUTE FUNCTION caller143_guard_startup_owner();
CREATE TRIGGER trg_caller_startup_truncate BEFORE TRUNCATE ON session_review_loops
    FOR EACH STATEMENT EXECUTE FUNCTION caller143_guard_startup_owner();

CREATE FUNCTION caller143_guard_review_start() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'TRUNCATE' THEN
        IF EXISTS (SELECT 1 FROM review_loop_start_intents s WHERE caller143_start_unresolved(s.org_id,s.id)) THEN
            RAISE EXCEPTION 'unresolved_review_start' USING ERRCODE = '55000'; END IF;
        RETURN NULL;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF caller143_start_unresolved(OLD.org_id,OLD.id) THEN
            RAISE EXCEPTION 'unresolved_review_start' USING ERRCODE = '55000'; END IF;
        RETURN OLD;
    END IF;
    IF TG_OP = 'INSERT' THEN
        PERFORM caller143_require_send_intents_enabled();
        IF jsonb_typeof(NEW.envelope) IS DISTINCT FROM 'object' OR octet_length(NEW.envelope::text) > 8388608
            OR NOT caller143_send_input_valid(NEW.startup_input,'review-loop-initial-send',NEW.id,
                NEW.org_id,NEW.session_id,NEW.thread_id) THEN
            RAISE EXCEPTION 'invalid_review_start_input' USING ERRCODE = '23514'; END IF;
        IF NEW.state IS DISTINCT FROM 'prepared' THEN
            RAISE EXCEPTION 'review_start_requires_prepared' USING ERRCODE = '55000'; END IF;
        RETURN NEW;
    END IF;
    IF ROW(NEW.id,NEW.org_id,NEW.session_id,NEW.caller_kind,NEW.automation_run_id,NEW.publication_changeset_id,
        NEW.envelope_digest,NEW.pass_id,NEW.thread_id,NEW.startup_digest,NEW.created_at)
        IS DISTINCT FROM ROW(OLD.id,OLD.org_id,OLD.session_id,OLD.caller_kind,OLD.automation_run_id,OLD.publication_changeset_id,
        OLD.envelope_digest,OLD.pass_id,OLD.thread_id,OLD.startup_digest,OLD.created_at) THEN
        RAISE EXCEPTION 'immutable_review_start_identity' USING ERRCODE = '55000'; END IF;
    IF OLD.state <> 'prepared' AND ROW(NEW.state,NEW.due_at,NEW.cancel_requested_at,NEW.detail)
        IS DISTINCT FROM ROW(OLD.state,OLD.due_at,OLD.cancel_requested_at,OLD.detail)
        OR OLD.state = 'prepared' AND NEW.state NOT IN ('prepared','created','refused')
        OR OLD.cancel_requested_at IS NOT NULL AND NEW.cancel_requested_at IS DISTINCT FROM OLD.cancel_requested_at
        OR OLD.cancel_requested_at IS NOT NULL AND NEW.state = 'created' THEN
        RAISE EXCEPTION 'invalid_review_start_transition' USING ERRCODE = '55000'; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_caller_review_start BEFORE INSERT OR UPDATE OR DELETE ON review_loop_start_intents
    FOR EACH ROW EXECUTE FUNCTION caller143_guard_review_start();
CREATE TRIGGER trg_caller_review_start_truncate BEFORE TRUNCATE ON review_loop_start_intents
    FOR EACH STATEMENT EXECUTE FUNCTION caller143_guard_review_start();

CREATE FUNCTION caller143_check_created_start() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- Only the creation edge needs the large retained-payload proof. Later
    -- due/cancellation/timestamp updates never detoast those immutable values.
    IF TG_OP = 'UPDATE' AND OLD.state = 'prepared' AND NEW.state = 'created' AND NOT EXISTS (
        SELECT 1 FROM session_review_loops l JOIN session_review_loop_passes p
            ON p.org_id = l.org_id AND p.session_id = l.session_id AND p.loop_id = l.id AND p.id = l.startup_send_pass_id
        WHERE l.org_id = NEW.org_id AND l.session_id = NEW.session_id AND l.id = NEW.id
            AND l.thread_id = NEW.thread_id AND l.startup_send_thread_id = NEW.thread_id
            AND l.startup_send_pass_id = NEW.pass_id AND l.startup_send_input = NEW.startup_input
            AND l.startup_send_digest = NEW.startup_digest) THEN
        RAISE EXCEPTION 'review_start_creation_binding_missing' USING ERRCODE = '23503';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER trg_caller_created_start AFTER INSERT OR UPDATE ON review_loop_start_intents
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION caller143_check_created_start();

-- Only explicit payload UPDATEs may inspect/detoast the frozen JSONB values.
-- Ordinary outcome, observation, cancellation and evidence updates never read them.
CREATE FUNCTION caller143_guard_send_payload_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'code_review_agent_results' THEN
        IF ROW(NEW.dispatch_input,NEW.dispatch_wake) IS DISTINCT FROM ROW(OLD.dispatch_input,OLD.dispatch_wake) THEN
            RAISE EXCEPTION 'immutable_caller_send_payload' USING ERRCODE = '55000'; END IF;
    ELSIF TG_TABLE_NAME = 'session_review_loops' THEN
        IF NEW.startup_send_input IS DISTINCT FROM OLD.startup_send_input THEN
            RAISE EXCEPTION 'immutable_caller_send_payload' USING ERRCODE = '55000'; END IF;
    ELSE
        IF ROW(NEW.envelope,NEW.startup_input) IS DISTINCT FROM ROW(OLD.envelope,OLD.startup_input) THEN
            RAISE EXCEPTION 'immutable_review_start_payload' USING ERRCODE = '55000'; END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_caller_dispatch_payload BEFORE UPDATE OF dispatch_input,dispatch_wake ON code_review_agent_results
    FOR EACH ROW EXECUTE FUNCTION caller143_guard_send_payload_update();
CREATE TRIGGER trg_caller_startup_payload BEFORE UPDATE OF startup_send_input ON session_review_loops
    FOR EACH ROW EXECUTE FUNCTION caller143_guard_send_payload_update();
CREATE TRIGGER trg_caller_review_start_payload BEFORE UPDATE OF envelope,startup_input ON review_loop_start_intents
    FOR EACH ROW EXECUTE FUNCTION caller143_guard_send_payload_update();

CREATE FUNCTION caller143_guard_review_caller() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_id uuid; old_org uuid;
BEGIN
    IF TG_OP = 'TRUNCATE' THEN
        IF TG_TABLE_NAME = 'automation_runs' THEN
            IF EXISTS (SELECT 1 FROM automation_runs a WHERE caller143_start_unresolved(a.org_id,a.review_start_id)) THEN
                RAISE EXCEPTION 'unresolved_review_start_caller' USING ERRCODE = '55000'; END IF;
        ELSE
            IF EXISTS (SELECT 1 FROM session_publications p WHERE caller143_start_unresolved(p.org_id,p.review_start_id)) THEN
                RAISE EXCEPTION 'unresolved_review_start_caller' USING ERRCODE = '55000'; END IF;
        END IF;
        RETURN NULL;
    END IF;
    IF TG_OP IN ('UPDATE','DELETE') THEN
        old_id := OLD.review_start_id; old_org := OLD.org_id;
        IF caller143_start_unresolved(old_org,old_id) THEN
            IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'unresolved_review_start_caller' USING ERRCODE = '55000'; END IF;
            IF ROW(NEW.id,NEW.org_id,NEW.review_start_id) IS DISTINCT FROM ROW(OLD.id,OLD.org_id,OLD.review_start_id) THEN
                RAISE EXCEPTION 'unresolved_review_start_caller' USING ERRCODE = '55000'; END IF;
            IF TG_TABLE_NAME = 'session_publications' THEN
                IF ROW(NEW.session_id,NEW.changeset_id) IS DISTINCT FROM ROW(OLD.session_id,OLD.changeset_id) THEN
                    RAISE EXCEPTION 'unresolved_review_start_caller' USING ERRCODE = '55000'; END IF;
            END IF;
        END IF;
        IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    END IF;
    IF NEW.review_start_id IS NOT NULL THEN
        IF TG_TABLE_NAME = 'automation_runs' THEN
            IF NOT EXISTS (SELECT 1 FROM review_loop_start_intents s WHERE s.org_id = NEW.org_id AND s.id = NEW.review_start_id
                AND s.caller_kind = 'automation' AND s.automation_run_id = NEW.id) THEN
                RAISE EXCEPTION 'review_start_caller_scope_mismatch' USING ERRCODE = '23503'; END IF;
        ELSE
            IF NOT EXISTS (SELECT 1 FROM review_loop_start_intents s WHERE s.org_id = NEW.org_id AND s.session_id = NEW.session_id
                AND s.id = NEW.review_start_id AND s.caller_kind = 'publication' AND s.publication_changeset_id = NEW.changeset_id) THEN
                RAISE EXCEPTION 'review_start_caller_scope_mismatch' USING ERRCODE = '23503'; END IF;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_caller_automation_start BEFORE INSERT OR UPDATE OR DELETE ON automation_runs
    FOR EACH ROW EXECUTE FUNCTION caller143_guard_review_caller();
CREATE TRIGGER trg_caller_publication_start BEFORE INSERT OR UPDATE OR DELETE ON session_publications
    FOR EACH ROW EXECUTE FUNCTION caller143_guard_review_caller();
CREATE TRIGGER trg_caller_automation_truncate BEFORE TRUNCATE ON automation_runs
    FOR EACH STATEMENT EXECUTE FUNCTION caller143_guard_review_caller();
CREATE TRIGGER trg_caller_publication_truncate BEFORE TRUNCATE ON session_publications
    FOR EACH STATEMENT EXECUTE FUNCTION caller143_guard_review_caller();

CREATE FUNCTION caller143_stamp_owner_unresolved(scope_org uuid, owner_purpose text, owner uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT CASE owner_purpose
        WHEN 'code-review-reviewer-dispatch' THEN EXISTS (SELECT 1 FROM code_review_agent_results r
            WHERE r.org_id = scope_org AND r.id = owner AND r.dispatch_due_at IS NOT NULL)
        WHEN 'review-loop-initial-send' THEN EXISTS (SELECT 1 FROM session_review_loops l
            WHERE l.org_id = scope_org AND l.id = owner AND l.startup_send_due_at IS NOT NULL)
        ELSE false END
$$;
CREATE FUNCTION caller143_guard_send_stamp() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'TRUNCATE' THEN
        IF EXISTS (SELECT 1 FROM caller_send_command_stamps s
            WHERE caller143_stamp_owner_unresolved(s.org_id,s.purpose,s.owner_id)) THEN
            RAISE EXCEPTION 'unresolved_caller_send_stamp' USING ERRCODE = '55000'; END IF;
        RETURN NULL;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF caller143_stamp_owner_unresolved(OLD.org_id,OLD.purpose,OLD.owner_id) THEN
            RAISE EXCEPTION 'unresolved_caller_send_stamp' USING ERRCODE = '55000'; END IF;
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'immutable_caller_send_stamp' USING ERRCODE = '55000'; END IF;
        RETURN NEW;
    END IF;
    PERFORM caller143_require_send_intents_enabled();
    IF NEW.purpose = 'code-review-reviewer-dispatch' THEN
        PERFORM 1 FROM code_review_agent_results r WHERE r.org_id = NEW.org_id AND r.session_id = NEW.session_id
            AND r.id = NEW.owner_id AND r.dispatch_thread_id = NEW.thread_id AND r.dispatch_key = NEW.client_message_id
            AND r.dispatch_version = NEW.version AND r.dispatch_digest = NEW.digest;
    ELSIF NEW.purpose = 'review-loop-initial-send' THEN
        PERFORM 1 FROM session_review_loops l WHERE l.org_id = NEW.org_id AND l.session_id = NEW.session_id
            AND l.id = NEW.owner_id AND l.startup_send_thread_id = NEW.thread_id AND l.startup_send_key = NEW.client_message_id
            AND l.startup_send_version = NEW.version AND l.startup_send_digest = NEW.digest;
    ELSE RAISE EXCEPTION 'caller_send_stamp_scope_mismatch' USING ERRCODE = '23503';
    END IF;
    IF NOT FOUND THEN RAISE EXCEPTION 'caller_send_stamp_scope_mismatch' USING ERRCODE = '23503'; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_caller_send_stamp BEFORE INSERT OR UPDATE OR DELETE ON caller_send_command_stamps
    FOR EACH ROW EXECUTE FUNCTION caller143_guard_send_stamp();
CREATE TRIGGER trg_caller_send_stamp_truncate BEFORE TRUNCATE ON caller_send_command_stamps
    FOR EACH STATEMENT EXECUTE FUNCTION caller143_guard_send_stamp();

CREATE FUNCTION caller143_assert_send_rollback_safe() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM code_review_agent_results WHERE dispatch_due_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM session_review_loops WHERE startup_send_due_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM review_loop_start_intents s WHERE caller143_start_unresolved(s.org_id,s.id)) THEN
        RAISE EXCEPTION 'unresolved_caller_send_prevents_rollback' USING ERRCODE = '55000';
    END IF;
END;
$$;
