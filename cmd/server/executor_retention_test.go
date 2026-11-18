package main

import (
	"testing"
	"time"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/worker"
	dockerclient "github.com/docker/docker/client"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestConfigureExecutorContainerGC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		change     func(*config.Config)
		missingDB  bool
		missingAPI bool
		wantGC     bool
		wantErr    bool
	}{
		{name: "enabled worker gets collector", wantGC: true},
		{name: "disabled collector needs no dependencies", change: func(c *config.Config) { c.SessionExecutorGCInterval = 0 }, missingDB: true, missingAPI: true},
		{name: "missing database rejects enabled collector", missingDB: true, wantErr: true},
		{name: "missing Docker rejects enabled collector", missingAPI: true, wantErr: true},
		{name: "zero retention rejected", change: func(c *config.Config) { c.SessionExecutorRetention = 0 }, wantErr: true},
		{name: "unbounded batch rejected", change: func(c *config.Config) { c.SessionExecutorGCBatchSize = 1001 }, wantErr: true},
		{name: "zero deadline rejected", change: func(c *config.Config) { c.SessionExecutorGCTimeout = 0 }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{SessionExecutorGCInterval: 5 * time.Minute, SessionExecutorRetention: 24 * time.Hour, SessionExecutorGCBatchSize: 100, SessionExecutorGCTimeout: 30 * time.Second}
			if tt.change != nil {
				tt.change(cfg)
			}
			// Wiring must not make database or Docker requests. These inert
			// handles allocate no external resources and cannot reach a host.
			pool := &pgxpool.Pool{}
			docker := &dockerclient.Client{}
			if tt.missingDB {
				pool = nil
			}
			if tt.missingAPI {
				docker = nil
			}
			svc := &worker.Services{}
			err := configureExecutorContainerGC(svc, cfg, pool, docker, zerolog.Nop())
			if tt.wantErr {
				require.Error(t, err, "unsafe enabled collector configuration must fail startup wiring")
			} else {
				require.NoError(t, err, "valid or disabled retention configuration should wire without host access")
			}
			require.Equal(t, tt.wantGC, svc.ExecutorContainerGC != nil, "collector should be installed only when enabled and valid")
		})
	}
}
