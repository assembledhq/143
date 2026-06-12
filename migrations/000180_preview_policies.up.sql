CREATE TABLE repository_preview_policies (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id      UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    auto_mode          TEXT        NOT NULL DEFAULT 'off',
    updated_by_user_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT repository_preview_policies_auto_mode_check
        CHECK (auto_mode IN ('off', 'warm', 'on'))
);

CREATE UNIQUE INDEX idx_repository_preview_policies_repo
    ON repository_preview_policies (org_id, repository_id);

ALTER TABLE preview_targets
    ADD COLUMN last_snapshot_key TEXT NOT NULL DEFAULT '';

ALTER TABLE preview_instances
    ADD COLUMN stopped_reason TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT preview_instances_stopped_reason_check
        CHECK (stopped_reason IN ('', 'user', 'expired', 'warm_policy',
                                  'pr_closed', 'drain', 'error'));

CREATE INDEX idx_preview_instances_org_terminal
    ON preview_instances (org_id, created_at DESC)
    WHERE status IN ('stopped', 'expired', 'failed', 'unavailable');
