-- Add multi-provider auth columns to users table.
ALTER TABLE users ADD COLUMN password_hash text;
ALTER TABLE users ADD COLUMN google_id text;

-- Partial unique index for Google ID lookups (same pattern as github_id).
CREATE UNIQUE INDEX idx_users_google_id ON users (google_id) WHERE google_id IS NOT NULL;
