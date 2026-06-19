ALTER TABLE repository_preview_policies
    ADD COLUMN pr_preview_surfaces_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN github_pr_comment_enabled boolean NOT NULL DEFAULT true,
    ADD COLUMN github_commit_status_enabled boolean NOT NULL DEFAULT true;

ALTER TABLE pr_preview_state
    ADD COLUMN last_surface_sync_sha text NOT NULL DEFAULT '',
    ADD COLUMN last_surface_sync_at timestamptz,
    ADD COLUMN last_surface_sync_error text NOT NULL DEFAULT '';
