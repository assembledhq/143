-- session_publications.initiated_by_user_id records a global user identity,
-- while users.org_id is only that user's legacy/default organization. Retarget
-- the tenant-scope FK to the authoritative multi-org membership pair so users
-- can publish sessions from any organization they belong to.
--
-- The replacement FK is installed NOT VALID to keep the catalog-locking step
-- brief. Migration 000279 validates existing rows separately. If a membership
-- is later removed, preserve the publication record but clear its attribution;
-- org_id remains intact and continues to scope the publication.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_publications
    DROP CONSTRAINT IF EXISTS session_publications_initiator_scope_fkey;

ALTER TABLE session_publications
    ADD CONSTRAINT session_publications_initiator_scope_fkey
        FOREIGN KEY (initiated_by_user_id, org_id)
        REFERENCES organization_memberships(user_id, org_id)
        ON DELETE SET NULL (initiated_by_user_id)
        NOT VALID;
