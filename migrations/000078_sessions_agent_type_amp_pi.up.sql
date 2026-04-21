-- Extend sessions.agent_type CHECK constraint to allow the new 'amp' and 'pi'
-- adapters. Without this, INSERT/UPDATE for Sourcegraph Amp or Pi sessions
-- fails at the DB even though Go-side validation accepts them.
--
-- Pattern mirrors migration 000035 (NOT VALID + VALIDATE) so the ALTER does
-- not block concurrent writes on large session tables; validation runs as a
-- separate statement.

ALTER TABLE sessions DROP CONSTRAINT IF EXISTS chk_sessions_agent_type;
ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_agent_type CHECK (agent_type IN (
        'claude_code', 'gemini_cli', 'codex', 'amp', 'pi', 'pm_agent'
    )) NOT VALID;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_agent_type;
