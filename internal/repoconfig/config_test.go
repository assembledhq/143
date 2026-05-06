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
		"bootstrap": {"commands": ["  npm ci  "]},
		"validation": {"commands": ["  npm run lint:js  "]}
	}`))
	require.NoError(t, err, "Parse should accept commands with leading and trailing whitespace")
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
