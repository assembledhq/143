package config

import (
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/require"
)

func TestExecutorRetentionEnvironment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		environment map[string]string
		interval    time.Duration
		retention   time.Duration
		batchSize   int
		timeout     time.Duration
		wantErr     bool
	}{
		{name: "default evidence window and bounded cadence", interval: 5 * time.Minute, retention: 24 * time.Hour, batchSize: 100, timeout: 30 * time.Second},
		{name: "operator overrides", environment: map[string]string{"SESSION_EXECUTOR_GC_INTERVAL": "10m", "SESSION_EXECUTOR_RETENTION": "72h", "SESSION_EXECUTOR_GC_BATCH_SIZE": "25", "SESSION_EXECUTOR_GC_TIMEOUT": "15s"}, interval: 10 * time.Minute, retention: 72 * time.Hour, batchSize: 25, timeout: 15 * time.Second},
		{name: "operator disables collection", environment: map[string]string{"SESSION_EXECUTOR_GC_INTERVAL": "0"}, retention: 24 * time.Hour, batchSize: 100, timeout: 30 * time.Second},
		{name: "invalid retention is rejected", environment: map[string]string{"SESSION_EXECUTOR_RETENTION": "yesterday"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// An explicit environment keeps parallel cases independent of the
			// developer's process environment and production configuration.
			environment := make(map[string]string, len(tt.environment))
			for key, value := range tt.environment {
				environment[key] = value
			}
			var cfg Config
			err := env.ParseWithOptions(&cfg, env.Options{Environment: environment})
			if tt.wantErr {
				require.Error(t, err, "invalid retention duration must not silently shorten evidence retention")
				return
			}
			require.NoError(t, err, "valid executor retention settings should parse independently")
			require.Equal(t, tt.interval, cfg.SessionExecutorGCInterval, "operator collection cadence should be honored")
			require.Equal(t, tt.retention, cfg.SessionExecutorRetention, "operator evidence retention window should be honored")
			require.Equal(t, tt.batchSize, cfg.SessionExecutorGCBatchSize, "operator candidate limit should be honored")
			require.Equal(t, tt.timeout, cfg.SessionExecutorGCTimeout, "operator pass deadline should be honored")
		})
	}
}
