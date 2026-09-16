package codereview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type schedulingSnapshotFixture struct {
	sync.Mutex
	snapshot ghservice.CodeReviewPullRequestSnapshot
	err      error
	calls    int
	failCall int
}

func (f *schedulingSnapshotFixture) GetCodeReviewPullRequestSnapshot(context.Context, uuid.UUID, uuid.UUID, int) (ghservice.CodeReviewPullRequestSnapshot, error) {
	f.Lock()
	defer f.Unlock()
	f.calls++
	if f.calls == f.failCall {
		return ghservice.CodeReviewPullRequestSnapshot{}, fmt.Errorf("snapshot refresh unavailable")
	}
	return f.snapshot, f.err
}
func (f *schedulingSnapshotFixture) update(fn func(*ghservice.CodeReviewPullRequestSnapshot)) {
	f.Lock()
	defer f.Unlock()
	fn(&f.snapshot)
}

// Opt in with a disposable PostgreSQL 15+ server whose user may create databases.
// Each test run creates, fully migrates, and drops its own database.
func TestCodeReviewSchedulingLifecyclePostgres(t *testing.T) {
	t.Parallel()
	raw := os.Getenv("CODE_REVIEW_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set CODE_REVIEW_TEST_DATABASE_URL for full migration and lifecycle proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect integration database server")
	t.Cleanup(func() { require.NoError(t, admin.Close(ctx), "close integration admin") })
	name := "review_lifecycle_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE DATABASE `+name)
	require.NoError(t, err, "create isolated lifecycle database")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP DATABASE `+name+` WITH (FORCE)`)
		require.NoError(t, err, "drop isolated lifecycle database")
	})
	parsed, err := url.Parse(raw)
	require.NoError(t, err, "parse integration URL")
	parsed.Path = "/" + name
	source, err := filepath.Abs("../../../migrations")
	require.NoError(t, err, "resolve real migrations")
	migrations, err := migrate.New("file://"+source, parsed.String())
	require.NoError(t, err, "initialize migrations")
	require.NoError(t, migrations.Up(), "full schema must migrate before lifecycle proof")
	sourceErr, dbErr := migrations.Close()
	require.NoError(t, sourceErr, "close migration source")
	require.NoError(t, dbErr, "close migration database")
	pool, err := pgxpool.New(ctx, parsed.String())
	require.NoError(t, err, "connect lifecycle pool")
	t.Cleanup(pool.Close)
	tests := []struct {
		name string
		run  func(*testing.T, *pgxpool.Pool, uuid.UUID, uuid.UUID, uuid.UUID, *schedulingSnapshotFixture)
	}{
		{"push burst restart and manual joining", testSchedulingBurst},
		{"policy patch and historical compatibility", testSchedulingPolicyCompatibility},
		{"same head context supersession", testSchedulingGenerationFence},
		{"approved automatic intent terminates", testSchedulingApprovalTerminal},
		{"legacy generation compatibility", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingGenerationPath(t, p, org, repo, pr, snapshot, "legacy")
		}},
		{"legacy change key fence", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingGenerationPath(t, p, org, repo, pr, snapshot, "legacy_key")
		}},
		{"untagged pending supersession fence", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingGenerationPath(t, p, org, repo, pr, snapshot, "legacy_pending")
		}},
		{"retry generation fence", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingGenerationPath(t, p, org, repo, pr, snapshot, "retry")
		}},
		{"dispute generation fence", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingGenerationPath(t, p, org, repo, pr, snapshot, "dispute")
		}},
		{"snapshot refresh failure preserves replacement recovery", testSchedulingRefreshRecovery},
		{"base ref missed edited event", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingBaseRef(t, p, org, repo, pr, snapshot, "missed")
		}},
		{"base ref webhook first", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingBaseRef(t, p, org, repo, pr, snapshot, "webhook")
		}},
		{"base ref completed result cannot be reused", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingBaseRef(t, p, org, repo, pr, snapshot, "completed")
		}},
		{"base ref explicit after automatic approval stop", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingBaseRef(t, p, org, repo, pr, snapshot, "approved")
		}},
		{"base ref unchanged preserves equivalent result", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingBaseRef(t, p, org, repo, pr, snapshot, "unchanged")
		}},
		{"base ref provider refresh failure recovers", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingBaseRef(t, p, org, repo, pr, snapshot, "refresh_failure")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, integration, repo, pr := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Scheduling test')`, org)
			require.NoError(t, err, "seed org")
			_, err = pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, org)
			require.NoError(t, err, "seed installation")
			_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,$4,'test/repo','https://github.com/test/repo.git',17)`, repo, org, integration, int64(repo.ID()))
			require.NoError(t, err, "seed repository")
			_, err = pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title) VALUES($1,$2,17,'https://github.com/test/repo/pull/17','test/repo','Scheduling test')`, pr, org)
			require.NoError(t, err, "seed PR")
			snapshot := &schedulingSnapshotFixture{snapshot: ghservice.CodeReviewPullRequestSnapshot{Number: 17, State: "open", Title: "Scheduling test", HTMLURL: "https://github.com/test/repo/pull/17", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), BaseRef: "main"}}
			tt.run(t, pool, org, repo, pr, snapshot)
		})
	}
}
func testSchedulingBurst(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	store := db.NewCodeReviewStore(pool)
	config := models.DefaultCodeReviewPolicyConfig()
	quiet, interval := 300, 900
	config.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{QuietPeriodSeconds: &quiet, MinimumIntervalSeconds: &interval}
	_, err := store.SavePolicy(ctx, org, config, nil)
	require.NoError(t, err, "persist timing policy")
	newService := func() *Service {
		s := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
		s.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
		s.scheduling.now = func() time.Time { return now }
		return s
	}
	service := newService()
	initial := ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, ExplicitRequest: true, GitHubDeliveryID: uuid.NewString(), TriggerSource: models.CodeReviewTriggerSourceSlashCommand}
	_, err = service.scheduleReview(ctx, initial, models.CodeReviewEnsureCurrent, false, nil)
	require.NoError(t, err, "record initial review without allocating a session")
	for i := 1; i <= 10; i++ {
		now = now.Add(time.Second)
		snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.HeadSHA = fmt.Sprintf("%040x", i) })
		_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, HeadSHA: "old-out-of-order-webhook"})
		require.NoError(t, err, "replace pending target from authoritative snapshot")
	}
	before, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read final burst target")
	require.Equal(t, fmt.Sprintf("%040x", 10), before.HeadSHA, "obsolete payload cannot overwrite provider snapshot")
	require.Equal(t, now.Add(5*time.Minute), before.EligibleAt.UTC(), "last distinct target starts quiet window")
	now = now.Add(time.Second)
	_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
	require.NoError(t, err, "redeliver same snapshot")
	after, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read repeated snapshot")
	require.Equal(t, before.EligibleAt, after.EligibleAt, "duplicate cannot postpone deadline")
	require.Equal(t, before.Generation, after.Generation, "duplicate cannot advance generation")
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE org_id=$1`, org).Scan(&count), "count pre-dispatch sessions")
	require.Zero(t, count, "pending requests allocate no sessions")
	wake := models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}
	claim := func() context.Context {
		token := uuid.New()
		var id uuid.UUID
		err := pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND job_type=$2 AND status='pending' RETURNING id`, org, models.JobTypeReconcileCodeReviewSchedule, token).Scan(&id)
		require.NoError(t, err, "claim single durable scheduling wake")
		return jobctx.WithLockToken(jobctx.WithJobID(ctx, id), token)
	}
	service = newService() // Drop all process-local scheduling state.
	snapshot.Lock()
	snapshot.err = fmt.Errorf("provider temporarily unavailable")
	snapshot.Unlock()
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "provider outage keeps one durable request")
	held, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read outage wait")
	require.Equal(t, models.CodeReviewWaitContext, held.WaitReason, "outage is visible rather than a misleading eligibility countdown")
	require.Equal(t, now.Add(time.Minute), held.RetryAt.UTC(), "outage records retry time")
	snapshot.Lock()
	snapshot.err = nil
	snapshot.Unlock()
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "early wake reschedules without execution")
	now = *before.EligibleAt
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "restart dispatches latest eligible revision")
	metadata, err := store.GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "load dispatched assessment")
	require.Equal(t, before.HeadSHA, metadata.HeadSHA, "only the final burst head is dispatched")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_session_metadata WHERE org_id=$1`, org).Scan(&count), "count assessments")
	require.Equal(t, 1, count, "ten pushes produce exactly one assessment")
	request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow}
	// UI callers always provide requester identity; seed it to exercise that path.
	user := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO users(id,org_id,email,name) VALUES($1,$2,$3,'Reviewer')`, user, org, user.String()+"@example.test")
	require.NoError(t, err, "seed requester")
	request.RequesterID = &user
	_, err = service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "manual request joins active target")
	_, err = service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "identical retry preserves request")
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "manual request serializes behind active work")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed' WHERE org_id=$1 AND session_id=$2`, org, metadata.SessionID)
	require.NoError(t, err, "finish assessment")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
	require.NoError(t, err, "finish reviewer starter")
	require.NoError(t, service.ReconcileSchedule(claim(), wake), "manual request reuses completed current-head result")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_session_metadata WHERE org_id=$1`, org).Scan(&count), "count after manual joins")
	require.Equal(t, 1, count, "manual joining must not duplicate an equivalent assessment")
	// A draft conversion missed by webhooks still prevents worker publication.
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='running' WHERE org_id=$1 AND session_id=$2`, org, metadata.SessionID)
	require.NoError(t, err, "represent active attempt before draft conversion")
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.IsDraft = true })
	allowed, err := service.ValidateScheduledExecution(ctx, org, pr, metadata.SessionID)
	require.NoError(t, err, "refresh worker eligibility when webhook was missed")
	require.False(t, allowed, "draft conversion stops dispatch and publication")
	held, err = service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read durable draft hold")
	require.Equal(t, models.CodeReviewWaitDraft, held.WaitReason, "draft retains pending replacement")
	stale, err := store.GetBySessionID(ctx, org, metadata.SessionID)
	require.NoError(t, err, "read invalidated active metadata")
	require.Equal(t, models.CodeReviewSessionStatusStale, stale.Status, "active attempt loses publication authority")
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.State = "closed" })
	_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
	require.NoError(t, err, "close cancels durable pending work")
	closed, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read closed target")
	require.Equal(t, models.CodeReviewScheduleClosed, closed.State, "closed PR cannot dispatch")
	require.Nil(t, closed.PendingInput, "closed target has no pending execution")
}
func testSchedulingPolicyCompatibility(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	original, err := store.SavePolicy(ctx, org, models.DefaultCodeReviewPolicyConfig(), nil)
	require.NoError(t, err, "create initial policy")
	_, err = pool.Exec(ctx, `UPDATE code_review_policies SET scheduling_policy='{}' WHERE org_id=$1 AND id=$2`, org, original.ID)
	require.NoError(t, err, "represent pre-scheduling historical version")
	saved, err := store.PatchPolicy(ctx, org, []byte(`{"scheduling_policy":{"automatic_re_review":false,"quiet_period_seconds":0,"minimum_interval_seconds":900}}`), original.Version, nil)
	require.NoError(t, err, "persist explicit false and zero")
	_, err = store.PatchPolicy(ctx, org, []byte(`{"enabled":false}`), original.Version, nil)
	require.ErrorIs(t, err, db.ErrCodeReviewPolicyVersionConflict, "stale editor cannot overwrite newer version")
	legacy := models.DefaultCodeReviewPolicyConfig()
	legacy.InlineCommentLimit = 4
	legacySaved, err := store.SavePolicy(ctx, org, legacy, nil)
	require.NoError(t, err, "legacy whole-policy save remains compatible")
	require.Equal(t, saved.SchedulingPolicy.Effective(), legacySaved.SchedulingPolicy.Effective(), "legacy omission preserves timing")
	historical, err := store.GetPolicyByID(ctx, org, original.ID)
	require.NoError(t, err, "read historical policy")
	restored, err := store.SavePolicy(ctx, org, historical.Config(), nil)
	require.NoError(t, err, "restore historic version")
	require.Equal(t, saved.SchedulingPolicy.Effective(), restored.SchedulingPolicy.Effective(), "pre-scheduling restore preserves current timing")
	reset, err := store.PatchPolicy(ctx, org, []byte(`{"scheduling_policy":null}`), restored.Version, nil)
	require.NoError(t, err, "explicit null resets override")
	require.Equal(t, models.CodeReviewSchedulingSettings{AutomaticReReview: true, QuietPeriodSeconds: 60}, reset.SchedulingPolicy.Effective(), "reset resolves documented defaults")
}

// Each fixture owns a distinct tenant, so these lifecycle scenarios run in parallel.
func schedulingLifecycleService(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) (*Service, func() context.Context, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	config := models.DefaultCodeReviewPolicyConfig()
	zero := 0
	config.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{QuietPeriodSeconds: &zero, MinimumIntervalSeconds: &zero}
	_, err := store.SavePolicy(ctx, org, config, nil)
	require.NoError(t, err, "save immediate scheduling policy")
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
	claim := func() context.Context {
		token := uuid.New()
		var id uuid.UUID
		err := pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND job_type=$2 AND status='pending' RETURNING id`, org, models.JobTypeReconcileCodeReviewSchedule, token).Scan(&id)
		require.NoError(t, err, "claim scheduling wake with lease")
		return jobctx.WithLockToken(jobctx.WithJobID(ctx, id), token)
	}
	_, err = service.scheduleReview(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, ExplicitRequest: true, GitHubDeliveryID: uuid.NewString(), TriggerSource: models.CodeReviewTriggerSourceSlashCommand}, models.CodeReviewReviewNow, false, nil)
	require.NoError(t, err, "queue initial explicit review")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "start initial review")
	metadata, err := store.GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read initial review")
	return service, claim, metadata.SessionID
}

func testSchedulingGenerationFence(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	testSchedulingGenerationPath(t, pool, org, repo, pr, snapshot, "scheduled")
}

func testSchedulingGenerationPath(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, path string) {
	ctx := context.Background()
	service, claim, sessionID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	switch path {
	case "legacy":
		_, err := pool.Exec(ctx, `UPDATE sessions SET revision_context=revision_context-'schedule_generation'-'change_key' WHERE org_id=$1 AND id=$2`, org, sessionID)
		require.NoError(t, err, "represent untagged legacy session")
		_, err = pool.Exec(ctx, `DELETE FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2`, org, pr)
		require.NoError(t, err, "legacy session predates scheduling state")
	case "legacy_key":
		_, err := pool.Exec(ctx, `UPDATE sessions SET revision_context=revision_context-'schedule_generation' WHERE org_id=$1 AND id=$2`, org, sessionID)
		require.NoError(t, err, "represent original schedule change-key provenance")
	case "retry", "dispute":
		_, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='failed',retryable_failure=true WHERE org_id=$1 AND session_id=$2`, org, sessionID)
		require.NoError(t, err, "terminalize source attempt")
		_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
		require.NoError(t, err, "release source starter")
		if path == "retry" {
			_, err = pool.Exec(ctx, `INSERT INTO pull_request_health_current(pull_request_id,org_id,version,head_sha,base_sha,summary_json) VALUES($1,$2,1,$3,$4,'{}')`, pr, org, snapshot.snapshot.HeadSHA, snapshot.snapshot.BaseSHA)
			require.NoError(t, err, "seed authoritative retry revision")
			service.SetRetryDependencies(db.NewPullRequestStore(pool), &pullRequestSyncerStub{})
			result, err := service.RetryReview(ctx, RetryReviewInput{OrgID: org, SessionID: sessionID})
			require.NoError(t, err, "start actual serialized retry")
			sessionID = result.SessionID
		} else {
			result, err := service.HandleReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, GitHubRepo: "test/repo", GitHubPRNumber: 17, GitHubPRURL: snapshot.snapshot.HTMLURL, ExplicitRequest: true, ChangeKey: "dispute:test", ChangeReason: "dispute", TriggerSource: models.CodeReviewTriggerSourceSlashCommand})
			require.NoError(t, err, "start serialized dispute reassessment")
			sessionID = result.SessionID
		}
		require.NotEqual(t, uuid.Nil, sessionID, "alternate admission allocates a session")
		allowed, err := service.ValidateScheduledExecution(ctx, org, pr, sessionID)
		require.NoError(t, err, "validate alternate admission before ordinary scheduler observation")
		require.True(t, allowed, "fresh retry or dispute has generation authority")
	}
	// A second equivalent explicit request joins the generation without revoking it.
	_, err := service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow})
	require.NoError(t, err, "queue equivalent manual join")
	allowed, err := service.ValidateScheduledExecution(ctx, org, pr, sessionID)
	require.NoError(t, err, "validate equivalent join")
	require.True(t, allowed, "same generation keeps execution authority")
	_, err = pool.Exec(ctx, `INSERT INTO session_threads(org_id,session_id,agent_type,label,status) VALUES($1,$2,'codex','Code review: generation test','running')`, org, sessionID)
	require.NoError(t, err, "start old generation reviewer thread")
	canceller := &schedulingThreadCanceller{pool: pool}
	service.SetThreadCanceller(canceller)
	changed := ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, ExplicitRequest: true, GitHubDeliveryID: uuid.NewString(), TriggerSource: models.CodeReviewTriggerSourceSlashCommand, RequestContext: &ReviewRequestContext{Source: "github_comment", Body: "Please focus on the authorization boundary."}}
	_, err = service.scheduleReview(ctx, changed, models.CodeReviewReviewNow, false, nil)
	require.NoError(t, err, "persist changed request context on identical head and base")
	pending, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read replacement intent before fence")
	if path == "legacy_pending" {
		_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=revision_context-'schedule_generation'-'change_key' WHERE org_id=$1 AND id=$2`, org, sessionID)
		require.NoError(t, err, "represent upgrade with old untagged session and already advanced generation")
	}
	allowed, err = service.ValidateScheduledExecution(ctx, org, pr, sessionID)
	require.NoError(t, err, "fence superseded same-head session")
	require.False(t, allowed, "changed context must revoke old generation publication")
	require.Equal(t, []uuid.UUID{sessionID}, canceller.cancelled, "postcommit cancellation targets only obsolete session")
	stale, err := db.NewCodeReviewStore(pool).GetBySessionID(ctx, org, sessionID)
	require.NoError(t, err, "read durably revoked attempt")
	require.Equal(t, models.CodeReviewSessionStatusStale, stale.Status, "false worker result must durably stale obsolete session")
	after, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read preserved replacement")
	require.Equal(t, pending.PendingInput, after.PendingInput, "generation fence preserves replacement request context")
	require.Equal(t, pending.Generation, after.Generation, "fence cannot invent a newer replacement generation")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "replacement can start after stale attempt")
	replacement, err := db.NewCodeReviewStore(pool).GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read replacement session")
	require.NotEqual(t, sessionID, replacement.SessionID, "changed context creates a new assessment")
	allowed, err = service.ValidateScheduledExecution(ctx, org, pr, replacement.SessionID)
	require.NoError(t, err, "validate replacement generation")
	require.True(t, allowed, "current replacement retains publication authority")

	allowed, err = service.ValidateScheduledExecution(ctx, org, pr, sessionID)
	require.NoError(t, err, "recheck old worker after replacement starts")
	require.False(t, allowed, "old worker cannot regain authority after replacement")
	replacement, err = db.NewCodeReviewStore(pool).GetBySessionID(ctx, org, replacement.SessionID)
	require.NoError(t, err, "read unaffected replacement")
	require.Equal(t, models.CodeReviewSessionStatusQueued, replacement.Status, "staling old session must not stale replacement")
}

func testSchedulingApprovalTerminal(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	ctx := context.Background()
	service, claim, sessionID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	_, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed',decision='approved',github_review_id=42 WHERE org_id=$1 AND session_id=$2`, org, sessionID)
	require.NoError(t, err, "complete submitted approval")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
	require.NoError(t, err, "complete initial starter")
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) {
		s.HeadSHA = strings.Repeat("c", 40)
		s.IsDraft = true
	})
	require.NoError(t, service.PauseSchedule(ctx, org, pr, true), "also pause automatic scheduling")
	_, err = db.NewCodeReviewStore(pool).PatchPolicy(ctx, org, []byte(`{"enabled":false}`), 1, nil)
	require.NoError(t, err, "also disable review policy")
	_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
	require.NoError(t, err, "automatic push after historical approval")
	// A pre-existing wake must finish successfully, including after competing holds.
	require.NoError(t, service.scheduling.store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, _ *models.CodeReviewPRState) error {
		return db.UpsertCodeReviewWake(ctx, tx, org, pr, time.Now())
	}), "represent existing approved-target wake")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "approved automatic wake finishes")
	state, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read terminal automatic intent")
	require.Equal(t, models.CodeReviewWaitApproved, state.WaitReason, "permanent approval takes precedence over transient holds")
	require.Nil(t, state.PendingInput, "approval discards automatic pending intent")
	require.Nil(t, state.PendingRequestID, "approval leaves no pending request")
	require.Nil(t, state.FirstPendingAt, "approval clears pending age")
	require.Nil(t, state.EligibleAt, "approval clears eligibility timer")
	require.Nil(t, state.RetryAt, "approval never polls forever")
	require.NoError(t, service.scheduling.store.RepairMissingWakes(ctx), "repair runs after terminal approval")
	var active int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1 AND job_type=$2 AND status IN ('pending','running')`, org, models.JobTypeReconcileCodeReviewSchedule).Scan(&active), "count repaired wakes")
	require.Zero(t, active, "repair cannot recreate automatic approval wake")
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.IsDraft = false })
	_, err = db.NewCodeReviewStore(pool).PatchPolicy(ctx, org, []byte(`{"enabled":true}`), 2, nil)
	require.NoError(t, err, "reenable policy for explicit request")
	_, err = service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow})
	require.NoError(t, err, "explicit review now remains available after approval")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "explicit review starts despite approval and automatic pause")
	latest, err := db.NewCodeReviewStore(pool).GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read explicit replacement")
	require.NotEqual(t, sessionID, latest.SessionID, "explicit request creates requested latest-head assessment")
}

// This fixture checks visibility from another connection, proving cancellation
// happens after durable revocation commits rather than inside its transaction.
type schedulingThreadCanceller struct {
	pool      *pgxpool.Pool
	cancelled []uuid.UUID
}

func (c *schedulingThreadCanceller) CancelActiveThreads(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) (int, error) {
	var allStale bool
	if err := c.pool.QueryRow(ctx, `SELECT bool_and(status='stale') FROM code_review_session_metadata WHERE org_id=$1 AND session_id=ANY($2)`, orgID, sessionIDs).Scan(&allStale); err != nil {
		return 0, err
	}
	if !allStale {
		return 0, fmt.Errorf("cancellation preceded committed revocation")
	}
	tag, err := c.pool.Exec(ctx, `UPDATE session_threads SET status='cancelled' WHERE org_id=$1 AND session_id=ANY($2) AND status IN ('pending','running','awaiting_input')`, orgID, sessionIDs)
	if err != nil {
		return 0, err
	}
	c.cancelled = append(c.cancelled, sessionIDs...)
	return int(tag.RowsAffected()), nil
}

func testSchedulingRefreshRecovery(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	ctx := context.Background()
	service, claim, sessionID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	snapshot.Lock()
	snapshot.snapshot.HeadSHA = strings.Repeat("d", 40)
	snapshot.failCall = snapshot.calls + 2
	snapshot.Unlock()
	allowed, err := service.ValidateScheduledExecution(ctx, org, pr, sessionID)
	require.Error(t, err, "failed second provider refresh must retry worker")
	require.False(t, allowed, "provider mismatch never permits publication")
	old, err := db.NewCodeReviewStore(pool).GetBySessionID(ctx, org, sessionID)
	require.NoError(t, err, "read source after unavailable refresh")
	require.Equal(t, models.CodeReviewSessionStatusQueued, old.Status, "source remains recoverable until replacement intent commits")
	allowed, err = service.ValidateScheduledExecution(ctx, org, pr, sessionID)
	require.NoError(t, err, "worker retry recovers latest intent")
	require.False(t, allowed, "obsolete source remains denied")
	pending, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read recovered target")
	require.NotNil(t, pending.PendingInput, "replacement must survive failed refresh and worker retry")
	require.Equal(t, strings.Repeat("d", 40), pending.HeadSHA, "replacement targets current provider head")
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "recovered replacement starts")
	latest, err := db.NewCodeReviewStore(pool).GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read recovered review")
	require.NotEqual(t, sessionID, latest.SessionID, "recovery produces replacement review")
}

func testSchedulingBaseRef(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, scenario string) {
	ctx := context.Background()
	service, claim, oldSessionID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	before, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read original target identity")
	allowed, err := service.ValidateScheduledExecution(ctx, org, pr, oldSessionID)
	require.NoError(t, err, "validate original base reference")
	require.True(t, allowed, "unchanged provider branch keeps execution authority")
	completed := scenario == "completed" || scenario == "approved" || scenario == "unchanged"
	canceller := &schedulingThreadCanceller{pool: pool}
	service.SetThreadCanceller(canceller)
	if completed {
		_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed' WHERE org_id=$1 AND session_id=$2`, org, oldSessionID)
		require.NoError(t, err, "complete assessment on original base branch")
		_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
		require.NoError(t, err, "finish original worker")
		if scenario == "approved" {
			_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET decision='approved',github_review_id=42 WHERE org_id=$1 AND session_id=$2`, org, oldSessionID)
			require.NoError(t, err, "publish historical approval")
		}
	} else {
		_, err = pool.Exec(ctx, `INSERT INTO session_threads(org_id,session_id,agent_type,label,status) VALUES($1,$2,'codex','Code review: base reference test','running')`, org, oldSessionID)
		require.NoError(t, err, "start reviewer on original base reference")
	}
	if scenario != "unchanged" {
		snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.BaseRef = "release" })
	}
	if scenario == "missed" || scenario == "refresh_failure" {
		if scenario == "refresh_failure" {
			snapshot.Lock()
			snapshot.failCall = snapshot.calls + 2
			snapshot.Unlock()
			allowed, err = service.ValidateScheduledExecution(ctx, org, pr, oldSessionID)
			require.Error(t, err, "unavailable replacement refresh must retry the worker")
			require.False(t, allowed, "retarget never permits publication during provider failure")
			old, err := db.NewCodeReviewStore(pool).GetBySessionID(ctx, org, oldSessionID)
			require.NoError(t, err, "read recoverable source attempt")
			require.Equal(t, models.CodeReviewSessionStatusQueued, old.Status, "failed refresh cannot discard source before replacement commits")
		}
		allowed, err = service.ValidateScheduledExecution(ctx, org, pr, oldSessionID)
		require.NoError(t, err, "detect missed base-only retarget from authoritative snapshot")
		require.False(t, allowed, "same commit SHAs do not authorize review of another base branch")
	} else {
		_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
		require.NoError(t, err, "observe current base reference via webhook")
		if scenario == "approved" {
			stopped, err := service.GetSchedule(ctx, org, pr)
			require.NoError(t, err, "read stopped automatic intent")
			require.Nil(t, stopped.PendingInput, "approval clears automatic retarget intent")
			require.Equal(t, "release", stopped.BaseRef, "approval still records current branch")
			_, err = service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow})
			require.NoError(t, err, "explicit request after terminal automatic approval hold")
		}
		if !completed {
			allowed, err = service.ValidateScheduledExecution(ctx, org, pr, oldSessionID)
			require.NoError(t, err, "validate worker after retarget webhook")
			require.False(t, allowed, "webhook-first retarget revokes original generation")
		}
	}
	pending, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read durable current target")
	require.Equal(t, before.HeadSHA, pending.HeadSHA, "retarget test holds head commit constant")
	require.Equal(t, before.BaseSHA, pending.BaseSHA, "retarget test holds base commit constant")
	if scenario != "unchanged" {
		require.Equal(t, "release", pending.BaseRef, "replacement tracks retargeted branch")
		require.Greater(t, pending.Generation, before.Generation, "base reference contributes to generation identity")
		var intent scheduledReviewIntent
		require.NoError(t, json.Unmarshal(pending.PendingInput, &intent), "decode replacement intent")
		require.True(t, intent.Force, "retarget must force reassessment despite identical diff SHAs")
	} else {
		require.Equal(t, before.Generation, pending.Generation, "same branch redelivery cannot advance generation")
	}
	if !completed {
		require.Equal(t, []uuid.UUID{oldSessionID}, canceller.cancelled, "retarget cancels only original session after commit")
	}
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "reconcile target after base reference observation")
	latest, err := db.NewCodeReviewStore(pool).GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read resulting assessment")
	if scenario == "unchanged" {
		require.Equal(t, oldSessionID, latest.SessionID, "equivalent completed assessment remains reusable")
		return
	}
	require.NotEqual(t, oldSessionID, latest.SessionID, "retarget allocates a new assessment instead of old completed result")
	allowed, err = service.ValidateScheduledExecution(ctx, org, pr, latest.SessionID)
	require.NoError(t, err, "validate replacement base reference")
	require.True(t, allowed, "replacement owns current branch and generation")
	allowed, err = service.ValidateScheduledExecution(ctx, org, pr, oldSessionID)
	require.NoError(t, err, "recheck old attempt after replacement")
	require.False(t, allowed, "old assessment cannot regain authority on same SHAs")
	latest, err = db.NewCodeReviewStore(pool).GetBySessionID(ctx, org, latest.SessionID)
	require.NoError(t, err, "read replacement after obsolete worker check")
	require.Equal(t, models.CodeReviewSessionStatusQueued, latest.Status, "old worker cleanup leaves replacement intact")
}
