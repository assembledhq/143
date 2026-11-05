package agent_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestWithCodeReviewExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		origin     models.SessionOrigin
		noSession  bool
		noThread   bool
		zeroThread bool
		expected   bool
	}{
		{name: "built-in review thread", origin: models.SessionOriginCodeReview, expected: true},
		{name: "ordinary thread", origin: models.SessionOriginManual},
		{name: "missing session", noSession: true},
		{name: "review parent without thread", origin: models.SessionOriginCodeReview, noThread: true},
		{name: "review parent with zero thread", origin: models.SessionOriginCodeReview, zeroThread: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := &models.Session{Origin: tt.origin}
			if tt.noSession {
				session = nil
			}
			threadID := uuid.New()
			if tt.zeroThread {
				threadID = uuid.Nil
			}
			thread := &threadID
			if tt.noThread {
				thread = nil
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := agent.WithCodeReviewExecution(parent, session, thread)

			require.False(t, agent.IsCodeReviewExecution(parent), "review identity must not mutate the caller context")
			require.Equal(t, tt.expected, agent.IsCodeReviewExecution(ctx), "only a persisted built-in review with a real thread should receive review runtime defaults")
			cancel()
			require.ErrorIs(t, ctx.Err(), context.Canceled, "execution context must preserve runtime cancellation")
		})
	}
}
