package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/assembledhq/143/internal/connector"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParsePublicKeyBase64(t *testing.T) {
	t.Parallel()

	publicKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err, "test key should be generated")

	parsed, err := parsePublicKeyBase64(base64.StdEncoding.EncodeToString(publicKey))
	require.NoError(t, err, "parsePublicKeyBase64 should parse a base64 Ed25519 public key")
	require.Equal(t, publicKey, parsed, "parsePublicKeyBase64 should return the decoded public key")

	_, err = parsePublicKeyBase64(base64.StdEncoding.EncodeToString([]byte("short")))
	require.Error(t, err, "parsePublicKeyBase64 should reject non-Ed25519 key lengths")
}

func TestBuildProviderRegistryFromEnvRegistersVictoriaLogs(t *testing.T) {
	// This test intentionally does not call t.Parallel because it verifies
	// process environment parsing.
	resourceID := uuid.New()
	t.Setenv("143_CONNECTOR_VICTORIALOGS_RESOURCE_ID", resourceID.String())
	t.Setenv("143_CONNECTOR_VICTORIALOGS_URL", "http://victorialogs:9428")
	t.Setenv("143_CONNECTOR_VICTORIALOGS_TOKEN", "secret")
	t.Setenv("143_CONNECTOR_VICTORIALOGS_TOKEN_FILE", "")
	t.Setenv("143_CONNECTOR_VICTORIALOGS_FIELD_NAMES_URL", "")
	t.Setenv("143_CONNECTOR_VICTORIALOGS_MAX_ROWS", "25")
	t.Setenv("143_CONNECTOR_VICTORIALOGS_REDACT_FIELDS", "authorization, cookie")

	registry, resourceIDs, capabilities, err := buildProviderRegistryFromEnv(nil)

	require.NoError(t, err, "buildProviderRegistryFromEnv should accept complete VictoriaLogs env config")
	require.NotNil(t, registry, "buildProviderRegistryFromEnv should return a registry")
	require.Contains(t, resourceIDs, resourceID, "buildProviderRegistryFromEnv should authorize the configured resource id")
	require.ElementsMatch(t, []string{"victorialogs.query", "victorialogs.context", "victorialogs.fields", "victorialogs.stats"}, capabilities, "buildProviderRegistryFromEnv should advertise provider capabilities")
}

func TestBuildProviderRegistryFromEnvRegistersPostgres(t *testing.T) {
	// This test intentionally does not call t.Parallel because it verifies
	// process environment parsing.
	resourceID := uuid.New()
	t.Setenv("143_CONNECTOR_VICTORIALOGS_RESOURCE_ID", "")
	t.Setenv("143_CONNECTOR_VICTORIALOGS_URL", "")
	t.Setenv("143_CONNECTOR_POSTGRES_RESOURCE_ID", resourceID.String())
	t.Setenv("143_CONNECTOR_POSTGRES_DATABASE_URL", "postgres://readonly:secret@db.internal:5432/app")
	t.Setenv("143_CONNECTOR_POSTGRES_DATABASE_URL_FILE", "")
	t.Setenv("143_CONNECTOR_POSTGRES_MAX_ROWS", "50")
	t.Setenv("143_CONNECTOR_POSTGRES_STATEMENT_TIMEOUT_MS", "3000")
	t.Setenv("143_CONNECTOR_POSTGRES_REDACT_COLUMNS", "email,ssn")

	registry, resourceIDs, capabilities, err := buildProviderRegistryFromEnv(nil)

	require.NoError(t, err, "buildProviderRegistryFromEnv should accept complete Postgres env config")
	require.NotNil(t, registry, "buildProviderRegistryFromEnv should return a registry")
	require.Contains(t, resourceIDs, resourceID, "buildProviderRegistryFromEnv should authorize the configured Postgres resource id")
	require.Contains(t, capabilities, "postgres.query", "buildProviderRegistryFromEnv should advertise Postgres query capability")
}

func TestLoadConnectorConfigFileParsesDocShapeResources(t *testing.T) {
	t.Parallel()

	logResourceID := uuid.New()
	dbResourceID := uuid.New()
	path := filepath.Join(t.TempDir(), "connector.yaml")
	err := os.WriteFile(path, []byte(`
api_url: "https://app.example.test"
state_path: "/state/state.json"
identity_path: "/state/identity.key"
connector:
  name: "production-vpc"
  gateway_region: "eu"
resources:
  logs:
    id: "`+logResourceID.String()+`"
    type: victorialogs
    mode: logs
    url: "http://victorialogs:9428"
    default_filters:
      environment: production
    limits:
      max_rows: 250
      max_time_range: 24h
      timeout: 10s
      max_series_cardinality: 500
      max_requests_per_minute: 45
    allowed_fields:
      - service
      - level
    denied_fields:
      - authorization
    redact_fields:
      - authorization
  prod_db:
    id: "`+dbResourceID.String()+`"
    type: postgres
    mode: agent_readonly
    dsn_env: PROD_READONLY_DATABASE_URL
    allowed_schemas:
      - public
    denied_tables:
      - public.payment_methods
    pii_columns:
      - email
    pii_column_patterns:
      - ".*_email$"
    limits:
      max_rows: 50
      timeout: 5s
`), 0o600)
	require.NoError(t, err, "test connector config should write")

	cfg, err := loadConnectorConfigFile(path)

	require.NoError(t, err, "loadConnectorConfigFile should parse documented connector config")
	require.Equal(t, "https://app.example.test", cfg.APIURL, "config should read api_url")
	require.Equal(t, "/state/state.json", cfg.StatePath, "config should read state path")
	require.Equal(t, "/state/identity.key", cfg.IdentityPath, "config should read identity path")
	require.Equal(t, "production-vpc", cfg.Connector.Name, "config should read connector name")
	require.Equal(t, "eu", cfg.Connector.GatewayRegion, "config should read connector region")
	require.Equal(t, logResourceID.String(), cfg.Resources["logs"].ID, "config should preserve VictoriaLogs resource id")
	require.Equal(t, "victorialogs", cfg.Resources["logs"].Type, "config should parse VictoriaLogs type")
	require.Equal(t, 250, cfg.Resources["logs"].Limits.MaxRows, "config should parse VictoriaLogs limits")
	require.Equal(t, "24h", cfg.Resources["logs"].Limits.MaxTimeRange, "config should parse VictoriaLogs max time range")
	require.Equal(t, 500, cfg.Resources["logs"].Limits.MaxSeriesCardinality, "config should parse VictoriaLogs cardinality limit")
	require.Equal(t, 45, cfg.Resources["logs"].Limits.MaxRequestsPerMinute, "config should parse VictoriaLogs request rate limit")
	require.Equal(t, []string{"service", "level"}, cfg.Resources["logs"].AllowedFields, "config should parse allowed log fields")
	require.Equal(t, []string{"authorization"}, cfg.Resources["logs"].DeniedFields, "config should parse denied log fields")
	require.Equal(t, []string{"authorization"}, cfg.Resources["logs"].RedactFields, "config should parse redacted log fields")
	require.Equal(t, dbResourceID.String(), cfg.Resources["prod_db"].ID, "config should preserve Postgres resource id")
	require.Equal(t, "PROD_READONLY_DATABASE_URL", cfg.Resources["prod_db"].DSNEnv, "config should parse Postgres secret references")
	require.Equal(t, []string{"public"}, cfg.Resources["prod_db"].AllowedSchemas, "config should parse allowed schemas")
	require.Equal(t, []string{"public.payment_methods"}, cfg.Resources["prod_db"].DeniedTables, "config should parse denied tables")
	require.Equal(t, []string{".*_email$"}, cfg.Resources["prod_db"].PIIColumnPatterns, "config should parse PII column patterns")
}

func TestMetadataOnlyConnectorConfigDoesNotBlockUIPushes(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	cfg := connectorConfigFile{
		Loaded: true,
		APIURL: "https://app.example.test",
	}
	providers := newConnectorProviderState(nil, nil, nil, cfg.ManagesResourceConfig(), nil, "")
	frame := connector.ConfigPushFrame{
		OrgID:       uuid.New(),
		ConnectorID: uuid.New(),
		Version:     1,
		Resources: []connector.ConfigPushResource{{
			ID:           resourceID,
			ResourceType: "victorialogs",
			Mode:         "logs",
			Config:       []byte(`{"base_url":"http://victorialogs:9428"}`),
		}},
	}

	result, err := providers.ApplyConfigPush(context.Background(), frame)

	require.NoError(t, err, "metadata-only connector config should not make resources local-file managed")
	require.Equal(t, int64(1), result.Version, "UI config push should apply to metadata-only installs")
	require.Contains(t, providers.ResourceIDs(), resourceID, "UI config push should replace the provider resource set")
	require.Contains(t, providers.Capabilities(), "victorialogs.query", "UI config push should advertise pushed provider capabilities")
}

func TestVictoriaLogsPolicyFromResourceUsesConfiguredLimits(t *testing.T) {
	t.Parallel()

	resource := normalizeConnectorConfigResource(connectorConfigResource{
		DefaultFilter: "environment:production",
		AllowedFields: []string{"service", "level"},
		DeniedFields:  []string{"authorization"},
		RedactFields:  []string{"cookie"},
		Limits: connectorConfigLimits{
			MaxRows:              250,
			MaxTimeRange:         "24h",
			MaxSeriesCardinality: 500,
			MaxRequestsPerMinute: 45,
		},
	})

	policy, err := victoriaLogsPolicyFromResource(resource)

	require.NoError(t, err, "victoriaLogsPolicyFromResource should parse configured durations")
	require.Equal(t, 250, policy.MaxRows, "VictoriaLogs policy should carry max rows")
	require.Equal(t, 24*60*60, int(policy.MaxQueryWindow.Seconds()), "VictoriaLogs policy should carry max query window")
	require.Equal(t, 500, policy.MaxSeriesCardinality, "VictoriaLogs policy should carry max series cardinality")
	require.Equal(t, 45, policy.MaxRequestsPerMinute, "VictoriaLogs policy should carry request rate limit")
	require.Equal(t, "environment:production", policy.DefaultFilter, "VictoriaLogs policy should carry default filter")
	require.Equal(t, []string{"service", "level"}, policy.AllowedFields, "VictoriaLogs policy should carry allowed fields")
	require.Equal(t, []string{"authorization"}, policy.DeniedFields, "VictoriaLogs policy should carry denied fields")
	require.Equal(t, []string{"cookie"}, policy.RedactFields, "VictoriaLogs policy should carry redaction fields")
}

func TestRunUpdateCommandRequiresExplicitCommand(t *testing.T) {
	t.Parallel()

	_, err := runUpdateCommand(context.Background(), "")

	require.Error(t, err, "runUpdateCommand should fail closed when no update command is configured")
}

func TestRunUpdateCommandExecutesConfiguredCommand(t *testing.T) {
	t.Parallel()

	result, err := runUpdateCommand(context.Background(), "printf updated")

	require.NoError(t, err, "runUpdateCommand should execute the configured update command")
	require.True(t, result.Started, "runUpdateCommand should report that update work started")
	require.Equal(t, "updated", result.Message, "runUpdateCommand should include trimmed command output")
}

func TestBuildProviderRegistryFromConfigRegistersOnlyConfiguredResources(t *testing.T) {
	// This test intentionally does not call t.Parallel because provider config
	// resolves local secret references from the process environment.
	logResourceID := uuid.New()
	dbResourceID := uuid.New()
	t.Setenv("PROD_READONLY_DATABASE_URL", "postgres://readonly:secret@db.internal:5432/app")

	cfg := connectorConfigFile{
		Resources: map[string]connectorConfigResource{
			"logs": {
				ID:           logResourceID.String(),
				Type:         "victorialogs",
				URL:          "http://victorialogs:9428",
				RedactFields: []string{"authorization"},
				Limits:       connectorConfigLimits{MaxRows: 25},
			},
			"prod_db": {
				ID:             dbResourceID.String(),
				Type:           "postgres",
				Mode:           "agent_readonly",
				DSNEnv:         "PROD_READONLY_DATABASE_URL",
				AllowedSchemas: []string{"public"},
				DeniedTables:   []string{"public.payment_methods"},
				PIIColumns:     []string{"email"},
				Limits:         connectorConfigLimits{MaxRows: 50, Timeout: "3s"},
			},
		},
	}

	registry, resourceIDs, capabilities, err := buildProviderRegistryFromConfig(cfg, nil)

	require.NoError(t, err, "buildProviderRegistryFromConfig should build providers from local file config")
	require.NotNil(t, registry, "buildProviderRegistryFromConfig should return a provider registry")
	require.Contains(t, resourceIDs, logResourceID, "config registry should authorize VictoriaLogs resource id")
	require.Contains(t, resourceIDs, dbResourceID, "config registry should authorize Postgres resource id")
	require.Contains(t, capabilities, "victorialogs.query", "config registry should advertise VictoriaLogs query")
	require.Contains(t, capabilities, "postgres.query", "config registry should advertise Postgres query")
	require.Contains(t, capabilities, "postgres.schema", "config registry should advertise Postgres schema inspection")
	require.Contains(t, capabilities, "postgres.explain", "config registry should advertise Postgres explain")
}
