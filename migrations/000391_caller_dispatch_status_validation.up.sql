-- Validation is separate from the initial ACCESS EXCLUSIVE constraint install.
SET LOCAL lock_timeout = '5s';
ALTER TABLE code_review_session_metadata
    VALIDATE CONSTRAINT code_review_session_metadata_status_code_v2_check;
