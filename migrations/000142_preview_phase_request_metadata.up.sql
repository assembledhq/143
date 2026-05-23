ALTER TABLE preview_targets
    ADD COLUMN request_id TEXT NOT NULL DEFAULT '';

ALTER TABLE preview_instances
    ADD COLUMN current_phase TEXT NOT NULL DEFAULT '',
    ADD COLUMN request_id TEXT NOT NULL DEFAULT '';
