package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateSecretsPRHealthSweepStaleAfter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		staleAfter time.Duration
		expectErr  bool
	}{
		{name: "default threshold", staleAfter: 10 * time.Minute},
		{name: "lower bound", staleAfter: time.Minute},
		{name: "upper bound", staleAfter: 48 * time.Hour},
		{name: "zero", expectErr: true},
		{name: "negative", staleAfter: -10 * time.Minute, expectErr: true},
		{name: "below lower bound", staleAfter: time.Minute - time.Second, expectErr: true},
		{name: "above upper bound", staleAfter: 48*time.Hour + time.Second, expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := (&Config{Env: "development", PRHealthSweepStaleAfter: tt.staleAfter}).ValidateSecrets()
			if tt.expectErr {
				require.EqualError(t, err, "PR_HEALTH_SWEEP_STALE_AFTER must be a duration between 1m and 48h", "out-of-range sweep thresholds should fail startup validation with an actionable message")
				return
			}
			require.NoError(t, err, "bounded sweep thresholds should validate")
		})
	}
}

//nolint:paralleltest // uses t.Setenv
func TestLoadPRHealthSweepStaleAfter(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		expected  time.Duration
		expectErr bool
	}{
		{name: "unset uses default", value: "", expected: 10 * time.Minute},
		{name: "configured threshold", value: "3h", expected: 3 * time.Hour},
		{name: "unparseable", value: "soon", expectErr: true},
		{name: "unitless number", value: "30", expectErr: true},
		{name: "explicit zero", value: "0s", expectErr: true},
		{name: "negative", value: "-5m", expectErr: true},
		{name: "beyond upper bound", value: "72h", expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENV", "development")
			t.Setenv("MODE", "api")
			t.Setenv("PR_HEALTH_SWEEP_STALE_AFTER", tt.value)

			cfg := Load()
			err := cfg.ValidateSecrets()
			if tt.expectErr {
				require.ErrorContains(t, err, "PR_HEALTH_SWEEP_STALE_AFTER", "invalid sweep thresholds should fail startup instead of silently changing sweep cadence")
				return
			}
			require.NoError(t, err, "valid sweep thresholds should pass startup validation")
			require.Equal(t, tt.expected, cfg.PRHealthSweepStaleAfter, "Load should resolve the configured sweep threshold")
		})
	}
}
