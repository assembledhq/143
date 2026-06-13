-- Remove the deprecated Gemini CLI adapter from active agent defaults and new
-- session writes. Historical sessions can still be read because the replacement
-- CHECK constraint is intentionally left NOT VALID.

WITH normalized AS (
    SELECT
        id,
        (
            CASE
                WHEN COALESCE(settings, '{}'::jsonb)->>'default_agent_type' = 'gemini_cli'
                    THEN jsonb_set(COALESCE(settings, '{}'::jsonb), '{default_agent_type}', '"codex"'::jsonb, true)
                ELSE COALESCE(settings, '{}'::jsonb)
            END
        ) #- '{agent_config,gemini_cli}' AS next_settings
    FROM organizations
    WHERE COALESCE(settings, '{}'::jsonb)->>'default_agent_type' = 'gemini_cli'
       OR COALESCE(settings, '{}'::jsonb)#>'{agent_config,gemini_cli}' IS NOT NULL
)
UPDATE organizations
SET settings = normalized.next_settings
FROM normalized
WHERE organizations.id = normalized.id;

UPDATE automations
SET agent_type = NULL,
    model_override = NULL,
    reasoning_effort = NULL
WHERE agent_type = 'gemini_cli';

ALTER TABLE sessions DROP CONSTRAINT IF EXISTS chk_sessions_agent_type;
ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_agent_type CHECK (agent_type IN (
        'claude_code', 'codex', 'amp', 'pi', 'opencode', 'pm_agent'
    )) NOT VALID;
