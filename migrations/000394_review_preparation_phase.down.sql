UPDATE code_review_session_metadata
SET phase = 'syncing_github'
WHERE phase = 'preparing_workspace';

ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT code_review_session_metadata_phase_check,
    ADD CONSTRAINT code_review_session_metadata_phase_check CHECK (
        phase IS NULL OR phase IN (
            'syncing_github',
            'waiting_for_github',
            'reviewing',
            'synthesizing',
            'publishing'
        )
    );
