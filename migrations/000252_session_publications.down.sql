-- Migration 000252 rollback.
DROP INDEX IF EXISTS uq_pull_requests_changeset;
DROP TABLE IF EXISTS session_publications;
