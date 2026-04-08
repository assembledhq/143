-- Allow issues to have no severity (empty string or NULL).
-- Manual issues and other sources may not have a meaningful severity at creation time.

-- Drop the existing constraint and re-add with '' allowed.
ALTER TABLE issues DROP CONSTRAINT IF EXISTS chk_issues_severity;
ALTER TABLE issues
    ADD CONSTRAINT chk_issues_severity CHECK (severity IN (
        'critical', 'high', 'medium', 'low', ''
    )) NOT VALID;
ALTER TABLE issues VALIDATE CONSTRAINT chk_issues_severity;

-- Allow NULL and remove the default so callers explicitly choose a value (or omit it).
ALTER TABLE issues ALTER COLUMN severity DROP NOT NULL;
ALTER TABLE issues ALTER COLUMN severity DROP DEFAULT;
