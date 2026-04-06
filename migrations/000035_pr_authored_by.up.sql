-- Track which token was used to create the PR: 'user' (personal GitHub token)
-- or 'app' (GitHub App installation token).
ALTER TABLE pull_requests ADD COLUMN authored_by text NOT NULL DEFAULT 'app' CHECK (authored_by IN ('user', 'app'));
