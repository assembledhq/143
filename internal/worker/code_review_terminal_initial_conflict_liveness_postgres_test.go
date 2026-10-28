package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// This records only real sealed results from the actual configured JobStore.
// It never constructs an outcome, modifies a request, or substitutes a runner.
type terminalInitialConflictStore struct {
	actual   *db.JobStore
	outcomes []db.CodeReviewTerminalOutcome
	errors   []error
}

func (s *terminalInitialConflictStore) DeadLetterCodeReviewWithLease(ctx context.Context, orgID, jobID, token uuid.UUID, failure db.CodeReviewTerminalFailure) (db.CodeReviewTerminalOutcome, error) {
	outcome, err := s.actual.DeadLetterCodeReviewWithLease(ctx, orgID, jobID, token, failure)
	s.outcomes = append(s.outcomes, outcome)
	s.errors = append(s.errors, err)
	return outcome, err
}

// The lease clock is an explicit synthetic fixture precondition, not evidence
// that wall-clock time elapsed. Two fixed actual reclaim/poll cycles must not
// execute a known-retired poison job again. Original Q source instead exposes
// running/refusal -> expired/reclaim -> claim/handler repetition beyond max=1.
// This does not exercise provider effects, real Send, or whole-controller work.
func TestCodeReviewTerminalInitialConflictDoesNotReexecutePostgres(t *testing.T) {
	t.Parallel()
	variants := []struct {
		name   string
		status models.CodeReviewAssessmentStatus
		legacy bool
	}{
		{name: "retained failed A", status: models.CodeReviewAssessmentFailed},
		{name: "retained completed A", status: models.CodeReviewAssessmentCompleted},
		{name: "genuine original A absence", status: models.CodeReviewAssessmentFailed, legacy: true},
	}
	paths := []struct {
		name  string
		fatal bool
	}{
		{name: "ordinary exhausted final attempt"},
		{name: "fatal final attempt", fatal: true},
	}
	for _, variant := range variants {
		for _, path := range paths {
			t.Run(variant.name+"/"+path.name, func(t *testing.T) {
				t.Parallel() // Each leaf owns a full-schema database; aggregate root validation uses -parallel=1.
				pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 2)
				require.Equal(t, int32(2), pool.Config().MaxConns, "the actual queue, unit and reserved-renewal paths must remain bounded by two slots")
				f, other := terminalOwnerPointerFixture(t, pool, variant.status, variant.legacy)
				reserved, err := pool.Acquire(context.Background())
				require.NoError(t, err, "reserve only the actual historical linker's second connection")
				err = terminalOwnerPointerLink(context.Background(), reserved, f, other)
				reserved.Release()
				require.NoError(t, err, "commit the genuine preexisting B link before any initial retirement scope read")
				terminalOwnerPointerAssertLinked(t, context.Background(), pool, f, other, variant.legacy)
				node := "synthetic-initial-conflict-" + uuid.NewString()
				_, err = pool.Exec(context.Background(), `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
				require.NoError(t, err, "register one fresh synthetic actual worker without marking any other node dead")
				tag, err := pool.Exec(context.Background(), `UPDATE jobs SET status='pending',locked_by_node_id=NULL,run_owner_id=NULL,
					lock_token=NULL,locked_at=NULL,lease_expires_at=NULL,attempts=0,max_attempts=1,priority=100000,
					target_node_id=$3,run_at=statement_timestamp() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, node)
				require.NoError(t, err, "make only the original fixture controller due for the actual first claim")
				require.Equal(t, int64(1), tag.RowsAffected(), "the exact original controller must be the sole deliberately targeted fixture job")
				ctx, cancel := context.WithTimeout(context.Background(), db.LifecycleTransactionTimeout)
				defer cancel()
				observed := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
				actual, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, observed)
				require.NoError(t, err, "compose the real exact-lease retirement store with its existing bounded Run")
				recorded := &terminalInitialConflictStore{actual: actual}
				w := New(pool, zerolog.Nop(), node, WithCodeReviewTerminalStore(recorded))
				var handlers, hooks atomic.Int32
				var firstClaim json.RawMessage
				var originalToken uuid.UUID
				var latestToken uuid.UUID
				failure := errors.New("Synthetic original controller poison failure")
				terminalError := error(failure)
				if path.fatal {
					terminalError = &FatalError{Err: failure}
				}
				w.Register(models.JobTypeRunCodeReview, func(handlerCtx context.Context, _ string, _ json.RawMessage) error {
					call := handlers.Add(1)
					jobID, ok := jobctx.JobIDFromContext(handlerCtx)
					require.True(t, ok, "the actual Worker.poll must supply its committed claim identity")
					require.Equal(t, f.jobID, jobID, "only the original full controller may enter this logical handler")
					token, ok := jobctx.LockTokenFromContext(handlerCtx)
					require.True(t, ok, "actual claim must supply a real current fencing token")
					require.NotEqual(t, uuid.Nil, token, "the fixture may not manufacture a missing lease token")
					if latestToken != uuid.Nil {
						require.NotEqual(t, latestToken, token, "any unexpected re-claim must obtain a fresh real token rather than reuse fixture authority")
					}
					latestToken = token
					var attempts, maxAttempts int
					require.NoError(t, pool.QueryRow(handlerCtx, `SELECT attempts,max_attempts FROM jobs WHERE org_id=$1 AND id=$2 AND status='running' AND lock_token=$3`, f.job.OrgID, jobID, token).Scan(&attempts, &maxAttempts), "inspect the real committed running claim before terminal classification")
					require.Equal(t, int(call), attempts, "each actual claim must increment attempts exactly once, never a direct handler call")
					require.Equal(t, 1, maxAttempts, "the probe must not extend the existing one-attempt fixture policy")
					if call == 1 {
						originalToken = token
						firstClaim, err = terminalContentionSnapshot(handlerCtx, pool, f)
						require.NoError(t, err, "retain the complete graph after the first actual claim, including its generated lease fields")
					}
					jobctx.RegisterDeadLetterHook(handlerCtx, func(context.Context, error) { hooks.Add(1) })
					return terminalError
				})
				w.poll(ctx) // Synchronous poll cancels and joins its own renewal goroutine.
				var expired, reclaimed []int64
				for cycle := 0; cycle < 2; cycle++ {
					tag, err := pool.Exec(ctx, `UPDATE jobs SET lease_expires_at=statement_timestamp()-interval '1 second'
						WHERE org_id=$1 AND id=$2 AND status='running' AND lock_token=$3`, f.job.OrgID, f.jobID, latestToken)
					require.NoError(t, err, "expire only this fixture's still-current running lease, if retirement failed; never directly requeue")
					expired = append(expired, tag.RowsAffected())
					count, err := db.NewJobStore(pool).ReclaimLostRunningJobs(ctx, time.Time{}, 1)
					require.NoError(t, err, "run the actual bounded recovery query without marking any node stale or changing production lease/retry budgets")
					reclaimed = append(reclaimed, count)
					w.poll(ctx) // Actual claim path, not a manual handler invocation.
				}
				after, err := terminalContentionSnapshot(ctx, pool, f)
				require.NoError(t, err, "inspect the whole durable graph after two actual reclaim/poll boundaries")
				// Deliberately assert only after both fixed probes: unchanged old
				// source can expose the actual three-handler repetition as RED.
				t.Logf("bounded reclaim observations: expired=%v reclaimed=%v handler_calls=%d retirement_attempts=%d hook_calls=%d", expired, reclaimed, handlers.Load(), len(recorded.outcomes), hooks.Load())
				require.Equal(t, []int64{0, 0}, expired, "known committed job-only retirement must leave no running lease to expire in either cycle")
				require.Equal(t, []int64{0, 0}, reclaimed, "actual recovery must find no poison job to requeue after known job-only commit")
				require.Equal(t, int32(1), handlers.Load(), "initial poison must run once, not be reclaimed and reexecuted beyond max_attempts=1")
				require.Zero(t, hooks.Load(), "known committed job-only retirement must suppress all target-derived hooks")
				require.Len(t, recorded.outcomes, 1, "only the first actual poll may attempt retirement")
				require.Equal(t, []error{nil}, recorded.errors, "the actual store must confirm retirement rather than expose a refused scope error")
				outcome := recorded.outcomes[0]
				require.True(t, outcome.Matches(f.job.OrgID, f.jobID, originalToken), "the sealed result must match exactly the actual original claim and known commit")
				scope, hasOwner := outcome.Scope()
				require.False(t, hasOwner, "positive initial inconsistency authorizes no metadata or assessment owner")
				require.Equal(t, db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: originalToken, JobType: models.JobTypeRunCodeReview}, scope, "the known job-only result must scrub every projected target")
				fullAssessmentDispatchAssertRun(t, observed)
				terminalOwnerPointerAssertLinked(t, ctx, pool, f, other, variant.legacy)
				terminalOwnerPointerAssertJobOnly(t, firstClaim, after, f.jobID, terminalError.Error())
			})
		}
	}
}
