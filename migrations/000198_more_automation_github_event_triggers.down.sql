UPDATE automations
    SET github_event_triggers = array_remove(
        array_remove(
            array_remove(
                array_remove(github_event_triggers, 'github.pull_request.updated'),
                'github.check_run.completed'
            ),
            'github.pull_request.merged'
        ),
        'github.check_suite.completed'
    )
    WHERE github_event_triggers && ARRAY[
        'github.pull_request.updated',
        'github.pull_request.merged',
        'github.check_suite.completed',
        'github.check_run.completed'
    ]::text[];

ALTER TABLE automations
    DROP CONSTRAINT chk_automations_github_event_triggers;

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_github_event_triggers CHECK (
        github_event_triggers <@ ARRAY[
            'github.pull_request.opened',
            'github.issue_comment.created',
            'github.pull_request_review.submitted',
            'github.pull_request_review_comment.created'
        ]::text[]
    );
