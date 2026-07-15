package agent

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestInjectInternalAPIEnvScopesAutomationOutcomeToPrimaryThread(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		primary     bool
		wantEnabled bool
	}{
		{name: "primary Main thread receives reporter", primary: true, wantEnabled: true},
		{name: "review-loop thread cannot report", primary: false, wantEnabled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			repoID := uuid.New()
			sessionID := uuid.New()
			runID := uuid.New()
			primaryThreadID := uuid.New()
			threadID := uuid.New()
			if tt.primary {
				threadID = primaryThreadID
			}
			session := &models.Session{
				ID: sessionID, OrgID: orgID, RepositoryID: &repoID,
				Origin: models.SessionOriginAutomation, AutomationRunID: &runID,
				PrimaryThreadID: &primaryThreadID,
			}
			orchestrator := &Orchestrator{internalAPIURL: "http://internal", internalAPISecret: "secret"}
			cfg := &SandboxConfig{Env: map[string]string{}, Timeout: time.Minute}
			orchestrator.injectInternalAPIEnv(context.Background(), session, &repoID, &threadID, cfg, zerolog.Nop())

			require.Equal(t, tt.wantEnabled, cfg.Env["AUTOMATION_RUN_REPORTING_ENABLED"] == "true", "only the primary thread should receive the reporting feature flag")
			claims, err := auth.ValidateInternalToken("secret", cfg.Env["INTERNAL_API_TOKEN"])
			require.NoError(t, err, "injected internal token should validate")
			hasScope := false
			for _, scope := range claims.AllowedToolScopes {
				if scope == "automation-run:report-outcome" {
					hasScope = true
				}
			}
			require.Equal(t, tt.wantEnabled, hasScope, "only the primary thread token should carry the reporting scope")
		})
	}
}
