package preview

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
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
	"postgres-15": {Image: "postgres:15-alpine", DefaultPort: 5432, HealthCmd: []string{"pg_isready"}, DefaultMemMB: 256, DefaultCPU: 0.25, MaxMemMB: 512},
	"postgres-14": {Image: "postgres:14-alpine", DefaultPort: 5432, HealthCmd: []string{"pg_isready"}, DefaultMemMB: 256, DefaultCPU: 0.25, MaxMemMB: 512},
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

	DefaultInstallTimeoutSeconds = 420
	MaxInstallTimeoutSeconds     = 1800

	DefaultPreviewDiskMiB = 10 * 1024
	// Per-topology memory defaults (MiB). Large frontend dev servers (e.g. an
	// rspack/webpack build running alongside a backend service) routinely need
	// several GiB; the previous 384/768/1024 defaults caused the dev server to
	// be OOM-killed mid-response, truncating bundles and serving a blank page.
	DefaultSingleServiceMemory = 1024
	DefaultMultiServiceMemory  = 2048
	DefaultInfraServiceMemory  = 4096
	MaxPreviewCPUMillis        = models.MaxPreviewMaxCPUMillis
	// MaxPreviewMemoryMiB is the ceiling a repo may request via
	// preview.resources.{requests,limits}.memory in .143/config.json. Defaults
	// stay modest (above) to bound per-worker capacity; repos that need more
	// opt in explicitly up to this cap.
	MaxPreviewMemoryMiB        = models.MaxPreviewMaxMemoryMiB
	MaxPreviewEphemeralDiskMiB = models.MaxPreviewMaxEphemeralDiskMiB
)

var ErrInvalidConfig = errors.New("invalid preview config")

// ErrPreviewConfigNotFound reports that a requested named preview config is
// absent from .143/config.json. Callers that source the name from stored state
// (e.g. a saved build profile that has since been renamed) can fall back to the
// default config instead of failing the build.
var ErrPreviewConfigNotFound = errors.New("preview config not found")

func InvalidConfigMessage(err error) string {
	detail := "unknown error"
	if err != nil {
		detail = err.Error()
		detail = strings.TrimPrefix(detail, ErrInvalidConfig.Error()+": ")
		detail = strings.TrimPrefix(detail, "parse "+repoconfig.ConfigPath+": ")
		detail = strings.TrimPrefix(detail, "validate "+repoconfig.ConfigPath+": ")
	}
	return fmt.Sprintf("Invalid %s preview config: %s. Fix the committed config and start preview again.", repoconfig.ConfigPath, detail)
}

// =============================================================================
// Raw preview config (direct JSON unmarshal of the nested "preview" section in
// .143/config.json).
// =============================================================================

type previewVersion string

func (v *previewVersion) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*v = ""
		return nil
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*v = previewVersion(asString)
		return nil
	}
	var asNumber json.Number
	if err := json.Unmarshal(data, &asNumber); err == nil {
		*v = previewVersion(asNumber.String())
		return nil
	}
	return fmt.Errorf("version must be a string or number")
}

// rawPreviewConfig is the direct JSON representation of the nested preview
// section inside .143/config.json.
// It supports both single-service (top-level command/port) and multi-service
// (services map) formats.
type rawPreviewConfig struct {
	Version        previewVersion                         `json:"version"`
	Name           string                                 `json:"name"`
	Primary        string                                 `json:"primary,omitempty"`
	Install        *models.PreviewInstallConfig           `json:"install,omitempty"`
	Services       map[string]models.ServiceConfig        `json:"services,omitempty"`
	Infrastructure map[string]models.InfrastructureConfig `json:"infrastructure,omitempty"`
	Resources      models.PreviewResourceRequirements     `json:"resources,omitempty"`
	Secrets        json.RawMessage                        `json:"secrets,omitempty"`
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

// ConfigOptions is lightweight metadata for presenting committed preview
// config choices without launching a runtime.
type ConfigOptions struct {
	Names             []string `json:"names"`
	DefaultName       string   `json:"default_name,omitempty"`
	SelectedName      string   `json:"selected_name,omitempty"`
	RequiresSelection bool     `json:"requires_selection"`
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
	return ParseNamedConfig(data, "")
}

// ParseNamedConfig parses a specific named preview config from .143/config.json.
// Repos may either use the legacy single preview object or a multi-config map:
//
//	{
//	  "preview": {
//	    "default": "web",
//	    "configs": {
//	      "web": {"primary": "web", "services": {...}},
//	      "docs": {"primary": "docs", "services": {...}}
//	    }
//	  }
//	}
//
// An omitted name auto-selects the only config or the declared default.
func ParseNamedConfig(data []byte, name string) (*models.PreviewConfig, error) {
	previewData, err := extractPreviewSection(data)
	if err != nil {
		return nil, err
	}
	if selected, ok, err := selectNamedPreviewSection(previewData, name); err != nil {
		return nil, err
	} else if ok {
		previewData = selected
	}

	var raw rawPreviewConfig
	if err := json.Unmarshal(previewData, &raw); err != nil {
		return nil, fmt.Errorf("parse preview config: %w", err)
	}

	if raw.hasBothFormats() {
		return nil, fmt.Errorf("config must use either single-service (top-level command/port) or multi-service (services map) format, not both")
	}

	cfg := &models.PreviewConfig{
		Version:        string(raw.Version),
		Name:           raw.Name,
		Install:        cloneInstallConfig(raw.Install),
		Infrastructure: raw.Infrastructure,
		Resources:      raw.Resources,
		Credentials:    raw.Credentials,
		Network:        raw.Network,
		Progressive:    raw.Progressive,
	}
	secrets, err := parsePreviewSecrets(raw.Secrets)
	if err != nil {
		return nil, fmt.Errorf("parse preview.secrets: %w", err)
	}
	cfg.Secrets = secrets

	if raw.isSingleService() {
		// Normalize single-service to multi-service format.
		svcName := raw.Name
		if svcName == "" {
			svcName = "app"
		}
		ready := models.ReadinessProbe{HTTPPath: "/", TimeoutSeconds: 300}
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
	defaultPreviewInstallConfig(cfg.Install)

	return cfg, nil
}

func parsePreviewSecrets(raw json.RawMessage) ([]models.PreviewSecretBundleRef, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	var single models.PreviewSecretBundleRef
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil, err
		}
		return []models.PreviewSecretBundleRef{single}, nil
	}
	var many []models.PreviewSecretBundleRef
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("must be an object or array of objects")
	}
	return many, nil
}

// InspectConfigOptions returns available named preview configs and the config
// that would be selected for a request. It intentionally avoids full validation
// so the create form can stay fast and only needs one GitHub content fetch.
func InspectConfigOptions(data []byte, name string) (ConfigOptions, error) {
	previewData, err := extractPreviewSection(data)
	if err != nil {
		return ConfigOptions{}, err
	}
	var probe struct {
		Default string                     `json:"default"`
		Configs map[string]json.RawMessage `json:"configs"`
	}
	if err := json.Unmarshal(previewData, &probe); err != nil {
		return ConfigOptions{}, fmt.Errorf("parse preview config metadata: %w", err)
	}
	selectedName := strings.TrimSpace(name)
	if len(probe.Configs) > 0 {
		names := sortedPreviewConfigNames(probe.Configs)
		defaultName := strings.TrimSpace(probe.Default)
		if selectedName == "" {
			selectedName = defaultName
		}
		if selectedName == "" && len(names) == 1 {
			selectedName = names[0]
		}
		return ConfigOptions{
			Names:             names,
			DefaultName:       defaultName,
			SelectedName:      selectedName,
			RequiresSelection: selectedName == "",
		}, nil
	}
	var raw rawPreviewConfig
	if err := json.Unmarshal(previewData, &raw); err != nil {
		return ConfigOptions{}, fmt.Errorf("parse preview config metadata: %w", err)
	}
	configName := strings.TrimSpace(raw.Name)
	if configName == "" {
		configName = "default"
	}
	if selectedName == "" {
		selectedName = configName
	}
	return ConfigOptions{
		Names:        []string{configName},
		SelectedName: selectedName,
	}, nil
}

func selectNamedPreviewSection(previewData []byte, name string) ([]byte, bool, error) {
	var probe struct {
		Default string                     `json:"default"`
		Configs map[string]json.RawMessage `json:"configs"`
	}
	if err := json.Unmarshal(previewData, &probe); err != nil {
		return nil, false, nil
	}
	if len(probe.Configs) == 0 {
		return nil, false, nil
	}
	names := sortedPreviewConfigNames(probe.Configs)
	selectedName := strings.TrimSpace(name)
	if selectedName == "" {
		selectedName = strings.TrimSpace(probe.Default)
	}
	if selectedName == "" && len(names) == 1 {
		selectedName = names[0]
	}
	if selectedName == "" {
		return nil, false, fmt.Errorf("preview_config_name is required; available configs: %s", strings.Join(names, ", "))
	}
	selected, ok := probe.Configs[selectedName]
	if !ok {
		return nil, false, fmt.Errorf("%w: %q; available configs: %s", ErrPreviewConfigNotFound, selectedName, strings.Join(names, ", "))
	}
	merged, err := mergeNamedPreviewSection(previewData, selected)
	if err != nil {
		return nil, false, err
	}
	return merged, true, nil
}

func mergeNamedPreviewSection(baseData, selectedData []byte) ([]byte, error) {
	var base map[string]json.RawMessage
	if err := json.Unmarshal(baseData, &base); err != nil {
		return nil, fmt.Errorf("parse base preview config: %w", err)
	}
	delete(base, "default")
	delete(base, "configs")
	var selected map[string]json.RawMessage
	if err := json.Unmarshal(selectedData, &selected); err != nil {
		return nil, fmt.Errorf("parse named preview config: %w", err)
	}
	for key, value := range selected {
		if key == "install" {
			mergedInstall, err := mergeInstallSection(base[key], value)
			if err != nil {
				return nil, err
			}
			base[key] = mergedInstall
			continue
		}
		base[key] = value
	}
	out, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("marshal merged preview config: %w", err)
	}
	return out, nil
}

func mergeInstallSection(baseData, selectedData json.RawMessage) (json.RawMessage, error) {
	if len(baseData) == 0 || string(baseData) == "null" {
		return selectedData, nil
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal(baseData, &base); err != nil {
		return nil, fmt.Errorf("parse base preview.install: %w", err)
	}
	var selected map[string]json.RawMessage
	if err := json.Unmarshal(selectedData, &selected); err != nil {
		return nil, fmt.Errorf("parse named preview.install: %w", err)
	}
	for key, value := range selected {
		if key == "cache" {
			mergedCache, err := mergeObjectSection(base[key], value, "preview.install.cache")
			if err != nil {
				return nil, err
			}
			base[key] = mergedCache
			continue
		}
		base[key] = value
	}
	out, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("marshal merged preview.install: %w", err)
	}
	return out, nil
}

func mergeObjectSection(baseData, selectedData json.RawMessage, field string) (json.RawMessage, error) {
	if len(baseData) == 0 || string(baseData) == "null" {
		return selectedData, nil
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal(baseData, &base); err != nil {
		return nil, fmt.Errorf("parse base %s: %w", field, err)
	}
	var selected map[string]json.RawMessage
	if err := json.Unmarshal(selectedData, &selected); err != nil {
		return nil, fmt.Errorf("parse named %s: %w", field, err)
	}
	for key, value := range selected {
		base[key] = value
	}
	out, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("marshal merged %s: %w", field, err)
	}
	return out, nil
}

func sortedPreviewConfigNames(configs map[string]json.RawMessage) []string {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func extractPreviewSection(data []byte) ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return data, nil
	}
	_, hasPreview := probe["preview"]
	_, hasDependencies := probe["dependencies"]
	_, hasBootstrap := probe["bootstrap"]
	_, hasValidation := probe["validation"]
	if !hasPreview && !hasDependencies && !hasBootstrap && !hasValidation {
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

// ResourcePolicy is the effective org resource policy for repo-authored
// preview.resources requests. Admin settings may lower these values, but they
// cannot raise them above the platform hard caps.
type ResourcePolicy struct {
	AllowRepoResourceRequests bool
	MaxCPUMillis              int
	MaxMemoryMiB              int
	MaxDiskMiB                int
}

func defaultResourcePolicy() ResourcePolicy {
	return ResourcePolicy{
		AllowRepoResourceRequests: true,
		MaxCPUMillis:              MaxPreviewCPUMillis,
		MaxMemoryMiB:              MaxPreviewMemoryMiB,
		MaxDiskMiB:                MaxPreviewEphemeralDiskMiB,
	}
}

func ResourcePolicyFromOrgSettings(settings models.OrgSettings) ResourcePolicy {
	return normalizeResourcePolicy(ResourcePolicy{
		AllowRepoResourceRequests: settings.SandboxResources.EffectiveAllowRepoResourceRequests(),
		MaxCPUMillis:              settings.SandboxResources.PreviewMaxCPUMillis,
		MaxMemoryMiB:              settings.SandboxResources.PreviewMaxMemoryMiB,
		MaxDiskMiB:                settings.SandboxResources.PreviewMaxEphemeralDiskMiB,
	})
}

func normalizeResourcePolicy(policy ResourcePolicy) ResourcePolicy {
	defaults := defaultResourcePolicy()
	if policy.MaxCPUMillis <= 0 {
		policy.MaxCPUMillis = defaults.MaxCPUMillis
	}
	if policy.MaxCPUMillis > MaxPreviewCPUMillis {
		policy.MaxCPUMillis = MaxPreviewCPUMillis
	}
	if policy.MaxMemoryMiB <= 0 {
		policy.MaxMemoryMiB = defaults.MaxMemoryMiB
	}
	if policy.MaxMemoryMiB > MaxPreviewMemoryMiB {
		policy.MaxMemoryMiB = MaxPreviewMemoryMiB
	}
	if policy.MaxDiskMiB <= 0 {
		policy.MaxDiskMiB = defaults.MaxDiskMiB
	}
	if policy.MaxDiskMiB > MaxPreviewEphemeralDiskMiB {
		policy.MaxDiskMiB = MaxPreviewEphemeralDiskMiB
	}
	return policy
}

// ValidateConfig checks all structural and security constraints on a parsed config.
func ValidateConfig(cfg *models.PreviewConfig) []string {
	return ValidateConfigWithResourcePolicy(cfg, defaultResourcePolicy())
}

// ValidateConfigWithResourcePolicy checks structural/security constraints using
// the effective org resource policy for preview.resources.
func ValidateConfigWithResourcePolicy(cfg *models.PreviewConfig, resourcePolicy ResourcePolicy) []string {
	var errs []string
	resourcePolicy = normalizeResourcePolicy(resourcePolicy)

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
		} else {
			for i, part := range svc.Command {
				if strings.TrimSpace(part) == "" {
					errs = append(errs, fmt.Sprintf("service %q: command[%d] must not be blank", name, i))
				}
			}
		}
		for i, part := range svc.Build {
			if strings.TrimSpace(part) == "" {
				errs = append(errs, fmt.Sprintf("service %q: build[%d] must not be blank", name, i))
			}
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

	errs = append(errs, validatePreviewInstallConfig(cfg.Install)...)
	errs = append(errs, validatePreviewResources(cfg.Resources, resourcePolicy)...)

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
	for i, ref := range cfg.Secrets {
		field := fmt.Sprintf("secrets[%d]", i)
		if strings.TrimSpace(ref.Bundle) == "" {
			errs = append(errs, field+": bundle is required")
		}
		if len(ref.Services) == 0 {
			errs = append(errs, field+": services is required")
		}
		for _, svcName := range ref.Services {
			if _, ok := cfg.Services[svcName]; !ok {
				errs = append(errs, fmt.Sprintf("%s: services references unknown service %q", field, svcName))
			}
		}
		for _, envName := range ref.Env {
			if !isValidSecretEnvName(envName) {
				errs = append(errs, fmt.Sprintf("%s: env %q is not a valid environment variable name", field, envName))
			}
		}
		for _, path := range ref.Files {
			errs = append(errs, validateSecretFilePath(field+": files", path)...)
		}
		if len(ref.Files) > 0 && !secretServicesCoverAll(ref.Services, previewServiceNames(cfg.Services)) {
			errs = append(errs, field+": files are workspace-wide, so services must include every preview service")
		}
	}

	return errs
}

// validHTTPPath only allows safe characters in readiness probe paths to prevent
// shell injection when the path is interpolated into a curl command. The negative
// lookahead equivalent is implemented by rejecting /.. sequences after the
// character allowlist passes, since Go's regexp package is RE2 (no lookahead).
var validHTTPPath = regexp.MustCompile(`^/[a-zA-Z0-9/_.\-?&=%]*$`)
var traversalSequence = regexp.MustCompile(`/\.\.(/|$)`)
var validPreviewInstallCleanPath = regexp.MustCompile(`^[A-Za-z0-9_./@*+\-]+$`)
var validSecretEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func isValidSecretEnvName(name string) bool {
	return validSecretEnvName.MatchString(name)
}

func previewServiceNames(services map[string]models.ServiceConfig) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	return names
}

func secretServicesCoverAll(scopedServices []string, allServices []string) bool {
	if len(allServices) == 0 {
		return true
	}
	seen := make(map[string]struct{}, len(scopedServices))
	for _, service := range scopedServices {
		seen[service] = struct{}{}
	}
	for _, service := range allServices {
		if _, ok := seen[service]; !ok {
			return false
		}
	}
	return true
}

// isValidHTTPPath checks that a readiness probe HTTP path contains only safe
// characters and does not contain path traversal sequences (/../).
func isValidHTTPPath(path string) bool {
	return validHTTPPath.MatchString(path) && !traversalSequence.MatchString(path)
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

func validateSecretFilePath(field, path string) []string {
	errs := validatePathInsideRepo(field, path)
	clean := filepath.Clean(path)
	if clean == "." || strings.TrimSpace(path) == "" {
		return append(errs, fmt.Sprintf("%s: path is required", field))
	}
	parts := strings.Split(clean, string(filepath.Separator))
	for _, part := range parts {
		if part == ".git" {
			errs = append(errs, fmt.Sprintf("%s: path %q must not target .git", field, path))
			break
		}
	}
	return errs
}

func validatePreviewInstallConfig(install *models.PreviewInstallConfig) []string {
	if install == nil {
		return nil
	}
	var errs []string
	if len(install.Command) == 0 {
		errs = append(errs, "preview.install.command is required")
	} else {
		for i, part := range install.Command {
			if strings.TrimSpace(part) == "" {
				errs = append(errs, fmt.Sprintf("preview.install.command[%d] is required", i))
			}
		}
	}
	if install.Cwd != "" {
		errs = append(errs, validatePathInsideRepo("preview.install.cwd", install.Cwd)...)
	}
	for i, path := range install.Lockfiles {
		field := fmt.Sprintf("preview.install.lockfiles[%d]", i)
		if strings.TrimSpace(path) == "" {
			errs = append(errs, field+" is required")
			continue
		}
		errs = append(errs, validatePathInsideRepo(field, path)...)
	}
	for i, path := range install.CleanPaths {
		field := fmt.Sprintf("preview.install.clean_paths[%d]", i)
		if strings.TrimSpace(path) == "" {
			errs = append(errs, field+" is required")
			continue
		}
		errs = append(errs, validatePathInsideRepo(field, path)...)
		if clean := filepath.Clean(path); clean == "." {
			errs = append(errs, fmt.Sprintf("%s: path %q is too broad to clean", field, path))
		}
		if !validPreviewInstallCleanPath.MatchString(path) {
			errs = append(errs, fmt.Sprintf("%s: path %q contains unsupported characters", field, path))
		}
	}
	if install.Cache != nil {
		if len(install.Cache.Paths) > 0 && len(install.Lockfiles) == 0 {
			errs = append(errs, "preview.install.cache.paths requires preview.install.lockfiles")
		}
		for i, path := range install.Cache.Paths {
			field := fmt.Sprintf("preview.install.cache.paths[%d]", i)
			errs = append(errs, validatePreviewDependencyCachePath(field, path, false)...)
		}
		if install.Cache.PackageManager != nil {
			for i, include := range install.Cache.PackageManager.Include {
				field := fmt.Sprintf("preview.install.cache.package_manager.include[%d]", i)
				if !knownPreviewPackageManager(include) {
					errs = append(errs, fmt.Sprintf("%s: unknown package manager %q", field, include))
				}
			}
			for i, path := range install.Cache.PackageManager.Paths {
				field := fmt.Sprintf("preview.install.cache.package_manager.paths[%d]", i)
				errs = append(errs, validatePreviewPackageManagerCachePath(field, path)...)
			}
		}
		if install.Cache.Build != nil {
			for i, path := range install.Cache.Build.Paths {
				field := fmt.Sprintf("preview.install.cache.build.paths[%d]", i)
				errs = append(errs, validatePreviewDependencyCachePath(field, path, true)...)
			}
		}
	}
	if paths, enabled := ResolvePreviewInstallCachePaths(install); enabled {
		for i, path := range paths {
			field := fmt.Sprintf("preview.install effective cache path[%d]", i)
			errs = append(errs, validatePreviewDependencyCachePath(field, path, true)...)
		}
	}
	if paths, enabled := ResolvePreviewBuildCachePaths(install); enabled {
		for i, path := range paths {
			field := fmt.Sprintf("preview.install effective build cache path[%d]", i)
			errs = append(errs, validatePreviewDependencyCachePath(field, path, true)...)
		}
	}
	for i, path := range install.VerifyPaths {
		field := fmt.Sprintf("preview.install.verify_paths[%d]", i)
		if strings.TrimSpace(path) == "" {
			errs = append(errs, field+" is required")
			continue
		}
		errs = append(errs, validatePathInsideRepo(field, path)...)
	}
	if install.TimeoutSeconds < 0 || install.TimeoutSeconds > MaxInstallTimeoutSeconds {
		errs = append(errs, fmt.Sprintf("preview.install.timeout_seconds must be between 1 and %d seconds when set", MaxInstallTimeoutSeconds))
	}
	return errs
}

func knownPreviewPackageManager(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "npm", "pnpm", "yarn", "bun", "pip", "uv", "poetry", "go":
		return true
	default:
		return false
	}
}

func validatePreviewDependencyCachePath(field, raw string, allowGlob bool) []string {
	var errs []string
	if strings.TrimSpace(raw) == "" {
		return []string{field + " is required"}
	}
	errs = append(errs, validatePathInsideRepo(field, raw)...)
	clean := filepath.Clean(raw)
	if clean == "." {
		errs = append(errs, fmt.Sprintf("%s: path %q is too broad to cache", field, raw))
	}
	parts := strings.Split(clean, string(filepath.Separator))
	for _, part := range parts {
		if part == ".git" {
			errs = append(errs, fmt.Sprintf("%s: path %q must not target .git", field, raw))
			break
		}
	}
	if dependencyCachePathTargetsPreviewInstallMarkers(filepath.ToSlash(clean)) {
		errs = append(errs, fmt.Sprintf("%s: path %q must not target preview install markers", field, raw))
	}
	if dependencyCachePathTargetsPlatformCache(filepath.ToSlash(clean)) {
		errs = append(errs, fmt.Sprintf("%s: path %q must not target platform preview cache", field, raw))
	}
	if !validPreviewInstallCleanPath.MatchString(raw) {
		errs = append(errs, fmt.Sprintf("%s: path %q contains unsupported characters", field, raw))
	}
	if !allowGlob && strings.Contains(raw, "*") {
		errs = append(errs, fmt.Sprintf("%s: path %q glob paths are not allowed", field, raw))
	}
	return errs
}

func validatePreviewPackageManagerCachePath(field, raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{field + " is required"}
	}
	if strings.HasPrefix(raw, "/") {
		return []string{fmt.Sprintf("%s: path %q must be a relative path", field, raw)}
	}
	if strings.Contains(raw, "*") {
		return []string{fmt.Sprintf("%s: path %q glob paths are not allowed", field, raw)}
	}
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
	if clean == "." {
		return []string{fmt.Sprintf("%s: path %q is too broad to cache", field, raw)}
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return []string{fmt.Sprintf("%s: path %q escapes the sandbox home", field, raw)}
	}
	sensitive := []string{".ssh", ".gnupg", ".codex", ".claude", ".config/gh", ".143"}
	for _, forbidden := range sensitive {
		if clean == forbidden || strings.HasPrefix(clean, forbidden+"/") || strings.HasPrefix(forbidden, clean+"/") {
			return []string{fmt.Sprintf("%s: path %q must not target sensitive sandbox home state", field, raw)}
		}
	}
	if !validPreviewInstallCleanPath.MatchString(raw) {
		return []string{fmt.Sprintf("%s: path %q contains unsupported characters", field, raw)}
	}
	return nil
}

func defaultPreviewInstallConfig(install *models.PreviewInstallConfig) {
	if install == nil {
		return
	}
	if install.TimeoutSeconds == 0 {
		install.TimeoutSeconds = DefaultInstallTimeoutSeconds
	}
}

// ResolvePreviewInstallCachePaths returns the effective dependency-cache paths
// and whether dependency caching is enabled for this install config. Caching is
// default-on only when lockfiles and at least one effective path exist.
func ResolvePreviewInstallCachePaths(install *models.PreviewInstallConfig) ([]string, bool) {
	if install == nil || len(install.Lockfiles) == 0 {
		return nil, false
	}
	if install.Cache != nil && install.Cache.Enabled != nil && !*install.Cache.Enabled {
		return nil, false
	}
	seen := make(map[string]struct{})
	var paths []string
	add := func(raw string) {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
		if clean == "" || clean == "." {
			return
		}
		if dependencyCachePathTargetsPreviewInstallMarkers(clean) || dependencyCachePathTargetsPlatformCache(clean) {
			return
		}
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		paths = append(paths, clean)
	}
	for _, path := range install.CleanPaths {
		add(path)
	}
	if install.Cache != nil {
		for _, path := range install.Cache.Paths {
			add(path)
		}
	}
	for _, lockfile := range install.Lockfiles {
		if inferred, ok := inferPreviewDependencyCachePath(lockfile); ok {
			add(inferred)
		}
	}
	sort.Strings(paths)
	return paths, len(paths) > 0
}

// ResolvePreviewBuildCachePaths returns the effective build-artifact cache
// paths and whether build caching is enabled for this install config. Build
// caching is default-on when dependency caching is on and a path can be
// inferred: JS lockfiles imply Turborepo's local cache locations next to the
// lockfile. Explicit paths come from preview.install.cache.build.paths.
func ResolvePreviewBuildCachePaths(install *models.PreviewInstallConfig) ([]string, bool) {
	if install == nil || len(install.Lockfiles) == 0 {
		return nil, false
	}
	if install.Cache != nil && install.Cache.Enabled != nil && !*install.Cache.Enabled {
		return nil, false
	}
	if install.Cache != nil && install.Cache.Build != nil && install.Cache.Build.Enabled != nil && !*install.Cache.Build.Enabled {
		return nil, false
	}
	seen := make(map[string]struct{})
	var paths []string
	add := func(raw string) {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
		if clean == "" || clean == "." {
			return
		}
		if dependencyCachePathTargetsPreviewInstallMarkers(clean) || dependencyCachePathTargetsPlatformCache(clean) {
			return
		}
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		paths = append(paths, clean)
	}
	if install.Cache != nil && install.Cache.Build != nil {
		for _, path := range install.Cache.Build.Paths {
			add(path)
		}
	}
	for _, lockfile := range install.Lockfiles {
		for _, inferred := range inferPreviewBuildCachePaths(lockfile) {
			add(inferred)
		}
	}
	sort.Strings(paths)
	return paths, len(paths) > 0
}

// ResolvePreviewBuildCacheHomePaths returns the effective HOME-rooted build
// cache paths and whether home-rooted build caching is enabled. Unlike the
// workdir build cache (which captures workspace-relative build-tool caches such
// as Turborepo's), this captures compiled-artifact caches that live under $HOME
// and are populated by a service's build step — most importantly Go's build
// cache (.cache/go-build) and module cache (go/pkg/mod). These are home-rooted
// because that is where Go places GOCACHE/GOMODCACHE by default in the sandbox,
// matching the package-manager cache's "go" defaults. Both Go caches are
// content/version-addressed, so a latest-wins blob shared across commits is
// safe and degrades to a partial (incremental) build rather than wrong output.
//
// Inference is gated on a Go lockfile (go.mod/go.sum) being declared in
// preview.install.lockfiles, the same signal the package-manager cache uses to
// enable Go caching. Build caching must also not be explicitly disabled.
func ResolvePreviewBuildCacheHomePaths(install *models.PreviewInstallConfig) ([]string, bool) {
	if install == nil || len(install.Lockfiles) == 0 {
		return nil, false
	}
	if install.Cache != nil && install.Cache.Enabled != nil && !*install.Cache.Enabled {
		return nil, false
	}
	if install.Cache != nil && install.Cache.Build != nil && install.Cache.Build.Enabled != nil && !*install.Cache.Build.Enabled {
		return nil, false
	}
	hasGoLockfile := false
	for _, lockfile := range install.Lockfiles {
		if manager, ok := inferPreviewPackageManagerFromLockfile(lockfile); ok && manager == "go" {
			hasGoLockfile = true
			break
		}
	}
	if !hasGoLockfile {
		return nil, false
	}
	// Both are home-rooted: GOCACHE defaults to ~/.cache/go-build (compiled
	// objects — the dominant cold-compile cost) and GOMODCACHE to ~/go/pkg/mod
	// (downloaded modules). Capturing both after the build phase means a launch
	// that compiled cold still warms every subsequent launch.
	return []string{".cache/go-build", "go/pkg/mod"}, true
}

// CacheRestorablePreviewInstallVerifyPaths returns the verify paths that can
// be satisfied by dependency cache restores. Platform-owned cache paths are
// intentionally ignored here for compatibility with existing configs: they may
// still be valid after a normal install, but restored dependency blobs never
// contain them.
func CacheRestorablePreviewInstallVerifyPaths(verifyPaths []string) []string {
	seen := make(map[string]struct{})
	paths := make([]string, 0, len(verifyPaths))
	for _, raw := range verifyPaths {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
		if clean == "" || clean == "." || dependencyCachePathTargetsPlatformCache(clean) {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		paths = append(paths, clean)
	}
	return paths
}

// inferPreviewBuildCachePaths maps a JS lockfile to the Turborepo local cache
// directories used by turbo >=1.9 (node_modules/.cache/turbo) and older or
// explicitly configured setups (.turbo/cache), rooted at the lockfile's
// directory.
func inferPreviewBuildCachePaths(lockfile string) []string {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(lockfile)))
	if clean == "" || clean == "." {
		return nil
	}
	switch filepath.Base(clean) {
	case "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb":
	default:
		return nil
	}
	dir := pathDir(clean)
	join := func(p string) string {
		if dir == "." {
			return p
		}
		return dir + "/" + p
	}
	return []string{join("node_modules/.cache/turbo"), join(".turbo/cache")}
}

func ResolvePreviewInstallPackageManagerCachePaths(install *models.PreviewInstallConfig) ([]string, []string, bool) {
	if install == nil || len(install.Lockfiles) == 0 {
		return nil, nil, false
	}
	if install.Cache != nil && install.Cache.Enabled != nil && !*install.Cache.Enabled {
		return nil, nil, false
	}
	if install.Cache != nil && install.Cache.PackageManager != nil && install.Cache.PackageManager.Enabled != nil && !*install.Cache.PackageManager.Enabled {
		return nil, nil, false
	}
	managersSeen := make(map[string]struct{})
	pathsSeen := make(map[string]struct{})
	var managers []string
	var paths []string
	addManager := func(manager string) {
		manager = strings.ToLower(strings.TrimSpace(manager))
		if !knownPreviewPackageManager(manager) {
			return
		}
		if _, ok := managersSeen[manager]; ok {
			return
		}
		managersSeen[manager] = struct{}{}
		managers = append(managers, manager)
		for _, p := range previewPackageManagerDefaultPaths(manager) {
			addPath := filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
			if addPath == "" || addPath == "." {
				continue
			}
			if _, ok := pathsSeen[addPath]; ok {
				continue
			}
			pathsSeen[addPath] = struct{}{}
			paths = append(paths, addPath)
		}
	}
	for _, lockfile := range install.Lockfiles {
		if manager, ok := inferPreviewPackageManagerFromLockfile(lockfile); ok {
			addManager(manager)
		}
	}
	for _, part := range install.Command {
		if manager, ok := inferPreviewPackageManagerFromCommandPart(part); ok {
			addManager(manager)
		}
	}
	if install.Cache != nil && install.Cache.PackageManager != nil {
		for _, manager := range install.Cache.PackageManager.Include {
			addManager(manager)
		}
		for _, p := range install.Cache.PackageManager.Paths {
			clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
			if clean == "" || clean == "." {
				continue
			}
			if _, ok := pathsSeen[clean]; ok {
				continue
			}
			pathsSeen[clean] = struct{}{}
			paths = append(paths, clean)
		}
	}
	sort.Strings(managers)
	sort.Strings(paths)
	return paths, managers, len(paths) > 0
}

func inferPreviewPackageManagerFromLockfile(lockfile string) (string, bool) {
	base := filepath.Base(filepath.ToSlash(filepath.Clean(strings.TrimSpace(lockfile))))
	switch base {
	case "package-lock.json", "npm-shrinkwrap.json":
		return "npm", true
	case "pnpm-lock.yaml":
		return "pnpm", true
	case "yarn.lock":
		return "yarn", true
	case "bun.lock", "bun.lockb":
		return "bun", true
	case "requirements.txt", "requirements-dev.txt", "Pipfile.lock":
		return "pip", true
	case "uv.lock":
		return "uv", true
	case "poetry.lock":
		return "poetry", true
	case "go.mod", "go.sum":
		return "go", true
	default:
		return "", false
	}
}

func inferPreviewPackageManagerFromCommandPart(part string) (string, bool) {
	name := filepath.Base(strings.TrimSpace(part))
	switch name {
	case "npm", "pnpm", "yarn", "bun", "pip", "uv", "poetry", "go":
		return name, true
	case "pip3":
		return "pip", true
	default:
		return "", false
	}
}

func previewPackageManagerDefaultPaths(manager string) []string {
	switch strings.ToLower(strings.TrimSpace(manager)) {
	case "npm":
		return []string{".npm"}
	case "pnpm":
		return []string{".local/share/pnpm/store"}
	case "yarn":
		return []string{".yarn/berry/cache"}
	case "bun":
		return []string{".bun/install/cache"}
	case "pip":
		return []string{".cache/pip"}
	case "uv":
		return []string{".cache/uv"}
	case "poetry":
		return []string{".cache/pypoetry"}
	case "go":
		return []string{"go/pkg/mod", ".cache/go-build"}
	default:
		return nil
	}
}

func inferPreviewDependencyCachePath(lockfile string) (string, bool) {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(lockfile)))
	if clean == "" || clean == "." {
		return "", false
	}
	dir := pathDir(clean)
	base := filepath.Base(clean)
	var cacheDir string
	switch base {
	case "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb":
		cacheDir = "node_modules"
	case "poetry.lock", "uv.lock", "Pipfile.lock", "pdm.lock", "requirements.txt", "requirements-dev.txt":
		cacheDir = ".venv"
	case "go.mod", "go.sum":
		cacheDir = "vendor"
	default:
		return "", false
	}
	if dir == "." || dir == "" {
		return cacheDir, true
	}
	return dir + "/" + cacheDir, true
}

func pathDir(clean string) string {
	idx := strings.LastIndex(clean, "/")
	if idx < 0 {
		return "."
	}
	return clean[:idx]
}

func validatePreviewResources(resources models.PreviewResourceRequirements, policy ResourcePolicy) []string {
	var errs []string
	hasResources := resources.Requests.CPU != "" ||
		resources.Requests.Memory != "" ||
		resources.Requests.EphemeralStorage != "" ||
		resources.Limits.CPU != "" ||
		resources.Limits.Memory != "" ||
		resources.Limits.EphemeralStorage != ""
	if hasResources && !policy.AllowRepoResourceRequests {
		errs = append(errs, "preview.resources cannot be set when repo resource requests are disabled")
	}

	reqCPU, reqCPUSet, err := parseCPUQuantity("preview.resources.requests.cpu", resources.Requests.CPU)
	if err != nil {
		errs = append(errs, err.Error())
	}
	reqMemory, reqMemorySet, err := parseByteQuantityMiB("preview.resources.requests.memory", resources.Requests.Memory)
	if err != nil {
		errs = append(errs, err.Error())
	}
	reqDisk, reqDiskSet, err := parseByteQuantityMiB("preview.resources.requests.ephemeral-storage", resources.Requests.EphemeralStorage)
	if err != nil {
		errs = append(errs, err.Error())
	}

	limitCPU, limitCPUSet, err := parseCPUQuantity("preview.resources.limits.cpu", resources.Limits.CPU)
	if err != nil {
		errs = append(errs, err.Error())
	}
	limitMemory, limitMemorySet, err := parseByteQuantityMiB("preview.resources.limits.memory", resources.Limits.Memory)
	if err != nil {
		errs = append(errs, err.Error())
	}
	limitDisk, limitDiskSet, err := parseByteQuantityMiB("preview.resources.limits.ephemeral-storage", resources.Limits.EphemeralStorage)
	if err != nil {
		errs = append(errs, err.Error())
	}

	if reqCPUSet && reqCPU > policy.MaxCPUMillis {
		errs = append(errs, "preview.resources.requests.cpu must be at most "+cpuLimitLabel(policy.MaxCPUMillis))
	}
	if limitCPUSet && limitCPU > policy.MaxCPUMillis {
		errs = append(errs, "preview.resources.limits.cpu must be at most "+cpuLimitLabel(policy.MaxCPUMillis))
	}
	if reqMemorySet && reqMemory > policy.MaxMemoryMiB {
		errs = append(errs, "preview.resources.requests.memory must be at most "+byteLimitLabel(policy.MaxMemoryMiB))
	}
	if limitMemorySet && limitMemory > policy.MaxMemoryMiB {
		errs = append(errs, "preview.resources.limits.memory must be at most "+byteLimitLabel(policy.MaxMemoryMiB))
	}
	if reqDiskSet && reqDisk > policy.MaxDiskMiB {
		errs = append(errs, "preview.resources.requests.ephemeral-storage must be at most "+byteLimitLabel(policy.MaxDiskMiB))
	}
	if limitDiskSet && limitDisk > policy.MaxDiskMiB {
		errs = append(errs, "preview.resources.limits.ephemeral-storage must be at most "+byteLimitLabel(policy.MaxDiskMiB))
	}

	if reqCPUSet && limitCPUSet && reqCPU > limitCPU {
		errs = append(errs, "preview.resources.requests.cpu must be less than or equal to preview.resources.limits.cpu")
	}
	if reqMemorySet && limitMemorySet && reqMemory > limitMemory {
		errs = append(errs, "preview.resources.requests.memory must be less than or equal to preview.resources.limits.memory")
	}
	if reqDiskSet && limitDiskSet && reqDisk > limitDisk {
		errs = append(errs, "preview.resources.requests.ephemeral-storage must be less than or equal to preview.resources.limits.ephemeral-storage")
	}

	return errs
}

func parseCPUQuantity(field, raw string) (int, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, false, nil
	}
	if strings.HasSuffix(value, "m") {
		millisRaw := strings.TrimSuffix(value, "m")
		millis, err := strconv.Atoi(millisRaw)
		if err != nil || millis <= 0 {
			return 0, true, fmt.Errorf("%s must be a positive CPU quantity such as 500m, 1, or 1.5", field)
		}
		return millis, true, nil
	}
	cores, err := strconv.ParseFloat(value, 64)
	if err != nil || cores <= 0 {
		return 0, true, fmt.Errorf("%s must be a positive CPU quantity such as 500m, 1, or 1.5", field)
	}
	return int(math.Ceil(cores * 1000)), true, nil
}

func cpuLimitLabel(millis int) string {
	if millis%1000 == 0 {
		cores := millis / 1000
		if cores == 1 {
			return "1 core"
		}
		return strconv.Itoa(cores) + " cores"
	}
	return strconv.Itoa(millis) + "m"
}

func byteLimitLabel(mib int) string {
	if mib%1024 == 0 {
		return strconv.Itoa(mib/1024) + "Gi"
	}
	return strconv.Itoa(mib) + "Mi"
}

func parseByteQuantityMiB(field, raw string) (int, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, false, nil
	}

	lower := strings.ToLower(value)
	units := []struct {
		suffix string
		mib    float64
	}{
		{suffix: "gib", mib: 1024},
		{suffix: "gi", mib: 1024},
		{suffix: "gb", mib: 1000000000.0 / 1048576.0},
		{suffix: "g", mib: 1000000000.0 / 1048576.0},
		{suffix: "mib", mib: 1},
		{suffix: "mi", mib: 1},
		{suffix: "mb", mib: 1000000.0 / 1048576.0},
		{suffix: "m", mib: 1000000.0 / 1048576.0},
		{suffix: "kib", mib: 1.0 / 1024.0},
		{suffix: "ki", mib: 1.0 / 1024.0},
		{suffix: "kb", mib: 1000.0 / 1048576.0},
		{suffix: "k", mib: 1000.0 / 1048576.0},
		{suffix: "b", mib: 1.0 / 1048576.0},
	}
	for _, unit := range units {
		if !strings.HasSuffix(lower, unit.suffix) {
			continue
		}
		numberRaw := strings.TrimSpace(value[:len(value)-len(unit.suffix)])
		number, err := strconv.ParseFloat(numberRaw, 64)
		if err != nil || number <= 0 {
			return 0, true, fmt.Errorf("%s must be a positive byte quantity such as 512Mi, 1Gi, 500mb, or 5gb", field)
		}
		return int(math.Ceil(number * unit.mib)), true, nil
	}

	bytes, err := strconv.ParseFloat(value, 64)
	if err != nil || bytes <= 0 {
		return 0, true, fmt.Errorf("%s must be a positive byte quantity such as 512Mi, 1Gi, 500mb, or 5gb", field)
	}
	return int(math.Ceil(bytes / 1048576.0)), true, nil
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
func ResolveConfig(baseCfg, diffCfg *models.PreviewConfig) (*models.PreviewConfig, error) {
	resolved := &models.PreviewConfig{
		Version:     baseCfg.Version,
		Name:        baseCfg.Name,
		Progressive: baseCfg.Progressive,

		// Security-sensitive: always from base.
		Primary:     baseCfg.Primary,
		Credentials: baseCfg.Credentials,
		Secrets:     append([]models.PreviewSecretBundleRef(nil), baseCfg.Secrets...),
		Network:     baseCfg.Network,
	}

	if IsConnected(baseCfg) {
		resolved.Install = cloneInstallConfig(baseCfg.Install)
		resolved.Resources = baseCfg.Resources
	} else {
		resolved.Install = cloneInstallConfig(diffCfg.Install)
		resolved.Resources = diffCfg.Resources
	}
	defaultPreviewInstallConfig(resolved.Install)

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

	if errs := ValidateConfig(resolved); len(errs) > 0 {
		return nil, fmt.Errorf("resolved config is invalid: %s", strings.Join(errs, "; "))
	}
	return resolved, nil
}

func cloneInstallConfig(install *models.PreviewInstallConfig) *models.PreviewInstallConfig {
	if install == nil {
		return nil
	}
	cloned := *install
	cloned.Command = append([]string(nil), install.Command...)
	cloned.Lockfiles = append([]string(nil), install.Lockfiles...)
	cloned.CleanPaths = append([]string(nil), install.CleanPaths...)
	cloned.VerifyPaths = append([]string(nil), install.VerifyPaths...)
	if install.Cache != nil {
		cache := *install.Cache
		cache.Paths = append([]string(nil), install.Cache.Paths...)
		cloned.Cache = &cache
	}
	return &cloned
}

// IsConnected returns true if the config references managed credentials or destinations.
func IsConnected(cfg *models.PreviewConfig) bool {
	if len(SecretBundleRefs(cfg)) > 0 {
		return true
	}
	if cfg.Credentials.Mode != "" && cfg.Credentials.Mode != "none" {
		return true
	}
	if len(cfg.Network.Destinations) > 0 {
		return true
	}
	return false
}

// SecretBundleRefs returns repo-authored secret bundle refs. Legacy
// credentials.managed_env config is normalized to the same shape so the
// resolver and readiness surfaces share one path.
func SecretBundleRefs(cfg *models.PreviewConfig) []models.PreviewSecretBundleRef {
	if len(cfg.Secrets) > 0 {
		return cfg.Secrets
	}
	if cfg.Credentials.Mode == "" || cfg.Credentials.Mode == "none" {
		return nil
	}
	return []models.PreviewSecretBundleRef{{
		Bundle:   cfg.Credentials.CredentialSet,
		Services: cfg.Credentials.InjectInto,
		Env:      cfg.Credentials.Env,
	}}
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
	for _, ref := range cfg.Secrets {
		result.MissingSecretBundles = append(result.MissingSecretBundles, models.MissingSecretBundle{
			Bundle:   ref.Bundle,
			Services: ref.Services,
			Env:      ref.Env,
			Files:    ref.Files,
			Status:   "setup_required",
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
	return ResolveResourceLimitsWithPolicy(cfg, defaultResourcePolicy())
}

// ResolveResourceLimitsWithPolicy returns resource limits capped by the
// effective org policy.
func ResolveResourceLimitsWithPolicy(cfg *models.PreviewConfig, resourcePolicy ResourcePolicy) models.ResourceLimits {
	resourcePolicy = normalizeResourcePolicy(resourcePolicy)
	limits := models.ResourceLimits{
		MemoryMiB: DefaultSingleServiceMemory,
		CPUMillis: 500,
		DiskMiB:   DefaultPreviewDiskMiB,
	}
	if len(cfg.Services) > 1 && len(cfg.Infrastructure) > 0 {
		limits.MemoryMiB = DefaultInfraServiceMemory
		limits.CPUMillis = 2000
	} else if len(cfg.Services) > 1 {
		limits.MemoryMiB = DefaultMultiServiceMemory
		limits.CPUMillis = 1000
	}

	applyResourceList := func(list models.PreviewResourceList) {
		if parsed, ok, err := parseCPUQuantity("preview.resources.cpu", list.CPU); err == nil && ok {
			limits.CPUMillis = parsed
		}
		if parsed, ok, err := parseByteQuantityMiB("preview.resources.memory", list.Memory); err == nil && ok {
			limits.MemoryMiB = parsed
		}
		if parsed, ok, err := parseByteQuantityMiB("preview.resources.ephemeral-storage", list.EphemeralStorage); err == nil && ok {
			limits.DiskMiB = parsed
		}
	}
	applyResourceList(cfg.Resources.Requests)
	applyResourceList(cfg.Resources.Limits)
	if limits.CPUMillis > resourcePolicy.MaxCPUMillis {
		limits.CPUMillis = resourcePolicy.MaxCPUMillis
	}
	if limits.MemoryMiB > resourcePolicy.MaxMemoryMiB {
		limits.MemoryMiB = resourcePolicy.MaxMemoryMiB
	}
	if limits.DiskMiB > resourcePolicy.MaxDiskMiB {
		limits.DiskMiB = resourcePolicy.MaxDiskMiB
	}

	return limits
}

// ApplyResourceLimitsToSandboxConfig maps preview topology limits onto the
// container resource config used when hydrating a preview sandbox.
func ApplyResourceLimitsToSandboxConfig(sandboxCfg *agent.SandboxConfig, cfg *models.PreviewConfig) {
	if sandboxCfg == nil || cfg == nil {
		return
	}
	limits := ResolveResourceLimits(cfg)
	sandboxCfg.MemoryLimitMB = limits.MemoryMiB
	sandboxCfg.CPULimit = float64(limits.CPUMillis) / 1000
	sandboxCfg.DiskLimitGB = int(math.Ceil(float64(limits.DiskMiB) / 1024.0))
}

// ApplyPreviewInstanceResourceLimitsToSandboxConfig maps persisted preview
// reservation limits onto the sandbox config used by durable worker jobs.
func ApplyPreviewInstanceResourceLimitsToSandboxConfig(sandboxCfg *agent.SandboxConfig, instance *models.PreviewInstance) {
	if sandboxCfg == nil || instance == nil {
		return
	}
	sandboxCfg.MemoryLimitMB = instance.MemoryLimitMB
	sandboxCfg.CPULimit = float64(instance.CPULimitMillis) / 1000
	sandboxCfg.DiskLimitGB = int(math.Ceil(float64(instance.DiskLimitMB) / 1024.0))
}
