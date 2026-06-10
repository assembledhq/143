// Package cli implements the laptop-side commands of 143-tools: browser
// login, token management, self-update, the server-proxied MCP gateway, and
// the cwd-aware preview commands. Sandbox-side behavior (env-credential
// tools, git helpers) lives in internal/services/mcp and
// internal/services/sandboxauth and is unaffected — the same binary serves
// both worlds, selected automatically by which credentials are present.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ConfigVersion is the current config-file schema version. The field exists
// so the format can evolve (the obvious future change is multi-server
// profiles keyed by server URL) without making the first migration a
// breaking change.
const ConfigVersion = 1

// Config is ~/.config/143-tools/config.json. Token is the personal
// "143u_..." CLI credential; mode 0600 because of it.
type Config struct {
	Version   int    `json:"version"`
	ServerURL string `json:"server_url"`
	Token     string `json:"token,omitempty"`
	OrgID     string `json:"org_id,omitempty"`
	// PendingJoinToken carries the join token from a tokened installer
	// invocation until login consumes it. Cleared on successful login.
	PendingJoinToken string `json:"pending_join_token,omitempty"`
}

// ConfigPath resolves the config file location, honoring XDG_CONFIG_HOME.
func ConfigPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "143-tools", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "143-tools", "config.json"), nil
}

// LoadConfig reads the config file. A missing file returns an empty config
// and no error — callers distinguish "not set up" by ServerURL == "".
func LoadConfig() (Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- fixed well-known config path
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// SaveConfig writes the config atomically with mode 0600 (it holds a
// credential). The parent directory is created as needed.
func SaveConfig(cfg Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if cfg.Version == 0 {
		cfg.Version = ConfigVersion
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
