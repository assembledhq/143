package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type workspaceRetryStoreStub struct {
	params       db.RetryCodeReviewWorkspaceControllerParams
	orgID        uuid.UUID
	runAt        time.Time
	ok           bool
	err          error
	calls        int
	keepSchedule bool
}

func (s *workspaceRetryStoreStub) RetryControllerWithLease(_ context.Context, orgID uuid.UUID, p db.RetryCodeReviewWorkspaceControllerParams) (time.Time, bool, error) {
	s.calls++
	s.orgID, s.params = orgID, p
	if s.keepSchedule {
		s.runAt = p.RunAt
	}
	return s.runAt, s.ok, s.err
}

type workspaceControllerJobStoreStub struct {
	checkpointRetryStore
	genericRetries int
}

func (s *workspaceControllerJobStoreStub) RetryWithoutConsumingAttemptWithLease(ctx context.Context, id, token uuid.UUID, message string, runAt time.Time) (bool, error) {
	s.genericRetries++
	return s.checkpointRetryStore.RetryWithoutConsumingAttemptWithLease(ctx, id, token, message, runAt)
}

func TestWorkerWorkspaceControllerRetryUsesDurableHandshake(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                string
		typed                               bool
		supported                           bool
		jobType                             string
		owned                               bool
		scheduleErr                         error
		notReady                            bool
		wantSpecial, wantGeneric, wantHooks int
	}{
		{name: "ready workspace uses atomic retry and actual schedule", typed: true, supported: true, jobType: models.JobTypeRunCodeReview, owned: true, wantSpecial: 1, wantHooks: 1},
		{name: "unready workspace retains polling without a premature wake", typed: true, supported: true, jobType: models.JobTypeRunCodeReview, owned: true, notReady: true, wantSpecial: 1, wantHooks: 1},
		{name: "lost controller lease cannot publish a schedule", typed: true, supported: true, jobType: models.JobTypeRunCodeReview, wantSpecial: 1},
		{name: "failed transaction leaves controller for lease recovery", typed: true, supported: true, jobType: models.JobTypeRunCodeReview, scheduleErr: errors.New("database unavailable"), wantSpecial: 1},
		{name: "legacy untyped retry keeps polling", supported: true, jobType: models.JobTypeRunCodeReview, wantGeneric: 1, wantHooks: 1},
		{name: "store without specialized support keeps polling", typed: true, jobType: models.JobTypeRunCodeReview, wantGeneric: 1, wantHooks: 1},
		{name: "preparation retry never uses controller handshake", typed: true, supported: true, jobType: models.JobTypePrepareCodeReviewWorkspace, wantGeneric: 1, wantHooks: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, reviewID, sessionID := uuid.New(), uuid.New(), uuid.New()
			startedAt := time.Now().UTC()
			store := &workspaceControllerJobStoreStub{checkpointRetryStore: checkpointRetryStore{job: models.Job{ID: uuid.New(), OrgID: orgID, JobType: tt.jobType, CreatedAt: startedAt, MaxAttempts: 8}}}
			special := &workspaceRetryStoreStub{runAt: startedAt, ok: tt.owned, err: tt.scheduleErr, keepSchedule: tt.notReady}
			w := &Worker{jobs: store, logger: zerolog.Nop(), nodeID: "worker-test", handlers: make(map[string]JobHandler), leaseDuration: time.Minute, renewInterval: time.Hour, wakeCh: make(chan struct{}, 1)}
			if tt.supported {
				w.workspaceRetries = special
			}
			retry := codeReviewControllerWorkspaceWaitError("preparation pending", startedAt)
			if tt.typed {
				retry.codeReviewWorkspaceWait = &codeReviewWorkspaceControllerWait{OrgID: orgID, ReviewID: reviewID, SessionID: sessionID, ExpectedHead: "head-current"}
			}
			var hookTimes []time.Time
			w.Register(tt.jobType, func(ctx context.Context, _ string, _ json.RawMessage) error {
				jobctx.RegisterRetryScheduledHook(ctx, func(_ context.Context, hookErr error, runAt time.Time) {
					require.ErrorIs(t, hookErr, retry, "retry hook should preserve the handler's wait error")
					hookTimes = append(hookTimes, runAt)
				})
				return retry
			})
			before := time.Now().UTC()
			w.poll(context.Background())
			after := time.Now().UTC()
			require.Equal(t, tt.wantSpecial, special.calls, "only typed controller waits with store support should enter the serialized retry path")
			require.Equal(t, tt.wantGeneric, store.genericRetries, "durable retry rejection must not fall through to an unfenced generic retry")
			require.Len(t, hookTimes, tt.wantHooks, "only successful lease-owned scheduling should publish retry metadata")
			if tt.wantSpecial != 0 {
				require.Equal(t, orgID, special.orgID, "workspace retry should remain scoped to the claimed job tenant")
				require.Equal(t, db.RetryCodeReviewWorkspaceControllerParams{OrgID: orgID, ReviewID: reviewID, SessionID: sessionID, JobID: store.job.ID, JobLockToken: *store.job.LockToken, ExpectedHead: "head-current", RunAt: special.params.RunAt, ErrorMessage: retry.Error()}, special.params, "retry should correlate the exact controller lease and review revision")
				require.WithinRange(t, special.params.RunAt, before.Add(codeReviewWorkspaceWait), after.Add(codeReviewWorkspaceWait), "non-ready workspace should retain its three-second poll schedule")
				if tt.wantHooks != 0 {
					require.Equal(t, []time.Time{special.runAt}, hookTimes, "ready workspace should expose the actual immediate database schedule to retry hooks")
					wantWake := 1
					if tt.notReady {
						wantWake = 0
					}
					require.Equal(t, wantWake, len(w.wakeCh), "only a due committed retry should wake ordinary priority-aware queue polling")
				} else {
					require.Equal(t, 0, len(w.wakeCh), "rejected retries must not signal a controller wake")
				}
			} else {
				require.Equal(t, 0, store.job.Attempts, "ordinary polling should continue preserving the attempt budget")
			}
		})
	}
}
