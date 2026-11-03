ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT code_review_session_metadata_phase_check,
    ADD CONSTRAINT code_review_session_metadata_phase_check CHECK (
        phase IS NULL OR phase IN (
            'preparing_workspace',
            'syncing_github',
            'waiting_for_github',
            'reviewing',
            'synthesizing',
            'publishing'
        )
    );
