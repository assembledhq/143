DROP TABLE IF EXISTS api_idempotency_keys;
DROP INDEX IF EXISTS idx_api_tokens_client_created;
DROP INDEX IF EXISTS idx_api_tokens_hash_active;
DROP TABLE IF EXISTS api_tokens;
DROP INDEX IF EXISTS idx_api_clients_org_created;
DROP TABLE IF EXISTS api_clients;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_origin;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_origin
        CHECK (origin IN ('issue_trigger', 'manual', 'project', 'automation', 'revision', 'slack'));
