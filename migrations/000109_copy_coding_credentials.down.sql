-- Reverse the data copy by deleting only those coding_credentials rows that
-- the up migration created. Two row classes match: rows whose id matches a
-- legacy org_credentials.id or user_credentials.id (steps 1 & 2), and the
-- fresh-uuid team-default rows minted by step 3 (identifiable by the
-- deterministic 'Team default (migrated from <uuid>)' label).
--
-- Refuses to run if coding_credentials contains any other rows: those would
-- have been created via the new `/api/v1/coding-credentials` endpoints after
-- 000109 was applied, and a blanket DELETE would silently drop user data.
-- Operators rolling back after live traffic must snapshot coding_credentials
-- (or hand-roll a partial cleanup) before retrying.
DO $$
DECLARE
    orphan_count integer;
BEGIN
    SELECT count(*) INTO orphan_count
    FROM coding_credentials cc
    WHERE NOT EXISTS (SELECT 1 FROM org_credentials  oc WHERE oc.id = cc.id)
      AND NOT EXISTS (SELECT 1 FROM user_credentials uc WHERE uc.id = cc.id)
      AND NOT (cc.user_id IS NULL AND cc.label LIKE 'Team default (migrated from %)');
    IF orphan_count > 0 THEN
        RAISE EXCEPTION 'coding_credentials has % row(s) without a legacy counterpart; refusing to roll back to avoid data loss. Snapshot the table or remove the rows manually before retrying.', orphan_count;
    END IF;
END $$;

DELETE FROM coding_credentials cc
 WHERE EXISTS (SELECT 1 FROM org_credentials  oc WHERE oc.id = cc.id)
    OR EXISTS (SELECT 1 FROM user_credentials uc WHERE uc.id = cc.id)
    OR (cc.user_id IS NULL AND cc.label LIKE 'Team default (migrated from %)');

DELETE FROM coding_credentials_migrations WHERE name = 'anthropic_split';
