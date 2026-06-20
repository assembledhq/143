package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/assembledhq/143/internal/connector"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

var version = "dev"

var errLocalConfigManaged = errors.New("local connector config file is active")

func main() {
	logger := zerolog.New(os.Stderr).With().Timestamp().Str("service", "private-connector").Logger()
	cfg, fileCfg, registerOnly, err := parseConfig()
	if err != nil {
		logger.Fatal().Err(err).Msg("configuration failed")
	}
	localConfigManaged := fileCfg.ManagesResourceConfig()
	providerRegistry, resourceIDs, capabilities, err := buildProviderRegistryFromConfig(fileCfg, cfg.HTTPClient)
	if err == nil && len(resourceIDs) == 0 && !localConfigManaged {
		providerRegistry, resourceIDs, capabilities, err = buildProviderRegistryFromEnv(cfg.HTTPClient)
	}
	if err != nil {
		logger.Fatal().Err(err).Msg("provider configuration failed")
	}
	providers := newConnectorProviderState(providerRegistry, resourceIDs, capabilities, localConfigManaged, cfg.HTTPClient, cfg.ConfigPath)
	updateCommand := envString("143_CONNECTOR_UPDATE_COMMAND", "")
	cfg.Capabilities = providers.Capabilities()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, err := connector.Bootstrap(ctx, cfg)
	if err != nil {
		logger.Fatal().Err(err).Msg("bootstrap failed")
	}
	logger.Info().
		Str("instance_id", result.State.InstanceID.String()).
		Str("connector_group_id", result.State.ConnectorGroupID.String()).
		Msg("connector bootstrapped")

	client := connector.NewDaemonClient(cfg.APIURL, cfg.HTTPClient)
	payload := connector.HeartbeatPayload{
		Version:                  cfg.Version,
		Protocol:                 result.State.Protocol,
		Capabilities:             providers.Capabilities(),
		HeartbeatIntervalSeconds: result.State.HeartbeatIntervalSeconds,
	}
	if err := client.SendHeartbeat(ctx, result.Identity, result.State, payload); err != nil {
		logger.Fatal().Err(err).Msg("initial heartbeat failed")
	}
	logger.Info().Msg("initial heartbeat sent")
	if registerOnly {
		return
	}

	gatewayPublicKey, err := parsePublicKeyBase64(envString("143_CONNECTOR_GATEWAY_PUBLIC_KEY", ""))
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid gateway public key")
	}
	if len(gatewayPublicKey) == 0 {
		logger.Warn().Msg("gateway public key is not configured; using heartbeat-only mode")
		runHeartbeatLoop(ctx, logger, client, result.Identity, result.State, payload)
		return
	}

	backoff := time.Second
	for {
		err := client.RunGatewaySession(ctx, result.Identity, result.State, providerRegistry, connector.GatewaySessionConfig{
			GatewayPublicKey: gatewayPublicKey,
			ResourceIDsFunc:  providers.ResourceIDs,
			HeartbeatFunc: func() connector.HeartbeatPayload {
				current := payload
				current.Capabilities = providers.Capabilities()
				return current
			},
			ConfigHandler: providers,
			RotateIdentityFunc: func(ctx context.Context) (string, error) {
				next, err := client.RotateInstanceIdentity(ctx, result.Identity, result.State, cfg.IdentityPath)
				if err != nil {
					return "", err
				}
				result.Identity = next
				return next.PublicKeyBase64(), nil
			},
			ReloadConfigFunc: providers.ReloadFromFile,
			UpdateFunc: func(ctx context.Context) (connector.UpdateResult, error) {
				return runUpdateCommand(ctx, updateCommand)
			},
		})
		if err == nil || errors.Is(err, context.Canceled) {
			logger.Info().Msg("connector stopping")
			return
		}
		logger.Warn().Err(err).Dur("retry_in", backoff).Msg("gateway session ended; reconnecting")
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Info().Msg("connector stopping")
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func runHeartbeatLoop(ctx context.Context, logger zerolog.Logger, client *connector.DaemonClient, identity connector.Identity, state connector.ConnectorState, payload connector.HeartbeatPayload) {
	interval := time.Duration(state.HeartbeatIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("connector stopping")
			return
		case <-ticker.C:
			if err := client.SendHeartbeat(ctx, identity, state, payload); err != nil {
				logger.Warn().Err(err).Msg("heartbeat failed")
			}
		}
	}
}

func parseConfig() (connector.DaemonConfig, connectorConfigFile, bool, error) {
	heartbeat := envInt("143_CONNECTOR_HEARTBEAT_SECONDS", 5)
	registerOnly := flag.Bool("register-only", envBool("143_CONNECTOR_REGISTER_ONLY"), "register, send one heartbeat, then exit")
	configPath := flag.String("config", envString("143_CONNECTOR_CONFIG", "/etc/143/connector.yaml"), "connector local config file path")
	apiURL := flag.String("api-url", envString("143_API_URL", "https://app.143.dev"), "143 API base URL")
	tokenFile := flag.String("token-file", envString("143_CONNECTOR_TOKEN_FILE", ""), "file containing the connector deployment token")
	identityPath := flag.String("identity", envString("143_CONNECTOR_IDENTITY_PATH", "/var/lib/143-connector/identity.key"), "connector identity key path")
	statePath := flag.String("state", envString("143_CONNECTOR_STATE_PATH", "/var/lib/143-connector/state.json"), "connector registration state path")
	instanceName := flag.String("name", envString("143_CONNECTOR_NAME", hostname()), "connector instance name")
	gatewayRegion := flag.String("region", envString("143_CONNECTOR_REGION", "us"), "connector gateway region")
	flag.IntVar(&heartbeat, "heartbeat-seconds", heartbeat, "heartbeat interval in seconds")
	flag.Parse()

	fileCfg, err := loadConnectorConfigFile(*configPath)
	if err != nil {
		return connector.DaemonConfig{}, connectorConfigFile{}, false, err
	}
	if os.Getenv("143_API_URL") == "" && fileCfg.APIURL != "" {
		*apiURL = fileCfg.APIURL
	}
	if os.Getenv("143_CONNECTOR_IDENTITY_PATH") == "" && fileCfg.IdentityPath != "" {
		*identityPath = fileCfg.IdentityPath
	}
	if os.Getenv("143_CONNECTOR_STATE_PATH") == "" && fileCfg.StatePath != "" {
		*statePath = fileCfg.StatePath
	}
	if os.Getenv("143_CONNECTOR_NAME") == "" && fileCfg.Connector.Name != "" {
		*instanceName = fileCfg.Connector.Name
	}
	if os.Getenv("143_CONNECTOR_REGION") == "" && fileCfg.Connector.GatewayRegion != "" {
		*gatewayRegion = fileCfg.Connector.GatewayRegion
	}
	if os.Getenv("143_CONNECTOR_HEARTBEAT_SECONDS") == "" && fileCfg.Connector.HeartbeatSeconds > 0 {
		heartbeat = fileCfg.Connector.HeartbeatSeconds
	}

	return connector.DaemonConfig{
		APIURL:                   *apiURL,
		DeploymentToken:          deploymentToken(*tokenFile),
		ConfigPath:               *configPath,
		IdentityPath:             *identityPath,
		StatePath:                *statePath,
		InstanceName:             *instanceName,
		Version:                  version,
		Protocol:                 models.PrivateConnectorProtocolWebSocket,
		GatewayRegion:            *gatewayRegion,
		HeartbeatIntervalSeconds: heartbeat,
	}, fileCfg, *registerOnly, nil
}

type connectorConfigFile struct {
	Loaded       bool                               `yaml:"-" json:"-"`
	APIURL       string                             `yaml:"api_url"`
	Providers    string                             `yaml:"providers"`
	StatePath    string                             `yaml:"state_path"`
	IdentityPath string                             `yaml:"identity_path"`
	Connector    connectorConfigConnector           `yaml:"connector"`
	Resources    map[string]connectorConfigResource `yaml:"resources"`
}

func (cfg connectorConfigFile) ManagesResourceConfig() bool {
	return cfg.Loaded && len(cfg.Resources) > 0
}

type connectorConfigConnector struct {
	Name             string `yaml:"name"`
	Environment      string `yaml:"environment"`
	GatewayRegion    string `yaml:"gateway_region"`
	HeartbeatSeconds int    `yaml:"heartbeat_seconds"`
}

type connectorConfigResource struct {
	ID                string                `yaml:"id" json:"id"`
	Type              string                `yaml:"type" json:"type"`
	Mode              string                `yaml:"mode" json:"mode"`
	URL               string                `yaml:"url" json:"url"`
	BaseURL           string                `yaml:"base_url" json:"base_url"`
	FieldNamesURL     string                `yaml:"field_names_url" json:"field_names_url"`
	TokenEnv          string                `yaml:"token_env" json:"token_env"`
	TokenFile         string                `yaml:"token_file" json:"token_file"`
	DSN               string                `yaml:"dsn" json:"dsn"`
	DSNEnv            string                `yaml:"dsn_env" json:"dsn_env"`
	DSNFile           string                `yaml:"dsn_file" json:"dsn_file"`
	DefaultFilter     string                `yaml:"default_filter" json:"default_filter"`
	DefaultFilters    map[string]string     `yaml:"default_filters" json:"default_filters"`
	AllowedFields     []string              `yaml:"allowed_fields" json:"allowed_fields"`
	DeniedFields      []string              `yaml:"denied_fields" json:"denied_fields"`
	AllowedSchemas    []string              `yaml:"allowed_schemas" json:"allowed_schemas"`
	DeniedTables      []string              `yaml:"denied_tables" json:"denied_tables"`
	PIIColumns        []string              `yaml:"pii_columns" json:"pii_columns"`
	PIIColumnPatterns []string              `yaml:"pii_column_patterns" json:"pii_column_patterns"`
	AllowSampleRows   bool                  `yaml:"allow_sample_rows" json:"allow_sample_rows"`
	RedactFields      []string              `yaml:"redact_fields" json:"redact_fields"`
	MaxRows           int                   `yaml:"max_rows" json:"max_rows"`
	QueryTimeout      string                `yaml:"query_timeout" json:"query_timeout"`
	Limits            connectorConfigLimits `yaml:"limits" json:"limits"`
}

type connectorConfigLimits struct {
	MaxRows              int    `yaml:"max_rows" json:"max_rows"`
	Timeout              string `yaml:"timeout" json:"timeout"`
	StatementTimeoutMS   int    `yaml:"statement_timeout_ms" json:"statement_timeout_ms"`
	MaxTimeRange         string `yaml:"max_time_range" json:"max_time_range"`
	MaxQueryWindow       string `yaml:"max_query_window" json:"max_query_window"`
	MaxSeriesCardinality int    `yaml:"max_series_cardinality" json:"max_series_cardinality"`
	MaxRequestsPerMinute int    `yaml:"max_requests_per_minute" json:"max_requests_per_minute"`
	MaxActiveLeases      int    `yaml:"max_active_leases" json:"max_active_leases"`
	MaxLeaseDuration     string `yaml:"max_lease_duration" json:"max_lease_duration"`
}

func loadConnectorConfigFile(path string) (connectorConfigFile, error) {
	if strings.TrimSpace(path) == "" {
		return connectorConfigFile{}, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return connectorConfigFile{}, nil
	}
	if err != nil {
		return connectorConfigFile{}, fmt.Errorf("read connector config file: %w", err)
	}
	var cfg connectorConfigFile
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return connectorConfigFile{}, fmt.Errorf("parse connector config file: %w", err)
	}
	cfg.Loaded = true
	return cfg, nil
}

func parsePublicKeyBase64(value string) (ed25519.PublicKey, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode gateway public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("decode gateway public key: expected %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

func buildProviderRegistryFromEnv(httpClient *http.Client) (*connector.ProviderRegistry, map[uuid.UUID]struct{}, []string, error) {
	registry := connector.NewProviderRegistry()
	resourceIDs := make(map[uuid.UUID]struct{})
	capabilities := make([]string, 0)
	victoriaLogsURL := envString("143_CONNECTOR_VICTORIALOGS_URL", "")
	victoriaLogsResourceID := envString("143_CONNECTOR_VICTORIALOGS_RESOURCE_ID", "")
	if victoriaLogsURL != "" || victoriaLogsResourceID != "" {
		if victoriaLogsURL == "" || victoriaLogsResourceID == "" {
			return nil, nil, nil, fmt.Errorf("143_CONNECTOR_VICTORIALOGS_URL and 143_CONNECTOR_VICTORIALOGS_RESOURCE_ID must be set together")
		}
		resourceID, err := uuid.Parse(victoriaLogsResourceID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("parse 143_CONNECTOR_VICTORIALOGS_RESOURCE_ID: %w", err)
		}
		authToken, err := envSecret("143_CONNECTOR_VICTORIALOGS_TOKEN", "143_CONNECTOR_VICTORIALOGS_TOKEN_FILE")
		if err != nil {
			return nil, nil, nil, err
		}
		provider := connector.NewVictoriaLogsProvider(resourceID, integration.NewVictoriaLogsProvider(integration.VictoriaLogsConfig{
			QueryURL:      victoriaLogsURL,
			FieldNamesURL: envString("143_CONNECTOR_VICTORIALOGS_FIELD_NAMES_URL", ""),
			AuthToken:     authToken,
			HTTPClient:    httpClient,
		}), connector.VictoriaLogsPolicy{
			MaxRows:              envInt("143_CONNECTOR_VICTORIALOGS_MAX_ROWS", integration.LogDefaultLimit),
			MaxQueryWindow:       durationFromEnv("143_CONNECTOR_VICTORIALOGS_MAX_QUERY_WINDOW", "143_CONNECTOR_VICTORIALOGS_MAX_TIME_RANGE"),
			MaxSeriesCardinality: envInt("143_CONNECTOR_VICTORIALOGS_MAX_SERIES_CARDINALITY", 0),
			MaxRequestsPerMinute: envInt("143_CONNECTOR_VICTORIALOGS_MAX_REQUESTS_PER_MINUTE", 0),
			DefaultFilter:        envString("143_CONNECTOR_VICTORIALOGS_DEFAULT_FILTER", ""),
			RedactFields:         envCSV("143_CONNECTOR_VICTORIALOGS_REDACT_FIELDS"),
			AllowedFields:        envCSV("143_CONNECTOR_VICTORIALOGS_ALLOWED_FIELDS"),
			DeniedFields:         envCSV("143_CONNECTOR_VICTORIALOGS_DENIED_FIELDS"),
		})
		if err := registry.Register(provider); err != nil {
			return nil, nil, nil, err
		}
		resourceIDs[resourceID] = struct{}{}
		capabilities = append(capabilities, provider.Capabilities()...)
	}
	postgresURL, err := envSecret("143_CONNECTOR_POSTGRES_DATABASE_URL", "143_CONNECTOR_POSTGRES_DATABASE_URL_FILE")
	if err != nil {
		return nil, nil, nil, err
	}
	postgresResourceID := envString("143_CONNECTOR_POSTGRES_RESOURCE_ID", "")
	if postgresURL != "" || postgresResourceID != "" {
		if postgresURL == "" || postgresResourceID == "" {
			return nil, nil, nil, fmt.Errorf("143_CONNECTOR_POSTGRES_DATABASE_URL and 143_CONNECTOR_POSTGRES_RESOURCE_ID must be set together")
		}
		resourceID, parseErr := uuid.Parse(postgresResourceID)
		if parseErr != nil {
			return nil, nil, nil, fmt.Errorf("parse 143_CONNECTOR_POSTGRES_RESOURCE_ID: %w", parseErr)
		}
		provider := connector.NewPostgresProviderFromConnString(resourceID, postgresURL, connector.PostgresPolicy{
			MaxRows:            envInt("143_CONNECTOR_POSTGRES_MAX_ROWS", 100),
			StatementTimeoutMs: envInt("143_CONNECTOR_POSTGRES_STATEMENT_TIMEOUT_MS", 5000),
			RedactColumns:      envCSV("143_CONNECTOR_POSTGRES_REDACT_COLUMNS"),
		})
		if err := registry.Register(provider); err != nil {
			return nil, nil, nil, err
		}
		resourceIDs[resourceID] = struct{}{}
		capabilities = append(capabilities, provider.Capabilities()...)
	}
	return registry, resourceIDs, capabilities, nil
}

func buildProviderRegistryFromConfig(cfg connectorConfigFile, httpClient *http.Client) (*connector.ProviderRegistry, map[uuid.UUID]struct{}, []string, error) {
	registry := connector.NewProviderRegistry()
	resourceIDs := make(map[uuid.UUID]struct{})
	capabilities := make([]string, 0)
	for name, resource := range cfg.Resources {
		resource = normalizeConnectorConfigResource(resource)
		resourceID, err := uuid.Parse(strings.TrimSpace(resource.ID))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("parse resource %s id: %w", name, err)
		}
		switch strings.ToLower(strings.TrimSpace(resource.Type)) {
		case "victorialogs":
			if strings.TrimSpace(resource.URL) == "" {
				return nil, nil, nil, fmt.Errorf("resource %s victorialogs url is required", name)
			}
			authToken, err := secretFromRefs(resource.TokenEnv, resource.TokenFile)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("resource %s token: %w", name, err)
			}
			policy, err := victoriaLogsPolicyFromResource(resource)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("resource %s victorialogs policy: %w", name, err)
			}
			provider := connector.NewVictoriaLogsProvider(resourceID, integration.NewVictoriaLogsProvider(integration.VictoriaLogsConfig{
				QueryURL:      resource.URL,
				FieldNamesURL: resource.FieldNamesURL,
				AuthToken:     authToken,
				HTTPClient:    httpClient,
			}), policy)
			if err := registry.Register(provider); err != nil {
				return nil, nil, nil, err
			}
			resourceIDs[resourceID] = struct{}{}
			capabilities = append(capabilities, provider.Capabilities()...)
		case "postgres":
			connString, err := connectionStringFromResource(resource)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("resource %s postgres dsn: %w", name, err)
			}
			timeoutMs, err := statementTimeoutMillis(resource.Limits)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("resource %s postgres timeout: %w", name, err)
			}
			provider := connector.NewPostgresProviderFromConnString(resourceID, connString, connector.PostgresPolicy{
				MaxRows:            resource.Limits.MaxRows,
				StatementTimeoutMs: timeoutMs,
				RedactColumns:      resource.PIIColumns,
				RedactPatterns:     resource.PIIColumnPatterns,
				AllowedSchemas:     resource.AllowedSchemas,
				DeniedTables:       resource.DeniedTables,
				AllowSampleRows:    resource.AllowSampleRows,
			})
			if err := registry.Register(provider); err != nil {
				return nil, nil, nil, err
			}
			resourceIDs[resourceID] = struct{}{}
			capabilities = append(capabilities, provider.Capabilities()...)
		case "":
			return nil, nil, nil, fmt.Errorf("resource %s type is required", name)
		default:
			return nil, nil, nil, fmt.Errorf("resource %s type %q is unsupported", name, resource.Type)
		}
	}
	return registry, resourceIDs, capabilities, nil
}

func normalizeConnectorConfigResource(resource connectorConfigResource) connectorConfigResource {
	if strings.TrimSpace(resource.URL) == "" && strings.TrimSpace(resource.BaseURL) != "" {
		resource.URL = resource.BaseURL
	}
	if resource.Limits.MaxRows <= 0 && resource.MaxRows > 0 {
		resource.Limits.MaxRows = resource.MaxRows
	}
	if strings.TrimSpace(resource.Limits.Timeout) == "" && strings.TrimSpace(resource.QueryTimeout) != "" {
		resource.Limits.Timeout = resource.QueryTimeout
	}
	if strings.TrimSpace(resource.Limits.MaxQueryWindow) == "" && strings.TrimSpace(resource.Limits.MaxTimeRange) != "" {
		resource.Limits.MaxQueryWindow = resource.Limits.MaxTimeRange
	}
	if strings.TrimSpace(resource.DefaultFilter) == "" && len(resource.DefaultFilters) > 0 {
		resource.DefaultFilter = defaultFilterExpression(resource.DefaultFilters)
	}
	return resource
}

func victoriaLogsPolicyFromResource(resource connectorConfigResource) (connector.VictoriaLogsPolicy, error) {
	window, err := parseOptionalDuration(resource.Limits.MaxQueryWindow)
	if err != nil {
		return connector.VictoriaLogsPolicy{}, err
	}
	return connector.VictoriaLogsPolicy{
		MaxRows:              resource.Limits.MaxRows,
		MaxQueryWindow:       window,
		MaxSeriesCardinality: resource.Limits.MaxSeriesCardinality,
		MaxRequestsPerMinute: resource.Limits.MaxRequestsPerMinute,
		DefaultFilter:        resource.DefaultFilter,
		RedactFields:         resource.RedactFields,
		AllowedFields:        resource.AllowedFields,
		DeniedFields:         resource.DeniedFields,
	}, nil
}

type connectorProviderState struct {
	mu                 sync.RWMutex
	registry           *connector.ProviderRegistry
	resourceIDs        map[uuid.UUID]struct{}
	capabilities       []string
	localConfigManaged bool
	httpClient         *http.Client
	configPath         string
	lastConfigVersion  int64
}

func newConnectorProviderState(registry *connector.ProviderRegistry, resourceIDs map[uuid.UUID]struct{}, capabilities []string, localConfigManaged bool, httpClient *http.Client, configPath string) *connectorProviderState {
	if registry == nil {
		registry = connector.NewProviderRegistry()
	}
	return &connectorProviderState{
		registry:           registry,
		resourceIDs:        copyResourceIDs(resourceIDs),
		capabilities:       copyStrings(capabilities),
		localConfigManaged: localConfigManaged,
		httpClient:         httpClient,
		configPath:         configPath,
	}
}

func (s *connectorProviderState) ResourceIDs() map[uuid.UUID]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyResourceIDs(s.resourceIDs)
}

func (s *connectorProviderState) Capabilities() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyStrings(s.capabilities)
}

func (s *connectorProviderState) ApplyConfigPush(_ context.Context, frame connector.ConfigPushFrame) (connector.ConfigApplyResult, error) {
	if s.localConfigManaged {
		return connector.ConfigApplyResult{}, errLocalConfigManaged
	}
	s.mu.RLock()
	lastVersion := s.lastConfigVersion
	s.mu.RUnlock()
	if frame.Version < lastVersion {
		return connector.ConfigApplyResult{}, connector.ErrConfigPushStale
	}

	cfg, err := connectorConfigFileFromPush(frame)
	if err != nil {
		return connector.ConfigApplyResult{}, err
	}
	nextRegistry, nextResourceIDs, nextCapabilities, err := buildProviderRegistryFromConfig(cfg, s.httpClient)
	if err != nil {
		return connector.ConfigApplyResult{}, err
	}
	s.registry.ReplaceWith(nextRegistry)
	s.mu.Lock()
	s.resourceIDs = copyResourceIDs(nextResourceIDs)
	s.capabilities = copyStrings(nextCapabilities)
	s.lastConfigVersion = frame.Version
	s.mu.Unlock()
	return connector.ConfigApplyResult{Version: frame.Version, Capabilities: copyStrings(nextCapabilities)}, nil
}

func (s *connectorProviderState) ReloadFromFile(context.Context) error {
	if !s.localConfigManaged {
		return errors.New("local connector config file is not active")
	}
	cfg, err := loadConnectorConfigFile(s.configPath)
	if err != nil {
		return err
	}
	if !cfg.Loaded {
		return errors.New("local connector config file is not present")
	}
	nextRegistry, nextResourceIDs, nextCapabilities, err := buildProviderRegistryFromConfig(cfg, s.httpClient)
	if err != nil {
		return err
	}
	s.registry.ReplaceWith(nextRegistry)
	s.mu.Lock()
	s.resourceIDs = copyResourceIDs(nextResourceIDs)
	s.capabilities = copyStrings(nextCapabilities)
	s.mu.Unlock()
	return nil
}

func connectorConfigFileFromPush(frame connector.ConfigPushFrame) (connectorConfigFile, error) {
	resources := make(map[string]connectorConfigResource, len(frame.Resources))
	for _, pushed := range frame.Resources {
		var resource connectorConfigResource
		if len(pushed.Config) > 0 {
			if err := json.Unmarshal(pushed.Config, &resource); err != nil {
				return connectorConfigFile{}, fmt.Errorf("parse resource %s config: %w", pushed.ID, err)
			}
		}
		resource.ID = pushed.ID.String()
		resource.Type = pushed.ResourceType
		resource.Mode = pushed.Mode
		resources[pushed.ID.String()] = normalizeConnectorConfigResource(resource)
	}
	return connectorConfigFile{Resources: resources}, nil
}

func copyResourceIDs(in map[uuid.UUID]struct{}) map[uuid.UUID]struct{} {
	out := make(map[uuid.UUID]struct{}, len(in))
	for id := range in {
		out[id] = struct{}{}
	}
	return out
}

func copyStrings(in []string) []string {
	return append([]string(nil), in...)
}

func connectionStringFromResource(resource connectorConfigResource) (string, error) {
	if strings.TrimSpace(resource.DSN) != "" {
		return strings.TrimSpace(resource.DSN), nil
	}
	value, err := secretFromRefs(resource.DSNEnv, resource.DSNFile)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("dsn, dsn_env, or dsn_file is required")
	}
	return value, nil
}

func statementTimeoutMillis(limits connectorConfigLimits) (int, error) {
	if limits.StatementTimeoutMS > 0 {
		return limits.StatementTimeoutMS, nil
	}
	if strings.TrimSpace(limits.Timeout) == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(limits.Timeout))
	if err != nil {
		return 0, err
	}
	return int(d / time.Millisecond), nil
}

func parseOptionalDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func durationFromEnv(primary string, aliases ...string) time.Duration {
	names := append([]string{primary}, aliases...)
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			d, err := time.ParseDuration(value)
			if err == nil {
				return d
			}
		}
	}
	return 0
}

func defaultFilterExpression(filters map[string]string) string {
	if len(filters) == 0 {
		return ""
	}
	parts := make([]string, 0, len(filters))
	for key, value := range filters {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		parts = append(parts, key+":"+value)
	}
	return strings.Join(parts, " AND ")
}

func runUpdateCommand(ctx context.Context, command string) (connector.UpdateResult, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return connector.UpdateResult{}, errors.New("connector update command is not configured")
	}
	updateCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(updateCtx, "/bin/sh", "-c", command)
	output, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(output))
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return connector.UpdateResult{}, fmt.Errorf("run connector update command: %s", message)
	}
	return connector.UpdateResult{Started: true, Message: message}, nil
}

func secretFromRefs(envName string, fileName string) (string, error) {
	if strings.TrimSpace(envName) != "" {
		return strings.TrimSpace(os.Getenv(strings.TrimSpace(envName))), nil
	}
	if strings.TrimSpace(fileName) == "" {
		return "", nil
	}
	raw, err := os.ReadFile(strings.TrimSpace(fileName))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func envSecret(valueName string, fileName string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(valueName)); value != "" {
		return value, nil
	}
	path := strings.TrimSpace(os.Getenv(fileName))
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", fileName, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func envCSV(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func deploymentToken(path string) string {
	if token := strings.TrimSpace(os.Getenv("143_CONNECTOR_TOKEN")); token != "" {
		return token
	}
	if strings.TrimSpace(path) == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func envString(name string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envBool(name string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return value == "1" || value == "true" || value == "yes"
}

func hostname() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "connector"
	}
	return host
}
