DROP INDEX IF EXISTS idx_private_connector_actions_session_recent;
DROP INDEX IF EXISTS idx_private_connector_actions_resource_recent;
DROP INDEX IF EXISTS idx_private_connector_actions_nonce;
DROP TABLE IF EXISTS private_connector_actions;

DROP INDEX IF EXISTS idx_private_connector_runtime_leases_resource_active;
DROP INDEX IF EXISTS idx_private_connector_runtime_leases_preview_active;
DROP INDEX IF EXISTS idx_private_connector_runtime_leases_token_hash;
DROP TABLE IF EXISTS private_connector_runtime_leases;

DROP INDEX IF EXISTS idx_private_connector_resources_capability;
DROP INDEX IF EXISTS idx_private_connector_resources_group_name;
DROP TABLE IF EXISTS private_connector_resources;

DROP INDEX IF EXISTS idx_private_connector_instances_group_health;
DROP INDEX IF EXISTS idx_private_connector_instances_active_key;
DROP TABLE IF EXISTS private_connector_instances;

DROP INDEX IF EXISTS idx_private_connector_deployment_tokens_group;
DROP INDEX IF EXISTS idx_private_connector_deployment_tokens_hash;
DROP TABLE IF EXISTS private_connector_deployment_tokens;

DROP INDEX IF EXISTS idx_private_connector_groups_org_status;
DROP INDEX IF EXISTS idx_private_connector_groups_org_name;
DROP TABLE IF EXISTS private_connector_groups;
