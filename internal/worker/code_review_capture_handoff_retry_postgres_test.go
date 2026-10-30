package worker

import (
	"context"
	"encoding/json"
	"errors"
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

func TestCaptureIdentityHandoffRetryWindowPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for the bounded capture handoff phase proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name   string
		expire bool
	}{
		{name: "expired generic GitHub wait cannot consume fresh durable handoff phase"},
		{name: "expired handoff phase still deadletters", expire: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			f.job.TriggeringDisputeID = nil
			_, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			require.NoError(t, err, "capture must precede immutable assessment creation")
			policy, err := f.stores.CodeReviews.SavePolicy(ctx, f.job.OrgID, models.DefaultCodeReviewPolicyConfig(), nil)
			require.NoError(t, err, "change policy after the controller's original external wait")
			pr, err := f.stores.PullRequests.GetByID(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "load source pull request")
			capture := &queuedIdentityCapture{result: codereviewsvc.AssessmentInputCaptureResult{Policy: policy, PullRequest: pr, Snapshot: ghservice.CodeReviewPullRequestSnapshot{Number: pr.GitHubPRNumber, Title: pr.Title, HTMLURL: pr.GitHubPRURL, State: "open", BaseSHA: "base", HeadSHA: f.job.HeadSHA}}}
			jobs := &queuedIdentityJobs{JobStore: f.stores.Jobs, err: errors.New("durable admission unavailable")}
			lifecycle := codereviewsvc.NewService(f.stores.CodeReviews, f.stores.CodeReviews, f.stores.Sessions, jobs, zerolog.Nop(), codereviewsvc.Config{})
			services := &Services{CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviewLifecycle: lifecycle}
			node := "handoff-phase-" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "isolate the actual controller claim")
			payload, err := json.Marshal(f.job)
			require.NoError(t, err, "retain immutable queued identity")
			_, err = pool.Exec(ctx, `UPDATE jobs SET payload=$3,status='pending',lock_token=NULL,attempts=7,max_attempts=8,priority=100000,target_node_id=$4,run_at=now(),retry_window_started_at=now()-interval '4 hours' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, payload, node)
			require.NoError(t, err, "delay original GitHub recovery beyond its generic two-hour deadline")
			if tt.expire {
				_, err = pool.Exec(ctx, `UPDATE jobs SET payload=jsonb_set(payload,'{_code_review_capture_identity_retry_started_at}',to_jsonb(now()-interval '121 minutes')) WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
				require.NoError(t, err, "expire only the distinct handoff phase")
			}
			worker := f.newWorker(t, pool, node)
			worker.Register(models.JobTypeRunCodeReview, newRunCodeReviewHandler(f.stores, services, zerolog.Nop()))
			worker.poll(ctx)
			var status models.JobStatus
			var attempts int
			var phaseStart time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts,(payload->>'_code_review_capture_identity_retry_started_at')::timestamptz FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&status, &attempts, &phaseStart), "read the lease-owned handoff retry checkpoint")
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read original review after handoff failure")
			if tt.expire {
				require.Equal(t, models.JobStatusDeadLetter, status, "capture handoff must retain its own finite two-hour deadline")
				require.Equal(t, models.CodeReviewSessionStatusFailed, metadata.Status, "handoff phase exhaustion must settle the original controller")
				return
			}
			require.Equal(t, models.JobStatusPending, status, "first handoff failure must not inherit an expired generic GitHub deadline")
			require.Equal(t, 7, attempts, "handoff admission waits should preserve ordinary attempts")
			require.WithinDuration(t, time.Now(), phaseStart, 5*time.Second, "handoff phase must begin only after successful capture first needs durable replacement")
			require.Equal(t, models.CodeReviewSessionStatusRunning, metadata.Status, "failed handoff admission must retain recoverable source work")
			_, err = pool.Exec(ctx, `UPDATE jobs SET run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
			require.NoError(t, err, "advance the next handoff attempt without sleeping")
			worker.poll(ctx)
			var repeated time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts,(payload->>'_code_review_capture_identity_retry_started_at')::timestamptz FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&status, &attempts, &repeated), "read the repeated handoff deadline")
			require.Equal(t, phaseStart, repeated, "repeated recovery must not extend the handoff deadline")
			require.Equal(t, models.JobStatusPending, status, "repeat admission failure should continue only within the same phase window")
			require.Equal(t, 7, attempts, "repeated admission wait must not consume ordinary controller attempts")
		})
	}
}
