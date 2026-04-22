ALTER TABLE sessions
    ADD COLUMN base_commit_sha text,
    ADD COLUMN diff_collected_at timestamptz,
    ADD COLUMN latest_diff_snapshot_id uuid;

CREATE TABLE session_diff_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations(id),
    turn_number integer NOT NULL DEFAULT 0,
    sequence_number integer NOT NULL DEFAULT 1,
    source text NOT NULL,
    base_commit_sha text NOT NULL,
    head_commit_sha text,
    working_branch text,
    target_branch text,
    diff text NOT NULL,
    files_changed integer NOT NULL DEFAULT 0,
    lines_added integer NOT NULL DEFAULT 0,
    lines_removed integer NOT NULL DEFAULT 0,
    captured_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE sessions
    ADD CONSTRAINT fk_sessions_latest_diff_snapshot
    FOREIGN KEY (latest_diff_snapshot_id) REFERENCES session_diff_snapshots(id) ON DELETE SET NULL;

CREATE INDEX idx_session_diff_snapshots_session_captured_at
    ON session_diff_snapshots (org_id, session_id, captured_at DESC);
