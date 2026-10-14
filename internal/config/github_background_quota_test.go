package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateSecretsGitHubBackgroundQuota(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		reserve   int64
		interval  time.Duration
		expectErr bool
	}{
		{name: "disabled zero value"},
		{name: "disabled parsed defaults", interval: time.Minute},
		{name: "enabled bounded interval", reserve: 100, interval: time.Minute},
		{name: "negative reserve", reserve: -1, interval: time.Minute, expectErr: true},
		{name: "negative interval", interval: -time.Second, expectErr: true},
		{name: "enabled zero interval", reserve: 100, expectErr: true},
		{name: "unbounded short interval", reserve: 100, interval: time.Millisecond, expectErr: true},
		{name: "unbounded long interval", reserve: 100, interval: time.Hour, expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := (&Config{Env: "development", GitHubBackgroundQuotaReserve: tt.reserve, GitHubBackgroundQuotaInterval: tt.interval}).ValidateSecrets()
			if tt.expectErr {
				require.Error(t, err, "invalid background budget configuration should fail startup validation")
				return
			}
			require.NoError(t, err, "disabled or bounded background budgeting should validate")
		})
	}
}
