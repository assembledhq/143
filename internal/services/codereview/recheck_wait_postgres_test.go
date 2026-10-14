package codereview

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Each case runs in the serial lifecycle table's isolated organization and PR.
func testRecheckPendingWait(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, scenario string) {
	t.Helper()
	testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	schedules := db.NewCodeReviewScheduleStore(pool)
	current, err := schedules.GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "read the active evidence review")
	manifest, err := decodeAssessmentManifest(current.InputManifest)
	require.NoError(t, err, "decode the active review's captured inputs")
	capture := ReviewInputCapture{Code: manifest.Code, Contract: manifest.Contract, Title: manifest.Title, Description: manifest.Description,
		Visual: manifest.Visual, TextEvidence: manifest.TextEvidence, Request: manifest.Request, Gates: manifest.Gates}
	capture.Visual.Images = []ReviewVisualImage{{SourceID: "waiting-image", SourceURL: "https://example.test/waiting.png", ContentDigest: strings.Repeat("d", 64)}}
	changed, err := BuildReviewInputManifest(capture)
	require.NoError(t, err, "capture new evidence that cannot join the active review")
	policy, err := store.ResolvePolicy(ctx, org)
	require.NoError(t, err, "resolve the continuation policy")
	require.NotNil(t, policy.Policy, "continuation capture requires its saved policy identity")
	fixture := assessmentAdmissionFixture{pool: pool, manifest: changed, policy: *policy.Policy, session: current.SessionID, snapshot: snapshot.snapshot}
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(schedules, snapshot)
	now := time.Now().UTC().Truncate(time.Microsecond)
	service.scheduling.now = func() time.Time { return now }
	captures := 0
	service.SetAssessmentContinuation(recheckCaptureFunc(func(ctx context.Context, input AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
		captures++
		if scenario != "active" {
			return AssessmentInputCaptureResult{}, &ghservice.GitHubAPIError{StatusCode: http.StatusForbidden, Body: []byte(`{"message":"API rate limit exceeded"}`), Header: http.Header{"X-Ratelimit-Remaining": {"0"}}}
		}
		return fixture.CaptureAssessmentInputs(ctx, input)
	}), true)
	request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck}
	queued, err := service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "pending recheck admission must persist a recoverable wait")
	require.Equal(t, models.CodeReviewRequestQueued, queued.Disposition, "changed evidence or throttled capture must wait")
	wantReason := models.CodeReviewWaitActive
	if scenario != "active" {
		wantReason = models.CodeReviewWaitContext
	}
	require.Equal(t, wantReason, queued.Schedule.WaitReason, "admission must retain the cause of its wait")
	now = now.Add(15 * time.Second)
	wantRetry := now.Add(15 * time.Second)
	if scenario == "later_retry" {
		wantRetry = now.Add(time.Minute)
		_, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET retry_at=$3 WHERE org_id=$1 AND pull_request_id=$2`, org, pr, wantRetry)
		require.NoError(t, err, "retain a newer retry deadline while an older wake is in flight")
	}
	captures = 0
	var wakeID uuid.UUID
	lease := uuid.New()
	err = pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND dedupe_key=$2 AND status='pending' RETURNING id`, org, "code_review_schedule:"+pr.String(), lease).Scan(&wakeID)
	require.NoError(t, err, "claim the due recheck wake")
	require.NoError(t, service.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, wakeID), lease), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "pending evidence rechecks must retain their own delayed admission path")
	type wakeRetry struct {
		Status   string
		Attempts int
		RunAt    time.Time
	}
	var actual wakeRetry
	require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts,run_at FROM jobs WHERE org_id=$1 AND id=$2`, org, wakeID).Scan(&actual.Status, &actual.Attempts, &actual.RunAt), "read the fenced recheck wake retry")
	actual.RunAt = actual.RunAt.UTC()
	expected := wakeRetry{Status: "pending", Attempts: 0, RunAt: wantRetry}
	require.Equal(t, expected, actual, "rechecks must retry after fifteen seconds without consuming attempts or immediately recapturing")
	state, err := schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read the preserved recheck admission state")
	require.Equal(t, wantReason, state.WaitReason, "the dispatch phase must preserve the active or unavailable-context wait")
	require.Equal(t, queued.Schedule.PendingRequestID, state.PendingRequestID, "the original recheck must remain the pending owner")
	require.NotNil(t, state.RetryAt, "pending rechecks must retain a retry deadline")
	require.Equal(t, expected.RunAt, state.RetryAt.UTC(), "queue state and owned wake must agree on the delayed retry")
	require.Equal(t, 1, captures, "one wake must attempt exactly one capture")
}
