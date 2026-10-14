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
		name     string
		reserve  int64
		interval time.Duration
		enabled  bool
		invalid  bool
	}{
		{name: "zero defaults disabled"},
		{name: "explicit reserve enables", reserve: 100, interval: time.Minute, enabled: true},
		{name: "invalid interval propagated", reserve: 100, interval: 16 * time.Minute, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controller, err := ResolveGitHubRateLimitController(&config.Config{GitHubBackgroundQuotaReserve: tt.reserve, GitHubBackgroundQuotaInterval: tt.interval}, nil, zerolog.Nop(), nil)
			if tt.invalid {
				require.Error(t, err, "unsafe background interval must reach controller validation")
				return
			}
			require.NoError(t, err, "valid process controls should initialize the shared controller")
			require.Equal(t, tt.enabled, controller.BackgroundQuotaEnabled(), "router and worker construction must preserve the opt-in reserve")
		})
	}
}
