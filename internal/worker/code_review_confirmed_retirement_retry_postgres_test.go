package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// This is actual claim/settlement storage composition, not a Send or provider
// test. Each parallel leaf owns a complete isolated schema. Root bounds total
// schema capacity with -parallel=1; the observer reserves one of only two slots.
func TestConfirmedPublicationLateKnownAbortClaimPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		confirmed bool
		state     db.ReviewerDispatchState
		attempts  int32
	}{
		{name: "retained original attempt cannot resend after confirmed retirement", confirmed: true, state: db.ReviewerDispatchCancelled, attempts: 1},
		{name: "unconfirmed active owner retains its existing known-abort retry", state: db.ReviewerDispatchAttempting, attempts: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "late original settlement and claim must fit one actual application slot beside the independent observer")
			f, foreign, children, foreignChildren := terminalDispatchOwnershipFixture(t, ctx, pool, "attempting")
			original := children[0].id
			require.Equal(t, f.job.MetadataID, children[0].metadata, "the first canary must be the actual original attempted owner, not its pending sibling")
			proof := confirmedRetirementProofCase{}
			if !tt.confirmed {
				proof.publication = "uncertain"
			}
			confirmedRetirementProofStage(t, ctx, pool, f, foreign, proof)
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the literal independent observer before any bounded original owner transaction")
			defer observer.Release()
			before, err := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, err, "freeze every original/sibling owner, immutable command/wake and durable attempt before retirement")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, before), children)
			foreignBefore, err := terminalContentionSnapshot(ctx, observer, foreign)
			require.NoError(t, err, "retain the genuine foreign pending owner and full evidence graph")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBefore), foreignChildren)
			attempted := terminalOwnerPointerPositiveRow(t, terminalOwnerPointerPositiveDecode(t, before), "results", original)
			if tt.confirmed {
				jobs, constructionErr := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, f.stores.ThreadSendDependencies.Transactions)
				require.NoError(t, constructionErr, "compose actual scoped retirement with the existing bounded transaction owner")
				retired, retirementErr := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token,
					db.CodeReviewTerminalFailure{JobError: "Synthetic exhausted original completion", Reason: "Synthetic exhausted original completion", Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic ordinary failure"})
				require.NoError(t, retirementErr, "retire the exact confirmed original controller without fabricating failed review state")
				require.True(t, retired.Matches(f.job.OrgID, f.jobID, f.token), "late original abort must occur only after actual known committed exact-lease retirement")
				require.True(t, retired.CompletionRecoveryPending(), "the positive confirmed original receipt must preserve its active domain for completion recovery")
				afterRetirement, readErr := terminalContentionSnapshot(ctx, observer, f)
				require.NoError(t, readErr, "inspect the independent committed original attempt after retirement")
				require.Equal(t, attempted, terminalOwnerPointerPositiveRow(t, terminalOwnerPointerPositiveDecode(t, afterRetirement), "results", original), "retirement must not cancel or rewrite the already attempting command, its identity or immutable input")
			}
			var active db.ReviewerDispatchIntent
			fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
				var readErr error
				active, readErr = fullAssessmentDispatchRead(unitCtx, tx, f, original)
				return readErr
			})
			require.Equal(t, db.ReviewerDispatchAttempting, active.State(), "the later known abort must correlate with the original still-attempting command")
			oldAttempt := active.Expectation().AttemptID()
			abort, err := db.ReviewerDispatchRetryAfterKnownAbort(db.ReviewerDispatchAbortContention)
			require.NoError(t, err, "only a real positive known-abort classification may request the existing retry transition")
			var pending db.ReviewerDispatchIntent
			fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
				var changed bool
				var settleErr error
				pending, changed, settleErr = db.SettleReviewerDispatchInTx(unitCtx, f.job.OrgID, tx, active.Expectation(), abort)
				if settleErr == nil && !changed {
					return db.ErrReviewerDispatchInput
				}
				return settleErr
			})
			require.Equal(t, db.ReviewerDispatchPending, pending.State(), "truthful known abort keeps its existing durable retry history; later claim separately decides new-send eligibility")
			require.Equal(t, oldAttempt, pending.Expectation().AttemptID(), "known-abort settlement must not manufacture a new send attempt")
			// Wait OUTSIDE every bounded owner for the actual saved retry due
			// time, not an arbitrary sleep or forbidden pending->pending clock
			// rewrite. The real schema intentionally rejects that rewrite.
			waitCtx, cancelWait := context.WithTimeout(ctx, 6*time.Second)
			defer cancelWait()
			var databaseNow time.Time
			require.NoError(t, observer.QueryRow(waitCtx, `SELECT statement_timestamp()`).Scan(&databaseNow), "derive the wait from the actual database clock and persisted retry deadline")
			remaining := pending.DueAt().Sub(databaseNow)
			require.LessOrEqual(t, remaining, 5*time.Second, "known-abort attempt one must retain its existing five-second retry schedule")
			if remaining > 0 {
				timer := time.NewTimer(remaining + time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-waitCtx.Done():
					require.NoError(t, waitCtx.Err(), "the actual persisted retry deadline must become due within the finite test-only observation guard")
				}
			}
			require.NoError(t, observer.QueryRow(waitCtx, `SELECT statement_timestamp()`).Scan(&databaseNow), "verify actual due time rather than inferring it from timer delivery")
			require.False(t, databaseNow.Before(pending.DueAt()), "the real fresh claim must observe a genuinely due original retry, with no clock or timeout override")
			baseline, err := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, err, "retain the complete actual due known-abort history immediately before the fresh claim")
			expected := terminalOwnerPointerPositiveDecode(t, baseline)
			expectedRow := terminalOwnerPointerPositiveRow(t, expected, "results", original)
			require.Equal(t, fullAssessmentDispatchJSON(t, "contention"), expectedRow["dispatch_detail"], "the real known-abort code must survive independently of new-send authorization")
			runner := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			newAttempt := uuid.New()
			var claimed db.ReviewerDispatchIntent
			err = runner.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
				claimed = db.ReviewerDispatchIntent{}
				var changed bool
				var claimErr error
				claimed, changed, claimErr = db.ClaimReviewerDispatchInTx(unitCtx, f.job.OrgID, tx, pending.Expectation(), newAttempt)
				if claimErr == nil && !changed {
					return db.ErrReviewerDispatchInput
				}
				return claimErr
			})
			require.NoError(t, err, "the original due claim must settle through actual SQL rather than fabricate an accepted or aborted send")
			fullAssessmentDispatchAssertRun(t, runner)
			after, err := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, err, "inspect every durable row after the real fresh claim decision")
			actual := terminalOwnerPointerPositiveDecode(t, after)
			actualRow := terminalOwnerPointerPositiveRow(t, actual, "results", original)
			var afterClock time.Time
			require.NoError(t, observer.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&afterClock), "bound the actual claim or cancellation statement using an independent postcommit database clock")
			var changedAt time.Time
			require.NoError(t, json.Unmarshal(actualRow["dispatch_updated_at"], &changedAt), "decode the actual fresh decision timestamp independently of its returned carrier")
			require.WithinRange(t, changedAt, runner.clock, afterClock, "only this original bounded transaction may change the dispatch decision clock")
			expectedRow["dispatch_state"] = fullAssessmentDispatchJSON(t, tt.state)
			expectedRow["dispatch_writer_seq"] = fullAssessmentDispatchJSON(t, pending.Expectation().WriterSeq()+1)
			expectedRow["dispatch_updated_at"] = actualRow["dispatch_updated_at"]
			if tt.confirmed {
				expectedRow["dispatch_resolution"] = fullAssessmentDispatchJSON(t, "owner_ineligible")
				expectedRow["dispatch_due_at"] = json.RawMessage(`null`)
			} else {
				expectedRow["dispatch_attempt_id"] = fullAssessmentDispatchJSON(t, newAttempt)
				expectedRow["dispatch_attempt_seq"] = fullAssessmentDispatchJSON(t, tt.attempts)
				expectedRow["dispatch_detail"] = json.RawMessage(`null`)
				for _, field := range []string{"dispatch_attempted_at", "dispatch_due_at"} {
					var observed time.Time
					require.NoError(t, json.Unmarshal(actualRow[field], &observed), "decode each actual new-attempt clock before permitting that one explicit field change")
					lower, upper := runner.clock, afterClock
					if field == "dispatch_due_at" {
						lower, upper = lower.Add(time.Minute), upper.Add(time.Minute)
					}
					require.WithinRange(t, observed, lower, upper, "unconfirmed control must preserve the existing real attempt lease schedule")
					expectedRow[field] = actualRow[field]
				}
			}
			require.Equal(t, tt.state, claimed.State(), "confirmed original publication cancels late retry instead of authorizing a second send, while unconfirmed active work retains the existing claim")
			require.Equal(t, tt.attempts, claimed.Expectation().AttemptSeq(), "confirmed cancellation must not increment the real original send attempt counter")
			require.Equal(t, expected, actual, "only the exact original pending dispatch decision may change; every original receipt/outcome/metadata/job/reservation, immutable input/wake and sibling row must remain exact")
			foreignAfter, readErr := terminalContentionSnapshot(ctx, observer, foreign)
			require.NoError(t, readErr, "read the genuine foreign owner after both actual settlement and claim")
			require.Equal(t, foreignBefore, foreignAfter, "an original late abort cannot mutate or borrow any foreign command or owner")
		})
	}
}
