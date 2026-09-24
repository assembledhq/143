-- One durable, assessment-scoped outbox and completion receipt. This is not a
-- second review controller: the existing continue_session job runs the turn.
-- Production: prebuild these two hot-table indexes with CREATE UNIQUE INDEX
-- CONCURRENTLY before applying this transactional migration. Check indisvalid;
-- drop an invalid failed build before retrying: IF NOT EXISTS does not validate
-- a prebuilt index. As in migration 281, lock_timeout bounds acquisition,
-- not the duration of an index build once its lock is held.
SET LOCAL max_parallel_maintenance_workers = 0;
SET LOCAL max_parallel_workers_per_gather = 0;
SET LOCAL maintenance_work_mem = '64MB';
SET LOCAL lock_timeout = '30s';
CREATE UNIQUE INDEX IF NOT EXISTS code_review_recheck_threads_org_id ON session_threads(org_id,id);
CREATE UNIQUE INDEX IF NOT EXISTS code_review_recheck_jobs_org_id ON jobs(org_id,id);
ALTER TABLE sessions ADD COLUMN code_review_owner_pr_id uuid;
ALTER TABLE sessions ADD CONSTRAINT sessions_code_review_owner_pr_fk
    FOREIGN KEY (org_id,code_review_owner_pr_id) REFERENCES pull_requests(org_id,id);

-- The session row lock closes the race between ordinary SendMessage's
-- preflight guard and its later message insert. Only the assessment outbox
-- may append a user message to a review-owned conversation.
CREATE FUNCTION guard_code_review_owned_message() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.role = 'user' AND COALESCE(NEW.source,'') <> 'code_review_recheck'
       AND EXISTS (SELECT 1 FROM sessions s WHERE s.org_id=NEW.org_id AND s.id=NEW.session_id
                   AND s.code_review_owner_pr_id IS NOT NULL FOR SHARE) THEN
        RAISE EXCEPTION 'session belongs to code review' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_code_review_owned_message BEFORE INSERT ON session_messages
    FOR EACH ROW EXECUTE FUNCTION guard_code_review_owned_message();

CREATE FUNCTION guard_code_review_owned_preview() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner_id uuid;
BEGIN
    IF NEW.preview_holding_container AND NOT COALESCE(OLD.preview_holding_container,false)
       AND NEW.session_id IS NOT NULL THEN
        SELECT s.code_review_owner_pr_id INTO owner_id FROM sessions s
        WHERE s.org_id=NEW.org_id AND s.id=NEW.session_id FOR SHARE;
        IF owner_id IS NOT NULL THEN
            RAISE EXCEPTION 'session belongs to code review' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_code_review_owned_preview BEFORE UPDATE OF preview_holding_container ON preview_instances
    FOR EACH ROW EXECUTE FUNCTION guard_code_review_owned_preview();

CREATE FUNCTION guard_code_review_owned_thread_structure() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner_id uuid;
BEGIN
    IF TG_OP='INSERT' THEN
        SELECT s.code_review_owner_pr_id INTO owner_id FROM sessions s
        WHERE s.org_id=NEW.org_id AND s.id=NEW.session_id FOR SHARE;
    ELSIF OLD.archived_at IS DISTINCT FROM NEW.archived_at THEN
        SELECT s.code_review_owner_pr_id INTO owner_id FROM sessions s
        WHERE s.org_id=NEW.org_id AND s.id=NEW.session_id FOR SHARE;
    END IF;
    IF owner_id IS NOT NULL THEN
        RAISE EXCEPTION 'session belongs to code review' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_code_review_owned_thread_structure BEFORE INSERT OR UPDATE OF archived_at ON session_threads
    FOR EACH ROW EXECUTE FUNCTION guard_code_review_owned_thread_structure();
CREATE TABLE code_review_recheck_dispatches (
    assessment_id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    repository_id uuid NOT NULL,
    pull_request_id uuid NOT NULL,
    session_id uuid NOT NULL,
    thread_id uuid NOT NULL,
    expected_turn integer NOT NULL CHECK (expected_turn > 0),
    payload_digest text NOT NULL CHECK (payload_digest <> ''),
    message_id bigint NOT NULL,
    job_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','completed','failed','cancelled')),
    attempt_lock_token uuid,
    result_message_id bigint,
    provider_session_id text,
    snapshot_key text,
    native_context boolean,
    failure_detail text,
    attempt_usage jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(attempt_usage) = 'object'),
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (org_id,assessment_id) REFERENCES code_review_revision_assessments(org_id,id),
    FOREIGN KEY (org_id,repository_id) REFERENCES repositories(org_id,id),
    FOREIGN KEY (org_id,pull_request_id) REFERENCES pull_requests(org_id,id),
    FOREIGN KEY (org_id,session_id) REFERENCES sessions(org_id,id),
    FOREIGN KEY (org_id,thread_id) REFERENCES session_threads(org_id,id),
    -- session_messages is partitioned by created_at and has no unique
    -- (org_id,id) key. Writers verify message identity transactionally.
    FOREIGN KEY (org_id,job_id) REFERENCES jobs(org_id,id),
    UNIQUE (org_id,thread_id,expected_turn),
    UNIQUE (org_id,message_id),
    UNIQUE (org_id,job_id)
);
CREATE INDEX code_review_recheck_dispatches_active
    ON code_review_recheck_dispatches(org_id,status,created_at)
    WHERE status IN ('pending','running');

-- A recheck receipt retains its exact job identity beyond normal job TTL.
-- Filter protected rows inside the batch candidate subquery so a batch of
-- old retained jobs cannot starve deletion of unrelated expired jobs.
CREATE OR REPLACE FUNCTION delete_expired_completed_jobs(p_retention_days int)
RETURNS bigint LANGUAGE plpgsql SET search_path = public AS $$
DECLARE total_deleted bigint := 0; batch_deleted bigint;
BEGIN
    IF p_retention_days <= 0 THEN RETURN 0; END IF;
    LOOP
        DELETE FROM jobs WHERE id IN (
            SELECT j.id FROM jobs j
            WHERE j.status IN ('completed','failed')
              AND j.updated_at < now() - make_interval(days => p_retention_days)
              AND NOT EXISTS (SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=j.org_id AND d.job_id=j.id)
              AND NOT EXISTS (SELECT 1 FROM code_review_revision_assessments a
                  WHERE a.org_id=j.org_id AND a.review_scope='full' AND a.status IN ('running','publishing')
                  AND a.result_origin IS NOT NULL AND j.job_type='run_code_review'
                  AND j.payload->>'session_id'=a.session_id::text
                  AND j.payload->>'review_output_key'=a.publication_key)
            LIMIT 10000
        );
        GET DIAGNOSTICS batch_deleted = ROW_COUNT;
        total_deleted := total_deleted + batch_deleted;
        EXIT WHEN batch_deleted < 10000;
    END LOOP;
    RETURN total_deleted;
END;
$$;
