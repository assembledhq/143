-- Rollback of multi-org membership infrastructure.
--
-- DATA LOSS WARNING: organization_memberships is the authoritative source of
-- user→org access in the multi-org world. Any second-org membership created
-- after the up migration shipped (invite accepts, manual grants) lives ONLY
-- in this table — `users.org_id` still reflects the user's legacy primary
-- org. Dropping the table deletes those memberships irreversibly.
--
-- To guard against an accidental rollback that silently orphans users, this
-- migration aborts if it sees any membership that is NOT mirrored in
-- `users.org_id` / `users.role` — i.e. any multi-org membership or any
-- membership whose role diverged from the legacy column. Set the GUC
-- `app.force_destructive_rollback = 'true'` (via `SET LOCAL`) before running
-- the down migration to acknowledge the data loss and proceed anyway.
SET LOCAL lock_timeout = '30s';

DO $$
DECLARE
    divergent_count integer;
    force_flag text;
BEGIN
    -- current_setting with missing_ok=true returns NULL when the GUC is unset
    -- rather than raising — keeps the check a simple string compare.
    force_flag := coalesce(current_setting('app.force_destructive_rollback', true), '');

    SELECT COUNT(*) INTO divergent_count
    FROM organization_memberships m
    LEFT JOIN users u ON u.id = m.user_id
    WHERE u.id IS NULL
       OR u.org_id IS DISTINCT FROM m.org_id
       OR u.role   IS DISTINCT FROM m.role;

    IF divergent_count > 0 AND force_flag <> 'true' THEN
        RAISE EXCEPTION
            'refusing to drop organization_memberships: % row(s) would be lost '
            '(memberships not mirrored by users.org_id / users.role). '
            'SET LOCAL app.force_destructive_rollback = ''true'' to override.',
            divergent_count;
    END IF;
END $$;

ALTER TABLE auth_sessions DROP COLUMN IF EXISTS last_org_id;
DROP TABLE IF EXISTS organization_memberships;
