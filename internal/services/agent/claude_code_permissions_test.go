package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestClaudeCodePermissionModeForAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		billingMode TokenBillingMode
		accountType string
		model       string
		version     string
		expected    string
	}{
		{
			name:        "api key with supported current model and version uses bypass",
			billingMode: TokenBillingModeAPIKey,
			model:       models.ClaudeCodeModelSonnet46,
			version:     "2.1.139",
			expected:    ClaudeCodePermissionModeBypassPermissions,
		},
		{
			name:        "api key with future sonnet model uses bypass",
			billingMode: TokenBillingModeAPIKey,
			model:       "claude-sonnet-4-9",
			version:     "2.2.0",
			expected:    ClaudeCodePermissionModeBypassPermissions,
		},
		{
			name:        "api key with provider prefix and dated model uses bypass",
			billingMode: TokenBillingModeAPIKey,
			model:       "anthropic/claude-sonnet-4-10-20260101",
			version:     "2.2.0",
			expected:    ClaudeCodePermissionModeBypassPermissions,
		},
		{
			name:        "api key with future opus model uses bypass",
			billingMode: TokenBillingModeAPIKey,
			model:       "claude-opus-5-0",
			version:     "2.2.0",
			expected:    ClaudeCodePermissionModeBypassPermissions,
		},
		{
			name:        "api key with unsupported model still uses bypass",
			billingMode: TokenBillingModeAPIKey,
			model:       models.ClaudeCodeModelHaiku45,
			version:     "2.1.139",
			expected:    ClaudeCodePermissionModeBypassPermissions,
		},
		{
			name:        "api key with older CLI still uses bypass",
			billingMode: TokenBillingModeAPIKey,
			model:       models.ClaudeCodeModelSonnet46,
			version:     "2.0.99",
			expected:    ClaudeCodePermissionModeBypassPermissions,
		},
		{
			name:        "pro subscription uses bypass",
			billingMode: TokenBillingModeSubscription,
			accountType: "claude_pro",
			model:       models.ClaudeCodeModelSonnet46,
			version:     "2.1.139",
			expected:    ClaudeCodePermissionModeBypassPermissions,
		},
		{
			name:        "max subscription with supported model and version uses bypass",
			billingMode: TokenBillingModeSubscription,
			accountType: "claude_max",
			model:       models.ClaudeCodeModelSonnet46,
			version:     "2.1.139",
			expected:    ClaudeCodePermissionModeBypassPermissions,
		},
		{
			name:        "unknown version still uses bypass",
			billingMode: TokenBillingModeAPIKey,
			model:       models.ClaudeCodeModelSonnet46,
			expected:    ClaudeCodePermissionModeBypassPermissions,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := claudeCodePermissionModeForAuth(tt.billingMode, tt.accountType, tt.model, tt.version)
			require.Equal(t, tt.expected, actual, "permission mode should match auth, model, and CLI compatibility")
		})
	}
}

func TestParseClaudeCodeVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		output   string
		expected string
	}{
		{name: "plain semver", output: "2.1.139\n", expected: "2.1.139"},
		{name: "prefixed output", output: "Claude Code 2.2.0", expected: "2.2.0"},
		{name: "package style output", output: "@anthropic-ai/claude-code/2.1.139 linux-x64 node-v22.0.0", expected: "2.1.139"},
		{name: "no version", output: "Claude Code", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, parseClaudeCodeVersion(tt.output), "parseClaudeCodeVersion should extract the CLI semver")
		})
	}
}
