package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestWorkerDeadLetterHooksRequireCommittedOwnership(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		owned     bool
		writeErr  error
		jobType   string
		terminal  error
		wantCalls int
		wantHooks int
	}{
		{name: "successful fenced write runs cleanup", owned: true, wantCalls: 1, wantHooks: 1},
		{name: "lost fence suppresses cleanup", wantCalls: 1},
		{name: "write failure suppresses cleanup", writeErr: errors.New("database unavailable"), wantCalls: 1},
		{name: "missing atomic review capability stays active", jobType: models.JobTypeRunCodeReview},
		{name: "missing atomic recheck capability stays active", jobType: models.JobTypeRunCodeReviewRecheck},
		{name: "ended parent rejection uses guarded recovery", jobType: models.JobTypeRunCodeReview, terminal: errCodeReviewEndedParentRecovery, owned: true, wantCalls: 1, wantHooks: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, token := uuid.New(), uuid.New()
			calls, hooks := 0, 0
			store := &terminalLeaseStoreStub{deadLetterFn: func(_ context.Context, jobID, lockToken uuid.UUID, _ string) (bool, error) {
				calls++
				require.Equal(t, id, jobID, "terminal persistence must use the claimed job")
				require.Equal(t, token, lockToken, "terminal persistence must use the claimed fence")
				return tt.owned, tt.writeErr
			}}
			w := &Worker{jobs: store, logger: zerolog.Nop()}
			ctx := jobctx.WithDeadLetterHooks(context.Background())
			jobctx.RegisterDeadLetterHook(ctx, func(context.Context, error) { hooks++ })
			terminal := tt.terminal
			if terminal == nil {
				terminal = errors.New("handler exhausted")
			}
			w.deadLetterJob(ctx, ctx, &models.Job{ID: id, LockToken: &token, JobType: tt.jobType}, terminal)
			require.Equal(t, tt.wantCalls, calls, "unsupported full review retirement must not fall back to a non-atomic write")
			require.Equal(t, tt.wantHooks, hooks, "only a committed owner may run terminal cleanup")
		})
	}
}
