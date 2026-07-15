ALTER TABLE session_diff_snapshots
    ADD CONSTRAINT session_diff_snapshots_tenant_identity UNIQUE (id, org_id, session_id);

CREATE TABLE session_changeset_split_plans (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL,
    source_diff_snapshot_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'accepted')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    accepted_at timestamptz,
    accepted_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (session_id, org_id) REFERENCES sessions(id, org_id) ON DELETE CASCADE,
    FOREIGN KEY (source_diff_snapshot_id, org_id, session_id)
        REFERENCES session_diff_snapshots(id, org_id, session_id) ON DELETE NO ACTION,
    UNIQUE (org_id, session_id)
);

CREATE TABLE session_changeset_split_paths (
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL,
    changeset_id uuid NOT NULL,
    path text NOT NULL CHECK (path <> '' AND path = btrim(path)),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, session_id, changeset_id, path),
    FOREIGN KEY (org_id, session_id) REFERENCES session_changeset_split_plans(org_id, session_id) ON DELETE CASCADE,
    FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id) ON DELETE CASCADE
);

CREATE INDEX session_changeset_split_paths_by_path
    ON session_changeset_split_paths (org_id, session_id, path);

CREATE TABLE session_changeset_split_omissions (
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL,
    path text NOT NULL CHECK (path <> '' AND path = btrim(path)),
    reason text NOT NULL CHECK (btrim(reason) <> ''),
    confirmed_by_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, session_id, path),
    FOREIGN KEY (org_id, session_id) REFERENCES session_changeset_split_plans(org_id, session_id) ON DELETE CASCADE
);

ALTER TABLE session_changesets
    ADD COLUMN worktree_path text,
    ADD COLUMN materialization_error text,
    ADD COLUMN materialized_diff text,
    ADD CONSTRAINT session_changesets_materialized_worktree
        CHECK (worktree_path IS NULL OR (working_branch IS NOT NULL AND btrim(worktree_path) <> ''));
CREATE UNIQUE INDEX session_changesets_one_materializing_per_session
    ON session_changesets (org_id, session_id) WHERE status = 'materializing';

ALTER TABLE pr_readiness_runs
    ADD COLUMN changeset_id uuid,
    ADD COLUMN evaluated_head_sha text;
UPDATE pr_readiness_runs r SET changeset_id = c.id
FROM session_changesets c
WHERE c.org_id = r.org_id AND c.session_id = r.session_id AND c.is_primary;
ALTER TABLE pr_readiness_runs
    ALTER COLUMN changeset_id SET NOT NULL,
    ADD CONSTRAINT pr_readiness_runs_changeset_scope_fkey
        FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id) ON DELETE CASCADE;
DROP INDEX idx_pr_readiness_runs_session_latest;
CREATE INDEX idx_pr_readiness_runs_changeset_latest
    ON pr_readiness_runs (org_id, session_id, changeset_id, created_at DESC, id DESC);

CREATE FUNCTION assign_pr_readiness_primary_changeset() RETURNS trigger AS $$
BEGIN
    IF NEW.changeset_id IS NULL THEN
        SELECT id INTO NEW.changeset_id FROM session_changesets
        WHERE org_id = NEW.org_id AND session_id = NEW.session_id AND is_primary;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_pr_readiness_assign_primary_changeset
    BEFORE INSERT ON pr_readiness_runs FOR EACH ROW EXECUTE FUNCTION assign_pr_readiness_primary_changeset();

ALTER TABLE pr_readiness_checks ADD COLUMN changeset_id uuid;
UPDATE pr_readiness_checks c SET changeset_id = r.changeset_id
FROM pr_readiness_runs r WHERE r.id = c.run_id AND r.org_id = c.org_id;
ALTER TABLE pr_readiness_checks
    ALTER COLUMN changeset_id SET NOT NULL,
    ADD CONSTRAINT pr_readiness_checks_changeset_scope_fkey
        FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id) ON DELETE CASCADE;

ALTER TABLE pr_readiness_bypasses ADD COLUMN changeset_id uuid;
UPDATE pr_readiness_bypasses b SET changeset_id = r.changeset_id
FROM pr_readiness_runs r WHERE r.id = b.readiness_run_id AND r.org_id = b.org_id;
ALTER TABLE pr_readiness_bypasses
    ALTER COLUMN changeset_id SET NOT NULL,
    ADD CONSTRAINT pr_readiness_bypasses_changeset_scope_fkey
        FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id) ON DELETE CASCADE;
