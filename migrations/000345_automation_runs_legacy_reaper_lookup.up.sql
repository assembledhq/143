-- The scheduler discovers abandoned pre-target runs across all organizations.
-- Keep the build online because automation_runs is written by live workers.
CREATE INDEX CONCURRENTLY idx_automation_runs_legacy_reaper_lookup
    ON automation_runs (triggered_at, org_id)
    WHERE dispatch_state IS NULL AND status IN ('pending', 'running');
