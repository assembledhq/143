SET LOCAL lock_timeout = '5s';

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_id_org_id_key;
