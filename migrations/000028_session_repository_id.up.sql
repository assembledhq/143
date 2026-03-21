ALTER TABLE sessions ADD COLUMN repository_id UUID REFERENCES repositories(id);

-- Backfill existing sessions from their linked issues.
UPDATE sessions s
SET repository_id = i.repository_id
FROM issues i
WHERE s.issue_id = i.id
  AND i.repository_id IS NOT NULL
  AND s.repository_id IS NULL;
