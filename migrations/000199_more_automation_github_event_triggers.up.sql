ALTER TABLE automations
    DROP CONSTRAINT chk_automations_github_event_triggers;

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_github_event_triggers CHECK (
        github_event_triggers <@ ARRAY[
            'github.pull_request.opened',
            'github.pull_request.updated',
            'github.pull_request.merged',
            'github.check_suite.completed',
            'github.check_run.completed',
            'github.issue_comment.created',
            'github.pull_request_review.submitted',
            'github.pull_request_review_comment.created'
        ]::text[]
    );
