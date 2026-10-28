package worker

import (
	"context"
	"encoding/json"
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

// These are storage/helper integration proofs. They do not drive Worker.poll,
// invoke a provider, deliver ACKs or observe SSE/publication. Each leaf owns a
// fully migrated isolated database and a two-connection pool. Run with
// -parallel=1 when the local PostgreSQL reservation permits only two checkouts.
// One checkout stays reserved across actual retirement and reconciliation, so
// neither path can silently obtain another application connection.
type terminalScheduleGraph struct {
	fullAssessmentDispatchGraph
	requests, assessments json.RawMessage
}

func terminalScheduleSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture) terminalScheduleGraph {
	t.Helper()
	g := terminalScheduleGraph{fullAssessmentDispatchGraph: fullAssessmentDispatchSnapshot(t, ctx, pool, f)}
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_requests r WHERE r.org_id=$1),'[]'::jsonb),
		COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM code_review_revision_assessments a WHERE a.org_id=$1),'[]'::jsonb)`, f.job.OrgID).Scan(&g.requests, &g.assessments),
		"retain every request and assessment column, including legal sibling/baseline pointer targets, alongside the complete conversation and scheduler graph")
	return g
}

// The legacy metadata API intentionally omits the assessment pointer. Read it
// independently from the exact tenant row rather than expanding a public model
// merely to support this private storage-association fixture.
func terminalScheduleReadMetadata(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture) (models.CodeReviewSessionMetadata, *uuid.UUID) {
	t.Helper()
	metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read the full existing metadata projection for the original synthetic owner")
	require.Equal(t, f.job.MetadataID, metadata.ID, "the fixture must observe the independently retained metadata row, never a newer sibling")
	var assessmentID *uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT assessment_id FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID).Scan(&assessmentID), "read the exact storage-only assessment pointer without changing the public metadata model")
	return metadata, assessmentID
}

// The observer changes no SQL result, policy or settlement. Every original
// statement reaches the actual supplied transaction. Recording all three query
// entry points also distinguishes an unchanged graph from an attempted no-op
// INSERT/UPDATE against requests or scheduler state.
type terminalScheduleRunRecorder struct {
	actual *fullAssessmentDispatchRunObserver
	writes []string
}

func (r *terminalScheduleRunRecorder) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return r.actual.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		return work(unitCtx, &terminalScheduleTxRecorder{Tx: tx, owner: r})
	})
}

type terminalScheduleTxRecorder struct {
	pgx.Tx
	owner *terminalScheduleRunRecorder
}

func (tx *terminalScheduleTxRecorder) record(query string) {
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	for _, rule := range []struct {
		prefix, label string
	}{
		{prefix: "insert into code_review_pr_state", label: "schedule:insert"},
		{prefix: "update code_review_pr_state ", label: "schedule:update"},
		{prefix: "delete from code_review_pr_state ", label: "schedule:delete"},
		{prefix: "insert into code_review_requests", label: "requests:insert"},
		{prefix: "update code_review_requests ", label: "requests:update"},
		{prefix: "delete from code_review_requests ", label: "requests:delete"},
	} {
		if strings.HasPrefix(normalized, rule.prefix) {
			tx.owner.writes = append(tx.owner.writes, rule.label)
			return
		}
	}
}

func (tx *terminalScheduleTxRecorder) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.record(query)
	return tx.Tx.Exec(ctx, query, args...)
}

func (tx *terminalScheduleTxRecorder) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.record(query)
	return tx.Tx.Query(ctx, query, args...)
}

func (tx *terminalScheduleTxRecorder) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	tx.record(query)
	return tx.Tx.QueryRow(ctx, query, args...)
}

func terminalScheduleSeed(t *testing.T, pool *pgxpool.Pool, recheck bool, status models.CodeReviewAssessmentStatus) (strandedReviewFixture, uuid.UUID, models.CodeReviewAssessment) {
	t.Helper()
	ctx := context.Background()
	var f strandedReviewFixture
	var baseline models.CodeReviewAssessment
	if recheck {
		f, baseline = seedDeadLetterRecheck(t, pool)
		_, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET head_sha='synthetic-recheck-head' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
		require.NoError(t, err, "give the exact recheck a legal captured head different from its full baseline metadata")
	} else {
		f = seedStrandedReview(t, pool, "idle")
		seedSavedReviewResults(t, pool, f)
	}
	metadataAssessment := f.assessment
	if recheck {
		metadataAssessment = baseline.ID
	}
	requestID, unrelatedRequestID := uuid.New(), uuid.New()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`UPDATE code_review_revision_assessments SET status=$3,completed_at=statement_timestamp(),
			publication_state='not_required',result_origin=CASE WHEN review_scope='full' THEN 'executed' ELSE 'evidence_only' END,
			decision='needs_human_review',acceptable=false,coverage_complete=true,risk_reason_details='[]',structured_outcome='{}',
			rendered_body='Synthetic immutable already-terminal result',failure_detail=CASE WHEN $3='failed' THEN 'Synthetic original failure' ELSE NULL END,
			superseded_at=CASE WHEN $3='superseded' THEN statement_timestamp() ELSE NULL END
			WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.assessment, status}},
		{`UPDATE code_review_session_metadata SET assessment_id=$3 WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.job.MetadataID, metadataAssessment}},
		{`UPDATE code_review_pr_state SET active_assessment_id=$3,active_session_id=$4,state='running',wait_reason='active_review',
			retry_at=statement_timestamp()+interval '1 hour',eligible_at=statement_timestamp()+interval '2 hours'
			WHERE org_id=$1 AND pull_request_id=$2`, []any{f.job.OrgID, f.job.PullRequestID, f.assessment, f.job.SessionID}},
		{`INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,session_id,assessment_id,status)
			VALUES($1,$2,$3,$4,'synthetic_terminal',$1::uuid::text,'review_now','synthetic-terminal-request',1,$5,$6,'joined')`, []any{requestID, f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID, f.job.SessionID, f.assessment}},
		{`INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,status)
			VALUES($1,$2,$3,$4,'synthetic_terminal',$1::uuid::text,'review_now','synthetic-unrelated-request',2,'pending')`, []any{unrelatedRequestID, f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID}},
		{`INSERT INTO jobs(org_id,queue,job_type,payload,status,dedupe_key,run_at)
			VALUES($1,'agent','reconcile_code_review_schedule',jsonb_build_object('org_id',$1::uuid::text,'pull_request_id',$2::uuid::text),'pending',$3,statement_timestamp()+interval '1 hour')`, []any{f.job.OrgID, f.job.PullRequestID, "code_review_schedule:" + f.job.PullRequestID.String()}},
		{`INSERT INTO code_review_findings(org_id,session_id,dedupe_key,severity,confidence,path,summary,body)
			VALUES($1,$2,'synthetic-terminal-schedule-finding','info','high','synthetic.go','Synthetic retained finding','Synthetic retained evidence')`, []any{f.job.OrgID, f.job.SessionID}},
		{`INSERT INTO code_review_prompt_records(org_id,session_id,record_key,role,content)
			VALUES($1,$2,'synthetic-terminal-schedule-prompt','reviewer','Synthetic retained prompt')`, []any{f.job.OrgID, f.job.SessionID}},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed only legal terminal-assessment, exact joined-request and original scheduler-reservation rows")
	}
	return f, requestID, baseline
}

func terminalScheduleRetire(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, recheck bool) db.CodeReviewTerminalOutcome {
	t.Helper()
	actualRun := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
	jobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, actualRun)
	require.NoError(t, err, "compose the actual queue with its existing explicit bounded transaction capability")
	outcome, err := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, db.CodeReviewTerminalFailure{
		JobError: "Synthetic terminal controller failure", Reason: "Synthetic terminal controller failure",
		Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic terminal controller failure",
	})
	require.NoError(t, err, "retire the actual exact leased controller before attempting scheduler reconciliation")
	fullAssessmentDispatchAssertRun(t, actualRun)
	require.True(t, outcome.Owned(), "the actual committed retirement must positively own its original lease")
	require.True(t, outcome.Matches(f.job.OrgID, f.jobID, f.token), "only the actual pool wrapper's known commit may authorize this exact job/token outcome")
	scope, hasOwner := outcome.Scope()
	require.True(t, hasOwner, "the actual retirement must retain the original exact assessment association, not job-only fallback")
	expected := db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: f.token,
		JobType: models.JobTypeRunCodeReview, SessionID: f.job.SessionID, MetadataID: f.job.MetadataID,
		RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID, PolicyID: f.job.PolicyID,
		AssessmentID: f.assessment, HeadSHA: f.job.HeadSHA, OutputKey: f.job.OutputKey, ReviewScope: models.CodeReviewScopeFull}
	if recheck {
		a, readErr := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
		require.NoError(t, readErr, "read only the independently retained exact recheck to compare the sealed scope")
		expected.JobType, expected.HeadSHA, expected.OutputKey, expected.ReviewScope = models.JobTypeRunCodeReviewRecheck, a.HeadSHA, a.PublicationKey, models.CodeReviewScopeEvidenceOnly
	}
	require.Equal(t, expected, scope, "scheduler authority must retain every original immutable job, tenant, owner, head/key and review-kind field")
	_, changedAssessment := outcome.Assessment()
	require.False(t, changedAssessment, "an already-terminal assessment must produce no changed-assessment descriptor, while retaining its legitimate cleanup path")
	require.False(t, outcome.UnresolvedDispatch(), "this prerequisite fixture has no unresolved version-one send and must not depend on the unimplemented new status-code schema")
	var status models.JobStatus
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&status), "confirm actual retirement from an independent committed read")
	require.Equal(t, models.JobStatusDeadLetter, status, "a provisional receipt or manually forged context cannot stand in for committed controller retirement")
	return outcome
}

func terminalScheduleReconcile(t *testing.T, ctx context.Context, f strandedReviewFixture, outcome db.CodeReviewTerminalOutcome) *terminalScheduleRunRecorder {
	t.Helper()
	run := &terminalScheduleRunRecorder{actual: &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}}
	require.NoError(t, db.SettleCodeReviewTerminalAssessment(ctx, f.job.OrgID, run, outcome), "the actual scheduler-only helper must either reconcile this exact terminal owner or safely skip its changed association")
	fullAssessmentDispatchAssertRun(t, run.actual)
	return run
}

func terminalScheduleAssertReleased(t *testing.T, before, after terminalScheduleGraph, f strandedReviewFixture, requestID uuid.UUID, baseline models.CodeReviewAssessment, status models.CodeReviewAssessmentStatus, recheck bool, run *terminalScheduleRunRecorder) {
	t.Helper()
	require.Equal(t, []string{"schedule:insert", "requests:update", "schedule:update"}, run.writes,
		"an unchanged terminal owner must reach the existing guarded scheduler insert, exact request settlement and scheduler update, not just return a no-op")
	expectedBusiness := before
	expectedBusiness.schedule, expectedBusiness.requests = after.schedule, after.requests
	require.Equal(t, expectedBusiness, after, "scheduler cleanup must preserve every assessment, metadata, parent, child, evidence, prompt, message, receipt, original lease-retirement row and durable wake column")
	expectedRequests, actualRequests := endedDispatchRows(t, before.requests), endedDispatchRows(t, after.requests)
	changed := 0
	for _, request := range expectedRequests {
		if string(request["id"]) == `"`+requestID.String()+`"` {
			changed++
			request["status"] = json.RawMessage(`"failed"`)
			if status == models.CodeReviewAssessmentCompleted {
				request["status"] = json.RawMessage(`"satisfied"`)
			}
		}
	}
	require.Equal(t, 1, changed, "the complete-row comparison must actually cover the independently identified original joined request")
	require.Equal(t, expectedRequests, actualRequests, "only the original assessment-bound request may change status; the distinct pending request and all other request columns must remain exact")
	expectedSchedule := fullAssessmentDispatchObject(t, before.schedule)
	actualSchedule := fullAssessmentDispatchObject(t, after.schedule)
	var updateClock time.Time
	require.NoError(t, json.Unmarshal(actualSchedule["updated_at"], &updateClock), "decode the actual scheduler statement's committed transaction clock")
	require.True(t, run.actual.clock.Equal(updateClock), "scheduler updated_at must come from this existing real supplied unit, not a second checkout or independently committed helper")
	expectedSchedule["updated_at"] = actualSchedule["updated_at"]
	expectedSchedule["active_assessment_id"], expectedSchedule["active_session_id"] = json.RawMessage(`null`), json.RawMessage(`null`)
	expectedSchedule["retry_at"], expectedSchedule["eligible_at"], expectedSchedule["wait_reason"] = json.RawMessage(`null`), json.RawMessage(`null`), json.RawMessage(`""`)
	expectedSchedule["state"] = json.RawMessage(`"idle"`)
	if status == models.CodeReviewAssessmentCompleted {
		expectedSchedule["current_assessment_id"] = fullAssessmentDispatchJSON(t, f.assessment)
		if !recheck {
			expectedSchedule["state"] = json.RawMessage(`"covered"`)
		}
	} else if recheck {
		expectedSchedule["current_assessment_id"] = fullAssessmentDispatchJSON(t, baseline.ID)
	} else {
		expectedSchedule["current_assessment_id"] = json.RawMessage(`null`)
	}
	require.Equal(t, expectedSchedule, actualSchedule, "the original settlement policy must release only the exact terminal reservation and preserve every unrelated scheduler generation, pending field and comparison head")
}

func TestCodeReviewTerminalScheduleChangedFullOwnerPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, change string
	}{
		{name: "late metadata head change", change: "head"},
		{name: "late metadata output key change", change: "key"},
		{name: "late legal conflicting assessment pointer", change: "pointer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			f, _, _ := terminalScheduleSeed(t, pool, false, models.CodeReviewAssessmentFailed)
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve one of the only two real checkouts before retirement and later scheduler reconciliation")
			defer reserved.Release()
			outcome := terminalScheduleRetire(t, ctx, pool, f, false)
			expectedMetadata, expectedAssessment := terminalScheduleReadMetadata(t, ctx, pool, f)
			require.Equal(t, f.job.HeadSHA, expectedMetadata.HeadSHA, "late head mismatch must start from the actual original full owner")
			require.Equal(t, f.job.OutputKey, expectedMetadata.ReviewOutputKey, "late output-key mismatch must start from the actual original full owner")
			require.Equal(t, &f.assessment, expectedAssessment, "late conflicting pointer must start from the exact retired assessment rather than an absent or already-conflicting target")
			var changed pgconn.CommandTag
			switch tt.change {
			case "head":
				expectedMetadata.HeadSHA = "synthetic-later-head"
				changed, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET head_sha=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID, expectedMetadata.HeadSHA)
			case "key":
				expectedMetadata.ReviewOutputKey = "synthetic-later-key:" + uuid.NewString()
				changed, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET review_output_key=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID, expectedMetadata.ReviewOutputKey)
			case "pointer":
				other := uuid.New()
				var inserted pgconn.CommandTag
				inserted, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,
					base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,
					review_scope,route_reason,status,publication_key,completed_at,failure_detail)
					SELECT $3,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,2,
					base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,
					'full','initial_full','failed',$4,statement_timestamp(),'Synthetic different terminal owner'
					FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, other, "synthetic-later-owner:"+other.String())
				require.NoError(t, err, "create a real legal composite-FK target in the same tenant, session, repository, PR and policy before changing the metadata pointer")
				require.Equal(t, int64(1), inserted.RowsAffected(), "the conflicting pointer must reference a genuinely inserted different assessment, not a missed INSERT SELECT")
				expectedAssessment = &other
				changed, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET assessment_id=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID, other)
			default:
				t.Fatalf("unknown synthetic late owner change %q", tt.change)
			}
			require.NoError(t, err, "commit the legal association change after genuine retirement and before scheduler reconciliation")
			require.Equal(t, int64(1), changed.RowsAffected(), "the intended late owner change must update exactly one real metadata row before the no-write assertion")
			changedMetadata, changedAssessment := terminalScheduleReadMetadata(t, ctx, pool, f)
			require.Equal(t, expectedMetadata, changedMetadata, "the fixture must change exactly the named head, output key or legal assessment pointer and preserve all other metadata columns")
			require.Equal(t, expectedAssessment, changedAssessment, "the legal storage-only pointer change must also be read back independently before the no-write proof")
			before := terminalScheduleSnapshot(t, ctx, pool, f)
			run := terminalScheduleReconcile(t, ctx, f, outcome)
			require.Equal(t, []string(nil), run.writes, "fresh full-owner head/key/pointer mismatch must prevent even an attempted request update or no-op scheduler insert/update")
			require.Equal(t, before, terminalScheduleSnapshot(t, ctx, pool, f), "legal late full-owner association change must leave the complete requests, scheduler, all assessments and original business graph byte-for-byte unchanged")
		})
	}
}

func TestCodeReviewTerminalScheduleAlreadyTerminalFullPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status models.CodeReviewAssessmentStatus
	}{
		{name: "already failed full assessment", status: models.CodeReviewAssessmentFailed},
		{name: "already completed full assessment", status: models.CodeReviewAssessmentCompleted},
		{name: "already cancelled full assessment", status: models.CodeReviewAssessmentCancelled},
		{name: "already superseded full assessment", status: models.CodeReviewAssessmentSuperseded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			f, requestID, baseline := terminalScheduleSeed(t, pool, false, tt.status)
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "retain only one application checkout while obtaining and consuming the genuine committed terminal outcome")
			defer reserved.Release()
			outcome := terminalScheduleRetire(t, ctx, pool, f, false)
			before := terminalScheduleSnapshot(t, ctx, pool, f)
			run := terminalScheduleReconcile(t, ctx, f, outcome)
			terminalScheduleAssertReleased(t, before, terminalScheduleSnapshot(t, ctx, pool, f), f, requestID, baseline, tt.status, false, run)
		})
	}
}

func TestCodeReviewTerminalScheduleRecheckBaselinePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status models.CodeReviewAssessmentStatus
	}{
		{name: "failed recheck retains full baseline", status: models.CodeReviewAssessmentFailed},
		{name: "completed recheck advances only current pointer", status: models.CodeReviewAssessmentCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			f, requestID, baseline := terminalScheduleSeed(t, pool, true, tt.status)
			metadata, metadataAssessment := terminalScheduleReadMetadata(t, ctx, pool, f)
			assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the distinct exact terminal recheck before obtaining its committed receipt")
			require.NotEqual(t, metadata.HeadSHA, assessment.HeadSHA, "the positive recheck fixture must genuinely exercise a different captured head rather than accidentally satisfying full-controller predicates")
			require.NotEqual(t, metadata.ReviewOutputKey, assessment.PublicationKey, "the positive recheck must retain a different baseline output key")
			require.Equal(t, &baseline.ID, metadataAssessment, "the legal baseline metadata pointer must remain bound to the original full assessment, not the terminal recheck")
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve one real checkout so recheck retirement and scheduler-only reconciliation must use only their original bounded owner")
			defer reserved.Release()
			outcome := terminalScheduleRetire(t, ctx, pool, f, true)
			_, changedMetadata := outcome.Metadata()
			require.False(t, changedMetadata, "recheck retirement must not produce a changed full-baseline metadata descriptor")
			before := terminalScheduleSnapshot(t, ctx, pool, f)
			run := terminalScheduleReconcile(t, ctx, f, outcome)
			terminalScheduleAssertReleased(t, before, terminalScheduleSnapshot(t, ctx, pool, f), f, requestID, baseline, tt.status, true, run)
			retainedBaseline, err := f.stores.CodeReviewAssessments.GetByID(ctx, baseline.OrgID, baseline.ID)
			require.NoError(t, err, "read the original full assessment after actual recheck cleanup")
			require.Equal(t, baseline, retainedBaseline, "terminal recheck cleanup may not rewrite any original full-baseline assessment field")
			retainedMetadata, retainedMetadataAssessment := terminalScheduleReadMetadata(t, ctx, pool, f)
			require.Equal(t, metadata, retainedMetadata, "even deliberately different baseline head, key and assessment pointer must remain byte-for-byte unchanged")
			require.Equal(t, metadataAssessment, retainedMetadataAssessment, "the distinct full-baseline storage pointer must remain exact after recheck retirement and cleanup")
		})
	}
}
