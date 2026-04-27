-- Enforce one deploy row per (pull_request_id, environment) so the merge API
-- path and the GitHub `pull_request closed` webhook cannot race to insert
-- duplicate deploys. The check-then-INSERT in runMergedPullRequestFollowUps
-- pairs with this constraint plus an ON CONFLICT clause for idempotency.
--
-- Pre-PR, deploys.Create was called unconditionally on every merge webhook,
-- so a PR that was merged → reopened → re-merged could have produced
-- duplicate rows. Collapse any pre-existing duplicates to the most recent
-- row per group so the unique index can be created without error.
DELETE FROM deploys
WHERE id IN (
    SELECT id
    FROM (
        SELECT id, row_number() OVER (
            PARTITION BY pull_request_id, environment
            ORDER BY deployed_at DESC, created_at DESC, id DESC
        ) AS rn
        FROM deploys
    ) ranked
    WHERE rn > 1
);

CREATE UNIQUE INDEX idx_deploys_pr_environment ON deploys (pull_request_id, environment);
