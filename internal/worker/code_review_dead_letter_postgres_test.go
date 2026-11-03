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
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // Cases deliberately run schema-wide repair sweeps, so they share this schema serially.
func TestCodeReviewDeadLetterAssessmentAtomicPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for atomic controller failure proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
	require.Equal(t, int32(2), pool.Config().MaxConns, "actual worker retirement and postcommit cleanup must fit the bounded two-slot pool")
	tests := []struct {
		name        string
		err         func() error
		loseLease   bool
		rejectWrite bool
		foreignOrg  bool
		publication models.CodeReviewPublicationState
		preserve    bool
	}{
		{name: "fatal error", err: func() error { return &FatalError{Err: errors.New("permanent review failure")} }},
		{name: "attempt exhaustion", err: func() error {
			return &RetryableError{Err: errFullAssessmentFreshnessInconclusive, ConsumeAttempt: true}
		}},
		{name: "duration exhaustion", err: func() error {
			start, budget := time.Now().Add(-time.Minute), time.Second
			return &RetryableError{Err: errFullAssessmentFreshnessInconclusive, RetryWindowStartedAt: &start, MaxRetryDuration: &budget}
		}},
		{name: "ordinary error exhaustion", err: func() error { return errors.New("review failed") }},
		{name: "foreign org cannot retire review", foreignOrg: true, err: func() error { return &FatalError{Err: errors.New("owned terminal failure")} }},
		{name: "lost fence preserves assessment", loseLease: true, err: func() error { return &FatalError{Err: errors.New("stale owner failure")} }},
		{name: "assessment failure rolls back job terminal write", rejectWrite: true, err: func() error { return &FatalError{Err: errors.New("failure requiring rollback")} }},
		{name: "unsent reserved publication fails atomically", publication: models.CodeReviewPublicationReserved, err: func() error { return &FatalError{Err: errors.New("unsent publication rejected")} }},
		{name: "uncertain publication stays recoverable", publication: models.CodeReviewPublicationUncertain, preserve: true, err: func() error { return &FatalError{Err: errors.New("publication remains uncertain")} }},
		{name: "confirmed publication stays recoverable", publication: models.CodeReviewPublicationConfirmed, preserve: true, err: func() error { return &FatalError{Err: errors.New("receipt recovery deferred")} }},
		{name: "ended parent ownership rejection stays fenced", preserve: true, err: func() error { return &FatalError{Err: errCodeReviewEndedParentRecovery} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := seedStrandedReview(t, pool, "idle")
			terminalJobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, f.stores.ThreadSendDependencies.Transactions)
			require.NoError(t, err, "supply the existing actual bounded transaction capability to controller retirement")
			f.stores.Jobs = terminalJobs
			seedSavedReviewResults(t, pool, f)
			_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET result_origin='executed',decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{}',rendered_body='saved original output' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			require.NoError(t, err, "retain staged model output eligible for controller recovery")
			initialStatus := models.CodeReviewAssessmentRunning
			if tt.publication != "" {
				initialStatus = models.CodeReviewAssessmentPublishing
				_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='publishing',publication_state=$3,submitted_commit_sha=head_sha WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, tt.publication)
				require.NoError(t, err, "retain the existing publication safety state")
				if tt.publication == models.CodeReviewPublicationConfirmed {
					_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET publication_receipt='{"github_review_id":77}',github_review_id=77 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
					require.NoError(t, err, "retain the confirmed external receipt")
				}
			}
			if tt.rejectWrite {
				constraint := "reject_failure_" + strings.ReplaceAll(f.job.SessionID.String(), "-", "")
				_, err = pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE code_review_revision_assessments ADD CONSTRAINT %s CHECK (org_id <> '%s'::uuid OR status <> 'failed')`, constraint, f.job.OrgID))
				require.NoError(t, err, "force only this assessment's terminal write to fail")
			}
			initialAssessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "retain the full immutable assessment before terminal persistence")
			if tt.foreignOrg {
				outcome, retireErr := f.stores.Jobs.DeadLetterCodeReviewWithLease(ctx, uuid.New(), f.jobID, f.token, db.CodeReviewTerminalFailure{
					JobError: "foreign failure", Reason: "foreign failure", Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic foreign failure",
				})
				require.NoError(t, retireErr, "foreign tenant attempts must safely miss the fenced job")
				require.False(t, outcome.Owned(), "org_id must fence the job even when its ID and token match")
				unchanged, readErr := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
				require.NoError(t, readErr, "read the real tenant's assessment after foreign retirement")
				require.Equal(t, initialAssessment, unchanged, "foreign retirement must preserve every assessment field")
			}
			node := "terminal-race-" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register the fixture's queue consumer")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='pending',lock_token=NULL,attempts=0,max_attempts=1,priority=100000,target_node_id=$3,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, node)
			require.NoError(t, err, "make exactly the original controller claimable")
			schedule := db.NewCodeReviewScheduleStore(pool)
			beforeCleanup := models.CodeReviewAssessmentStatus("")
			terminalReason := ""
			hooks := 0
			w := New(pool, zerolog.Nop(), node, WithCodeReviewTerminalStore(terminalJobs))
			w.Register(models.JobTypeRunCodeReview, func(handlerCtx context.Context, _ string, _ json.RawMessage) error {
				// Force the formerly racy order: observe committed dead-lettering,
				// then sweep before the ordinary assessment cleanup hook runs.
				jobctx.RegisterDeadLetterHook(handlerCtx, func(hookCtx context.Context, terminalErr error) {
					hooks++
					terminalReason = codeReviewDeadLetterReason(terminalErr)
					a, readErr := f.stores.CodeReviewAssessments.GetByID(hookCtx, f.job.OrgID, f.assessment)
					require.NoError(t, readErr, "observe assessment before non-atomic cleanup hooks")
					beforeCleanup = a.Status
					require.NoError(t, schedule.RepairMissingWakes(hookCtx), "force recovery between terminal persistence and cleanup")
				})
				registerCodeReviewDeadLetterReconciliation(handlerCtx, f.stores, nil, zerolog.Nop(), f.job)
				if tt.loseLease {
					_, writeErr := pool.Exec(handlerCtx, `UPDATE jobs SET lock_token=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, uuid.New())
					require.NoError(t, writeErr, "give a newer owner the fencing token before terminal persistence")
				}
				return tt.err()
			})
			w.poll(ctx)
			require.NoError(t, schedule.RepairMissingWakes(ctx), "repeat repair after the terminal attempt")
			wantJob, wantAssessment, wantHooks := models.JobStatusDeadLetter, models.CodeReviewAssessmentFailed, 1
			if tt.loseLease || tt.rejectWrite {
				wantJob, wantAssessment, wantHooks = models.JobStatusRunning, models.CodeReviewAssessmentRunning, 0
			} else if tt.preserve {
				wantAssessment = initialStatus
			}
			var jobStatus models.JobStatus
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&jobStatus), "read the original job's durable terminal result")
			require.Equal(t, wantJob, jobStatus, "only a fully successful fenced terminal write may release the controller")
			a, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the assessment after terminal persistence and hooks")
			require.Equal(t, wantAssessment, a.Status, "the assessment must fail atomically or remain protected with its running controller")
			expectedAssessment := initialAssessment
			if wantAssessment == models.CodeReviewAssessmentFailed {
				expectedAssessment.Status = wantAssessment
				expectedAssessment.FailureDetail = &terminalReason
				expectedAssessment.CompletedAt = a.CompletedAt
				require.NotNil(t, a.CompletedAt, "atomic failure must retain a durable completion timestamp")
			}
			require.Equal(t, expectedAssessment, a, "retirement may change only failure status, detail, and completion time")
			require.Equal(t, wantHooks, hooks, "cleanup hooks must run only after successful fenced terminal persistence")
			if wantHooks == 1 {
				require.Equal(t, wantAssessment, beforeCleanup, "the first cleanup hook must already observe the final assessment state")
				if !tt.preserve {
					require.Equal(t, terminalReason, *a.FailureDetail, "atomic failure must preserve the domain's terminal reason")
				}
			}
			if tt.publication != "" {
				require.Equal(t, tt.publication, a.PublicationState, "dead-lettering must never downgrade the external publication fence")
			}
			require.Equal(t, "saved original output", *a.RenderedBody, "terminal persistence must retain the original model result")
			var counts []int
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type='run_code_review'),(SELECT count(*)::int FROM code_review_revision_assessments WHERE org_id=$1),(SELECT count(*)::int FROM code_review_agent_results WHERE org_id=$1)]`, f.job.OrgID).Scan(&counts), "count original durable review work after interleaved recovery")
			expectedCounts := []int{1, 1, 2}
			if tt.preserve {
				expectedCounts = []int{2, 1, 2}
				require.Nil(t, a.FailureDetail, "protected recovery must retain its original assessment rather than fail it")
			}
			require.Equal(t, expectedCounts, counts, "only protected publication or ownership recovery may restore an original controller")
		})
	}
}
