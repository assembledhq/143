package repoconfig

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	ConfigPath = ".143/config.json"
)

type CommandSection struct {
	Commands []string `json:"commands,omitempty"`
}

type Config struct {
	Preview      json.RawMessage   `json:"preview,omitempty"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
	Bootstrap    CommandSection    `json:"bootstrap,omitempty"`
	Validation   CommandSection    `json:"validation,omitempty"`
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

	return cfg, nil
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
