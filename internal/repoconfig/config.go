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
	Preview      json.RawMessage `json:"preview,omitempty"`
	Bootstrap    CommandSection  `json:"bootstrap,omitempty"`
	Validation   CommandSection  `json:"validation,omitempty"`
	Dependencies []Dependency    `json:"dependencies,omitempty"`
}

// Dependency declares a build-time tool the sandbox should provision before
// bootstrap commands run. The set of supported names is the registry in
// dependencies.go; arbitrary install scripts belong in bootstrap.commands.
type Dependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func Parse(data []byte) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse repo config: %w", err)
	}

	if err := normalizeCommandSection("bootstrap.commands", &cfg.Bootstrap); err != nil {
		return Config{}, err
	}
	if err := normalizeCommandSection("validation.commands", &cfg.Validation); err != nil {
		return Config{}, err
	}
	if err := normalizeDependencies(cfg.Dependencies); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func normalizeDependencies(deps []Dependency) error {
	seen := make(map[string]struct{}, len(deps))
	for i := range deps {
		deps[i].Name = strings.TrimSpace(deps[i].Name)
		deps[i].Version = strings.TrimSpace(deps[i].Version)
		if deps[i].Name == "" {
			return fmt.Errorf("dependencies[%d].name must be a non-empty string", i)
		}
		if deps[i].Version == "" {
			return fmt.Errorf("dependencies[%d].version must be a non-empty string", i)
		}
		if _, ok := dependencyInstallers[deps[i].Name]; !ok {
			return fmt.Errorf("dependencies[%d].name %q is not a supported dependency", i, deps[i].Name)
		}
		if _, dup := seen[deps[i].Name]; dup {
			return fmt.Errorf("dependencies[%d].name %q is declared more than once", i, deps[i].Name)
		}
		seen[deps[i].Name] = struct{}{}
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
