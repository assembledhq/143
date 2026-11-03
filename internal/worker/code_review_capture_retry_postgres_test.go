package worker

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestFullAssessmentCaptureRateLimitLifecyclePostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for actual assessment capture recovery proof")
	}
	tests := []struct {
		name    string
		initial bool
		staged  bool
		expire  bool
	}{
		{name: "initial capture preserves last attempt", initial: true},
		{name: "final freshness preserves completed reviewer work"},
		{name: "staged result resumes after reset", staged: true},
		{name: "prolonged throttle retains finite recovery bound", staged: true, expire: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPool(t, ctx)
			f := seedStrandedReview(t, pool, "idle")
			f.stores.SessionLogs = nil
			seedSavedReviewResults(t, pool, f)
			if tt.initial {
				_, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
				require.NoError(t, err, "start before the initial immutable capture exists")
			}
			if tt.staged {
				_, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET result_origin='executed',decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{}',rendered_body='original completed review' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
				require.NoError(t, err, "retain the saved result before its publication freshness check")
			}
			node := "capture-retry-" + uuid.NewString()
			_, err := pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register a worker isolated from other test controllers")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='pending',lock_token=NULL,attempts=7,max_attempts=8,priority=100000,target_node_id=$3,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, node)
			require.NoError(t, err, "reproduce the original controller reaching its last ordinary attempt")
			reset := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
			capture := &fixedRecheckCapture{captureErr: fmt.Errorf("load pull request for code review mention: %w", &ghservice.GitHubAPIError{StatusCode: http.StatusForbidden, Method: "GET", Path: "/repos/test/saved-review/pulls/9", Header: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {fmt.Sprint(reset.Unix())}}, Body: []byte(`{"message":"API rate limit exceeded for installation ID 42"}`)})}
			publisher := &inlineRecoveryPublisher{}
			visual := models.CodeReviewVisualEvidenceSnapshot{Version: 1, AssessmentID: &f.assessment, RepositoryID: f.job.RepositoryID, PullRequestNumber: 9, HeadSHA: f.job.HeadSHA, Complete: true}
			services := &Services{CodeReviewInputCapture: capture, CodeReviewAssessmentsEnabled: true, CodeReviewVisualEvidence: &assessmentVisualCheckpoint{snapshot: visual}, CodeReviews: publisher}
			worker := New(pool, zerolog.Nop(), node)
			worker.Register(models.JobTypeRunCodeReview, newRunCodeReviewHandler(f.stores, services, zerolog.Nop()))
			worker.poll(ctx)
			var status models.JobStatus
			var attempts int
			var retryStart *time.Time
			var runAt time.Time
			var lastError *string
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts,retry_window_started_at,run_at,last_error FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&status, &attempts, &retryStart, &runAt, &lastError), "read the lease-owned recovery schedule")
			require.Equal(t, models.JobStatusPending, status, "capture throttling must defer instead of dead-lettering on the eighth claim: %s", stringPtrValue(lastError))
			require.Equal(t, 7, attempts, "the rate-limit wait must refund the claimed ordinary attempt")
			require.NotNil(t, retryStart, "the GitHub recovery budget must start durably")
			require.WithinDuration(t, time.Now(), *retryStart, 5*time.Second, "recovery must retain a current durable start time")
			require.WithinDuration(t, reset.Add(15*time.Second), runAt, 15*time.Second, "the actual job must wait for reset with only bounded jitter")
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read durable automatic-wait status for the original session")
			require.Equal(t, models.CodeReviewSessionStatusRunning, metadata.Status, "the original session must remain live during throttling")
			require.Equal(t, new(models.CodeReviewStatusCodeGitHubRateLimited), metadata.StatusCode, "the UI must retain automatic recovery rather than a terminal retry instruction")
			require.Equal(t, &runAt, metadata.RetryAt, "status and controller must report the same actual recovery deadline")
			require.Empty(t, publisher.requests, "a failed freshness capture must not publish or duplicate an external review")
			if tt.initial {
				return
			}
			before, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the original assessment after the rate-limit wait")
			require.Equal(t, models.CodeReviewAssessmentRunning, before.Status, "a saved assessment must remain recoverable")
			require.Equal(t, models.CodeReviewPublicationNotStarted, before.PublicationState, "waiting for GitHub must preserve the unsent publication fence")
			if tt.expire {
				_, err = pool.Exec(ctx, `UPDATE jobs SET retry_window_started_at=now()-interval '121 minutes' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
				require.NoError(t, err, "expire only the durable external recovery budget")
			} else {
				capture.captureErr = nil
				capture.result = codereviewsvc.AssessmentInputCaptureResult{Manifest: f.manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}}
			}
			_, err = pool.Exec(ctx, `UPDATE jobs SET run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
			require.NoError(t, err, "advance the test to the next controller claim without sleeping")
			worker.poll(ctx)
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&status), "read the same controller after recovery or budget expiry")
			after, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the same immutable assessment after the second claim")
			if tt.expire {
				require.Equal(t, models.JobStatusDeadLetter, status, "sustained throttling must still honor the finite two-hour bound")
				require.Equal(t, models.CodeReviewAssessmentFailed, after.Status, "duration exhaustion must atomically settle the unsent assessment")
				require.Empty(t, publisher.requests, "an exhausted recovery window must never publish")
			} else {
				require.Equal(t, models.JobStatusSucceeded, status, "GitHub recovery must finish the original controller")
				require.Equal(t, models.CodeReviewAssessmentCompleted, after.Status, "GitHub recovery must finish the original assessment")
				require.Equal(t, 1, len(publisher.requests), "the saved outcome must publish exactly once")
				require.Equal(t, before.InputManifest, after.InputManifest, "recovery must preserve the original immutable capture")
			}
			var counts []int
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM code_review_revision_assessments WHERE org_id=$1),(SELECT count(*)::int FROM code_review_agent_results WHERE org_id=$1),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type IN ('run_agent','continue_session','prepare_code_review_workspace'))]`, f.job.OrgID).Scan(&counts), "count immutable assessment and reviewer evidence after recovery")
			require.Equal(t, []int{1, 2, 0}, counts, "recovery must retain one assessment and its original reviewer work without model fanout")
		})
	}
}
