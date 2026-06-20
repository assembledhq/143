package db

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const PrivateConnectorDeploymentTokenPrefix = "143pc_"
const PrivateConnectorRuntimeLeaseTokenPrefix = "143pcl_"

const privateConnectorDeploymentTokenRandomBytes = 32
const privateConnectorRuntimeLeaseTokenRandomBytes = 32

const privateConnectorGroupColumns = `id, org_id, name, environment, gateway_region, status,
	health_alert_url, offline_alert_after_seconds, created_by_user_id, disabled_at, created_at, updated_at`

const privateConnectorDeploymentTokenColumns = `id, org_id, connector_group_id, name, token_hash, token_prefix,
	preset, max_registrations, registration_count, allowed_source_cidrs,
	allowed_gateway_region, expires_at, last_used_at, revoked_at,
	revoked_by_user_id, created_by_user_id, created_at`

const privateConnectorInstanceColumns = `id, org_id, connector_group_id, deployment_token_id, instance_name,
	public_key, status, version, protocol, gateway_region, capabilities,
	last_heartbeat_at, heartbeat_interval_seconds, online_at, offline_at,
	revoked_at, revoked_by_user_id, created_at, updated_at`

const privateConnectorResourceColumns = `id, org_id, connector_group_id, display_name, resource_type, mode,
	config, config_source, config_version, status, last_test_status,
	last_test_error, last_successful_request_at, last_error,
	created_by_user_id, created_at, updated_at`

const privateConnectorResourceColumnsQualified = `r.id, r.org_id, r.connector_group_id, r.display_name, r.resource_type, r.mode,
	r.config, r.config_source, r.config_version, r.status, r.last_test_status,
	r.last_test_error, r.last_successful_request_at, r.last_error,
	r.created_by_user_id, r.created_at, r.updated_at`

const privateConnectorActionColumns = `id, org_id, connector_group_id, connector_instance_id,
	resource_id, capability, actor_type, actor_id, repository_id, session_id,
	preview_id, request_nonce, request_fingerprint, status, error_code,
	error_message, result_count, duration_ms, created_at, completed_at`

const privateConnectorRuntimeLeaseColumns = `id, org_id, repository_id, preview_id, preview_runtime_id,
	connector_group_id, resource_id, status, access_mode, target_host, target_port,
	target_database, lease_token_hash, lease_token_prefix, max_connections,
	idle_timeout_seconds, byte_limit, expires_at, revoked_at, created_at, updated_at`

func GeneratePrivateConnectorDeploymentToken() (string, error) {
	raw := make([]byte, privateConnectorDeploymentTokenRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate private connector deployment token: %w", err)
	}
	return PrivateConnectorDeploymentTokenPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func GeneratePrivateConnectorRuntimeLeaseToken() (string, error) {
	raw := make([]byte, privateConnectorRuntimeLeaseTokenRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate private connector runtime lease token: %w", err)
	}
	return PrivateConnectorRuntimeLeaseTokenPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func PrivateConnectorDeploymentTokenDisplayPrefix(token string) string {
	if len(token) <= len(PrivateConnectorDeploymentTokenPrefix)+8 {
		return token
	}
	return token[:len(PrivateConnectorDeploymentTokenPrefix)+8]
}

func PrivateConnectorRuntimeLeaseTokenDisplayPrefix(token string) string {
	if len(token) <= len(PrivateConnectorRuntimeLeaseTokenPrefix)+8 {
		return token
	}
	return token[:len(PrivateConnectorRuntimeLeaseTokenPrefix)+8]
}

type PrivateConnectorStore struct {
	db DBTX
}

func NewPrivateConnectorStore(db DBTX) *PrivateConnectorStore {
	return &PrivateConnectorStore{db: db}
}

func (s *PrivateConnectorStore) CreateGroup(ctx context.Context, orgID uuid.UUID, group *models.PrivateConnectorGroup) error {
	group.OrgID = orgID
	if group.Status == "" {
		group.Status = models.PrivateConnectorStatusWaiting
	}
	query := fmt.Sprintf(`INSERT INTO private_connector_groups (
		org_id, name, environment, gateway_region, status, health_alert_url,
		offline_alert_after_seconds, created_by_user_id
	) VALUES (
		@org_id, @name, @environment, @gateway_region, @status, @health_alert_url,
		@offline_alert_after_seconds, @created_by_user_id
	) RETURNING %s`, privateConnectorGroupColumns)
	if group.OfflineAlertAfterSeconds == 0 {
		group.OfflineAlertAfterSeconds = 60
	}
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":                      group.OrgID,
		"name":                        group.Name,
		"environment":                 group.Environment,
		"gateway_region":              group.GatewayRegion,
		"status":                      group.Status,
		"health_alert_url":            group.HealthAlertURL,
		"offline_alert_after_seconds": group.OfflineAlertAfterSeconds,
		"created_by_user_id":          group.CreatedByUserID,
	})
	if err != nil {
		return fmt.Errorf("create private connector group: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorGroup])
	if err != nil {
		return fmt.Errorf("scan private connector group: %w", err)
	}
	*group = row
	return nil
}

func (s *PrivateConnectorStore) ListGroups(ctx context.Context, orgID uuid.UUID) ([]models.PrivateConnectorGroup, error) {
	query := fmt.Sprintf(`SELECT %s FROM private_connector_groups WHERE org_id = @org_id ORDER BY created_at DESC, id DESC`, privateConnectorGroupColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list private connector groups: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PrivateConnectorGroup])
}

func (s *PrivateConnectorStore) GetGroup(ctx context.Context, orgID, groupID uuid.UUID) (models.PrivateConnectorGroup, error) {
	query := fmt.Sprintf(`SELECT %s FROM private_connector_groups WHERE org_id = @org_id AND id = @id`, privateConnectorGroupColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": groupID})
	if err != nil {
		return models.PrivateConnectorGroup{}, fmt.Errorf("get private connector group: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorGroup])
}

func (s *PrivateConnectorStore) UpdateGroupStatus(ctx context.Context, orgID, groupID uuid.UUID, status models.PrivateConnectorStatus) error {
	tag, err := s.db.Exec(ctx, `UPDATE private_connector_groups
		SET status = @status, updated_at = now()
		WHERE org_id = @org_id AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "id": groupID, "status": status})
	if err != nil {
		return fmt.Errorf("update private connector group status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PrivateConnectorStore) UpdateGroupSettings(ctx context.Context, orgID, groupID uuid.UUID, healthAlertURL *string, offlineAlertAfterSeconds int) (models.PrivateConnectorGroup, error) {
	query := fmt.Sprintf(`UPDATE private_connector_groups
		SET health_alert_url = @health_alert_url,
			offline_alert_after_seconds = @offline_alert_after_seconds,
			updated_at = now()
		WHERE org_id = @org_id AND id = @id
		RETURNING %s`, privateConnectorGroupColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":                      orgID,
		"id":                          groupID,
		"health_alert_url":            healthAlertURL,
		"offline_alert_after_seconds": offlineAlertAfterSeconds,
	})
	if err != nil {
		return models.PrivateConnectorGroup{}, fmt.Errorf("update private connector group settings: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorGroup])
}

func (s *PrivateConnectorStore) DisableGroup(ctx context.Context, orgID, groupID uuid.UUID) (models.PrivateConnectorGroup, error) {
	query := fmt.Sprintf(`UPDATE private_connector_groups
		SET status = 'disabled', disabled_at = COALESCE(disabled_at, now()), updated_at = now()
		WHERE org_id = @org_id AND id = @id AND status <> 'disabled'
		RETURNING %s`, privateConnectorGroupColumns)
	rows, err := s.db.Query(ctx, query,
		pgx.NamedArgs{"org_id": orgID, "id": groupID})
	if err != nil {
		return models.PrivateConnectorGroup{}, fmt.Errorf("disable private connector group: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorGroup])
}

func (s *PrivateConnectorStore) CreateDeploymentToken(ctx context.Context, orgID uuid.UUID, token *models.PrivateConnectorDeploymentToken) error {
	token.OrgID = orgID
	query := fmt.Sprintf(`INSERT INTO private_connector_deployment_tokens (
		org_id, connector_group_id, name, token_hash, token_prefix, preset,
		max_registrations, allowed_source_cidrs, allowed_gateway_region,
		expires_at, created_by_user_id
	) VALUES (
		@org_id, @connector_group_id, @name, @token_hash, @token_prefix, @preset,
		@max_registrations, @allowed_source_cidrs, @allowed_gateway_region,
		@expires_at, @created_by_user_id
	) RETURNING %s`, privateConnectorDeploymentTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":                 token.OrgID,
		"connector_group_id":     token.ConnectorGroupID,
		"name":                   token.Name,
		"token_hash":             token.TokenHash,
		"token_prefix":           token.TokenPrefix,
		"preset":                 token.Preset,
		"max_registrations":      token.MaxRegistrations,
		"allowed_source_cidrs":   token.AllowedSourceCIDRs,
		"allowed_gateway_region": token.AllowedGatewayRegion,
		"expires_at":             token.ExpiresAt,
		"created_by_user_id":     token.CreatedByUserID,
	})
	if err != nil {
		return fmt.Errorf("create private connector deployment token: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorDeploymentToken])
	if err != nil {
		return fmt.Errorf("scan private connector deployment token: %w", err)
	}
	*token = row
	return nil
}

func (s *PrivateConnectorStore) ListDeploymentTokens(ctx context.Context, orgID, groupID uuid.UUID) ([]models.PrivateConnectorDeploymentToken, error) {
	query := fmt.Sprintf(`SELECT %s FROM private_connector_deployment_tokens
		WHERE org_id = @org_id AND connector_group_id = @connector_group_id
		ORDER BY created_at DESC, id DESC`, privateConnectorDeploymentTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "connector_group_id": groupID})
	if err != nil {
		return nil, fmt.Errorf("list private connector deployment tokens: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PrivateConnectorDeploymentToken])
}

func (s *PrivateConnectorStore) RevokeDeploymentToken(ctx context.Context, orgID, tokenID, revokedBy uuid.UUID) (models.PrivateConnectorDeploymentToken, error) {
	query := fmt.Sprintf(`UPDATE private_connector_deployment_tokens
		SET revoked_at = now(), revoked_by_user_id = @revoked_by
		WHERE org_id = @org_id AND id = @id AND revoked_at IS NULL
		RETURNING %s`, privateConnectorDeploymentTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": tokenID, "revoked_by": revokedBy})
	if err != nil {
		return models.PrivateConnectorDeploymentToken{}, fmt.Errorf("revoke private connector deployment token: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorDeploymentToken])
}

// ConsumeDeploymentToken resolves and burns one registration use of an opaque
// bootstrap token. It is intentionally pre-auth: org scoping comes from the
// returned token row, and callers must use that org for every follow-up write.
//
// lint:allow-no-orgid reason="connector bootstrap by opaque deployment token hash; returned token carries org_id"
func (s *PrivateConnectorStore) ConsumeDeploymentToken(ctx context.Context, rawToken, gatewayRegion, sourceIP string) (models.PrivateConnectorDeploymentToken, error) {
	query := fmt.Sprintf(`UPDATE private_connector_deployment_tokens
		SET registration_count = registration_count + 1, last_used_at = now()
		WHERE token_hash = @token_hash
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		  AND (max_registrations IS NULL OR registration_count < max_registrations)
		  AND (allowed_gateway_region IS NULL OR allowed_gateway_region = @gateway_region)
		  AND (
			cardinality(allowed_source_cidrs) = 0
			OR (
				@source_ip <> ''
				AND EXISTS (
					SELECT 1
					FROM unnest(allowed_source_cidrs) AS allowed_cidr(cidr)
					WHERE CAST(@source_ip AS inet) <<= allowed_cidr.cidr::cidr
				)
			)
		  )
		RETURNING %s`, privateConnectorDeploymentTokenColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"token_hash":     HashAPIToken(rawToken),
		"gateway_region": gatewayRegion,
		"source_ip":      sourceIP,
	})
	if err != nil {
		return models.PrivateConnectorDeploymentToken{}, fmt.Errorf("consume private connector deployment token: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorDeploymentToken])
}

func (s *PrivateConnectorStore) CreateInstance(ctx context.Context, orgID uuid.UUID, instance *models.PrivateConnectorInstance) error {
	instance.OrgID = orgID
	if instance.Status == "" {
		instance.Status = models.PrivateConnectorInstanceStatusOnline
	}
	query := fmt.Sprintf(`INSERT INTO private_connector_instances (
		org_id, connector_group_id, deployment_token_id, instance_name,
		public_key, status, version, protocol, gateway_region, capabilities,
		heartbeat_interval_seconds
	) VALUES (
		@org_id, @connector_group_id, @deployment_token_id, @instance_name,
		@public_key, @status, @version, @protocol, @gateway_region, @capabilities,
		@heartbeat_interval_seconds
	) RETURNING %s`, privateConnectorInstanceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":                     instance.OrgID,
		"connector_group_id":         instance.ConnectorGroupID,
		"deployment_token_id":        instance.DeploymentTokenID,
		"instance_name":              instance.InstanceName,
		"public_key":                 instance.PublicKey,
		"status":                     instance.Status,
		"version":                    instance.Version,
		"protocol":                   instance.Protocol,
		"gateway_region":             instance.GatewayRegion,
		"capabilities":               instance.Capabilities,
		"heartbeat_interval_seconds": instance.HeartbeatIntervalSeconds,
	})
	if err != nil {
		return fmt.Errorf("create private connector instance: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorInstance])
	if err != nil {
		return fmt.Errorf("scan private connector instance: %w", err)
	}
	*instance = row
	return nil
}

func (s *PrivateConnectorStore) ListInstances(ctx context.Context, orgID, groupID uuid.UUID) ([]models.PrivateConnectorInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM private_connector_instances
		WHERE org_id = @org_id AND connector_group_id = @connector_group_id
		ORDER BY created_at DESC, id DESC`, privateConnectorInstanceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "connector_group_id": groupID})
	if err != nil {
		return nil, fmt.Errorf("list private connector instances: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PrivateConnectorInstance])
}

func (s *PrivateConnectorStore) GetInstance(ctx context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM private_connector_instances WHERE org_id = @org_id AND id = @id`, privateConnectorInstanceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": instanceID})
	if err != nil {
		return models.PrivateConnectorInstance{}, fmt.Errorf("get private connector instance: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorInstance])
}

// GetInstanceByID is used by connector-authenticated heartbeat endpoints,
// where the instance id and Ed25519 signature establish the org before an
// org-scoped update can run.
//
// lint:allow-no-orgid reason="connector heartbeat lookup by instance id before signature establishes org"
func (s *PrivateConnectorStore) GetInstanceByID(ctx context.Context, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	query := fmt.Sprintf(`SELECT %s FROM private_connector_instances WHERE id = @id`, privateConnectorInstanceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": instanceID})
	if err != nil {
		return models.PrivateConnectorInstance{}, fmt.Errorf("get private connector instance by id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorInstance])
}

func (s *PrivateConnectorStore) UpdateInstanceHeartbeat(ctx context.Context, orgID, instanceID uuid.UUID, version string, protocol models.PrivateConnectorProtocol, capabilities []string, heartbeatIntervalSeconds int) (models.PrivateConnectorInstance, error) {
	query := fmt.Sprintf(`UPDATE private_connector_instances
		SET status = 'online', version = @version, protocol = @protocol,
			capabilities = @capabilities, heartbeat_interval_seconds = @heartbeat_interval_seconds,
			last_heartbeat_at = now(), online_at = COALESCE(online_at, now()),
			offline_at = NULL, updated_at = now()
		WHERE org_id = @org_id AND id = @id AND revoked_at IS NULL
		RETURNING %s`, privateConnectorInstanceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":                     orgID,
		"id":                         instanceID,
		"version":                    version,
		"protocol":                   protocol,
		"capabilities":               capabilities,
		"heartbeat_interval_seconds": heartbeatIntervalSeconds,
	})
	if err != nil {
		return models.PrivateConnectorInstance{}, fmt.Errorf("update private connector heartbeat: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorInstance])
}

func (s *PrivateConnectorStore) RevokeInstance(ctx context.Context, orgID, instanceID, revokedBy uuid.UUID) (models.PrivateConnectorInstance, error) {
	query := fmt.Sprintf(`UPDATE private_connector_instances
		SET status = 'revoked', revoked_at = now(), revoked_by_user_id = @revoked_by, updated_at = now()
		WHERE org_id = @org_id AND id = @id AND revoked_at IS NULL
		RETURNING %s`, privateConnectorInstanceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": instanceID, "revoked_by": revokedBy})
	if err != nil {
		return models.PrivateConnectorInstance{}, fmt.Errorf("revoke private connector instance: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorInstance])
}

func (s *PrivateConnectorStore) RotateInstancePublicKey(ctx context.Context, orgID, instanceID uuid.UUID, publicKey string) (models.PrivateConnectorInstance, error) {
	query := fmt.Sprintf(`UPDATE private_connector_instances
		SET public_key = @public_key, updated_at = now()
		WHERE org_id = @org_id AND id = @id AND revoked_at IS NULL
		RETURNING %s`, privateConnectorInstanceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": instanceID, "public_key": publicKey})
	if err != nil {
		return models.PrivateConnectorInstance{}, fmt.Errorf("rotate private connector instance key: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorInstance])
}

func (s *PrivateConnectorStore) MarkInstanceReconnecting(ctx context.Context, orgID, instanceID uuid.UUID) (models.PrivateConnectorInstance, error) {
	query := fmt.Sprintf(`UPDATE private_connector_instances
		SET status = 'reconnecting', updated_at = now()
		WHERE org_id = @org_id
		  AND id = @id
		  AND revoked_at IS NULL
		  AND status <> 'revoked'
		RETURNING %s`, privateConnectorInstanceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": instanceID})
	if err != nil {
		return models.PrivateConnectorInstance{}, fmt.Errorf("mark private connector reconnecting: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorInstance])
}

func (s *PrivateConnectorStore) MarkOfflineInstances(ctx context.Context, now time.Time) ([]models.PrivateConnectorHealthTransition, error) {
	rows, err := s.db.Query(ctx, `WITH transitioned AS (
		UPDATE private_connector_instances i
		SET status = 'offline',
			offline_at = COALESCE(offline_at, now()),
			updated_at = now()
		FROM private_connector_groups g
		WHERE i.org_id = g.org_id
		  AND i.connector_group_id = g.id
		  AND i.revoked_at IS NULL
		  AND i.status IN ('online', 'reconnecting')
		  AND i.last_heartbeat_at IS NOT NULL
		  AND i.last_heartbeat_at < @now - make_interval(secs => g.offline_alert_after_seconds)
		RETURNING i.*
	), updated_groups AS (
		UPDATE private_connector_groups g
		SET status = 'offline', updated_at = now()
		WHERE EXISTS (
			SELECT 1 FROM transitioned t
			WHERE t.org_id = g.org_id AND t.connector_group_id = g.id
		)
		AND NOT EXISTS (
			SELECT 1 FROM private_connector_instances i
			WHERE i.org_id = g.org_id
			  AND i.connector_group_id = g.id
			  AND i.revoked_at IS NULL
			  AND i.status IN ('online', 'reconnecting')
		)
		RETURNING g.id
	)
	SELECT to_jsonb(t), to_jsonb(g)
	FROM transitioned t
	JOIN private_connector_groups g ON g.org_id = t.org_id AND g.id = t.connector_group_id
	ORDER BY t.updated_at DESC, t.id DESC`, pgx.NamedArgs{"now": now})
	if err != nil {
		return nil, fmt.Errorf("mark private connector instances offline: %w", err)
	}
	defer rows.Close()
	transitions := make([]models.PrivateConnectorHealthTransition, 0)
	for rows.Next() {
		var instanceRaw, groupRaw []byte
		if err := rows.Scan(&instanceRaw, &groupRaw); err != nil {
			return nil, fmt.Errorf("scan private connector offline transition: %w", err)
		}
		var transition models.PrivateConnectorHealthTransition
		if err := json.Unmarshal(instanceRaw, &transition.Instance); err != nil {
			return nil, fmt.Errorf("decode private connector offline instance: %w", err)
		}
		if err := json.Unmarshal(groupRaw, &transition.Group); err != nil {
			return nil, fmt.Errorf("decode private connector offline group: %w", err)
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate private connector offline transitions: %w", err)
	}
	return transitions, nil
}

func (s *PrivateConnectorStore) CreateResource(ctx context.Context, orgID uuid.UUID, resource *models.PrivateConnectorResource) error {
	resource.OrgID = orgID
	if resource.Config == nil {
		resource.Config = json.RawMessage(`{}`)
	}
	if resource.ConfigSource == "" {
		resource.ConfigSource = models.PrivateConnectorConfigSourceUI
	}
	if resource.Status == "" {
		resource.Status = models.PrivateConnectorResourceStatusConfigured
	}
	if resource.ConfigVersion == 0 {
		resource.ConfigVersion = 1
	}
	query := fmt.Sprintf(`INSERT INTO private_connector_resources (
		org_id, connector_group_id, display_name, resource_type, mode, config,
		config_source, config_version, status, created_by_user_id
	) VALUES (
		@org_id, @connector_group_id, @display_name, @resource_type, @mode, @config,
		@config_source, @config_version, @status, @created_by_user_id
	) RETURNING %s`, privateConnectorResourceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":             resource.OrgID,
		"connector_group_id": resource.ConnectorGroupID,
		"display_name":       resource.DisplayName,
		"resource_type":      resource.ResourceType,
		"mode":               resource.Mode,
		"config":             resource.Config,
		"config_source":      resource.ConfigSource,
		"config_version":     resource.ConfigVersion,
		"status":             resource.Status,
		"created_by_user_id": resource.CreatedByUserID,
	})
	if err != nil {
		return fmt.Errorf("create private connector resource: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorResource])
	if err != nil {
		return fmt.Errorf("scan private connector resource: %w", err)
	}
	*resource = row
	return nil
}

func (s *PrivateConnectorStore) ListResources(ctx context.Context, orgID, groupID uuid.UUID) ([]models.PrivateConnectorResource, error) {
	query := fmt.Sprintf(`SELECT %s FROM private_connector_resources WHERE org_id = @org_id AND connector_group_id = @connector_group_id ORDER BY created_at DESC, id DESC`, privateConnectorResourceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "connector_group_id": groupID})
	if err != nil {
		return nil, fmt.Errorf("list private connector resources: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PrivateConnectorResource])
}

func (s *PrivateConnectorStore) ListResourcesWithOnlineCapability(ctx context.Context, orgID uuid.UUID, resourceType models.PrivateConnectorResourceType, capability string) ([]models.PrivateConnectorResource, error) {
	query := fmt.Sprintf(`SELECT DISTINCT %s
		FROM private_connector_resources r
		JOIN private_connector_groups g
			ON g.org_id = r.org_id AND g.id = r.connector_group_id
		JOIN private_connector_instances i
			ON i.org_id = r.org_id AND i.connector_group_id = r.connector_group_id
		WHERE r.org_id = @org_id
		  AND r.resource_type = @resource_type
			  AND r.status IN ('configured', 'ready')
		  AND g.status <> 'disabled'
		  AND i.status = 'online'
		  AND i.revoked_at IS NULL
		  AND @capability = ANY(i.capabilities)
		ORDER BY r.created_at DESC, r.id DESC`, privateConnectorResourceColumnsQualified)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "resource_type": resourceType, "capability": capability})
	if err != nil {
		return nil, fmt.Errorf("list private connector resources with online capability: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PrivateConnectorResource])
}

func (s *PrivateConnectorStore) GetResource(ctx context.Context, orgID, resourceID uuid.UUID) (models.PrivateConnectorResource, error) {
	query := fmt.Sprintf(`SELECT %s FROM private_connector_resources WHERE org_id = @org_id AND id = @id`, privateConnectorResourceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": resourceID})
	if err != nil {
		return models.PrivateConnectorResource{}, fmt.Errorf("get private connector resource: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorResource])
}

func (s *PrivateConnectorStore) UpdateResourceTestResult(ctx context.Context, orgID, resourceID uuid.UUID, status models.PrivateConnectorResourceStatus, testStatus, testError *string) (models.PrivateConnectorResource, error) {
	query := fmt.Sprintf(`UPDATE private_connector_resources
		SET status = @status, last_test_status = @last_test_status,
			last_test_error = @last_test_error, updated_at = now()
		WHERE org_id = @org_id AND id = @id
		RETURNING %s`, privateConnectorResourceColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":           orgID,
		"id":               resourceID,
		"status":           status,
		"last_test_status": testStatus,
		"last_test_error":  testError,
	})
	if err != nil {
		return models.PrivateConnectorResource{}, fmt.Errorf("update private connector resource test result: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorResource])
}

func (s *PrivateConnectorStore) CreateRuntimeLease(ctx context.Context, orgID uuid.UUID, lease *models.PrivateConnectorRuntimeLease) error {
	lease.OrgID = orgID
	if lease.Status == "" {
		lease.Status = models.PrivateConnectorRuntimeLeaseStatusActive
	}
	query := fmt.Sprintf(`INSERT INTO private_connector_runtime_leases (
		org_id, repository_id, preview_id, preview_runtime_id, connector_group_id,
		resource_id, status, access_mode, target_host, target_port, target_database,
		lease_token_hash, lease_token_prefix, max_connections, idle_timeout_seconds,
		byte_limit, expires_at
	) VALUES (
		@org_id, @repository_id, @preview_id, @preview_runtime_id, @connector_group_id,
		@resource_id, @status, @access_mode, @target_host, @target_port, @target_database,
		@lease_token_hash, @lease_token_prefix, @max_connections, @idle_timeout_seconds,
		@byte_limit, @expires_at
	) RETURNING %s`, privateConnectorRuntimeLeaseColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":               lease.OrgID,
		"repository_id":        lease.RepositoryID,
		"preview_id":           lease.PreviewID,
		"preview_runtime_id":   lease.PreviewRuntimeID,
		"connector_group_id":   lease.ConnectorGroupID,
		"resource_id":          lease.ResourceID,
		"status":               lease.Status,
		"access_mode":          lease.AccessMode,
		"target_host":          lease.TargetHost,
		"target_port":          lease.TargetPort,
		"target_database":      lease.TargetDatabase,
		"lease_token_hash":     lease.LeaseTokenHash,
		"lease_token_prefix":   lease.LeaseTokenPrefix,
		"max_connections":      lease.MaxConnections,
		"idle_timeout_seconds": lease.IdleTimeoutSeconds,
		"byte_limit":           lease.ByteLimit,
		"expires_at":           lease.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("create private connector runtime lease: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorRuntimeLease])
	if err != nil {
		return fmt.Errorf("scan private connector runtime lease: %w", err)
	}
	*lease = row
	return nil
}

func (s *PrivateConnectorStore) CountActiveRuntimeLeases(ctx context.Context, orgID, resourceID uuid.UUID) (int, error) {
	var count int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM private_connector_runtime_leases
		WHERE org_id = @org_id
		  AND resource_id = @resource_id
		  AND status = 'active'
		  AND revoked_at IS NULL
		  AND expires_at > now()`,
		pgx.NamedArgs{"org_id": orgID, "resource_id": resourceID}).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active private connector runtime leases: %w", err)
	}
	return count, nil
}

func (s *PrivateConnectorStore) GetActiveRuntimeLease(ctx context.Context, orgID, leaseID uuid.UUID) (models.PrivateConnectorRuntimeLease, error) {
	query := fmt.Sprintf(`SELECT %s FROM private_connector_runtime_leases WHERE org_id = @org_id AND id = @id
		AND status = 'active' AND revoked_at IS NULL AND expires_at > now()`, privateConnectorRuntimeLeaseColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": leaseID})
	if err != nil {
		return models.PrivateConnectorRuntimeLease{}, fmt.Errorf("get active private connector runtime lease: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorRuntimeLease])
}

// GetActiveRuntimeLeaseByToken is used by the preview data-plane proxy before
// the lease token can establish org scope. The returned lease carries org_id
// for all follow-up authorization and audit decisions.
//
// lint:allow-no-orgid reason="preview runtime proxy lookup by opaque lease token before org scope is known"
func (s *PrivateConnectorStore) GetActiveRuntimeLeaseByToken(ctx context.Context, rawToken string) (models.PrivateConnectorRuntimeLease, error) {
	query := fmt.Sprintf(`SELECT %s FROM private_connector_runtime_leases WHERE lease_token_hash = @lease_token_hash
		AND status = 'active' AND revoked_at IS NULL AND expires_at > now()`, privateConnectorRuntimeLeaseColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"lease_token_hash": HashAPIToken(rawToken)})
	if err != nil {
		return models.PrivateConnectorRuntimeLease{}, fmt.Errorf("get active private connector runtime lease by token: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorRuntimeLease])
}

func (s *PrivateConnectorStore) RevokeRuntimeLease(ctx context.Context, orgID, leaseID uuid.UUID) (models.PrivateConnectorRuntimeLease, error) {
	query := fmt.Sprintf(`UPDATE private_connector_runtime_leases
		SET status = 'revoked', revoked_at = COALESCE(revoked_at, now()), updated_at = now()
		WHERE org_id = @org_id AND id = @id AND status = 'active'
		RETURNING %s`, privateConnectorRuntimeLeaseColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": leaseID})
	if err != nil {
		return models.PrivateConnectorRuntimeLease{}, fmt.Errorf("revoke private connector runtime lease: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorRuntimeLease])
}

func (s *PrivateConnectorStore) RecordAction(ctx context.Context, orgID uuid.UUID, action *models.PrivateConnectorAction) error {
	action.OrgID = orgID
	if action.Status == "" {
		action.Status = models.PrivateConnectorActionStatusPending
	}
	query := fmt.Sprintf(`INSERT INTO private_connector_actions (
		org_id, connector_group_id, connector_instance_id, resource_id,
		capability, actor_type, actor_id, repository_id, session_id,
		preview_id, request_nonce, request_fingerprint, status
	) VALUES (
		@org_id, @connector_group_id, @connector_instance_id, @resource_id,
		@capability, @actor_type, @actor_id, @repository_id, @session_id,
		@preview_id, @request_nonce, @request_fingerprint, @status
	) RETURNING %s`, privateConnectorActionColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":                action.OrgID,
		"connector_group_id":    action.ConnectorGroupID,
		"connector_instance_id": action.ConnectorInstanceID,
		"resource_id":           action.ResourceID,
		"capability":            action.Capability,
		"actor_type":            action.ActorType,
		"actor_id":              action.ActorID,
		"repository_id":         action.RepositoryID,
		"session_id":            action.SessionID,
		"preview_id":            action.PreviewID,
		"request_nonce":         action.RequestNonce,
		"request_fingerprint":   action.RequestFingerprint,
		"status":                action.Status,
	})
	if err != nil {
		return fmt.Errorf("record private connector action: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorAction])
	if err != nil {
		return fmt.Errorf("scan private connector action: %w", err)
	}
	*action = row
	return nil
}

func (s *PrivateConnectorStore) CompleteAction(ctx context.Context, orgID, actionID uuid.UUID, status models.PrivateConnectorActionStatus, errorCode, errorMessage *string, resultCount, durationMs *int) (models.PrivateConnectorAction, error) {
	query := fmt.Sprintf(`UPDATE private_connector_actions
		SET status = @status, error_code = @error_code, error_message = @error_message,
			result_count = @result_count, duration_ms = @duration_ms, completed_at = now()
		WHERE org_id = @org_id AND id = @id
		RETURNING %s`, privateConnectorActionColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":        orgID,
		"id":            actionID,
		"status":        status,
		"error_code":    errorCode,
		"error_message": errorMessage,
		"result_count":  resultCount,
		"duration_ms":   durationMs,
	})
	if err != nil {
		return models.PrivateConnectorAction{}, fmt.Errorf("complete private connector action: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PrivateConnectorAction])
}

func (s *PrivateConnectorStore) ListRecentActions(ctx context.Context, orgID, groupID uuid.UUID, limit int) ([]models.PrivateConnectorAction, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	query := fmt.Sprintf(`SELECT %s FROM private_connector_actions
		WHERE org_id = @org_id AND connector_group_id = @connector_group_id
		ORDER BY created_at DESC, id DESC
		LIMIT @limit`, privateConnectorActionColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "connector_group_id": groupID, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list recent private connector actions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PrivateConnectorAction])
}

func PrivateConnectorInteractiveTokenDefaults(now time.Time) (models.PrivateConnectorTokenPreset, *int, *time.Time) {
	maxRegistrations := 1
	expiresAt := now.Add(24 * time.Hour)
	return models.PrivateConnectorTokenPresetInteractive, &maxRegistrations, &expiresAt
}
