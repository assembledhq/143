package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Each case uses the migrated lifecycle suite's isolated synthetic tenant.
func testRecheckCaptureClockFences(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, mode string) {
	t.Helper()
	testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	schedules := db.NewCodeReviewScheduleStore(pool)
	current, err := schedules.GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "load the existing captured assessment")
	manifest, err := decodeAssessmentManifest(current.InputManifest)
	require.NoError(t, err, "decode complete synthetic capture inputs")
	policy, err := store.ResolvePolicy(ctx, org)
	require.NoError(t, err, "read the captured policy identity")
	fixture := assessmentAdmissionFixture{pool: pool, manifest: manifest, policy: *policy.Policy, session: current.SessionID, snapshot: snapshot.snapshot}
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(schedules, snapshot)
	now := time.Now().UTC().Truncate(time.Microsecond)
	service.scheduling.now = func() time.Time { return now }
	service.SetAssessmentContinuation(recheckCaptureFunc(func(context.Context, AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
		return AssessmentInputCaptureResult{}, errors.New("synthetic initial capture failure")
	}), true)
	request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck}
	_, err = service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "initial actual failure should create durable intake and episode")
	now = now.Add(30 * time.Second)
	var expected models.CodeReviewPRState
	var expectedStarted *time.Time
	var expectedTracked bool
	service.SetAssessmentContinuation(recheckCaptureFunc(func(ctx context.Context, input AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
		_, err := pool.Exec(ctx, `UPDATE code_review_pr_state SET generation=generation+1,head_sha='newer-synthetic-head' WHERE org_id=$1 AND pull_request_id=$2`, org, pr)
		require.NoError(t, err, "advance the durable target while an older capture is outside the PR lock")
		if strings.HasSuffix(mode, "newer request") {
			newer := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck}
			hash, err := reviewRequestHash(pr, newer.Mode, nil, false)
			require.NoError(t, err, "hash the new request identity")
			queued, err := service.queuePendingAssessmentCapture(ctx, newer, repo, nil, hash, "github")
			require.NoError(t, err, "persist the new pending owner independently of old capture")
			_, err = pool.Exec(ctx, `UPDATE code_review_requests SET capture_failure_started_at=$3,capture_failure_clock_tracked=false WHERE org_id=$1 AND id=$2`, org, queued.Schedule.PendingRequestID, now.Add(-3*time.Minute))
			require.NoError(t, err, "give the newer owner its own durable legacy episode")
		}
		expected, err = schedules.Get(ctx, org, pr)
		require.NoError(t, err, "read the exact new target before stale capture returns")
		require.NoError(t, pool.QueryRow(ctx, `SELECT capture_failure_started_at,capture_failure_clock_tracked FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, expected.PendingRequestID).Scan(&expectedStarted, &expectedTracked), "read the protected newer episode")
		if strings.HasPrefix(mode, "failure") {
			return AssessmentInputCaptureResult{}, errors.New("stale synthetic capture failure")
		}
		return fixture.CaptureAssessmentInputs(ctx, input)
	}), true)
	_, err = service.requestAssessmentReview(ctx, request, true)
	require.NoError(t, err, "stale capture must defer or replay without modifying newer ownership")
	actual, err := schedules.Get(ctx, org, pr)
	require.NoError(t, err, "read pending work after stale capture")
	require.Equal(t, expected.PendingRequestID, actual.PendingRequestID, "stale capture must preserve the exact current owner")
	require.Equal(t, expected.PendingInput, actual.PendingInput, "stale capture cannot rewrite newer pending provenance")
	require.Equal(t, expected.FirstPendingAt, actual.FirstPendingAt, "request age must remain byte-identical")
	require.Equal(t, expected.RetryAt, actual.RetryAt, "stale capture must not reset the newer retry deadline")
	var actualStarted *time.Time
	var actualTracked bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT capture_failure_started_at,capture_failure_clock_tracked FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, actual.PendingRequestID).Scan(&actualStarted, &actualTracked), "read unchanged episode after stale result")
	require.Equal(t, expectedStarted, actualStarted, "stale success or failure cannot end, start or renew a newer-head episode")
	require.Equal(t, expectedTracked, actualTracked, "stale success cannot promote unknown legacy timing")
	var assessments int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_revision_assessments WHERE org_id=$1`, org).Scan(&assessments), "read protected assessment clocks")
	require.Equal(t, 2, assessments, "stale capture cannot allocate a new assessment or clock")
}

func testRecheckCaptureClockMigration(t *testing.T, pool *pgxpool.Pool, org, _, _ uuid.UUID, _ *schedulingSnapshotFixture) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "open an isolated temporary-table migration proof")
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE organizations(id uuid PRIMARY KEY) ON COMMIT DROP`)
	require.NoError(t, err, "isolate the temporary request's tenant foreign key")
	_, err = tx.Exec(ctx, `INSERT INTO organizations(id) VALUES($1)`, org)
	require.NoError(t, err, "seed only the temporary synthetic organization")
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE code_review_requests(id uuid PRIMARY KEY,org_id uuid NOT NULL REFERENCES organizations(id)) ON COMMIT DROP`)
	require.NoError(t, err, "shadow only this connection's migration target with synthetic legacy rows")
	legacy, newRequest := uuid.New(), uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO code_review_requests(id,org_id) VALUES($1,$2)`, legacy, org)
	require.NoError(t, err, "seed a request before the clock migration")
	up, err := os.ReadFile("../../../migrations/000311_code_review_capture_failure_episode.up.sql")
	require.NoError(t, err, "read the exact public up migration")
	_, err = tx.Exec(ctx, string(up))
	require.NoError(t, err, "migrate existing synthetic request timing")
	_, err = tx.Exec(ctx, `INSERT INTO code_review_requests(id,org_id) VALUES($1,$2)`, newRequest, org)
	require.NoError(t, err, "insert a new request after the migration")
	for _, tt := range []struct {
		id      uuid.UUID
		tracked bool
	}{{legacy, false}, {newRequest, true}} {
		var tracked bool
		var started *time.Time
		require.NoError(t, tx.QueryRow(ctx, `SELECT capture_failure_clock_tracked,capture_failure_started_at FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, tt.id).Scan(&tracked, &started), "read exact default and legacy discrimination")
		require.Equal(t, tt.tracked, tracked, "migration must preserve unknown legacy state and track only new requests by default")
		require.Nil(t, started, "migration must not invent a provider failure without an actual failed capture")
	}
	down, err := os.ReadFile("../../../migrations/000311_code_review_capture_failure_episode.down.sql")
	require.NoError(t, err, "read the exact public down migration")
	_, err = tx.Exec(ctx, string(down))
	require.NoError(t, err, "rollback only the temporary synthetic clock schema")
	var count int
	require.NoError(t, tx.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_requests WHERE org_id=$1`, org).Scan(&count), "read preserved request identities after rollback")
	require.Equal(t, 2, count, "clock rollback must preserve durable request rows")
}

// Capture is deliberately blocked outside the PR lock while a second
// connection commits authoritative synchronize or explicit-request intake.
// Each parallel case has its own tenant; this suite performs no global repair.
func TestCodeReviewCaptureOwnershipPostgres(t *testing.T) {
	t.Parallel()
	raw := os.Getenv("CODE_REVIEW_TEST_DATABASE_URL")
	if raw == "" {
		raw = os.Getenv("TEST_DATABASE_URL")
	}
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL or CODE_REVIEW_TEST_DATABASE_URL to a disposable PostgreSQL server")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect disposable PostgreSQL administrative connection")
	t.Cleanup(func() { require.NoError(t, admin.Close(ctx), "close capture-test administrative connection") })
	name := "review_capture_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE DATABASE `+name)
	require.NoError(t, err, "create isolated capture-race database")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP DATABASE `+name+` WITH (FORCE)`)
		require.NoError(t, err, "drop only this capture-race database")
	})
	parsed, err := url.Parse(raw)
	require.NoError(t, err, "parse disposable PostgreSQL URL")
	parsed.Path = "/" + name
	source, err := filepath.Abs("../../../migrations")
	require.NoError(t, err, "resolve real public migrations")
	migration, err := migrate.New("file://"+source, parsed.String())
	require.NoError(t, err, "initialize isolated capture-race migrations")
	require.NoError(t, migration.Up(), "migrate the entire real capture-race schema")
	sourceErr, dbErr := migration.Close()
	require.NoError(t, sourceErr, "close capture-race migration source")
	require.NoError(t, dbErr, "close capture-race migration database")
	poolConfig, err := pgxpool.ParseConfig(parsed.String())
	require.NoError(t, err, "parse isolated capture-race pool configuration")
	poolConfig.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	require.NoError(t, err, "connect bounded capture-race pool")
	t.Cleanup(pool.Close)

	type captureCase struct {
		name, owner, outcome, revision string
		paused, ui, legacy             bool
	}
	tests := []captureCase{
		{name: "current permanent failure is terminal", owner: "current", outcome: "permanent"},
		{name: "current permanent wake is terminal while paused", owner: "current", outcome: "permanent", paused: true},
		{name: "current closed wake is terminal", owner: "current", outcome: "closed"},
		{name: "current closed wake is terminal while paused", owner: "current", outcome: "closed", paused: true},
		{name: "successful current capture promotes legacy episode", owner: "promotion", outcome: "success", legacy: true},
		{name: "successful current capture promotes legacy episode while paused", owner: "promotion", outcome: "success", legacy: true, paused: true},
	}
	for _, paused := range []bool{false, true} {
		for _, outcome := range []string{"success", "transient", "permanent", "closed", "ineligible"} {
			tests = append(tests, captureCase{name: fmt.Sprintf("fresh closed target %s paused=%t", outcome, paused), owner: "closed target", outcome: outcome, paused: paused})
		}
		for _, ui := range []bool{false, true} {
			for _, outcome := range []string{"success", "transient", "permanent", "closed", "ineligible"} {
				tests = append(tests, captureCase{name: fmt.Sprintf("fresh nil owner %s paused=%t ui=%t", outcome, paused, ui), owner: "fresh", outcome: outcome, revision: "head", paused: paused, ui: ui})
			}
		}
		for _, outcome := range []string{"success", "transient", "permanent", "closed", "ineligible"} {
			for _, revision := range []string{"head", "base", "ref", "generation"} {
				tests = append(tests, captureCase{name: fmt.Sprintf("same owner %s %s paused=%t", outcome, revision, paused), owner: "same", outcome: outcome, revision: revision, paused: paused, legacy: true})
			}
			for _, owner := range []string{"newer", "force"} {
				tests = append(tests, captureCase{name: fmt.Sprintf("%s owner %s paused=%t", owner, outcome, paused), owner: owner, outcome: outcome, revision: "head", paused: paused})
			}
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, integration, repo, pr := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Capture race test')`, org)
			require.NoError(t, err, "seed isolated synthetic capture-race tenant")
			_, err = pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, org)
			require.NoError(t, err, "seed synthetic capture-race installation")
			_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,$4,'test/repo','https://github.com/test/repo.git',17)`, repo, org, integration, int64(repo.ID()))
			require.NoError(t, err, "seed isolated capture-race repository")
			_, err = pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title) VALUES($1,$2,17,'https://github.com/test/repo/pull/17','test/repo','Scheduling test')`, pr, org)
			require.NoError(t, err, "seed isolated capture-race pull request")
			snapshot := &schedulingSnapshotFixture{snapshot: ghservice.CodeReviewPullRequestSnapshot{Number: 17, State: "open", Title: "Scheduling test", HTMLURL: "https://github.com/test/repo/pull/17", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), BaseRef: "main"}}
			testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
			store := db.NewCodeReviewStore(pool)
			schedules := db.NewCodeReviewScheduleStore(pool)
			assessments := db.NewCodeReviewAssessmentStore(pool)
			previous, err := schedules.GetLatestAssessment(ctx, org, pr)
			require.NoError(t, err, "load synthetic evidence assessment before racing capture")
			require.NoError(t, assessments.Fail(ctx, org, previous.ID, previous.Generation, previous.InputDigest, "synthetic fixture finished"), "settle only the synthetic evidence attempt")
			require.NoError(t, schedules.SettleAssessment(ctx, org, previous.ID), "release the synthetic evidence assessment ownership")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND status IN ('pending','running')`, org)
			require.NoError(t, err, "finish only this tenant's setup jobs")
			baseline, err := schedules.GetLatestFullBaseline(ctx, org, pr)
			require.NoError(t, err, "read completed output protected throughout the race")
			manifest, err := decodeAssessmentManifest(previous.InputManifest)
			require.NoError(t, err, "decode synthetic successful-capture manifest")
			policy, err := store.ResolvePolicy(ctx, org)
			require.NoError(t, err, "load synthetic capture policy")
			captureFixture := assessmentAdmissionFixture{pool: pool, manifest: manifest, policy: *policy.Policy, session: previous.SessionID, snapshot: snapshot.snapshot}
			service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
			service.SetScheduling(schedules, snapshot)
			now := time.Now().UTC().Truncate(time.Microsecond)
			service.scheduling.now = func() time.Time { return now }
			_, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET automatic_paused=$3 WHERE org_id=$1 AND pull_request_id=$2`, org, pr, tt.paused)
			require.NoError(t, err, "set the isolated automatic-review pause preference")
			request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck, RequestContext: &ReviewRequestContext{Source: "ui", Body: "Please verify the synthetic acceptance criteria."}}
			kind := "github"
			if tt.ui {
				user := uuid.New()
				_, err := pool.Exec(ctx, `INSERT INTO users(id,org_id,email,name) VALUES($1,$2,$3,'Capture tester')`, user, org, user.String()+"@example.test")
				require.NoError(t, err, "seed tenant-scoped UI requester identity")
				request.RequesterID = &user
				kind = "ui"
			}
			if tt.owner != "fresh" && tt.owner != "closed target" {
				service.SetAssessmentContinuation(recheckCaptureFunc(func(context.Context, AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
					return AssessmentInputCaptureResult{}, errors.New("synthetic initial provider outage")
				}), true)
				_, err := service.RequestScheduledReview(ctx, request)
				require.NoError(t, err, "persist explicit pending intake before recovery capture")
				if tt.legacy {
					record, err := schedules.GetRequestByIdentity(ctx, org, kind, request.RequestID.String())
					require.NoError(t, err, "load the pending request's immutable identity")
					_, err = pool.Exec(ctx, `UPDATE code_review_requests SET capture_failure_clock_tracked=false,capture_failure_started_at=$3 WHERE org_id=$1 AND id=$2`, org, record.ID, now.Add(-20*time.Minute))
					require.NoError(t, err, "represent an expired legacy episode that a stale outcome must preserve")
				}
			}
			captures := 0
			entered, release := make(chan struct{}), make(chan struct{})
			service.SetAssessmentContinuation(recheckCaptureFunc(func(ctx context.Context, input AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
				captures++
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return AssessmentInputCaptureResult{}, ctx.Err()
				}
				switch tt.outcome {
				case "transient":
					return AssessmentInputCaptureResult{}, errors.New("synthetic stale provider outage")
				case "permanent":
					return AssessmentInputCaptureResult{}, ErrAssessmentReuseUnavailable
				case "closed":
					return AssessmentInputCaptureResult{Snapshot: ghservice.CodeReviewPullRequestSnapshot{State: "closed"}}, ErrReviewIneligible
				case "ineligible":
					return AssessmentInputCaptureResult{}, ErrReviewIneligible
				default:
					return captureFixture.CaptureAssessmentInputs(ctx, input)
				}
			}), true)
			captureCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			type captureResponse struct {
				result ScheduleRequestResult
				err    error
			}
			done := make(chan captureResponse, 1)
			go func() {
				result, err := service.requestAssessmentReview(captureCtx, request, tt.owner != "fresh" && tt.owner != "closed target")
				done <- captureResponse{result: result, err: err}
			}()
			select {
			case <-entered:
			case <-captureCtx.Done():
				t.Fatal("capture must enter before concurrent intake changes its target")
			}
			if tt.owner != "current" && tt.owner != "promotion" {
				snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) {
					if tt.owner == "closed target" {
						s.State = "closed"
					}
					switch tt.revision {
					case "head":
						s.HeadSHA = strings.Repeat("c", 40)
					case "base":
						s.BaseSHA = strings.Repeat("d", 40)
					case "ref":
						s.BaseRef = "release"
					}
				})
				if tt.revision == "generation" {
					require.NoError(t, schedules.WithLockedPR(ctx, org, repo, pr, func(_ pgx.Tx, state *models.CodeReviewPRState) error {
						state.Generation++
						return nil
					}), "advance only the durable generation under the PR lock")
				} else {
					_, err := service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
					require.NoError(t, err, "commit real synchronize intake while capture is outside the PR lock")
				}
				if tt.owner == "newer" || tt.owner == "force" {
					newer := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck, Reason: "Newer explicit synthetic intent"}
					if tt.owner == "force" {
						newer.Mode = models.CodeReviewForceFresh
						_, err := service.RequestScheduledReview(ctx, newer)
						require.NoError(t, err, "commit true newer force-fresh ownership during the blocked capture")
					} else {
						hash, err := reviewRequestHash(pr, newer.Mode, assessmentRequestContext(newer), false)
						require.NoError(t, err, "hash the newer request's distinct context")
						_, err = service.queuePendingAssessmentCapture(ctx, newer, repo, assessmentRequestContext(newer), hash, "github")
						require.NoError(t, err, "commit independent newer explicit ownership")
					}
				}
			}
			expected, err := schedules.Get(ctx, org, pr)
			require.NoError(t, err, "read the exact committed target before releasing stale capture")
			var expectedStarted *time.Time
			var expectedTracked bool
			if expected.PendingRequestID != nil {
				require.NoError(t, pool.QueryRow(ctx, `SELECT capture_failure_started_at,capture_failure_clock_tracked FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, expected.PendingRequestID).Scan(&expectedStarted, &expectedTracked), "read the current owner's exact capture episode")
			}
			close(release)
			var response captureResponse
			select {
			case response = <-done:
			case <-captureCtx.Done():
				t.Fatal("capture reconciliation must complete after concurrent target commit")
			}
			actual, err := schedules.Get(ctx, org, pr)
			require.NoError(t, err, "read schedule after old capture result is reconciled")
			record, err := schedules.GetRequestByIdentity(ctx, org, kind, request.RequestID.String())
			require.NoError(t, err, "read explicit request ledger after capture reconciliation")
			if tt.owner == "promotion" {
				require.NoError(t, response.err, "successful current capture must remain admissible with legacy timing")
				var actualStarted *time.Time
				var actualTracked bool
				require.NoError(t, pool.QueryRow(ctx, `SELECT capture_failure_started_at,capture_failure_clock_tracked FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, record.ID).Scan(&actualStarted, &actualTracked), "read the successfully ended provider-failure episode")
				require.Nil(t, actualStarted, "actual success on the current target must end the old provider episode")
				require.True(t, actualTracked, "actual success on the current target must promote legacy clock provenance")
				require.NotNil(t, response.result.AssessmentID, "successful evidence capture must allocate the next assessment")
			} else if tt.owner == "closed target" {
				require.NoError(t, response.err, "a durable closed target must settle the obsolete fresh request")
				require.Equal(t, "cancelled", record.Status, "synchronize closure must durably reject fresh intake")
				require.Equal(t, models.CodeReviewRequestCancelled, response.result.Disposition, "fresh capture must observe authoritative closure")
				actual.UpdatedAt = expected.UpdatedAt
				require.Equal(t, expected, actual, "old capture must preserve the entire authoritative closed schedule")
			} else if tt.owner == "current" {
				if tt.outcome == "permanent" {
					require.ErrorIs(t, response.err, ErrRecheckUnavailable, "current-target permanent failure must still reject durably")
					require.Equal(t, "failed", record.Status, "current-target permanent failure must settle its request ledger")
				} else {
					require.NoError(t, response.err, "current-target closed recovery must settle normally")
					require.Equal(t, "cancelled", record.Status, "current-target closed capture must cancel its ledger")
					require.Equal(t, models.CodeReviewScheduleClosed, actual.State, "current-target closed capture must retain terminal schedule")
				}
				require.Nil(t, actual.PendingRequestID, "terminal current-target result must release its own pending owner")
				require.Nil(t, actual.PendingInput, "terminal current-target result must release its own pending intent")
				require.Nil(t, actual.FirstPendingAt, "terminal current-target result must clear its own request age")
				require.Nil(t, actual.RetryAt, "terminal current-target result must clear its own retry clock")
			} else {
				require.NoError(t, response.err, "old capture outcome must never fail the newer target's admission")
				require.Equal(t, expected.Generation, actual.Generation, "capture reconciliation must preserve the current target generation")
				require.Equal(t, expected.HeadSHA, actual.HeadSHA, "capture reconciliation must retain synchronized current head")
				require.Equal(t, expected.BaseSHA, actual.BaseSHA, "capture reconciliation must retain synchronized current base")
				require.Equal(t, expected.BaseRef, actual.BaseRef, "capture reconciliation must retain synchronized current base ref")
				if tt.owner == "fresh" {
					require.Nil(t, expected.PendingRequestID, "synchronize must leave no competing explicit owner for fresh intake")
					require.Equal(t, "pending", record.Status, "fresh explicit intake must survive a revision race")
					require.Equal(t, &record.ID, actual.PendingRequestID, "fresh caller must become the current target's explicit owner")
					var targetGeneration int64
					var captureStarted *time.Time
					var captureTracked bool
					require.NoError(t, pool.QueryRow(ctx, `SELECT target_generation,capture_failure_started_at,capture_failure_clock_tracked FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, record.ID).Scan(&targetGeneration, &captureStarted, &captureTracked), "read the fresh request's current target and episode provenance")
					require.Equal(t, expected.Generation, targetGeneration, "fresh request ledger must bind the current synchronized target")
					require.Equal(t, models.CodeReviewRequestQueued, response.result.Disposition, "fresh request must queue for recapture rather than disappear")
					var pending scheduledReviewIntent
					require.NoError(t, json.Unmarshal(actual.PendingInput, &pending), "decode preserved fresh request intent")
					require.Equal(t, scheduledReviewIntent{Input: ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, ExplicitRequest: true, GitHubDeliveryID: request.RequestID.String(), RequestContext: assessmentRequestContext(request), TriggerSource: models.CodeReviewTriggerSourceSlashCommand}, Mode: models.CodeReviewRecheck, RequesterID: request.RequesterID}, pending, "fresh race must retain the exact explicit request and context")
					require.Nil(t, captureStarted, "obsolete failure must not start an episode for the new target")
					require.True(t, captureTracked, "new request must retain its known timing provenance")
					require.NotNil(t, actual.RetryAt, "fresh intake must retain a durable recapture deadline")
					var wakes int
					require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$2 AND status='pending'`, org, "code_review_schedule:"+pr.String()).Scan(&wakes), "read durable recapture wake")
					require.Equal(t, 1, wakes, "fresh intake must retain exactly one coalesced recapture wake")
				} else {
					actual.UpdatedAt = expected.UpdatedAt
					require.Equal(t, expected, actual, "obsolete capture must preserve the entire newer schedule except its observation timestamp")
					var actualStarted *time.Time
					var actualTracked bool
					require.NoError(t, pool.QueryRow(ctx, `SELECT capture_failure_started_at,capture_failure_clock_tracked FROM code_review_requests WHERE org_id=$1 AND id=$2`, org, actual.PendingRequestID).Scan(&actualStarted, &actualTracked), "read exact episode after stale capture result")
					require.Equal(t, expectedStarted, actualStarted, "stale success, transient or terminal outcome must preserve newer episode age")
					require.Equal(t, expectedTracked, actualTracked, "stale success must preserve unknown legacy provenance")
					if tt.owner == "same" {
						require.Equal(t, "pending", record.Status, "retargeted same request must remain pending even after old permanent failure")
					} else {
						require.Equal(t, "superseded", record.Status, "a truly newer owner must supersede the old caller's ledger")
						require.Equal(t, models.CodeReviewRequestCancelled, response.result.Disposition, "a truly replaced caller must observe cancellation")
					}
				}
			}
			require.Equal(t, tt.paused, actual.AutomaticPaused, "capture handling must preserve the user's automatic pause preference")
			protected, err := schedules.GetLatestFullBaseline(ctx, org, pr)
			require.NoError(t, err, "read historical completed output after capture race")
			require.Equal(t, baseline, protected, "capture handling must not modify historical completed outputs or publication provenance")
			var assessmentCount int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&assessmentCount), "read assessment allocation after obsolete capture")
			expectedAssessmentCount := 2
			if tt.owner == "promotion" {
				expectedAssessmentCount = 3
			}
			require.Equal(t, expectedAssessmentCount, assessmentCount, "only current successful capture may allocate an assessment")
			beforeReplay := captures
			replayed, replayErr := service.RequestScheduledReview(ctx, request)
			if tt.owner == "current" && tt.outcome == "permanent" {
				require.ErrorIs(t, replayErr, ErrRecheckUnavailable, "terminal redelivery must return the same durable rejection")
			} else {
				require.NoError(t, replayErr, "redelivery must replay the settled or queued request")
			}
			require.Equal(t, response.result.Disposition, replayed.Disposition, "redelivery must preserve queued or cancelled disposition")
			require.Equal(t, beforeReplay, captures, "redelivery must not repeat the completed capture attempt")
		})
	}
}
