-- Track which token was used to create the PR: 'user' (personal GitHub token)
-- or 'app' (GitHub App installation token).
ALTER TABLE pull_requests ADD COLUMN authored_by text NOT NULL DEFAULT 'app';

ALTER TABLE pull_requests
    ADD CONSTRAINT chk_pull_requests_authored_by CHECK (authored_by IN ('user', 'app')) NOT VALID;
ALTER TABLE pull_requests VALIDATE CONSTRAINT chk_pull_requests_authored_by;
