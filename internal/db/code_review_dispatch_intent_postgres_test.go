package db

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type reviewerDispatchPGFixture struct {
	pool          *pgxpool.Pool
	org, metadata uuid.UUID
	input         CallerSendStampInput
	initial       ReviewerDispatchIntent
}

func newReviewerDispatchPGFixture(t *testing.T) reviewerDispatchPGFixture {
	t.Helper()
	return newReviewerDispatchPGFixtureWithMaxConns(t, 2)
}

func newReviewerDispatchPGFixtureWithMaxConns(t *testing.T, maxConns int32) reviewerDispatchPGFixture {
	t.Helper()
	f := reviewerDispatchPGFixture{pool: testpostgres.NewPool(t, context.Background(), "../../migrations", maxConns), metadata: uuid.New()}
	err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`); err != nil {
			return err
		}
		// This existing helper installs genuine SQL-scope-valid immutable owner
		// storage, not a full thread codec/controller or actual service send.
		f.org, f.input = callerSendStampPGOwner(t, ctx, tx, CallerSendStampCodeReviewDispatch)
		integration, repository, pr, policy := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		for _, row := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, f.org}},
			{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0)&9223372036854775807),'synthetic/dispatch','https://example.invalid/dispatch.git',1)`, []any{repository, f.org, integration}},
			{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,1,'https://example.invalid/pr/1','synthetic/dispatch','Synthetic dispatch','head','base')`, []any{pr, f.org}},
			{`INSERT INTO code_review_policies(id,org_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,1,'approve_acceptable','{}','{}','{}','','','{}')`, []any{policy, f.org}},
			{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($1,$2,$3,$4,$5,$6,'base','head','slash_command','queued',$1::uuid::text)`, []any{f.metadata, f.org, f.input.Scope.SessionID, repository, pr, policy}},
		} {
			if _, err := tx.Exec(ctx, row.query, row.args...); err != nil {
				return err
			}
		}
		var found bool
		var err error
		f.initial, found, err = ReadReviewerDispatchIntentInTx(ctx, f.org, tx, f.metadata, f.input.Scope.SessionID, f.input.Scope.OwnerID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("synthetic supported reviewer intent missing")
		}
		return nil
	})
	require.NoError(t, err, "commit the isolated real-schema owner, exact metadata identity, and immutable typed storage fixture")
	return f
}

func reviewerDispatchPGExec(t *testing.T, f reviewerDispatchPGFixture, query string, args ...any) {
	t.Helper()
	require.NoError(t, RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, query, args...); return err }), "fixture history must obey all real guards within the existing configured transaction owner")
}
func reviewerDispatchPGRead(t *testing.T, f reviewerDispatchPGFixture) ReviewerDispatchIntent {
	t.Helper()
	var i ReviewerDispatchIntent
	var found bool
	err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		i, found, err = ReadReviewerDispatchIntentInTx(ctx, f.org, tx, f.metadata, f.input.Scope.SessionID, f.input.Scope.OwnerID)
		return err
	})
	require.NoError(t, err, "read only the exact retained reviewer generation")
	require.True(t, found, "the supported isolated reviewer row must remain retained")
	return i
}
func reviewerDispatchPGClaim(t *testing.T, f reviewerDispatchPGFixture, e ReviewerDispatchExpectation) ReviewerDispatchIntent {
	t.Helper()
	attempt := uuid.New()
	var i ReviewerDispatchIntent
	var changed bool
	err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		i, changed = ReviewerDispatchIntent{}, false
		var err error
		i, changed, err = ClaimReviewerDispatchInTx(ctx, f.org, tx, e, attempt)
		return err
	})
	require.NoError(t, err, "settle one real bounded claim transaction before exposing the provisional claimant")
	require.True(t, changed, "the exact due pending row should produce one committed attempt")
	require.Equal(t, ReviewerDispatchAttempting, i.State(), "a successful claim must not be confused with pending cancellation")
	require.Equal(t, attempt, i.Expectation().AttemptID(), "the actual claim retains exactly the caller's fresh original attempt identity")
	require.Equal(t, i.UpdatedAt(), i.AttemptedAt(), "claim attempted_at and update clock belong to the same actual statement")
	require.Equal(t, i.UpdatedAt().Add(60*time.Second), i.DueAt(), "a real claim must schedule exactly sixty seconds without changing production budgets")
	return i
}
func reviewerDispatchPGSettle(t *testing.T, f reviewerDispatchPGFixture, e ReviewerDispatchExpectation, s ReviewerDispatchSettlement) ReviewerDispatchIntent {
	t.Helper()
	var i ReviewerDispatchIntent
	var changed bool
	err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		i, changed = ReviewerDispatchIntent{}, false
		var err error
		i, changed, err = SettleReviewerDispatchInTx(ctx, f.org, tx, e, s)
		return err
	})
	if err != nil {
		var cause *pgconn.PgError
		if errors.As(err, &cause) {
			t.Logf("synthetic reviewer settlement SQLSTATE=%s constraint=%s", cause.Code, cause.ConstraintName)
		}
	}
	require.NoError(t, err, "the actual owner settlement must satisfy the real finite transition/history guards")
	require.True(t, changed, "the matching current generation should settle exactly once")
	return i
}
func reviewerDispatchPGSnapshot(t *testing.T, f reviewerDispatchPGFixture, immutable bool) json.RawMessage {
	t.Helper()
	query := `SELECT to_jsonb(r) FROM code_review_agent_results r WHERE org_id=$1 AND session_id=$2 AND id=$3`
	if immutable {
		query = `SELECT to_jsonb(r)-ARRAY['dispatch_state','dispatch_attempt_id','dispatch_attempt_seq','dispatch_attempted_at','dispatch_due_at','dispatch_observe_deadline','dispatch_attention_at','dispatch_observed_at','dispatch_message_id','dispatch_resolution','dispatch_detail','dispatch_writer_seq','dispatch_updated_at'] FROM code_review_agent_results r WHERE org_id=$1 AND session_id=$2 AND id=$3`
	}
	var raw json.RawMessage
	require.NoError(t, f.pool.QueryRow(context.Background(), query, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID).Scan(&raw), "retain every original business/immutable column rather than only graph cardinality")
	return raw
}
func requireReviewerDispatchAttemptRetained(t *testing.T, before, after ReviewerDispatchIntent) {
	t.Helper()
	require.Equal(t, before.Expectation().AttemptID(), after.Expectation().AttemptID(), "settlement cannot allocate another original send attempt")
	require.Equal(t, before.Expectation().AttemptSeq(), after.Expectation().AttemptSeq(), "settlement must preserve the original attempt sequence")
	require.Equal(t, before.AttemptedAt(), after.AttemptedAt(), "settlement must preserve the original attempt timestamp")
	require.Equal(t, before.Expectation().WriterSeq()+1, after.Expectation().WriterSeq(), "one state change advances exactly the expected writer generation")
}

// Historical fixtures use actual legal SQL transitions with backdated clocks
// solely to reach due gates without sleeps. They are not an execution of the
// claim/send/controller protocol or a fake clock installed in production code.
func reviewerDispatchPGUnknownHistory(t *testing.T, f reviewerDispatchPGFixture, priorSeconds int, futureObserved, expired bool) ReviewerDispatchIntent {
	t.Helper()
	attempt := uuid.New()
	age := 10
	if expired {
		age = 30
	}
	reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET dispatch_state='attempting',dispatch_attempt_id=$4,dispatch_attempt_seq=1,dispatch_attempted_at=statement_timestamp()-$5*interval '1 minute',dispatch_due_at=statement_timestamp()-interval '10 minutes',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID, attempt, age)
	var observed any
	if priorSeconds >= 0 {
		observed = priorSeconds
	}
	if futureObserved {
		observed = -2
	}
	reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET dispatch_state='unknown',dispatch_detail='unknown_ack',dispatch_due_at=CASE WHEN $4::integer IS NULL OR $4=-2 THEN statement_timestamp()-interval '1 second' ELSE statement_timestamp()-interval '2 minutes'+$4*interval '1 second' END,dispatch_observed_at=CASE WHEN $4::integer IS NULL THEN NULL WHEN $4=-2 THEN statement_timestamp()+interval '1 hour' ELSE statement_timestamp()-interval '2 minutes' END,dispatch_observe_deadline=dispatch_attempted_at+interval '15 minutes',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID, observed)
	return reviewerDispatchPGRead(t, f)
}

func TestReviewerDispatchSchedulesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                    string
		prior                                   int
		delay                                   time.Duration
		expired, future, extraWriter, attention bool
	}{
		{name: "first negative", prior: -1, delay: 5 * time.Second},
		{name: "five to ten", prior: 5, delay: 10 * time.Second},
		{name: "ten to twenty", prior: 10, delay: 20 * time.Second},
		{name: "twenty to forty", prior: 20, delay: 40 * time.Second},
		{name: "forty capped sixty", prior: 40, delay: 60 * time.Second},
		{name: "sixty remains capped", prior: 60, delay: 60 * time.Second},
		{name: "anomalous low interval clamp", prior: 1, delay: 5 * time.Second},
		{name: "status writer does not count as observation", prior: 5, delay: 10 * time.Second, extraWriter: true},
		{name: "deadline enters attention", prior: 5, delay: time.Hour, expired: true},
		{name: "attention reobservation retains history", prior: 5, delay: time.Hour, expired: true, attention: true},
		{name: "future historical observed timestamp stays monotone", prior: 5, delay: 5 * time.Second, future: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewerDispatchPGFixture(t)
			before := reviewerDispatchPGUnknownHistory(t, f, tt.prior, tt.future, tt.expired)
			if tt.attention {
				reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET dispatch_state='attention',dispatch_attention_at=statement_timestamp()-interval '1 minute',dispatch_due_at=statement_timestamp(),dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID)
				before = reviewerDispatchPGRead(t, f)
			}
			if tt.extraWriter {
				reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET status='timed_out',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID)
				before = reviewerDispatchPGRead(t, f)
			}
			immutable := reviewerDispatchPGSnapshot(t, f, true)
			after := reviewerDispatchPGSettle(t, f, before.Expectation(), ReviewerDispatchNegativeObservation())
			requireReviewerDispatchAttemptRetained(t, before, after)
			require.Equal(t, after.UpdatedAt().Add(tt.delay), after.DueAt(), "negative observation uses the exact persisted-interval schedule, not writer count or a new timeout")
			require.Equal(t, before.ObserveDeadline(), after.ObserveDeadline(), "the original observe deadline cannot be extended by another observation")
			require.False(t, after.ObservedAt().Before(before.ObservedAt()), "observation history must never regress even for a legal future retained timestamp")
			if tt.future {
				require.Equal(t, before.ObservedAt(), after.ObservedAt(), "a clock-regression fixture must retain its exact later historical observation instant")
			}
			if tt.expired {
				require.Equal(t, ReviewerDispatchAttention, after.State(), "an expired unknown is observe-only attention, never pending or resend")
				if tt.attention {
					require.Equal(t, before.AttentionAt(), after.AttentionAt(), "attention re-observation retains the exact original attention instant")
				} else {
					require.Equal(t, after.UpdatedAt(), after.AttentionAt(), "the first attention transition owns exactly one database attention instant")
				}
			} else {
				require.Equal(t, ReviewerDispatchUnknown, after.State(), "negative evidence before the fixed deadline cannot become acceptance or retry-pending")
			}
			require.JSONEq(t, string(immutable), string(reviewerDispatchPGSnapshot(t, f, true)), "observation scheduling must preserve every business, provider, scope, command and wake column")
		})
	}
}

func TestReviewerDispatchClaimAndKnownOutcomesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		operation string
		abort     ReviewerDispatchAbortCode
		cycles    int
		delay     time.Duration
	}{
		{name: "known contention abort", operation: "retry", abort: ReviewerDispatchAbortContention, delay: 5 * time.Second},
		{name: "known normalizing abort", operation: "retry", abort: ReviewerDispatchAbortNormalizing, delay: 5 * time.Second},
		{name: "known mode change abort", operation: "retry", abort: ReviewerDispatchAbortModeChanged, delay: 5 * time.Second},
		{name: "known replay-observation abort", operation: "retry", abort: ReviewerDispatchAbortReplayObservation, delay: 5 * time.Second},
		{name: "known abort capped five minutes", operation: "retry", abort: ReviewerDispatchAbortContention, cycles: 7, delay: 5 * time.Minute},
		{name: "known prewrite refusal", operation: "refuse"},
		{name: "unknown acknowledgement remains observe only", operation: "unknown", delay: 5 * time.Second},
		{name: "stale attempted claimant becomes unknown", operation: "stale", delay: 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewerDispatchPGFixture(t)
			// Fast-forward only guard-valid historical known-abort cycles. The one
			// helper transition below is the actual current-clock policy under test.
			for range tt.cycles {
				attempt := uuid.New()
				reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET dispatch_state='attempting',dispatch_attempt_id=$4,dispatch_attempt_seq=dispatch_attempt_seq+1,dispatch_attempted_at=statement_timestamp(),dispatch_due_at=statement_timestamp(),dispatch_detail=NULL,dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID, attempt)
				reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET dispatch_state='pending',dispatch_detail='contention',dispatch_due_at=statement_timestamp()-interval '1 second',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID)
			}
			pending := reviewerDispatchPGRead(t, f)
			var claim ReviewerDispatchIntent
			if tt.operation == "stale" {
				attempt := uuid.New()
				reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET dispatch_state='attempting',dispatch_attempt_id=$4,dispatch_attempt_seq=1,dispatch_attempted_at=statement_timestamp()-interval '2 minutes',dispatch_due_at=statement_timestamp()-interval '1 minute',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID, attempt)
				claim = reviewerDispatchPGRead(t, f)
			} else {
				claim = reviewerDispatchPGClaim(t, f, pending.Expectation())
			}
			var settlement ReviewerDispatchSettlement
			var err error
			switch tt.operation {
			case "retry":
				settlement, err = ReviewerDispatchRetryAfterKnownAbort(tt.abort)
			case "refuse":
				settlement, err = ReviewerDispatchKnownRefusal(ReviewerDispatchSendRefused)
			case "unknown":
				settlement = ReviewerDispatchUnknownAcknowledgement()
			case "stale":
				settlement = ReviewerDispatchStaleAttempt()
			}
			require.NoError(t, err, "only a closed positively classified outcome may construct the supplied-tx settlement")
			immutable := reviewerDispatchPGSnapshot(t, f, true)
			after := reviewerDispatchPGSettle(t, f, claim.Expectation(), settlement)
			requireReviewerDispatchAttemptRetained(t, claim, after)
			switch tt.operation {
			case "retry":
				require.Equal(t, ReviewerDispatchPending, after.State(), "only a positively known no-commit outcome may become retry-pending")
				require.Equal(t, string(tt.abort), after.Detail(), "known retry detail remains one of the four approved finite codes")
			case "refuse":
				require.Equal(t, ReviewerDispatchRefused, after.State(), "known prewrite refusal must settle without creating another attempt")
				require.Equal(t, "send_refusal", after.Resolution(), "refusal resolution must not imply uncertainty was aborted")
				require.True(t, after.DueAt().IsZero(), "resolved refusal has no due wake")
			default:
				require.Equal(t, ReviewerDispatchUnknown, after.State(), "unknown ACK and stale claims are observe-only, never retry-pending")
				require.Equal(t, claim.AttemptedAt().Add(15*time.Minute), after.ObserveDeadline(), "uncertainty deadline is anchored to the original attempt, not current time")
			}
			if tt.delay != 0 {
				require.Equal(t, after.UpdatedAt().Add(tt.delay), after.DueAt(), "actual helper update must obey the exact retry/unknown cap")
			}
			require.JSONEq(t, string(immutable), string(reviewerDispatchPGSnapshot(t, f, true)), "settlement cannot change business contribution, provider, frozen command, scope or wake")
		})
	}
}

func TestReviewerDispatchClaimAttemptHistoryPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		future bool
	}{
		{name: "ordinary prior attempt"},
		{name: "future retained attempt after backward clock", future: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewerDispatchPGFixture(t)
			priorAttempt := uuid.New()
			// Legal historical transitions model a backward wall-clock step;
			// neither the server clock nor any guard or timeout is changed.
			reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET dispatch_state='attempting',dispatch_attempt_id=$4,dispatch_attempt_seq=1,dispatch_attempted_at=statement_timestamp()+CASE WHEN $5 THEN interval '1 hour' ELSE interval '0 seconds' END,dispatch_due_at=statement_timestamp(),dispatch_detail=NULL,dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID, priorAttempt, tt.future)
			reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET dispatch_state='pending',dispatch_detail='contention',dispatch_due_at=statement_timestamp()-interval '1 second',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID)
			before := reviewerDispatchPGRead(t, f)
			immutable := reviewerDispatchPGSnapshot(t, f, true)
			newAttempt := uuid.New()
			var after ReviewerDispatchIntent
			var changed bool
			err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
				after, changed = ReviewerDispatchIntent{}, false
				var err error
				after, changed, err = ClaimReviewerDispatchInTx(ctx, f.org, tx, before.Expectation(), newAttempt)
				return err
			})
			if err != nil {
				var cause *pgconn.PgError
				if errors.As(err, &cause) {
					t.Logf("synthetic reviewer claim SQLSTATE=%s constraint=%s", cause.Code, cause.ConstraintName)
				}
			}
			require.NoError(t, err, "a due known-abort retry must claim through real guards without regressing its retained attempt clock")
			require.True(t, changed, "the exact due pending generation must produce a committed distinct attempt")
			expected := before
			expected.expectation.state = ReviewerDispatchAttempting
			expected.expectation.attemptID = newAttempt
			expected.expectation.attemptSeq++
			expected.expectation.writerSeq++
			expected.updatedAt = after.UpdatedAt()
			expected.attemptedAt = after.UpdatedAt()
			if tt.future {
				expected.attemptedAt = before.AttemptedAt()
			}
			expected.dueAt = after.UpdatedAt().Add(60 * time.Second)
			expected.due = false
			expected.detail = ""
			require.Equal(t, expected, after, "the entire claimed projection must retain monotone history, exact new identity and unchanged current-clock due policy")
			require.JSONEq(t, string(immutable), string(reviewerDispatchPGSnapshot(t, f, true)), "a backward clock cannot rewrite any original scope, provider, business field, input or wake")
		})
	}
}

func TestReviewerDispatchPendingCancellationOrderPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, prior string
		reason      ReviewerDispatchCancelReason
		changed     bool
	}{
		{name: "cancellation commits before claim", reason: ReviewerDispatchOwnerCancelled, changed: true},
		{name: "claim commits before cancellation", prior: "attempting", reason: ReviewerDispatchOwnerCancelled},
		{name: "known aborted pending can be cancelled", prior: "retry", reason: ReviewerDispatchOwnerCancelled, changed: true},
		{name: "unknown cannot be cancelled into abort", prior: "unknown", reason: ReviewerDispatchOwnerCancelled},
		{name: "authorized deadline cancels pending only", reason: ReviewerDispatchDeadlineBeforeAttempt, changed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewerDispatchPGFixture(t)
			before := f.initial
			if tt.prior != "" {
				before = reviewerDispatchPGClaim(t, f, before.Expectation())
				if tt.prior == "retry" {
					s, err := ReviewerDispatchRetryAfterKnownAbort(ReviewerDispatchAbortContention)
					require.NoError(t, err, "construct the known-abort fixture classification")
					before = reviewerDispatchPGSettle(t, f, before.Expectation(), s)
				} else if tt.prior == "unknown" {
					before = reviewerDispatchPGSettle(t, f, before.Expectation(), ReviewerDispatchUnknownAcknowledgement())
				}
			}
			wholeBefore, immutable := reviewerDispatchPGSnapshot(t, f, false), reviewerDispatchPGSnapshot(t, f, true)
			var changed bool
			var after ReviewerDispatchIntent
			err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
				// This models authorized metadata cancellation first in the SAME
				// outer unit. A deadline reason is caller-owned policy, not a grant
				// of authority inherent in the supplied-tx cancellation primitive.
				if tt.reason == ReviewerDispatchOwnerCancelled {
					if _, err := tx.Exec(ctx, `UPDATE code_review_session_metadata SET status='cancelled' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.metadata); err != nil {
						return err
					}
				}
				after, changed = ReviewerDispatchIntent{}, false
				var err error
				after, changed, err = CancelPendingReviewerDispatchInTx(ctx, f.org, tx, before.Expectation(), tt.reason)
				return err
			})
			require.NoError(t, err, "metadata-first cancellation must compose within the actual whole owner transaction")
			require.Equal(t, tt.changed, changed, "only pending can be settled as positive known-before-send cancellation")
			if changed {
				require.Equal(t, ReviewerDispatchCancelled, after.State(), "pending cancellation must become a resolved non-send state")
				require.Equal(t, string(tt.reason), after.Resolution(), "retain the exact finite authorized cancellation reason")
				require.True(t, after.DueAt().IsZero(), "resolved pending cancellation cannot remain due")
				require.Equal(t, before.Expectation().AttemptID(), after.Expectation().AttemptID(), "retry-pending cancellation preserves its original historical attempt")
				require.Equal(t, before.AttemptedAt(), after.AttemptedAt(), "cancellation cannot erase known-abort history")
			} else {
				require.Equal(t, ReviewerDispatchIntent{}, after, "attempted/unknown cancellation returns no counterfeit settlement")
				require.JSONEq(t, string(wholeBefore), string(reviewerDispatchPGSnapshot(t, f, false)), "claim-first and unknown cancellation leave the entire result/intent row byte-identical")
			}
			require.JSONEq(t, string(immutable), string(reviewerDispatchPGSnapshot(t, f, true)), "pending cancellation never changes contribution status/output, provider identity, frozen command or wake")
			if tt.prior == "" {
				var claimed bool
				err = RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
					_, claimed, err = ClaimReviewerDispatchInTx(ctx, f.org, tx, before.Expectation(), uuid.New())
					return err
				})
				require.NoError(t, err, "a claimant after committed pending cancellation must safely miss")
				require.False(t, claimed, "cancel-first must never mint an attempted generation or send permission")
			}
		})
	}
}

func reviewerDispatchPGInstallLink(t *testing.T, f reviewerDispatchPGFixture) CallerSendStamp {
	t.Helper()
	input := f.input
	var stamp CallerSendStamp
	err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		callerSendStampPGLink(t, ctx, tx, f.org, &input)
		var err error
		stamp, err = RecordCallerSendStampInTx(ctx, f.org, tx, input)
		return err
	})
	require.NoError(t, err, "storage-only positive fixture must create actual typed message/inbox and stamp in one real unit, not stamp historical input")
	return stamp
}

func reviewerDispatchPGObserve(t *testing.T, f reviewerDispatchPGFixture) (ReviewerDispatchExpectation, CallerSendLinkage) {
	t.Helper()
	var retained RetainedCallerSend
	var found, visible bool
	var link CallerSendLinkage
	err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET TRANSACTION READ ONLY`); err != nil {
			return err
		}
		mode, err := ObserveThreadSendModeInTx(ctx, tx, f.org, f.input.Scope.SessionID)
		if err != nil {
			return err
		}
		if mode != ThreadSendModeLegacy {
			return errors.New("synthetic observation fixture mode not legacy")
		}
		retained, found, err = ReadRetainedCallerSendInTx(ctx, f.org, tx, CallerSendOwnerRef{Purpose: CallerSendStampCodeReviewDispatch, OwnerID: f.input.Scope.OwnerID, SessionID: f.input.Scope.SessionID})
		if err != nil {
			return err
		}
		if !found {
			return errors.New("synthetic retained observation owner absent")
		}
		visible, err = ReadCallerSendVisibilityInTx(ctx, f.org, tx, f.input.Scope.SessionID, f.input.Scope.ThreadID)
		if err != nil {
			return err
		}
		if !visible {
			return errors.New("synthetic original observation scope unavailable")
		}
		link, found, err = ReadCallerSendLinkageInTx(ctx, f.org, tx, f.input.Scope)
		return err
	})
	require.NoError(t, err, "positive readonly evidence must settle separately before writable original-generation CAS")
	require.True(t, found, "the actual original typed stamp linkage must be present")
	require.True(t, visible, "the readonly binding establishes its own visibility before later archival")
	e, err := ReviewerDispatchExpectationFromRetained(f.metadata, retained)
	require.NoError(t, err, "derive the original complete typed binding, never a bare message id")
	return e, link
}

func TestReviewerDispatchLateAcceptancePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                          string
		observed, future, staleWriter bool
	}{
		{name: "known ACK after archival and terminal contribution"},
		{name: "observed acceptance after archival and terminal contribution", observed: true},
		{name: "future observed history is preserved on positive settlement", observed: true, future: true},
		{name: "stale observer misses after an aware status writer", observed: true, staleWriter: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewerDispatchPGFixture(t)
			var before ReviewerDispatchIntent
			if tt.future {
				before = reviewerDispatchPGUnknownHistory(t, f, 5, true, false)
			} else {
				before = reviewerDispatchPGClaim(t, f, f.initial.Expectation())
				if tt.observed {
					before = reviewerDispatchPGSettle(t, f, before.Expectation(), ReviewerDispatchUnknownAcknowledgement())
				}
			}
			stamp := reviewerDispatchPGInstallLink(t, f)
			var settlement ReviewerDispatchSettlement
			var err error
			if tt.observed {
				// The DB-only fixture exercises the same sealed retained projection
				// and actual typed linkage inside a KNOWN completed readonly unit.
				// It is not a full thread codec/ObserveCallerSend integration proof.
				e, link := reviewerDispatchPGObserve(t, f)
				require.Equal(t, before.Expectation(), e, "readonly owner must match the expected original state, attempt, digest and writer generation")
				settlement, err = ReviewerDispatchObservedAccepted(e, link.MessageID(), link.CreatedAt(), false)
			} else {
				settlement, err = ReviewerDispatchSendAcknowledged(stamp.MessageID)
			}
			require.NoError(t, err, "construct only the closed original positive settlement")
			// An aware terminal contribution writer precedes archival. Receipt
			// settlement must not revive this timed-out result or overwrite output.
			reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET status='timed_out',raw_output='Synthetic closed contribution',structured_result='{"reason":"dispatch_unresolved"}'::jsonb,dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID)
			current := reviewerDispatchPGRead(t, f)
			if tt.staleWriter {
				snapshot := reviewerDispatchPGSnapshot(t, f, false)
				var changed bool
				err = RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
					_, changed, err = SettleReviewerDispatchInTx(ctx, f.org, tx, before.Expectation(), settlement)
					return err
				})
				require.NoError(t, err, "an earlier positive candidate cannot override a newer writer")
				require.False(t, changed, "stale writer generation must produce a zero-row CAS")
				require.JSONEq(t, string(snapshot), string(reviewerDispatchPGSnapshot(t, f, false)), "a stale observer must preserve every field of the newer terminal owner")
				return
			}
			reviewerDispatchPGExec(t, f, `UPDATE code_review_session_metadata SET status='completed',completed_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.metadata)
			// Refresh the independent current generation after the intentional aware
			// status writer; a real controller must obtain a new settled observation.
			if tt.observed {
				e, link := reviewerDispatchPGObserve(t, f)
				require.Equal(t, current.Expectation(), e, "a fresh successful readonly unit must bind the intentional aware writer's new generation before archival")
				settlement, err = ReviewerDispatchObservedAccepted(e, link.MessageID(), link.CreatedAt(), false)
				require.NoError(t, err, "a refreshed full binding is required after an intentional writer increment")
			}
			reviewerDispatchPGExec(t, f, `UPDATE sessions SET archived_at=statement_timestamp() WHERE org_id=$1 AND id=$2`, f.org, f.input.Scope.SessionID)
			reviewerDispatchPGExec(t, f, `UPDATE session_threads SET archived_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.ThreadID)
			var metadataBefore json.RawMessage
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT to_jsonb(m) FROM code_review_session_metadata m WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.metadata).Scan(&metadataBefore), "retain every field of the already-terminal metadata owner before late settlement")
			immutable := reviewerDispatchPGSnapshot(t, f, true)
			after := reviewerDispatchPGSettle(t, f, current.Expectation(), settlement)
			require.Equal(t, ReviewerDispatchAccepted, after.State(), "an original known ACK or prior positive readonly binding remains recordable after archival/terminality")
			require.Equal(t, stamp.MessageID, after.MessageID(), "settlement names the actual original immutable typed message")
			require.True(t, after.DueAt().IsZero(), "positive receipt settlement resolves only its original due intent")
			requireReviewerDispatchAttemptRetained(t, current, after)
			require.Equal(t, current.ObserveDeadline(), after.ObserveDeadline(), "late acceptance cannot extend or clear retained observation history")
			require.Equal(t, current.AttentionAt(), after.AttentionAt(), "late acceptance cannot erase the original attention instant")
			if tt.future {
				require.Equal(t, current.ObservedAt(), after.ObservedAt(), "positive acceptance must retain a later legal historical observation instant rather than regress to the wall clock")
			}
			require.JSONEq(t, string(immutable), string(reviewerDispatchPGSnapshot(t, f, true)), "late acceptance changes dispatch receipt state only; the full terminal business result, provider, original input and wake remain byte-identical")
			var metadataAfter json.RawMessage
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT to_jsonb(m) FROM code_review_session_metadata m WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.metadata).Scan(&metadataAfter), "read back the exact terminal metadata owner after late receipt settlement")
			require.JSONEq(t, string(metadataBefore), string(metadataAfter), "late acceptance cannot revive or rewrite a completed review owner or its publication/contribution metadata")
		})
	}
}

func TestReviewerDispatchIneligiblePendingPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, scope string }{{name: "terminal metadata owner", scope: "terminal"}, {name: "stale queued metadata owner", scope: "stale"}, {name: "timed-out reviewer before claim", scope: "result"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewerDispatchPGFixture(t)
			switch tt.scope {
			case "terminal":
				reviewerDispatchPGExec(t, f, `UPDATE code_review_session_metadata SET status='completed',completed_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.metadata)
			case "stale":
				reviewerDispatchPGExec(t, f, `UPDATE code_review_session_metadata SET stale=true WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.metadata)
			case "result":
				reviewerDispatchPGExec(t, f, `UPDATE code_review_agent_results SET status='timed_out',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.input.Scope.SessionID, f.input.Scope.OwnerID)
			}
			before := reviewerDispatchPGRead(t, f)
			immutable := reviewerDispatchPGSnapshot(t, f, true)
			var after ReviewerDispatchIntent
			var changed bool
			err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				after, changed, err = ClaimReviewerDispatchInTx(ctx, f.org, tx, before.Expectation(), uuid.New())
				return err
			})
			require.NoError(t, err, "fresh metadata/result ineligibility must settle only pending, under the real ordered owner locks")
			require.True(t, changed, "a positively ineligible pending owner should be cancelled rather than left executable")
			require.Equal(t, ReviewerDispatchCancelled, after.State(), "ineligible metadata or terminal contribution cannot produce a send claimant")
			require.Equal(t, "owner_ineligible", after.Resolution(), "the pending-only cancellation must name its finite positive ineligibility reason")
			require.Equal(t, uuid.Nil, after.Expectation().AttemptID(), "no attempt identity may be installed after eligibility is lost")
			require.Equal(t, int32(0), after.Expectation().AttemptSeq(), "metadata terminality is not an attempted-send history")
			require.True(t, after.DueAt().IsZero(), "ineligible pending cancellation cannot retain an executable due wake")
			require.JSONEq(t, string(immutable), string(reviewerDispatchPGSnapshot(t, f, true)), "eligibility cancellation preserves every business result and original frozen command field")
		})
	}
}

func TestReviewerDispatchForeignAndLegacyPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, scope string }{{name: "foreign org", scope: "org"}, {name: "foreign session", scope: "session"}, {name: "different metadata identity", scope: "metadata"}, {name: "different digest", scope: "digest"}, {name: "different lane", scope: "lane"}, {name: "ordinary legacy result", scope: "legacy"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewerDispatchPGFixture(t)
			e := f.initial.Expectation()
			org := f.org
			baseline := reviewerDispatchPGSnapshot(t, f, false)
			if tt.scope == "legacy" {
				var legacy models.CodeReviewAgentResult
				err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
					legacy = models.CodeReviewAgentResult{OrgID: f.org, SessionID: f.input.Scope.SessionID, AgentProvider: string(models.DefaultDefaultAgentType), Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusQueued}
					if err := NewCodeReviewStore(tx).CreateAgentResult(ctx, &legacy); err != nil {
						return err
					}
					raw := "Synthetic ordinary legacy result"
					_, err := NewCodeReviewStore(tx).UpdateAgentResultOutcome(ctx, f.org, legacy.ID, models.CodeReviewAgentResultStatusCompleted, &raw, json.RawMessage(`{"ordinary":true}`))
					return err
				})
				require.NoError(t, err, "old public result creation/outcome writes must retain their ordinary no-intent behavior")
				var found bool
				err = RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
					_, found, err = ReadReviewerDispatchIntentInTx(ctx, f.org, tx, f.metadata, f.input.Scope.SessionID, legacy.ID)
					return err
				})
				require.NoError(t, err, "ordinary legacy rows are safe misses, not candidates for implicit intent upgrade")
				require.False(t, found, "legacy no-intent result cannot acquire S1 claim authority")
				require.JSONEq(t, string(baseline), string(reviewerDispatchPGSnapshot(t, f, false)), "legacy companion writes cannot modify the separate retained S1 owner")
				return
			}
			switch tt.scope {
			case "org":
				org = uuid.New()
			case "session":
				e.sessionID = uuid.New()
			case "metadata":
				e.metadataID = uuid.New()
			case "digest":
				e.digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			case "lane":
				e.threadID = uuid.New()
			}
			var changed bool
			err := RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				_, changed, err = ClaimReviewerDispatchInTx(ctx, org, tx, e, uuid.New())
				return err
			})
			require.NoError(t, err, "independent foreign or mismatched expectation must safely miss without adopting another row")
			require.False(t, changed, "foreign/mismatched scope cannot manufacture a claimant")
			require.JSONEq(t, string(baseline), string(reviewerDispatchPGSnapshot(t, f, false)), "every foreign or stale comparison preserves the entire original row")
		})
	}
}

// Transparent statement gates announce the actual competing lock/UPDATE call;
// they never answer SQL, change its arguments, or substitute settlement. The
// announcement alone is not lock-wait proof: the race separately requires the
// catalog observer to see this backend blocked by the actual holder before commit.
type reviewerDispatchPGLockSignalTx struct {
	pgx.Tx
	started        chan struct{}
	metadataUpdate bool
	announcement   *sync.Once
}

func (tx *reviewerDispatchPGLockSignalTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if !tx.metadataUpdate && query == reviewerDispatchMetadataLockQuery {
		tx.announcement.Do(func() { close(tx.started) })
	}
	return tx.Tx.QueryRow(ctx, query, args...)
}
func (tx *reviewerDispatchPGLockSignalTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if tx.metadataUpdate && query == reviewerDispatchPGCancelMetadataQuery {
		tx.announcement.Do(func() { close(tx.started) })
	}
	return tx.Tx.Exec(ctx, query, args...)
}

const reviewerDispatchPGCancelMetadataQuery = `UPDATE code_review_session_metadata SET status='cancelled' WHERE org_id=$1 AND session_id=$2 AND id=$3`

func TestReviewerDispatchCancellationLockRacePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		claimFirst bool
	}{{name: "cancel holds owner before competing claim"}, {name: "claim holds owner before competing cancellation", claimFirst: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Only this race needs holder, competitor, and catalog observer at
			// once. All ordinary reviewer fixtures retain their two-connection cap.
			f := newReviewerDispatchPGFixtureWithMaxConns(t, 3)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			e := f.initial.Expectation()
			holder, err := f.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			require.NoError(t, err, "reserve the first real bounded owner transaction")
			t.Cleanup(func() {
				err := holder.Rollback(context.Background())
				if !errors.Is(err, pgx.ErrTxClosed) {
					require.NoError(t, err, "release only this race's first real transaction")
				}
			})
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, holder), "the held race transaction must keep the actual existing timeout/isolation policy")
			var holderPID int32
			require.NoError(t, holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID), "identify the actual holder backend without releasing its transaction")
			observer, err := f.pool.Acquire(ctx)
			require.NoError(t, err, "reserve the sole third-party catalog observer before the competitor starts its lock budget")
			t.Cleanup(observer.Release)
			var observerPID int32
			require.NoError(t, observer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&observerPID), "warm the independent catalog checkout before the competing lock starts")
			require.NotEqual(t, holderPID, observerPID, "the catalog observer must not borrow or query through the held owner transaction")
			var winner ReviewerDispatchIntent
			var changed bool
			if tt.claimFirst {
				winner, changed, err = ClaimReviewerDispatchInTx(ctx, f.org, holder, e, uuid.New())
			} else {
				_, err = holder.Exec(ctx, reviewerDispatchPGCancelMetadataQuery, f.org, f.input.Scope.SessionID, f.metadata)
				require.NoError(t, err, "cancellation must lock/change metadata first in its actual whole unit")
				winner, changed, err = CancelPendingReviewerDispatchInTx(ctx, f.org, holder, e, ReviewerDispatchOwnerCancelled)
			}
			require.NoError(t, err, "the first owner transaction must stage a real guarded winner before the competitor")
			require.True(t, changed, "the first staged operation must actually change the pending generation")
			started := make(chan struct{})
			var announcement sync.Once
			type competingBackend struct {
				pid int32
				ctx context.Context
			}
			backend := make(chan competingBackend, 1)
			var backendAnnouncement sync.Once
			type result struct {
				intent  ReviewerDispatchIntent
				changed bool
				err     error
			}
			done := make(chan result, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				var out result
				out.err = RunLifecycleTransaction(ctx, f.pool, func(unitCtx context.Context, tx pgx.Tx) error {
					out.intent, out.changed = ReviewerDispatchIntent{}, false
					var pid int32
					if err := tx.QueryRow(unitCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
						return err
					}
					backendAnnouncement.Do(func() { backend <- competingBackend{pid: pid, ctx: unitCtx} })
					observed := &reviewerDispatchPGLockSignalTx{Tx: tx, started: started, metadataUpdate: tt.claimFirst, announcement: &announcement}
					if tt.claimFirst {
						if _, err := observed.Exec(unitCtx, reviewerDispatchPGCancelMetadataQuery, f.org, f.input.Scope.SessionID, f.metadata); err != nil {
							return err
						}
						var err error
						out.intent, out.changed, err = CancelPendingReviewerDispatchInTx(unitCtx, f.org, observed, e, ReviewerDispatchOwnerCancelled)
						return err
					}
					var err error
					out.intent, out.changed, err = ClaimReviewerDispatchInTx(unitCtx, f.org, observed, e, uuid.New())
					return err
				})
				done <- out
			}()
			// A failed assertion must cancel the actual runner and join its
			// goroutine before observer/holder/pool cleanup can proceed. The
			// buffered outcome and separate join remain safe if done was read.
			t.Cleanup(func() {
				cancel()
				<-finished
			})
			var contender competingBackend
			select {
			case contender = <-backend:
			case out := <-done:
				require.FailNow(t, "the real competitor must identify its backend before any winner settles", "early competing outcome: %v", out.err)
			}
			require.NotEqual(t, holderPID, contender.pid, "the competing runner must own a backend separate from the held transaction")
			require.NotEqual(t, observerPID, contender.pid, "the real competing runner must use the remaining checkout, not the catalog observer")
			var early *result
			select {
			case <-started:
			case out := <-done:
				early = &out
			case <-contender.ctx.Done():
				require.FailNow(t, "the competing statement must begin inside its unchanged real lifecycle budget")
			}
			if early != nil {
				require.NoError(t, holder.Rollback(context.Background()), "release held owner if the competing statement could not start")
				require.FailNow(t, "competing owner statement must start before the held winner settles", "early competing outcome: %v", early.err)
			}
			var blocked bool
			for !blocked {
				select {
				case out := <-done:
					require.FailNow(t, "the real competing backend must be observed waiting before it finishes", "unobserved competing outcome: %v", out.err)
				case <-contender.ctx.Done():
					require.FailNow(t, "the exact competing lock wait must be visible within its unchanged real lifecycle budget")
				default:
				}
				// Each autocommit catalog statement is fresh. There is no sleep,
				// raised timeout, or replacement runner: only a positive exact-PID
				// Lock wait permits releasing the real first owner transaction.
				err := observer.QueryRow(contender.ctx, `SELECT EXISTS (
					SELECT 1 FROM pg_stat_activity a
					WHERE a.datname=current_database() AND a.pid=$1::integer
					AND a.state='active' AND a.wait_event_type='Lock'
					AND $2::integer=ANY(pg_blocking_pids(a.pid)))`, contender.pid, holderPID).Scan(&blocked)
				require.NoError(t, err, "observe the actual isolated-database competing backend's holder-specific lock wait")
			}
			require.True(t, blocked, "PostgreSQL must positively confirm the competitor is blocked by the exact actual holder PID")
			require.NoError(t, holder.Commit(ctx), "only settle the first winner after PostgreSQL confirms the actual competing holder-specific lock wait")
			out := <-done
			require.NoError(t, out.err, "the later owner operation must recheck committed source after metadata/result lock serialization")
			require.False(t, out.changed, "the losing original generation cannot claim after cancellation or cancel a committed attempted intent")
			require.Equal(t, ReviewerDispatchIntent{}, out.intent, "a race loser must return no replacement receipt or send claimant")
			current := reviewerDispatchPGRead(t, f)
			winner.metadataEligible = false // Both schedules deliberately commit metadata cancellation.
			require.Equal(t, winner, current, "the full original intent generation must match the first committed winner after both real owner units; only separately changed metadata eligibility differs")
			if tt.claimFirst {
				require.Equal(t, ReviewerDispatchAttempting, current.State(), "claim-first leaves its original attempt retained and observe-only after cancellation")
			} else {
				require.Equal(t, ReviewerDispatchCancelled, current.State(), "cancel-first must never permit another claim attempt")
				require.Equal(t, uuid.Nil, current.Expectation().AttemptID(), "cancel-first leaves no fabricated attempt identity")
			}
		})
	}
}
