ALTER TABLE pull_request_repair_runs
    ADD COLUMN head_sha text,
    ADD COLUMN base_sha text;

UPDATE pull_request_repair_runs AS repair
SET head_sha = snapshot.head_sha,
    base_sha = snapshot.base_sha
FROM pull_request_health_snapshots AS snapshot
WHERE snapshot.org_id = repair.org_id
  AND snapshot.pull_request_id = repair.pull_request_id
  AND snapshot.version = repair.health_version
  AND (repair.head_sha IS NULL OR repair.base_sha IS NULL);

CREATE UNIQUE INDEX idx_pull_request_repair_runs_active_head
    ON pull_request_repair_runs (pull_request_id, action_type, head_sha)
    WHERE active = true AND head_sha IS NOT NULL;
