ALTER TABLE users
    ADD COLUMN last_org_id uuid REFERENCES organizations(id) ON DELETE SET NULL;

WITH ranked AS (
    SELECT
        user_id,
        last_org_id,
        ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY created_at DESC, id DESC) AS rn
    FROM auth_sessions
    WHERE last_org_id IS NOT NULL
)
UPDATE users u
SET last_org_id = ranked.last_org_id
FROM ranked
WHERE ranked.user_id = u.id
  AND ranked.rn = 1;
