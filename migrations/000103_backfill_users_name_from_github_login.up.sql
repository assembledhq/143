-- Backfill users.name with the GitHub login for accounts whose name was
-- persisted as an empty string during OAuth signup/login. GitHub's /user
-- API returns name:"" for users who haven't set a public display name on
-- their profile, and prior to this change we wrote that empty value
-- through to the DB — which then surfaced as "Unknown user" anywhere the
-- frontend rendered users.name (session attribution, audit logs, etc.).
--
-- The auth callback now substitutes the login at write time (see
-- internal/api/handlers/auth.go), so this is a one-shot heal for rows
-- that predate that fix. Idempotent: re-running selects nothing.
UPDATE users
SET name = github_login
WHERE (name IS NULL OR name = '')
  AND github_login IS NOT NULL
  AND github_login <> '';
