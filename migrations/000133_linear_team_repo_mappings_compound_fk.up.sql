-- Tighten cross-org safety on linear_team_repo_mappings.repository_id.
--
-- The original migration 000123 declared
--   repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE
-- which lets a future code path that bypasses LinearTeamRepoMappingStore
-- associate a repository from org A with a mapping row in org B. The store
-- defends against this at write time via `SELECT ... WHERE r.org_id =
-- @org_id`, but defense in depth at the DB level is cheap and catches the
-- "someone wrote raw SQL" failure mode.
--
-- Mechanic: add a composite UNIQUE (id, org_id) on repositories — the PK
-- already makes id unique, so the new unique adds no row-level rejection,
-- only an indexable shape Postgres can use as an FK target. Then drop the
-- old single-column FK and add a compound FK that requires (repository_id,
-- org_id) on the mapping to match (id, org_id) on repositories. From here
-- on, a row whose org_id disagrees with the referenced repo's org_id is a
-- hard constraint failure.
--
-- Locking: both ALTERs take ACCESS EXCLUSIVE briefly. The 5s lock_timeout
-- below caps the worst case; both tables are small in practice (one
-- mapping per (team, project) per org; one repository per github_id per
-- org) so the rebuild is millisecond-class.

SET LOCAL lock_timeout = '5s';

ALTER TABLE repositories
    ADD CONSTRAINT repositories_id_org_id_key UNIQUE (id, org_id);

ALTER TABLE linear_team_repo_mappings
    DROP CONSTRAINT linear_team_repo_mappings_repository_id_fkey;

ALTER TABLE linear_team_repo_mappings
    ADD CONSTRAINT linear_team_repo_mappings_repo_org_fkey
        FOREIGN KEY (repository_id, org_id)
        REFERENCES repositories (id, org_id)
        ON DELETE CASCADE;
