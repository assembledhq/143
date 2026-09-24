package db

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Applies the real runtime migration to isolated parent shapes, then races
// identical dispatches and proves job lease takeover cannot launch twice.
func TestCodeReviewRecheckDispatchPostgres(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL dispatch proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect disposable PostgreSQL")
	schema := "review_dispatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated schema")
	t.Cleanup(func() {
		_, cleanupErr := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "drop isolated schema")
		require.NoError(t, admin.Close(ctx), "close database connection")
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse pool DSN")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create concurrent test pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
CREATE TABLE organizations(id uuid PRIMARY KEY);
CREATE TABLE repositories(id uuid PRIMARY KEY,org_id uuid NOT NULL);
CREATE UNIQUE INDEX repo_org_id ON repositories(org_id,id);
CREATE TABLE pull_requests(id uuid PRIMARY KEY,org_id uuid NOT NULL);
CREATE UNIQUE INDEX pr_org_id ON pull_requests(org_id,id);
CREATE TABLE sessions(id uuid PRIMARY KEY,org_id uuid NOT NULL,origin text NOT NULL,status text NOT NULL DEFAULT 'idle',container_id text,sandbox_state text NOT NULL DEFAULT 'none',turn_holding_container boolean NOT NULL DEFAULT false,snapshot_key text,pending_snapshot_key text,automation_owner_generation_id uuid,current_turn int NOT NULL DEFAULT 0,started_at timestamptz,completed_at timestamptz,last_activity_at timestamptz,workspace_generation bigint NOT NULL DEFAULT 0,agent_session_id text,token_usage jsonb,model_used text,result_summary text,diff text,error text,failure_explanation text,failure_category text,failure_next_steps text,failure_retry_advised boolean NOT NULL DEFAULT false,base_commit_sha text,diff_collected_at timestamptz,diff_stats jsonb,diff_history jsonb NOT NULL DEFAULT '[]');
CREATE UNIQUE INDEX session_org_id ON sessions(org_id,id);
CREATE TABLE session_threads(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,current_turn int NOT NULL DEFAULT 0,status text NOT NULL DEFAULT 'idle',archived_at timestamptz,cancel_requested_at timestamptz,pending_message_count int NOT NULL DEFAULT 0,started_at timestamptz,completed_at timestamptz,last_activity_at timestamptz,agent_session_id text,result_summary text,diff text,failure_explanation text,failure_category text);
CREATE TABLE session_messages(id bigserial PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,thread_id uuid,user_id uuid,turn_number int NOT NULL,role text NOT NULL,content text NOT NULL,attachments text[],"references" jsonb,commands jsonb,token_usage jsonb,source text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE jobs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL,queue text NOT NULL,job_type text NOT NULL,payload jsonb NOT NULL,priority int NOT NULL DEFAULT 0,dedupe_key text,status text NOT NULL DEFAULT 'pending',max_attempts int NOT NULL DEFAULT 3,lock_token uuid,updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX jobs_active_dedupe ON jobs(queue,dedupe_key) WHERE status IN ('pending','running');
CREATE TABLE code_review_revision_assessments(id uuid PRIMARY KEY,org_id uuid NOT NULL,repository_id uuid NOT NULL,pull_request_id uuid NOT NULL,session_id uuid NOT NULL,review_scope text NOT NULL,status text NOT NULL,head_sha text NOT NULL DEFAULT 'head',code_digest text NOT NULL DEFAULT 'code',contract_digest text NOT NULL DEFAULT 'contract',input_digest text NOT NULL DEFAULT 'input',result_origin text,failure_detail text,completed_at timestamptz,updated_at timestamptz);
CREATE UNIQUE INDEX assessment_org_id ON code_review_revision_assessments(org_id,id);
CREATE TABLE code_review_session_metadata(org_id uuid NOT NULL,session_id uuid NOT NULL,pull_request_id uuid NOT NULL,status text NOT NULL);
CREATE TABLE preview_instances(org_id uuid,session_id uuid,preview_holding_container boolean);
CREATE TABLE thread_runtimes(org_id uuid,session_id uuid,status text);
CREATE TABLE session_executors(org_id uuid,session_id uuid,status text);
CREATE TABLE session_cancel_requests(org_id uuid NOT NULL,session_id uuid NOT NULL,requested_at timestamptz NOT NULL,delivered_at timestamptz);
CREATE TABLE session_publish_state(org_id uuid,session_id uuid,pr_creation_state text,pr_creation_error text,pr_push_state text,pr_push_error text,pr_push_error_code text,branch_creation_state text,branch_creation_error text,updated_at timestamptz);
CREATE TABLE thread_inbox_entries(id uuid NOT NULL DEFAULT gen_random_uuid(),org_id uuid NOT NULL,session_id uuid NOT NULL,thread_id uuid NOT NULL,sequence_no bigint NOT NULL,message_id bigint,client_message_id text,entry_type text NOT NULL,payload jsonb NOT NULL DEFAULT '{}',delivery_state text NOT NULL,delivery_attempts int NOT NULL DEFAULT 0,last_error text,owner_node_id text,runtime_id uuid,accepted_at timestamptz NOT NULL DEFAULT now(),delivered_at timestamptz,acked_at timestamptz,applied_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX inbox_client_id ON thread_inbox_entries(org_id,thread_id,client_message_id) WHERE client_message_id IS NOT NULL;
`)
	require.NoError(t, err, "create migration parent shapes")
	up, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000295_code_review_recheck_runtime.up.sql"))
	require.NoError(t, err, "read actual runtime migration")
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "apply runtime migration")
	org, repo, pr, session, thread, assessment := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seed := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO organizations VALUES($1)`, []any{org}},
		{`INSERT INTO repositories VALUES($1,$2)`, []any{repo, org}},
		{`INSERT INTO pull_requests VALUES($1,$2)`, []any{pr, org}},
		{`INSERT INTO sessions(id,org_id,origin) VALUES($1,$2,'code_review')`, []any{session, org}},
		{`INSERT INTO session_threads(id,org_id,session_id) VALUES($1,$2,$3)`, []any{thread, org, session}},
		{`INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,session_id,review_scope,status) VALUES($1,$2,$3,$4,$5,'evidence_only','running')`, []any{assessment, org, repo, pr, session}},
		{`INSERT INTO code_review_session_metadata VALUES($1,$2,$3,'completed')`, []any{org, session, pr}},
	}
	for _, row := range seed {
		_, err = pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed matching tenant and assessment")
	}
	store := NewCodeReviewRecheckStore(pool)
	_, err = pool.Exec(ctx, `UPDATE sessions SET token_usage='{"total_cost_usd":4}'::jsonb WHERE org_id=$1 AND id=$2`, org, session)
	require.NoError(t, err, "seed historical full-review usage")
	_, err = pool.Exec(ctx, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, org, session)
	require.NoError(t, err, "model full review before final session reconciliation")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin baseline ownership transaction")
	claimed, err := store.ClaimFullAssessmentOwner(ctx, tx, org, session, pr)
	require.NoError(t, err, "claim completed full assessment's conversation")
	require.True(t, claimed, "completed metadata and drained runtime should permit baseline claim")
	require.NoError(t, tx.Commit(ctx), "commit baseline conversation owner")
	_, err = pool.Exec(ctx, `UPDATE sessions SET status='idle' WHERE org_id=$1 AND id=$2`, org, session)
	require.NoError(t, err, "reconcile completed full review session")
	in := models.CodeReviewRecheckDispatchInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, AssessmentID: assessment, SessionID: session, ThreadID: thread, ExpectedTurn: 1, Prompt: "Check the new screenshot", ImageURLs: []string{"https://example.invalid/evidence.png"}}
	const contenders = 4
	var wg sync.WaitGroup
	type result struct {
		d      models.CodeReviewRecheckDispatch
		reused bool
		err    error
	}
	results := make(chan result, contenders)
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, reused, callErr := store.Dispatch(ctx, in)
			results <- result{d, reused, callErr}
		}()
	}
	wg.Wait()
	close(results)
	first := uuid.Nil
	created := 0
	for r := range results {
		require.NoError(t, r.err, "concurrent same-assessment dispatch must succeed")
		if !r.reused {
			created++
		}
		if first == uuid.Nil {
			first = r.d.JobID
		}
		require.Equal(t, first, r.d.JobID, "redelivery must retain one job")
	}
	require.Equal(t, 1, created, "only one dispatch may create the message and job")
	var messages, inbox, jobs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM session_messages WHERE org_id=$1 AND session_id=$2`, org, session).Scan(&messages), "count persisted messages")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2`, org, session).Scan(&inbox), "count persisted inbox entries")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1`, org).Scan(&jobs), "count persisted jobs")
	require.Equal(t, 1, messages, "one user message must be durable")
	require.Equal(t, 1, inbox, "one inbox entry must be durable")
	require.Equal(t, 1, jobs, "one continue job must be durable")
	var sessionStarted, threadStarted *time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT started_at FROM sessions WHERE org_id=$1 AND id=$2`, org, session).Scan(&sessionStarted), "read reused session start time")
	require.NoError(t, pool.QueryRow(ctx, `SELECT started_at FROM session_threads WHERE org_id=$1 AND id=$2`, org, thread).Scan(&threadStarted), "read reused thread start time")
	require.NotNil(t, sessionStarted, "reused session reaper clock must restart at dispatch")
	require.NotNil(t, threadStarted, "reused thread reaper clock must restart at dispatch")
	var ownerErr *models.SessionCodeReviewOwnedError
	require.ErrorAs(t, NewSessionStore(pool).RejectIfCodeReviewOwned(ctx, org, session), &ownerErr, "conversation must reject ordinary human messages")
	require.Equal(t, pr, ownerErr.PullRequestID, "owner error must identify the review PR")
	_, err = NewSessionStore(pool).PublishHydratedContainerID(ctx, org, session, "preview-container")
	require.ErrorIs(t, err, pgx.ErrNoRows, "a preview cannot publish a container into a review-owned conversation")
	_, err = pool.Exec(ctx, `INSERT INTO preview_instances(org_id,session_id,preview_holding_container) VALUES($1,$2,false)`, org, session)
	require.NoError(t, err, "seed unclaimed preview")
	_, err = pool.Exec(ctx, `UPDATE preview_instances SET preview_holding_container=true WHERE org_id=$1 AND session_id=$2`, org, session)
	require.Error(t, err, "a preview cannot claim the review-owned sandbox")
	_, err = pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id) VALUES($1,$2,$3)`, uuid.New(), org, session)
	require.Error(t, err, "a new human tab cannot enter a review-owned conversation")
	in.Prompt = "Changed prompt"
	_, _, err = store.Dispatch(ctx, in)
	require.ErrorIs(t, err, ErrCodeReviewRecheckFence, "same assessment cannot silently accept a changed prompt")
	d, err := store.Get(ctx, org, assessment)
	require.NoError(t, err, "load dispatch receipt")
	oldToken, newToken := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$2 WHERE id=$1`, d.JobID, oldToken)
	require.NoError(t, err, "claim queued job")
	owned, err := store.Claim(ctx, org, assessment, d.JobID, oldToken, session, thread, 1, d.MessageID)
	require.NoError(t, err, "claim exact dispatch")
	require.True(t, owned, "first job lease should own turn")
	_, err = pool.Exec(ctx, `INSERT INTO thread_runtimes(org_id,session_id,status) VALUES($1,$2,'live')`, org, session)
	require.NoError(t, err, "mark original runtime live before lease takeover")
	_, err = pool.Exec(ctx, `UPDATE jobs SET lock_token=$2 WHERE id=$1`, d.JobID, newToken)
	require.NoError(t, err, "simulate job lease takeover")
	owned, err = store.Claim(ctx, org, assessment, d.JobID, newToken, session, thread, 1, d.MessageID)
	require.NoError(t, err, "check reclaimed lease")
	require.False(t, owned, "lease takeover cannot launch while old runtime is live")
	_, err = pool.Exec(ctx, `DELETE FROM thread_runtimes WHERE org_id=$1 AND session_id=$2`, org, session)
	require.NoError(t, err, "record original runtime drain")
	owned, err = store.Claim(ctx, org, assessment, d.JobID, newToken, session, thread, 1, d.MessageID)
	require.NoError(t, err, "reclaim exact turn after runtime drain")
	require.True(t, owned, "crash after claim must recover without a duplicate message or job")
	_, err = store.Complete(ctx, models.CodeReviewRecheckTurnCompletion{OrgID: org, AssessmentID: assessment, SessionID: session, ThreadID: thread, JobID: d.JobID, LockToken: oldToken, ExpectedTurn: 1, SessionTurn: 1, Summary: "stale", Result: &models.SessionResult{}})
	require.ErrorIs(t, err, ErrCodeReviewRecheckFence, "old owner cannot complete after drained reclaim")
	_, err = pool.Exec(ctx, `INSERT INTO session_messages(org_id,session_id,thread_id,turn_number,role,content) VALUES($1,$2,$3,1,'user','human mutation')`, org, session, thread)
	require.Error(t, err, "review ownership must reject a raced human message")
	require.NoError(t, store.RecordAttemptUsage(ctx, org, assessment, d.JobID, newToken, newToken.String()+":primary", json.RawMessage(`{"input_tokens":10}`)), "persist measured provider attempt before completion")
	summary := "first recheck result"
	completedID, err := store.Complete(ctx, models.CodeReviewRecheckTurnCompletion{OrgID: org, AssessmentID: assessment, SessionID: session, ThreadID: thread, JobID: d.JobID, LockToken: newToken, ExpectedTurn: 1, SessionTurn: 1, Summary: summary, Result: &models.SessionResult{ResultSummary: &summary}, ProviderSessionID: "thread-provider", ParentAgentSessionID: "parent-provider", SnapshotKey: "thread-snapshot", TokenUsage: json.RawMessage(`{"input_tokens":10}`)})
	require.NoError(t, err, "exact lease should atomically persist assistant and terminal turn")
	completed, err := store.Get(ctx, org, assessment)
	require.NoError(t, err, "read committed completion receipt")
	require.Equal(t, models.CodeReviewRecheckDispatchCompleted, completed.Status, "receipt should become completed")
	require.Equal(t, &completedID, completed.ResultMessageID, "receipt should bind exact assistant message")
	require.JSONEq(t, `{"`+newToken.String()+`:primary":{"input_tokens":10}}`, string(completed.AttemptUsage), "attempt usage should remain per launch")
	var rootUsage, assistantUsage json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT token_usage FROM sessions WHERE org_id=$1 AND id=$2`, org, session).Scan(&rootUsage), "read legacy session usage")
	require.NoError(t, pool.QueryRow(ctx, `SELECT token_usage FROM session_messages WHERE org_id=$1 AND id=$2`, org, completedID).Scan(&assistantUsage), "read recheck turn usage")
	require.JSONEq(t, `{"total_cost_usd":4}`, string(rootUsage), "recheck must preserve historical full-review session cost")
	require.JSONEq(t, `{"input_tokens":10}`, string(assistantUsage), "recheck cost belongs to exact assistant turn")
	_, err = store.Complete(ctx, models.CodeReviewRecheckTurnCompletion{OrgID: org, AssessmentID: assessment, SessionID: session, ThreadID: thread, JobID: d.JobID, LockToken: newToken, ExpectedTurn: 1, SessionTurn: 1, Summary: "duplicate", Result: &models.SessionResult{}})
	require.ErrorIs(t, err, ErrCodeReviewRecheckFence, "completed receipt cannot append a duplicate assistant")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='completed' WHERE id=$1`, d.JobID)
	require.NoError(t, err, "finish first continue job")
	_, err = pool.Exec(ctx, `UPDATE jobs SET updated_at=now()-interval '60 days' WHERE id=$1`, d.JobID)
	require.NoError(t, err, "age referenced completed job")
	unrelatedJob := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,updated_at) VALUES($1,$2,'agent','noop','{}','completed',now()-interval '60 days')`, unrelatedJob, org)
	require.NoError(t, err, "seed unrelated expired job")
	_, err = pool.Exec(ctx, `ALTER FUNCTION delete_expired_completed_jobs(int) SET search_path = `+schema+`,public`)
	require.NoError(t, err, "scope retention function to isolated schema")
	var deleted int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT delete_expired_completed_jobs(30)`).Scan(&deleted), "run real job retention function")
	require.Equal(t, int64(1), deleted, "retention should delete only unrelated expired job")
	var protectedJobCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE id=$1`, d.JobID).Scan(&protectedJobCount), "check referenced job retention")
	require.Equal(t, 1, protectedJobCount, "dispatch receipt must retain exact job identity")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',result_origin='evidence_only' WHERE org_id=$1 AND id=$2`, org, assessment)
	require.NoError(t, err, "publish previous assessment before its checkpoint can authorize native resume")
	secondAssessment := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,session_id,review_scope,status) VALUES($1,$2,$3,$4,$5,'evidence_only','running')`, secondAssessment, org, repo, pr, session)
	require.NoError(t, err, "seed next assessment with unchanged code and contract")
	in.AssessmentID, in.ExpectedTurn, in.Prompt = secondAssessment, 2, "Check another captured screenshot"
	second, _, err := store.Dispatch(ctx, in)
	require.NoError(t, err, "dispatch next exact turn")
	secondToken := uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$2 WHERE id=$1`, second.JobID, secondToken)
	require.NoError(t, err, "lease next continuation job")
	owned, err = store.Claim(ctx, org, secondAssessment, second.JobID, secondToken, session, thread, 2, second.MessageID)
	require.NoError(t, err, "claim next exact turn")
	require.True(t, owned, "next job lease should own turn")
	provider, native, err := store.NativeResumeProvider(ctx, org, secondAssessment, second.JobID, secondToken)
	require.NoError(t, err, "read exact prior checkpoint provenance")
	require.True(t, native, "same code and contract with installed snapshot should permit native resume")
	require.Equal(t, "thread-provider", provider, "native resume must use thread provider rather than parent session provider")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='failed' WHERE org_id=$1 AND id=$2`, org, assessment)
	require.NoError(t, err, "model rejected previous recheck response")
	_, native, err = store.NativeResumeProvider(ctx, org, secondAssessment, second.JobID, secondToken)
	require.NoError(t, err, "check previous assessment completion fence")
	require.False(t, native, "a rejected previous response cannot authorize native resume")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed' WHERE org_id=$1 AND id=$2`, org, assessment)
	require.NoError(t, err, "restore published assessment for snapshot mismatch test")
	_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key='different-snapshot' WHERE org_id=$1 AND id=$2`, org, session)
	require.NoError(t, err, "simulate mismatched installed checkpoint")
	_, native, err = store.NativeResumeProvider(ctx, org, secondAssessment, second.JobID, secondToken)
	require.NoError(t, err, "check mismatched checkpoint")
	require.False(t, native, "a different installed snapshot must force reconstruction")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='dead_letter' WHERE org_id=$1 AND id=$2`, org, second.JobID)
	require.NoError(t, err, "model a bound continuation that dead-lettered without a receipt")
	terminal, err := store.FailTerminalJob(ctx, org, secondAssessment, "worker ended without receipt")
	require.NoError(t, err, "reconcile terminal bound job")
	require.True(t, terminal, "terminal bound job should close the exact dispatch")
	failedDispatch, err := store.Get(ctx, org, secondAssessment)
	require.NoError(t, err, "read terminal dispatch")
	require.Equal(t, models.CodeReviewRecheckDispatchFailed, failedDispatch.Status, "dead-lettered bound job cannot remain running")
	var failedTurn int
	require.NoError(t, pool.QueryRow(ctx, `SELECT current_turn FROM session_threads WHERE org_id=$1 AND id=$2`, org, thread).Scan(&failedTurn), "read consumed failed thread turn")
	require.Equal(t, 2, failedTurn, "failed exact turn must advance monotonically without removing its durable user message")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='failed' WHERE org_id=$1 AND id=$2`, org, secondAssessment)
	require.NoError(t, err, "settle second assessment before a new recheck")
	thirdAssessment := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,session_id,review_scope,status) VALUES($1,$2,$3,$4,$5,'evidence_only','running')`, thirdAssessment, org, repo, pr, session)
	require.NoError(t, err, "seed next recheck after a failed exact turn")
	in.AssessmentID, in.ExpectedTurn, in.Prompt = thirdAssessment, 3, "Recheck after the failed turn"
	third, _, err := store.Dispatch(ctx, in)
	require.NoError(t, err, "unique turn identity should permit next monotonic turn")
	thirdToken := uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$2 WHERE org_id=$1 AND id=$3`, org, thirdToken, third.JobID)
	require.NoError(t, err, "lease cancellable recheck turn")
	owned, err = store.Claim(ctx, org, thirdAssessment, third.JobID, thirdToken, session, thread, 3, third.MessageID)
	require.NoError(t, err, "claim cancellable turn")
	require.True(t, owned, "exact cancellation needs a valid job lease")
	require.NoError(t, store.Cancel(ctx, org, thirdAssessment, third.JobID, thirdToken), "cancel exact turn and assessment without fallback")
	cancelledDispatch, err := store.Get(ctx, org, thirdAssessment)
	require.NoError(t, err, "read cancelled receipt")
	require.Equal(t, models.CodeReviewRecheckDispatchCancelled, cancelledDispatch.Status, "cancelled turn must not be marked failed")
	var cancelledAssessmentStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, org, thirdAssessment).Scan(&cancelledAssessmentStatus), "read cancelled assessment")
	require.Equal(t, "cancelled", cancelledAssessmentStatus, "user cancellation must settle assessment without forced full fallback")
	fourthAssessment := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,session_id,review_scope,status) VALUES($1,$2,$3,$4,$5,'evidence_only','running')`, fourthAssessment, org, repo, pr, session)
	require.NoError(t, err, "seed pre-claim cancellation assessment")
	in.AssessmentID, in.ExpectedTurn, in.Prompt = fourthAssessment, 4, "Cancel before provider claim"
	fourth, _, err := store.Dispatch(ctx, in)
	require.NoError(t, err, "dispatch cancellable queued turn")
	fourthToken := uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$2 WHERE org_id=$1 AND id=$3`, org, fourthToken, fourth.JobID)
	require.NoError(t, err, "lease queued turn before user cancellation")
	_, err = pool.Exec(ctx, `UPDATE session_threads SET cancel_requested_at=now() WHERE org_id=$1 AND id=$2`, org, thread)
	require.NoError(t, err, "persist user cancellation before claim")
	owned, err = store.Claim(ctx, org, fourthAssessment, fourth.JobID, fourthToken, session, thread, 4, fourth.MessageID)
	require.ErrorIs(t, err, ErrCodeReviewRecheckUserCancelled, "pre-claim user cancellation must be terminal without provider launch")
	require.False(t, owned, "cancelled queued turn cannot own provider launch")
	fourthStatus, err := store.Get(ctx, org, fourthAssessment)
	require.NoError(t, err, "read pre-claim cancellation receipt")
	require.Equal(t, models.CodeReviewRecheckDispatchCancelled, fourthStatus.Status, "pre-claim cancellation must not fail into full fallback")
	_, err = pool.Exec(ctx, `UPDATE session_threads SET cancel_requested_at=NULL WHERE org_id=$1 AND id=$2`, org, thread)
	require.NoError(t, err, "clear old per-turn cancel marker before next assessment")
	fifthAssessment := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,session_id,review_scope,status) VALUES($1,$2,$3,$4,$5,'evidence_only','running')`, fifthAssessment, org, repo, pr, session)
	require.NoError(t, err, "seed terminal queued-job cancellation assessment")
	in.AssessmentID, in.ExpectedTurn, in.Prompt = fifthAssessment, 5, "Cancel queued job before claim"
	fifth, _, err := store.Dispatch(ctx, in)
	require.NoError(t, err, "dispatch last queued turn")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='cancelled' WHERE org_id=$1 AND id=$2`, org, fifth.JobID)
	require.NoError(t, err, "model queue cancellation before claim")
	_, err = pool.Exec(ctx, `INSERT INTO session_cancel_requests(org_id,session_id,requested_at) VALUES($1,$2,now())`, org, session)
	require.NoError(t, err, "persist session-wide user cancellation")
	terminal, err = store.FailTerminalJob(ctx, org, fifthAssessment, "bound job ended")
	require.NoError(t, err, "reconcile cancelled bound job")
	require.True(t, terminal, "terminal cancellation should settle exact dispatch")
	fifthStatus, err := store.Get(ctx, org, fifthAssessment)
	require.NoError(t, err, "read terminal cancellation receipt")
	require.Equal(t, models.CodeReviewRecheckDispatchCancelled, fifthStatus.Status, "cancelled queue job with durable user request cannot force full review")
}
