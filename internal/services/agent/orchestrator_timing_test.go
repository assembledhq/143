package agent

import (
	"bytes"
	"context"
	"errors"
	"path"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestPrepareSandboxRepositoryTimingOnlyForCodeReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		origin   models.SessionOrigin
		wantLogs bool
	}{
		{name: "code review", origin: models.SessionOriginCodeReview, wantLogs: true},
		{name: "ordinary session", origin: models.SessionOriginManual, wantLogs: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			logger := zerolog.New(&output)
			provider := &testInternalSandboxProvider{readFiles: map[string][]byte{
				path.Join("/workspace", repoconfig.ConfigPath): []byte(`{"bootstrap":{"commands":["true"]}}`),
			}}
			err := prepareSandboxRepositoryForSession(context.Background(), provider, &models.Session{Origin: tt.origin}, &Sandbox{ID: "sandbox-1", WorkDir: "/workspace"}, "/workspace", repositoryPreparationFull, logger)
			require.NoError(t, err, "repository preparation should run the declared bootstrap command")
			require.Equal(t, []string{"cd '/workspace' && true"}, provider.execCalls, "both session types should run their declared bootstrap command")
			if tt.wantLogs {
				require.Contains(t, output.String(), `"stage":"repository_preparation"`, "review preparation should emit timing boundaries")
				require.Contains(t, output.String(), `"stage":"repository_bootstrap"`, "review preparation should time bootstrap work")
				require.Contains(t, output.String(), `"outcome":"succeeded"`, "review preparation should record success")
			} else {
				require.NotContains(t, output.String(), `"stage":"repository_preparation"`, "ordinary sessions should not emit the review-specific outer boundary")
				require.NotContains(t, output.String(), `"stage":"repository_bootstrap"`, "ordinary sessions should not emit review-specific inner timing")
			}
		})
	}
}

func TestSandboxCapacityStageOutcome(t *testing.T) {
	t.Parallel()

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		err     error
		outcome string
	}{
		{name: "admitted", ctx: context.Background(), outcome: "succeeded"},
		{name: "host full", ctx: context.Background(), err: ErrSandboxCapacityReached, outcome: "waiting"},
		{name: "configuration failure", ctx: context.Background(), err: ErrSandboxCapacity, outcome: "failed"},
		{name: "counter failure", ctx: context.Background(), err: errors.Join(ErrSandboxCapacity, errors.New("database unavailable")), outcome: "failed"},
		{name: "cancelled while full", ctx: cancelledCtx, err: ErrSandboxCapacityReached, outcome: "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.outcome, sandboxCapacityStageOutcome(tt.ctx, tt.err), "capacity timing should distinguish pressure from failures")
		})
	}
}
