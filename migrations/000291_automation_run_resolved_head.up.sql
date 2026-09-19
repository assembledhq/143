-- Per-target continuity: the head a run resolved to at dispatch (design doc
-- 125, "Head Authority"). Additive. The delivered head stays in
-- config_snapshot as audit; resolved_head_sha is the head the run reviews
-- once dispatch-time resolution adopted a newer one, so a degraded retry
-- (lookup unavailable) reviews the same head under the same epoch.

ALTER TABLE automation_runs
    ADD COLUMN resolved_head_sha text;
