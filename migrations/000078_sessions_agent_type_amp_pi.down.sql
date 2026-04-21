-- Roll back to the pre-migration-78 allowlist (claude_code/gemini_cli/codex/pm_agent).
--
-- This migration is destructive to any persisted Amp or Pi sessions and will
-- fail at VALIDATE CONSTRAINT below if any rows remain with
-- agent_type IN ('amp', 'pi'). Run the following playbook before applying:
--
--   1. Confirm the scope:
--        SELECT agent_type, count(*) FROM sessions
--         WHERE agent_type IN ('amp', 'pi')
--         GROUP BY agent_type;
--
--   2. Pick ONE of:
--        a) Archive + delete (recommended when Amp/Pi are being sunset):
--             DELETE FROM sessions WHERE agent_type IN ('amp', 'pi');
--           Note: cascades to session_logs / session_messages / session_threads
--           via ON DELETE CASCADE; operators who want an audit trail should
--           export the rows to cold storage first.
--        b) Reassign to a still-supported agent (for in-flight sessions only):
--             UPDATE sessions SET agent_type = 'claude_code'
--              WHERE agent_type IN ('amp', 'pi') AND status IN ('running','idle','pending');
--           Historical/completed rows should not be rewritten — prefer (a).
--
--   3. Re-run this migration. The ADD CONSTRAINT ... NOT VALID step always
--      succeeds; the VALIDATE step is what enforces the invariant.

ALTER TABLE sessions DROP CONSTRAINT IF EXISTS chk_sessions_agent_type;
ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_agent_type CHECK (agent_type IN (
        'claude_code', 'gemini_cli', 'codex', 'pm_agent'
    )) NOT VALID;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_agent_type;
