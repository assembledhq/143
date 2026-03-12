ALTER TABLE auth_sessions RENAME TO sessions;
ALTER INDEX idx_auth_sessions_token RENAME TO idx_sessions_token;
ALTER INDEX idx_auth_sessions_expires_at RENAME TO idx_sessions_expires_at;
