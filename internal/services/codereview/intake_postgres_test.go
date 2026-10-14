package codereview

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type intakeSnapshotFixture struct {
	*schedulingSnapshotFixture
	beforePrepare func()
	afterSnapshot func()
}

type intakeThreadCanceller struct {
	pool         *pgxpool.Pool
	beforeCancel func() error
}

func (c intakeThreadCanceller) CancelActiveThreads(ctx context.Context, org uuid.UUID, sessions []uuid.UUID) (int, error) {
	if err := c.beforeCancel(); err != nil {
		return 0, err
	}
	return (&schedulingThreadCanceller{pool: c.pool}).CancelActiveThreads(ctx, org, sessions)
}

// The snapshot case queues intake behind the hydration transaction's advisory
// lock; PostgreSQL grants that waiter before the dispatch transaction. The idle
// case admits intake during cancellation, after the wake skipped hydration.
func testSchedulingIntakeDispatchRace(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, idle bool) {
	t.Helper()
	ctx := context.Background()
	service, claim, baseline := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	_, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed',completed_at=now() WHERE org_id=$1 AND session_id=$2`, org, baseline)
	require.NoError(t, err, "complete the prior full review")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
	require.NoError(t, err, "retire the prior controller")
	in := ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, GitHubRepo: "test/repo", GitHubPRNumber: 17, GitHubPRURL: snapshot.snapshot.HTMLURL, PullRequestTitle: snapshot.snapshot.Title, PullRequestAuthor: snapshot.snapshot.AuthorLogin, HeadSHA: snapshot.snapshot.HeadSHA, BaseSHA: snapshot.snapshot.BaseSHA, ExplicitRequest: true, GitHubDeliveryID: uuid.NewString(), RequestedTeamSlug: "143-code-reviewer", TriggerSource: models.CodeReviewTriggerSourceTeamReviewer, ChangeReason: explicitReviewRequestChangeReason}
	stage := func() error {
		_, _, err := service.persistReviewIntake(ctx, in, models.CodeReviewReviewNow, true)
		return err
	}
	var intakeDone chan error
	if idle {
		_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='stale' WHERE org_id=$1 AND session_id=$2`, org, baseline)
		require.NoError(t, err, "prepare an idle wake with a draining stale thread")
		_, err = pool.Exec(ctx, `INSERT INTO session_threads(org_id,session_id,agent_type,label,status) VALUES($1,$2,'codex','Code review: stale intake race','running')`, org, baseline)
		require.NoError(t, err, "ensure cancellation runs between the wake's read and dispatch")
		service.SetThreadCanceller(intakeThreadCanceller{pool: pool, beforeCancel: stage})
		err = service.scheduling.store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, _ *models.CodeReviewPRState) error {
			return db.UpsertCodeReviewWake(ctx, tx, org, pr, time.Now().UTC())
		})
		require.NoError(t, err, "queue the idle wake")
	} else {
		older := in
		older.GitHubDeliveryID = uuid.NewString()
		_, _, err = service.persistReviewIntake(ctx, older, models.CodeReviewReviewNow, true)
		require.NoError(t, err, "queue the original wake's intake")
		// The newer webhook can describe a different target. Until refreshed,
		// older workers must retain the prior hydrated target's complete inputs.
		in.HeadSHA, in.BaseSHA, in.PullRequestTitle = strings.Repeat("c", 40), strings.Repeat("d", 40), "New webhook title"
		intakeDone = make(chan error, 1)
		service.scheduling.snapshots = intakeSnapshotFixture{schedulingSnapshotFixture: snapshot, afterSnapshot: func() {
			go func() { intakeDone <- stage() }()
			pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			for {
				var waiting bool
				err := pool.QueryRow(pollCtx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting)
				require.NoError(t, err, "intake must queue behind the hydration lock before its transaction releases")
				if waiting {
					break
				}
				time.Sleep(time.Millisecond)
			}
		}}
	}
	wake := models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "a wake must defer intake committed after its hydration phase")
	if intakeDone != nil {
		require.NoError(t, <-intakeDone, "concurrent intake must commit before the wake dispatch check")
	}
	service.scheduling.snapshots = snapshot
	service.SetThreadCanceller(&schedulingThreadCanceller{pool: pool})
	state, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read the retained incoming request")
	var pending scheduledReviewIntent
	require.NoError(t, json.Unmarshal(state.PendingInput, &pending), "new intake must remain pending until its own snapshot refresh")
	require.Equal(t, in.GitHubDeliveryID, pending.Input.GitHubDeliveryID, "the newer explicit delivery must remain the pending owner")
	require.True(t, pending.NeedsSnapshot, "new intake must explicitly require a locked snapshot refresh")
	var legacy struct {
		Input ReviewChangedInput `json:"input"`
		Force bool               `json:"force"`
	}
	require.NoError(t, json.Unmarshal(state.PendingInput, &legacy), "an older worker must decode the existing scheduler envelope")
	expectedTarget := in
	if !idle {
		expectedTarget.HeadSHA, expectedTarget.BaseSHA, expectedTarget.PullRequestTitle = snapshot.snapshot.HeadSHA, snapshot.snapshot.BaseSHA, snapshot.snapshot.Title
	}
	require.Equal(t, expectedTarget, legacy.Input, "legacy dispatch must receive a complete validated or previously hydrated target with the new request provenance")
	require.True(t, legacy.Force, "legacy workers must honor the intentional fresh-review request")
	require.Equal(t, models.CodeReviewWaitContext, state.WaitReason, "dispatch must wait for the new intake's snapshot")
	request, err := service.scheduling.store.GetRequestByIdentity(ctx, org, "github", in.GitHubDeliveryID)
	require.NoError(t, err, "read the incoming delivery receipt")
	require.Equal(t, "pending", request.Status, "the wake must not settle an unrefreshed request")
	require.Nil(t, request.SessionID, "unrefreshed intake must not bind a session")
	var heads []string
	require.NoError(t, pool.QueryRow(ctx, `SELECT array_agg(head_sha ORDER BY created_at,id) FROM code_review_session_metadata WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&heads), "inspect every allocated full review target")
	require.Equal(t, []string{snapshot.snapshot.HeadSHA}, heads, "the racing wake must not allocate a premature or empty-head review")
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "the next wake must hydrate and dispatch the preserved request")
	request, err = service.scheduling.store.GetRequestByIdentity(ctx, org, "github", in.GitHubDeliveryID)
	require.NoError(t, err, "read the recovered receipt")
	require.Equal(t, "satisfied", request.Status, "the original incoming delivery must settle without redelivery")
	require.NotNil(t, request.SessionID, "recovered intake must bind the fresh review")
	require.NotEqual(t, baseline, *request.SessionID, "an intentional team request must receive a fresh review")
}

func (f intakeSnapshotFixture) PrepareCodeReviewPullRequestSnapshot(ctx context.Context, org, repo uuid.UUID) (ghservice.CodeReviewPullRequestSnapshotReader, error) {
	if f.beforePrepare != nil {
		f.beforePrepare()
	}
	read, err := f.schedulingSnapshotFixture.PrepareCodeReviewPullRequestSnapshot(ctx, org, repo)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, number int) (ghservice.CodeReviewPullRequestSnapshot, error) {
		snapshot, err := read(ctx, number)
		if f.afterSnapshot != nil {
			f.afterSnapshot()
		}
		return snapshot, err
	}, nil
}

func testSchedulingDurableIntake(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, scenario string) {
	t.Helper()
	ctx := context.Background()
	if scenario == "single_connection" {
		config := pool.Config()
		config.MaxConns = 1
		limited, err := pgxpool.NewWithConfig(ctx, config)
		require.NoError(t, err, "create the production-shaped one-connection pool")
		t.Cleanup(limited.Close)
		pool = limited
	}
	store := db.NewCodeReviewStore(pool)
	var baseline uuid.UUID
	baselineCount := 0
	if scenario == "completed" || scenario == "queued" || scenario == "running" {
		_, _, baseline = schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
		baselineCount = 1
		status := models.CodeReviewSessionStatus(scenario)
		_, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET status=$3,completed_at=CASE WHEN $3='completed' THEN now() ELSE NULL END WHERE org_id=$1 AND session_id=$2`, org, baseline, status)
		require.NoError(t, err, "stage the pre-existing review lifecycle")
		if scenario == "completed" {
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
			require.NoError(t, err, "retire the completed source controller")
		} else {
			_, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET active_session_id=NULL WHERE org_id=$1 AND pull_request_id=$2`, org, pr)
			require.NoError(t, err, "exercise adoption of an active session without a prior schedule link")
		}
	} else {
		_, err := store.SavePolicy(ctx, org, models.DefaultCodeReviewPolicyConfig(), nil)
		require.NoError(t, err, "enable the first review's policy")
	}
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(3 * time.Minute)
	limited := &ghservice.GitHubAPIError{StatusCode: http.StatusForbidden, Body: []byte(`{"message":"API rate limit exceeded"}`), Header: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)}}}
	snapshot.err = limited
	if scenario == "credentials" {
		snapshot.err, snapshot.prepareErr = nil, limited
	}
	input := ReviewRequestedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, GitHubRepo: "test/repo", GitHubPRNumber: 17, HeadSHA: snapshot.snapshot.HeadSHA, BaseSHA: snapshot.snapshot.BaseSHA, RequestedTeam: "143-code-reviewer", DeliveryID: uuid.NewString()}
	preparations := 0
	observed := intakeSnapshotFixture{schedulingSnapshotFixture: snapshot, beforePrepare: func() {
		preparations++
		if scenario == "single_connection" {
			require.Zero(t, pool.Stat().AcquiredConns(), "credential preparation must release the intake transaction's sole connection")
		}
		// A separate pool acquisition must already observe committed intake;
		// assertions inside the hydration transaction would miss the crash gap.
		var counts []int
		err := pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM code_review_requests WHERE org_id=$1 AND pull_request_id=$2 AND source_identity=$3),(SELECT count(*)::int FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2 AND pending_input IS NOT NULL),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type=$4 AND status IN ('pending','running'))]`, org, pr, input.DeliveryID, models.JobTypeReconcileCodeReviewSchedule).Scan(&counts)
		require.NoError(t, err, "observe durable intake before preparing GitHub credentials")
		require.Equal(t, []int{1, 1, 1}, counts, "request, pending intent, and wake must all commit before provider work")
	}}
	newService := func() *Service {
		s := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{TeamSlugs: []string{"143-code-reviewer"}})
		s.SetScheduling(db.NewCodeReviewScheduleStore(pool), observed)
		s.scheduling.now = func() time.Time { return now }
		return s
	}
	service := newService()
	actual, err := service.HandleReviewRequested(ctx, input)
	require.NoError(t, err, "GitHub rate limiting must acknowledge the durably accepted request")
	require.Equal(t, ReviewRequestedResult{Processed: true, Deferred: true}, actual, "intake must wait without allocating a session")
	state, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "load the persisted provider wait")
	require.Equal(t, models.CodeReviewWaitContext, state.WaitReason, "the pending queue must expose unavailable GitHub context")
	require.Equal(t, reset, state.RetryAt.UTC(), "retry must honor the GitHub reset deadline")
	var pending scheduledReviewIntent
	require.NoError(t, json.Unmarshal(state.PendingInput, &pending), "decode recoverable intake")
	require.Equal(t, input.DeliveryID, pending.Input.GitHubDeliveryID, "retain the original delivery identity across recovery")
	require.Equal(t, input.RequestedTeam, pending.Input.RequestedTeamSlug, "retain reviewer cleanup provenance")
	require.Equal(t, input.HeadSHA, pending.Input.HeadSHA, "the validated webhook head must survive credential or snapshot failure")
	require.Equal(t, input.BaseSHA, pending.Input.BaseSHA, "the validated webhook base must survive credential or snapshot failure")
	require.Equal(t, input.GitHubRepo, pending.Input.GitHubRepo, "older workers must receive the full repository target")
	require.Equal(t, input.GitHubPRNumber, pending.Input.GitHubPRNumber, "older workers must receive the pull request number")
	service = newService() // Simulate losing every process-local intake value.
	replayed, err := service.HandleReviewRequested(ctx, input)
	require.NoError(t, err, "redelivery must replay committed intake during the outage")
	require.Equal(t, ReviewRequestedResult{Processed: true, Deferred: true, Reused: true}, replayed, "redelivery should acknowledge the same pending work")
	require.Equal(t, 1, preparations, "redelivery must not attempt another provider fetch")
	var coalesced *ReviewRequestedInput
	if scenario == "late_failure" || scenario == "same_delivery_late_failure" {
		older := input
		if scenario == "late_failure" {
			coalesced = &older
			input.DeliveryID = uuid.NewString()
		}
		snapshot.err = nil
		if scenario == "same_delivery_late_failure" {
			_, err = service.refreshSchedule(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr}, models.CodeReviewEnsureCurrent, false, nil)
		} else {
			_, err = service.HandleReviewRequested(ctx, input)
		}
		require.NoError(t, err, "admit a newer successfully hydrated request")
		before, err := service.GetSchedule(ctx, org, pr)
		require.NoError(t, err, "capture the newer request's eligibility")
		require.NoError(t, service.waitForReviewIntakeSnapshot(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, GitHubDeliveryID: older.DeliveryID}, limited), "a delayed error may only defer its own pending intent")
		after, err := service.GetSchedule(ctx, org, pr)
		require.NoError(t, err, "reload the newer request after delayed failure")
		before.UpdatedAt = after.UpdatedAt
		require.Equal(t, before, after, "late snapshot failure must preserve every field of the newer hydrated intent")
		snapshot.err = limited
	}
	claim := func() context.Context {
		lease := uuid.New()
		var id uuid.UUID
		err := pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND job_type=$2 AND status='pending' RETURNING id`, org, models.JobTypeReconcileCodeReviewSchedule, lease).Scan(&id)
		require.NoError(t, err, "claim the single recoverable intake wake")
		return jobctx.WithLockToken(jobctx.WithJobID(ctx, id), lease)
	}
	wake := models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "continued throttling must preserve intake and release the wake for retry")
	var attempts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT attempts FROM jobs WHERE org_id=$1 AND job_type=$2 AND status='pending'`, org, models.JobTypeReconcileCodeReviewSchedule).Scan(&attempts), "read the fenced retry attempt budget")
	require.Zero(t, attempts, "provider waits must not consume terminal job attempts")
	snapshot.err, snapshot.prepareErr = nil, nil
	now = reset
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "reset recovery must start or join review without a new team request")
	request, err := service.scheduling.store.GetRequestByIdentity(ctx, org, "github", input.DeliveryID)
	require.NoError(t, err, "load the original request's settlement")
	settledState, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read the settled generation")
	var targetGeneration int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT target_generation FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, request.ID).Scan(&targetGeneration), "read the original delivery's generation")
	require.Equal(t, settledState.Generation, targetGeneration, "the original request must follow its forced or hydrated generation")
	wantStatus, wantSessions := "satisfied", baselineCount+1
	if scenario == "queued" || scenario == "running" {
		wantStatus, wantSessions = "joined", baselineCount
		require.Equal(t, &baseline, request.SessionID, "equivalent active analysis must be joined")
		require.Equal(t, &baseline, settledState.ActiveSessionID, "joined intake must retain the session link for completion tracking")
	} else {
		require.NotNil(t, request.SessionID, "the accepted request must retain its fresh session identity")
		require.NotEqual(t, baseline, *request.SessionID, "completed results must not swallow the intentional team request")
	}
	require.Equal(t, wantStatus, request.Status, "the original request must settle after provider recovery")
	if coalesced != nil {
		older, err := service.scheduling.store.GetRequestByIdentity(ctx, org, "github", coalesced.DeliveryID)
		require.NoError(t, err, "read the coalesced older delivery")
		require.Equal(t, request.SessionID, older.SessionID, "coalesced deliveries must share the single new full review")
		require.Equal(t, "satisfied", older.Status, "the older accepted delivery must also settle")
	}
	var allocations []int
	require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM code_review_session_metadata WHERE org_id=$1 AND pull_request_id=$2),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type=$3)]`, org, pr, models.JobTypeRunCodeReview).Scan(&allocations), "count full review sessions and controller jobs")
	require.Equal(t, []int{wantSessions, wantSessions}, allocations, "recovery must allocate exactly one session and controller or join equivalent live work")
	beforeReplay := preparations
	_, err = service.HandleReviewRequested(ctx, input)
	require.NoError(t, err, "completed intake must remain idempotent")
	require.Equal(t, beforeReplay, preparations, "settled delivery replay must not fetch provider inputs")
}
