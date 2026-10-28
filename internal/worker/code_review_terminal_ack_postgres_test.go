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
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// terminalACKRun injects ambiguity only at the application dependency boundary.
// The existing actual bounded Run still owns every statement and settlement.
// No PgError, transaction result, commit, rollback, kernel fault or replay is
// fabricated. The rollback path returns one finite nonretry sentinel only AFTER
// all actual retirement work and its complete provisional graph are observed.
type terminalACKRun struct {
	actual                     *fullAssessmentDispatchRunObserver
	fixture                    strandedReviewFixture
	rollback, unknown          bool
	sentinel, unknownACK       error
	calls, completedCallbacks  int
	callbackError, actualError error
	staged                     json.RawMessage
	stagedObserved             time.Time
}

func (r *terminalACKRun) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	r.calls++
	r.actualError = r.actual.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		r.callbackError = work(unitCtx, tx)
		if r.callbackError != nil {
			return r.callbackError
		}
		var err error
		r.staged, err = terminalContentionSnapshot(unitCtx, tx, r.fixture)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(unitCtx, `SELECT statement_timestamp()`).Scan(&r.stagedObserved); err != nil {
			return err
		}
		r.completedCallbacks++
		if r.rollback {
			return r.sentinel
		}
		return nil
	})
	if r.rollback {
		// The unchanged runner returns this SAME nonretry sentinel only after
		// successful real rollback. A joined cleanup error cannot masquerade
		// as that confirmation and is not replaced by the injected ACK error.
		if r.actualError != r.sentinel {
			if r.actualError == nil {
				return errors.New("synthetic rollback settlement was not observed")
			}
			return r.actualError
		}
	} else if r.actualError != nil {
		return r.actualError
	}
	if r.unknown {
		return r.unknownACK
	}
	return nil
}

// This transparent adapter records the actual JobStore result returned to the
// actual worker. It cannot manufacture the sealed outcome or hide an error.
type terminalACKStore struct {
	actual  *db.JobStore
	calls   int
	failure db.CodeReviewTerminalFailure
	outcome db.CodeReviewTerminalOutcome
	err     error
}

func (s *terminalACKStore) DeadLetterCodeReviewWithLease(ctx context.Context, orgID, jobID, token uuid.UUID, failure db.CodeReviewTerminalFailure) (db.CodeReviewTerminalOutcome, error) {
	s.calls++
	s.failure = failure
	s.outcome, s.err = s.actual.DeadLetterCodeReviewWithLease(ctx, orgID, jobID, token, failure)
	return s.outcome, s.err
}

// This is APPLICATION-DEPENDENCY ACK injection, not packet/kernel/proxy loss,
// provider delivery, all production hooks/SSE, or durable effect delivery proof.
// A real commit with its acknowledgment replaced and a real rollback with the
// same unconfirmed application error must both suppress every registered probe.
// The known-ACK canary requires that the identical real hook actually fires.
func TestCodeReviewTerminalApplicationACKBoundaryPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		rollback, unknown bool
		expectedHooks     int32
	}{
		{name: "actual whole commit with application acknowledgment suppressed", unknown: true},
		{name: "actual whole rollback with same unconfirmed application acknowledgment", rollback: true, unknown: true},
		{name: "known actual whole commit releases the nonvacuous hook canary", expectedHooks: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Each leaf owns its full-schema database; root bounds aggregate capacity with -parallel=1.
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "actual claim, renewal and bounded retirement must fit literally two slots without a held observer checkout")
			f, foreign, children, foreignChildren := terminalDispatchOwnershipFixture(t, ctx, pool, "pending")
			var original uuid.UUID
			for _, child := range children {
				if child.metadata == f.job.MetadataID {
					original = child.id
				}
			}
			require.NotEqual(t, uuid.Nil, original, "the actual Prepare-born original pending intent must really exist before the ACK proof")
			foreignBefore, err := terminalContentionSnapshot(ctx, pool, foreign)
			require.NoError(t, err, "retain the complete genuine foreign tenant graph and original pending wake")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBefore), foreignChildren)
			node := "synthetic-terminal-ack:" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register exactly one synthetic live queue consumer")
			tag, err := pool.Exec(ctx, `UPDATE jobs SET status='pending',locked_by_node_id=NULL,run_owner_id=NULL,lock_token=NULL,
				locked_at=NULL,lease_expires_at=NULL,attempts=0,max_attempts=1,priority=100000,target_node_id=$3,
				run_at=statement_timestamp() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, node)
			require.NoError(t, err, "make only the original controller due for the real first claim without changing its immutable payload or wake")
			require.Equal(t, int64(1), tag.RowsAffected(), "the real queue must claim the exact original controller, not an absent fixture or another sibling")
			observed := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			injected := &terminalACKRun{actual: observed, fixture: f, rollback: tt.rollback, unknown: tt.unknown,
				sentinel: errors.New("synthetic finite callback rollback boundary"), unknownACK: errors.New("synthetic unconfirmed application dependency acknowledgment")}
			actualStore, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, injected)
			require.NoError(t, err, "supply the actual JobStore with an explicitly decorated existing bounded Run, not fabricated outcome authority")
			recorded := &terminalACKStore{actual: actualStore}
			w := New(pool, zerolog.Nop(), node, WithCodeReviewTerminalStore(recorded))
			var handlers, hooks, targetEffects atomic.Int32
			var claimed, hookGraph json.RawMessage
			var token uuid.UUID
			terminalErr := &FatalError{Err: errors.New("Synthetic fatal retirement ACK probe")}
			w.Register(models.JobTypeRunCodeReview, func(handlerCtx context.Context, _ string, _ json.RawMessage) error {
				handlers.Add(1)
				jobID, present := jobctx.JobIDFromContext(handlerCtx)
				require.True(t, present, "the logical fatal controller must run only through actual Worker.poll claim context")
				require.Equal(t, f.jobID, jobID, "the actual queue must enter only the original full-controller handler")
				var found bool
				token, found = jobctx.LockTokenFromContext(handlerCtx)
				require.True(t, found, "the actual first claim must provide its real fenced token")
				require.NotEqual(t, uuid.Nil, token, "no synthetic missing token may short-circuit retirement before the ACK boundary")
				claimed, err = terminalContentionSnapshot(handlerCtx, pool, f)
				require.NoError(t, err, "retain the complete baseline after the actual claim, not before its real token and ownership fields commit")
				terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, claimed), children)
				jobctx.RegisterDeadLetterHook(handlerCtx, func(hookCtx context.Context, failure error) {
					hooks.Add(1)
					outcome, scope, confirmed := committedCodeReviewTerminalOutcome(hookCtx, f.job.OrgID, models.JobTypeRunCodeReview)
					require.True(t, confirmed, "the positive hook must receive the actual worker-installed known-committed carrier")
					require.True(t, outcome.Matches(f.job.OrgID, f.jobID, token), "the hook's real carrier must bind the exact actual original claim")
					require.True(t, codeReviewTerminalScopeMatchesFull(scope, f.job), "target effect authorization must retain the complete original full owner instead of a sibling")
					require.Equal(t, terminalErr, failure, "the hook must receive the original actual terminal error, not the injected dependency acknowledgment")
					hookGraph, err = terminalContentionSnapshot(hookCtx, pool, f)
					require.NoError(t, err, "the nonvacuous hook must independently observe the already committed whole retirement graph")
					targetEffects.Add(1) // Logical probe only; no provider, notification or durable effect is invoked.
				})
				return terminalErr
			})
			w.poll(ctx) // The actual poll cancels and joins its renewal goroutine before settlement.
			after, err := terminalContentionSnapshot(ctx, pool, f)
			require.NoError(t, err, "read every durable original-tenant row independently after actual poll and settlement return")
			foreignAfter, err := terminalContentionSnapshot(ctx, pool, foreign)
			require.NoError(t, err, "independently read all original foreign-tenant rows after the ACK boundary")
			var observedAfter time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&observedAfter), "bound the real cancellation statement by an independent post-settlement database clock")
			require.Equal(t, int32(1), handlers.Load(), "the actual worker must not reexecute its handler after any commit-attempt/application ACK result")
			require.Equal(t, 1, recorded.calls, "actual Worker.deadLetterJob must call the configured retirement owner once with no alternate fallback")
			require.Equal(t, 1, injected.calls, "the decorator must invoke exactly one supplied actual bounded Run")
			require.Equal(t, 1, injected.completedCallbacks, "all actual job, eligible assessment, exact metadata and pending writes must finish once before injecting acknowledgment ambiguity")
			require.NoError(t, injected.callbackError, "neither constructor refusal nor a storage error may masquerade as the intended post-work ACK boundary")
			fullAssessmentDispatchAssertRun(t, observed)
			expectedFailure := db.CodeReviewTerminalFailure{JobError: terminalErr.Error(), Reason: "code review job failed: " + terminalErr.Error(),
				Code: models.CodeReviewStatusCodeWorkerFailed, Message: "The review stopped because of a non-retryable error. Check the failure details before trying again."}
			require.Equal(t, expectedFailure, recorded.failure, "the actual worker's finite original failure classification must reach the one real unit unchanged")
			require.NotEmpty(t, injected.staged, "a genuine complete provisional retirement graph must exist before the dependency boundary is altered")
			terminalDispatchOwnershipAssertGraph(t, claimed, injected.staged, f, expectedFailure, original, true, true, observed.clock, injected.stagedObserved)
			require.Equal(t, foreignBefore, foreignAfter, "all real foreign pending commands, wake/history, jobs and business rows must remain byte-identical")
			require.Equal(t, tt.expectedHooks, hooks.Load(), "ambiguous ACK must suppress hooks while the identical known-ACK canary proves registration is nonvacuous")
			require.Equal(t, tt.expectedHooks, targetEffects.Load(), "only the actual known-commit carrier may release this logical exact-target effect probe")
			if tt.rollback {
				require.Same(t, injected.sentinel, injected.actualError, "the unmodified runner must return the exact finite nonretry sentinel after successful actual rollback, not a joined cleanup failure")
				require.Equal(t, claimed, after, "actual rollback must restore the exact complete graph captured after the real claim, including every original lease and pending field")
			} else {
				require.NoError(t, injected.actualError, "the supplied existing Run must actually complete its whole commit before the application acknowledgment may be suppressed")
				require.Equal(t, injected.staged, after, "actual commit must preserve exactly the entire provisional graph, never a partial job/assessment/metadata/pending retirement")
				terminalDispatchOwnershipAssertGraph(t, claimed, after, f, expectedFailure, original, true, true, observed.clock, observedAfter)
			}
			if tt.unknown {
				require.Same(t, injected.unknownACK, recorded.err, "both actual commit and actual rollback must expose the same unconfirmed application error shape")
				require.Equal(t, db.CodeReviewTerminalOutcome{}, recorded.outcome, "outer JobStore must discard every provisional target, metadata, assessment and owned bit on acknowledgment error")
				require.Empty(t, hookGraph, "no hook may receive target authority merely because provisional rows were positive")
			} else {
				require.NoError(t, recorded.err, "known actual commit acknowledgment must remain successful")
				require.True(t, recorded.outcome.Matches(f.job.OrgID, f.jobID, token), "the positive canary must retain a real sealed known-commit result for the exact claimed job")
				require.Equal(t, after, hookGraph, "the positive hook must have observed the same independently confirmed whole committed graph")
				metadata, changedMetadata := recorded.outcome.Metadata()
				require.True(t, changedMetadata, "the positive canary must release an actual changed metadata descriptor, not a zero-owner shortcut")
				storedMetadata, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
				require.NoError(t, err, "independently read only the original full owner's committed metadata")
				require.Equal(t, storedMetadata, metadata, "the released descriptor must exactly equal independently read committed storage")
				assessment, changedAssessment := recorded.outcome.Assessment()
				require.True(t, changedAssessment, "the positive canary must include the eligible original assessment failure")
				storedAssessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
				require.NoError(t, err, "independently read only the exact original committed assessment")
				require.Equal(t, storedAssessment, assessment, "released assessment evidence must equal actual committed original storage")
			}
		})
	}
}
