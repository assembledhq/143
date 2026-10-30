package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// This transparent observer delegates work, retries and settlement to the
// actual supplied runner. Its one extra SELECT only observes the real policy;
// it never answers application SQL, installs settings or manufactures outcomes.
type fullAssessmentDispatchRunObserver struct {
	base             db.CodeReviewMetadataTransactions
	calls, callbacks int
	policy           [4]string
	hasDeadline      bool
	remaining        time.Duration
	clock            time.Time
}

func (r *fullAssessmentDispatchRunObserver) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	r.calls++
	return r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		r.callbacks++
		deadline, ok := unitCtx.Deadline()
		r.hasDeadline = ok
		r.remaining = time.Until(deadline)
		if err := tx.QueryRow(unitCtx, `SELECT current_setting('transaction_isolation'),
			current_setting('lifecycle143.writer_version',true),current_setting('lock_timeout'),
			current_setting('statement_timeout'),transaction_timestamp()`).Scan(&r.policy[0], &r.policy[1], &r.policy[2], &r.policy[3], &r.clock); err != nil {
			return err
		}
		return work(unitCtx, tx)
	})
}

func fullAssessmentDispatchRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, work func(context.Context, pgx.Tx) error) {
	t.Helper()
	require.NoError(t, db.RunLifecycleTransaction(ctx, pool, work), "fixture history must use the existing actual bounded owner and unmodified full-schema guards")
}

func fullAssessmentDispatchJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err, "encode only complete synthetic retained fixture input or expected rows")
	return raw
}

func fullAssessmentDispatchRead(ctx context.Context, tx pgx.Tx, f strandedReviewFixture, result uuid.UUID) (db.ReviewerDispatchIntent, error) {
	value, found, err := db.ReadReviewerDispatchIntentInTx(ctx, f.job.OrgID, tx, f.job.MetadataID, f.job.SessionID, result)
	if err != nil {
		return db.ReviewerDispatchIntent{}, err
	}
	if !found {
		return db.ReviewerDispatchIntent{}, errors.New("synthetic full-assessment dispatch owner missing")
	}
	return value, nil
}

func fullAssessmentDispatchChild(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, thread uuid.UUID, state string) uuid.UUID {
	t.Helper()
	result := uuid.New()
	key := "cr-dispatch:v1:" + result.String()
	input := db.CodeReviewDispatchPreparation{ResultID: result, SessionID: f.job.SessionID, MetadataID: f.job.MetadataID,
		ThreadID: thread, AgentProvider: "claude_code", Version: 1, Key: key, Digest: strings.Repeat("a", 64),
		ReviewOutputKey: f.job.OutputKey, RawOutput: new("Synthetic retained reviewer contribution"),
		StructuredResult: json.RawMessage(`{"synthetic":"retained"}`)}
	// Storage/scope fixtures are intentionally not semantic codec envelopes or
	// evidence of provider delivery. Original input and wake are never rerendered
	// by the actual completion/reconciliation calls below.
	input.Input = fullAssessmentDispatchJSON(t, map[string]any{"version": 1, "purpose": db.CallerSendStampCodeReviewDispatch,
		"org_id": f.job.OrgID, "session_id": f.job.SessionID, "thread_id": thread, "client_message_id": key, "message": "Synthetic original reviewer command"})
	input.Wake = fullAssessmentDispatchJSON(t, map[string]any{"job_type": "run_code_review", "queue": "agent",
		"dedupe_key": "code_review:" + f.job.OutputKey, "payload": map[string]any{"org_id": f.job.OrgID,
			"session_id": f.job.SessionID, "metadata_id": f.job.MetadataID, "review_output_key": f.job.OutputKey, "head_sha": f.job.HeadSHA}})
	var pending db.ReviewerDispatchIntent
	fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
		pending = db.ReviewerDispatchIntent{}
		if _, err := db.PrepareCodeReviewDispatchInTx(unitCtx, f.job.OrgID, tx, input); err != nil {
			return err
		}
		var err error
		pending, err = fullAssessmentDispatchRead(unitCtx, tx, f, result)
		return err
	})
	if state == "pending" {
		return result
	}
	var claim db.ReviewerDispatchIntent
	if state == "attention" {
		// These two legal historical transitions make the fixed original
		// observation deadline due without sleeps or changing a server clock.
		fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
			claim = db.ReviewerDispatchIntent{}
			if _, err := tx.Exec(unitCtx, `UPDATE code_review_agent_results SET dispatch_state='attempting',dispatch_attempt_id=$4,
				dispatch_attempt_seq=1,dispatch_attempted_at=statement_timestamp()-interval '20 minutes',
				dispatch_due_at=statement_timestamp()-interval '5 minutes',dispatch_writer_seq=dispatch_writer_seq+1,
				dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.job.OrgID, f.job.SessionID, result, uuid.New()); err != nil {
				return err
			}
			if _, err := tx.Exec(unitCtx, `UPDATE code_review_agent_results SET dispatch_state='unknown',dispatch_detail='unknown_ack',
				dispatch_observe_deadline=dispatch_attempted_at+interval '15 minutes',dispatch_due_at=statement_timestamp()-interval '1 minute',
				dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp()
				WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.job.OrgID, f.job.SessionID, result); err != nil {
				return err
			}
			var err error
			claim, err = fullAssessmentDispatchRead(unitCtx, tx, f, result)
			return err
		})
	} else {
		attempt := uuid.New()
		fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
			claim = db.ReviewerDispatchIntent{}
			var changed bool
			var err error
			claim, changed, err = db.ClaimReviewerDispatchInTx(unitCtx, f.job.OrgID, tx, pending.Expectation(), attempt)
			if err != nil {
				return err
			}
			if !changed {
				return errors.New("synthetic full-assessment dispatch claim did not change")
			}
			return nil
		})
	}
	if state == "attempting" {
		return result
	}
	var settlement db.ReviewerDispatchSettlement
	var err error
	switch state {
	case "retry pending":
		settlement, err = db.ReviewerDispatchRetryAfterKnownAbort(db.ReviewerDispatchAbortContention)
	case "unknown":
		settlement = db.ReviewerDispatchUnknownAcknowledgement()
	case "attention":
		settlement = db.ReviewerDispatchNegativeObservation()
	case "accepted":
		var stamp db.CallerSendStamp
		fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
			stamp = db.CallerSendStamp{}
			link := db.CallerSendStampInput{Scope: db.CallerSendStampScope{SessionID: f.job.SessionID, ThreadID: thread,
				ClientMessageID: key, Purpose: db.CallerSendStampCodeReviewDispatch, OwnerID: result}, Version: 1, Digest: input.Digest}
			if err := tx.QueryRow(unitCtx, `INSERT INTO session_messages(org_id,session_id,thread_id,turn_number,role,content)
				VALUES($1,$2,$3,1,'user','Synthetic retained accepted input') RETURNING id`, f.job.OrgID, f.job.SessionID, thread).Scan(&link.MessageID); err != nil {
				return err
			}
			if _, err := tx.Exec(unitCtx, `INSERT INTO thread_inbox_entries(org_id,session_id,thread_id,sequence_no,message_id,client_message_id,entry_type,payload)
				VALUES($1,$2,$3,1,$4,$5,'user_message',jsonb_build_object('message_id',$4::bigint))`, f.job.OrgID, f.job.SessionID, thread, link.MessageID, key); err != nil {
				return err
			}
			var err error
			stamp, err = db.RecordCallerSendStampInTx(unitCtx, f.job.OrgID, tx, link)
			return err
		})
		settlement, err = db.ReviewerDispatchSendAcknowledged(stamp.MessageID)
	default:
		t.Fatalf("unknown synthetic full-assessment dispatch state %q", state)
	}
	require.NoError(t, err, "construct only the finite fixture classification without invoking any provider")
	fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
		_, changed, err := db.SettleReviewerDispatchInTx(unitCtx, f.job.OrgID, tx, claim.Expectation(), settlement)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("synthetic full-assessment dispatch settlement did not change")
		}
		return nil
	})
	return result
}

type fullAssessmentDispatchGraph struct{ session, metadata, assessment, schedule, results, findings, prompts, jobs, threads, messages, inbox, stamps json.RawMessage }

func fullAssessmentDispatchSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture) fullAssessmentDispatchGraph {
	t.Helper()
	var g fullAssessmentDispatchGraph
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(s),to_jsonb(m),to_jsonb(a),to_jsonb(p),
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_agent_results r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_findings r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_prompt_records r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM jobs r WHERE r.org_id=$1),'[]'::jsonb),
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM session_threads r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM session_messages r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM thread_inbox_entries r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.owner_id) FROM caller_send_command_stamps r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb)
		FROM sessions s JOIN code_review_session_metadata m ON m.org_id=s.org_id AND m.session_id=s.id
		JOIN code_review_revision_assessments a ON a.org_id=m.org_id AND a.id=$3
		JOIN code_review_pr_state p ON p.org_id=a.org_id AND p.pull_request_id=a.pull_request_id
		WHERE s.org_id=$1 AND s.id=$2 AND m.id=$4`, f.job.OrgID, f.job.SessionID, f.assessment, f.job.MetadataID).
		Scan(&g.session, &g.metadata, &g.assessment, &g.schedule, &g.results, &g.findings, &g.prompts, &g.jobs, &g.threads, &g.messages, &g.inbox, &g.stamps),
		"snapshot the actual complete assessment, scheduler, coverage owner, metadata, evidence and immutable caller graph")
	return g
}

func fullAssessmentDispatchObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var row map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &row), "decode a complete synthetic database row without dropping durable fields")
	return row
}

func fullAssessmentDispatchFixture(t *testing.T, pool *pgxpool.Pool, route string, coverage bool) (strandedReviewFixture, map[string]uuid.UUID) {
	t.Helper()
	f := seedStrandedReview(t, pool, "failed")
	ctx := f.ctx()
	thread := uuid.New()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`, nil},
		{`INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,completed_at) VALUES($1,$2,$3,'claude_code','Synthetic drained caller lane','completed',statement_timestamp())`, []any{thread, f.job.OrgID, f.job.SessionID}},
		{`INSERT INTO code_review_findings(org_id,session_id,dedupe_key,severity,confidence,path,summary,body) VALUES($1,$2,'synthetic-dispatch-finding','info','high','synthetic.go','Synthetic retained finding','Synthetic retained evidence')`, []any{f.job.OrgID, f.job.SessionID}},
		{`INSERT INTO code_review_prompt_records(org_id,session_id,record_key,role,content) VALUES($1,$2,'synthetic-dispatch-prompt','reviewer','Synthetic retained prompt')`, []any{f.job.OrgID, f.job.SessionID}},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed only legal full-schema synthetic assessment, owner, lane and retained evidence prerequisites")
	}
	seedSavedReviewResults(t, pool, f)
	children := make(map[string]uuid.UUID)
	for _, state := range []string{"pending", "retry pending", "attempting", "unknown", "attention", "accepted"} {
		children[state] = fullAssessmentDispatchChild(t, ctx, pool, f, thread, state)
	}
	// Retain children before publication confirmation closes fresh dispatch.
	stageConfirmedDrainReview(t, pool, f)
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`UPDATE code_review_session_metadata SET status='running',phase='reviewing',failure_reason=NULL WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.job.MetadataID}},
		{`UPDATE code_review_revision_assessments SET coverage_complete=$3,structured_outcome=jsonb_build_object('coverage_complete',$3::boolean,'description_assessments','[]'::jsonb) WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.assessment, coverage}},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "retain the exact confirmed assessment and metadata state after its original reviewer history")
	}
	if route != "complete" {
		_, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed',phase=NULL,decision='blocked',acceptable=false,
			final_review_body='immutable saved review',failure_reason=NULL,completed_at=statement_timestamp()-interval '1 day'
			WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID)
		require.NoError(t, err, "retain legacy-completed metadata before the actual worker reconciliation path")
	}
	if route == "already completed" {
		// Model the pre-upgrade crash boundary using real business DB primitives
		// but deliberately no new private pending-settlement helper. This is
		// legal retained history, not proof the changed worker executed before.
		staged, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
		require.NoError(t, err, "retain the complete original immutable business tuple for historical completion")
		fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
			if err := db.NewCodeReviewRecheckStore(tx).LockFullAssessmentCompletion(unitCtx, tx, f.job.OrgID, f.job.PullRequestID); err != nil {
				return err
			}
			if err := db.LockCodeReviewMetadataForCompletionInTx(unitCtx, f.job.OrgID, tx, f.job.SessionID); err != nil {
				return err
			}
			store := db.NewCodeReviewAssessmentStore(tx)
			if err := store.LinkFullReviewEvidence(unitCtx, f.job.OrgID, f.assessment, f.job.SessionID); err != nil {
				return err
			}
			if coverage {
				claimed, err := db.NewCodeReviewRecheckStore(tx).ClaimFullAssessmentOwner(unitCtx, tx, f.job.OrgID, f.job.SessionID, f.job.PullRequestID)
				if err != nil {
					return err
				}
				if !claimed {
					return errors.New("synthetic historical full assessment owner not drained")
				}
			}
			if err := store.Complete(unitCtx, f.job.OrgID, f.assessment, staged.Generation, staged.InputDigest, models.CodeReviewAssessmentCompletion{
				ResultOrigin: *staged.ResultOrigin, CoverageComplete: staged.CoverageComplete, Decision: *staged.Decision, Acceptable: *staged.Acceptable,
				RiskReasonDetails: staged.RiskReasonDetails, StructuredOutcome: staged.StructuredOutcome, RenderedBody: *staged.RenderedBody}); err != nil {
				return err
			}
			return db.NewCodeReviewScheduleStore(tx).SettleAssessmentInTx(unitCtx, f.job.OrgID, tx, f.assessment)
		})
	}
	return f, children
}

func fullAssessmentDispatchInvoke(t *testing.T, pool *pgxpool.Pool, f strandedReviewFixture, route string) (*fullAssessmentDispatchRunObserver, error) {
	t.Helper()
	ctx := f.ctx()
	staged, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read the independently retained actual immutable assessment before invoking its worker owner")
	metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read exact completed provenance before actual reconciliation")
	reserved, err := pool.Acquire(ctx)
	require.NoError(t, err, "reserve one of two fixture checkouts so the actual worker has only one connection for its whole unit")
	t.Cleanup(reserved.Release)
	observer := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
	f.stores.ThreadSendDependencies.Transactions = observer
	if route == "complete" {
		err = completeFullAssessment(ctx, observer, staged, db.CompleteCodeReviewParams{SessionID: f.job.SessionID,
			Decision: *staged.Decision, Acceptable: *staged.Acceptable, GitHubReviewID: staged.GitHubReviewID,
			GitHubReviewURL: staged.GitHubReviewURL, FinalReviewBody: *staged.RenderedBody, RiskReasonDetails: []models.CodeReviewRiskReason{}},
			staged.StructuredOutcome, staged.CoverageComplete, *staged.RenderedBody)
	} else {
		err = reconcileCompletedFullAssessment(ctx, f.stores, metadata)
	}
	return observer, err
}

func fullAssessmentDispatchAssertRun(t *testing.T, observer *fullAssessmentDispatchRunObserver) {
	t.Helper()
	require.Equal(t, 1, observer.calls, "the actual worker owner must use its explicitly supplied existing Run capability exactly once")
	require.Equal(t, 1, observer.callbacks, "uncontended success or real nonretryable CHECK failure must not replay the whole owner callback")
	require.Equal(t, [4]string{"read committed", "1", "250ms", "1500ms"}, observer.policy, "the real supplied unit must retain its existing production isolation, compatibility marker and lock/statement budgets")
	require.True(t, observer.hasDeadline, "the actual callback must carry the real whole-unit deadline rather than a background or legacy Begin context")
	require.Greater(t, observer.remaining, time.Duration(0), "the observed real unit must still have positive original budget before application work")
	require.LessOrEqual(t, observer.remaining, db.LifecycleTransactionTimeout, "the observer cannot extend or replace the existing two-second owner budget")
	require.False(t, observer.clock.IsZero(), "the real supplied transaction must expose its actual database clock for exact completion assertions")
}

func fullAssessmentDispatchAssertMetadata(t *testing.T, before, after fullAssessmentDispatchGraph, f strandedReviewFixture, route string, observer *fullAssessmentDispatchRunObserver) {
	t.Helper()
	expected := fullAssessmentDispatchObject(t, before.metadata)
	actual := fullAssessmentDispatchObject(t, after.metadata)
	if route != "already completed" {
		expected["assessment_id"] = fullAssessmentDispatchJSON(t, f.assessment)
	}
	if route == "complete" {
		j := func(value any) json.RawMessage { return fullAssessmentDispatchJSON(t, value) }
		expected["status"], expected["phase"], expected["decision"], expected["acceptable"] = j("completed"), j(nil), j("blocked"), j(false)
		expected["failure_reason"], expected["status_code"], expected["status_message"], expected["retry_at"], expected["last_error_at"] = j(nil), j(nil), j(nil), j(nil), j(nil)
		expected["retryable_failure"], expected["final_review_body"], expected["risk_reason_details"] = j(false), j("immutable saved review"), j([]any{})
		var completed time.Time
		require.NoError(t, json.Unmarshal(actual["completed_at"], &completed), "read the actual legacy metadata completion instant")
		require.True(t, observer.clock.Equal(completed), "legacy metadata must complete at the exact actual outer transaction clock")
		expected["completed_at"] = actual["completed_at"]
	}
	require.JSONEq(t, string(fullAssessmentDispatchJSON(t, expected)), string(after.metadata), "worker completion/reconciliation may change only documented business completion or exact existing evidence attribution fields")
}

func fullAssessmentDispatchAssertRows(t *testing.T, before, after fullAssessmentDispatchGraph, f strandedReviewFixture, route string, children map[string]uuid.UUID) {
	t.Helper()
	var expected, actual []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(before.results, &expected), "retain every original typed and legacy result field before worker completion")
	require.NoError(t, json.Unmarshal(after.results, &actual), "decode complete committed result rows after the actual owner settles")
	actualByID := make(map[uuid.UUID]map[string]json.RawMessage, len(actual))
	for _, row := range actual {
		var id uuid.UUID
		require.NoError(t, json.Unmarshal(row["id"], &id), "match actual result rows by immutable identity")
		actualByID[id] = row
	}
	changed := 0
	for _, row := range expected {
		var id uuid.UUID
		require.NoError(t, json.Unmarshal(row["id"], &id), "retain each exact original reviewer or legacy contribution identity")
		if route != "already completed" {
			row["assessment_id"] = fullAssessmentDispatchJSON(t, f.assessment)
		}
		if id != children["pending"] && id != children["retry pending"] {
			continue
		}
		changed++
		var seq int32
		require.NoError(t, json.Unmarshal(row["dispatch_writer_seq"], &seq), "retain the original zero/prior-aborted attempt writer generation")
		var oldTime, newTime time.Time
		require.NoError(t, json.Unmarshal(row["dispatch_updated_at"], &oldTime), "retain original pending update history")
		require.NoError(t, json.Unmarshal(actualByID[id]["dispatch_updated_at"], &newTime), "read actual later cancellation statement time")
		require.False(t, newTime.IsZero() || newTime.Before(oldTime), "pending settlement must keep a real nonregressing database update clock")
		row["dispatch_state"], row["dispatch_due_at"], row["dispatch_resolution"] = json.RawMessage(`"cancelled"`), json.RawMessage("null"), json.RawMessage(`"owner_cancelled"`)
		row["dispatch_writer_seq"], row["dispatch_updated_at"] = fullAssessmentDispatchJSON(t, seq+1), actualByID[id]["dispatch_updated_at"]
	}
	require.Equal(t, 2, changed, "both actual fresh and retry-pending rows must be exercised by worker completion, not substituted by an existing cancelled row")
	require.JSONEq(t, string(fullAssessmentDispatchJSON(t, expected)), string(after.results), "only original evidence attribution and five pending fields may change; all business output and attempting/unknown/attention/accepted attempt history remain exact")
	for _, pair := range []struct {
		name          string
		before, after json.RawMessage
	}{
		{"jobs", before.jobs, after.jobs}, {"threads", before.threads, after.threads}, {"messages", before.messages, after.messages},
		{"inbox", before.inbox, after.inbox}, {"stamps", before.stamps, after.stamps},
	} {
		require.Equal(t, pair.before, pair.after, "actual worker completion must not send, reschedule, claim runtime or rewrite retained %s evidence", pair.name)
	}
	for _, pair := range []struct {
		name          string
		before, after json.RawMessage
	}{{"findings", before.findings, after.findings}, {"prompts", before.prompts, after.prompts}} {
		var rows []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(pair.before, &rows), "retain all original %s evidence before assessment attribution", pair.name)
		if route != "already completed" {
			for _, row := range rows {
				row["assessment_id"] = fullAssessmentDispatchJSON(t, f.assessment)
			}
		}
		require.JSONEq(t, string(fullAssessmentDispatchJSON(t, rows)), string(pair.after), "worker attribution may only attach the original assessment to unchanged %s content", pair.name)
	}
}

func TestFullAssessmentDispatchTerminalPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, route string
		coverage    bool
	}{
		{"completion with coverage", "complete", true}, {"completion without coverage", "complete", false},
		{"reconcile staged publishing assessment", "reconcile", true}, {"reconcile already completed assessment", "already completed", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Every case owns a fresh full-schema database and at most two checkouts.
			pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "worker integration must remain constrained to two actual pool slots")
			f, children := fullAssessmentDispatchFixture(t, pool, tt.route, tt.coverage)
			ctx := f.ctx()
			before := fullAssessmentDispatchSnapshot(t, ctx, pool, f)
			staged, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "retain the complete immutable assessment before the actual worker call")
			observer, err := fullAssessmentDispatchInvoke(t, pool, f, tt.route)
			require.NoError(t, err, "the actual worker must complete/reconcile its whole owner using only the single unreserved checkout")
			fullAssessmentDispatchAssertRun(t, observer)
			after := fullAssessmentDispatchSnapshot(t, ctx, pool, f)
			fullAssessmentDispatchAssertRows(t, before, after, f, tt.route, children)
			fullAssessmentDispatchAssertMetadata(t, before, after, f, tt.route, observer)
			completed, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the exact committed immutable assessment independently after the worker returned")
			expectedAssessment := staged
			if tt.route != "already completed" {
				require.NotNil(t, completed.CompletedAt, "actual assessment completion must retain its database completion clock")
				require.False(t, completed.CompletedAt.IsZero(), "completed immutable assessment cannot substitute a zero completion timestamp")
				require.True(t, observer.clock.Equal(*completed.CompletedAt), "immutable assessment and legacy metadata must settle at the same actual owner transaction clock")
				expectedAssessment.Status, expectedAssessment.CompletedAt = models.CodeReviewAssessmentCompleted, completed.CompletedAt
			}
			require.Equal(t, expectedAssessment, completed, "completion may change only immutable assessment status/time and must preserve the complete original publication, input, body, outcome and coverage tuple")
			if tt.route == "already completed" {
				require.Equal(t, before.metadata, after.metadata, "already-completed reconciliation cannot rewrite the original business metadata")
				require.Equal(t, before.session, after.session, "already-completed reconciliation cannot reclaim or reclassify the conversation owner")
				require.Equal(t, before.schedule, after.schedule, "already-completed reconciliation cannot re-settle previously committed coverage")
			} else {
				metadata := fullAssessmentDispatchObject(t, after.metadata)
				require.Equal(t, json.RawMessage(`"completed"`), metadata["status"], "actual worker completion must retain ordinary terminal metadata behavior")
				require.Equal(t, fullAssessmentDispatchJSON(t, f.assessment), metadata["assessment_id"], "the actual ordered outer unit must attach exact assessment provenance before finishing")
				var owner *uuid.UUID
				require.NoError(t, pool.QueryRow(ctx, `SELECT code_review_owner_pr_id FROM sessions WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID).Scan(&owner), "read durable conversation coverage independently after completion")
				if tt.coverage {
					require.Equal(t, &f.job.PullRequestID, owner, "full coverage must preserve the existing exact PR owner claim")
				} else {
					require.Nil(t, owner, "coverage=false must not gain a conversation owner merely because pending admission was settled")
					require.Equal(t, before.session, after.session, "coverage=false keeps the ordinary parent lifecycle untouched")
				}
				schedule, err := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.job.OrgID, f.job.PullRequestID)
				require.NoError(t, err, "read the actual same-unit scheduler settlement")
				require.Equal(t, &f.assessment, schedule.CurrentAssessmentID, "completion retains its existing current-assessment scheduling projection")
				require.Nil(t, schedule.ActiveAssessmentID, "known completion clears only the original active assessment reservation")
				require.Nil(t, schedule.ActiveSessionID, "known completion clears the original active controller session")
				require.Equal(t, models.CodeReviewScheduleCovered, schedule.State, "existing exact-head scheduler behavior remains independent of the separate coverage-complete owner-claim flag")
			}
		})
	}
}

func TestFullAssessmentDispatchReconcileRejectsSiblingMetadataPostgres(t *testing.T) {
	t.Parallel() // This regression owns a fresh database and two bounded pool slots.
	pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 2)
	f, _ := fullAssessmentDispatchFixture(t, pool, "already completed", true)
	ctx := f.ctx()
	metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "retain the independently bound first completed metadata before introducing a legal sibling owner")
	require.Equal(t, f.job.MetadataID, metadata.ID, "the reconciliation argument must be the original owner, not reconstructed from the later session lookup")
	staged, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "retain immutable original assessment provenance for the separate legal sibling fixture")
	siblingMetadata, siblingAssessment := uuid.New(), uuid.New()
	key := "synthetic-sibling-completion:" + siblingAssessment.String()
	receipt, err := fullAssessmentPublicationReceipt(siblingAssessment, staged.InputDigest, staged.HeadSHA, staged.GitHubReviewID, staged.GitHubReviewURL, nil, nil)
	require.NoError(t, err, "retain a correctly self-bound synthetic publication receipt for the separate sibling assessment")
	fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(unitCtx, `INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,
			base_sha,head_sha,trigger_source,status,decision,acceptable,review_output_key,github_review_id,github_review_url,final_review_body,completed_at)
			SELECT $3,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,
			'completed',decision,acceptable,$4,github_review_id,github_review_url,final_review_body,completed_at
			FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID, siblingMetadata, key); err != nil {
			return err
		}
		// A later legal retained clock makes the existing created_at/id lookup
		// deterministically select the other owner; no sleep or server clock change.
		if _, err := tx.Exec(unitCtx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,
			pull_request_id,metadata_id,session_id,policy_id,generation,base_sha,base_ref,head_sha,input_version,
			code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,
			review_scope,route_reason,result_origin,coverage_complete,status,decision,acceptable,risk_reason_details,
			structured_outcome,rendered_body,publication_key,publication_state,publication_receipt,github_review_id,
			github_review_url,submitted_commit_sha,created_at,completed_at)
			SELECT $3,org_id,repository_id,repository_full_name,pull_request_id,$4,session_id,policy_id,generation+1,
			base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,
			gate_digest,input_digest,input_manifest,review_scope,route_reason,result_origin,coverage_complete,'completed',
			decision,acceptable,risk_reason_details,structured_outcome,rendered_body,$5,publication_state,$6::jsonb,
			github_review_id,github_review_url,submitted_commit_sha,created_at+interval '1 minute',completed_at+interval '1 minute'
			FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, siblingAssessment, siblingMetadata, key, []byte(receipt)); err != nil {
			return err
		}
		_, err := tx.Exec(unitCtx, `UPDATE code_review_session_metadata SET assessment_id=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, siblingMetadata, siblingAssessment)
		return err
	})
	latest, err := f.stores.CodeReviewAssessments.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "the actual latest-full session lookup must reach a valid real sibling, not a fabricated mismatching mock")
	require.Equal(t, siblingAssessment, latest.ID, "the regression must exercise the actual latest assessment selected from another legal completed metadata owner")
	require.Equal(t, siblingMetadata, latest.MetadataID, "the actual latest assessment must be independently associated with the sibling rather than the retained reconciliation argument")
	association := func() json.RawMessage {
		t.Helper()
		var raw json.RawMessage
		require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_array(
			(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM code_review_session_metadata m WHERE m.org_id=$1 AND m.session_id=$2),
			(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM code_review_revision_assessments a WHERE a.org_id=$1 AND a.session_id=$2))`, f.job.OrgID, f.job.SessionID).Scan(&raw), "snapshot both complete legal owners and both immutable assessments in the same session domain")
		return raw
	}
	before, ownersBefore := fullAssessmentDispatchSnapshot(t, ctx, pool, f), association()
	reserved, err := pool.Acquire(ctx)
	require.NoError(t, err, "leave only one unreserved actual connection while exercising the exact-owner reconciliation refusal")
	t.Cleanup(reserved.Release)
	observer := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
	f.stores.ThreadSendDependencies.Transactions = observer
	err = reconcileCompletedFullAssessment(ctx, f.stores, metadata)
	after, ownersAfter := fullAssessmentDispatchSnapshot(t, ctx, pool, f), association()
	require.Equal(t, before, after, "a latest sibling assessment must not authorize cleanup of the original owner's pending rows or mutate any coverage/business/evidence graph")
	require.Equal(t, ownersBefore, ownersAfter, "scope rejection must leave both complete metadata and assessment rows exact, not just the original projection")
	require.ErrorIs(t, err, db.ErrCodeReviewAssessmentState, "the actual worker must reject a latest full assessment bound to another metadata ID even within the same tenant/session/PR")
}

func TestFullAssessmentDispatchChildFailureRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, route string }{{"completion", "complete"}, {"publishing reconciliation", "reconcile"}, {"already completed reconciliation", "already completed"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Each exact-name fault CHECK lives in a separate fully migrated database.
			pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 2)
			f, children := fullAssessmentDispatchFixture(t, pool, tt.route, true)
			ctx := f.ctx()
			before := fullAssessmentDispatchSnapshot(t, ctx, pool, f)
			query := fmt.Sprintf(`ALTER TABLE code_review_agent_results ADD CONSTRAINT synthetic_assessment_dispatch_child_failure CHECK (id<>'%s'::uuid OR dispatch_state IS DISTINCT FROM 'cancelled') NOT VALID`, children["pending"].String())
			_, err := pool.Exec(ctx, query)
			require.NoError(t, err, "install one actual exact-child CHECK without changing any original database guard or writer")
			t.Cleanup(func() {
				_, err := pool.Exec(context.Background(), `ALTER TABLE code_review_agent_results DROP CONSTRAINT synthetic_assessment_dispatch_child_failure`)
				require.NoError(t, err, "remove only the isolated synthetic fault constraint before fixture database teardown")
			})
			observer, err := fullAssessmentDispatchInvoke(t, pool, f, tt.route)
			var actual *pgconn.PgError
			require.True(t, errors.As(err, &actual), "the actual worker must reach the genuine child write and preserve its PostgreSQL cause, not fail in fixture/capability setup")
			require.Equal(t, "23514", actual.Code, "the worker failure must be the real nonretryable child CHECK violation")
			require.Equal(t, "synthetic_assessment_dispatch_child_failure", actual.ConstraintName, "the exact intended child CHECK must be the actual failure source")
			fullAssessmentDispatchAssertRun(t, observer)
			require.Equal(t, before, fullAssessmentDispatchSnapshot(t, ctx, pool, f), "real child failure rolls back complete assessment, coverage owner, scheduler, metadata, original evidence attribution, all reviewer history and retained publication/linkage graph together")
		})
	}
}
