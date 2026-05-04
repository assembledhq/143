-- Reverse the data copy by deleting only those coding_credentials rows that
-- the up migration created. Two row classes match: rows whose id matches a
-- legacy org_credentials.id or user_credentials.id (steps 1 & 2), and the
-- fresh-uuid team-default rows minted by step 3 (identifiable by the
-- team_default_origin_user_id marker column).
--
-- Refuses to run if coding_credentials contains any other rows: those would
-- have been created via the new `/api/v1/coding-credentials` endpoints after
-- 000111 was applied, and a blanket DELETE would silently drop user data.
-- Operators rolling back after live traffic must snapshot coding_credentials
-- (or hand-roll a partial cleanup) before retrying.
--
-- Also refuses to run if the Anthropic split post-step has executed: the
-- forward `migrate-coding-credentials-anthropic-split` rewrites a row with
-- both APIKey and Subscription set to drop the API key (the design splits
-- each method into its own row). That drop is not recoverable from this
-- migration — the original encrypted blob is gone and the legacy row in
-- org_credentials/user_credentials may have been mutated since. Operators
-- rolling back after the split has run MUST restore from a backup taken
-- before the split, then re-run this down migration on the restored DB.
--
-- The marker column is checked instead of a label LIKE pattern so a
-- user-supplied label that happens to look like the migration's
-- 'Team default (migrated from <uuid>)' string cannot fool the orphan
-- detector into deleting live data.
DO $$
DECLARE
    orphan_count    integer;
    split_marker    integer;
    subscription_ct integer;
BEGIN
    SELECT count(*) INTO split_marker
    FROM coding_credentials_migrations WHERE name = 'anthropic_split';
    SELECT count(*) INTO subscription_ct
    FROM coding_credentials WHERE provider = 'anthropic_subscription';
    IF split_marker > 0 OR subscription_ct > 0 THEN
        RAISE EXCEPTION 'anthropic_split post-step has run (sentinel=% subscription_rows=%); rolling back would silently lose any APIKey dropped from dual-set anthropic rows. Restore from a pre-split backup and re-run this migration there.',
            split_marker, subscription_ct;
    END IF;

    SELECT count(*) INTO orphan_count
    FROM coding_credentials cc
    WHERE NOT EXISTS (SELECT 1 FROM org_credentials  oc WHERE oc.id = cc.id)
      AND NOT EXISTS (SELECT 1 FROM user_credentials uc WHERE uc.id = cc.id)
      AND cc.team_default_origin_user_id IS NULL;
    IF orphan_count > 0 THEN
        RAISE EXCEPTION 'coding_credentials has % row(s) without a legacy counterpart; refusing to roll back to avoid data loss. Snapshot the table or remove the rows manually before retrying.', orphan_count;
    END IF;
END $$;

DELETE FROM coding_credentials cc
 WHERE EXISTS (SELECT 1 FROM org_credentials  oc WHERE oc.id = cc.id)
    OR EXISTS (SELECT 1 FROM user_credentials uc WHERE uc.id = cc.id)
    OR cc.team_default_origin_user_id IS NOT NULL;

DELETE FROM coding_credentials_migrations WHERE name = 'anthropic_split';
