-- Preserve one preparation budget across workspace generations without
-- scanning or sorting every agent job for the organization. Build outside a
-- transaction so the hot jobs table remains writable throughout the build.
-- An interrupted concurrent build requires removal of its invalid index
-- before retrying; do not mask that state with IF NOT EXISTS.
CREATE INDEX CONCURRENTLY idx_jobs_code_review_initial_preparation
    ON jobs (org_id, (payload->>'metadata_id'), (payload->>'session_id'), created_at, id)
    WHERE queue = 'agent' AND job_type = 'prepare_code_review_workspace';
