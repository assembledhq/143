-- Adds the per-user "GitHub-attribution" email — the address GitHub uses to
-- link commits to the user's profile. Stored separately from users.email
-- (the human-facing contact address) so that contact email changes don't
-- silently break commit attribution and so we never expose the user's real
-- email to commit metadata. Populated during OAuth from /user/emails or
-- computed from the user_id+login fallback.
ALTER TABLE users
    ADD COLUMN github_noreply_email TEXT;

-- Backfill existing rows so the agent-push flow can attribute commits today
-- without waiting for users to re-authorize. The user-id-prefixed format is
-- the canonical noreply scheme (introduced August 2017) and never reveals
-- private email — at worst, GitHub falls back to the user's profile lookup.
UPDATE users
SET github_noreply_email = github_id::text || '+' || github_login || '@users.noreply.github.com'
WHERE github_id IS NOT NULL
  AND github_login IS NOT NULL
  AND github_noreply_email IS NULL;
