ALTER TABLE org_credentials ADD COLUMN priority integer;

WITH ranked AS (
    SELECT
        id,
        ROW_NUMBER() OVER (
            PARTITION BY org_id
            ORDER BY
                CASE
                    WHEN provider = 'openai_chatgpt' THEN 1
                    WHEN provider = 'openai' THEN 2
                    WHEN provider = 'anthropic' THEN 3
                    ELSE 100
                END,
                created_at,
                id
        ) AS new_priority
    FROM org_credentials
)
UPDATE org_credentials AS c
SET priority = ranked.new_priority
FROM ranked
WHERE c.id = ranked.id;

ALTER TABLE org_credentials ALTER COLUMN priority SET NOT NULL;
ALTER TABLE org_credentials ALTER COLUMN priority SET DEFAULT 1000;

CREATE INDEX idx_org_credentials_priority
    ON org_credentials (org_id, priority, created_at)
    WHERE status != 'disabled';
