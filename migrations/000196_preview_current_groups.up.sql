CREATE TABLE preview_groups (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id       UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    group_kind          TEXT        NOT NULL,
    branch              TEXT        NOT NULL DEFAULT '',
    preview_config_name TEXT        NOT NULL DEFAULT '',
    pull_request_number INTEGER,
    source_type         TEXT        NOT NULL DEFAULT '',
    source_id           TEXT        NOT NULL DEFAULT '',
    source_url          TEXT        NOT NULL DEFAULT '',
    current_target_id   UUID        REFERENCES preview_targets(id) ON DELETE SET NULL,
    latest_commit_sha   TEXT        NOT NULL DEFAULT '',
    current_status      TEXT        NOT NULL DEFAULT 'none',
    pinned              BOOLEAN     NOT NULL DEFAULT false,
    created_by_user_id  UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_activity_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT preview_groups_kind_check
        CHECK (group_kind IN ('pull_request', 'branch', 'source', 'session', 'pinned')),
    CONSTRAINT preview_groups_status_check
        CHECK (current_status IN ('none', 'target_created', 'starting', 'ready', 'partially_ready',
                                  'unhealthy', 'stopped', 'failed', 'expired', 'unavailable',
                                  'warm', 'recycling', 'blocked', 'capacity_blocked',
                                  'config_invalid', 'outdated')),
    CONSTRAINT preview_groups_branch_not_blank
        CHECK (group_kind = 'session' OR length(trim(branch)) > 0)
);

CREATE UNIQUE INDEX idx_preview_groups_identity
    ON preview_groups (
        org_id,
        repository_id,
        group_kind,
        branch,
        preview_config_name,
        COALESCE(pull_request_number, 0),
        source_type,
        source_id,
        pinned
    );

CREATE INDEX idx_preview_groups_org_status_activity
    ON preview_groups (org_id, current_status, last_activity_at DESC, id DESC);

CREATE INDEX idx_preview_groups_org_activity
    ON preview_groups (org_id, last_activity_at DESC, id DESC);

ALTER TABLE preview_targets
    ADD COLUMN preview_group_id UUID REFERENCES preview_groups(id) ON DELETE SET NULL;

CREATE INDEX idx_preview_targets_group_created
    ON preview_targets (org_id, preview_group_id, created_at DESC, id DESC);
