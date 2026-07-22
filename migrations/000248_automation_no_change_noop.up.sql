WITH target_sessions AS MATERIALIZED (
    SELECT
        ar.id AS automation_run_id,
        ar.org_id,
        s.id AS session_id
    FROM session_publish_state sps
    JOIN session_automation_links sal
      ON sal.session_id = sps.session_id
     AND sal.org_id = sps.org_id
    JOIN automation_runs ar
      ON ar.id = sal.automation_run_id
     AND ar.org_id = sal.org_id
    JOIN sessions s
      ON s.id = sal.session_id
     AND s.org_id = sal.org_id
    WHERE ar.status = 'completed'
      AND s.deleted_at IS NULL
      AND COALESCE(s.diff, '') = ''
      AND sps.pr_creation_state = 'failed'
      AND sps.pr_creation_error = 'No changes to push.'
      AND NOT EXISTS (
          SELECT 1
          FROM pull_requests pr
          WHERE pr.org_id = s.org_id
            AND pr.session_id = s.id
      )
),
updated_runs AS (
    UPDATE automation_runs ar
    SET status = 'completed_noop',
        updated_at = now()
    FROM target_sessions target
    WHERE ar.id = target.automation_run_id
      AND ar.org_id = target.org_id
      AND ar.status = 'completed'
    RETURNING ar.id, ar.org_id
)
UPDATE session_publish_state sps
SET pr_creation_state = 'idle',
    pr_creation_error = NULL,
    updated_at = now()
FROM target_sessions target
JOIN updated_runs updated
  ON updated.id = target.automation_run_id
 AND updated.org_id = target.org_id
WHERE sps.session_id = target.session_id
  AND sps.org_id = target.org_id
  AND sps.pr_creation_state = 'failed'
  AND sps.pr_creation_error = 'No changes to push.';

-- Migration 000238's session_publish_state update trigger mirrors this reset
-- to the primary session_changesets row, keeping both publish-state surfaces
-- consistent without updating the same logical state twice in this statement.
