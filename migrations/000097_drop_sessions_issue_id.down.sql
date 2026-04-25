ALTER TABLE sessions ADD COLUMN issue_id uuid REFERENCES issues(id);

UPDATE sessions s
SET issue_id = l.issue_id
FROM session_issue_links l
WHERE l.session_id = s.id
  AND l.org_id = s.org_id
  AND l.role = 'primary';

CREATE INDEX IF NOT EXISTS idx_sessions_issue ON sessions (org_id, issue_id);
