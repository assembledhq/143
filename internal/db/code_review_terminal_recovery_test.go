package db

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestReconcileTerminalReviewsPostgres(t *testing.T) {
	t.Parallel()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for terminal review recovery proof")
	}
	tests := []struct {
		name        string
		setup       string
		assessment  string
		thread      string
		request     string
		keepPointer bool
		foreignOrg  bool
		foreignPR   bool
	}{
		{name: "superseded assessment and orphan thread", assessment: "superseded", thread: "cancelled", request: "failed"},
		{name: "failed metadata", setup: "UPDATE code_review_session_metadata SET status='failed'", assessment: "failed", thread: "cancelled", request: "failed"},
		{name: "cancelled metadata", setup: "UPDATE code_review_session_metadata SET status='cancelled'", assessment: "cancelled", thread: "cancelled", request: "failed"},
		{name: "reserved assessment", setup: "UPDATE code_review_revision_assessments SET status='reserved'", assessment: "superseded", thread: "cancelled", request: "failed"},
		{name: "unsent reserved publication", setup: "UPDATE code_review_revision_assessments SET status='publishing',publication_state='reserved'", assessment: "superseded", thread: "cancelled", request: "failed"},
		{name: "uncertain publication remains blocked", setup: "UPDATE code_review_revision_assessments SET status='publishing',publication_state='uncertain'", assessment: "publishing", thread: "cancelled", request: "joined", keepPointer: true},
		{name: "confirmed publication remains blocked", setup: "UPDATE code_review_revision_assessments SET status='publishing',publication_state='confirmed'", assessment: "publishing", thread: "cancelled", request: "joined", keepPointer: true},
		{name: "publication receipt cannot be discarded", setup: "UPDATE code_review_revision_assessments SET publication_receipt='{}'", assessment: "running", thread: "cancelled", request: "joined", keepPointer: true},
		{name: "github review cannot be discarded", setup: "UPDATE code_review_revision_assessments SET github_review_id=123", assessment: "running", thread: "cancelled", request: "joined", keepPointer: true},
		{name: "evidence continuation remains independent", setup: "UPDATE code_review_revision_assessments SET review_scope='evidence_only'", assessment: "running", thread: "cancelled", request: "joined", keepPointer: true},
		{name: "live metadata untouched", setup: "UPDATE code_review_session_metadata SET status='running'", assessment: "running", thread: "running", request: "joined", keepPointer: true},
		{name: "completed metadata untouched", setup: "UPDATE code_review_session_metadata SET status='completed'", assessment: "running", thread: "running", request: "joined", keepPointer: true},
		{name: "live executor protects thread", setup: "UPDATE session_executors SET status='running'", assessment: "superseded", thread: "running", request: "failed"},
		{name: "starting executor protects thread", setup: "UPDATE session_executors SET status='starting'", assessment: "superseded", thread: "running", request: "failed"},
		{name: "draining executor protects thread", setup: "UPDATE session_executors SET status='draining'", assessment: "superseded", thread: "running", request: "failed"},
		{name: "no terminal executor proof", setup: "DELETE FROM session_executors", assessment: "superseded", thread: "running", request: "failed"},
		{name: "executor job still running", setup: "UPDATE jobs SET status='running'", assessment: "superseded", thread: "running", request: "failed"},
		{name: "queued retry protects thread", setup: "UPDATE jobs SET status='pending'", assessment: "superseded", thread: "running", request: "failed"},
		{name: "live runtime protects thread", setup: "INSERT INTO thread_runtimes SELECT org_id,session_id,'live' FROM session_threads", assessment: "superseded", thread: "running", request: "failed"},
		{name: "paused runtime protects thread", setup: "INSERT INTO thread_runtimes SELECT org_id,session_id,'paused' FROM session_threads", assessment: "superseded", thread: "running", request: "failed"},
		{name: "active recheck protects thread", setup: "INSERT INTO code_review_recheck_dispatches SELECT org_id,session_id,'running' FROM session_threads", assessment: "superseded", thread: "running", request: "failed"},
		{name: "new continuation protects thread", setup: "INSERT INTO jobs SELECT gen_random_uuid(),org_id,'pending','continue_session',payload FROM jobs", assessment: "superseded", thread: "running", request: "failed"},
		{name: "cancellation is required", setup: "UPDATE session_threads SET cancel_requested_at=NULL", assessment: "superseded", thread: "running", request: "failed"},
		{name: "failed synthesis without cancellation", setup: "UPDATE code_review_session_metadata SET status='failed'; UPDATE session_threads SET cancel_requested_at=NULL; INSERT INTO thread_runtimes SELECT org_id,session_id,'failed' FROM session_threads", assessment: "failed", thread: "failed", request: "failed"},
		{name: "lost reviewer without cancellation", setup: "UPDATE code_review_session_metadata SET status='failed'; UPDATE session_threads SET cancel_requested_at=NULL; UPDATE session_executors SET status='lost'", assessment: "failed", thread: "failed", request: "failed"},
		{name: "lost executor with pending recovery remains blocked", setup: "UPDATE code_review_session_metadata SET status='failed'; UPDATE session_threads SET cancel_requested_at=NULL; UPDATE session_executors SET status='lost'; UPDATE jobs SET status='pending'", assessment: "failed", thread: "running", request: "failed"},
		{name: "failed controller cannot retire live runtime", setup: "UPDATE code_review_session_metadata SET status='failed'; UPDATE session_threads SET cancel_requested_at=NULL; INSERT INTO thread_runtimes SELECT org_id,session_id,'live' FROM session_threads", assessment: "failed", thread: "running", request: "failed"},
		{name: "failed controller cannot retire active sibling executor", setup: "UPDATE code_review_session_metadata SET status='failed'; UPDATE session_threads SET cancel_requested_at=NULL; INSERT INTO session_executors SELECT org_id,session_id,gen_random_uuid(),job_id,'running' FROM session_executors", assessment: "failed", thread: "running", request: "failed"},
		{name: "failed controller without executor proof remains blocked", setup: "UPDATE code_review_session_metadata SET status='failed'; UPDATE session_threads SET cancel_requested_at=NULL; DELETE FROM session_executors", assessment: "failed", thread: "running", request: "failed"},
		{name: "other tenant untouched", assessment: "running", thread: "running", request: "joined", keepPointer: true, foreignOrg: true},
		{name: "other PR untouched", assessment: "running", thread: "running", request: "joined", keepPointer: true, foreignPR: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			conn, err := pgx.Connect(ctx, url)
			require.NoError(t, err, "connect disposable PostgreSQL")
			defer func() { require.NoError(t, conn.Close(ctx), "close PostgreSQL connection") }()
			schema := "review_recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			_, err = conn.Exec(ctx, "CREATE SCHEMA "+schema)
			require.NoError(t, err, "create isolated test schema")
			defer func() {
				_, err := conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
				require.NoError(t, err, "remove test schema")
			}()
			_, err = conn.Exec(ctx, "SET search_path TO "+schema+",public")
			require.NoError(t, err, "use isolated test schema")
			_, err = conn.Exec(ctx, `
CREATE TABLE code_review_session_metadata(id uuid,org_id uuid,pull_request_id uuid,session_id uuid,status text);
CREATE TABLE code_review_revision_assessments(id uuid,org_id uuid,pull_request_id uuid,metadata_id uuid,session_id uuid,review_scope text,status text,publication_state text,publication_receipt jsonb,github_review_id bigint,completed_at timestamptz,superseded_at timestamptz,failure_detail text);
CREATE TABLE code_review_requests(id uuid,org_id uuid,pull_request_id uuid,assessment_id uuid,status text);
CREATE TABLE session_threads(id uuid,org_id uuid,session_id uuid,status text,cancel_requested_at timestamptz,completed_at timestamptz,last_activity_at timestamptz);
CREATE TABLE session_executors(org_id uuid,session_id uuid,thread_id uuid,job_id uuid,status text);
CREATE TABLE jobs(id uuid,org_id uuid,status text,job_type text,payload jsonb);
CREATE TABLE thread_runtimes(org_id uuid,session_id uuid,status text);
CREATE TABLE code_review_recheck_dispatches(org_id uuid,session_id uuid,status text);`)
			require.NoError(t, err, "create recovery table shapes")
			org, pr, session, metadata, assessment, thread, job, request, pendingRequest := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			seed := []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO code_review_session_metadata VALUES($1,$2,$3,$4,'stale')`, []any{metadata, org, pr, session}},
				{`INSERT INTO code_review_revision_assessments(id,org_id,pull_request_id,metadata_id,session_id,review_scope,status,publication_state) VALUES($1,$2,$3,$4,$5,'full','running','not_started')`, []any{assessment, org, pr, metadata, session}},
				{`INSERT INTO code_review_requests VALUES($1,$2,$3,$4,'joined'),($5,$2,$3,NULL,'pending')`, []any{request, org, pr, assessment, pendingRequest}},
				{`INSERT INTO session_threads VALUES($1,$2,$3,'running',now()-interval '1 hour',NULL,now()-interval '2 hours')`, []any{thread, org, session}},
				{`INSERT INTO jobs VALUES($1,$2,'dead_letter','continue_session',jsonb_build_object('session_id',$3::text,'thread_id',$4::text))`, []any{job, org, session, thread}},
				{`INSERT INTO session_executors VALUES($1,$2,$3,$4,'failed')`, []any{org, session, thread, job}},
			}
			for _, row := range seed {
				_, err = conn.Exec(ctx, row.sql, row.args...)
				require.NoError(t, err, "seed terminal review with abandoned executor")
			}
			if tt.setup != "" {
				_, err = conn.Exec(ctx, tt.setup)
				require.NoError(t, err, "apply regression scenario")
			}
			state := models.CodeReviewPRState{ActiveAssessmentID: &assessment, ActiveSessionID: &session, PendingInput: json.RawMessage(`{"preserve":"new request"}`), PendingRequestID: &pendingRequest, State: models.CodeReviewScheduleWaiting}
			expected := state
			if !tt.keepPointer {
				expected.ActiveAssessmentID = nil
				expected.ActiveSessionID = nil
			}
			queryOrg, queryPR := org, pr
			if tt.foreignOrg {
				queryOrg = uuid.New()
			}
			if tt.foreignPR {
				queryPR = uuid.New()
			}
			tx, err := conn.Begin(ctx)
			require.NoError(t, err, "begin recovery transaction")
			defer func() { _ = tx.Rollback(ctx) }()
			require.NoError(t, reconcileTerminalReviews(ctx, tx, queryOrg, queryPR, &state), "recover terminal review blockers")
			require.NoError(t, reconcileTerminalReviews(ctx, tx, queryOrg, queryPR, &state), "recovery should be idempotent")
			require.NoError(t, tx.Commit(ctx), "commit recovery")
			require.Equal(t, expected, state, "recovery should release only the retired pointer and preserve the replacement request")
			var actual struct{ Assessment, Thread, Request, Pending string }
			err = conn.QueryRow(ctx, `SELECT a.status,t.status,r.status,p.status FROM code_review_revision_assessments a CROSS JOIN session_threads t JOIN code_review_requests r ON r.id=$1 JOIN code_review_requests p ON p.id=$2`, request, pendingRequest).Scan(&actual.Assessment, &actual.Thread, &actual.Request, &actual.Pending)
			require.NoError(t, err, "read recovered state")
			require.Equal(t, struct{ Assessment, Thread, Request, Pending string }{tt.assessment, tt.thread, tt.request, "pending"}, actual, "only provably terminal review state should be repaired")
		})
	}
}
