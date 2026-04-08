-- Restore NOT NULL with default and strict check constraint.
UPDATE issues SET severity = 'medium' WHERE severity = '' OR severity IS NULL;

ALTER TABLE issues ALTER COLUMN severity SET NOT NULL;
ALTER TABLE issues ALTER COLUMN severity SET DEFAULT 'medium';

ALTER TABLE issues DROP CONSTRAINT IF EXISTS chk_issues_severity;
ALTER TABLE issues
    ADD CONSTRAINT chk_issues_severity CHECK (severity IN (
        'critical', 'high', 'medium', 'low'
    )) NOT VALID;
ALTER TABLE issues VALIDATE CONSTRAINT chk_issues_severity;
