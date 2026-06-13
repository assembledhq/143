-- Restore the pre-000186 sessions.agent_type set.

ALTER TABLE sessions DROP CONSTRAINT IF EXISTS chk_sessions_agent_type;
ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_agent_type CHECK (agent_type IN (
        'claude_code', 'gemini_cli', 'codex', 'amp', 'pi', 'opencode', 'pm_agent'
    )) NOT VALID;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_agent_type;
