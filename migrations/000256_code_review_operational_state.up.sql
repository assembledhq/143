ALTER TABLE code_review_session_metadata
    ADD COLUMN phase text,
    ADD COLUMN status_code text,
    ADD COLUMN status_message text,
    ADD COLUMN retry_at timestamptz,
    ADD COLUMN last_error_at timestamptz,
    ADD COLUMN retryable_failure boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT code_review_session_metadata_phase_check CHECK (
        phase IS NULL OR phase IN (
            'syncing_github',
            'waiting_for_github',
            'reviewing',
            'synthesizing',
            'publishing'
        )
    ),
    ADD CONSTRAINT code_review_session_metadata_status_code_check CHECK (
        status_code IS NULL OR status_code IN (
            'github_rate_limited',
            'github_unavailable',
            'reviewer_failed',
            'worker_failed'
        )
    );

UPDATE code_review_session_metadata
SET phase = 'syncing_github'
WHERE status IN ('queued', 'running');
