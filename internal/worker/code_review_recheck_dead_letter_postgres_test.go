package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // Cases share a schema-wide recovery sweep and run serially within an isolated schema.
func TestRecheckDeadLetterAssessmentAtomicPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for atomic recheck supervisor failure proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name        string
		terminal    func() error
		status      models.CodeReviewAssessmentStatus
		publication models.CodeReviewPublicationState
		detail      string
		preserve    bool
		loseLease   bool
		rejectWrite bool
		foreignOrg  bool
		skipSweep   bool
	}{
		{name: "fatal error", terminal: func() error { return &FatalError{Err: errors.New("permanent recheck failure")} }},
		{name: "attempt exhaustion", terminal: func() error { return &RetryableError{Err: errors.New("recheck unavailable"), ConsumeAttempt: true} }},
		{name: "duration exhaustion", terminal: func() error {
			start, budget := time.Now().Add(-time.Minute), time.Second
			return &RetryableError{Err: errors.New("recheck unavailable"), RetryWindowStartedAt: &start, MaxRetryDuration: &budget}
		}},
		{name: "ordinary error exhaustion", terminal: func() error { return errors.New("recheck failed") }},
		{name: "reserved assessment fails", status: models.CodeReviewAssessmentReserved},
		{name: "unsent reserved publication fails", status: models.CodeReviewAssessmentPublishing, publication: models.CodeReviewPublicationReserved},
		{name: "cleanup settles without repair", skipSweep: true},
		{name: "foreign tenant cannot retire supervisor", foreignOrg: true},
		{name: "lost fence preserves assessment", loseLease: true},
		{name: "assessment write failure rolls back supervisor", rejectWrite: true},
		{name: "uncertain publication stays recoverable", status: models.CodeReviewAssessmentPublishing, publication: models.CodeReviewPublicationUncertain, preserve: true},
		{name: "confirmed publication stays recoverable", status: models.CodeReviewAssessmentPublishing, publication: models.CodeReviewPublicationConfirmed, preserve: true},
		{name: "full fallback marker stays recoverable", status: models.CodeReviewAssessmentFailed, detail: "full_review:retry replacement", preserve: true},
		{name: "evidence refresh marker stays recoverable", status: models.CodeReviewAssessmentSuperseded, detail: "evidence_recheck:inputs changed", preserve: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, baseline := seedDeadLetterRecheck(t, pool)
			status, publication := tt.status, tt.publication
			if status == "" {
				status = models.CodeReviewAssessmentRunning
			}
			if publication == "" {
				publication = models.CodeReviewPublicationNotStarted
			}
			_, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status=$3,publication_state=$4,failure_detail=NULLIF($5,''),completed_at=CASE WHEN $3 IN ('failed','superseded') THEN now() ELSE NULL END,superseded_at=CASE WHEN $3='superseded' THEN now() ELSE NULL END WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, status, publication, tt.detail)
			require.NoError(t, err, "stage the recheck's current publication safety state")
			if publication == models.CodeReviewPublicationConfirmed {
				_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET publication_receipt='{"github_review_id":77}',github_review_id=77 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
				require.NoError(t, err, "retain the confirmed recheck receipt")
			}
			if tt.rejectWrite {
				constraint := "reject_recheck_" + strings.ReplaceAll(f.assessment.String(), "-", "")
				_, err = pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE code_review_revision_assessments ADD CONSTRAINT %s CHECK (org_id <> '%s'::uuid OR status <> 'failed')`, constraint, f.job.OrgID))
				require.NoError(t, err, "reject only this tenant's assessment failure to prove rollback")
			}
			initial, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "retain every immutable recheck field")
			if tt.foreignOrg {
				owned, retireErr := f.stores.Jobs.DeadLetterCodeReviewWithLease(ctx, uuid.New(), f.jobID, f.token, "foreign failure", "foreign failure")
				require.NoError(t, retireErr, "foreign retirement must safely miss the supervisor")
				require.False(t, owned, "org_id must fence the supervisor even with a matching job ID and token")
			}
			node := "recheck-terminal-" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register the isolated supervisor worker")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='pending',lock_token=NULL,attempts=0,max_attempts=1,priority=100000,target_node_id=$3,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, node)
			require.NoError(t, err, "make only the original supervisor the highest-priority runnable job")
			schedule := db.NewCodeReviewScheduleStore(pool)
			hooks, beforeCleanup, reason := 0, models.CodeReviewAssessmentStatus(""), ""
			w := New(pool, zerolog.Nop(), node)
			w.Register(models.JobTypeRunCodeReviewRecheck, func(handlerCtx context.Context, _ string, _ json.RawMessage) error {
				jobctx.RegisterDeadLetterHook(handlerCtx, func(hookCtx context.Context, terminalErr error) {
					hooks++
					reason = codeReviewDeadLetterReason(terminalErr)
					a, readErr := f.stores.CodeReviewAssessments.GetByID(hookCtx, f.job.OrgID, f.assessment)
					require.NoError(t, readErr, "observe the committed assessment before ordinary cleanup")
					beforeCleanup = a.Status
					if !tt.skipSweep {
						require.NoError(t, schedule.RepairMissingWakes(hookCtx), "force repair between supervisor retirement and cleanup")
					}
				})
				registerCodeReviewRecheckDeadLetterReconciliation(handlerCtx, f.stores, zerolog.Nop(), initial)
				if tt.loseLease {
					_, writeErr := pool.Exec(handlerCtx, `UPDATE jobs SET lock_token=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, uuid.New())
					require.NoError(t, writeErr, "transfer the supervisor's fence before terminal persistence")
				}
				if tt.terminal != nil {
					return tt.terminal()
				}
				return &FatalError{Err: errors.New("terminal recheck failure")}
			})
			w.poll(ctx)
			wantJob, wantAssessment, wantHooks, wantCount := models.JobStatusDeadLetter, models.CodeReviewAssessmentFailed, 1, 1
			if tt.preserve {
				wantAssessment, wantCount = initial.Status, 2
			} else if tt.loseLease || tt.rejectWrite {
				wantJob, wantAssessment, wantHooks = models.JobStatusRunning, initial.Status, 0
			}
			state, err := schedule.Get(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read settlement before any post-cleanup repair")
			if wantAssessment == models.CodeReviewAssessmentFailed {
				require.Nil(t, state.ActiveAssessmentID, "cleanup must release the failed assessment even without a repair sweep")
				require.Nil(t, state.ActiveSessionID, "cleanup must release only the failed assessment's session reservation")
			}
			require.Equal(t, &baseline.ID, state.CurrentAssessmentID, "recheck failure must retain the original full-review baseline")
			require.NoError(t, schedule.RepairMissingWakes(ctx), "repeat repair after supervisor cleanup")
			var jobStatus models.JobStatus
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&jobStatus), "read the supervisor's durable terminal state")
			require.Equal(t, wantJob, jobStatus, "only a successful atomic fenced write may release the supervisor")
			a, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the assessment after terminal persistence")
			expected := initial
			if wantAssessment == models.CodeReviewAssessmentFailed && !tt.preserve {
				expected.Status, expected.FailureDetail, expected.CompletedAt = wantAssessment, &reason, a.CompletedAt
				require.NotNil(t, a.CompletedAt, "atomic failure must record the assessment completion time")
			}
			require.Equal(t, expected, a, "retirement may change only assessment failure status, detail, and time")
			require.Equal(t, wantHooks, hooks, "stale or rolled-back owners must not run cleanup")
			if wantHooks == 1 {
				require.Equal(t, wantAssessment, beforeCleanup, "cleanup must already observe failed or protected assessment state")
			}
			unchangedBaseline, err := f.stores.CodeReviewAssessments.GetByID(ctx, baseline.OrgID, baseline.ID)
			require.NoError(t, err, "reload the source full review")
			require.Equal(t, baseline, unchangedBaseline, "supervisor failure must never modify the source full review")
			var counts []int
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type='run_code_review_recheck'),(SELECT count(*)::int FROM code_review_revision_assessments WHERE org_id=$1),(SELECT count(*)::int FROM code_review_agent_results WHERE org_id=$1)]`, f.job.OrgID).Scan(&counts), "count durable recheck and baseline work after recovery")
			require.Equal(t, []int{wantCount, 2, 2}, counts, "only protected publication recovery may restore a supervisor; saved reviewers must remain unchanged")
		})
	}
}

func seedDeadLetterRecheck(t *testing.T, pool *pgxpool.Pool) (strandedReviewFixture, models.CodeReviewAssessment) {
	t.Helper()
	ctx := context.Background()
	f := seedStrandedReview(t, pool, "idle")
	seedSavedReviewResults(t, pool, f)
	_, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',completed_at=now(),publication_state='not_required',result_origin='executed',coverage_complete=true,decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{}',rendered_body='saved full review' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
	require.NoError(t, err, "complete the immutable source full review")
	baseline, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "retain the source full review for exact preservation checks")
	f.assessment = uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,source_assessment_id,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,status,result_origin,decision,acceptable,risk_reason_details,structured_outcome,rendered_body,publication_key)
 SELECT $3,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,2,id,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,'recheck-input',input_manifest,'evidence_only','visual_changed','running','evidence_only',decision,acceptable,risk_reason_details,structured_outcome,'saved recheck output',$4
 FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, baseline.ID, f.assessment, "recheck-output:"+f.assessment.String())
	require.NoError(t, err, "seed the evidence-only successor in the source conversation")
	payload, err := json.Marshal(codeReviewRecheckJob{OrgID: f.job.OrgID, AssessmentID: f.assessment})
	require.NoError(t, err, "encode the exact recheck supervisor identity")
	_, err = pool.Exec(ctx, `UPDATE jobs SET job_type='run_code_review_recheck',payload=$3,dedupe_key=$4 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, payload, "code_review_recheck:"+f.assessment.String())
	require.NoError(t, err, "retain the original leased supervisor row")
	_, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET generation=2,current_assessment_id=$3,active_assessment_id=$4 WHERE org_id=$1 AND pull_request_id=$2`, f.job.OrgID, f.job.PullRequestID, baseline.ID, f.assessment)
	require.NoError(t, err, "reserve the exact successor while retaining the original baseline")
	return f, baseline
}
