-- Drop 'opencode' from the sessions.agent_type allowlist, restoring the
-- pre-migration-185 set. This rollback fails with an explicit message if any
-- persisted OpenCode sessions remain.

DO $$
DECLARE
    leftover_count bigint;
BEGIN
    SELECT count(*) INTO leftover_count
      FROM sessions
     WHERE agent_type = 'opencode';
    IF leftover_count > 0 THEN
        RAISE EXCEPTION
            'cannot roll back migration 000185: % session row(s) still reference agent_type = ''opencode''. Delete, archive, or reassign them before re-running the down migration.',
            leftover_count;
    END IF;
END$$;

ALTER TABLE sessions DROP CONSTRAINT IF EXISTS chk_sessions_agent_type;
ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_agent_type CHECK (agent_type IN (
        'claude_code', 'gemini_cli', 'codex', 'amp', 'pi', 'pm_agent'
    )) NOT VALID;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_agent_type;
