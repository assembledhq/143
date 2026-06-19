ALTER TABLE pr_preview_state
    DROP COLUMN IF EXISTS last_surface_sync_error,
    DROP COLUMN IF EXISTS last_surface_sync_at,
    DROP COLUMN IF EXISTS last_surface_sync_sha;

ALTER TABLE repository_preview_policies
    DROP COLUMN IF EXISTS github_commit_status_enabled,
    DROP COLUMN IF EXISTS github_pr_comment_enabled,
    DROP COLUMN IF EXISTS pr_preview_surfaces_enabled;
