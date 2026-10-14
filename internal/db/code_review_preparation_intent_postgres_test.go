package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type preparationIntentPostgresFixture struct {
	orgID, repositoryID, pullRequestID, policyID, metadataID, threadID, jobID, token uuid.UUID
	session                                                                          models.Session
	thread                                                                           models.SessionThread
}

func seedPreparationIntentPostgres(t *testing.T, pool *pgxpool.Pool, scoped bool) preparationIntentPostgresFixture {
	t.Helper()
	ctx := context.Background()
	f := preparationIntentPostgresFixture{orgID: uuid.New(), repositoryID: uuid.New(), pullRequestID: uuid.New(), policyID: uuid.New(), metadataID: uuid.New(), threadID: uuid.New(), jobID: uuid.New(), token: uuid.New()}
	integrationID := uuid.New()
	f.session = models.Session{ID: uuid.New(), OrgID: f.orgID, Origin: models.SessionOriginCodeReview, Status: models.SessionStatusRunning, CurrentTurn: 2, WorkspaceGeneration: 3}
	f.thread = models.SessionThread{ID: f.threadID, OrgID: f.orgID, SessionID: f.session.ID, CurrentTurn: 4}
	payload := map[string]any{"org_id": f.orgID.String(), "session_id": f.session.ID.String(), "audit_marker": "preserve original payload"}
	if scoped {
		payload["thread_id"] = f.threadID.String()
	}
	rows := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO organizations(id,name) VALUES($1,$2)`, []any{f.orgID, "preparation-intent-" + f.orgID.String()}},
		{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integrationID, f.orgID}},
		{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0) & 9223372036854775807),'test/intents','https://example.invalid/intents.git',1)`, []any{f.repositoryID, f.orgID, integrationID}},
		{`INSERT INTO sessions(id,org_id,origin,status,current_turn,workspace_generation,repository_id) VALUES($1,$2,'code_review','running',2,3,$3)`, []any{f.session.ID, f.orgID, f.repositoryID}},
		{`INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,current_turn) VALUES($1,$2,$3,'claude','Reviewer','running',4)`, []any{f.threadID, f.orgID, f.session.ID}},
		{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,1,'https://example.invalid/pr/1','test/intents','Synthetic preparation','head123','base123')`, []any{f.pullRequestID, f.orgID}},
		{`INSERT INTO code_review_policies(id,org_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,1,'approve_acceptable','{}','{}','{"reviewers":["claude_code"],"orchestrator":"claude_code","timeout_seconds":60}','','','{}')`, []any{f.policyID, f.orgID}},
		{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($1,$2,$3,$4,$5,$6,'base123','head123','app_reviewer','running',$7)`, []any{f.metadataID, f.orgID, f.session.ID, f.repositoryID, f.pullRequestID, f.policyID, "intent-review:" + f.session.ID.String()}},
		{`INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token,lease_expires_at,locked_at) VALUES($1,$2,'agent','continue_session',$3,'running',$4,now()+interval '5 minutes',now())`, []any{f.jobID, f.orgID, payload, f.token}},
	}
	for _, row := range rows {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed a separate tenant and real migrated preparation relationship")
	}
	return f
}

func capturePreparationIntentPostgres(t *testing.T, pool *pgxpool.Pool, f preparationIntentPostgresFixture, scoped bool) models.CodeReviewPreparationIntent {
	t.Helper()
	var thread *models.SessionThread
	if scoped {
		thread = &f.thread
	}
	i, err := NewCodeReviewStore(pool).CaptureCodeReviewPreparationIntent(context.Background(), f.orgID, f.jobID, f.token, f.session, thread)
	require.NoError(t, err, "capture immutable review identity before any preparation work")
	i.Stage, i.FailureClass = models.CodeReviewPreparationStageDependencySetup, models.CodeReviewPreparationFailureSetup
	return i
}

func checkpointPreparationIntentPostgres(t *testing.T, pool *pgxpool.Pool, f preparationIntentPostgresFixture, scoped bool) models.CodeReviewPreparationIntent {
	t.Helper()
	i := capturePreparationIntentPostgres(t, pool, f, scoped)
	owned, err := NewJobStore(pool).DeadLetterContinueSessionWithPreparationIntentWithLease(context.Background(), f.orgID, f.jobID, f.token, i)
	require.NoError(t, err, "atomically retire the owned continuation and checkpoint its preparation failure")
	require.True(t, owned, "the original lease must own its terminal checkpoint")
	return i
}

type preparationIntentDurableState struct {
	JobStatus                         string
	LeaseCleared, Completed, Consumed bool
	Payload                           json.RawMessage
	MetadataStatus                    string
	FailureReason                     *string
	ThreadStatus                      string
	ThreadTurn                        int
}

func readPreparationIntentState(t *testing.T, pool *pgxpool.Pool, f preparationIntentPostgresFixture) preparationIntentDurableState {
	t.Helper()
	var state preparationIntentDurableState
	err := pool.QueryRow(context.Background(), `SELECT j.status,j.lock_token IS NULL AND j.lease_expires_at IS NULL,j.completed_at IS NOT NULL,COALESCE((j.payload->>'terminal_preparation_intent_consumed')::boolean,false),j.payload,m.status,m.failure_reason,t.status,t.current_turn
 FROM jobs j JOIN code_review_session_metadata m ON m.org_id=j.org_id AND m.id=$3 JOIN session_threads t ON t.org_id=j.org_id AND t.id=$4
 WHERE j.org_id=$1 AND j.id=$2`, f.orgID, f.jobID, f.metadataID, f.threadID).
		Scan(&state.JobStatus, &state.LeaseCleared, &state.Completed, &state.Consumed, &state.Payload, &state.MetadataStatus, &state.FailureReason, &state.ThreadStatus, &state.ThreadTurn)
	require.NoError(t, err, "read the actual terminal job, operational review, and untouched reviewer state")
	return state
}

func seedPreparationIntentAssessment(t *testing.T, pool *pgxpool.Pool, f preparationIntentPostgresFixture, publication, receipt, origin string) {
	t.Helper()
	capture := testAssessmentCapture()
	capture.OrgID, capture.RepositoryID, capture.PullRequestID, capture.PolicyID, capture.SessionID = f.orgID, f.repositoryID, f.pullRequestID, f.policyID, f.session.ID
	capture.BaseSHA, capture.HeadSHA = "base123", "head123"
	a, _, err := NewCodeReviewAssessmentStore(pool).Create(context.Background(), capture)
	require.NoError(t, err, "create a real composite-FK assessment for publication fencing")
	var receiptValue, originValue any
	if receipt != "" {
		receiptValue = json.RawMessage(receipt)
	}
	if origin != "" {
		originValue = origin
	}
	_, err = pool.Exec(context.Background(), `UPDATE code_review_revision_assessments SET publication_state=$3,publication_receipt=$4,result_origin=$5 WHERE org_id=$1 AND id=$2`, f.orgID, a.ID, publication, receiptValue, originValue)
	require.NoError(t, err, "retain the competing publication or staged-result ownership")
}

func seedPreparationReplacement(t *testing.T, pool *pgxpool.Pool, f preparationIntentPostgresFixture, threadID *uuid.UUID, status string, live bool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	id, token := uuid.New(), uuid.New()
	payload := map[string]any{"session_id": f.session.ID.String(), "org_id": f.orgID.String()}
	if threadID != nil {
		payload["thread_id"] = threadID.String()
	}
	lease := time.Now().UTC().Add(-time.Minute)
	if live {
		lease = time.Now().UTC().Add(5 * time.Minute)
	}
	_, err := pool.Exec(context.Background(), `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token,lease_expires_at,created_at) VALUES($1,$2,'agent','continue_session',$3,$4,$5,$6,now()+interval '1 second')`, id, f.orgID, payload, status, token, lease)
	require.NoError(t, err, "seed a newer continuation with explicit durable ownership")
	return id, token
}

type preparationIntentCorrectionDB struct {
	DBTX
	onClose func()
}

func (d preparationIntentCorrectionDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := d.DBTX.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &preparationIntentCorrectionRows{Rows: rows, onClose: d.onClose}, nil
}

type preparationIntentCorrectionRows struct {
	pgx.Rows
	onClose func()
}

func (r *preparationIntentCorrectionRows) Close() {
	r.Rows.Close()
	if r.onClose != nil {
		callback := r.onClose
		r.onClose = nil
		callback()
	}
}

func seedPreparationIntentOwner(t *testing.T, pool *pgxpool.Pool, f preparationIntentPostgresFixture, kind, lease string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var expiry any
	switch lease {
	case "future":
		expiry = time.Now().UTC().Add(5 * time.Minute)
	case "expired":
		expiry = time.Now().UTC().Add(-time.Minute)
	}
	if kind == "executor" {
		nodeID := "cutoff-owner-" + uuid.NewString()
		_, err := pool.Exec(context.Background(), `INSERT INTO nodes(id,host,mode,status) VALUES($1,'example.invalid','worker','active')`, nodeID)
		require.NoError(t, err, "seed a separate test-only executor host")
		_, err = pool.Exec(context.Background(), `INSERT INTO session_executors(id,org_id,session_id,thread_id,job_id,job_type,host_node_id,owner_id,lock_token,status,heartbeat_at,lease_expires_at) VALUES($1,$2,$3,$4,$5,'continue_session',$6,'cutoff-owner',$7,'draining',clock_timestamp(),$8)`, id, f.orgID, f.session.ID, f.threadID, f.jobID, nodeID, f.token, expiry)
		require.NoError(t, err, "retain an executor owner without assuming lease expiry permits reaping")
	} else {
		_, err := pool.Exec(context.Background(), `INSERT INTO thread_runtimes(id,org_id,session_id,thread_id,agent_type,owner_node_id,lease_token,status,heartbeat_at,lease_expires_at) VALUES($1,$2,$3,$4,'claude_code','test-owner',$5,'live',clock_timestamp(),$6)`, id, f.orgID, f.session.ID, f.threadID, f.token, expiry)
		require.NoError(t, err, "retain a runtime owner without changing renewal or reaper policy")
	}
	return id
}
func readPreparationIntentOwner(t *testing.T, pool *pgxpool.Pool, f preparationIntentPostgresFixture, kind string, id uuid.UUID) json.RawMessage {
	t.Helper()
	query := `SELECT to_jsonb(e) FROM session_executors e WHERE org_id=$1 AND id=$2`
	if kind == "runtime" {
		query = `SELECT to_jsonb(r) FROM thread_runtimes r WHERE org_id=$1 AND id=$2`
	}
	var row json.RawMessage
	require.NoError(t, pool.QueryRow(context.Background(), query, f.orgID, id).Scan(&row), "read every field of the exact owner row before and after deadline projection")
	return row
}
func setPreparationReviewCutoff(t *testing.T, pool *pgxpool.Pool, f preparationIntentPostgresFixture, expired bool) {
	t.Helper()
	created := time.Now().UTC()
	if expired {
		created = created.Add(-time.Hour)
	}
	_, err := pool.Exec(context.Background(), `UPDATE code_review_session_metadata SET created_at=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.metadataID, created)
	require.NoError(t, err, "set the original captured review clock without changing its policy or workspace identity")
}

// One parent applies the full migration chain once. Parallel cases have distinct
// tenants; the lock choreography cases run serially because they intentionally
// hold several connections from this one bounded pool.
func TestCodeReviewPreparationIntentPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	_, err := pool.Exec(context.Background(), `CREATE FUNCTION preparation_intent_test_reject_write() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF NEW.payload ? 'terminal_preparation_intent_consumed' AND OLD.payload->>'test_reject_intent_consumption'='true' THEN RAISE EXCEPTION 'injected preparation intent consumption failure'; END IF;
 IF NEW.status='dead_letter' AND OLD.status='running' AND OLD.payload->>'test_reject_terminal_checkpoint'='true' THEN RAISE EXCEPTION 'injected preparation terminal checkpoint failure'; END IF;
 RETURN NEW;
 END $$;
 CREATE TRIGGER preparation_intent_test_reject_write BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION preparation_intent_test_reject_write()`)
	require.NoError(t, err, "install row-local failure injection only inside the disposable migrated test database")

	t.Run("terminal atomicity", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name, fence     string
			succeeds, fails bool
		}{
			{name: "owned token", succeeds: true}, {name: "rotated token", fence: "token"},
			{name: "other tenant", fence: "org"}, {name: "different payload session", fence: "session"},
			{name: "different payload thread", fence: "thread"}, {name: "recheck payload", fence: "recheck"},
			{name: "database refuses checkpoint", fence: "storage", fails: true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				f := seedPreparationIntentPostgres(t, pool, true)
				i := capturePreparationIntentPostgres(t, pool, f, true)
				orgID, token := f.orgID, f.token
				switch tt.fence {
				case "token":
					token = uuid.New()
				case "org":
					orgID = uuid.New()
					// Keep the envelope consistent to reach the SQL tenant fence;
					// mismatched call/envelope identity is covered by the unit test.
					i.OrgID = orgID
				case "session":
					_, err := pool.Exec(context.Background(), `UPDATE jobs SET payload=jsonb_set(payload,'{session_id}',to_jsonb($3::text)) WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID, uuid.NewString())
					require.NoError(t, err, "simulate a different containing session without changing captured provenance")
				case "thread":
					_, err := pool.Exec(context.Background(), `UPDATE jobs SET payload=jsonb_set(payload,'{thread_id}',to_jsonb($3::text)) WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID, uuid.NewString())
					require.NoError(t, err, "retain the containing payload thread mismatch")
				case "recheck":
					_, err := pool.Exec(context.Background(), `UPDATE jobs SET payload=payload || jsonb_build_object('code_review_assessment_id',$3::text) WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID, uuid.NewString())
					require.NoError(t, err, "leave evidence recheck terminal authority with its assessment")
				case "storage":
					_, err := pool.Exec(context.Background(), `UPDATE jobs SET payload=payload || '{"test_reject_terminal_checkpoint":true}' WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID)
					require.NoError(t, err, "arm only this job's atomic write failure")
				}
				before := readPreparationIntentState(t, pool, f)
				owned, err := NewJobStore(pool).DeadLetterContinueSessionWithPreparationIntentWithLease(context.Background(), orgID, f.jobID, token, i)
				if tt.fails {
					require.Error(t, err, "an injected storage failure must reject the entire terminal statement")
				} else {
					require.NoError(t, err, "ownership fences must reject with no affected job rather than a partial write")
				}
				require.Equal(t, tt.succeeds, owned, "only the exact owned continuation may retire with this preparation intent")
				after := readPreparationIntentState(t, pool, f)
				if !tt.succeeds {
					require.Equal(t, before, after, "lost ownership or failed SQL must change neither terminal state nor intent payload")
					return
				}
				require.Equal(t, "dead_letter", after.JobStatus, "terminal checkpoint must make the preparation job unrunnable")
				require.True(t, after.LeaseCleared && after.Completed && !after.Consumed, "terminal state and pending intent must commit with lease retirement")
				var payload map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(after.Payload, &payload), "read durable original payload and safe terminal intent")
				require.JSONEq(t, `"preserve original payload"`, string(payload["audit_marker"]), "terminal checkpoint must preserve unrelated original payload members")
				actual, err := models.DecodeCodeReviewPreparationIntent(payload[models.CodeReviewPreparationIntentKey])
				require.NoError(t, err, "stored intent must remain strict schema-valid provenance")
				require.Equal(t, i, actual, "terminal write must not replace observed generation with current mutable state")
			})
		}
	})

	t.Run("projection fences", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name, fence         string
			inferred, projected bool
		}{
			{name: "exact explicit attempt", projected: true}, {name: "inferred null projects metadata only", inferred: true, projected: true},
			{name: "new workspace generation", fence: "generation"}, {name: "metadata head changed", fence: "metadata_head"},
			{name: "current pull request head changed", fence: "pr_head"}, {name: "thread turn advanced", fence: "thread_turn"},
			{name: "inferred session turn advanced", inferred: true, fence: "session_turn"},
			{name: "review became stale", fence: "stale"}, {name: "review was cancelled", fence: "cancelled"},
			{name: "thread cancellation request", fence: "thread_cancel"}, {name: "parent completed", fence: "parent"},
			{name: "uncertain publication", fence: "uncertain"}, {name: "reserved publication", fence: "reserved"},
			{name: "publication receipt", fence: "receipt"}, {name: "staged assessment result", fence: "result"},
			{name: "new live same-thread continuation", fence: "live"}, {name: "new pending same-thread continuation", fence: "pending"},
			{name: "older pending continuation cannot mask current failure", fence: "older_pending", projected: true},
			{name: "older queued continuation with a live claim remains authoritative", fence: "older_live"},
			{name: "new unscoped continuation overlaps explicit failure", fence: "unscoped"},
			{name: "inferred failure cannot cancel live sibling continuation", inferred: true, fence: "live"},
			{name: "expired replacement lease is not an owner", fence: "expired", projected: true},
			{name: "other tenant has no authority", fence: "org"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				f := seedPreparationIntentPostgres(t, pool, !tt.inferred)
				i := checkpointPreparationIntentPostgres(t, pool, f, !tt.inferred)
				orgID := f.orgID
				var query string
				var args []any
				switch tt.fence {
				case "generation":
					query, args = `UPDATE sessions SET workspace_generation=workspace_generation+1 WHERE org_id=$1 AND id=$2`, []any{f.orgID, f.session.ID}
				case "metadata_head":
					query, args = `UPDATE code_review_session_metadata SET head_sha='newhead' WHERE org_id=$1 AND id=$2`, []any{f.orgID, f.metadataID}
				case "pr_head":
					query, args = `UPDATE pull_requests SET head_sha='newhead' WHERE org_id=$1 AND id=$2`, []any{f.orgID, f.pullRequestID}
				case "thread_turn":
					query, args = `UPDATE session_threads SET current_turn=current_turn+1 WHERE org_id=$1 AND id=$2`, []any{f.orgID, f.threadID}
				case "session_turn":
					query, args = `UPDATE sessions SET current_turn=current_turn+1 WHERE org_id=$1 AND id=$2`, []any{f.orgID, f.session.ID}
				case "stale":
					query, args = `UPDATE code_review_session_metadata SET stale=true WHERE org_id=$1 AND id=$2`, []any{f.orgID, f.metadataID}
				case "cancelled":
					query, args = `UPDATE code_review_session_metadata SET status='cancelled' WHERE org_id=$1 AND id=$2`, []any{f.orgID, f.metadataID}
				case "thread_cancel":
					query, args = `UPDATE session_threads SET cancel_requested_at=now() WHERE org_id=$1 AND id=$2`, []any{f.orgID, f.threadID}
				case "parent":
					query, args = `UPDATE sessions SET status='completed' WHERE org_id=$1 AND id=$2`, []any{f.orgID, f.session.ID}
				case "uncertain", "reserved":
					seedPreparationIntentAssessment(t, pool, f, tt.fence, "", "")
				case "receipt":
					seedPreparationIntentAssessment(t, pool, f, "not_started", `{}`, "")
				case "result":
					seedPreparationIntentAssessment(t, pool, f, "not_started", "", "executed")
				case "live", "pending", "unscoped", "expired", "older_pending", "older_live":
					threadID := &f.threadID
					if tt.fence == "unscoped" {
						threadID = nil
					}
					status := "running"
					if tt.fence == "pending" || tt.fence == "older_pending" {
						status = "pending"
					}
					replacementID, _ := seedPreparationReplacement(t, pool, f, threadID, status, tt.fence != "expired")
					if tt.fence == "older_pending" || tt.fence == "older_live" {
						_, err := pool.Exec(context.Background(), `UPDATE jobs SET created_at=(SELECT source.created_at-interval '1 hour' FROM jobs source WHERE source.org_id=$1 AND source.id=$3) WHERE org_id=$1 AND id=$2`, f.orgID, replacementID, f.jobID)
						require.NoError(t, err, "retain a job queued before the failed attempt without weakening a subsequently acquired live lease")
					}
				case "org":
					orgID = uuid.New()
				}
				if query != "" {
					_, err := pool.Exec(context.Background(), query, args...)
					require.NoError(t, err, "retain the competing durable generation, revision, or turn state")
				}
				before := readPreparationIntentState(t, pool, f)
				reason := "Review workspace preparation failed during declared dependency and bootstrap setup. Review did not run. This is an operational review failure."
				actual, err := NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), orgID, i, reason)
				if tt.fence == "org" {
					require.Error(t, err, "a foreign tenant must be rejected before projection")
					require.Equal(t, before, readPreparationIntentState(t, pool, f), "a foreign caller must not mutate or consume the original tenant's intent")
					return
				}
				require.NoError(t, err, "supersession is a durable fence outcome, not setup retry authority")
				require.Equal(t, CodeReviewPreparationProjection{Projected: tt.projected}, actual, "only the original unowned current attempt may project its operational failure")
				after := readPreparationIntentState(t, pool, f)
				require.True(t, after.Consumed, "projected and superseded intents must be consumed once")
				require.Equal(t, before.ThreadStatus, after.ThreadStatus, "database projection must never choose or rewrite a sibling reviewer")
				require.Equal(t, before.ThreadTurn, after.ThreadTurn, "metadata projection must not advance a reviewer turn")
				if tt.projected {
					require.Equal(t, "failed", after.MetadataStatus, "eligible setup failure must become terminal before cleanup")
					require.Equal(t, &reason, after.FailureReason, "operational failure must retain the finite stage reason")
				} else {
					require.Equal(t, before.MetadataStatus, after.MetadataStatus, "superseded intent must preserve current review status")
					require.Equal(t, before.FailureReason, after.FailureReason, "superseded intent must preserve current review result")
				}
				repeated, err := NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), f.orgID, i, reason)
				require.NoError(t, err, "controller retry after consumption must be idempotent")
				require.Equal(t, CodeReviewPreparationProjection{}, repeated, "an already consumed record must not project a second transition")
				require.Equal(t, after, readPreparationIntentState(t, pool, f), "idempotent retry must preserve every observed terminal field and payload")
			})
		}
	})

	t.Run("projection and consumption roll back together", func(t *testing.T) {
		t.Parallel()
		f := seedPreparationIntentPostgres(t, pool, true)
		i := checkpointPreparationIntentPostgres(t, pool, f, true)
		_, err := pool.Exec(context.Background(), `UPDATE jobs SET payload=payload || '{"test_reject_intent_consumption":true}' WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID)
		require.NoError(t, err, "inject a storage failure only after metadata projection reaches consumption")
		before := readPreparationIntentState(t, pool, f)
		_, err = NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), f.orgID, i, "bounded operational reason")
		var storage *pgconn.PgError
		require.ErrorAs(t, err, &storage, "consumption failure must propagate the PostgreSQL transaction error")
		require.Equal(t, "P0001", storage.Code, "the intended row-local trigger must cause the projection rollback")
		require.Equal(t, before, readPreparationIntentState(t, pool, f), "metadata failure and intent consumption must both roll back on storage error")
		_, err = pool.Exec(context.Background(), `UPDATE jobs SET payload=payload-'test_reject_intent_consumption' WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID)
		require.NoError(t, err, "restore storage without restarting the failed continuation")
		actual, err := NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), f.orgID, i, "bounded operational reason")
		require.NoError(t, err, "controller-only retry must recover the same durable checkpoint")
		require.Equal(t, CodeReviewPreparationProjection{Projected: true}, actual, "recovery should project after storage returns without another preparation attempt")
	})

	t.Run("active executor retains checkpoint for projection only", func(t *testing.T) {
		t.Parallel()
		f := seedPreparationIntentPostgres(t, pool, true)
		i := checkpointPreparationIntentPostgres(t, pool, f, true)
		nodeID := "intent-test-" + uuid.NewString()
		_, err := pool.Exec(context.Background(), `INSERT INTO nodes(id,host,mode,status) VALUES($1,'example.invalid','worker','active')`, nodeID)
		require.NoError(t, err, "seed a disposable executor host identity")
		_, err = pool.Exec(context.Background(), `INSERT INTO session_executors(org_id,session_id,thread_id,job_id,job_type,host_node_id,owner_id,lock_token,status,heartbeat_at,lease_expires_at) VALUES($1,$2,$3,$4,'continue_session',$5,'test-owner',$6,'draining',clock_timestamp(),clock_timestamp()+interval '5 minutes')`, f.orgID, f.session.ID, f.threadID, f.jobID, nodeID, f.token)
		require.NoError(t, err, "retain the failing executor while its runtime drains")
		before := readPreparationIntentState(t, pool, f)
		actual, err := NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), f.orgID, i, "bounded operational reason")
		require.NoError(t, err, "runtime drain is a pending projection outcome rather than setup retry")
		require.Equal(t, CodeReviewPreparationProjection{Pending: true}, actual, "active runtime ownership must retain the only terminal checkpoint")
		require.Equal(t, before, readPreparationIntentState(t, pool, f), "runtime ownership must preserve both running metadata and unconsumed intent")
		_, err = pool.Exec(context.Background(), `UPDATE session_executors SET status='failed',completed_at=now() WHERE org_id=$1 AND job_id=$2`, f.orgID, f.jobID)
		require.NoError(t, err, "finish the original runtime without restarting preparation")
		actual, err = NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), f.orgID, i, "bounded operational reason")
		require.NoError(t, err, "controller should project after the original runtime drains")
		require.Equal(t, CodeReviewPreparationProjection{Projected: true}, actual, "drain completion must permit recovery of the same terminal preparation outcome")
	})

	t.Run("active owners obey captured cutoff without retirement", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []string{"executor", "runtime"} {
			for _, lease := range []string{"future", "expired", "null"} {
				for _, expired := range []bool{false, true} {
					name := kind + "/" + lease + "/before cutoff"
					if expired {
						name = kind + "/" + lease + "/after cutoff"
					}
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						f := seedPreparationIntentPostgres(t, pool, true)
						i := checkpointPreparationIntentPostgres(t, pool, f, true)
						owner := seedPreparationIntentOwner(t, pool, f, kind, lease)
						setPreparationReviewCutoff(t, pool, f, expired)
						before := readPreparationIntentState(t, pool, f)
						ownerBefore := readPreparationIntentOwner(t, pool, f, kind, owner)
						actual, err := NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), f.orgID, i, "bounded operational reason")
						require.NoError(t, err, "active ownership must wait only until the captured review cutoff")
						require.Equal(t, CodeReviewPreparationProjection{Projected: expired, Pending: !expired}, actual, "lease expiry alone must not grant projection authority before the policy cutoff")
						require.Equal(t, ownerBefore, readPreparationIntentOwner(t, pool, f, kind, owner), "deadline projection must preserve owner status, token, lease, heartbeat and every other owner field")
						after := readPreparationIntentState(t, pool, f)
						if expired {
							require.Equal(t, "failed", after.MetadataStatus, "expired captured review must durably project the original preparation failure")
							originalReason := "bounded operational reason"
							require.Equal(t, &originalReason, after.FailureReason, "elapsed owner wait must preserve the original preparation reason rather than substitute a review timeout")
							require.True(t, after.Consumed, "deadline projection must atomically consume its exact terminal intent")
						} else {
							require.Equal(t, before, after, "before cutoff both metadata and intent must remain unchanged")
						}
					})
				}
			}
		}
	})

	t.Run("owner cutoff retains newer revision and publication fences", func(t *testing.T) {
		t.Parallel()
		for _, fence := range []string{"live continuation", "pending continuation", "head", "publication"} {
			t.Run(fence, func(t *testing.T) {
				t.Parallel()
				f := seedPreparationIntentPostgres(t, pool, true)
				i := checkpointPreparationIntentPostgres(t, pool, f, true)
				owner := seedPreparationIntentOwner(t, pool, f, "runtime", "null")
				setPreparationReviewCutoff(t, pool, f, true)
				switch fence {
				case "live continuation":
					seedPreparationReplacement(t, pool, f, &f.threadID, "running", true)
				case "pending continuation":
					seedPreparationReplacement(t, pool, f, &f.threadID, "pending", true)
				case "head":
					require.NoError(t, NewPullRequestStore(pool).UpdateHeadSHA(context.Background(), f.orgID, f.pullRequestID, "newhead"), "preserve the competing current PR head")
				case "publication":
					seedPreparationIntentAssessment(t, pool, f, "uncertain", "", "")
				}
				before := readPreparationIntentState(t, pool, f)
				ownerBefore := readPreparationIntentOwner(t, pool, f, "runtime", owner)
				actual, err := NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), f.orgID, i, "bounded operational reason")
				if fence == "publication" {
					require.NoError(t, err, "publication fence must remain authoritative even after the owner wait expires")
				} else {
					require.NoError(t, err, "captured cutoff cannot supersede a newer revision or continuation")
				}
				require.Equal(t, CodeReviewPreparationProjection{}, actual, "policy cutoff must preserve all original revision and owner supersession guards")
				after := readPreparationIntentState(t, pool, f)
				require.Equal(t, before.MetadataStatus, after.MetadataStatus, "deadline must never overwrite a fenced competing review state")
				require.True(t, after.Consumed, "superseded terminal intent must be consumed without operational projection")
				require.Equal(t, ownerBefore, readPreparationIntentOwner(t, pool, f, "runtime", owner), "supersession must never retire an owner row")
			})
		}
	})

	t.Run("captured cutoff provenance cannot be fabricated", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name, mode         string
			pending, projected bool
		}{
			{name: "fresh exact orchestrator gets its existing phase clock", mode: "orchestrator fresh", pending: true},
			{name: "expired exact orchestrator retains its original phase clock", mode: "orchestrator expired", projected: true},
			{name: "post-checkpoint orchestrator cannot grant a fresh recovery wait", mode: "post checkpoint", projected: true},
			{name: "missing terminal completion timestamp fails closed", mode: "missing completed"},
			{name: "fresh reviewer does not renew original review", mode: "reviewer", projected: true},
			{name: "unrelated orchestrator cannot renew this thread", mode: "unrelated", projected: true},
			{name: "inferred thread cannot borrow orchestrator phase clock", mode: "inferred", projected: true},
			{name: "ambiguous exact orchestrators fail closed", mode: "ambiguous"},
			{name: "conflicting exact running roles fail closed", mode: "role conflict"},
			{name: "terminal reviewer role still conflicts with exact orchestrator", mode: "terminal role conflict"},
			{name: "zero original creation time fails closed", mode: "zero"},
			{name: "nonobject captured roster fails closed", mode: "roster"},
			{name: "missing tenant-scoped captured policy fails closed", mode: "missing policy"},
			{name: "inactive captured policy is not renewed by new active policy", mode: "new active policy", projected: true},
			{name: "legacy roster waits before its default cutoff", mode: "legacy before", pending: true},
			{name: "legacy roster settles after its default cutoff", mode: "legacy after", projected: true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				scoped := tt.mode != "inferred"
				f := seedPreparationIntentPostgres(t, pool, scoped)
				i := checkpointPreparationIntentPostgres(t, pool, f, scoped)
				owner := seedPreparationIntentOwner(t, pool, f, "executor", "expired")
				setPreparationReviewCutoff(t, pool, f, true)
				if tt.mode == "new active policy" {
					_, err := pool.Exec(context.Background(), `UPDATE code_review_policies SET active=false WHERE org_id=$1 AND id=$2`, f.orgID, f.policyID)
					require.NoError(t, err, "retain original captured policy as an inactive historical version")
					_, err = pool.Exec(context.Background(), `INSERT INTO code_review_policies(id,org_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,2,'approve_acceptable','{}','{}','{"reviewers":["claude_code"],"orchestrator":"claude_code","timeout_seconds":7200}','','','{}')`, uuid.New(), f.orgID)
					require.NoError(t, err, "activate a newer longer policy without rebinding the original review")
				}
				if tt.mode == "legacy before" || tt.mode == "legacy after" {
					_, err := pool.Exec(context.Background(), `UPDATE code_review_policies SET agent_roster='{}' WHERE org_id=$1 AND id=$2`, f.orgID, f.policyID)
					require.NoError(t, err, "retain legacy raw roster with no explicit timeout")
					age := 29 * time.Minute
					if tt.mode == "legacy after" {
						age = 31 * time.Minute
					}
					_, err = pool.Exec(context.Background(), `UPDATE code_review_session_metadata SET created_at=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.metadataID, time.Now().UTC().Add(-age))
					require.NoError(t, err, "use original creation against the legacy thirty-minute default")
				}
				if tt.mode == "missing completed" {
					_, err := pool.Exec(context.Background(), `UPDATE jobs SET completed_at=NULL WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID)
					require.NoError(t, err, "remove the original terminal cutoff anchor without inventing a replacement")
				}
				if tt.mode == "zero" {
					_, err := pool.Exec(context.Background(), `UPDATE code_review_session_metadata SET created_at='0001-01-01 00:00:00+00' WHERE org_id=$1 AND id=$2`, f.orgID, f.metadataID)
					require.NoError(t, err, "remove valid original cutoff authority without changing the stored intent")
				}
				if tt.mode == "roster" {
					_, err := pool.Exec(context.Background(), `UPDATE code_review_policies SET agent_roster='[]' WHERE org_id=$1 AND id=$2`, f.orgID, f.policyID)
					require.NoError(t, err, "retain an invalid captured roster that cannot authorize projection")
				}
				if tt.mode == "missing policy" {
					otherOrg, otherPolicy := uuid.New(), uuid.New()
					_, err := pool.Exec(context.Background(), `INSERT INTO organizations(id,name) VALUES($1,'other-cutoff-tenant')`, otherOrg)
					require.NoError(t, err, "seed an independently owned policy tenant")
					_, err = pool.Exec(context.Background(), `INSERT INTO code_review_policies(id,org_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,1,'approve_acceptable','{}','{}','{}','','','{}')`, otherPolicy, otherOrg)
					require.NoError(t, err, "seed a policy whose tenant cannot supply this review's cutoff")
					_, err = pool.Exec(context.Background(), `UPDATE code_review_session_metadata SET policy_id=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.metadataID, otherPolicy)
					require.NoError(t, err, "exercise the independent tenant-scoped policy join without disabling schema guards")
				}
				if tt.mode == "orchestrator fresh" || tt.mode == "orchestrator expired" || tt.mode == "post checkpoint" || tt.mode == "reviewer" || tt.mode == "unrelated" || tt.mode == "inferred" || tt.mode == "ambiguous" || tt.mode == "role conflict" || tt.mode == "terminal role conflict" {
					role, threadID := "orchestrator", f.threadID
					if tt.mode == "reviewer" {
						role = "reviewer"
					}
					if tt.mode == "unrelated" {
						threadID = uuid.New()
					}
					var checkpoint time.Time
					require.NoError(t, pool.QueryRow(context.Background(), `SELECT completed_at FROM jobs WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID).Scan(&checkpoint), "use the durable original checkpoint to distinguish original and newly observed phases")
					created := checkpoint.Add(-time.Microsecond)
					if tt.mode == "orchestrator expired" {
						created = checkpoint.Add(-2 * time.Minute)
					}
					if tt.mode == "post checkpoint" {
						created = checkpoint.Add(time.Second)
					}
					count := 1
					if tt.mode == "ambiguous" {
						count = 2
					}
					for range count {
						_, err := pool.Exec(context.Background(), `INSERT INTO code_review_agent_results(org_id,session_id,agent_provider,role,status,structured_result,created_at) VALUES($1,$2,'claude_code',$3,'running',jsonb_build_object('thread_id',$4::text),$5)`, f.orgID, f.session.ID, role, threadID.String(), created)
						require.NoError(t, err, "retain exact agent-result phase provenance without selecting latest unrelated work")
					}
					if tt.mode == "role conflict" || tt.mode == "terminal role conflict" {
						status := "running"
						if tt.mode == "terminal role conflict" {
							status = "completed"
						}
						_, err := pool.Exec(context.Background(), `INSERT INTO code_review_agent_results(org_id,session_id,agent_provider,role,status,structured_result,created_at) VALUES($1,$2,'claude_code','reviewer',$3,jsonb_build_object('thread_id',$4::text),$5)`, f.orgID, f.session.ID, status, threadID.String(), created)
						require.NoError(t, err, "retain the conflicting exact baseline role even when its result is terminal")
					}
				}
				before := readPreparationIntentState(t, pool, f)
				ownerBefore := readPreparationIntentOwner(t, pool, f, "executor", owner)
				actual, err := NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), f.orgID, i, "bounded operational reason")
				if tt.pending || tt.projected {
					require.NoError(t, err, "only an unambiguous captured cutoff may control the owner wait")
					require.Equal(t, CodeReviewPreparationProjection{Projected: tt.projected, Pending: tt.pending}, actual, "result phase clocks must preserve original review and exact thread provenance")
				} else {
					require.Error(t, err, "missing or ambiguous cutoff authority must fail closed")
					require.Equal(t, CodeReviewPreparationProjection{}, actual, "invalid cutoff cannot claim projection or consume the checkpoint")
				}
				after := readPreparationIntentState(t, pool, f)
				if !tt.projected {
					require.Equal(t, before, after, "unexpired or invalid cutoff must preserve every original review/intent field")
				} else {
					require.Equal(t, "failed", after.MetadataStatus, "only expired valid provenance may project")
					require.True(t, after.Consumed, "valid expired provenance must consume the exact checkpoint atomically")
				}
				require.Equal(t, ownerBefore, readPreparationIntentOwner(t, pool, f, "executor", owner), "phase-clock selection must not change executor ownership or leases")
			})
		}
	})

	t.Run("bounded batches cannot starve a valid older intent", func(t *testing.T) {
		t.Parallel()
		for _, malformed := range []bool{false, true} {
			name := "superseded valid batch"
			if malformed {
				name = "malformed batch"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := seedPreparationIntentPostgres(t, pool, false)
				i := checkpointPreparationIntentPostgres(t, pool, f, false)
				_, err := pool.Exec(context.Background(), `UPDATE jobs SET updated_at=now()-interval '1 minute' WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID)
				require.NoError(t, err, "place the valid recoverable intent after a full newest-first batch")
				for range 32 {
					journal := i
					journal.JobID = uuid.New()
					journal.WorkspaceGeneration = i.WorkspaceGeneration - 1
					raw, err := json.Marshal(journal)
					require.NoError(t, err, "encode an older-generation competing journal record")
					var fields map[string]any
					require.NoError(t, json.Unmarshal(raw, &fields), "retain the exact containing journal identity")
					if malformed {
						delete(fields, "turn")
					}
					payload := map[string]any{"session_id": f.session.ID.String(), models.CodeReviewPreparationIntentKey: fields}
					_, err = pool.Exec(context.Background(), `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,completed_at,updated_at) VALUES($1,$2,'agent','continue_session',$3,'dead_letter',now(),now())`, journal.JobID, f.orgID, payload)
					require.NoError(t, err, "seed a full bounded batch without granting its records provider authority")
				}
				first, err := NewJobStore(pool).PendingCodeReviewPreparationIntents(context.Background(), f.orgID, f.metadataID, f.session.ID)
				require.NoError(t, err, "the first bounded batch must drain invalid records without starving later work")
				require.True(t, first.More, "a full batch must require another controller-only pass before fanout")
				if malformed {
					require.Empty(t, first.Intents, "invalid records cannot gain operational projection authority")
				} else {
					require.Len(t, first.Intents, 32, "all first-batch journals must be valid but superseded by the original generation")
					for _, old := range first.Intents {
						outcome, err := NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), f.orgID, old, "bounded operational reason")
						require.NoError(t, err, "consume superseded record under the same projection fence")
						require.Equal(t, CodeReviewPreparationProjection{}, outcome, "old generation must never project the current attempt")
					}
				}
				second, err := NewJobStore(pool).PendingCodeReviewPreparationIntents(context.Background(), f.orgID, f.metadataID, f.session.ID)
				require.NoError(t, err, "the next bounded pass must reach the older valid preparation failure")
				require.Equal(t, CodeReviewPreparationIntentBatch{Intents: []models.CodeReviewPreparationIntent{i}}, second, "consumed first batch must not hide the exact remaining original intent")
				outcome, err := NewCodeReviewStore(pool).ProjectCodeReviewPreparationIntent(context.Background(), f.orgID, i, "bounded operational reason")
				require.NoError(t, err, "the older valid checkpoint must recover without preparation replay")
				require.Equal(t, CodeReviewPreparationProjection{Projected: true}, outcome, "the bounded controller sweep must eventually project its valid terminal failure")
			})
		}
	})

	t.Run("malformed and recheck records have no authority", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []string{"missing generation", "recheck", "wrong containing job"} {
			t.Run(kind, func(t *testing.T) {
				t.Parallel()
				f := seedPreparationIntentPostgres(t, pool, false)
				i := checkpointPreparationIntentPostgres(t, pool, f, false)
				raw, err := json.Marshal(i)
				require.NoError(t, err, "encode the original null-thread checkpoint")
				var fields map[string]any
				require.NoError(t, json.Unmarshal(raw, &fields), "prepare a concrete malformed durable record")
				switch kind {
				case "missing generation":
					delete(fields, "workspace_generation")
				case "recheck":
					fields["recheck"] = true
				case "wrong containing job":
					fields["job_id"] = uuid.NewString()
				}
				_, err = pool.Exec(context.Background(), `UPDATE jobs SET payload=jsonb_set(payload,'{terminal_preparation_intent}',$3::jsonb) WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID, fields)
				require.NoError(t, err, "persist the invalid record alongside legacy safe diagnostic text")
				before := readPreparationIntentState(t, pool, f)
				_, err = NewJobStore(pool).PendingCodeReviewPreparationIntents(context.Background(), f.orgID, f.metadataID, f.session.ID)
				require.NoError(t, err, "invalid durable records must not be promoted by parsing last_error")
				after := readPreparationIntentState(t, pool, f)
				require.Equal(t, before.MetadataStatus, after.MetadataStatus, "invalid record must never fail baseline review metadata")
				require.Equal(t, before.ThreadStatus, after.ThreadStatus, "recheck and invalid provenance must never rewrite baseline reviewers")
			})
		}
	})

	t.Run("partial index matches the bounded pending query", func(t *testing.T) {
		t.Parallel()
		f := seedPreparationIntentPostgres(t, pool, false)
		checkpointPreparationIntentPostgres(t, pool, f, false)
		tx, err := pool.Begin(context.Background())
		require.NoError(t, err, "inspect the real migrated index in a transaction-local planner configuration")
		defer func() { _ = tx.Rollback(context.Background()) }()
		_, err = tx.Exec(context.Background(), `SET LOCAL enable_seqscan=off`)
		require.NoError(t, err, "ask the planner whether this exact partial index can service the bounded query")
		rows, err := tx.Query(context.Background(), `EXPLAIN (COSTS OFF) SELECT id, CASE WHEN octet_length((payload->'terminal_preparation_intent')::text)<=2048 THEN (payload->'terminal_preparation_intent')::text ELSE NULL END, octet_length((payload->'terminal_preparation_intent')::text)>2048 AS oversized FROM jobs
 WHERE org_id=$1 AND job_type='continue_session' AND status='dead_letter'
 AND payload->>'session_id'=$2 AND payload->'terminal_preparation_intent'->>'metadata_id'=$3
 AND payload ? 'terminal_preparation_intent'
 AND NOT (payload ? 'terminal_preparation_intent_consumed') ORDER BY updated_at DESC,id LIMIT 32`, f.orgID, f.session.ID.String(), f.metadataID.String())
		require.NoError(t, err, "explain the actual pending-query predicate and newest-first order")
		plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err, "read the bounded intent query plan")
		require.Contains(t, strings.Join(plan, "\n"), "idx_jobs_review_preparation_intents", "migration313 must match the pending journal lookup without a full dead-letter scan")
	})

	t.Run("concurrent oversized correction is not consumed", func(t *testing.T) {
		t.Parallel()
		f := seedPreparationIntentPostgres(t, pool, false)
		i := checkpointPreparationIntentPostgres(t, pool, f, false)
		raw, err := json.Marshal(i)
		require.NoError(t, err, "encode a valid replacement for the same immutable job identity")
		var oversized map[string]any
		require.NoError(t, json.Unmarshal(raw, &oversized), "retain required routing identity inside an oversized journal")
		oversized["padding"] = strings.Repeat("x", 4096)
		_, err = pool.Exec(context.Background(), `UPDATE jobs SET payload=jsonb_set(payload,'{terminal_preparation_intent}',$3::jsonb) WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID, oversized)
		require.NoError(t, err, "seed a journal whose body must never cross the SQL byte bound")
		var correctionErr error
		db := preparationIntentCorrectionDB{DBTX: pool, onClose: func() {
			_, correctionErr = pool.Exec(context.Background(), `UPDATE jobs SET payload=jsonb_set(payload,'{terminal_preparation_intent}',$3::jsonb) WHERE org_id=$1 AND id=$2`, f.orgID, f.jobID, raw)
		}}
		batch, err := NewJobStore(db).PendingCodeReviewPreparationIntents(context.Background(), f.orgID, f.metadataID, f.session.ID)
		require.NoError(t, correctionErr, "commit the competing valid correction after bounded rows close and before discard")
		require.NoError(t, err, "the original oversized observation must not authorize consuming a corrected replacement")
		require.Equal(t, CodeReviewPreparationIntentBatch{More: true}, batch, "failed oversized CAS must stop fanout until the corrected journal is reread")
		require.False(t, readPreparationIntentState(t, pool, f).Consumed, "size-CAS discard must preserve the concurrently corrected valid checkpoint")
		next, err := NewJobStore(pool).PendingCodeReviewPreparationIntents(context.Background(), f.orgID, f.metadataID, f.session.ID)
		require.NoError(t, err, "a fresh bounded pass must recover the corrected small record")
		require.Equal(t, CodeReviewPreparationIntentBatch{Intents: []models.CodeReviewPreparationIntent{i}}, next, "the corrected checkpoint must retain exact original provenance")
	})

	//nolint:paralleltest // these lock-order cases retain several connections from one bounded PostgreSQL pool and must run serially
	t.Run("current PR head writer race orders", func(t *testing.T) {
		// Each case retains a first transaction, a competing backend, and a lock
		// observer. Serialize these cases in the one bounded migrated pool.
		for _, writer := range []string{"push head update", "GitHub snapshot update"} {
			for _, tt := range []struct {
				name, operation string
				writerFirst     bool
			}{
				{name: "head update commits before admission", operation: "capture", writerFirst: true},
				{name: "admission locks head before update", operation: "capture"},
				{name: "head update commits before projection", operation: "projection", writerFirst: true},
				{name: "projection locks head before update", operation: "projection"},
			} {
				//nolint:paralleltest // each head race retains its first transaction and waiter in the shared bounded PostgreSQL pool
				t.Run(writer+"/"+tt.name, func(t *testing.T) {
					f := seedPreparationIntentPostgres(t, pool, true)
					var intent models.CodeReviewPreparationIntent
					if tt.operation == "projection" {
						intent = checkpointPreparationIntentPostgres(t, pool, f, true)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					updateHead := func(db DBTX) error {
						if writer == "push head update" {
							return NewPullRequestStore(db).UpdateHeadSHA(ctx, f.orgID, f.pullRequestID, "newhead")
						}
						head, base := "newhead", "base123"
						return NewPullRequestStore(db).UpdateGitHubSnapshot(ctx, f.orgID, f.pullRequestID, PullRequestGitHubSnapshot{GitHubPRURL: "https://example.invalid/pr/1", Title: "Synthetic preparation", HeadSHA: &head, BaseSHA: &base})
					}
					tx, err := pool.Begin(ctx)
					require.NoError(t, err, "begin the first actual owner of this PR head row")
					defer func() { _ = tx.Rollback(context.Background()) }()
					if tt.writerFirst {
						require.NoError(t, updateHead(tx), "stage the real production head writer while retaining its PR row lock")
					} else if tt.operation == "capture" {
						captured, err := NewCodeReviewStore(tx).CaptureCodeReviewPreparationIntent(ctx, f.orgID, f.jobID, f.token, f.session, &f.thread)
						require.NoError(t, err, "admit the current review under its locked PR head before the competing update")
						require.Equal(t, "head123", captured.HeadSHA, "admission must capture the exact head held by its row lock")
					} else {
						projected, err := NewCodeReviewStore(tx).ProjectCodeReviewPreparationIntent(ctx, f.orgID, intent, "bounded operational reason")
						require.NoError(t, err, "project while holding the current PR head through the metadata CAS")
						require.Equal(t, CodeReviewPreparationProjection{Projected: true}, projected, "the original current head may project before a later head update")
					}
					if !tt.writerFirst {
						keyCtx, keyCancel := context.WithTimeout(ctx, time.Second)
						var keyShareID uuid.UUID
						keyErr := pool.QueryRow(keyCtx, `SELECT id FROM pull_requests WHERE org_id=$1 AND id=$2 FOR KEY SHARE`, f.orgID, f.pullRequestID).Scan(&keyShareID)
						keyCancel()
						require.NoError(t, keyErr, "the preparation head fence must remain compatible with foreign-key KEY SHARE readers")
						require.Equal(t, f.pullRequestID, keyShareID, "the compatible foreign-key reader must observe the same locked PR identity")
						if tt.operation == "capture" {
							shareCtx, shareCancel := context.WithTimeout(ctx, time.Second)
							var sessionID, metadataID, jobID uuid.UUID
							shareErr := pool.QueryRow(shareCtx, `SELECT s.id,m.id,j.id FROM sessions s JOIN code_review_session_metadata m ON m.org_id=s.org_id AND m.session_id=s.id JOIN jobs j ON j.org_id=s.org_id AND j.id=$4 WHERE s.org_id=$1 AND s.id=$2 AND m.id=$3 FOR KEY SHARE OF s,m,j`, f.orgID, f.session.ID, f.metadataID, f.jobID).Scan(&sessionID, &metadataID, &jobID)
							shareCancel()
							require.NoError(t, shareErr, "admission must keep session/metadata/job identity rows compatible with foreign-key KEY SHARE readers")
							require.Equal(t, []uuid.UUID{f.session.ID, f.metadataID, f.jobID}, []uuid.UUID{sessionID, metadataID, jobID}, "compatible foreign-key readers must observe the exact admitted durable identities")
						}
					}
					conn, err := pool.Acquire(ctx)
					require.NoError(t, err, "reserve a dedicated backend for the competing head operation")
					t.Cleanup(conn.Release)
					var backendID int
					require.NoError(t, conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backendID), "identify the exact competing backend without inspecting unrelated work")
					type outcome struct {
						projection CodeReviewPreparationProjection
						err        error
					}
					done := make(chan outcome, 1)
					received := false
					defer func() {
						_ = tx.Rollback(context.Background())
						cancel()
						if !received {
							select {
							case <-done:
							case <-time.After(time.Second):
								t.Error("head race did not stop after releasing its test-owned locks")
							}
						}
					}()
					go func() {
						if !tt.writerFirst {
							done <- outcome{err: updateHead(conn)}
							return
						}
						if tt.operation == "capture" {
							_, err := NewCodeReviewStore(conn).CaptureCodeReviewPreparationIntent(ctx, f.orgID, f.jobID, f.token, f.session, &f.thread)
							done <- outcome{err: err}
							return
						}
						projected, err := NewCodeReviewStore(conn).ProjectCodeReviewPreparationIntent(ctx, f.orgID, intent, "bounded operational reason")
						done <- outcome{projection: projected, err: err}
					}()
					require.Eventually(t, func() bool {
						var blocked bool
						err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, backendID).Scan(&blocked)
						return err == nil && blocked
					}, 5*time.Second, 5*time.Millisecond, "actual head writer and preparation transition must serialize on the same PR row")
					require.NoError(t, tx.Commit(ctx), "commit the complete first transition before the blocked head operation continues")
					var result outcome
					select {
					case result = <-done:
						received = true
					case <-ctx.Done():
						t.Fatal("competing head operation did not finish after first owner committed")
					}
					state := readPreparationIntentState(t, pool, f)
					if tt.writerFirst && tt.operation == "capture" {
						require.ErrorIs(t, result.err, pgx.ErrNoRows, "admission after a committed head change must reject before repository preparation")
						require.Equal(t, "running", state.MetadataStatus, "head rejection during admission must preserve review lifecycle ownership")
					} else {
						require.NoError(t, result.err, "the second serialized transition must complete without deadlocking its head writer")
						if tt.operation == "projection" {
							expected := "failed"
							if tt.writerFirst {
								expected = "running"
								require.Equal(t, CodeReviewPreparationProjection{}, result.projection, "a committed newer head must supersede the old terminal intent")
							}
							require.Equal(t, expected, state.MetadataStatus, "terminal metadata CAS must linearize against the current PR head")
							require.True(t, state.Consumed, "a projected or superseded terminal intent must be consumed in that same transaction")
						}
					}
					var finalHead string
					require.NoError(t, pool.QueryRow(ctx, `SELECT head_sha FROM pull_requests WHERE org_id=$1 AND id=$2`, f.orgID, f.pullRequestID).Scan(&finalHead), "read the production writer's final durable head")
					require.Equal(t, "newhead", finalHead, "serializing preparation must preserve the actual committed PR head update")
				})
			}
		}
	})

	//nolint:paralleltest // lease-wait cases retain an owner, waiter and observer in one bounded PostgreSQL pool
	t.Run("lease expiration during PR lock wait", func(t *testing.T) {
		// Serial lock choreography keeps the shared migrated pool available to
		// one retained PR owner, one waiter, and the clock/lease observer.
		for _, operation := range []string{"capture", "projection"} {
			//nolint:paralleltest // each lease-expiry race reserves the shared PostgreSQL pool for deterministic lock choreography
			t.Run(operation, func(t *testing.T) {
				f := seedPreparationIntentPostgres(t, pool, true)
				var intent models.CodeReviewPreparationIntent
				expiringJob := f.jobID
				if operation == "projection" {
					intent = checkpointPreparationIntentPostgres(t, pool, f, true)
					expiringJob, _ = seedPreparationReplacement(t, pool, f, &f.threadID, "running", true)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				tx, err := pool.Begin(ctx)
				require.NoError(t, err, "retain the PR row without changing its current reviewed head")
				defer func() { _ = tx.Rollback(context.Background()) }()
				var heldID uuid.UUID
				require.NoError(t, tx.QueryRow(ctx, `SELECT id FROM pull_requests WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE`, f.orgID, f.pullRequestID).Scan(&heldID), "hold the same PR row fence used by real head writers")
				require.Equal(t, f.pullRequestID, heldID, "the retained lock must belong to this exact review's PR")
				conn, err := pool.Acquire(ctx)
				require.NoError(t, err, "reserve the preparation transition's waiting backend")
				t.Cleanup(conn.Release)
				var backendID int
				require.NoError(t, conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backendID), "identify the exact transaction whose start time must not define lease freshness")
				type outcome struct {
					projection CodeReviewPreparationProjection
					err        error
				}
				done := make(chan outcome, 1)
				received := false
				defer func() {
					_ = tx.Rollback(context.Background())
					cancel()
					if !received {
						select {
						case <-done:
						case <-time.After(time.Second):
							t.Error("lease freshness waiter did not stop after releasing test locks")
						}
					}
				}()
				go func() {
					if operation == "capture" {
						_, err := NewCodeReviewStore(conn).CaptureCodeReviewPreparationIntent(ctx, f.orgID, f.jobID, f.token, f.session, &f.thread)
						done <- outcome{err: err}
						return
					}
					result, err := NewCodeReviewStore(conn).ProjectCodeReviewPreparationIntent(ctx, f.orgID, intent, "bounded operational reason")
					done <- outcome{projection: result, err: err}
				}()
				var transactionStarted time.Time
				require.Eventually(t, func() bool {
					var blocked bool
					err := pool.QueryRow(ctx, `SELECT wait_event_type='Lock',xact_start FROM pg_stat_activity WHERE pid=$1`, backendID).Scan(&blocked, &transactionStarted)
					return err == nil && blocked
				}, 5*time.Second, 5*time.Millisecond, "the preparation transaction must already be waiting before its lease expires")
				var expiredAt time.Time
				require.NoError(t, pool.QueryRow(ctx, `UPDATE jobs SET lease_expires_at=clock_timestamp() WHERE org_id=$1 AND id=$2 RETURNING lease_expires_at`, f.orgID, expiringJob).Scan(&expiredAt), "expire only the test-owned lease while its preparation transition waits")
				require.True(t, expiredAt.After(transactionStarted), "lease expiry must be after transaction start so transaction-time now() would incorrectly treat it as live")
				require.NoError(t, tx.Commit(ctx), "resume the waiting preparation transition after its relevant lease has expired")
				var result outcome
				select {
				case result = <-done:
					received = true
				case <-ctx.Done():
					t.Fatal("preparation transition did not finish after releasing its PR lock")
				}
				if operation == "capture" {
					require.ErrorIs(t, result.err, pgx.ErrNoRows, "an admission lease expired during the PR wait must reject before preparation")
					require.Equal(t, "running", readPreparationIntentState(t, pool, f).MetadataStatus, "rejected admission must preserve controller lifecycle ownership")
				} else {
					require.NoError(t, result.err, "an expired replacement owner must not hide the original terminal failure")
					require.Equal(t, CodeReviewPreparationProjection{Projected: true}, result.projection, "wall-clock lease freshness must permit the original failure projection instead of consuming it as superseded")
					state := readPreparationIntentState(t, pool, f)
					require.Equal(t, "failed", state.MetadataStatus, "the failure checkpoint must become durable after the replacement lease expires")
					require.True(t, state.Consumed, "the original failure must be consumed only with its successful operational projection")
				}
			})
		}
	})

	//nolint:paralleltest // admission/projection cases retain several connections from one bounded PostgreSQL pool
	t.Run("admission and projection race orders", func(t *testing.T) {
		// These cases reserve several connections and observe the exact blocked
		// backend; serialize them to avoid starving this fixture's bounded pool.
		for _, admissionFirst := range []bool{false, true} {
			name := "projection commits before late admission"
			if admissionFirst {
				name = "admitted live replacement fences old projection"
			}
			//nolint:paralleltest // each admission/projection race reserves the shared PostgreSQL pool for deterministic lock choreography
			t.Run(name, func(t *testing.T) {
				f := seedPreparationIntentPostgres(t, pool, true)
				i := checkpointPreparationIntentPostgres(t, pool, f, true)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				tx, err := pool.Begin(ctx)
				require.NoError(t, err, "begin the first owner while retaining its actual row and publication locks")
				defer func() { _ = tx.Rollback(context.Background()) }()
				var replacementID, replacementToken uuid.UUID
				if admissionFirst {
					replacementID, replacementToken = seedPreparationReplacement(t, pool, f, &f.threadID, "running", true)
					_, err = NewCodeReviewStore(tx).CaptureCodeReviewPreparationIntent(ctx, f.orgID, replacementID, replacementToken, f.session, &f.thread)
					require.NoError(t, err, "first replacement must be admitted under the same metadata/session lock before setup")
				} else {
					actual, err := NewCodeReviewStore(tx).ProjectCodeReviewPreparationIntent(ctx, f.orgID, i, "bounded operational reason")
					require.NoError(t, err, "first projection must acquire its exact metadata and generation fence")
					require.Equal(t, CodeReviewPreparationProjection{Projected: true}, actual, "first projection must stage the terminal metadata and consumption atomically")
					replacementID, replacementToken = seedPreparationReplacement(t, pool, f, &f.threadID, "running", true)
				}
				conn, err := pool.Acquire(ctx)
				require.NoError(t, err, "reserve a dedicated competing backend for lock-order proof")
				t.Cleanup(conn.Release)
				var backendID int
				require.NoError(t, conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backendID), "identify only the test's competing database backend")
				type outcome struct {
					projection CodeReviewPreparationProjection
					err        error
				}
				done := make(chan outcome, 1)
				received := false
				defer func() {
					// Release the first lock before joining a failed competing
					// call, so a regression fails without stranding a pool slot.
					_ = tx.Rollback(context.Background())
					cancel()
					if !received {
						select {
						case <-done:
						case <-time.After(time.Second):
							t.Error("competing database call did not stop after test cancellation")
						}
					}
				}()
				go func() {
					if admissionFirst {
						projected, err := NewCodeReviewStore(conn).ProjectCodeReviewPreparationIntent(ctx, f.orgID, i, "bounded operational reason")
						done <- outcome{projection: projected, err: err}
						return
					}
					_, err := NewCodeReviewStore(conn).CaptureCodeReviewPreparationIntent(ctx, f.orgID, replacementID, replacementToken, f.session, &f.thread)
					done <- outcome{err: err}
				}()
				require.Eventually(t, func() bool {
					var blocked bool
					err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, backendID).Scan(&blocked)
					return err == nil && blocked
				}, 5*time.Second, 5*time.Millisecond, "competing operation must wait on the first owner's real publication lock")
				require.NoError(t, tx.Commit(ctx), "release the first owner's complete durable state before the competing operation proceeds")
				var result outcome
				select {
				case result = <-done:
					received = true
				case <-ctx.Done():
					t.Fatal("competing projection/admission did not complete after releasing the first owner")
				}
				if admissionFirst {
					require.NoError(t, result.err, "old failure should become a superseded consumed outcome after replacement admission")
					require.Equal(t, CodeReviewPreparationProjection{}, result.projection, "admitted live replacement must prevent an old failure projection")
					require.Equal(t, "running", readPreparationIntentState(t, pool, f).MetadataStatus, "old failure must leave the admitted replacement's review untouched")
				} else {
					require.ErrorIs(t, result.err, pgx.ErrNoRows, "late admission must observe terminal metadata and reject before any provider or preparation call")
					require.Equal(t, "failed", readPreparationIntentState(t, pool, f).MetadataStatus, "late claimant must not revive the terminal review")
				}
			})
		}
	})
}
