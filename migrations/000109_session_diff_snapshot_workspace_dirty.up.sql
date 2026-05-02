ALTER TABLE session_diff_snapshots
    ADD COLUMN workspace_dirty boolean NOT NULL DEFAULT false;
