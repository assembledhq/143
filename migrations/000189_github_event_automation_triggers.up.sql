ALTER TABLE automations
    ADD COLUMN github_event_triggers text[] NOT NULL DEFAULT '{}'::text[];

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_github_event_triggers CHECK (
        github_event_triggers <@ ARRAY[
            'github.pull_request.opened',
            'github.issue_comment.created',
            'github.pull_request_review.submitted',
            'github.pull_request_review_comment.created'
        ]::text[]
    );

ALTER TABLE automation_runs
    DROP CONSTRAINT IF EXISTS chk_automation_runs_triggered_by;

ALTER TABLE automation_runs
    ADD CONSTRAINT chk_automation_runs_triggered_by CHECK (triggered_by IN ('schedule', 'manual', 'github'));

CREATE INDEX idx_automations_github_event_triggers
    ON automations USING gin (github_event_triggers)
    WHERE enabled = true AND deleted_at IS NULL;
