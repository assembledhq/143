ALTER TABLE code_review_session_metadata
    ADD COLUMN size_limit_override_applied boolean NOT NULL DEFAULT false;
