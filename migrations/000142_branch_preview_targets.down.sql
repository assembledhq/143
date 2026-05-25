DROP INDEX IF EXISTS idx_preview_api_tokens_hash_active;
DROP INDEX IF EXISTS idx_preview_api_tokens_org_hash;
DROP TABLE IF EXISTS preview_api_tokens;
DROP INDEX IF EXISTS idx_preview_idempotency_keys_org_key;
DROP TABLE IF EXISTS preview_idempotency_keys;

DROP INDEX IF EXISTS idx_preview_links_org_pr;
DROP INDEX IF EXISTS idx_preview_links_org_type_slug;
DROP TABLE IF EXISTS preview_links;

DROP INDEX IF EXISTS idx_preview_instances_active_target;
DROP INDEX IF EXISTS idx_preview_instances_org_target;
ALTER TABLE preview_instances
    DROP COLUMN IF EXISTS preview_target_id;
DELETE FROM preview_instances WHERE session_id IS NULL;
ALTER TABLE preview_instances
    ALTER COLUMN session_id SET NOT NULL;

DROP INDEX IF EXISTS idx_preview_targets_org_repo_created;
DROP INDEX IF EXISTS idx_preview_targets_identity;
DROP TABLE IF EXISTS preview_targets;
