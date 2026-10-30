package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Only the external GitHub boundary is logical. Controller, completion,
// retirement, repair, dispute/reply/status and outcome storage below are real.
// These request records are not a claim of real network/provider delivery.
type confirmedRetirementEffectsPublisher struct {
	postpublicationPRLockPublisher
	statusRequests []codereviewsvc.UpsertReviewStatusCommentRequest
	hideRequests   []codereviewsvc.SyncReviewSummaryRequest
	replyRequests  []ghservice.CodeReviewDisputeReplyRequest
}

func (p *confirmedRetirementEffectsPublisher) UpsertReviewStatusComment(_ context.Context, request codereviewsvc.UpsertReviewStatusCommentRequest) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statusRequests = append(p.statusRequests, request)
	return 9001, nil
}

func (p *confirmedRetirementEffectsPublisher) SyncReviewSummary(_ context.Context, request codereviewsvc.SyncReviewSummaryRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hideRequests = append(p.hideRequests, request)
	return nil
}

func (p *confirmedRetirementEffectsPublisher) PublishCodeReviewDisputeReply(_ context.Context, request ghservice.CodeReviewDisputeReplyRequest) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replyRequests = append(p.replyRequests, request)
	return 9101, nil
}

func confirmedRetirementDisputeRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, dispute uuid.UUID) map[string]json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(d) FROM code_review_decision_disputes d WHERE d.org_id=$1 AND d.id=$2`, org, dispute).Scan(&raw), "read every durable dispute field, including its exact owner, status, version and reply cycle")
	return fullAssessmentDispatchObject(t, raw)
}

func confirmedRetirementProviderRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org uuid.UUID) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_object(
	 'human',COALESCE((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.github_review_id) FROM code_review_human_review_observations o WHERE o.org_id=$1),'[]'::jsonb),
	 'lifecycle',COALESCE((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.pull_request_id) FROM code_review_pull_request_lifecycle_observations o WHERE o.org_id=$1),'[]'::jsonb))`, org).Scan(&raw), "retain complete nonempty real provider-observation rows, not only an outcome count")
	return raw
}

// This finite schedule delegates all three attempts, contexts and settlement to
// the original bounded Run. A retained real PR holder causes actual 55P03s.
// Nothing injects a controller error or lengthens a production timeout.
func confirmedRetirementExhaustCompletion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, w *Worker, run *postpublicationPRLockRun) (postpublicationPRLockRunResult, json.RawMessage) {
	t.Helper()
	done := make(chan struct{})
	var holder pgx.Tx
	var observer *pgxpool.Conn
	var startOnce, returnOnce sync.Once
	joined, released := false, false
	defer func() {
		startOnce.Do(func() { close(run.start) })
		returnOnce.Do(func() { close(run.returnGate) })
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if holder != nil && !released {
			if err := holder.Rollback(cleanupCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
				t.Errorf("release only the test-owned PR holder: %v", err)
			}
		}
		if !joined {
			select {
			case <-done:
			case <-cleanupCtx.Done():
				t.Errorf("join actual controller poll before database cleanup: %v", cleanupCtx.Err())
			}
		}
		if observer != nil {
			observer.Release()
		}
	}()
	go func() { w.poll(ctx); close(done) }()
	select {
	case <-run.entered:
	case <-done:
		joined = true
		require.FailNow(t, "the real controller must durably confirm publication before reaching local completion")
	case <-ctx.Done():
		require.FailNow(t, "reach the real postpublication completion gate", "%v", ctx.Err())
	}
	published, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read the exact durable assessment before holding its PR lock")
	metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read the actual metadata publication identity")
	require.Equal(t, models.CodeReviewPublicationConfirmed, published.PublicationState, "the exception must be tested only after positive provider confirmation")
	require.True(t, confirmedFullAssessmentReceiptMatches(published, metadata), "saved receipt must match this original assessment, digest, submitted head and review identity")
	wantReceipt, err := fullAssessmentPublicationReceipt(f.assessment, f.manifest.InputDigest, f.job.HeadSHA, new(int64(77)), new("https://example.invalid/review/77"), nil, nil)
	require.NoError(t, err, "construct the complete exact expected original logical-provider receipt")
	require.JSONEq(t, string(wantReceipt), string(published.PublicationReceipt), "the first real controller must commit every original publication receipt field before contention")
	before, err := terminalContentionSnapshot(ctx, pool, f)
	require.NoError(t, err, "retain the complete durable owner/queue/intent graph after confirmed publication and before contention")
	holder, err = pool.Begin(ctx)
	require.NoError(t, err, "retain a genuine PR-domain transaction holder")
	holderPID := holder.Conn().PgConn().PID()
	_, err = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+f.job.OrgID.String()+":"+f.job.PullRequestID.String())
	require.NoError(t, err, "hold the exact production PR advisory key")
	observer, err = pool.Acquire(ctx)
	require.NoError(t, err, "warm the sole independent observer before starting bounded completion")
	var observerPID uint32
	require.NoError(t, observer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&observerPID), "identify the already acquired observer backend")
	startOnce.Do(func() { close(run.start) })
	var deadline time.Time
	for attempt := 0; attempt < 3; attempt++ {
		var observation postpublicationPRLockAttempt
		select {
		case observation = <-run.attempt:
		case <-ctx.Done():
			require.FailNow(t, "observe each actual completion callback within its existing budget", "%v", ctx.Err())
		}
		require.NotEqual(t, holderPID, observation.pid, "the real completion contender cannot borrow the holder checkout")
		require.NotEqual(t, observerPID, observation.pid, "the real completion contender cannot borrow the observer checkout")
		require.Equal(t, [4]string{"read committed", "1", "250ms", "1500ms"}, observation.policy, "all callbacks must retain the original lifecycle policy")
		require.Greater(t, observation.remaining, time.Duration(0), "the unextended real unit budget must still be live")
		require.LessOrEqual(t, observation.remaining, db.LifecycleTransactionTimeout, "observation must not extend the production unit budget")
		if attempt == 0 {
			deadline = observation.deadline
		} else {
			require.Equal(t, deadline, observation.deadline, "every eligible retry must share the same original whole-unit deadline")
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
		waiting := false
		for !waiting && probeCtx.Err() == nil {
			var blockers []int32
			var state, waitType, waitEvent, query string
			err = observer.QueryRow(probeCtx, `SELECT state,COALESCE(wait_event_type,''),COALESCE(wait_event,''),pg_blocking_pids(pid),query FROM pg_stat_activity WHERE pid=$1`, observation.pid).Scan(&state, &waitType, &waitEvent, &blockers, &query)
			if err != nil {
				break
			}
			waiting = state == "active" && waitType == "Lock" && waitEvent == "advisory" && query == `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`
			if waiting {
				require.Equal(t, []int32{int32(holderPID)}, blockers, "each actual PR advisory wait must name only the retained real holder")
			}
		}
		probeCancel()
		require.NoError(t, err, "observe the live completion backend through the independent warmed observer")
		require.True(t, waiting, "each callback must positively wait on its exact real PR holder, not merely consume time")
	}
	var result postpublicationPRLockRunResult
	select {
	case result = <-run.returned:
	case <-ctx.Done():
		require.FailNow(t, "the original bounded completion unit must return with the holder still retained", "%v", ctx.Err())
	}
	require.ErrorIs(t, result.err, db.ErrLifecycleContention, "real lock exhaustion must retain its confirmed-aborted classification")
	var contention *db.LifecycleContentionError
	require.ErrorAs(t, result.err, &contention, "exhaustion must originate in the real transaction kernel")
	require.Equal(t, 3, contention.Attempts, "the original completion budget must exhaust exactly three actual attempts")
	var codes []string
	for _, attempt := range result.attempts {
		var pgErr *pgconn.PgError
		require.ErrorAs(t, attempt.err, &pgErr, "every real callback must return its PostgreSQL lock timeout")
		codes = append(codes, pgErr.Code)
	}
	require.Equal(t, []string{"55P03", "55P03", "55P03"}, codes, "all three real completion callbacks must abort on the held PR lock")
	var stillHeld bool
	require.NoError(t, observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND granted)`, holderPID).Scan(&stillHeld), "inspect the actual advisory holder after the real Run returned")
	require.True(t, stillHeld, "the independent holder must outlive the entire failed bounded unit")
	after, err := terminalContentionSnapshot(ctx, pool, f)
	require.NoError(t, err, "read the complete graph after all three actual completion rollbacks")
	require.Equal(t, before, after, "failed completion must roll back every owner, job, pending intent, receipt and provenance field")
	require.NoError(t, holder.Rollback(ctx), "release the PR holder before the distinct exact-lease retirement unit")
	released = true
	observer.Release()
	observer = nil
	returnOnce.Do(func() { close(run.returnGate) })
	select {
	case <-done:
		joined = true
	case <-ctx.Done():
		require.FailNow(t, "join actual Worker.poll after its real retirement and postcommit hook", "%v", ctx.Err())
	}
	return result, before
}

func confirmedRetirementOnlyOriginalQueueAndPending(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, before json.RawMessage, started, finished time.Time, diagnostic error) {
	t.Helper()
	after, err := terminalContentionSnapshot(ctx, pool, f)
	require.NoError(t, err, "read every durable original owner/evidence/queue/intent field after actual retirement")
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, after)
	job, actualJob := terminalOwnerPointerPositiveRow(t, expected, "jobs", f.jobID), terminalOwnerPointerPositiveRow(t, actual, "jobs", f.jobID)
	job["status"], job["last_error"], job["owner_kind"] = fullAssessmentDispatchJSON(t, "dead_letter"), fullAssessmentDispatchJSON(t, diagnostic.Error()), fullAssessmentDispatchJSON(t, "worker")
	for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
		job[field] = json.RawMessage(`null`)
	}
	for _, field := range []string{"completed_at", "updated_at"} {
		var stamp time.Time
		require.NoError(t, json.Unmarshal(actualJob[field], &stamp), "read the actual committed queue %s clock", field)
		require.WithinRange(t, stamp, started, finished, "the exact retirement's %s must lie within the observed actual poll", field)
		job[field] = actualJob[field]
	}
	assessment := terminalOwnerPointerPositiveRow(t, expected, "assessments", f.assessment)
	require.JSONEq(t, `null`, string(assessment["failure_detail"]), "the first actual exhaustion must begin before any durable completion-recovery allowance was consumed")
	assessment["failure_detail"] = fullAssessmentDispatchJSON(t, db.CodeReviewConfirmedCompletionRecoveryPending)
	changed := 0
	var priorAttemptSequences []int
	for _, result := range expected.rows["results"] {
		if string(result["dispatch_version"]) != "1" {
			continue
		}
		changed++
		var resultID uuid.UUID
		require.NoError(t, json.Unmarshal(result["id"], &resultID), "retain the exact original pending result identity")
		actualResult := terminalOwnerPointerPositiveRow(t, actual, "results", resultID)
		require.JSONEq(t, `"pending"`, string(result["dispatch_state"]), "both original canaries must genuinely be new-send-eligible before retirement")
		var seq, attemptSeq int
		require.NoError(t, json.Unmarshal(result["dispatch_writer_seq"], &seq), "read the actual preexisting original intent writer sequence")
		require.NoError(t, json.Unmarshal(result["dispatch_attempt_seq"], &attemptSeq), "read the original new or known-aborted retry history")
		priorAttemptSequences = append(priorAttemptSequences, attemptSeq)
		result["dispatch_state"], result["dispatch_resolution"], result["dispatch_due_at"] = fullAssessmentDispatchJSON(t, "cancelled"), fullAssessmentDispatchJSON(t, "owner_cancelled"), json.RawMessage(`null`)
		result["dispatch_writer_seq"] = fullAssessmentDispatchJSON(t, seq+1)
		var stamp time.Time
		require.NoError(t, json.Unmarshal(actualResult["dispatch_updated_at"], &stamp), "read the actual later pending-cancellation statement clock")
		require.WithinRange(t, stamp, started, finished, "pending cancellation must be a real fresh statement within this exact retirement")
		result["dispatch_updated_at"] = actualResult["dispatch_updated_at"]
	}
	require.Equal(t, 2, changed, "the full-graph cancellation proof must have both a new and a known-aborted retry-pending original intent")
	require.ElementsMatch(t, []int{0, 1}, priorAttemptSequences, "the two canaries must retain distinct genuine original dispatch histories")
	require.Equal(t, expected, actual, "confirmed-pending retirement may change only its exact queue, first recovery diagnostic and original pending cancellations; every business metadata, parent, receipt, outcome, reservation, provenance and immutable attempt remains exact")
}

//nolint:paralleltest // Each leaf owns a fully migrated database and real three-slot holder/contender/observer choreography; root serializes these bounded integration schedules.
func TestConfirmedPublicationRetirementEffectsPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for genuine confirmed-publication retirement and wired effect recovery")
	}
	tests := []struct {
		name              string
		replyBeforeRepair bool
	}{
		{name: "independently queued reply and status read pending truth before receipt repair", replyBeforeRepair: true},
		{name: "independently queued reply reads the completed original owner only after receipt repair"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: full-schema setup and the real bounded PR lock schedule
			// are intentionally serialized; every leaf still owns its own database.
			pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 3)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.Equal(t, int32(3), pool.Config().MaxConns, "use only genuine holder, contender and warmed-observer capacity")
			f := seedStrandedReview(t, pool, "idle")
			seedSavedReviewResults(t, pool, f)
			disputeID, reviewedSession := uuid.New(), uuid.New()
			f.job.TriggeringDisputeID = &disputeID
			payload := fullAssessmentDispatchJSON(t, f.job)
			body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{Decision: models.CodeReviewDecisionNeedsHumanReview})
			for _, row := range []struct {
				query string
				args  []any
			}{
				{`INSERT INTO sessions(id,org_id,repository_id,origin,status) VALUES($1,$2,$3,'code_review','completed')`, []any{reviewedSession, f.job.OrgID, f.job.RepositoryID}},
				{`INSERT INTO code_review_decision_disputes(id,org_id,session_id,pull_request_id,repository_id,policy_id,reviewed_head_sha,decision,source,github_comment_id,github_thread_root_comment_id,source_body_hash,body,semantic_input_hash_at_filing,direction,routing,asserts_new_information,intake_status,reassessment_status,reassessment_session_id,adjudication_status)
				 VALUES($1,$2,$3,$4,$5,$6,'head','blocked','github_comment',8101,8100,'synthetic-confirmed-body','Please reconsider this synthetic change.','synthetic-original-input','should_have_approved','reassess',true,'triaged','running',$7,'pending')`, []any{disputeID, f.job.OrgID, reviewedSession, f.job.PullRequestID, f.job.RepositoryID, f.job.PolicyID, f.job.SessionID}},
				{`UPDATE code_review_session_metadata SET trigger_source='dispute_reassessment' WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.job.MetadataID}},
				{`UPDATE sessions SET revision_context=jsonb_build_object('pull_request_title','Saved review','triggering_dispute_id',$3::text) WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.job.SessionID, disputeID.String()}},
				{`UPDATE jobs SET payload=$3 WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.jobID, payload}},
				{`UPDATE code_review_revision_assessments SET result_origin='executed',coverage_complete=true,decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{"coverage_complete":true,"description_assessments":[]}',rendered_body=$3 WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.assessment, body}},
				{`UPDATE code_review_pr_state SET generation=1,active_assessment_id=$3 WHERE org_id=$1 AND pull_request_id=$2`, []any{f.job.OrgID, f.job.PullRequestID, f.assessment}},
			} {
				_, err := pool.Exec(ctx, row.query, row.args...)
				require.NoError(t, err, "seed matching durable original dispute, session revision, full payload and staged outcome without capture-replacement authority")
			}
			f.stores.CodeReviewDisputes = db.NewCodeReviewDisputeStore(pool)
			f.stores.CodeReviewInsights = db.NewCodeReviewInsightStore(pool)
			disputes := codereviewsvc.NewDisputeService(f.stores.CodeReviewDisputes, f.stores.CodeReviews, f.stores.PullRequests, f.stores.Jobs, nil, "https://example.invalid", zerolog.Nop())
			insights := codereviewsvc.NewInsightService(f.stores.CodeReviewInsights, zerolog.Nop())
			observedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			_, err := f.stores.CodeReviewInsights.RecordPullRequestOpen(ctx, f.job.OrgID, f.job.PullRequestID, observedAt)
			require.NoError(t, err, "record an actual synthetic provider lifecycle observation before outcome projection")
			require.NoError(t, f.stores.CodeReviewInsights.RecordHumanReview(ctx, f.job.OrgID, f.job.PullRequestID, 8201, "synthetic-human", "User", "MEMBER", "synthetic-author", "approved", observedAt), "record a real independent human-review observation that projection must retain")
			providerRows := confirmedRetirementProviderRows(t, ctx, pool, f.job.OrgID)
			run := newPostpublicationPRLockRun(f.stores.ThreadSendDependencies.Transactions, pool, f)
			f.stores.ThreadSendDependencies.Transactions = run
			f.stores.CodeReviews, err = db.NewCodeReviewStoreWithTerminalTransactions(pool, run)
			require.NoError(t, err, "wire the actual metadata capability to the original bounded runner")
			terminalJobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, run)
			require.NoError(t, err, "wire exact-lease retirement to the same original bounded runner")
			f.stores.Jobs = terminalJobs
			// Services constructed above retain real stores on this same pool;
			// they do not need or impersonate a controller's transaction carrier.
			files := []codereviewsvc.PullRequestFile{{Filename: "file.go", Patch: "@@ -1 +1 @@\n-old\n+new", Additions: 1, Deletions: 1}}
			publisher := &confirmedRetirementEffectsPublisher{postpublicationPRLockPublisher: postpublicationPRLockPublisher{inlineRecoveryPublisher: inlineRecoveryPublisher{files: files}}}
			capture := &fixedRecheckCapture{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: f.manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}, Files: files}}
			services := &Services{CodeReviews: publisher, CodeReviewInputCapture: capture, CodeReviewDisputes: disputes, CodeReviewDisputePublisher: publisher, CodeReviewInsights: insights, FrontendURL: "https://example.invalid"}
			node := "confirmed-retirement-" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register the exact synthetic consumer node")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='pending',lock_token=NULL,attempts=0,max_attempts=1,priority=100000,target_node_id=$3,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, node)
			require.NoError(t, err, "make exactly the retained original controller claimable at its unchanged final attempt")
			payload = postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, f.jobID)["payload"]
			// These histories genuinely precede publication. Do not install a
			// command after confirmation: the corrected producer must refuse that.
			// Staged needs-human-review recovery does not rerun roster selection;
			// its completion/drain starts only after the positively held PR lock.
			tag, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
			require.NoError(t, err, "enable only this disposable database behind both actual readiness gates")
			require.Equal(t, int64(1), tag.RowsAffected(), "both actual readiness gates must admit the original prepublication histories")
			thread := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,completed_at) VALUES($1,$2,$3,'claude_code','Synthetic retained completion lane','completed',statement_timestamp())`, thread, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "seed a drained original lane for real prepublication pending-intent history")
			for _, state := range []string{"pending", "retry pending"} {
				fullAssessmentDispatchChild(t, ctx, pool, f, thread, state)
			}
			require.NoError(t, disputes.EnqueueReply(ctx, f.job.OrgID, disputeID, "independent_before_controller"), "queue a genuine independent reply before retirement can suppress or enqueue any terminal effect")
			var replyID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND job_type='reply_code_review_dispute' AND dedupe_key=$2`, f.job.OrgID, "reply_code_review_dispute:"+disputeID.String()+":independent_before_controller").Scan(&replyID), "retain the exact genuinely prequeued reply identity")
			statusPayload := codereviewsvc.SyncReviewStatusCommentJobPayload{OrgID: f.job.OrgID, SessionID: f.job.SessionID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID}
			statusKey := "synthetic-independent-status:" + f.job.SessionID.String()
			statusID, err := terminalJobs.EnqueueWithOpts(ctx, f.job.OrgID, db.EnqueueOpts{Queue: "default", JobType: models.JobTypeSyncCodeReviewStatusComment, Payload: statusPayload, DedupeKey: &statusKey})
			require.NoError(t, err, "queue a real independent status reader without a supplied dispute ID so it must recover durable revision provenance")
			beforeDispute := confirmedRetirementDisputeRow(t, ctx, pool, f.job.OrgID, disputeID)
			parentBefore, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "retain the whole original active parent before publication and retirement")
			w := New(pool, zerolog.Nop(), node, WithCodeReviewTerminalStore(terminalJobs))
			handler := newRunCodeReviewHandler(f.stores, services, zerolog.Nop())
			var claims []postpublicationPRLockClaim
			w.Register(models.JobTypeRunCodeReview, func(handlerCtx context.Context, kind string, raw json.RawMessage) error {
				id, _ := jobctx.JobIDFromContext(handlerCtx)
				token, _ := jobctx.LockTokenFromContext(handlerCtx)
				claims = append(claims, postpublicationPRLockClaim{jobID: id, token: token})
				return handler(handlerCtx, kind, raw)
			})
			w.Register(models.JobTypeReplyCodeReviewDispute, newReplyCodeReviewDisputeHandler(f.stores, services, zerolog.Nop()))
			w.Register(models.JobTypeSyncCodeReviewStatusComment, newSyncCodeReviewStatusCommentHandler(f.stores, services, zerolog.Nop()))
			w.Register(models.JobTypeReconcileCodeReviewOutcomes, newReconcileCodeReviewOutcomesHandler(insights))
			pollStarted := time.Now()
			result, publishedBaseline := confirmedRetirementExhaustCompletion(t, ctx, pool, f, w, run)
			pollFinished := time.Now()
			require.Len(t, claims, 1, "only one real original controller claim may precede repair")
			require.Equal(t, f.jobID, claims[0].jobID, "the exhausted claim must be the exact persisted original job")
			require.NotEqual(t, uuid.Nil, claims[0].token, "the actual claim must supply a nonzero lease")
			require.NotEqual(t, f.token, claims[0].token, "real claiming must replace the discarded fixture lease")
			retired := postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, f.jobID)
			require.JSONEq(t, `"dead_letter"`, string(retired["status"]), "completion exhaustion must retire only the exact owned queue attempt")
			require.JSONEq(t, `1`, string(retired["attempts"]), "the exception must not add retry attempts or increase the original queue budget")
			require.JSONEq(t, `1`, string(retired["max_attempts"]), "the original final-attempt cap must remain unchanged")
			require.Equal(t, payload, retired["payload"], "retirement must retain full original fork/dispute/head/output provenance")
			require.JSONEq(t, fmt.Sprintf("%q", result.err.Error()), string(retired["last_error"]), "retired queue diagnostic must be the real completion-contention result")
			for _, field := range []string{"lock_token", "locked_by_node_id", "run_owner_id", "locked_at", "lease_expires_at"} {
				require.JSONEq(t, `null`, string(retired[field]), "exact retirement must release its own queue lease field %s", field)
			}
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read the durable business truth after exact retirement and actual hook")
			require.Equal(t, models.CodeReviewSessionStatusRunning, metadata.Status, "confirmed publication pending local completion must never become a false failed review")
			require.Nil(t, metadata.FailureReason, "local completion exhaustion cannot advertise a failed original review")
			require.Nil(t, metadata.CompletedAt, "the receipt alone cannot manufacture review completion")
			parentAfter, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read the complete parent after committed recovery-pending retirement")
			require.Equal(t, parentBefore, parentAfter, "the recovery-pending hook must preserve every parent field rather than fail it or advise a fresh review")
			require.Equal(t, beforeDispute, confirmedRetirementDisputeRow(t, ctx, pool, f.job.OrgID, disputeID), "known-commit retirement must not terminalize or enqueue a false failed dispute outcome")
			confirmedRetirementOnlyOriginalQueueAndPending(t, ctx, pool, f, publishedBaseline, pollStarted, pollFinished, result.err)
			published, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read retained confirmed original assessment")
			require.Equal(t, models.CodeReviewAssessmentPublishing, published.Status, "the exact confirmed assessment must remain staged rather than falsely fail or complete")
			require.True(t, confirmedFullAssessmentReceiptMatches(published, metadata), "retirement must preserve the original exact usable confirmed receipt")
			require.Equal(t, &body, published.RenderedBody, "retirement must preserve the original published outcome")
			schedule, err := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read the protected original scheduler reservation")
			require.Equal(t, &f.assessment, schedule.ActiveAssessmentID, "recovery-pending retirement must retain the original nonterminal assessment reservation")
			require.Equal(t, &f.job.SessionID, schedule.ActiveSessionID, "recovery-pending retirement must retain the original session reservation")
			var cancelled []string
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY(SELECT dispatch_state::text||':'||dispatch_resolution FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND dispatch_wake->'payload'->>'metadata_id'=$3::text ORDER BY dispatch_key)`, f.job.OrgID, f.job.SessionID, f.job.MetadataID).Scan(&cancelled), "read both genuine original pending-intent histories after retirement")
			require.Equal(t, []string{"cancelled:owner_cancelled", "cancelled:owner_cancelled"}, cancelled, "preserving active metadata must not leave original pending or retry-pending commands eligible for a new send")
			consume := func(id uuid.UUID, kind string) {
				t.Helper()
				tag, updateErr := pool.Exec(ctx, `UPDATE jobs SET priority=200000,target_node_id=$3,run_at=now() WHERE org_id=$1 AND id=$2 AND status='pending'`, f.job.OrgID, id, node)
				require.NoError(t, updateErr, "make only the exact already-persisted effect/wake next in the real queue")
				require.Equal(t, int64(1), tag.RowsAffected(), "the exact selected real queue row must still be pending")
				w.poll(ctx)
				var status models.JobStatus
				var actualKind string
				require.NoError(t, pool.QueryRow(ctx, `SELECT status,job_type FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, id).Scan(&status, &actualKind), "inspect actual queue settlement, never infer effect application from enqueue success")
				require.Equal(t, kind, actualKind, "the exact selected wake must retain its real handler kind")
				require.Equal(t, models.JobStatusSucceeded, status, "the actual selected handler must finish under a real queue lease")
			}
			consume(statusID, models.JobTypeSyncCodeReviewStatusComment)
			wantPendingStatus := codereviewsvc.UpsertReviewStatusCommentRequest{InstallationID: 1, Repository: "test/saved-review", PullNumber: 9, Body: codeReviewStatusCommentBody(metadata, nil, codeReviewSessionURL(services.FrontendURL, f.job.SessionID), "")}
			require.Equal(t, []codereviewsvc.UpsertReviewStatusCommentRequest{wantPendingStatus}, publisher.statusRequests, "an independently queued real status reader must publish exact ongoing truth, not failure/fresh-review guidance")
			require.Empty(t, publisher.hideRequests, "an independent status reader must not finalize the formal result or remove progress before actual local completion")
			require.Equal(t, beforeDispute, confirmedRetirementDisputeRow(t, ctx, pool, f.job.OrgID, disputeID), "independent active status reconciliation must not fail or otherwise rewrite the whole original dispute")
			pendingBody := "I recorded the objection and started a reassessment of the latest pull request revision. [View the dispute](https://example.invalid/code-reviews?evidence=" + reviewedSession.String() + ")\n\n" + ghservice.PRFeedbackHiddenMarker("code-review-dispute:"+disputeID.String())
			wantReply := ghservice.CodeReviewDisputeReplyRequest{OrgID: f.job.OrgID, InstallationID: 1, Repository: "test/saved-review", PullRequestNumber: 9, ThreadRootCommentID: new(int64(8100)), Body: pendingBody}
			if tt.replyBeforeRepair {
				consume(replyID, models.JobTypeReplyCodeReviewDispute)
				require.Equal(t, []ghservice.CodeReviewDisputeReplyRequest{wantReply}, publisher.replyRequests, "the actual independent BuildReply/reserve/publisher/mark path must describe a still-running reassessment, not irreversible failure")
				pending, loadErr := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
				require.NoError(t, loadErr, "read the real published pending reply state")
				require.Equal(t, models.CodeReviewDisputeReassessmentRunning, pending.ReassessmentStatus, "publishing ongoing reply copy must retain the original nonterminal reassessment")
				require.Equal(t, new(int64(9101)), pending.ReplyCommentID, "the exact logical reply identity must be durable before recovery")
			}
			require.NoError(t, insights.ProjectDecision(ctx, f.job.OrgID, f.job.SessionID), "invoke the real insight service and actual projection store before local completion")
			_, err = f.stores.CodeReviewInsights.GetOutcome(ctx, f.job.OrgID, f.job.SessionID)
			require.ErrorIs(t, err, pgx.ErrNoRows, "active confirmed publication cannot manufacture a completed or failed decision-outcome row")
			require.Equal(t, providerRows, confirmedRetirementProviderRows(t, ctx, pool, f.job.OrgID), "pending truth and independent readers must preserve every provider observation")
			require.NoError(t, db.NewCodeReviewScheduleStore(pool).RepairMissingWakes(ctx), "exercise the actual explicit full-controller receipt recovery entry, not a fabricated recovery wake")
			var recoveryID uuid.UUID
			var repairedPayload json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT id,payload FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND status='pending' AND dedupe_key=$2`, f.job.OrgID, "code_review:"+f.job.OutputKey).Scan(&recoveryID, &repairedPayload), "repair must reuse the exact retained original full-controller identity")
			require.NotEqual(t, f.jobID, recoveryID, "explicit repair must create a new wake rather than revive the retired queue attempt")
			require.Equal(t, payload, repairedPayload, "the original dispute/fork/head/policy/output payload must survive repair byte-for-byte")
			captureCalls := capture.calls
			consume(recoveryID, models.JobTypeRunCodeReview)
			require.Equal(t, 2, len(claims), "only the actual original and repaired controller claims may execute")
			require.Equal(t, recoveryID, claims[1].jobID, "the next real controller must be the explicitly repaired original wake")
			require.NotEqual(t, claims[0].token, claims[1].token, "actual recovery must acquire a fresh real lease")
			require.Equal(t, captureCalls, capture.calls, "confirmed receipt recovery must not recapture or manufacture a different review")
			submissions, fileRequests := publisher.submitted()
			require.Equal(t, []codereviewsvc.SubmitReviewRequest{{InstallationID: 1, Repository: "test/saved-review", PullNumber: 9, HeadSHA: f.job.HeadSHA, OutputKey: f.job.OutputKey, Decision: codereviewsvc.SubmitReviewDecisionNeedsHumanReview, Body: body, Comments: []codereviewsvc.SubmitReviewComment{}, RequirePublicationReceipt: true}}, submissions, "both controller executions together must submit exactly one complete original logical-provider request without a fresh review or altered receipt identity")
			require.Equal(t, []codereviewsvc.PullRequestFilesRequest{{InstallationID: 1, Repository: "test/saved-review", PullNumber: 9}}, fileRequests, "actual receipt completion must not fetch or rebuild a second review diff")
			completed, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the exact original assessment after real receipt completion")
			expectedAssessment := published
			expectedAssessment.Status, expectedAssessment.CompletedAt = models.CodeReviewAssessmentCompleted, completed.CompletedAt
			expectedAssessment.FailureDetail = nil
			require.NotNil(t, completed.CompletedAt, "only actual guarded completion may commit the terminal assessment timestamp")
			require.Equal(t, expectedAssessment, completed, "actual completion may change only original status/completion time and clear its exact temporary recovery diagnostic")
			completedMetadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read completed original metadata")
			require.Equal(t, models.CodeReviewSessionStatusCompleted, completedMetadata.Status, "the actual original receipt recovery must complete metadata")
			require.Equal(t, new(models.CodeReviewDecisionNeedsHumanReview), completedMetadata.Decision, "completion must persist the exact original staged decision")
			require.Equal(t, &body, completedMetadata.FinalReviewBody, "completion must retain the original published body")
			completedParent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read the actual same-owner parent success projection after receipt completion")
			require.Equal(t, models.SessionStatusCompleted, completedParent.Status, "only actual guarded receipt completion may complete the original parent")
			var completedOwner *uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT code_review_owner_pr_id FROM sessions WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID).Scan(&completedOwner), "read the exact durable parent full-review owner not exposed by the public session model")
			require.Equal(t, &f.job.PullRequestID, completedOwner, "completed parent must retain the original full-review conversation owner")
			completedSchedule, err := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read actual scheduler settlement after original receipt completion")
			require.Equal(t, &f.assessment, completedSchedule.CurrentAssessmentID, "successful completion must install only the exact original assessment")
			require.Nil(t, completedSchedule.ActiveAssessmentID, "actual successful completion must release the original active assessment reservation")
			require.Nil(t, completedSchedule.ActiveSessionID, "actual successful completion must release the original active session reservation")
			require.Equal(t, models.CodeReviewScheduleCovered, completedSchedule.State, "the original matching head becomes covered only after actual completion")
			finalDispute, err := f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read actual successful reassessment effects after real original-controller completion")
			require.Equal(t, models.CodeReviewDisputeReassessmentCompleted, finalDispute.ReassessmentStatus, "same-owner confirmed completion must not remain irreversibly failed")
			require.Equal(t, &f.job.SessionID, finalDispute.ReassessmentSessionID, "successful dispute outcome must retain the original reassessment owner")
			require.Equal(t, completedMetadata.Decision, finalDispute.ReassessmentDecision, "successful dispute outcome must report the original staged decision")
			require.Equal(t, new(true), finalDispute.ReassessmentFlipped, "the original blocked decision must truthfully record its change to needs-human-review")
			finalReplyID := replyID
			if tt.replyBeforeRepair {
				require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND job_type='reply_code_review_dispute' AND status='pending' AND dedupe_key=$2`, f.job.OrgID, "reply_code_review_dispute:"+disputeID.String()+":terminal").Scan(&finalReplyID), "actual completion must enqueue the existing normal terminal reply")
				wantReply.KnownReplyCommentID, wantReply.SearchExistingReply = new(int64(9101)), true
			}
			wantReply.Body = "The reassessment completed and the decision changed to needs human review. [View the dispute](https://example.invalid/code-reviews?evidence=" + reviewedSession.String() + ")\n\n" + ghservice.PRFeedbackHiddenMarker("code-review-dispute:"+disputeID.String())
			consume(finalReplyID, models.JobTypeReplyCodeReviewDispute)
			wantReplies := []ghservice.CodeReviewDisputeReplyRequest{wantReply}
			if tt.replyBeforeRepair {
				first := wantReply
				first.KnownReplyCommentID, first.SearchExistingReply, first.Body = nil, false, pendingBody
				wantReplies = append([]ghservice.CodeReviewDisputeReplyRequest{first}, wantReply)
			}
			require.Equal(t, wantReplies, publisher.replyRequests, "actual reply publication must deliver exact final success copy and update the same retained logical comment, never advise a fresh failed reassessment")
			var finalStatusID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND job_type='sync_code_review_status_comment' AND status='pending' AND dedupe_key=$2`, f.job.OrgID, "code_review_status_comment:"+f.job.SessionID.String()+":terminal").Scan(&finalStatusID), "actual successful completion must enqueue normal terminal status sync")
			consume(finalStatusID, models.JobTypeSyncCodeReviewStatusComment)
			wantFinalSummary := codereviewsvc.SyncReviewSummaryRequest{InstallationID: 1, Repository: "test/saved-review", PullNumber: 9,
				ReviewID: 77, OutputKey: f.job.OutputKey,
				Body:                  codeReviewStatusCommentBody(completedMetadata, nil, codeReviewSessionURL(services.FrontendURL, f.job.SessionID), ""),
				RemoveProgressComment: true}
			require.Equal(t, []codereviewsvc.UpsertReviewStatusCommentRequest{wantPendingStatus}, publisher.statusRequests, "terminal sync must finalize the formal review without publishing a second issue-comment summary")
			require.Equal(t, []codereviewsvc.SyncReviewSummaryRequest{wantFinalSummary}, publisher.hideRequests, "only after actual completion may normal terminal sync preserve the full original formal result and remove temporary progress")
			require.Contains(t, publisher.hideRequests[0].Body, body, "formal summary synchronization must retain the complete original published review body")
			var outcomeID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND job_type=$3 AND status='pending' AND dedupe_key=$2`, f.job.OrgID, "code_review_outcome:"+f.job.SessionID.String(), models.JobTypeReconcileCodeReviewOutcomes).Scan(&outcomeID), "actual completion must enqueue normal outcome projection")
			consume(outcomeID, models.JobTypeReconcileCodeReviewOutcomes)
			outcome, err := f.stores.CodeReviewInsights.GetOutcome(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "the actual outcome handler/service/store must apply a real completed decision row")
			require.NotNil(t, completedMetadata.CompletedAt, "completed metadata must provide the projection's actual completed observation time")
			require.Equal(t, models.CodeReviewDecisionOutcome{OrgID: f.job.OrgID, SessionID: f.job.SessionID, PullRequestID: f.job.PullRequestID, RepositoryID: f.job.RepositoryID, PolicyID: f.job.PolicyID, Decision: models.CodeReviewDecisionNeedsHumanReview,
				ReasonCodes: []models.CodeReviewRiskReasonCode{}, IndependentApproverLogin: new("synthetic-human"), ObservedUntil: *completedMetadata.CompletedAt, ProjectionUpdatedAt: outcome.ProjectionUpdatedAt, CreatedAt: *completedMetadata.CompletedAt}, outcome, "the whole real outcome must retain exact owner/decision and independent provider facts without a manufactured failed decision")
			require.Equal(t, providerRows, confirmedRetirementProviderRows(t, ctx, pool, f.job.OrgID), "successful completion/projection must preserve every original provider observation")
			finalDispute, err = f.stores.CodeReviewDisputes.GetByID(ctx, f.job.OrgID, disputeID)
			require.NoError(t, err, "read final durable reply and ranking fields")
			require.Equal(t, models.CodeReviewDisputeReplyPublished, finalDispute.ReplyStatus, "real reply settlement must record applied logical publication")
			require.Equal(t, new(int64(9101)), finalDispute.ReplyCommentID, "both schedules must retain one stable published comment identity")
			require.True(t, finalDispute.ReplyCycleReserved, "real reply publication must retain its durable single machine-cycle reservation")
			var signals models.CodeReviewQueueSignals
			require.NoError(t, json.Unmarshal(finalDispute.QueueSignals, &signals), "inspect actual persisted rank inputs rather than queue insertion")
			require.False(t, signals.ComputedAt.IsZero(), "the real outcome handler must actually compute and persist ranking inputs")
			require.Equal(t, models.CodeReviewQueueSignals{ReassessmentFlipped: true, FilerIsNotPRAuthor: true, ComputedAt: signals.ComputedAt}, signals, "all persisted rank inputs must reflect the completed changed decision, active policy and absent original-session projection without manufacturing a failed or unchanged reassessment")
			require.Equal(t, float64(0), finalDispute.QueuePriority, "actual default-disabled ranking must preserve neutral priority while retaining factual changed-decision signals")
			finalRow := confirmedRetirementDisputeRow(t, ctx, pool, f.job.OrgID, disputeID)
			version := 4
			if tt.replyBeforeRepair {
				version++
			}
			require.Equal(t, fullAssessmentDispatchJSON(t, version), finalRow["version"], "only same-owner completion, one reservation and actual successful publication writes may advance dispute version")
			postpublicationPRLockRow(t, beforeDispute, finalRow, map[string]json.RawMessage{
				"reassessment_status": fullAssessmentDispatchJSON(t, "completed"), "reassessment_decision": fullAssessmentDispatchJSON(t, "needs_human_review"), "reassessment_flipped": fullAssessmentDispatchJSON(t, true),
				"reply_status": fullAssessmentDispatchJSON(t, "published"), "reply_comment_id": fullAssessmentDispatchJSON(t, int64(9101)), "reply_cycle_reserved": fullAssessmentDispatchJSON(t, true),
				"status_detail": json.RawMessage(`null`), "version": fullAssessmentDispatchJSON(t, version), "updated_at": finalRow["updated_at"], "queue_signals": finalRow["queue_signals"], "queue_priority": finalRow["queue_priority"],
			})
			var cycles int
			require.NoError(t, pool.QueryRow(ctx, `SELECT code_review_dispute_cycles_in_epoch FROM pull_requests WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.PullRequestID).Scan(&cycles), "read actual durable conversation budget after both reply schedules")
			require.Equal(t, 1, cycles, "a truthful pending reply and later same-comment correction must consume only one machine cycle")
			require.Equal(t, retired, postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, f.jobID), "original repair and all later effects must leave every retired queue field untouched")
		})
	}
}
