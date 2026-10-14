package api

import (
	"testing"
	"time"

	"github.com/assembledhq/143/internal/config"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestResolveGitHubRateLimitControllerWiresBackgroundQuota(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		reserve   int64
		interval  time.Duration
		enabled   bool
		invalid   bool
		mode      string
		allowlist []int64
		enforces  bool
	}{
		{name: "zero defaults disabled"},
		{name: "positive reserve observes by default", reserve: 100, interval: time.Minute, enabled: true},
		{name: "observe selected remains predictive", reserve: 100, interval: time.Minute, enabled: true, mode: "observe", allowlist: []int64{42}},
		{name: "enforce empty allowlist bypasses", reserve: 100, interval: time.Minute, enabled: true, mode: "enforce"},
		{name: "enforce selected suppresses", reserve: 100, interval: time.Minute, enabled: true, mode: "enforce", allowlist: []int64{42}, enforces: true},
		{name: "enforce unselected bypasses", reserve: 100, interval: time.Minute, enabled: true, mode: "enforce", allowlist: []int64{43}},
		{name: "invalid interval propagated", reserve: 100, interval: 16 * time.Minute, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controller, err := ResolveGitHubRateLimitController(&config.Config{GitHubAppID: 1, GitHubRateLimitMode: tt.mode, GitHubRateLimitInstallationAllowlist: tt.allowlist, GitHubBackgroundQuotaReserve: tt.reserve, GitHubBackgroundQuotaInterval: tt.interval}, nil, zerolog.Nop(), nil)
			if tt.invalid {
				require.Error(t, err, "unsafe background interval must reach controller validation")
				return
			}
			require.NoError(t, err, "valid process controls should initialize the shared controller")
			require.Equal(t, tt.enabled, controller.BackgroundQuotaEnabled(), "router and worker construction must preserve the opt-in reserve")
			require.Equal(t, tt.enforces, controller.EnforcesBackgroundQuota(42), "router and worker construction must preserve observation and explicit enforcement scope")
		})
	}
}
