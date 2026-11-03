package worker

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Literal diagnostics keep this regression compilable against the predecessor
// core. Absence of the approved behavior must fail an assertion, not the build.
const confirmedBudgetPending = "confirmed_completion_recovery_pending"
const confirmedBudgetOperator = "operator_reconciliation_required:confirmed_completion_exhausted"

type confirmedBudgetPoll struct {
	worker *Worker
	run    *postpublicationPRLockRun
	claims chan postpublicationPRLockClaim
	hooks  chan postpublicationPRLockHook
}

// Recompose only test-owned wrappers for each actual poll. The original Run,
// contexts, queue, full handler, retries, leases and retirement remain real.
func newConfirmedBudgetPoll(t *testing.T, pool *pgxpool.Pool, f strandedReviewFixture, services *Services, node string, base db.CodeReviewMetadataTransactions) confirmedBudgetPoll {
	t.Helper()
	run := newPostpublicationPRLockRun(base, pool, f)
	f.stores.ThreadSendDependencies.Transactions = run
	var err error
	f.stores.CodeReviews, err = db.NewCodeReviewStoreWithTerminalTransactions(pool, run)
	require.NoError(t, err, "each poll must retain an aware metadata store on the original bounded runner")
	jobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, run)
	require.NoError(t, err, "each poll must retain exact-lease retirement on that same original runner")
	f.stores.Jobs = jobs
	poll := confirmedBudgetPoll{worker: New(pool, zerolog.Nop(), node, WithCodeReviewTerminalStore(jobs)), run: run,
		claims: make(chan postpublicationPRLockClaim, 1), hooks: make(chan postpublicationPRLockHook, 1)}
	handler := newRunCodeReviewHandler(f.stores, services, zerolog.Nop())
	poll.worker.Register(models.JobTypeRunCodeReview, func(handlerCtx context.Context, kind string, payload json.RawMessage) error {
		id, hasJob := jobctx.JobIDFromContext(handlerCtx)
		token, hasLease := jobctx.LockTokenFromContext(handlerCtx)
		if !hasJob || !hasLease {
			return db.ErrCodeReviewTerminalInput
		}
		claim := postpublicationPRLockClaim{jobID: id, token: token}
		poll.claims <- claim
		jobctx.RegisterDeadLetterHook(handlerCtx, func(hookCtx context.Context, _ error) {
			outcome, scope, committed := committedCodeReviewTerminalOutcome(hookCtx, f.job.OrgID, models.JobTypeRunCodeReview)
			poll.hooks <- postpublicationPRLockHook{claim: claim, scope: scope, committed: committed, pending: outcome.CompletionRecoveryPending()}
		})
		return handler(handlerCtx, kind, payload)
	})
	return poll
}

func confirmedBudgetClaim(t *testing.T, poll confirmedBudgetPoll, id uuid.UUID) postpublicationPRLockClaim {
	t.Helper()
	select {
	case claim := <-poll.claims:
		require.Equal(t, id, claim.jobID, "the actual queue claim must select the exact original or repaired controller")
		require.NotEqual(t, uuid.Nil, claim.token, "the actual queue must supply a fresh nonzero lease")
		require.Empty(t, poll.claims, "one poll cannot manufacture a second controller claim")
		return claim
	default:
		t.Fatal("the actual full handler did not receive its expected persisted controller")
		return postpublicationPRLockClaim{}
	}
}

func confirmedBudgetHook(t *testing.T, poll confirmedBudgetPoll) postpublicationPRLockHook {
	t.Helper()
	select {
	case hook := <-poll.hooks:
		require.Empty(t, poll.hooks, "one retired queue attempt must release only its single registered probe")
		return hook
	default:
		t.Fatal("the joined actual retirement did not release its expected known-commit hook")
		return postpublicationPRLockHook{}
	}
}

// Compare every actual graph column after the helper has already proved that
// all three completion attempts rolled back. These are only queue settlement,
// the approved exact-A diagnostic and the first original pending cancellation.
func confirmedBudgetAssertPollGraph(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, before json.RawMessage, result postpublicationPRLockRunResult, attempt int, retired bool, detail string, pending uuid.UUID, started, finished time.Time) {
	t.Helper()
	after, err := terminalContentionSnapshot(ctx, pool, f)
	require.NoError(t, err, "read the complete durable graph after the real poll's queue handling")
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, after)
	job := terminalOwnerPointerPositiveRow(t, expected, "jobs", f.jobID)
	actualJob := terminalOwnerPointerPositiveRow(t, actual, "jobs", f.jobID)
	require.JSONEq(t, `"running"`, string(job["status"]), "the comparison must begin with the actual claimed running row")
	require.Equal(t, fullAssessmentDispatchJSON(t, attempt), job["attempts"], "only actual claiming may consume this exact attempt number")
	status := "pending"
	if retired {
		status = "dead_letter"
	}
	job["status"], job["last_error"], job["owner_kind"] = fullAssessmentDispatchJSON(t, status), fullAssessmentDispatchJSON(t, result.err.Error()), fullAssessmentDispatchJSON(t, "worker")
	for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
		job[field] = json.RawMessage(`null`)
	}
	clockFields := []string{"updated_at"}
	if retired {
		clockFields = append(clockFields, "completed_at")
	} else {
		var runAt time.Time
		require.NoError(t, json.Unmarshal(actualJob["run_at"], &runAt), "decode the real queue's ordinary exponential backoff")
		delay := retryBackoff(attempt)
		require.WithinRange(t, runAt, started.Add(delay), finished.Add(delay), "ordinary queue retry must use the unchanged backoff before the fixture makes it due")
		job["run_at"] = actualJob["run_at"]
	}
	for _, field := range clockFields {
		var stamp time.Time
		require.NoError(t, json.Unmarshal(actualJob[field], &stamp), "read the actual queue settlement clock %s", field)
		require.WithinRange(t, stamp, started, finished, "queue settlement clock %s must belong to this actual poll", field)
		job[field] = actualJob[field]
	}
	assessment := terminalOwnerPointerPositiveRow(t, expected, "assessments", f.assessment)
	assessment["failure_detail"] = fullAssessmentDispatchJSON(t, detail)
	if pending != uuid.Nil {
		row := terminalOwnerPointerPositiveRow(t, expected, "results", pending)
		actualRow := terminalOwnerPointerPositiveRow(t, actual, "results", pending)
		require.JSONEq(t, `"pending"`, string(row["dispatch_state"]), "the canary must be genuinely send-eligible before original retirement")
		var writer int
		require.NoError(t, json.Unmarshal(row["dispatch_writer_seq"], &writer), "retain the original pending canary's actual writer sequence")
		row["dispatch_state"], row["dispatch_resolution"], row["dispatch_due_at"] = fullAssessmentDispatchJSON(t, "cancelled"), fullAssessmentDispatchJSON(t, "owner_cancelled"), json.RawMessage(`null`)
		row["dispatch_writer_seq"] = fullAssessmentDispatchJSON(t, writer+1)
		var stamp time.Time
		require.NoError(t, json.Unmarshal(actualRow["dispatch_updated_at"], &stamp), "read the actual original pending cancellation's statement clock")
		require.WithinRange(t, stamp, started, finished, "pending cancellation must occur in the original actual retirement")
		row["dispatch_updated_at"] = actualRow["dispatch_updated_at"]
	}
	require.Equal(t, expected, actual, "receipt, outcome, active metadata, parent, scheduler reservation, immutable intents, evidence and every unrelated row must remain exact")
}

//nolint:paralleltest // Owns one migrated database and nine real holder/contender/observer schedules; serialize this finite budget proof and join every poll before cleanup.
func TestConfirmedPublicationRecoveryBudgetPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for actual original-plus-eight repaired controller attempts")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 3)
	require.Equal(t, int32(3), pool.Config().MaxConns, "use only actual holder, contender and warmed observer capacity")
	f := seedStrandedReview(t, pool, "idle")
	seedSavedReviewResults(t, pool, f)
	base := f.stores.ThreadSendDependencies.Transactions
	disputeID, reviewedSession := uuid.New(), uuid.New()
	f.job.TriggeringDisputeID = &disputeID
	body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{Decision: models.CodeReviewDecisionNeedsHumanReview})
	node := "confirmed-budget-" + uuid.NewString()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sessions(id,org_id,repository_id,origin,status) VALUES($1,$2,$3,'code_review','completed')`, []any{reviewedSession, f.job.OrgID, f.job.RepositoryID}},
		{`INSERT INTO code_review_decision_disputes(id,org_id,session_id,pull_request_id,repository_id,policy_id,reviewed_head_sha,decision,source,github_comment_id,github_thread_root_comment_id,source_body_hash,body,semantic_input_hash_at_filing,direction,routing,asserts_new_information,intake_status,reassessment_status,reassessment_session_id,adjudication_status)
 VALUES($1,$2,$3,$4,$5,$6,'head','blocked','github_comment',8101,8100,'synthetic-budget-body','Please reconsider this synthetic change.','synthetic-budget-input','should_have_approved','reassess',true,'triaged','running',$7,'pending')`, []any{disputeID, f.job.OrgID, reviewedSession, f.job.PullRequestID, f.job.RepositoryID, f.job.PolicyID, f.job.SessionID}},
		{`UPDATE code_review_session_metadata SET trigger_source='dispute_reassessment' WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.job.MetadataID}},
		{`UPDATE sessions SET revision_context=jsonb_build_object('triggering_dispute_id',$3::text) WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.job.SessionID, disputeID.String()}},
		{`UPDATE code_review_revision_assessments SET result_origin='executed',coverage_complete=true,decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{"coverage_complete":true,"description_assessments":[]}',rendered_body=$3 WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.assessment, body}},
		{`UPDATE code_review_pr_state SET generation=1,active_assessment_id=$3 WHERE org_id=$1 AND pull_request_id=$2`, []any{f.job.OrgID, f.job.PullRequestID, f.assessment}},
		{`INSERT INTO nodes(id,mode) VALUES($1,'worker')`, []any{node}},
		{`UPDATE jobs SET payload=$3,status='pending',lock_token=NULL,attempts=0,max_attempts=1,priority=100000,target_node_id=$4,run_at=now() WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.jobID, fullAssessmentDispatchJSON(t, f.job), node}},
		{`UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`, nil},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed only the actual original staged owner, dispute provenance and one-attempt original queue fixture")
	}
	thread := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,completed_at) VALUES($1,$2,$3,'claude_code','Synthetic budget lane','completed',statement_timestamp())`, thread, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "retain one drained lane for a genuine prepublication pending intent")
	pending := fullAssessmentDispatchChild(t, ctx, pool, f, thread, "pending")
	f.stores.CodeReviewDisputes = db.NewCodeReviewDisputeStore(pool)
	disputes := codereviewsvc.NewDisputeService(f.stores.CodeReviewDisputes, f.stores.CodeReviews, f.stores.PullRequests, f.stores.Jobs, nil, "https://example.invalid", zerolog.Nop())
	files := []codereviewsvc.PullRequestFile{{Filename: "file.go", Patch: "@@ -1 +1 @@\n-old\n+new", Additions: 1, Deletions: 1}}
	publisher := &confirmedRetirementEffectsPublisher{postpublicationPRLockPublisher: postpublicationPRLockPublisher{inlineRecoveryPublisher: inlineRecoveryPublisher{files: files}}}
	capture := &fixedRecheckCapture{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: f.manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}, Files: files}}
	services := &Services{CodeReviews: publisher, CodeReviewInputCapture: capture, CodeReviewDisputes: disputes, CodeReviewDisputePublisher: publisher, FrontendURL: "https://example.invalid"}
	original := postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, f.jobID)
	disputeBefore := confirmedRetirementDisputeRow(t, ctx, pool, f.job.OrgID, disputeID)
	first := newConfirmedBudgetPoll(t, pool, f, services, node, base)
	started := time.Now()
	result, before := confirmedRetirementExhaustCompletion(t, ctx, pool, f, first.worker, first.run)
	finished := time.Now()
	firstClaim := confirmedBudgetClaim(t, first, f.jobID)
	confirmedBudgetAssertPollGraph(t, ctx, pool, f, before, result, 1, true, confirmedBudgetPending, pending, started, finished)
	hook := confirmedBudgetHook(t, first)
	require.True(t, hook.committed && hook.pending, "only the actual known-committed positive retirement may release the preserved-completion hook")
	require.Equal(t, firstClaim, hook.claim, "the positive retirement must retain the actual original lease")
	require.Equal(t, disputeBefore, confirmedRetirementDisputeRow(t, ctx, pool, f.job.OrgID, disputeID), "original retirement must preserve the complete running dispute")
	requests, _ := publisher.submitted()
	require.Equal(t, 1, len(requests), "the original actual full handler must perform exactly one logical SubmitReview")
	captureCalls := capture.calls

	sweep := db.NewCodeReviewScheduleStore(pool)
	require.NoError(t, sweep.RepairMissingWakes(ctx), "the first committed pending marker must permit actual original-payload wake repair")
	var repairedID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND status='pending' AND dedupe_key=$2`, f.job.OrgID, "code_review:"+f.job.OutputKey).Scan(&repairedID), "find the actual repaired original wake")
	require.NotEqual(t, f.jobID, repairedID, "repair must create a new queue attempt instead of reviving the retired original")
	repaired := postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, repairedID)
	require.Equal(t, original["payload"], repaired["payload"], "repair must retain every original dispute, fork, policy, head and output payload field")
	require.Equal(t, original["dedupe_key"], repaired["dedupe_key"], "repair must retain the original controller identity")
	require.JSONEq(t, `0`, string(repaired["attempts"]), "the real repaired wake must start with no consumed attempts")
	require.JSONEq(t, `8`, string(repaired["max_attempts"]), "RepairMissingWakes must retain its unchanged actual eight-attempt budget")
	tag, err := pool.Exec(ctx, `UPDATE jobs SET priority=100000,target_node_id=$3 WHERE org_id=$1 AND id=$2 AND status='pending'`, f.job.OrgID, repairedID, node)
	require.NoError(t, err, "pin only the actual repaired fixture wake to the isolated worker")
	require.Equal(t, int64(1), tag.RowsAffected(), "pin the one genuinely repaired pending row without changing its attempt counters or budget")
	lastClaim := firstClaim
	for attempt := 1; attempt <= 8; attempt++ {
		if attempt > 1 {
			tag, err = pool.Exec(ctx, `UPDATE jobs SET run_at=now() WHERE org_id=$1 AND id=$2 AND status='pending'`, f.job.OrgID, repairedID)
			require.NoError(t, err, "make only the actual repaired fixture job's ordinary queue backoff due")
			require.Equal(t, int64(1), tag.RowsAffected(), "the exact repaired row must remain pending between real claims")
		}
		poll := newConfirmedBudgetPoll(t, pool, f, services, node, base)
		current := f
		current.jobID = repairedID
		started = time.Now()
		result, before = confirmedRetirementExhaustCompletion(t, ctx, pool, current, poll.worker, poll.run)
		finished = time.Now()
		claim := confirmedBudgetClaim(t, poll, repairedID)
		require.NotEqual(t, lastClaim.token, claim.token, "every real repaired attempt must obtain a fresh queue lease")
		lastClaim = claim
		detail := confirmedBudgetPending
		if attempt == 8 {
			detail = confirmedBudgetOperator
		}
		confirmedBudgetAssertPollGraph(t, ctx, pool, current, before, result, attempt, attempt == 8, detail, uuid.Nil, started, finished)
		job := postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, repairedID)
		require.JSONEq(t, `8`, string(job["max_attempts"]), "all eight actual polls must retain the repaired job's original maximum unchanged")
		if attempt < 8 {
			require.Empty(t, poll.hooks, "ordinary repaired retries must not run a retirement hook or consume the diagnostic allowance")
		} else {
			hook = confirmedBudgetHook(t, poll)
			require.True(t, hook.committed && hook.pending, "the exhausted repaired job must still preserve positive confirmed completion truth")
			require.Equal(t, claim, hook.claim, "the second retirement must bind only the final actual repaired lease")
		}
		require.Equal(t, captureCalls, capture.calls, "confirmed repaired attempts must never recapture or replace the review")
		require.Equal(t, disputeBefore, confirmedRetirementDisputeRow(t, ctx, pool, f.job.OrgID, disputeID), "none of the actual repaired attempts may false-fail or rewrite the dispute")
		requests, _ = publisher.submitted()
		require.Equal(t, 1, len(requests), "all original and repaired attempts together must retain one logical total SubmitReview")
	}
	paused, err := terminalContentionSnapshot(ctx, pool, f)
	require.NoError(t, err, "retain every durable row after actual repaired-budget exhaustion")
	for pass := 0; pass < 3; pass++ {
		require.NoError(t, sweep.RepairMissingWakes(ctx), "repeat the actual repair sweep after the committed operator pause")
		after, readErr := terminalContentionSnapshot(ctx, pool, f)
		require.NoError(t, readErr, "read the whole graph after repeated repair")
		require.Equal(t, paused, after, "operator reconciliation must stop repair without a new wake or any domain mutation")
	}
	var controllerCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND dedupe_key=$2`, f.job.OrgID, "code_review:"+f.job.OutputKey).Scan(&controllerCount), "count the actual original and repaired controller rows")
	require.Equal(t, 2, controllerCount, "automatic repair must stop after exactly original plus one genuinely exhausted repaired job")

	// An explicitly queued wake must also pause when legacy metadata already
	// has the actual matching provider ID. This is an isolated fixture write
	// copied from the confirmed A, not a new publication or fabricated receipt.
	metadataBefore := fullAssessmentDispatchSnapshot(t, ctx, pool, f).metadata
	tag, err = pool.Exec(ctx, `UPDATE code_review_session_metadata m SET github_review_id=a.github_review_id,github_review_url=a.github_review_url FROM code_review_revision_assessments a WHERE m.org_id=$1 AND m.id=$2 AND a.org_id=m.org_id AND a.id=$3 AND a.metadata_id=m.id AND a.publication_state='confirmed' AND a.github_review_id IS NOT NULL`, f.job.OrgID, f.job.MetadataID, f.assessment)
	require.NoError(t, err, "retain a compatible metadata provider identity from the actual already-confirmed assessment")
	require.Equal(t, int64(1), tag.RowsAffected(), "the exact original metadata must receive its matching nonnull provider identity")
	metadataAfter := fullAssessmentDispatchSnapshot(t, ctx, pool, f).metadata
	postpublicationPRLockRow(t, fullAssessmentDispatchObject(t, metadataBefore), fullAssessmentDispatchObject(t, metadataAfter), map[string]json.RawMessage{
		"github_review_id": json.RawMessage(`77`), "github_review_url": fullAssessmentDispatchJSON(t, "https://example.invalid/review/77"),
	})
	key := "code_review:" + f.job.OutputKey
	manualID, err := db.NewJobStore(pool).EnqueueWithOpts(ctx, f.job.OrgID, db.EnqueueOpts{Queue: "agent", JobType: models.JobTypeRunCodeReview, Payload: json.RawMessage(original["payload"]), Priority: 100000, DedupeKey: &key, TargetNodeID: &node})
	require.NoError(t, err, "explicitly queue one original-payload wake to test operator-pause enforcement")
	require.NotEqual(t, uuid.Nil, manualID, "the manual-wake canary must genuinely exist before the actual poll")
	businessBefore := postpublicationPRLockSnapshot(t, ctx, pool, f)
	manual := newConfirmedBudgetPoll(t, pool, f, services, node, base)
	close(manual.run.start)
	close(manual.run.returnGate)
	manualStarted := time.Now()
	manual.worker.poll(ctx)
	manualFinished := time.Now()
	confirmedBudgetClaim(t, manual, manualID)
	manual.run.mu.Lock()
	calls := manual.run.calls
	manual.run.mu.Unlock()
	require.Equal(t, 0, calls, "operator pause must stop before any completion or retirement Run despite metadata provider identity")
	manualJob := postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, manualID)
	require.JSONEq(t, `"succeeded"`, string(manualJob["status"]), "the actual full handler acknowledges a paused wake without manufacturing business completion")
	require.JSONEq(t, `1`, string(manualJob["attempts"]), "only the actual manual wake claim may consume its first queue attempt")
	require.Empty(t, manual.hooks, "a paused wake is not a false failure or retirement")
	businessAfter := postpublicationPRLockSnapshot(t, ctx, pool, f)
	queueBefore := terminalOwnerPointerPositiveGraph{rows: map[string][]map[string]json.RawMessage{"jobs": endedDispatchRows(t, businessBefore.jobs)}}
	queueAfter := terminalOwnerPointerPositiveGraph{rows: map[string][]map[string]json.RawMessage{"jobs": endedDispatchRows(t, businessAfter.jobs)}}
	manualExpected := terminalOwnerPointerPositiveRow(t, queueBefore, "jobs", manualID)
	manualExpected["status"], manualExpected["attempts"], manualExpected["owner_kind"] = fullAssessmentDispatchJSON(t, "succeeded"), fullAssessmentDispatchJSON(t, 1), fullAssessmentDispatchJSON(t, "worker")
	for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
		manualExpected[field] = json.RawMessage(`null`)
	}
	for _, field := range []string{"completed_at", "updated_at"} {
		var stamp time.Time
		require.NoError(t, json.Unmarshal(manualJob[field], &stamp), "read the manually queued wake's actual %s clock", field)
		require.WithinRange(t, stamp, manualStarted, manualFinished, "manual paused acknowledgment %s must belong to its actual poll", field)
		manualExpected[field] = manualJob[field]
	}
	require.Equal(t, queueBefore, queueAfter, "manual pause may acknowledge only its exact queue row; both exhausted controller histories and all other jobs remain exact")
	businessBefore.jobs = businessAfter.jobs
	require.Equal(t, businessBefore, businessAfter, "paused manual delivery must preserve the exact receipt, staged outcome, active metadata, parent, reservation, requests, evidence and execution guards")
	require.Equal(t, disputeBefore, confirmedRetirementDisputeRow(t, ctx, pool, f.job.OrgID, disputeID), "manual delivery cannot bypass operator pause into irreversible dispute failure")
	requests, _ = publisher.submitted()
	require.Equal(t, 1, len(requests), "the complete bounded repair and paused manual-wake sequence has exactly one logical provider submission")
	require.Empty(t, publisher.hideRequests, "completion exhaustion must never remove the retained published summary as failure cleanup")
	require.Empty(t, publisher.replyRequests, "pending retirement and pause must never emit failure or fresh-review dispute guidance")
	require.Empty(t, publisher.statusRequests, "no unqueued status effect is mistaken for actual provider delivery")
	final, err := terminalContentionSnapshot(ctx, pool, f)
	require.NoError(t, err, "retain every durable row after paused manual delivery")
	require.NoError(t, sweep.RepairMissingWakes(ctx), "a manually acknowledged paused wake must not reset automatic recovery allowance")
	afterFinalRepair, err := terminalContentionSnapshot(ctx, pool, f)
	require.NoError(t, err, "read final repeated repair graph")
	require.Equal(t, final, afterFinalRepair, "operator pause remains durable after a manually queued wake is acknowledged")
}
