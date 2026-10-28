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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These positive storage/transaction fixtures distinguish successful actual
// legacy-NULL retirement from a path that only exhausts lock contention. They
// do not execute Worker.poll, scheduler hooks, providers, Send, ACKs, SSE or
// activation. Every leaf owns an isolated fully migrated database; root uses
// -parallel=1 to bound aggregate capacity. One of two checkouts remains reserved
// across real retirement, so the existing supplied Run has one application slot.
func terminalOwnerPointerPositiveFixture(t *testing.T, pool *pgxpool.Pool, legacy bool) strandedReviewFixture {
	t.Helper()
	ctx := context.Background()
	f := seedStrandedReview(t, pool, "idle")
	seedSavedReviewResults(t, pool, f)
	siblingMetadata, siblingJob, siblingToken := uuid.New(), uuid.New(), uuid.New()
	sibling := f.job
	sibling.MetadataID, sibling.HeadSHA, sibling.OutputKey = siblingMetadata, "synthetic-positive-sibling-head", "synthetic-positive-sibling:"+siblingMetadata.String()
	siblingPayload, err := json.Marshal(sibling)
	require.NoError(t, err, "encode only the separate synthetic same-session sibling controller, never rerender the retained original job")
	var originalAssessment any = f.assessment
	if legacy {
		originalAssessment = nil
	}
	node := "synthetic-positive-owner:" + uuid.NewString()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO nodes(id,mode) VALUES($1,'worker')`, []any{node}},
		{`UPDATE jobs SET locked_by_node_id=$3,locked_at=statement_timestamp()-interval '1 minute',
			lease_expires_at=statement_timestamp()+interval '10 minutes',updated_at=statement_timestamp()-interval '1 minute',
			attempts=2,max_attempts=5,last_error='Synthetic retained previous job failure'
			WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.jobID, node}},
		{`UPDATE code_review_session_metadata SET decision='approved',acceptable=true,failure_reason='Synthetic retained previous metadata failure',
			status_code='github_unavailable',status_message='Synthetic retained previous status',retry_at=statement_timestamp()+interval '1 hour',
			last_error_at=statement_timestamp()-interval '1 minute',retryable_failure=false
			WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.job.MetadataID}},
		{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,phase,review_output_key,failure_reason)
			VALUES($1,$2,$3,$4,$5,$6,'base',$7,'slash_command','running','reviewing',$8,'Synthetic unrelated sibling metadata')`, []any{siblingMetadata, f.job.OrgID, f.job.SessionID, f.job.RepositoryID, f.job.PullRequestID, f.job.PolicyID, sibling.HeadSHA, sibling.OutputKey}},
		{`INSERT INTO jobs(id,org_id,queue,job_type,payload,dedupe_key,status,lock_token,locked_by_node_id,locked_at,lease_expires_at,last_error)
			VALUES($1,$2,'agent','run_code_review',$3,$4,'running',$5,$6,statement_timestamp()-interval '1 minute',statement_timestamp()+interval '10 minutes','Synthetic unrelated sibling job')`, []any{siblingJob, f.job.OrgID, siblingPayload, "code_review:" + sibling.OutputKey, siblingToken, node}},
		{`UPDATE code_review_pr_state SET active_assessment_id=$3,active_session_id=$4,state='running',wait_reason='active_review',
			retry_at=statement_timestamp()+interval '1 hour',eligible_at=statement_timestamp()+interval '2 hours'
			WHERE org_id=$1 AND pull_request_id=$2`, []any{f.job.OrgID, f.job.PullRequestID, originalAssessment, f.job.SessionID}},
		{`INSERT INTO code_review_requests(org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,session_id,assessment_id,status)
			VALUES($1,$2,$3,'synthetic_pointer_positive','original','review_now','synthetic-original-request',1,$4,$5,'joined'),
			($1,$2,$3,'synthetic_pointer_positive','unrelated','review_now','synthetic-unrelated-request',2,$4,NULL,'pending')`, []any{f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID, f.job.SessionID, originalAssessment}},
		{`INSERT INTO jobs(org_id,queue,job_type,payload,status,dedupe_key,run_at)
			VALUES($1,'agent','reconcile_code_review_schedule',jsonb_build_object('org_id',$1::uuid::text,'pull_request_id',$2::uuid::text),'pending',$3,statement_timestamp()+interval '1 hour')`, []any{f.job.OrgID, f.job.PullRequestID, "code_review_schedule:" + f.job.PullRequestID.String()}},
		{`INSERT INTO code_review_findings(org_id,session_id,dedupe_key,severity,confidence,path,summary,body)
			VALUES($1,$2,'synthetic-positive-pointer-finding','info','high','synthetic.go','Synthetic retained finding','Synthetic retained evidence')`, []any{f.job.OrgID, f.job.SessionID}},
		{`INSERT INTO code_review_prompt_records(org_id,session_id,record_key,role,content)
			VALUES($1,$2,'synthetic-positive-pointer-prompt','reviewer','Synthetic retained prompt')`, []any{f.job.OrgID, f.job.SessionID}},
	} {
		tag, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed legal original/sibling leases, active metadata, retained requests, scheduler and immutable evidence before positive retirement")
		require.Positive(t, tag.RowsAffected(), "positive owner fixtures must genuinely create every intended row rather than compare absent unrelated state")
	}
	if legacy {
		tag, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
		require.NoError(t, err, "create true original-assessment absence while retaining the valid original full job and NULL metadata association")
		require.Equal(t, int64(1), tag.RowsAffected(), "the successful legacy case must really remove only the original synthetic assessment")
	}
	return f
}

// Full raw row values remain json.RawMessage, not float-decoded interface{}
// objects. Every snapshot table/column is retained, including sibling rows and
// future unrelated columns. Only the exact source assignments below may change.
type terminalOwnerPointerPositiveGraph struct {
	rows    map[string][]map[string]json.RawMessage
	control json.RawMessage
}

func terminalOwnerPointerPositiveDecode(t *testing.T, raw json.RawMessage) terminalOwnerPointerPositiveGraph {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields), "decode the complete actual PostgreSQL snapshot without dropping numeric precision or durable columns")
	graph := terminalOwnerPointerPositiveGraph{rows: make(map[string][]map[string]json.RawMessage, len(fields)), control: fields["control"]}
	for name, value := range fields {
		if name == "control" {
			continue
		}
		graph.rows[name] = endedDispatchRows(t, value)
	}
	return graph
}

func terminalOwnerPointerPositiveRow(t *testing.T, graph terminalOwnerPointerPositiveGraph, table string, id uuid.UUID) map[string]json.RawMessage {
	t.Helper()
	for _, row := range graph.rows[table] {
		var actual uuid.UUID
		require.NoError(t, json.Unmarshal(row["id"], &actual), "read the independently retained original %s identity from its complete row", table)
		if actual == id {
			return row
		}
	}
	t.Fatalf("complete %s graph lacks original synthetic owner %s", table, id)
	return nil
}

func terminalOwnerPointerPositiveClock(t *testing.T, expected, actual map[string]json.RawMessage, field string, stamp time.Time) {
	t.Helper()
	var clock time.Time
	require.NoError(t, json.Unmarshal(actual[field], &clock), "read the actual committed %s timestamp without replacing it by a guessed expected clock", field)
	require.True(t, stamp.Equal(clock), "the changed %s must use the independently observed original supplied transaction timestamp", field)
	// Only after exact typed clock equality may PostgreSQL's presentation of
	// that timestamp be reused; unrelated fields are never whitelisted.
	expected[field] = actual[field]
}

func terminalOwnerPointerPositiveAssertGraph(t *testing.T, before, after json.RawMessage, f strandedReviewFixture, failure db.CodeReviewTerminalFailure, legacy bool, stamp time.Time) {
	t.Helper()
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, after)
	job := terminalOwnerPointerPositiveRow(t, expected, "jobs", f.jobID)
	actualJob := terminalOwnerPointerPositiveRow(t, actual, "jobs", f.jobID)
	require.Equal(t, json.RawMessage(`"running"`), job["status"], "the positive path must begin with the actual original running lease, not an already-retired replay")
	require.NotEqual(t, json.RawMessage(`null`), job["locked_by_node_id"], "clearing the original worker owner must be a real non-NULL change")
	require.NotEqual(t, json.RawMessage(`null`), job["lease_expires_at"], "clearing the original expiry must be a real non-NULL change")
	job["status"], job["last_error"], job["owner_kind"] = json.RawMessage(`"dead_letter"`), fullAssessmentDispatchJSON(t, failure.JobError), json.RawMessage(`"worker"`)
	for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
		job[field] = json.RawMessage(`null`)
	}
	terminalOwnerPointerPositiveClock(t, job, actualJob, "completed_at", stamp)
	terminalOwnerPointerPositiveClock(t, job, actualJob, "updated_at", stamp)
	metadata := terminalOwnerPointerPositiveRow(t, expected, "metadata", f.job.MetadataID)
	actualMetadata := terminalOwnerPointerPositiveRow(t, actual, "metadata", f.job.MetadataID)
	require.Equal(t, json.RawMessage(`"running"`), metadata["status"], "the exact original metadata must be active before the real failure mutation")
	metadata["status"] = json.RawMessage(`"failed"`)
	metadata["phase"], metadata["decision"], metadata["acceptable"], metadata["retry_at"] = json.RawMessage(`null`), json.RawMessage(`null`), json.RawMessage(`null`), json.RawMessage(`null`)
	metadata["failure_reason"], metadata["status_code"], metadata["status_message"] = fullAssessmentDispatchJSON(t, strings.TrimSpace(failure.Reason)), fullAssessmentDispatchJSON(t, failure.Code), fullAssessmentDispatchJSON(t, strings.TrimSpace(failure.Message))
	metadata["retryable_failure"] = fullAssessmentDispatchJSON(t, failure.Retryable)
	terminalOwnerPointerPositiveClock(t, metadata, actualMetadata, "last_error_at", stamp)
	terminalOwnerPointerPositiveClock(t, metadata, actualMetadata, "completed_at", stamp)
	if !legacy {
		assessment := terminalOwnerPointerPositiveRow(t, expected, "assessments", f.assessment)
		actualAssessment := terminalOwnerPointerPositiveRow(t, actual, "assessments", f.assessment)
		require.Equal(t, json.RawMessage(`"running"`), assessment["status"], "the retained full assessment must genuinely transition from eligible unsent running state")
		assessment["status"], assessment["failure_detail"] = json.RawMessage(`"failed"`), fullAssessmentDispatchJSON(t, strings.TrimSpace(failure.Reason))
		terminalOwnerPointerPositiveClock(t, assessment, actualAssessment, "completed_at", stamp)
	}
	require.Equal(t, expected, actual, "only the exact original job lease/terminal fields, original active metadata failure fields and eligible retained assessment failure fields may change; every sibling, request, scheduler, evidence, control and unrelated column must remain exact")
}

func TestCodeReviewTerminalFullOwnerPointerPositivePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		self, legacy bool
	}{
		{name: "retained running A with genuine NULL pointer"},
		{name: "retained running A with actual linked self pointer", self: true},
		{name: "true original assessment absence with genuine NULL pointer", legacy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			f := terminalOwnerPointerPositiveFixture(t, pool, tt.legacy)
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve one of exactly two actual checkouts before positive retirement")
			defer reserved.Release()
			if tt.self {
				require.NoError(t, terminalOwnerPointerLink(ctx, reserved, f, f.assessment), "establish the original full A self association through the actual bounded, ordered evidence linker before retirement")
			}
			var pointer *uuid.UUID
			var originalCount int
			require.NoError(t, reserved.QueryRow(ctx, `SELECT assessment_id,(SELECT count(*)::int FROM code_review_revision_assessments a WHERE a.org_id=$1 AND a.id=$3)
				FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID, f.assessment).Scan(&pointer, &originalCount),
				"independently observe the exact storage-only pointer and original assessment presence without expanding the public metadata model")
			var expectedPointer *uuid.UUID
			if tt.self {
				expectedPointer = &f.assessment
			}
			require.Equal(t, expectedPointer, pointer, "the positive fixture must genuinely begin with exactly NULL or its real original self association")
			wantCount := 1
			if tt.legacy {
				wantCount = 0
			}
			require.Equal(t, wantCount, originalCount, "legacy success must execute the actual absent-original-assessment storage path")
			beforeMetadata, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
			require.NoError(t, err, "retain every public metadata descriptor field through the unique original output key rather than a latest-session reader")
			var beforeAssessment models.CodeReviewAssessment
			if !tt.legacy {
				beforeAssessment, err = f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
				require.NoError(t, err, "retain the complete exact eligible original full-assessment descriptor before failure")
			}
			before, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "retain all complete original and same-session sibling rows before the actual bounded retirement unit")
			observed := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			jobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, observed)
			require.NoError(t, err, "supply the actual aware JobStore with its existing explicit real bounded Run capability")
			failure := db.CodeReviewTerminalFailure{JobError: "  Synthetic original positive job error  ", Reason: "  Synthetic original positive retirement reason  ",
				Code: models.CodeReviewStatusCodeWorkerFailed, Message: "  Synthetic original positive retirement message  ", Retryable: true}
			outcome, err := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, failure)
			require.NoError(t, err, "genuine NULL/self ownership or original-assessment absence must permit exact atomic retirement, not only survive a blocked contention attempt")
			fullAssessmentDispatchAssertRun(t, observed)
			require.True(t, outcome.Owned(), "positive retirement must actually own and retire the original leased job")
			require.True(t, outcome.Matches(f.job.OrgID, f.jobID, f.token), "the returned sealed outcome must prove known commit for the exact original tenant/job/token")
			scope, hasOwner := outcome.Scope()
			require.True(t, hasOwner, "true legacy absence remains valid exact full metadata ownership, not malformed-job-only fallback")
			expectedScope := db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: f.token, JobType: models.JobTypeRunCodeReview,
				SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID,
				PolicyID: f.job.PolicyID, AssessmentID: f.assessment, HeadSHA: f.job.HeadSHA, OutputKey: f.job.OutputKey, ReviewScope: models.CodeReviewScopeFull}
			if tt.legacy {
				expectedScope.AssessmentID = uuid.Nil
			}
			require.Equal(t, expectedScope, scope, "known-committed scope must retain the complete original association and must not discover an assessment for genuine legacy absence")
			require.False(t, outcome.UnresolvedDispatch(), "positive pointer ownership must not rely on unimplemented new-status DDL or fabricate unresolved send intent")
			after, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "read the exact committed complete graph independently from the application owner after known success")
			terminalOwnerPointerPositiveAssertGraph(t, before, after, f, failure, tt.legacy, observed.clock)
			metadata, metadataChanged := outcome.Metadata()
			require.True(t, metadataChanged, "successful original active full-metadata retirement must return its actual changed descriptor")
			stamp := observed.clock
			reason, message, code := strings.TrimSpace(failure.Reason), strings.TrimSpace(failure.Message), failure.Code
			beforeMetadata.Status, beforeMetadata.Phase, beforeMetadata.Decision, beforeMetadata.Acceptable = models.CodeReviewSessionStatusFailed, nil, nil, nil
			beforeMetadata.FailureReason, beforeMetadata.StatusCode, beforeMetadata.StatusMessage = &reason, &code, &message
			beforeMetadata.RetryAt, beforeMetadata.LastErrorAt, beforeMetadata.RetryableFailure, beforeMetadata.CompletedAt = nil, &stamp, failure.Retryable, &stamp
			require.Equal(t, beforeMetadata, metadata, "the returned changed metadata descriptor must retain every unrelated original field and the same actual unit clock as committed storage")
			storedMetadata, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
			require.NoError(t, err, "independently read the actual original metadata after commit without adopting the unrelated newer sibling")
			require.Equal(t, storedMetadata, metadata, "a returned metadata descriptor must be the actual committed original row, not merely a synthetic expected struct")
			assessment, assessmentChanged := outcome.Assessment()
			require.Equal(t, !tt.legacy, assessmentChanged, "only a genuinely present eligible full assessment may produce a changed-assessment descriptor")
			if tt.legacy {
				require.Equal(t, models.CodeReviewAssessment{}, assessment, "actual original-assessment absence must not fabricate or acquire a new changed assessment")
			} else {
				beforeAssessment.Status, beforeAssessment.FailureDetail, beforeAssessment.CompletedAt = models.CodeReviewAssessmentFailed, &reason, &stamp
				require.Equal(t, beforeAssessment, assessment, "the changed assessment descriptor may differ only by its original failure fields and actual unit clock")
				storedAssessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
				require.NoError(t, err, "independently read the exact actual original full assessment after known commit")
				require.Equal(t, storedAssessment, assessment, "the returned assessment descriptor must match actual committed storage without losing evidence or receipt fields")
			}
			var afterPointer *uuid.UUID
			require.NoError(t, reserved.QueryRow(ctx, `SELECT assessment_id,(SELECT count(*)::int FROM code_review_revision_assessments a WHERE a.org_id=$1 AND a.id=$3)
				FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID, f.assessment).Scan(&afterPointer, &originalCount), "recheck the exact actual storage association and original assessment count after positive retirement")
			require.Equal(t, expectedPointer, afterPointer, "retirement must retain a real self pointer or genuine NULL, never install or acquire another assessment")
			require.Equal(t, wantCount, originalCount, "positive legacy retirement must not create or adopt an originally absent assessment")
		})
	}
}
