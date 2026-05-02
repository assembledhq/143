-- Track the state of the "Push changes" follow-up action for sessions that
-- already have an open PR. Independent from pr_creation_state so a session can
-- show "PR opened" while a follow-up push is mid-flight without the two state
-- machines interfering.
ALTER TABLE sessions
    ADD COLUMN pr_push_state text NOT NULL DEFAULT 'idle',
    ADD COLUMN pr_push_error text;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_pr_push_state CHECK (pr_push_state IN (
        'idle', 'queued', 'pushing', 'succeeded', 'failed'
    )) NOT VALID;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_pr_push_state;

-- Persist the PR's head branch name so subsequent pushes target the same ref
-- the original CreatePR landed on, even if the session's title or Linear key
-- changes (which would shift formatBranchName's deterministic output and
-- otherwise create a divergent branch on GitHub that the PR doesn't track).
-- Nullable because rows created before this migration didn't capture it; the
-- push code falls back to recomputing via formatBranchName when NULL.
ALTER TABLE pull_requests
    ADD COLUMN head_ref text;
