-- Request age and an actual provider failure episode have independent clocks.
ALTER TABLE code_review_requests ADD COLUMN capture_failure_started_at timestamptz;
-- Existing requests have ambiguous historical capture state. Preserve their
-- old effective deadline until an actual successful capture proves recovery.
ALTER TABLE code_review_requests ADD COLUMN capture_failure_clock_tracked boolean NOT NULL DEFAULT false;
ALTER TABLE code_review_requests ALTER COLUMN capture_failure_clock_tracked SET DEFAULT true;
