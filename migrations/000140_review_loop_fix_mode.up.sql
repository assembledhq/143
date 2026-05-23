ALTER TABLE session_review_loops
    ADD COLUMN fix_mode TEXT NOT NULL DEFAULT 'minimal';

ALTER TABLE session_review_loops
    ADD CONSTRAINT chk_session_review_loops_fix_mode CHECK (fix_mode IN ('minimal', 'exhaustive'));
