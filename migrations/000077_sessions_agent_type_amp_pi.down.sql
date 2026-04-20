-- Roll back to the pre-migration-77 allowlist (claude_code/gemini_cli/codex/pm_agent).
-- Will fail if any sessions row currently has agent_type='amp' or 'pi'; operators
-- should purge or re-assign those rows before applying the down migration.

ALTER TABLE sessions DROP CONSTRAINT IF EXISTS chk_sessions_agent_type;
ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_agent_type CHECK (agent_type IN (
        'claude_code', 'gemini_cli', 'codex', 'pm_agent'
    )) NOT VALID;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_agent_type;
