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

	if err := normalizeDependencies(cfg.Dependencies); err != nil {
		return Config{}, err
	}
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
func normalizeDependencies(deps map[string]string) error {
	for name, version := range deps {
		trimmedName := strings.TrimSpace(name)
		if trimmedName == "" {
			return fmt.Errorf("dependencies: name must be a non-empty string")
		}
		trimmedVersion := strings.TrimSpace(version)
		if trimmedVersion == "" {
			return fmt.Errorf("dependencies.%s must be a non-empty exact version pin", trimmedName)
		}
		if strings.EqualFold(trimmedVersion, "latest") {
			return fmt.Errorf("dependencies.%s must be an exact version pin, not %q", trimmedName, trimmedVersion)
		}
		deps[name] = trimmedVersion
	}
	return nil
}
