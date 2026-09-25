ALTER TABLE code_review_session_metadata DROP CONSTRAINT code_review_session_metadata_status_code_check;
ALTER TABLE code_review_session_metadata ADD CONSTRAINT code_review_session_metadata_status_code_check
CHECK (status_code IS NULL OR status_code IN ('github_rate_limited','github_unavailable','reviewer_failed','worker_failed','review_loop_detected'));
