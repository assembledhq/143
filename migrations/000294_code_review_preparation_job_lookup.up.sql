-- Preparation polls need the first enqueue time and latest status even after
-- a job is terminal, so the existing in-flight-only dedupe index cannot serve
-- these lookups. Build without blocking writes to the hot jobs table.
-- Deliberately omit IF NOT EXISTS: an interrupted concurrent build may leave
-- an INVALID index with this name. Retrying must fail visibly until an
-- operator drops that invalid index and repairs the dirty migration marker.
CREATE INDEX CONCURRENTLY idx_jobs_org_queue_dedupe_created
    ON jobs (org_id, queue, dedupe_key, created_at, id)
    WHERE dedupe_key IS NOT NULL;
