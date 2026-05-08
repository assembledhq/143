package repoconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse_WithPreviewBootstrapAndValidation(t *testing.T) {
	t.Parallel()

	cfg, err := Parse([]byte(`{
		"preview": {
			"primary": "web",
			"services": {
				"web": {
					"command": ["npm", "run", "dev"],
					"port": 3000,
					"ready": {"http_path": "/"}
				}
			},
			"credentials": {"mode": "none"},
			"network": {"mode": "managed"}
		},
		"bootstrap": {
			"commands": ["npm ci"]
		},
		"validation": {
			"commands": ["npm run lint:js"]
		}
	}`))
	require.NoError(t, err, "Parse should accept repo config with preview, bootstrap, and validation sections")
	require.JSONEq(t, `{
		"primary": "web",
		"services": {
			"web": {
				"command": ["npm", "run", "dev"],
				"port": 3000,
				"ready": {"http_path": "/"}
			}
		},
		"credentials": {"mode": "none"},
		"network": {"mode": "managed"}
	}`, string(cfg.Preview), "Parse should preserve the nested preview section for downstream preview parsing")
	require.Equal(t, []string{"npm ci"}, cfg.Bootstrap.Commands, "Parse should preserve bootstrap commands")
	require.Equal(t, []string{"npm run lint:js"}, cfg.Validation.Commands, "Parse should preserve validation commands")
}

func TestParse_TrimsCommandWhitespace(t *testing.T) {
	t.Parallel()

	cfg, err := Parse([]byte(`{
		"environment": {"commands": ["  install-tool  "]},
		"bootstrap": {"commands": ["  npm ci  "]},
		"validation": {"commands": ["  npm run lint:js  "]}
	}`))
	require.NoError(t, err, "Parse should accept commands with leading and trailing whitespace")
	require.Equal(t, []string{"install-tool"}, cfg.Environment.Commands, "Parse should trim environment command whitespace")
	require.Equal(t, []string{"npm ci"}, cfg.Bootstrap.Commands, "Parse should trim bootstrap command whitespace")
	require.Equal(t, []string{"npm run lint:js"}, cfg.Validation.Commands, "Parse should trim validation command whitespace")
}

func TestParse_RejectsBlankCommand(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`{
		"validation": {"commands": ["   "]}
	}`))
	require.Error(t, err, "Parse should reject blank repo config commands")
	require.Contains(t, err.Error(), "validation.commands[0]", "Parse should identify the invalid command path")
}

func TestParse_PreservesEnvironmentCommands(t *testing.T) {
	t.Parallel()

	cfg, err := Parse([]byte(`{
		"environment": {
			"commands": [
				"curl -fsSL https://example.com/tool.tgz | tar -xz -C $HOME/.local/bin"
			]
		}
	}`))
	require.NoError(t, err, "Parse should accept an environment section with shell commands")
	require.Equal(t, []string{"curl -fsSL https://example.com/tool.tgz | tar -xz -C $HOME/.local/bin"}, cfg.Environment.Commands, "Parse should preserve environment commands verbatim so repos can declare their own install scripts")
}

func TestParse_RejectsBlankEnvironmentCommand(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`{
		"environment": {"commands": [""]}
	}`))
	require.Error(t, err, "Parse should reject blank environment commands the same way it rejects blank bootstrap/validation commands")
	require.Contains(t, err.Error(), "environment.commands[0]", "Parse should identify the invalid environment command path")
}
