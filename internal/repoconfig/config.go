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
	Preview     json.RawMessage `json:"preview,omitempty"`
	Environment CommandSection  `json:"environment,omitempty"`
	Bootstrap   CommandSection  `json:"bootstrap,omitempty"`
	Validation  CommandSection  `json:"validation,omitempty"`
}

func Parse(data []byte) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse repo config: %w", err)
	}

	if err := normalizeCommandSection("environment.commands", &cfg.Environment); err != nil {
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
