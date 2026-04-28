-- Reverse the data copy by deleting every row from coding_credentials. The
-- legacy org_credentials and user_credentials rows are still intact, so this
-- restores the pre-copy state. Pre-MVP — for production rollback we would
-- snapshot first; this is enough for local dev.
DELETE FROM coding_credentials;
DELETE FROM coding_credentials_migrations WHERE name = 'anthropic_split';
