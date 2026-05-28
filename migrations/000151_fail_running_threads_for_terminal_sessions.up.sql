UPDATE session_threads AS t
SET status = 'failed',
    completed_at = COALESCE(t.completed_at, now()),
    failure_explanation = COALESCE(t.failure_explanation, 'Thread stopped because its parent session already reached a terminal status.'),
    failure_category = COALESCE(t.failure_category, 'stuck_thread')
FROM sessions AS s
WHERE t.org_id = s.org_id
  AND t.session_id = s.id
  AND t.status = 'running'
  AND s.status IN ('completed', 'pr_created', 'failed', 'cancelled', 'skipped');
