-- session_views tracks when each user last viewed a session (for unread indicators).
CREATE TABLE IF NOT EXISTS session_views (
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    org_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    last_viewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, session_id)
);

CREATE INDEX idx_session_views_user_org ON session_views (user_id, org_id);

-- Add CI status tracking to pull requests.
ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS ci_status TEXT NOT NULL DEFAULT '';

-- Index for BatchGetBySessionIDs queries (session list enrichment).
CREATE INDEX IF NOT EXISTS idx_pull_requests_session_org ON pull_requests (org_id, session_id) WHERE session_id IS NOT NULL;
