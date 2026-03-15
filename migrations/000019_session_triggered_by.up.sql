ALTER TABLE sessions ADD COLUMN triggered_by_user_id uuid REFERENCES users(id);
CREATE INDEX idx_sessions_triggered_by ON sessions(triggered_by_user_id);
