-- Rename auth sessions table to auth_sessions to free up "sessions" name
-- for the agent work sessions (currently agent_runs).
ALTER TABLE sessions RENAME TO auth_sessions;
ALTER INDEX idx_sessions_token RENAME TO idx_auth_sessions_token;
ALTER INDEX idx_sessions_expires_at RENAME TO idx_auth_sessions_expires_at;
