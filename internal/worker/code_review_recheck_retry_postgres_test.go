package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRecheckPublicationRateLimitRecoveryPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for publication retry recovery proof")
	}
	tests := []struct {
		name      string
		uncertain bool
		submit    bool
	}{
		{name: "reserved capture and receipt lookup throttle"},
		{name: "uncertain capture and receipt lookup throttle", uncertain: true},
		{name: "reserved submit throttle", submit: true},
		{name: "uncertain submit throttle", uncertain: true, submit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPool(t, ctx)
			f, baseline := seedDeadLetterRecheck(t, pool)
			f.stores.CodeReviewRechecks = db.NewCodeReviewRecheckStore(pool)
			manifest := f.manifest
			manifest.InputDigest = "recheck-input"
			raw, err := json.Marshal(manifest)
			require.NoError(t, err, "encode the original saved recheck capture")
			publication := models.CodeReviewPublicationReserved
			if tt.uncertain {
				publication = models.CodeReviewPublicationUncertain
			}
			_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET input_manifest=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, raw)
			require.NoError(t, err, "retain the saved result and its immutable capture")
			require.NoError(t, f.stores.CodeReviewAssessments.ReservePublication(ctx, f.job.OrgID, f.assessment, 2, manifest.InputDigest, f.job.HeadSHA), "reserve publication with its real submitted-head fence")
			if tt.uncertain {
				require.NoError(t, f.stores.CodeReviewAssessments.MarkPublicationAttemptUncertain(ctx, f.job.OrgID, f.assessment, 2, manifest.InputDigest), "retain a prior real uncertain-send fence")
			}
			node := "recheck-retry-" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register an isolated publication worker")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='pending',lock_token=NULL,attempts=7,max_attempts=8,priority=100000,target_node_id=$3,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, node)
			require.NoError(t, err, "reproduce publication reaching the last ordinary attempt")
			reset := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
			throttle := fmt.Errorf("GitHub publication operation: %w", &ghservice.GitHubAPIError{StatusCode: http.StatusForbidden, Header: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {fmt.Sprint(reset.Unix())}}, Body: []byte(`{"message":"API rate limit exceeded for installation ID 42"}`)})
			capture := &fixedRecheckCapture{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest}}
			publisher := &fakeRecheckPublisher{}
			if tt.submit {
				publisher.submitErr = throttle
			} else {
				capture.captureErr, publisher.reconcileErr = throttle, throttle
			}
			services := &Services{CodeReviewInputCapture: capture, CodeReviews: publisher}
			worker := New(pool, zerolog.Nop(), node)
			worker.Register(models.JobTypeRunCodeReviewRecheck, newRunCodeReviewRecheckHandler(f.stores, services, zerolog.Nop()))
			worker.poll(ctx)
			var status models.JobStatus
			var attempts int
			var retryStart *time.Time
			var runAt time.Time
			var lastError *string
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts,retry_window_started_at,run_at,last_error FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&status, &attempts, &retryStart, &runAt, &lastError), "read the durable publication recovery schedule")
			require.Equal(t, models.JobStatusPending, status, "publication throttling must defer the original supervisor on its eighth claim: %s", stringPtrValue(lastError))
			require.Equal(t, 7, attempts, "publication throttling must refund the ordinary attempt")
			require.NotNil(t, retryStart, "publication must persist its finite GitHub recovery window")
			require.WithinDuration(t, reset.Add(15*time.Second), runAt, 15*time.Second, "publication must wait for GitHub's reset with bounded jitter")
			waiting, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the retained original staged result")
			require.Equal(t, models.CodeReviewAssessmentPublishing, waiting.Status, "throttling must retain completed continuation work for publication recovery")
			if tt.submit {
				publication = models.CodeReviewPublicationUncertain
				require.Equal(t, 1, len(publisher.requests), "one failed send attempt must remain fenced")
			} else {
				require.Empty(t, publisher.requests, "failed freshness capture must not send while attempting receipt reconciliation")
				require.Equal(t, 1, len(publisher.reconciliations), "failed capture must still reconcile any prior uncertain send")
			}
			require.Equal(t, publication, waiting.PublicationState, "rate-limit recovery must preserve the correct unsent or uncertain fence")
			capture.captureErr, publisher.reconcileErr, publisher.submitErr = nil, nil, nil
			_, err = pool.Exec(ctx, `UPDATE jobs SET run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
			require.NoError(t, err, "advance the isolated fixture past GitHub reset without sleeping")
			worker.poll(ctx)
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,last_error FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&status, &lastError), "read the same supervisor after recovery")
			require.Equal(t, models.JobStatusSucceeded, status, "the original publication supervisor must finish after recovery: %s", stringPtrValue(lastError))
			completed, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the same published evidence assessment")
			require.Equal(t, models.CodeReviewAssessmentCompleted, completed.Status, "the saved continuation must publish without replacement")
			require.Equal(t, models.CodeReviewPublicationConfirmed, completed.PublicationState, "recovery must persist a confirmed receipt")
			require.Equal(t, waiting.InputManifest, completed.InputManifest, "publication retries must retain the immutable capture")
			require.Equal(t, waiting.RenderedBody, completed.RenderedBody, "publication retries must retain the saved review body")
			retainedBaseline, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, baseline.ID)
			require.NoError(t, err, "read the immutable source full review after publication")
			require.Equal(t, baseline, retainedBaseline, "retrying evidence publication must not rewrite its source review")
			var counts []int
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM code_review_revision_assessments WHERE org_id=$1),(SELECT count(*)::int FROM code_review_agent_results WHERE org_id=$1),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type IN ('run_agent','continue_session','prepare_code_review_workspace'))]`, f.job.OrgID).Scan(&counts), "count preserved source and continuation work")
			require.Equal(t, []int{2, 2, 0}, counts, "recovery must preserve both assessments and original reviewers without a new model dispatch")
		})
	}
}
