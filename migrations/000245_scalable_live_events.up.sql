ALTER TABLE sessions ADD COLUMN live_version bigint NOT NULL DEFAULT 1;
ALTER TABLE preview_instances ADD COLUMN live_version bigint NOT NULL DEFAULT 1;
ALTER TABLE automations ADD COLUMN live_version bigint NOT NULL DEFAULT 1;
ALTER TABLE automation_runs ADD COLUMN live_version bigint NOT NULL DEFAULT 1;

CREATE FUNCTION validate_live_event_json(value jsonb) RETURNS boolean
LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT COALESCE((
        value->>'schema_version' = '1'
        AND (value->>'event_id')::uuid IS NOT NULL
        AND (value->>'org_id')::uuid IS NOT NULL
        AND value->>'type' IN (
            'session.created', 'session.updated', 'preview.updated', 'automation.updated',
            'automation.run.updated', 'code_review.updated', 'pull_request.updated',
            'eval_batch.updated', 'eval_bootstrap.updated', 'authorization.changed'
        )
        AND value->>'scope' IN ('resource', 'collection')
        AND value->>'audience' IN ('org', 'repository', 'resource')
        AND value->>'resource_type' = CASE value->>'type'
            WHEN 'session.created' THEN 'session'
            WHEN 'session.updated' THEN 'session'
            WHEN 'preview.updated' THEN 'preview'
            WHEN 'automation.updated' THEN 'automation'
            WHEN 'automation.run.updated' THEN 'automation_run'
            WHEN 'code_review.updated' THEN 'code_review'
            WHEN 'pull_request.updated' THEN 'pull_request'
            WHEN 'eval_batch.updated' THEN 'eval_batch'
            WHEN 'eval_bootstrap.updated' THEN 'eval_bootstrap'
            WHEN 'authorization.changed' THEN 'authorization'
        END
        AND COALESCE((value->>'changed_at')::timestamptz IS NOT NULL, false)
        AND jsonb_typeof(value->'payload') = 'object'
        AND octet_length((value->'payload')::text) <= 4096
        AND (
            (value->>'scope' = 'collection' AND NOT (value ? 'resource_id'))
            OR (value->>'scope' = 'resource' AND COALESCE((value->>'resource_id')::uuid IS NOT NULL, false))
        )
        AND (value->>'audience' <> 'repository' OR COALESCE((value->>'repository_id')::uuid IS NOT NULL, false))
        AND (value->>'audience' <> 'resource' OR COALESCE((value->>'resource_id')::uuid IS NOT NULL, false))
        AND ((value ? 'parent_type') = (value ? 'parent_id'))
        AND (NOT (value ? 'parent_type') OR (
            COALESCE(value->>'parent_type' IN (
                'session', 'preview', 'automation', 'automation_run', 'code_review',
                'pull_request', 'eval_batch', 'eval_bootstrap', 'authorization'
            ), false)
            AND COALESCE((value->>'parent_id')::uuid IS NOT NULL, false)
        ))
        AND (value->>'type' <> 'authorization.changed' OR COALESCE((value->'payload'->>'user_id')::uuid IS NOT NULL, false))
        AND (value->>'scope' <> 'resource' OR value->>'type' NOT IN (
            'session.updated', 'preview.updated', 'automation.updated', 'automation.run.updated'
        ) OR COALESCE((value->>'version')::bigint, 0) > 0)
        AND (NOT (value->'payload' ? 'status_projection') OR COALESCE((value->>'version')::bigint, 0) > 0)
    ), false)
$$;

CREATE TABLE live_event_outbox (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    event_type text NOT NULL,
    coalesce_key text,
    event jsonb NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    claim_owner text,
    claim_expires_at timestamptz,
    aggregate boolean NOT NULL DEFAULT false,
    published_at timestamptz,
    folded_into_event_id uuid REFERENCES live_event_outbox(id),
    last_error text,
    originated_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT live_event_outbox_event_valid CHECK (validate_live_event_json(event)),
    CONSTRAINT live_event_outbox_event_identity CHECK (
        event->>'event_id' = id::text AND event->>'org_id' = org_id::text AND event->>'type' = event_type
    )
);

CREATE INDEX live_event_outbox_pending_idx ON live_event_outbox (available_at, claim_expires_at, created_at)
WHERE published_at IS NULL AND folded_into_event_id IS NULL;
CREATE INDEX live_event_outbox_pending_age_idx ON live_event_outbox (originated_at)
WHERE published_at IS NULL AND folded_into_event_id IS NULL;

CREATE FUNCTION notify_live_event_outbox() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('live_event_outbox', NEW.org_id::text);
    RETURN NEW;
END;
$$;
CREATE TRIGGER live_event_outbox_notify AFTER INSERT ON live_event_outbox
FOR EACH ROW EXECUTE FUNCTION notify_live_event_outbox();

CREATE FUNCTION bump_live_version() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    old_projection jsonb;
    new_projection jsonb;
BEGIN
    -- Related-table triggers (for example session_publish_state below) bump
    -- the owning projection revision explicitly. Preserve only the expected
    -- one-step increment; otherwise the generic row comparison would erase it
    -- because live_version itself is intentionally excluded from projection
    -- equality.
    IF NEW.live_version IS DISTINCT FROM OLD.live_version THEN
        IF NEW.live_version <> OLD.live_version + 1 THEN
            RAISE EXCEPTION 'live_version must advance exactly once';
        END IF;
        RETURN NEW;
    END IF;
    old_projection := to_jsonb(OLD) - 'live_version' - 'updated_at';
    new_projection := to_jsonb(NEW) - 'live_version' - 'updated_at';
    IF TG_TABLE_NAME = 'preview_instances' THEN
        old_projection := old_projection - 'last_accessed_at' - 'peak_memory_bytes' - 'peak_memory_sampled_at' - 'peak_memory_phase';
        new_projection := new_projection - 'last_accessed_at' - 'peak_memory_bytes' - 'peak_memory_sampled_at' - 'peak_memory_phase';
    ELSIF TG_TABLE_NAME = 'sessions' THEN
        old_projection := old_projection - 'last_activity_at' - 'token_usage' - 'runtime_last_progress_at' - 'runtime_last_progress_type' - 'runtime_last_progress_strength';
        new_projection := new_projection - 'last_activity_at' - 'token_usage' - 'runtime_last_progress_at' - 'runtime_last_progress_type' - 'runtime_last_progress_strength';
    END IF;
    IF old_projection IS NOT DISTINCT FROM new_projection THEN
        NEW.live_version := OLD.live_version;
        RETURN NEW;
    END IF;
    NEW.live_version := OLD.live_version + 1;
    RETURN NEW;
END;
$$;

CREATE TRIGGER sessions_bump_live_version BEFORE UPDATE ON sessions
FOR EACH ROW EXECUTE FUNCTION bump_live_version();
CREATE TRIGGER preview_instances_bump_live_version BEFORE UPDATE ON preview_instances
FOR EACH ROW EXECUTE FUNCTION bump_live_version();
CREATE TRIGGER automations_bump_live_version BEFORE UPDATE ON automations
FOR EACH ROW EXECUTE FUNCTION bump_live_version();
CREATE TRIGGER automation_runs_bump_live_version BEFORE UPDATE ON automation_runs
FOR EACH ROW EXECUTE FUNCTION bump_live_version();

CREATE FUNCTION enqueue_live_projection() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    event_id uuid := gen_random_uuid();
    event_type text;
    resource_type text := TG_ARGV[0];
    event_scope text := 'resource';
    resource_id uuid := NEW.id;
    parent_type text;
    parent_id uuid;
    payload jsonb;
    key text;
    session_pr_creation_state text;
    session_pr_push_state text;
    session_branch_creation_state text;
    mutation_id uuid := NULLIF(NULLIF(current_setting('app.client_mutation_id', true), ''), '00000000-0000-0000-0000-000000000000')::uuid;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.live_version = OLD.live_version THEN
        RETURN NEW;
    END IF;
    IF TG_TABLE_NAME = 'sessions' THEN
        IF TG_OP = 'INSERT' THEN
            event_type := 'session.created';
            event_scope := 'collection';
            resource_id := NULL;
            payload := jsonb_build_object('list_affected', true, 'counts_affected', true);
        ELSE
            event_type := 'session.updated';
            SELECT pr_creation_state, pr_push_state, branch_creation_state
            INTO session_pr_creation_state, session_pr_push_state, session_branch_creation_state
            FROM session_publish_state
            WHERE session_id = NEW.id AND org_id = NEW.org_id;
            payload := jsonb_build_object(
                'status_projection', jsonb_build_object(
                    'status', NEW.status,
                    'pr_creation_state', COALESCE(session_pr_creation_state, 'idle'),
                    'pr_push_state', COALESCE(session_pr_push_state, 'idle'),
                    'branch_creation_state', COALESCE(session_branch_creation_state, 'idle')
                ),
                'list_affected', true,
                'counts_affected', OLD.status IS DISTINCT FROM NEW.status OR OLD.archived_at IS DISTINCT FROM NEW.archived_at
            );
        END IF;
    ELSIF TG_TABLE_NAME = 'preview_instances' THEN
        event_type := 'preview.updated';
        payload := jsonb_build_object(
            'status_projection', jsonb_build_object('status', NEW.status),
            'list_affected', true,
            'counts_affected', false
        );
    ELSIF TG_TABLE_NAME = 'automations' THEN
        event_type := 'automation.updated';
        payload := jsonb_build_object(
            'status_projection', jsonb_build_object('enabled', NEW.enabled),
            'list_affected', true,
            'counts_affected', false
        );
    ELSIF TG_TABLE_NAME = 'automation_runs' THEN
        event_type := 'automation.run.updated';
        parent_type := 'automation';
        parent_id := NEW.automation_id;
        payload := jsonb_build_object(
            'status_projection', jsonb_build_object('status', NEW.status),
            'list_affected', true,
            'counts_affected', NEW.status IN ('completed', 'failed', 'cancelled', 'skipped')
        );
    ELSE
        RAISE EXCEPTION 'unsupported live projection table %', TG_TABLE_NAME;
    END IF;

    key := NEW.org_id::text || ':' || resource_type || ':' || COALESCE(resource_id::text, 'collection');
    INSERT INTO live_event_outbox (id, org_id, event_type, coalesce_key, event, originated_at)
    VALUES (
        event_id,
        NEW.org_id,
        event_type,
        key,
        jsonb_build_object(
            'schema_version', 1,
            'event_id', event_id,
            'type', event_type,
            'scope', event_scope,
            'org_id', NEW.org_id,
            'resource_type', resource_type,
            'resource_id', resource_id,
            'parent_type', parent_type,
            'parent_id', parent_id,
            'audience', 'org',
            'causation_id', mutation_id,
            'version', CASE WHEN event_scope = 'resource' THEN NEW.live_version ELSE NULL END,
            'changed_at', now(),
            'payload', payload
        ) - CASE WHEN resource_id IS NULL THEN 'resource_id' ELSE '__never__' END
          - CASE WHEN parent_id IS NULL THEN 'parent_id' ELSE '__never__' END
          - CASE WHEN parent_type IS NULL THEN 'parent_type' ELSE '__never__' END
          - CASE WHEN event_scope = 'collection' THEN 'version' ELSE '__never__' END,
        now()
    );
    RETURN NEW;
END;
$$;

CREATE TRIGGER sessions_enqueue_live_projection AFTER INSERT OR UPDATE ON sessions
FOR EACH ROW EXECUTE FUNCTION enqueue_live_projection('session');
CREATE TRIGGER preview_instances_enqueue_live_projection AFTER INSERT OR UPDATE ON preview_instances
FOR EACH ROW EXECUTE FUNCTION enqueue_live_projection('preview');
CREATE TRIGGER automations_enqueue_live_projection AFTER INSERT OR UPDATE ON automations
FOR EACH ROW EXECUTE FUNCTION enqueue_live_projection('automation');
CREATE TRIGGER automation_runs_enqueue_live_projection AFTER INSERT OR UPDATE ON automation_runs
FOR EACH ROW EXECUTE FUNCTION enqueue_live_projection('automation_run');

CREATE FUNCTION touch_session_live_projection_from_publish_state() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE sessions
    SET live_version = live_version + 1
    WHERE id = NEW.session_id AND org_id = NEW.org_id;
    RETURN NEW;
END;
$$;

CREATE TRIGGER session_publish_state_touch_live_projection
AFTER INSERT OR UPDATE OF pr_creation_state, pr_push_state, branch_creation_state ON session_publish_state
FOR EACH ROW EXECUTE FUNCTION touch_session_live_projection_from_publish_state();

CREATE FUNCTION enqueue_live_authorization_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    event_id uuid := gen_random_uuid();
    target_org_id uuid := COALESCE(NEW.org_id, OLD.org_id);
    target_user_id uuid := COALESCE(NEW.user_id, OLD.user_id);
BEGIN
    INSERT INTO live_event_outbox (id, org_id, event_type, coalesce_key, event, originated_at)
    VALUES (event_id, target_org_id, 'authorization.changed', target_org_id::text || ':authorization:' || target_user_id::text,
      jsonb_build_object('schema_version',1,'event_id',event_id,'type','authorization.changed','scope','collection',
        'org_id',target_org_id,'resource_type','authorization','audience','org','changed_at',now(),
        'payload',jsonb_build_object('user_id',target_user_id)), now());
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER organization_memberships_enqueue_live_authorization_change
AFTER UPDATE OR DELETE ON organization_memberships FOR EACH ROW EXECUTE FUNCTION enqueue_live_authorization_change();

CREATE FUNCTION enqueue_live_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_id uuid := gen_random_uuid(); event_type text := TG_ARGV[0]; resource_type text := TG_ARGV[1];
    mutation_id uuid := NULLIF(NULLIF(current_setting('app.client_mutation_id', true), ''), '00000000-0000-0000-0000-000000000000')::uuid;
BEGIN
    INSERT INTO live_event_outbox (id,org_id,event_type,coalesce_key,event,originated_at)
    VALUES (event_id,NEW.org_id,event_type,NEW.org_id::text||':'||resource_type||':'||NEW.id::text,
      jsonb_build_object('schema_version',1,'event_id',event_id,'type',event_type,'scope','resource','org_id',NEW.org_id,
        'resource_type',resource_type,'resource_id',NEW.id,'audience','org','changed_at',now(),
        'causation_id',mutation_id,
        'payload',jsonb_build_object('list_affected',true,'counts_affected',false)),now());
    RETURN NEW;
END;
$$;
CREATE TRIGGER pull_requests_enqueue_live_invalidation AFTER INSERT OR UPDATE ON pull_requests
FOR EACH ROW EXECUTE FUNCTION enqueue_live_invalidation('pull_request.updated','pull_request');
CREATE TRIGGER eval_batches_enqueue_live_invalidation AFTER INSERT OR UPDATE ON eval_batches
FOR EACH ROW EXECUTE FUNCTION enqueue_live_invalidation('eval_batch.updated','eval_batch');
CREATE TRIGGER eval_bootstrap_runs_enqueue_live_invalidation AFTER INSERT OR UPDATE ON eval_bootstrap_runs
FOR EACH ROW EXECUTE FUNCTION enqueue_live_invalidation('eval_bootstrap.updated','eval_bootstrap');
CREATE TRIGGER code_review_metadata_enqueue_live_invalidation AFTER INSERT OR UPDATE ON code_review_session_metadata
FOR EACH ROW EXECUTE FUNCTION enqueue_live_invalidation('code_review.updated','code_review');

CREATE FUNCTION enqueue_live_related_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    event_id uuid := gen_random_uuid();
    event_type text := TG_ARGV[0];
    resource_type text := TG_ARGV[1];
    related_id uuid;
    event_scope text := 'resource';
    mutation_id uuid := NULLIF(NULLIF(current_setting('app.client_mutation_id', true), ''), '00000000-0000-0000-0000-000000000000')::uuid;
BEGIN
    IF TG_TABLE_NAME = 'eval_runs' THEN
        related_id := NEW.batch_id;
        IF related_id IS NULL THEN RETURN NEW; END IF;
    ELSIF TG_TABLE_NAME = 'automation_goal_improvements' THEN
        related_id := NEW.automation_id;
        event_scope := 'collection';
    ELSIF TG_TABLE_NAME = 'session_preview_prewarm_runs' THEN
        related_id := NEW.preview_id;
        event_scope := 'collection';
    ELSIF TG_TABLE_NAME = 'preview_targets' OR TG_TABLE_NAME = 'preview_links' THEN
        related_id := NULL;
        event_scope := 'collection';
    ELSE
        RAISE EXCEPTION 'unsupported related live invalidation table %', TG_TABLE_NAME;
    END IF;

    INSERT INTO live_event_outbox (id,org_id,event_type,coalesce_key,event,originated_at)
    VALUES (event_id,NEW.org_id,event_type,NEW.org_id::text||':'||resource_type||':'||COALESCE(related_id::text,'collection'),
      jsonb_strip_nulls(jsonb_build_object('schema_version',1,'event_id',event_id,'type',event_type,'scope',event_scope,
        'org_id',NEW.org_id,'resource_type',resource_type,
        'resource_id',CASE WHEN event_scope = 'resource' THEN related_id ELSE NULL END,
        'causation_id',mutation_id,
        'audience','org','changed_at',now(),
        'payload',jsonb_build_object('list_affected',true,'counts_affected',false))),now());
    RETURN NEW;
END;
$$;
CREATE TRIGGER eval_runs_enqueue_live_invalidation AFTER INSERT OR UPDATE ON eval_runs
FOR EACH ROW EXECUTE FUNCTION enqueue_live_related_invalidation('eval_batch.updated','eval_batch');
CREATE TRIGGER automation_goal_improvements_enqueue_live_invalidation AFTER INSERT OR UPDATE ON automation_goal_improvements
FOR EACH ROW EXECUTE FUNCTION enqueue_live_related_invalidation('automation.updated','automation');
CREATE TRIGGER session_preview_prewarm_runs_enqueue_live_invalidation AFTER INSERT OR UPDATE ON session_preview_prewarm_runs
FOR EACH ROW EXECUTE FUNCTION enqueue_live_related_invalidation('preview.updated','preview');
CREATE TRIGGER preview_targets_enqueue_live_invalidation AFTER INSERT OR UPDATE ON preview_targets
FOR EACH ROW EXECUTE FUNCTION enqueue_live_related_invalidation('preview.updated','preview');
CREATE TRIGGER preview_links_enqueue_live_invalidation AFTER INSERT OR UPDATE ON preview_links
FOR EACH ROW EXECUTE FUNCTION enqueue_live_related_invalidation('preview.updated','preview');
