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
		expected    string
	}{
		{
			name:        "api key with supported model enables auto",
			billingMode: TokenBillingModeAPIKey,
			model:       models.ClaudeCodeModelSonnet46,
			expected:    ClaudeCodePermissionModeAuto,
		},
		{
			name:        "api key with unsupported model keeps accept edits",
			billingMode: TokenBillingModeAPIKey,
			model:       models.ClaudeCodeModelHaiku45,
			expected:    ClaudeCodePermissionModeAcceptEdits,
		},
		{
			name:        "pro subscription keeps accept edits",
			billingMode: TokenBillingModeSubscription,
			accountType: "claude_pro",
			model:       models.ClaudeCodeModelSonnet46,
			expected:    ClaudeCodePermissionModeAcceptEdits,
		},
		{
			name:        "max subscription with supported model enables auto",
			billingMode: TokenBillingModeSubscription,
			accountType: "claude_max",
			model:       models.ClaudeCodeModelSonnet46,
			expected:    ClaudeCodePermissionModeAuto,
		},
		{
			name:        "unknown subscription keeps accept edits",
			billingMode: TokenBillingModeSubscription,
			model:       models.ClaudeCodeModelSonnet46,
			expected:    ClaudeCodePermissionModeAcceptEdits,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := claudeCodePermissionModeForAuth(tt.billingMode, tt.accountType, tt.model)
			require.Equal(t, tt.expected, actual, "permission mode should match auth and model compatibility")
		})
	}
}
