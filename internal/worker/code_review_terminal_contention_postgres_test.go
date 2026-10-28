package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// This regression preserves the original actual Worker.New/poll queue owner.
// The handler injects only a finite fatal or exhausted result and registers
// the real full-controller cleanup hook. Explicit retirement uses the same
// observed bounded transaction capability; no queue replacement or fabricated
// outcome can authorize hooks. This does not exercise providers or real Send.
type terminalContentionRowReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// One real snapshot also supports a valid legacy full owner with NO assessment.
// Empty arrays come from actual absence, never manufactured replacement rows.
// All complete rows are retained as PostgreSQL JSONB, preserving integer values.
func terminalContentionSnapshot(ctx context.Context, q terminalContentionRowReader, f strandedReviewFixture) (json.RawMessage, error) {
	var graph json.RawMessage
	err := q.QueryRow(ctx, `SELECT jsonb_build_object(
		'sessions',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM sessions r WHERE r.org_id=$1),'[]'::jsonb),
		'metadata',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_session_metadata r WHERE r.org_id=$1),'[]'::jsonb),
		'assessments',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_revision_assessments r WHERE r.org_id=$1),'[]'::jsonb),
		'schedule',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.pull_request_id) FROM code_review_pr_state r WHERE r.org_id=$1),'[]'::jsonb),
		'requests',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_requests r WHERE r.org_id=$1),'[]'::jsonb),
		'results',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_agent_results r WHERE r.org_id=$1),'[]'::jsonb),
		'findings',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_findings r WHERE r.org_id=$1),'[]'::jsonb),
		'prompts',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_prompt_records r WHERE r.org_id=$1),'[]'::jsonb),
		'jobs',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM jobs r WHERE r.org_id=$1),'[]'::jsonb),
		'threads',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM session_threads r WHERE r.org_id=$1),'[]'::jsonb),
		'messages',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM session_messages r WHERE r.org_id=$1),'[]'::jsonb),
		'inbox',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM thread_inbox_entries r WHERE r.org_id=$1),'[]'::jsonb),
		'stamps',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.owner_id) FROM caller_send_command_stamps r WHERE r.org_id=$1),'[]'::jsonb),
		'control',(SELECT to_jsonb(c) FROM caller_send_intent_control c WHERE c.singleton))`, f.job.OrgID).Scan(&graph)
	return graph, err
}

type terminalContentionAttempt struct {
	ctx         context.Context
	pid         int32
	clock       time.Time
	policy      [4]string
	hasDeadline bool
	remaining   time.Duration
}

// This observer delegates the entire actual unit to the existing capability.
// It neither answers application SQL nor changes policy, timing, retry causes,
// rows, callbacks or transaction settlement. Read fields only after poll joins.
type terminalContentionRun struct {
	base             db.CodeReviewMetadataTransactions
	attempts         chan terminalContentionAttempt
	calls, callbacks int
	final            error
}

func (r *terminalContentionRun) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	r.calls++
	r.final = r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		r.callbacks++
		attempt := terminalContentionAttempt{ctx: unitCtx}
		deadline, ok := unitCtx.Deadline()
		attempt.hasDeadline, attempt.remaining = ok, time.Until(deadline)
		if err := tx.QueryRow(unitCtx, `SELECT pg_backend_pid(),transaction_timestamp(),current_setting('transaction_isolation'),
			current_setting('lifecycle143.writer_version',true),current_setting('lock_timeout'),current_setting('statement_timeout')`).
			Scan(&attempt.pid, &attempt.clock, &attempt.policy[0], &attempt.policy[1], &attempt.policy[2], &attempt.policy[3]); err != nil {
			return err
		}
		select {
		case r.attempts <- attempt:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		return work(unitCtx, tx)
	})
	return r.final
}

func terminalContentionWait(t *testing.T, observer *pgxpool.Conn, holder int32, attempt terminalContentionAttempt, finished <-chan struct{}) {
	t.Helper()
	for {
		select {
		case <-finished:
			t.Fatal("actual poll ended before its exact metadata lock wait was independently observed")
		case <-attempt.ctx.Done():
			t.Fatal("unchanged bounded owner expired before its exact metadata lock wait was independently observed")
		default:
		}
		var waiting bool
		err := observer.QueryRow(attempt.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a
			WHERE a.datname=current_database() AND a.pid=$1::integer AND a.xact_start=$2::timestamptz
			AND a.state='active' AND a.wait_event_type='Lock' AND $3::integer=ANY(pg_blocking_pids(a.pid))
			AND position('code_review_session_metadata' IN a.query)>0)`, attempt.pid, attempt.clock, holder).Scan(&waiting)
		require.NoError(t, err, "warmed independent observer must read exact PID, transaction identity and real metadata blocker without inferring contention from duration")
		if waiting {
			return
		}
	}
}

func terminalContentionFixture(t *testing.T, pool *pgxpool.Pool, legacy bool) (strandedReviewFixture, string) {
	t.Helper()
	f := seedStrandedReview(t, pool, "idle")
	ctx := f.ctx()
	thread := uuid.New()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`, nil},
		{`INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,completed_at)
			VALUES($1,$2,$3,'claude_code','Synthetic retained terminal lane','completed',statement_timestamp())`, []any{thread, f.job.OrgID, f.job.SessionID}},
		{`INSERT INTO code_review_findings(org_id,session_id,dedupe_key,severity,confidence,path,summary,body)
			VALUES($1,$2,'synthetic-contention-finding','info','high','synthetic.go','Synthetic retained finding','Synthetic retained evidence')`, []any{f.job.OrgID, f.job.SessionID}},
		{`INSERT INTO code_review_prompt_records(org_id,session_id,record_key,role,content)
			VALUES($1,$2,'synthetic-contention-prompt','reviewer','Synthetic retained prompt')`, []any{f.job.OrgID, f.job.SessionID}},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed only isolated synthetic terminal-owner, drained lane and retained evidence prerequisites")
	}
	seedSavedReviewResults(t, pool, f)
	// Frozen helpers exercise actual Prepare, claim and settlement storage,
	// not a semantic command codec, provider acceptance or real Send.
	for _, state := range []string{"pending", "retry pending", "attempting", "unknown", "attention", "accepted"} {
		fullAssessmentDispatchChild(t, ctx, pool, f, thread, state)
	}
	if legacy {
		tag, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
		require.NoError(t, err, "create actual no-assessment legacy full ownership without corrupting the complete metadata/payload identity")
		require.Equal(t, int64(1), tag.RowsAffected(), "the legacy variant must genuinely remove only its original synthetic assessment")
	}
	node := "synthetic-terminal-contention-" + uuid.NewString()
	_, err := pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
	require.NoError(t, err, "register only the synthetic actual queue consumer")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='pending',lock_token=NULL,attempts=0,max_attempts=1,priority=100000,
		target_node_id=$3,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, node)
	require.NoError(t, err, "make exactly the original controller claimable by the actual worker at its finite final attempt")
	return f, node
}

func TestCodeReviewTerminalContentionPreservesOwnerPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		legacy   bool
		terminal func() error
	}{
		{"fatal full assessment", false, func() error { return &FatalError{Err: errors.New("synthetic permanent controller failure")} }},
		{"exhausted full assessment", false, func() error {
			return &RetryableError{Err: errors.New("synthetic exhausted controller failure"), ConsumeAttempt: true}
		}},
		{"fatal valid legacy owner without assessment", true, func() error { return &FatalError{Err: errors.New("synthetic permanent legacy controller failure")} }},
		{"exhausted valid legacy owner without assessment", true, func() error {
			return &RetryableError{Err: errors.New("synthetic exhausted legacy controller failure"), ConsumeAttempt: true}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Every case owns a separate full-schema database; root reserves aggregate capacity with -parallel=1.
			pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 3)
			require.Equal(t, int32(3), pool.Config().MaxConns, "actual metadata holder, worker and independent observer must fit exactly three pool slots")
			f, node := terminalContentionFixture(t, pool, tt.legacy)
			parent, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			observed := &terminalContentionRun{base: f.stores.ThreadSendDependencies.Transactions, attempts: make(chan terminalContentionAttempt, 3)}
			aware, err := db.NewCodeReviewStoreWithTerminalTransactions(pool, observed)
			require.NoError(t, err, "the real full hook must receive the actual explicit bounded transaction capability, not fail from an absent-capability fixture")
			f.stores.CodeReviews = aware
			observer, err := pool.Acquire(parent)
			require.NoError(t, err, "reserve and warm the independent observer before the actual poll begins")
			t.Cleanup(observer.Release)
			var observerPID int32
			require.NoError(t, observer.QueryRow(parent, `SELECT pg_backend_pid()`).Scan(&observerPID), "warm the third backend before either real contender consumes its bounded budget")
			holderCtx, stopHolder := context.WithTimeout(parent, db.LifecycleTransactionTimeout)
			defer stopHolder()
			holder, err := pool.BeginTx(holderCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			require.NoError(t, err, "the exact metadata holder must be an actual explicitly owned READ COMMITTED transaction")
			defer func() {
				cancel()
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), db.LifecycleTransactionTimeout)
				defer cleanupCancel()
				_ = holder.Rollback(cleanupCtx)
			}()
			require.NoError(t, db.ConfigureLifecycleTransactionInTx(holderCtx, holder), "retain the unchanged lifecycle marker, statement and lock budgets for the explicit metadata holder")
			var metadata uuid.UUID
			var holderPID int32
			require.NoError(t, holder.QueryRow(holderCtx, `SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR SHARE`, f.job.OrgID, f.job.SessionID, f.job.MetadataID).Scan(&metadata), "hold the actual exact metadata row before the worker can retire its original controller")
			require.Equal(t, f.job.MetadataID, metadata, "held metadata must be the independently supplied valid full owner, not a synthetic absent or foreign row")
			require.NoError(t, holder.QueryRow(holderCtx, `SELECT pg_backend_pid()`).Scan(&holderPID), "identify the exact real metadata blocker before retirement starts")
			require.NotEqual(t, observerPID, holderPID, "metadata holder and warmed lock observer must own independent actual backends")
			var logs bytes.Buffer
			logger := zerolog.New(&logs)
			terminalJobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, observed)
			require.NoError(t, err, "atomic retirement must receive the same observed actual bounded unit, never a fabricated result")
			w := New(pool, logger, node, WithCodeReviewTerminalStore(terminalJobs))
			// Keep the actual Worker.New queue owner and unchanged lease,
			// renewal, retry and whole-unit budgets.
			var hooks atomic.Int32
			type handlerBaseline struct {
				graph json.RawMessage
				err   error
			}
			claimed := make(chan handlerBaseline, 1)
			w.Register(models.JobTypeRunCodeReview, func(handlerCtx context.Context, _ string, _ json.RawMessage) error {
				// The real claim has already committed. Snapshot its real token,
				// expiry, owner and attempt fields rather than guessing a before row.
				graph, err := terminalContentionSnapshot(handlerCtx, pool, f)
				claimed <- handlerBaseline{graph: graph, err: err}
				if err != nil {
					return &FatalError{Err: err}
				}
				jobctx.RegisterDeadLetterHook(handlerCtx, func(context.Context, error) { hooks.Add(1) })
				registerCodeReviewDeadLetterReconciliation(handlerCtx, f.stores, nil, logger, f.job)
				return tt.terminal()
			})
			finished := make(chan struct{})
			go func() { defer close(finished); w.poll(parent) }()
			t.Cleanup(func() { cancel(); <-finished })
			var baseline handlerBaseline
			select {
			case baseline = <-claimed:
			case <-finished:
				t.Fatal("real poll did not reach its original claimed controller handler")
			}
			require.NoError(t, baseline.err, "the OLD-LOSS baseline must capture actual claimed durable state before any retirement or cleanup, not fail during fixture setup")
			for i := 0; i < 3; i++ {
				var attempt terminalContentionAttempt
				select {
				case attempt = <-observed.attempts:
				case <-finished:
					t.Fatal("actual poll ended before all unchanged bounded metadata lock attempts were reached")
				}
				require.NotEqual(t, holderPID, attempt.pid, "real terminal work cannot use its own metadata holder as a counterfeit competing backend")
				require.NotEqual(t, observerPID, attempt.pid, "the independent warmed observer cannot answer its own terminal SQL")
				require.Equal(t, [4]string{"read committed", "1", "250ms", "1500ms"}, attempt.policy, "every actual attempt must retain the real isolation, marker and original production lock/statement budgets")
				require.True(t, attempt.hasDeadline, "every observed attempt must retain the real whole-unit deadline")
				require.Greater(t, attempt.remaining, time.Duration(0), "the actual attempt must begin with positive remaining original budget")
				require.LessOrEqual(t, attempt.remaining, db.LifecycleTransactionTimeout, "no observer or holder may enlarge the existing two-second owner budget")
				terminalContentionWait(t, observer, holderPID, attempt, finished)
			}
			<-finished
			require.Equal(t, 1, observed.calls, "the actual retirement metadata boundary must exhaust one explicitly supplied real Run, not three independent units or a fabricated refusal")
			require.Equal(t, 3, observed.callbacks, "the unchanged real lock budget and eligible confirmed-abort policy must exhaust its original three attempts")
			require.ErrorIs(t, observed.final, db.ErrLifecycleContention, "failure must come from actual exhausted lock contention rather than absent capability, invalid input, fake SQL or a canceled test")
			var cause *pgconn.PgError
			require.True(t, errors.As(observed.final, &cause), "the actual bounded failure must preserve PostgreSQL's real lock cause")
			require.Equal(t, "55P03", cause.Code, "the real exact metadata holder must produce PostgreSQL lock timeout, not a manufactured error or unrelated database failure")
			after, err := terminalContentionSnapshot(parent, observer, f)
			require.NoError(t, err, "read complete durable owner state independently while the actual metadata holder is still retained")
			type settlement struct {
				graph            json.RawMessage
				hooks            int32
				metadataWarnings int
			}
			expected := settlement{graph: baseline.graph}
			actual := settlement{graph: after, hooks: hooks.Load(), metadataWarnings: strings.Count(logs.String(), "failed to reconcile dead-lettered code review metadata")}
			require.Equal(t, expected, actual, "failed atomic retirement must preserve every claimed job/assessment/metadata/result/evidence/request/scheduler field and release no hook or dropped-metadata effect; OLD job-first CTE plus warning-only hook must fail this invariant")
		})
	}
}
