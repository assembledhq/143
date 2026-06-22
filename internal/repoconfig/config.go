package repoconfig

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/assembledhq/143/internal/models"
)

const (
	ConfigPath = ".143/config.json"

	// PRReadinessCheckConfigTypePrompt is the only supported custom-check type today.
	PRReadinessCheckConfigTypePrompt = "prompt"
)

type CommandSection struct {
	Commands []string `json:"commands,omitempty"`
}

type Config struct {
	Preview      json.RawMessage   `json:"preview,omitempty"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
	Bootstrap    CommandSection    `json:"bootstrap,omitempty"`
	Validation   CommandSection    `json:"validation,omitempty"`
	PRReadiness  PRReadinessConfig `json:"pr_readiness,omitempty"`
}

type PRReadinessConfig struct {
	Checks []PRReadinessCheckConfig `json:"checks,omitempty"`
}

type PRReadinessCheckConfig struct {
	ID          string                              `json:"id"`
	Name        string                              `json:"name"`
	Type        string                              `json:"type"`
	Enforcement models.PRReadinessEnforcementByRole `json:"enforcement,omitempty"`
	Paths       PRReadinessPathFilter               `json:"paths,omitempty"`
	Prompt      string                              `json:"prompt"`
}

type PRReadinessPathFilter struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

func Parse(data []byte) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse repo config: %w", err)
	}

	deps, err := normalizeDependencies(cfg.Dependencies)
	if err != nil {
		return Config{}, err
	}
	cfg.Dependencies = deps
	if err := normalizeCommandSection("bootstrap.commands", &cfg.Bootstrap); err != nil {
		return Config{}, err
	}
	if err := normalizeCommandSection("validation.commands", &cfg.Validation); err != nil {
		return Config{}, err
	}
	if err := normalizePRReadiness(&cfg.PRReadiness); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

var readinessCheckIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

func normalizePRReadiness(cfg *PRReadinessConfig) error {
	if len(cfg.Checks) > models.MaxPRReadinessCustomChecks {
		return fmt.Errorf("pr_readiness.checks must not exceed %d entries", models.MaxPRReadinessCustomChecks)
	}
	seen := map[string]struct{}{}
	for i := range cfg.Checks {
		check := &cfg.Checks[i]
		fieldPath := fmt.Sprintf("pr_readiness.checks[%d]", i)
		check.ID = strings.TrimSpace(check.ID)
		check.Name = strings.TrimSpace(check.Name)
		check.Type = strings.TrimSpace(check.Type)
		check.Prompt = strings.TrimSpace(check.Prompt)
		if !readinessCheckIDPattern.MatchString(check.ID) {
			return fmt.Errorf("%s.id must match %s", fieldPath, readinessCheckIDPattern.String())
		}
		if _, ok := seen[check.ID]; ok {
			return fmt.Errorf("%s.id must be unique", fieldPath)
		}
		seen[check.ID] = struct{}{}
		if check.Name == "" {
			return fmt.Errorf("%s.name must be a non-empty string", fieldPath)
		}
		if len(check.Name) > models.MaxPRReadinessCustomCheckName {
			return fmt.Errorf("%s.name must not exceed %d characters", fieldPath, models.MaxPRReadinessCustomCheckName)
		}
		if check.Type != PRReadinessCheckConfigTypePrompt {
			return fmt.Errorf("%s.type must be %q", fieldPath, PRReadinessCheckConfigTypePrompt)
		}
		if check.Prompt == "" {
			return fmt.Errorf("%s.prompt must be a non-empty string", fieldPath)
		}
		if len(check.Prompt) > models.MaxPRReadinessCustomCheckPrompt {
			return fmt.Errorf("%s.prompt must not exceed %d characters", fieldPath, models.MaxPRReadinessCustomCheckPrompt)
		}
		// The prompt is rendered as a Go text/template at readiness time; reject
		// templates that don't parse here so authors get a clear config error
		// instead of a silent per-run check failure later.
		if _, err := template.New("custom_readiness_prompt").Parse(check.Prompt); err != nil {
			return fmt.Errorf("%s.prompt is not a valid template: %w", fieldPath, err)
		}
		if err := check.Enforcement.Validate(); err != nil {
			return fmt.Errorf("%s.enforcement: %w", fieldPath, err)
		}
		if check.Enforcement.EnforcementFor(models.RoleBuilder) == models.PRReadinessEnforcementOff &&
			check.Enforcement.EnforcementFor(models.RoleMember) == models.PRReadinessEnforcementOff &&
			check.Enforcement.EnforcementFor(models.RoleAdmin) == models.PRReadinessEnforcementOff {
			return fmt.Errorf("%s.enforcement must enable at least one role (advisory or blocking)", fieldPath)
		}
		if err := normalizePathFilter(fieldPath+".paths.include", check.Paths.Include); err != nil {
			return err
		}
		if err := normalizePathFilter(fieldPath+".paths.exclude", check.Paths.Exclude); err != nil {
			return err
		}
	}
	return nil
}

func normalizePathFilter(fieldPath string, values []string) error {
	if len(values) > models.MaxPRReadinessPathPatterns {
		return fmt.Errorf("%s must not exceed %d patterns", fieldPath, models.MaxPRReadinessPathPatterns)
	}
	for i, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return fmt.Errorf("%s[%d] must be a non-empty string", fieldPath, i)
		}
		if len(trimmed) > models.MaxPRReadinessPathPatternLen {
			return fmt.Errorf("%s[%d] must not exceed %d characters", fieldPath, i, models.MaxPRReadinessPathPatternLen)
		}
		values[i] = trimmed
	}
	return nil
}

func normalizeCommandSection(fieldPath string, section *CommandSection) error {
	for i, command := range section.Commands {
		trimmed := strings.TrimSpace(command)
		if trimmed == "" {
			return fmt.Errorf("%s[%d] must be a non-empty string", fieldPath, i)
		}
		section.Commands[i] = trimmed
	}
	return nil
}

// normalizeDependencies enforces exact-pin versions. "latest", empty strings,
// or anything not a concrete pin is rejected so installs are deterministic and
// cacheable by name@version.
func normalizeDependencies(deps map[string]string) (map[string]string, error) {
	if len(deps) == 0 {
		return deps, nil
	}
	normalized := make(map[string]string, len(deps))
	for name, version := range deps {
		trimmedName := strings.TrimSpace(name)
		if trimmedName == "" {
			return nil, fmt.Errorf("dependencies: name must be a non-empty string")
		}
		trimmedVersion := strings.TrimSpace(version)
		if trimmedVersion == "" {
			return nil, fmt.Errorf("dependencies.%s must be a non-empty exact version pin", trimmedName)
		}
		if strings.EqualFold(trimmedVersion, "latest") {
			return nil, fmt.Errorf("dependencies.%s must be an exact version pin, not %q", trimmedName, trimmedVersion)
		}
		if _, exists := normalized[trimmedName]; exists {
			return nil, fmt.Errorf("dependencies.%s must only be specified once", trimmedName)
		}
		normalized[trimmedName] = trimmedVersion
	}
	return normalized, nil
}
