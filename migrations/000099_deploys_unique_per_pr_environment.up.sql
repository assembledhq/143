-- Enforce one deploy row per (pull_request_id, environment) so the merge API
-- path and the GitHub `pull_request closed` webhook cannot race to insert
-- duplicate deploys. The check-then-INSERT in runMergedPullRequestFollowUps
-- pairs with this constraint plus an ON CONFLICT clause for idempotency.
CREATE UNIQUE INDEX idx_deploys_pr_environment ON deploys (pull_request_id, environment);
