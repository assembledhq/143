-- Idempotent setup for the `readonly` Postgres role.
-- Run via deploy/scripts/setup-readonly-user.sh (or `make setup-readonly-user`).
-- Requires psql variables:
--   ropass — the readonly password
--   dbname — database name (e.g. onefortythree)
--   owner  — the app role that owns migration-created tables (e.g. onefortythree)

\set ON_ERROR_STOP on

-- Create role if it doesn't exist. \gexec materializes the SQL text from the
-- SELECT and executes it, so this is a no-op when the role is already present.
SELECT 'CREATE ROLE readonly NOLOGIN'
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'readonly') \gexec

-- Always (re)apply login + password + read-only default so reruns rotate cleanly.
ALTER ROLE readonly WITH LOGIN PASSWORD :'ropass';
ALTER ROLE readonly SET default_transaction_read_only = on;

-- Grants on the current set of objects.
GRANT CONNECT ON DATABASE :"dbname" TO readonly;
GRANT USAGE ON SCHEMA public TO readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO readonly;
GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO readonly;

-- Future tables/sequences added by migrations: grant SELECT automatically,
-- but only for objects created by the app owner.
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public
  GRANT SELECT ON TABLES TO readonly;
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public
  GRANT SELECT ON SEQUENCES TO readonly;
