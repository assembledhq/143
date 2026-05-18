SET LOCAL lock_timeout = '5s';

ALTER TABLE linear_team_repo_mappings
    DROP CONSTRAINT linear_team_repo_mappings_repo_org_fkey;

ALTER TABLE linear_team_repo_mappings
    ADD CONSTRAINT linear_team_repo_mappings_repository_id_fkey
        FOREIGN KEY (repository_id)
        REFERENCES repositories (id)
        ON DELETE CASCADE;

ALTER TABLE repositories
    DROP CONSTRAINT repositories_id_org_id_key;
