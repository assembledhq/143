package preview

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
)

// =============================================================================
// Supported platform infrastructure templates
// =============================================================================

// InfraTemplate describes a platform-provided infrastructure template.
type InfraTemplate struct {
	Image        string
	DefaultPort  int
	HealthCmd    []string
	DefaultMemMB int
	DefaultCPU   float64
	MaxMemMB     int
}

var supportedTemplates = map[string]InfraTemplate{
	"postgres-17": {Image: "postgres:17-alpine", DefaultPort: 5432, HealthCmd: []string{"pg_isready"}, DefaultMemMB: 256, DefaultCPU: 0.25, MaxMemMB: 512},
	"postgres-16": {Image: "postgres:16-alpine", DefaultPort: 5432, HealthCmd: []string{"pg_isready"}, DefaultMemMB: 256, DefaultCPU: 0.25, MaxMemMB: 512},
	"redis-7":     {Image: "redis:7-alpine", DefaultPort: 6379, HealthCmd: []string{"redis-cli", "ping"}, DefaultMemMB: 128, DefaultCPU: 0.1, MaxMemMB: 256},
	"mysql-8":     {Image: "mysql:8.4", DefaultPort: 3306, HealthCmd: []string{"mysqladmin", "ping"}, DefaultMemMB: 384, DefaultCPU: 0.25, MaxMemMB: 768},
}

// LookupInfraTemplate returns the template definition for a known template name.
func LookupInfraTemplate(name string) (InfraTemplate, bool) {
	t, ok := supportedTemplates[name]
	return t, ok
}

// =============================================================================
// Constraints
// =============================================================================

const (
	MaxServicesPerConfig = 4
	MaxInfraPerConfig    = 2
	MinPort              = 1024
	MaxPort              = 65535
)

// =============================================================================
// Raw preview config (direct JSON unmarshal of the nested "preview" section in
// .143/config.json).
// =============================================================================

// rawPreviewConfig is the direct JSON representation of the nested preview
// section inside .143/config.json.
// It supports both single-service (top-level command/port) and multi-service
// (services map) formats.
type rawPreviewConfig struct {
	Version        string                                 `json:"version"`
	Name           string                                 `json:"name"`
	Primary        string                                 `json:"primary,omitempty"`
	Services       map[string]models.ServiceConfig        `json:"services,omitempty"`
	Infrastructure map[string]models.InfrastructureConfig `json:"infrastructure,omitempty"`
	Credentials    models.CredentialConfig                `json:"credentials"`
	Network        models.NetworkConfig                   `json:"network"`
	Progressive    bool                                   `json:"progressive,omitempty"`

	// Single-service top-level fields (mutually exclusive with Services).
	Command []string               `json:"command,omitempty"`
	Cwd     string                 `json:"cwd,omitempty"`
	Port    int                    `json:"port,omitempty"`
	Env     map[string]string      `json:"env,omitempty"`
	Ready   *models.ReadinessProbe `json:"ready,omitempty"`
}

// isSingleService returns true if the config uses single-service top-level format.
func (r *rawPreviewConfig) isSingleService() bool {
	return len(r.Services) == 0 && len(r.Command) > 0
}

// hasBothFormats returns true if the config has both single-service and
// multi-service fields set, which is ambiguous and should be rejected.
func (r *rawPreviewConfig) hasBothFormats() bool {
	return len(r.Services) > 0 && (len(r.Command) > 0 || r.Port > 0)
}

// =============================================================================
// ParseConfig
// =============================================================================

// ParseConfig parses the nested preview section from .143/config.json and
// normalizes single-service configs to the multi-service format.
func ParseConfig(data []byte) (*models.PreviewConfig, error) {
	previewData, err := extractPreviewSection(data)
	if err != nil {
		return nil, err
	}

	var raw rawPreviewConfig
	if err := json.Unmarshal(previewData, &raw); err != nil {
		return nil, fmt.Errorf("parse preview config: %w", err)
	}

	if raw.hasBothFormats() {
		return nil, fmt.Errorf("config must use either single-service (top-level command/port) or multi-service (services map) format, not both")
	}

	cfg := &models.PreviewConfig{
		Version:        raw.Version,
		Name:           raw.Name,
		Infrastructure: raw.Infrastructure,
		Credentials:    raw.Credentials,
		Network:        raw.Network,
		Progressive:    raw.Progressive,
	}

	if raw.isSingleService() {
		// Normalize single-service to multi-service format.
		svcName := raw.Name
		if svcName == "" {
			svcName = "app"
		}
		ready := models.ReadinessProbe{HTTPPath: "/", TimeoutSeconds: 90}
		if raw.Ready != nil {
			ready = *raw.Ready
		}
		cfg.Primary = svcName
		cfg.Services = map[string]models.ServiceConfig{
			svcName: {
				Command: raw.Command,
				Cwd:     raw.Cwd,
				Port:    raw.Port,
				Env:     raw.Env,
				Ready:   ready,
			},
		}
	} else {
		cfg.Primary = raw.Primary
		cfg.Services = raw.Services
	}

	if cfg.Infrastructure == nil {
		cfg.Infrastructure = make(map[string]models.InfrastructureConfig)
	}

	return cfg, nil
}

func extractPreviewSection(data []byte) ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return data, nil
	}
	_, hasPreview := probe["preview"]
	_, hasBootstrap := probe["bootstrap"]
	_, hasValidation := probe["validation"]
	if !hasPreview && !hasBootstrap && !hasValidation {
		return data, nil
	}
	if _, hasLegacyPreviewFields := probe["services"]; hasLegacyPreviewFields {
		return data, nil
	}
	if _, hasLegacyPreviewFields := probe["command"]; hasLegacyPreviewFields {
		return data, nil
	}
	if !hasPreview {
		return nil, fmt.Errorf("parse preview config: missing preview section in %s", repoconfig.ConfigPath)
	}
	cfg, err := repoconfig.Parse(data)
	if err != nil || len(cfg.Preview) == 0 {
		if err != nil {
			return nil, fmt.Errorf("parse preview config: %w", err)
		}
		return nil, fmt.Errorf("parse preview config: missing preview section in %s", repoconfig.ConfigPath)
	}
	return cfg.Preview, nil
}

// =============================================================================
// ValidateConfig
// =============================================================================

// ValidateConfig checks all structural and security constraints on a parsed config.
func ValidateConfig(cfg *models.PreviewConfig) []string {
	var errs []string

	// Primary must reference an existing service.
	if cfg.Primary == "" {
		errs = append(errs, "primary service name is required")
	} else if _, ok := cfg.Services[cfg.Primary]; !ok {
		errs = append(errs, fmt.Sprintf("primary %q does not reference a service in the services map", cfg.Primary))
	}

	// Service count.
	if len(cfg.Services) == 0 {
		errs = append(errs, "at least one service is required")
	}
	if len(cfg.Services) > MaxServicesPerConfig {
		errs = append(errs, fmt.Sprintf("at most %d services allowed, got %d", MaxServicesPerConfig, len(cfg.Services)))
	}

	// Infrastructure count.
	if len(cfg.Infrastructure) > MaxInfraPerConfig {
		errs = append(errs, fmt.Sprintf("at most %d infrastructure services allowed, got %d", MaxInfraPerConfig, len(cfg.Infrastructure)))
	}

	// Per-service validation.
	ports := make(map[int]string)
	for name, svc := range cfg.Services {
		if len(svc.Command) == 0 {
			errs = append(errs, fmt.Sprintf("service %q: command is required", name))
		}
		if svc.Port < MinPort || svc.Port > MaxPort {
			errs = append(errs, fmt.Sprintf("service %q: port %d must be in range %d-%d", name, svc.Port, MinPort, MaxPort))
		}
		if other, dup := ports[svc.Port]; dup {
			errs = append(errs, fmt.Sprintf("service %q: port %d conflicts with service %q", name, svc.Port, other))
		}
		ports[svc.Port] = name

		if svc.Cwd != "" {
			errs = append(errs, validatePathInsideRepo("service "+name+": cwd", svc.Cwd)...)
		}
		if svc.Ready.HTTPPath == "" {
			errs = append(errs, fmt.Sprintf("service %q: ready.http_path is required", name))
		} else if !isValidHTTPPath(svc.Ready.HTTPPath) {
			errs = append(errs, fmt.Sprintf("service %q: ready.http_path %q contains invalid characters (must match /[a-zA-Z0-9/_.-?&=%%]*)", name, svc.Ready.HTTPPath))
		}
	}

	// Per-infrastructure validation.
	for name, infra := range cfg.Infrastructure {
		if _, ok := supportedTemplates[infra.Template]; !ok {
			errs = append(errs, fmt.Sprintf("infrastructure %q: template %q is not supported", name, infra.Template))
		}
		if infra.InitScript != "" {
			errs = append(errs, validatePathInsideRepo("infrastructure "+name+": init_script", infra.InitScript)...)
		}
		for _, svcName := range infra.InjectInto {
			if _, ok := cfg.Services[svcName]; !ok {
				errs = append(errs, fmt.Sprintf("infrastructure %q: inject_into references unknown service %q", name, svcName))
			}
		}
	}

	// Network mode validation.
	switch cfg.Network.Mode {
	case "managed", "":
		// OK
	default:
		errs = append(errs, fmt.Sprintf("network.mode %q is not supported (expected \"managed\")", cfg.Network.Mode))
	}

	// Credential inject_into validation.
	for _, svcName := range cfg.Credentials.InjectInto {
		if _, ok := cfg.Services[svcName]; !ok {
			errs = append(errs, fmt.Sprintf("credentials: inject_into references unknown service %q", svcName))
		}
	}

	return errs
}

// validHTTPPath only allows safe characters in readiness probe paths to prevent
// shell injection when the path is interpolated into a curl command.
var validHTTPPath = regexp.MustCompile(`^/[a-zA-Z0-9/_.\-?&=%]*$`)

// isValidHTTPPath checks that a readiness probe HTTP path contains only safe characters.
func isValidHTTPPath(path string) bool {
	return validHTTPPath.MatchString(path)
}

// validatePathInsideRepo checks that a relative path does not escape the repo root.
func validatePathInsideRepo(field, path string) []string {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) {
		return []string{fmt.Sprintf("%s: must be a relative path, got %q", field, path)}
	}
	if strings.HasPrefix(clean, "..") {
		return []string{fmt.Sprintf("%s: path %q escapes the repo root", field, path)}
	}
	return nil
}

// =============================================================================
// Trust split resolution
// =============================================================================

// ResolveConfig merges a base branch config with a session diff config using the
// trust split rules from the design doc:
//   - Security-sensitive fields come from baseCfg
//   - Runtime behavior fields come from diffCfg
//
// For connected previews (credentials.mode != "none"), ALL fields are pinned to
// the base branch.
func ResolveConfig(baseCfg, diffCfg *models.PreviewConfig) *models.PreviewConfig {
	resolved := &models.PreviewConfig{
		Version:     baseCfg.Version,
		Name:        baseCfg.Name,
		Progressive: baseCfg.Progressive,

		// Security-sensitive: always from base.
		Primary:     baseCfg.Primary,
		Credentials: baseCfg.Credentials,
		Network:     baseCfg.Network,
	}

	// Infrastructure: structure from base, init_script from diff (unless connected).
	resolved.Infrastructure = make(map[string]models.InfrastructureConfig, len(baseCfg.Infrastructure))
	for name, baseInfra := range baseCfg.Infrastructure {
		infra := baseInfra
		if !IsConnected(baseCfg) {
			if diffInfra, ok := diffCfg.Infrastructure[name]; ok {
				infra.InitScript = diffInfra.InitScript
			}
		}
		resolved.Infrastructure[name] = infra
	}

	// Services: set of names from base. Per-service runtime fields from diff
	// (unless connected, in which case all pinned to base).
	resolved.Services = make(map[string]models.ServiceConfig, len(baseCfg.Services))
	for name, baseSvc := range baseCfg.Services {
		if IsConnected(baseCfg) {
			// Connected preview: all fields pinned to base branch.
			resolved.Services[name] = baseSvc
		} else {
			// Non-connected: runtime fields from diff, existence from base.
			if diffSvc, ok := diffCfg.Services[name]; ok {
				resolved.Services[name] = models.ServiceConfig{
					Command: diffSvc.Command,
					Cwd:     diffSvc.Cwd,
					Port:    diffSvc.Port,
					Env:     diffSvc.Env,
					Ready:   diffSvc.Ready,
				}
			} else {
				// Service exists in base but not in diff — use base.
				resolved.Services[name] = baseSvc
			}
		}
	}

	return resolved
}

// IsConnected returns true if the config references managed credentials or destinations.
func IsConnected(cfg *models.PreviewConfig) bool {
	if cfg.Credentials.Mode != "" && cfg.Credentials.Mode != "none" {
		return true
	}
	if len(cfg.Network.Destinations) > 0 {
		return true
	}
	return false
}

// =============================================================================
// DetectReadiness
// =============================================================================

// DetectReadiness determines whether a repo can run a preview.
func DetectReadiness(cfg *models.PreviewConfig) models.PreviewDetectionResult {
	result := models.PreviewDetectionResult{
		ConfigName:     cfg.Name,
		PrimaryService: cfg.Primary,
	}

	for name := range cfg.Services {
		result.Services = append(result.Services, name)
	}
	for name := range cfg.Infrastructure {
		result.Infrastructure = append(result.Infrastructure, name)
	}

	// Check validation errors first.
	if errs := ValidateConfig(cfg); len(errs) > 0 {
		result.Readiness = models.PreviewReadinessNotSupported
		result.ValidationErrors = errs
		return result
	}

	// Check if credentials/network need admin setup.
	if cfg.Credentials.Mode != "" && cfg.Credentials.Mode != "none" {
		result.MissingCredentials = append(result.MissingCredentials, models.MissingCredential{
			CredentialSet: cfg.Credentials.CredentialSet,
			EnvVars:       cfg.Credentials.Env,
		})
	}
	if len(cfg.Network.Destinations) > 0 {
		result.MissingDestinations = cfg.Network.Destinations
	}

	if IsConnected(cfg) {
		result.Readiness = models.PreviewReadinessAdminSetupRequired
	} else {
		result.Readiness = models.PreviewReadinessReady
	}

	return result
}

// =============================================================================
// Resource limit resolution
// =============================================================================

// ResolveResourceLimits returns the appropriate resource limits based on config.
func ResolveResourceLimits(cfg *models.PreviewConfig) models.ResourceLimits {
	if len(cfg.Services) > 1 {
		return models.ResourceLimits{MemoryMB: 1024, CPUMillis: 1000}
	}
	return models.ResourceLimits{MemoryMB: 512, CPUMillis: 500}
}
