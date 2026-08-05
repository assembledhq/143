-- Validation takes SHARE UPDATE EXCLUSIVE rather than the ACCESS EXCLUSIVE lock
-- used to add the constraint. Keeping it in a separate migration lets the hot
-- pull_requests table continue serving ordinary reads and writes during its scan.
SET LOCAL lock_timeout = '30s';

ALTER TABLE pull_requests
    VALIDATE CONSTRAINT chk_pr_code_review_dispute_cycles;
