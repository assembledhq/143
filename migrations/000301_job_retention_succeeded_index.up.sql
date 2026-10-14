-- Build the ordered valid terminal-status retention index before enabling succeeded
-- deletion. A standalone concurrent build avoids blocking writes to hot jobs.
-- No IF NOT EXISTS: an interrupted build can leave an invalid index; retry
-- must fail until an operator removes it and repairs the dirty marker.
CREATE INDEX CONCURRENTLY idx_jobs_retention_succeeded
    ON jobs (updated_at, id)
    WHERE status IN ('succeeded', 'failed');
