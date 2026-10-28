package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// These are storage/transaction integration fixtures, not semantic command,
// real Send, provider delivery, notification or publication evidence. Every
// case owns an isolated fully migrated database. Run with -parallel=1 when the
// local PostgreSQL reservation permits only this file's two/three checkouts.
type endedDispatchGraph struct {
	fullAssessmentDispatchGraph
	requests, control json.RawMessage
}

func endedDispatchSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture) endedDispatchGraph {
	t.Helper()
	g := endedDispatchGraph{fullAssessmentDispatchGraph: fullAssessmentDispatchSnapshot(t, ctx, pool, f)}
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_requests r WHERE r.org_id=$1),'[]'::jsonb),
		(SELECT to_jsonb(c) FROM caller_send_intent_control c WHERE c.singleton)`, f.job.OrgID).Scan(&g.requests, &g.control),
		"retain complete source requests and local control in addition to the frozen full owner graph")
	return g
}

func endedDispatchFixture(t *testing.T, pool *pgxpool.Pool, states []string) (strandedReviewFixture, uuid.UUID, map[string]uuid.UUID) {
	t.Helper()
	// Unlike the completion fixture, this is the actual unsent ended boundary:
	// result_origin remains NULL and publication remains not_started.
	f := seedStrandedReview(t, pool, "failed")
	ctx := f.ctx()
	thread := uuid.New()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`, nil},
		{`INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,completed_at)
			VALUES($1,$2,$3,'claude_code','Synthetic drained ended lane','completed',statement_timestamp())`, []any{thread, f.job.OrgID, f.job.SessionID}},
		{`UPDATE code_review_pr_state SET active_assessment_id=$3 WHERE org_id=$1 AND pull_request_id=$2`, []any{f.job.OrgID, f.job.PullRequestID, f.assessment}},
		{`INSERT INTO code_review_findings(org_id,session_id,dedupe_key,severity,confidence,path,summary,body)
			VALUES($1,$2,'synthetic-ended-finding','info','high','synthetic.go','Synthetic retained finding','Synthetic retained evidence')`, []any{f.job.OrgID, f.job.SessionID}},
		{`INSERT INTO code_review_prompt_records(org_id,session_id,record_key,role,content)
			VALUES($1,$2,'synthetic-ended-prompt','reviewer','Synthetic retained prompt')`, []any{f.job.OrgID, f.job.SessionID}},
		{`INSERT INTO code_review_requests(org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,session_id,assessment_id,status)
			VALUES($1,$2,$3,'synthetic','ended-exact-assessment','review_now','synthetic-input',1,$4,$5,'joined'),
			($1,$2,$3,'synthetic','ended-legacy-session','review_now','synthetic-input',1,$4,NULL,'pending')`, []any{f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID, f.job.SessionID, f.assessment}},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed only synthetic full-schema ended-parent, drained-lane and original source-request prerequisites")
	}
	seedSavedReviewResults(t, pool, f)
	var originalJobs json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j WHERE j.org_id=$1`, f.job.OrgID).Scan(&originalJobs), "retain the original running wake and exact controller lease before actual dispatch preparation")
	children := make(map[string]uuid.UUID, len(states))
	for _, state := range states {
		children[state] = fullAssessmentDispatchChild(t, ctx, pool, f, thread, state)
	}
	var preparedJobs json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j WHERE j.org_id=$1`, f.job.OrgID).Scan(&preparedJobs), "read actual original-wake dedupe after all real preparation and claim/settlement fixtures")
	require.Equal(t, originalJobs, preparedJobs, "every original running-controller wake must dedupe without creating a competing recovery job or changing its lease")
	return f, thread, children
}

func endedDispatchInvoke(t *testing.T, pool *pgxpool.Pool, f strandedReviewFixture) (*fullAssessmentDispatchRunObserver, bool, bool, error) {
	t.Helper()
	reserved, err := pool.Acquire(f.ctx())
	require.NoError(t, err, "reserve one of two actual checkouts before invoking the complete ended-owner path")
	defer reserved.Release()
	observer := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
	f.stores.ThreadSendDependencies.Transactions = observer
	resumed, settled, err := recoverEndedCodeReviewParent(f.ctx(), f.stores, f.job, models.CodeReviewPolicyConfig{})
	return observer, resumed, settled, err
}

func endedDispatchAssertTime(t *testing.T, row map[string]json.RawMessage, field string, expected time.Time) {
	t.Helper()
	var actual time.Time
	require.NoError(t, json.Unmarshal(row[field], &actual), "decode the actual ended-owner %s database clock", field)
	require.True(t, expected.Equal(actual), "ended-owner %s must use the same actual supplied transaction clock", field)
}

func endedDispatchRows(t *testing.T, raw json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &rows), "decode complete database rows without float conversion or dropped private/history fields")
	// A row captured directly from PostgreSQL and a filtered complete-row
	// array have different presentation whitespace. Compact only that syntax;
	// never decode numbers through interface{} or compare float-based JSONEq.
	for _, row := range rows {
		for key, value := range row {
			var compact bytes.Buffer
			require.NoError(t, json.Compact(&compact, value), "retain exact numeric lexemes and complete nested values while normalizing JSON presentation whitespace")
			row[key] = append(json.RawMessage(nil), compact.Bytes()...)
		}
	}
	return rows
}

func endedDispatchAssertChildren(t *testing.T, before, after json.RawMessage, pending map[uuid.UUID]bool) {
	t.Helper()
	expected, actual := endedDispatchRows(t, before), endedDispatchRows(t, after)
	actualByID := make(map[uuid.UUID]map[string]json.RawMessage, len(actual))
	for _, row := range actual {
		var id uuid.UUID
		require.NoError(t, json.Unmarshal(row["id"], &id), "match a complete committed child by independent immutable identity")
		actualByID[id] = row
	}
	changed := 0
	for _, row := range expected {
		var id uuid.UUID
		require.NoError(t, json.Unmarshal(row["id"], &id), "retain each original reviewer or legacy business result identity")
		if !pending[id] {
			continue
		}
		changed++
		actualRow, found := actualByID[id]
		require.True(t, found, "pending cancellation must retain the original result rather than replacing or deleting it")
		var seq int64
		var previous, current time.Time
		require.NoError(t, json.Unmarshal(row["dispatch_writer_seq"], &seq), "retain the original pending writer generation including known-abort history")
		require.NoError(t, json.Unmarshal(row["dispatch_updated_at"], &previous), "retain the original pending database update clock")
		require.NoError(t, json.Unmarshal(actualRow["dispatch_updated_at"], &current), "read the actual later pending cancellation clock")
		require.False(t, current.IsZero() || current.Before(previous), "pending cancellation must use a nonregressing real statement clock")
		row["dispatch_state"], row["dispatch_resolution"], row["dispatch_due_at"] = json.RawMessage(`"cancelled"`), json.RawMessage(`"owner_cancelled"`), json.RawMessage(`null`)
		row["dispatch_writer_seq"], row["dispatch_updated_at"] = fullAssessmentDispatchJSON(t, seq+1), actualRow["dispatch_updated_at"]
	}
	require.Equal(t, len(pending), changed, "the exact fresh/retry-pending identities must reach the actual later cancellation statement")
	require.Equal(t, expected, actual, "only five pending fields may change; every attempted/unknown/attention/accepted tuple and all original business/evidence fields must remain exact")
}

func endedDispatchAssertComment(t *testing.T, before, after json.RawMessage, f strandedReviewFixture, clock time.Time) {
	t.Helper()
	oldRows, rows := endedDispatchRows(t, before), endedDispatchRows(t, after)
	oldByID := make(map[uuid.UUID]map[string]json.RawMessage, len(oldRows))
	for _, row := range oldRows {
		var id uuid.UUID
		require.NoError(t, json.Unmarshal(row["id"], &id), "retain the independent original job identity")
		oldByID[id] = row
	}
	var added []map[string]json.RawMessage
	for _, row := range rows {
		var id uuid.UUID
		require.NoError(t, json.Unmarshal(row["id"], &id), "read each actual post-settlement job identity")
		if old, found := oldByID[id]; found {
			require.Equal(t, old, row, "the original running-controller payload, lease and dedupe identity cannot be rewritten by ended settlement")
			delete(oldByID, id)
		} else {
			added = append(added, row)
		}
	}
	require.Empty(t, oldByID, "no original controller or retained job may disappear during ended settlement")
	require.Len(t, added, 1, "the only new job is the original terminal status-comment intent, not a dispatch resend or replacement controller")
	raw := fullAssessmentDispatchJSON(t, added[0])
	var actual models.Job
	require.NoError(t, json.Unmarshal(raw, &actual), "read the complete newly committed terminal-comment job")
	require.NotEqual(t, uuid.Nil, actual.ID, "terminal-comment identity must be genuinely minted by the actual enqueue statement")
	for _, field := range []string{"run_at", "created_at", "updated_at"} {
		endedDispatchAssertTime(t, added[0], field, clock)
	}
	var payload map[string]uuid.UUID
	require.NoError(t, json.Unmarshal(actual.Payload, &payload), "decode only the complete synthetic original terminal-comment payload")
	expectedPayload := map[string]uuid.UUID{"org_id": f.job.OrgID, "repository_id": f.job.RepositoryID, "pull_request_id": f.job.PullRequestID, "session_id": f.job.SessionID, "triggering_dispute_id": *f.job.TriggeringDisputeID}
	require.Equal(t, expectedPayload, payload, "terminal status-comment intent must retain the complete original source provenance without adding a new publication decision")
	key := "code_review_status_comment:" + f.job.SessionID.String() + ":terminal"
	expected := models.Job{ID: actual.ID, OrgID: f.job.OrgID, Queue: "default", JobType: models.JobTypeSyncCodeReviewStatusComment,
		Payload: actual.Payload, Priority: 3, Status: models.JobStatusPending, MaxAttempts: 3, RunAt: actual.RunAt,
		OwnerKind: models.JobOwnerKindWorker, DedupeKey: &key, CreatedAt: actual.CreatedAt, UpdatedAt: actual.UpdatedAt}
	require.Equal(t, expected, actual, "the complete same-unit comment enqueue must preserve original queue, priority, attempts and empty ownership/error/completion fields")
}

func endedDispatchAssertSettlement(t *testing.T, before, after endedDispatchGraph, f strandedReviewFixture, pending map[uuid.UUID]bool, observer *fullAssessmentDispatchRunObserver) {
	t.Helper()
	metadata, actualMetadata := fullAssessmentDispatchObject(t, before.metadata), fullAssessmentDispatchObject(t, after.metadata)
	for _, field := range []string{"phase", "decision", "acceptable", "retry_at"} {
		metadata[field] = json.RawMessage(`null`)
	}
	metadata["status"], metadata["failure_reason"] = json.RawMessage(`"failed"`), fullAssessmentDispatchJSON(t, "original execution timeout")
	metadata["status_code"] = fullAssessmentDispatchJSON(t, models.CodeReviewStatusCodeReviewerFailed)
	metadata["status_message"] = fullAssessmentDispatchJSON(t, "Review execution ended before usable output was saved. Retry the review to start a fresh attempt.")
	metadata["retryable_failure"] = json.RawMessage(`true`)
	for _, field := range []string{"last_error_at", "completed_at"} {
		endedDispatchAssertTime(t, actualMetadata, field, observer.clock)
		metadata[field] = actualMetadata[field]
	}
	require.Equal(t, metadata, actualMetadata, "ended failure may change only the original documented metadata failure projection, never attribution, receipt or saved output")
	assessment, actualAssessment := fullAssessmentDispatchObject(t, before.assessment), fullAssessmentDispatchObject(t, after.assessment)
	assessment["status"], assessment["failure_detail"] = json.RawMessage(`"failed"`), fullAssessmentDispatchJSON(t, "original execution timeout")
	endedDispatchAssertTime(t, actualAssessment, "completed_at", observer.clock)
	assessment["completed_at"] = actualAssessment["completed_at"]
	require.Equal(t, assessment, actualAssessment, "the exact unsent assessment must fail without inventing result origin, coverage, execution, receipt or publication")
	schedule, actualSchedule := fullAssessmentDispatchObject(t, before.schedule), fullAssessmentDispatchObject(t, after.schedule)
	for _, field := range []string{"active_assessment_id", "active_session_id", "retry_at", "eligible_at"} {
		schedule[field] = json.RawMessage(`null`)
	}
	schedule["state"], schedule["wait_reason"] = fullAssessmentDispatchJSON(t, models.CodeReviewScheduleIdle), fullAssessmentDispatchJSON(t, models.CodeReviewWaitNone)
	endedDispatchAssertTime(t, actualSchedule, "updated_at", observer.clock)
	schedule["updated_at"] = actualSchedule["updated_at"]
	require.Equal(t, schedule, actualSchedule, "same-unit failure must release only the exact original reservation and retain every other scheduler field")
	requests, actualRequests := endedDispatchRows(t, before.requests), endedDispatchRows(t, after.requests)
	require.Len(t, requests, 2, "exercise both exact-assessment and legacy session-only original request settlement predicates")
	for _, row := range requests {
		row["status"] = json.RawMessage(`"failed"`)
	}
	require.Equal(t, requests, actualRequests, "both original source-bound requests must fail in the same unit without changing identity, generation, provenance or timestamps")
	endedDispatchAssertChildren(t, before.results, after.results, pending)
	endedDispatchAssertComment(t, before.jobs, after.jobs, f, observer.clock)
	for _, pair := range []struct {
		name          string
		before, after json.RawMessage
	}{
		{"session", before.session, after.session}, {"findings", before.findings, after.findings}, {"prompts", before.prompts, after.prompts},
		{"threads", before.threads, after.threads}, {"messages", before.messages, after.messages}, {"inbox", before.inbox, after.inbox},
		{"stamps", before.stamps, after.stamps}, {"local control", before.control, after.control},
	} {
		require.Equal(t, pair.before, pair.after, "ended-owner settlement cannot rewrite retained %s or confer a new send/runtime/publication authority", pair.name)
	}
}

func TestEndedReviewDispatchTerminalPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		states []string
	}{
		{"fresh pending", []string{"pending"}},
		{"mixed attempted and unresolved history", []string{"pending", "retry pending", "attempting", "unknown", "attention", "accepted"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "ended-owner integration must stay within two actual pool slots")
			f, _, children := endedDispatchFixture(t, pool, tt.states)
			before := endedDispatchSnapshot(t, f.ctx(), pool, f)
			observer, resumed, settled, err := endedDispatchInvoke(t, pool, f)
			require.NoError(t, err, "the actual failed/drained-parent path must settle its whole graph using only one unreserved checkout")
			require.False(t, resumed, "a failed parent cannot escape as validated saved-work continuation")
			require.True(t, settled, "known whole-unit commit must preserve the existing worker terminal-settlement result")
			fullAssessmentDispatchAssertRun(t, observer)
			pending := map[uuid.UUID]bool{children["pending"]: true}
			if id, found := children["retry pending"]; found {
				pending[id] = true
			}
			endedDispatchAssertSettlement(t, before, endedDispatchSnapshot(t, f.ctx(), pool, f), f, pending, observer)
		})
	}
}

func TestEndedReviewDispatchTerminalRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, constraint string
		comment          bool
	}{
		{"exact pending child CHECK", "synthetic_ended_dispatch_child_failure", false},
		{"terminal comment enqueue CHECK", "synthetic_ended_dispatch_comment_failure", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Each fault constraint and retained graph live in a separate full-schema database.
			pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 2)
			f, _, children := endedDispatchFixture(t, pool, []string{"pending", "retry pending", "attempting", "unknown", "attention", "accepted"})
			query, cleanupQuery := "", `ALTER TABLE code_review_agent_results DROP CONSTRAINT synthetic_ended_dispatch_child_failure`
			if tt.comment {
				query = `ALTER TABLE jobs ADD CONSTRAINT synthetic_ended_dispatch_comment_failure CHECK (job_type<>'sync_code_review_status_comment') NOT VALID`
				cleanupQuery = `ALTER TABLE jobs DROP CONSTRAINT synthetic_ended_dispatch_comment_failure`
			} else {
				query = fmt.Sprintf(`ALTER TABLE code_review_agent_results ADD CONSTRAINT synthetic_ended_dispatch_child_failure CHECK (id<>'%s'::uuid OR dispatch_state IS DISTINCT FROM 'cancelled') NOT VALID`, children["pending"].String())
			}
			_, err := pool.Exec(f.ctx(), query)
			require.NoError(t, err, "install only the exact isolated synthetic fault, preserving every original shape/transition guard")
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), db.LifecycleTransactionTimeout)
				defer cancel()
				_, err := pool.Exec(ctx, cleanupQuery)
				require.NoError(t, err, "remove only the isolated synthetic fault before disposable database teardown")
			})
			before := endedDispatchSnapshot(t, f.ctx(), pool, f)
			observer, resumed, settled, err := endedDispatchInvoke(t, pool, f)
			var cause *pgconn.PgError
			require.True(t, errors.As(err, &cause), "failure must preserve the actual PostgreSQL cause rather than stop at capability, scope or fixture setup")
			require.Equal(t, "23514", cause.Code, "the actual nonretryable failure must be the intended PostgreSQL CHECK violation")
			require.Equal(t, tt.constraint, cause.ConstraintName, "the exact child or same-unit terminal-comment CHECK must be the cause")
			require.False(t, resumed, "a callback failure cannot leak a provisional saved-work continuation")
			require.False(t, settled, "a rolled-back unit cannot escape as successful terminal settlement")
			fullAssessmentDispatchAssertRun(t, observer)
			require.Equal(t, before, endedDispatchSnapshot(t, f.ctx(), pool, f), "a genuine child/comment failure must roll back metadata, assessment, both requests, comment, scheduler, every dispatch history and all retained evidence/control together")
		})
	}
}

type endedDispatchBackend struct {
	pid int32
	ctx context.Context
}

// This transparent gate observes the real backend and optionally pauses only
// AFTER the actual DB callback. The existing supplied Run still owns setup,
// retries, transaction settlement and the unchanged whole-unit deadline.
type endedDispatchRunGate struct {
	base                  db.CodeReviewMetadataTransactions
	before, after         chan endedDispatchBackend
	release               <-chan struct{}
	beforeOnce, afterOnce sync.Once
}

func (r *endedDispatchRunGate) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		var pid int32
		if err := tx.QueryRow(unitCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		point := endedDispatchBackend{pid: pid, ctx: unitCtx}
		if r.before != nil {
			r.beforeOnce.Do(func() { r.before <- point })
		}
		if err := work(unitCtx, tx); err != nil {
			return err
		}
		if r.after != nil {
			r.afterOnce.Do(func() { r.after <- point })
		}
		if r.release != nil {
			select {
			case <-r.release:
			case <-unitCtx.Done():
				return unitCtx.Err()
			}
		}
		return nil
	})
}

type endedDispatchRecoveryResult struct {
	resumed, settled bool
	err              error
}

func endedDispatchAwaitBackend(t *testing.T, ctx context.Context, points <-chan endedDispatchBackend, finished <-chan struct{}) endedDispatchBackend {
	t.Helper()
	select {
	case point := <-points:
		return point
	case <-finished:
		t.Fatal("actual database operation ended before reaching its required independently observable lock boundary")
	case <-ctx.Done():
		t.Fatal("existing bounded operation ended before its database lock boundary was observable")
	}
	return endedDispatchBackend{}
}

func endedDispatchAwaitMetadataWait(t *testing.T, observer *pgxpool.Conn, waiting endedDispatchBackend, holder int32, marker string, finished <-chan struct{}) {
	t.Helper()
	for {
		select {
		case <-finished:
			t.Fatal("actual competing operation ended before the exact metadata holder relationship was observed")
		case <-waiting.ctx.Done():
			t.Fatal("unchanged operation budget ended before the exact metadata wait relationship was observed")
		default:
		}
		var blocked bool
		err := observer.QueryRow(waiting.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a
			WHERE a.datname=current_database() AND a.pid=$1::integer AND a.state='active' AND a.wait_event_type='Lock'
			AND $2::integer=ANY(pg_blocking_pids(a.pid)) AND position($3::text IN a.query)>0)`, waiting.pid, holder, marker).Scan(&blocked)
		require.NoError(t, err, "independent warmed observer must prove the exact real backend is waiting on the actual metadata holder")
		if blocked {
			return
		}
	}
}

func endedDispatchPreparation(t *testing.T, pool *pgxpool.Pool, f strandedReviewFixture, thread uuid.UUID) db.CodeReviewDispatchPreparation {
	t.Helper()
	var original json.RawMessage
	require.NoError(t, pool.QueryRow(f.ctx(), `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&original), "retain the original running-controller payload instead of rebuilding its typed projection")
	wake, err := freezeCodeReviewReviewerDispatchWake(f.job, original)
	require.NoError(t, err, "freeze the complete original controller wake with exact independently supplied routing correlation")
	result := uuid.New()
	key := "cr-dispatch:v1:" + result.String()
	return db.CodeReviewDispatchPreparation{ResultID: result, SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, ThreadID: thread,
		AgentProvider: "claude_code", Version: 1, Key: key, Digest: strings.Repeat("a", 64), ReviewOutputKey: f.job.OutputKey,
		RawOutput: new("Synthetic retained installer contribution"), StructuredResult: json.RawMessage(`{"synthetic":"retained"}`), Wake: wake,
		Input: fullAssessmentDispatchJSON(t, map[string]any{"version": 1, "purpose": db.CallerSendStampCodeReviewDispatch, "org_id": f.job.OrgID,
			"session_id": f.job.SessionID, "thread_id": thread, "client_message_id": key, "message": "Synthetic original installer command"})}
}

func TestEndedReviewDispatchMetadataLockOrderPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		installerFirst bool
	}{{"installer before ended owner", true}, {"ended owner before installer", false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Cases use separate databases; only three simultaneous actual checkouts are permitted within a case.
			pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 3)
			require.Equal(t, int32(3), pool.Config().MaxConns, "metadata ordering proof must stay within installer, owner and independent observer slots")
			f, thread, _ := endedDispatchFixture(t, pool, []string{"pending"})
			input := endedDispatchPreparation(t, pool, f, thread)
			before := endedDispatchSnapshot(t, f.ctx(), pool, f)
			parent, cancel := context.WithCancel(f.ctx())
			t.Cleanup(cancel)
			observer, err := pool.Acquire(parent)
			require.NoError(t, err, "reserve and warm the independent lock observer before either contender starts")
			t.Cleanup(observer.Release)
			var observerPID int32
			require.NoError(t, observer.QueryRow(parent, `SELECT pg_backend_pid()`).Scan(&observerPID), "warm the independent actual observer without adding capacity during contention")
			require.Greater(t, observerPID, int32(0), "lock evidence must come from a real independent PostgreSQL backend")
			realRun := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			recoveryDone := make(chan endedDispatchRecoveryResult, 1)
			recoveryFinished := make(chan struct{})
			launchRecovery := func(gate *endedDispatchRunGate) {
				f.stores.ThreadSendDependencies.Transactions = gate
				go func() {
					defer close(recoveryFinished)
					resumed, settled, err := recoverEndedCodeReviewParent(parent, f.stores, f.job, models.CodeReviewPolicyConfig{})
					recoveryDone <- endedDispatchRecoveryResult{resumed: resumed, settled: settled, err: err}
				}()
				t.Cleanup(func() { cancel(); <-recoveryFinished })
			}
			if tt.installerFirst {
				unitCtx, stop := context.WithTimeout(parent, db.LifecycleTransactionTimeout)
				defer stop()
				installer, err := pool.BeginTx(unitCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
				require.NoError(t, err, "the installer must explicitly own its bounded READ COMMITTED transaction before locking metadata")
				defer func() {
					cancel()
					cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), db.LifecycleTransactionTimeout)
					defer cleanupCancel()
					_ = installer.Rollback(cleanupCtx)
				}()
				require.NoError(t, db.ConfigureLifecycleTransactionInTx(unitCtx, installer), "the test-owned installer must retain the real unchanged production marker and lock/statement policy")
				var metadata uuid.UUID
				var holderPID int32
				require.NoError(t, installer.QueryRow(unitCtx, `SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR SHARE`, f.job.OrgID, f.job.SessionID, f.job.MetadataID).Scan(&metadata), "installer must hold the actual exact metadata SHARE before any child/session-FK write")
				require.Equal(t, f.job.MetadataID, metadata, "the actual installer holder must be the independently bound original metadata owner")
				require.NoError(t, installer.QueryRow(unitCtx, `SELECT pg_backend_pid()`).Scan(&holderPID), "identify the actual metadata SHARE holder rather than infer ordering from elapsed time")
				started := make(chan endedDispatchBackend, 1)
				launchRecovery(&endedDispatchRunGate{base: realRun, before: started})
				waiting := endedDispatchAwaitBackend(t, unitCtx, started, recoveryFinished)
				require.NotEqual(t, holderPID, waiting.pid, "the actual ended owner and installer must hold different backend checkouts")
				require.NotEqual(t, observerPID, waiting.pid, "the lock observer must not answer its own contender query")
				endedDispatchAwaitMetadataWait(t, observer, waiting, holderPID, "SELECT id FROM code_review_session_metadata", recoveryFinished)
				var session uuid.UUID
				require.NoError(t, installer.QueryRow(unitCtx, `SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR KEY SHARE NOWAIT`, f.job.OrgID, f.job.SessionID).Scan(&session), "metadata-first recovery must not already hold the inverted parent-session UPDATE lock needed by this installer FK")
				require.Equal(t, f.job.SessionID, session, "the nonblocking FK-domain probe must be the exact original ended parent")
				prepared, err := db.PrepareCodeReviewDispatchInTx(unitCtx, f.job.OrgID, installer, input)
				require.NoError(t, err, "the real installer must still persist its child while holding metadata ahead of the waiting terminal owner")
				require.Equal(t, input.ResultID, prepared.Result().ID, "actual preparation must persist the independent preminted result identity")
				require.False(t, prepared.WakeInserted(), "the original running controller wake must dedupe without creating competing live work")
				var insertedBefore json.RawMessage
				require.NoError(t, installer.QueryRow(unitCtx, `SELECT to_jsonb(r) FROM code_review_agent_results r WHERE r.org_id=$1 AND r.session_id=$2 AND r.id=$3`, f.job.OrgID, f.job.SessionID, input.ResultID).Scan(&insertedBefore), "capture the genuinely inserted complete child inside its original transaction before releasing the waiting owner")
				require.NoError(t, installer.Commit(unitCtx), "installer must commit before the waiting real ended owner takes its fresh child snapshot")
				result := <-recoveryDone
				<-recoveryFinished
				require.NoError(t, result.err, "actual ended recovery must finish after installer commit within the unchanged lock and whole-unit budgets")
				require.False(t, result.resumed, "a failed original parent cannot resume merely because an installer won the metadata lock")
				require.True(t, result.settled, "the actual owner must commit the original failure after observing the committed child")
				fullAssessmentDispatchAssertRun(t, realRun)
				after := endedDispatchSnapshot(t, parent, pool, f)
				// The ordinary success root checks all original rows. Here compare
				// this genuinely concurrent addition separately, never fabricate a
				// before snapshot for the row that did not exist before contention.
				var retained []map[string]json.RawMessage
				var insertedAfter []map[string]json.RawMessage
				for _, row := range endedDispatchRows(t, after.results) {
					if string(row["id"]) == string(fullAssessmentDispatchJSON(t, input.ResultID)) {
						insertedAfter = append(insertedAfter, row)
					} else {
						retained = append(retained, row)
					}
				}
				endedDispatchAssertChildren(t, fullAssessmentDispatchJSON(t, []json.RawMessage{insertedBefore}), fullAssessmentDispatchJSON(t, insertedAfter), map[uuid.UUID]bool{input.ResultID: true})
				after.results = fullAssessmentDispatchJSON(t, retained)
				originalRows := endedDispatchRows(t, before.results)
				pending := make(map[uuid.UUID]bool)
				for _, row := range originalRows {
					if string(row["dispatch_state"]) == `"pending"` {
						var id uuid.UUID
						require.NoError(t, json.Unmarshal(row["id"], &id), "retain actual original pending identity for concurrent whole-graph comparison")
						pending[id] = true
					}
				}
				endedDispatchAssertSettlement(t, before, after, f, pending, realRun)
			} else {
				release := make(chan struct{})
				var releaseOnce sync.Once
				defer func() { cancel(); releaseOnce.Do(func() { close(release) }) }()
				staged := make(chan endedDispatchBackend, 1)
				launchRecovery(&endedDispatchRunGate{base: realRun, after: staged, release: release})
				// The real callback has already staged the complete original failure
				// and holds its metadata lock, but the supplied Run has not committed.
				holder := endedDispatchAwaitBackend(t, parent, staged, recoveryFinished)
				installerStarted := make(chan endedDispatchBackend, 1)
				installerDone := make(chan error, 1)
				installerFinished := make(chan struct{})
				var prepared db.PreparedCodeReviewDispatch
				go func() {
					defer close(installerFinished)
					installerDone <- db.RunLifecycleTransaction(parent, pool, func(unitCtx context.Context, tx pgx.Tx) error {
						prepared = db.PreparedCodeReviewDispatch{}
						var pid int32
						if err := tx.QueryRow(unitCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
							return err
						}
						select {
						case installerStarted <- endedDispatchBackend{pid: pid, ctx: unitCtx}:
						default:
						}
						var err error
						prepared, err = db.PrepareCodeReviewDispatchInTx(unitCtx, f.job.OrgID, tx, input)
						return err
					})
				}()
				t.Cleanup(func() { cancel(); <-installerFinished })
				waiting := endedDispatchAwaitBackend(t, holder.ctx, installerStarted, installerFinished)
				require.NotEqual(t, holder.pid, waiting.pid, "the real installer and terminal owner must be independent PostgreSQL backends")
				require.NotEqual(t, observerPID, waiting.pid, "the warmed observer cannot occupy the real installer checkout")
				endedDispatchAwaitMetadataWait(t, observer, waiting, holder.pid, "code_review_session_metadata", installerFinished)
				releaseOnce.Do(func() { close(release) })
				result := <-recoveryDone
				<-recoveryFinished
				installerErr := <-installerDone
				<-installerFinished
				require.NoError(t, result.err, "the actual existing owner must commit the already-staged full failure before installer eligibility is rechecked")
				require.False(t, result.resumed, "terminal-first ordering cannot leak provisional resume state")
				require.True(t, result.settled, "terminal-first ordering retains the original known-commit worker result")
				require.ErrorIs(t, installerErr, db.ErrCodeReviewDispatchPreparationIneligible, "an actual blocked installer must recheck committed owner eligibility, not install into terminal metadata")
				require.Equal(t, db.PreparedCodeReviewDispatch{}, prepared, "ineligible preparation must not escape a provisional child or wake descriptor")
				fullAssessmentDispatchAssertRun(t, realRun)
				after := endedDispatchSnapshot(t, parent, pool, f)
				var pendingID uuid.UUID
				for _, row := range endedDispatchRows(t, before.results) {
					if string(row["dispatch_state"]) == `"pending"` {
						require.NoError(t, json.Unmarshal(row["id"], &pendingID), "retain the only original pending identity before terminal-first ordering")
					}
				}
				require.NotEqual(t, uuid.Nil, pendingID, "reverse-order proof must include an original pending result, not a vacuous owner-only fixture")
				endedDispatchAssertSettlement(t, before, after, f, map[uuid.UUID]bool{pendingID: true}, realRun)
			}
		})
	}
}
