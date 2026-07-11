ALTER TABLE sessions
    ADD CONSTRAINT sessions_id_org_id_key UNIQUE (id, org_id);

CREATE TABLE session_changesets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL,
    is_primary boolean NOT NULL DEFAULT false,
    order_index integer NOT NULL,
    title text NOT NULL,
    summary text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'planned',
    target_branch text NOT NULL,
    base_branch text NOT NULL,
    working_branch text,
    stacked_on_changeset_id uuid,
    head_sha text,
    expected_remote_head_sha text,
    base_head_sha text,
    pr_creation_state text NOT NULL DEFAULT 'idle',
    pr_creation_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT session_changesets_status_check CHECK (status IN (
        'planned', 'materializing', 'published_branch', 'pr_open',
        'needs_restack', 'restacking', 'restack_conflict',
        'external_update_detected', 'ready', 'merged', 'abandoned'
    )),
    CONSTRAINT session_changesets_order_nonnegative CHECK (order_index >= 0),
    CONSTRAINT session_changesets_pr_creation_state_check CHECK (
        pr_creation_state IN ('idle', 'queued', 'pushing', 'succeeded', 'failed')
    ),
    CONSTRAINT session_changesets_not_self_stacked CHECK (stacked_on_changeset_id IS NULL OR stacked_on_changeset_id <> id),
    UNIQUE (org_id, session_id, order_index),
    UNIQUE (org_id, session_id, working_branch),
    UNIQUE (id, org_id, session_id),
    FOREIGN KEY (session_id, org_id)
        REFERENCES sessions(id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (stacked_on_changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX session_changesets_one_primary
    ON session_changesets (org_id, session_id) WHERE is_primary;
CREATE INDEX session_changesets_session_order
    ON session_changesets (org_id, session_id, order_index);
CREATE INDEX session_changesets_parent
    ON session_changesets (org_id, stacked_on_changeset_id)
    WHERE stacked_on_changeset_id IS NOT NULL;

ALTER TABLE pull_requests
    ADD COLUMN changeset_id uuid,
    ADD CONSTRAINT pull_requests_changeset_requires_session
        CHECK (changeset_id IS NULL OR session_id IS NOT NULL),
    ADD CONSTRAINT pull_requests_changeset_scope_fkey
        FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id) ON DELETE RESTRICT;
INSERT INTO session_changesets (
    org_id, session_id, is_primary, order_index, title, summary, status,
    target_branch, base_branch, working_branch, head_sha,
    expected_remote_head_sha, base_head_sha, pr_creation_state, pr_creation_error
)
SELECT
    s.org_id,
    s.id,
    true,
    0,
    COALESCE(NULLIF(s.title, ''), 'Pull request'),
    COALESCE(s.result_summary, ''),
    CASE
        WHEN pr.status = 'merged' THEN 'merged'
        WHEN pr.status = 'open' THEN 'pr_open'
        WHEN s.working_branch IS NOT NULL THEN 'published_branch'
        ELSE 'planned'
    END,
    COALESCE(NULLIF(s.target_branch, ''), 'main'),
    COALESCE(NULLIF(s.target_branch, ''), 'main'),
    NULLIF(s.working_branch, ''),
    pr.head_sha,
    pr.head_sha,
    s.base_commit_sha,
    COALESCE(sps.pr_creation_state, 'idle'),
    sps.pr_creation_error
FROM sessions s
LEFT JOIN session_publish_state sps
    ON sps.org_id = s.org_id AND sps.session_id = s.id
LEFT JOIN LATERAL (
    SELECT status, head_sha
    FROM pull_requests
    WHERE org_id = s.org_id AND session_id = s.id
    ORDER BY created_at DESC, id DESC
    LIMIT 1
) pr ON true;

UPDATE pull_requests pr
SET changeset_id = sc.id
FROM session_changesets sc
WHERE sc.org_id = pr.org_id
  AND sc.session_id = pr.session_id
  AND sc.is_primary
  AND pr.id = (
      SELECT canonical.id
      FROM pull_requests canonical
      WHERE canonical.org_id = pr.org_id
        AND canonical.session_id = pr.session_id
      ORDER BY canonical.created_at DESC, canonical.id DESC
      LIMIT 1
  );

CREATE FUNCTION create_primary_session_changeset() RETURNS trigger AS $$
BEGIN
    INSERT INTO session_changesets (
        org_id, session_id, is_primary, order_index, title, summary, status,
        target_branch, base_branch, working_branch, base_head_sha
    ) VALUES (
        NEW.org_id,
        NEW.id,
        true,
        0,
        COALESCE(NULLIF(NEW.title, ''), 'Pull request'),
        COALESCE(NEW.result_summary, ''),
        'planned',
        COALESCE(NULLIF(NEW.target_branch, ''), 'main'),
        COALESCE(NULLIF(NEW.target_branch, ''), 'main'),
        NULLIF(NEW.working_branch, ''),
        NEW.base_commit_sha
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_sessions_create_primary_changeset
    AFTER INSERT ON sessions
    FOR EACH ROW EXECUTE FUNCTION create_primary_session_changeset();

CREATE FUNCTION enforce_session_primary_changeset() RETURNS trigger AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM sessions WHERE id = OLD.session_id AND org_id = OLD.org_id
    ) AND NOT EXISTS (
        SELECT 1 FROM session_changesets
        WHERE session_id = OLD.session_id AND org_id = OLD.org_id AND is_primary
    ) THEN
        RAISE EXCEPTION 'session % in org % must have a primary changeset', OLD.session_id, OLD.org_id;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER trg_session_changesets_require_primary_delete
    AFTER DELETE ON session_changesets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (OLD.is_primary)
    EXECUTE FUNCTION enforce_session_primary_changeset();

CREATE CONSTRAINT TRIGGER trg_session_changesets_require_primary_update
    AFTER UPDATE ON session_changesets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (
        OLD.is_primary AND (
            NOT NEW.is_primary
            OR OLD.session_id IS DISTINCT FROM NEW.session_id
            OR OLD.org_id IS DISTINCT FROM NEW.org_id
        )
    )
    EXECUTE FUNCTION enforce_session_primary_changeset();

CREATE FUNCTION mirror_session_branches_to_primary_changeset() RETURNS trigger AS $$
BEGIN
    UPDATE session_changesets
    SET title = COALESCE(NULLIF(NEW.title, ''), title),
        summary = COALESCE(NEW.result_summary, summary),
        target_branch = COALESCE(NULLIF(NEW.target_branch, ''), target_branch),
        base_branch = CASE
            WHEN stacked_on_changeset_id IS NULL THEN COALESCE(NULLIF(NEW.target_branch, ''), base_branch)
            ELSE base_branch
        END,
        working_branch = NULLIF(NEW.working_branch, ''),
        base_head_sha = NEW.base_commit_sha,
        status = CASE
            WHEN OLD.working_branch IS DISTINCT FROM NEW.working_branch
             AND NULLIF(NEW.working_branch, '') IS NOT NULL
             AND status IN ('planned', 'published_branch')
            THEN 'published_branch'
            ELSE status
        END,
        updated_at = now()
    WHERE org_id = NEW.org_id AND session_id = NEW.id AND is_primary;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_sessions_mirror_primary_changeset_branches
    AFTER UPDATE OF title, result_summary, target_branch, working_branch, base_commit_sha ON sessions
    FOR EACH ROW
    WHEN (OLD.title IS DISTINCT FROM NEW.title
       OR OLD.result_summary IS DISTINCT FROM NEW.result_summary
       OR OLD.target_branch IS DISTINCT FROM NEW.target_branch
       OR OLD.working_branch IS DISTINCT FROM NEW.working_branch
       OR OLD.base_commit_sha IS DISTINCT FROM NEW.base_commit_sha)
    EXECUTE FUNCTION mirror_session_branches_to_primary_changeset();

CREATE FUNCTION mirror_primary_changeset_branches_to_session() RETURNS trigger AS $$
BEGIN
    UPDATE sessions
    SET target_branch = NEW.target_branch,
        working_branch = NEW.working_branch,
        base_commit_sha = NEW.base_head_sha
    WHERE org_id = NEW.org_id
      AND id = NEW.session_id
      AND (target_branch IS DISTINCT FROM NEW.target_branch
        OR working_branch IS DISTINCT FROM NEW.working_branch
        OR base_commit_sha IS DISTINCT FROM NEW.base_head_sha);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_primary_changeset_mirror_session_branches
    AFTER UPDATE OF target_branch, working_branch, base_head_sha ON session_changesets
    FOR EACH ROW
    WHEN (NEW.is_primary AND (
        OLD.target_branch IS DISTINCT FROM NEW.target_branch
        OR OLD.working_branch IS DISTINCT FROM NEW.working_branch
        OR OLD.base_head_sha IS DISTINCT FROM NEW.base_head_sha
    ))
    EXECUTE FUNCTION mirror_primary_changeset_branches_to_session();

CREATE FUNCTION mirror_session_publish_state_to_primary_changeset() RETURNS trigger AS $$
BEGIN
    UPDATE session_changesets
    SET pr_creation_state = NEW.pr_creation_state,
        pr_creation_error = NEW.pr_creation_error,
        updated_at = now()
    WHERE org_id = NEW.org_id AND session_id = NEW.session_id AND is_primary
      AND (pr_creation_state IS DISTINCT FROM NEW.pr_creation_state
        OR pr_creation_error IS DISTINCT FROM NEW.pr_creation_error);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_session_publish_state_mirror_primary_changeset_insert
    AFTER INSERT ON session_publish_state
    FOR EACH ROW EXECUTE FUNCTION mirror_session_publish_state_to_primary_changeset();

CREATE TRIGGER trg_session_publish_state_mirror_primary_changeset_update
    AFTER UPDATE OF pr_creation_state, pr_creation_error ON session_publish_state
    FOR EACH ROW EXECUTE FUNCTION mirror_session_publish_state_to_primary_changeset();

CREATE FUNCTION mirror_primary_changeset_publish_state_to_session() RETURNS trigger AS $$
BEGIN
    INSERT INTO session_publish_state (session_id, org_id, pr_creation_state, pr_creation_error)
    VALUES (NEW.session_id, NEW.org_id, NEW.pr_creation_state, NEW.pr_creation_error)
    ON CONFLICT (session_id) DO UPDATE
    SET pr_creation_state = EXCLUDED.pr_creation_state,
        pr_creation_error = EXCLUDED.pr_creation_error,
        updated_at = now()
    WHERE session_publish_state.pr_creation_state IS DISTINCT FROM EXCLUDED.pr_creation_state
       OR session_publish_state.pr_creation_error IS DISTINCT FROM EXCLUDED.pr_creation_error;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_primary_changeset_mirror_session_publish_state
    AFTER UPDATE OF pr_creation_state, pr_creation_error ON session_changesets
    FOR EACH ROW
    WHEN (NEW.is_primary AND (
        OLD.pr_creation_state IS DISTINCT FROM NEW.pr_creation_state
        OR OLD.pr_creation_error IS DISTINCT FROM NEW.pr_creation_error
    ))
    EXECUTE FUNCTION mirror_primary_changeset_publish_state_to_session();

CREATE FUNCTION assign_pull_request_primary_changeset() RETURNS trigger AS $$
BEGIN
    IF NEW.session_id IS NOT NULL AND NEW.changeset_id IS NULL THEN
        SELECT id INTO NEW.changeset_id
        FROM session_changesets
        WHERE org_id = NEW.org_id AND session_id = NEW.session_id AND is_primary;

        IF NEW.changeset_id IS NULL THEN
            RAISE EXCEPTION 'primary changeset missing for session % in org %', NEW.session_id, NEW.org_id;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_pull_requests_assign_primary_changeset
    BEFORE INSERT ON pull_requests
    FOR EACH ROW EXECUTE FUNCTION assign_pull_request_primary_changeset();

CREATE FUNCTION sync_pull_request_changeset_state() RETURNS trigger AS $$
BEGIN
    IF NEW.changeset_id IS NULL THEN
        RETURN NEW;
    END IF;

    UPDATE session_changesets
    SET status = CASE NEW.status
            WHEN 'open' THEN 'pr_open'
            WHEN 'merged' THEN 'merged'
            ELSE 'published_branch'
        END,
        head_sha = CASE WHEN TG_OP = 'INSERT' THEN COALESCE(NEW.head_sha, head_sha) ELSE head_sha END,
        expected_remote_head_sha = CASE WHEN TG_OP = 'INSERT' THEN COALESCE(NEW.head_sha, expected_remote_head_sha) ELSE expected_remote_head_sha END,
        updated_at = now()
    WHERE org_id = NEW.org_id AND id = NEW.changeset_id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_pull_requests_sync_changeset_state_insert
    AFTER INSERT ON pull_requests
    FOR EACH ROW EXECUTE FUNCTION sync_pull_request_changeset_state();

CREATE TRIGGER trg_pull_requests_sync_changeset_state_update
    AFTER UPDATE OF status, head_sha, changeset_id ON pull_requests
    FOR EACH ROW EXECUTE FUNCTION sync_pull_request_changeset_state();
