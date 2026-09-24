package codereview

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	snapshot   ghservice.CodeReviewPullRequestSnapshot
	err        error
	prepareErr error
	calls      int
	failCall   int
}

func (f *schedulingSnapshotFixture) PrepareCodeReviewPullRequestSnapshot(_ context.Context, orgID, repoID uuid.UUID) (ghservice.CodeReviewPullRequestSnapshotReader, error) {
	f.Lock()
	err := f.prepareErr
	f.Unlock()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, number int) (ghservice.CodeReviewPullRequestSnapshot, error) {
		return f.GetCodeReviewPullRequestSnapshot(ctx, orgID, repoID, number)
	}, nil
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
		{"first recheck request with continuation disabled", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testAssessmentFirstRequestAdmission(t, p, org, pr, snapshot, false)
		}},
		{"first recheck request with continuation enabled", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testAssessmentFirstRequestAdmission(t, p, org, pr, snapshot, true)
		}},
		{"assessment admission preserves schedule generation", testAssessmentGenerationAdmission},
		{"unsent evidence refresh admits another evidence turn", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testRefreshUnsentEvidenceAssessment(t, p, org, repo, pr, snapshot, false, false)
		}},
		{"uncertain evidence publication cannot refresh", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testRefreshUnsentEvidenceAssessment(t, p, org, repo, pr, snapshot, true, false)
		}},
		{"intent drift refresh routes full", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testRefreshUnsentEvidenceAssessment(t, p, org, repo, pr, snapshot, false, true)
		}},
		{"push burst restart and manual joining", testSchedulingBurst},
		{"draft automatic", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingDraft(t, p, org, repo, pr, snapshot, "automatic")
		}},
		{"draft manual", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingDraft(t, p, org, repo, pr, snapshot, "manual")
		}},
		{"draft retry", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingDraft(t, p, org, repo, pr, snapshot, "retry")
		}},
		{"draft dispute", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingDraft(t, p, org, repo, pr, snapshot, "dispute")
		}},
		{"draft legacy_hold", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingDraft(t, p, org, repo, pr, snapshot, "legacy_hold")
		}},
		{"draft active_missed", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingDraft(t, p, org, repo, pr, snapshot, "active_missed")
		}},
		{"draft active_webhook", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingDraft(t, p, org, repo, pr, snapshot, "active_webhook")
		}},
		{"draft closed", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingDraft(t, p, org, repo, pr, snapshot, "closed")
		}},
		{"single connection schedule", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingSingleConnection(t, p, org, repo, pr, snapshot, "schedule")
		}},
		{"single connection retry", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingSingleConnection(t, p, org, repo, pr, snapshot, "retry")
		}},
		{"single connection dispute", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingSingleConnection(t, p, org, repo, pr, snapshot, "dispute")
		}},
		{"single connection snapshot after lock wait", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingSingleConnection(t, p, org, repo, pr, snapshot, "wait")
		}},
		{"snapshot preparation outage preserves pending intent", testSchedulingPreparationRecovery},
		{"snapshot preparation outage preserves replay and terminal paths", testSchedulingPreparationFastPaths},
		{"single connection validation", func(t *testing.T, p *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
			testSchedulingSingleConnection(t, p, org, repo, pr, snapshot, "validation")
		}},
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

type assessmentAdmissionNoCapture struct{}

func (assessmentAdmissionNoCapture) CaptureAssessmentInputs(context.Context, AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
	return AssessmentInputCaptureResult{}, fmt.Errorf("capture should not run before the first full baseline")
}

type assessmentAdmissionFixture struct {
	pool     *pgxpool.Pool
	manifest ReviewInputManifest
	policy   models.CodeReviewPolicyRecord
	session  uuid.UUID
	snapshot ghservice.CodeReviewPullRequestSnapshot
}

func (f assessmentAdmissionFixture) CaptureAssessmentInputs(ctx context.Context, in AssessmentInputCaptureRequest) (AssessmentInputCaptureResult, error) {
	key := "code-review-prompts/" + f.session.String() + "/assessments/" + in.AssessmentID.String() + "/head/visual-evidence-v1"
	metadata := json.RawMessage(`{"assessment_id":"` + in.AssessmentID.String() + `"}`)
	if _, err := f.pool.Exec(ctx, `INSERT INTO code_review_prompt_records(id,org_id,session_id,record_key,role,content,metadata) VALUES($1,$2,$3,$4,'visual_evidence','',$5)`, uuid.New(), in.OrgID, f.session, key, metadata); err != nil {
		return AssessmentInputCaptureResult{}, err
	}
	return AssessmentInputCaptureResult{Manifest: f.manifest, VisualEvidence: models.CodeReviewVisualEvidenceSnapshot{AssessmentID: &in.AssessmentID, Complete: true}, Policy: f.policy, Snapshot: f.snapshot}, nil
}

func testAssessmentFirstRequestAdmission(t *testing.T, pool *pgxpool.Pool, org, pr uuid.UUID, snapshot *schedulingSnapshotFixture, continuationEnabled bool) {
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	config := models.DefaultCodeReviewPolicyConfig()
	config.ContinuationPolicy = &models.CodeReviewContinuationPolicy{Enabled: continuationEnabled}
	_, err := store.SavePolicy(ctx, org, config, nil)
	require.NoError(t, err, "admission policy should save")
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
	service.SetAssessmentContinuation(assessmentAdmissionNoCapture{}, true)
	requestID := uuid.New()
	result, err := service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: requestID, Mode: models.CodeReviewRecheck})
	require.NoError(t, err, "first recheck request should start a normal full review without prior metadata")
	require.Equal(t, models.CodeReviewRequestQueued, result.Disposition, "first request should queue a full review")
	require.NotNil(t, result.Schedule.PendingInput, "first review should have durable pending intent")
	var pending scheduledReviewIntent
	require.NoError(t, json.Unmarshal(result.Schedule.PendingInput, &pending), "pending full intent should decode")
	require.Equal(t, models.CodeReviewReviewNow, pending.Mode, "no baseline or disabled continuation should use legacy review mode")
	require.False(t, pending.Force, "first review should not spend a forced duplicate panel")
	replayed, err := service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: requestID, Mode: models.CodeReviewRecheck})
	require.NoError(t, err, "same first request ID should replay without conflict")
	require.Equal(t, result.Schedule.Generation, replayed.Schedule.Generation, "idempotent replay should not advance schedule generation")
	var wakeID uuid.UUID
	lease := uuid.New()
	err = pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND job_type=$2 AND status='pending' RETURNING id`, org, models.JobTypeReconcileCodeReviewSchedule, lease).Scan(&wakeID)
	require.NoError(t, err, "first full review wake should be claimable")
	require.NoError(t, service.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, wakeID), lease), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "first full review should start")
	active, err := service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck})
	require.NoError(t, err, "same-head request during active full review should join")
	require.Equal(t, models.CodeReviewRequestJoined, active.Disposition, "equivalent active review should join rather than force a duplicate")
	require.Equal(t, result.Schedule.Generation, active.Schedule.Generation, "joining active review should preserve schedule generation")
	var sessions int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM code_review_session_metadata WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&sessions)
	require.NoError(t, err, "review session count should load")
	require.Equal(t, 1, sessions, "active join must not allocate a second review session")
}

func testAssessmentGenerationAdmission(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	ctx := context.Background()
	store := db.NewCodeReviewStore(pool)
	config := models.DefaultCodeReviewPolicyConfig()
	config.ContinuationPolicy = &models.CodeReviewContinuationPolicy{Enabled: true}
	zero := 0
	config.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{QuietPeriodSeconds: &zero, MinimumIntervalSeconds: &zero}
	policy, err := store.SavePolicy(ctx, org, config, nil)
	require.NoError(t, err, "continuation policy should save")
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
	_, err = service.scheduleReview(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, ExplicitRequest: true, GitHubDeliveryID: uuid.NewString(), TriggerSource: models.CodeReviewTriggerSourceSlashCommand}, models.CodeReviewReviewNow, false, nil)
	require.NoError(t, err, "initial full review should queue")
	var wakeID uuid.UUID
	lease := uuid.New()
	err = pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes',attempts=attempts+1 WHERE org_id=$1 AND job_type=$2 AND status='pending' RETURNING id`, org, models.JobTypeReconcileCodeReviewSchedule, lease).Scan(&wakeID)
	require.NoError(t, err, "initial wake should be claimable")
	require.NoError(t, service.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, wakeID), lease), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "initial full review should start")
	metadata, err := store.GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "full review metadata should exist")
	baselineInput := reviewTestCapture()
	baselineInput.Code.OrgID, baselineInput.Code.RepositoryID, baselineInput.Code.PullRequestID = org, repo, pr
	baselineInput.Code.HeadSHA, baselineInput.Code.BaseSHA, baselineInput.Code.BaseRef = snapshot.snapshot.HeadSHA, snapshot.snapshot.BaseSHA, snapshot.snapshot.BaseRef
	baselineInput.Contract.PolicyID, baselineInput.Contract.PolicyVersion = policy.ID, int64(policy.Version)
	baseline, err := BuildReviewInputManifest(baselineInput)
	require.NoError(t, err, "full baseline manifest should build")
	baselineRaw, err := json.Marshal(baseline)
	require.NoError(t, err, "full baseline manifest should marshal")
	assessmentStore := db.NewCodeReviewAssessmentStore(pool)
	full, _, err := assessmentStore.Create(ctx, models.CodeReviewAssessmentCapture{OrgID: org, RepositoryID: repo, PullRequestID: pr, PolicyID: policy.ID, SessionID: metadata.SessionID, Generation: 1, BaseSHA: baseline.Code.BaseSHA, BaseRef: baseline.Code.BaseRef, HeadSHA: baseline.Code.HeadSHA, InputVersion: baseline.InputVersion, CodeDigest: baseline.CodeDigest, ContractDigest: baseline.ContractDigest, IntentDigest: baseline.IntentDigest, VisualDigest: baseline.VisualDigest, RequestDigest: baseline.RequestDigest, GateDigest: baseline.GateDigest, InputDigest: baseline.InputDigest, InputManifest: baselineRaw, ReviewScope: models.CodeReviewScopeFull, RouteReason: models.CodeReviewRouteInitialFull, PublicationKey: "generation-baseline:" + pr.String()})
	require.NoError(t, err, "complete full baseline should insert")
	reasons := json.RawMessage(`[{"code":"blocking_findings"}]`)
	outcome := json.RawMessage(`{"description_assessments":[{"key":"description","status":"satisfied"}],"risk_reasons":[{"code":"blocking_findings"}],"coverage_complete":true}`)
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',result_origin='executed',coverage_complete=true,decision='blocked',acceptable=false,risk_reason_details=$3,structured_outcome=$4,publication_state='not_required',completed_at=now() WHERE org_id=$1 AND id=$2`, org, full.ID, reasons, outcome)
	require.NoError(t, err, "full assessment should become a complete baseline")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed',decision='blocked',completed_at=now() WHERE org_id=$1 AND session_id=$2`, org, metadata.SessionID)
	require.NoError(t, err, "legacy full session should become terminal")
	_, err = pool.Exec(ctx, `UPDATE sessions SET status='completed' WHERE org_id=$1 AND id=$2`, org, metadata.SessionID)
	require.NoError(t, err, "underlying full session should become terminal")
	_, err = pool.Exec(ctx, `UPDATE session_threads SET status='completed' WHERE org_id=$1 AND session_id=$2`, org, metadata.SessionID)
	require.NoError(t, err, "full reviewer threads should become terminal")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
	require.NoError(t, err, "full review job should become terminal")
	_, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET generation=7,state='covered',active_session_id=NULL WHERE org_id=$1 AND pull_request_id=$2`, org, pr)
	require.NoError(t, err, "schedule should retain independent generation seven")
	_, err = pool.Exec(ctx, `UPDATE pull_requests SET head_sha=$3,base_sha=$4 WHERE org_id=$1 AND id=$2`, org, pr, snapshot.snapshot.HeadSHA, snapshot.snapshot.BaseSHA)
	require.NoError(t, err, "provider-synced PR revision should match captured head and base")
	changedInput := baselineInput
	changedInput.Visual.Images = []ReviewVisualImage{{SourceID: "new-image", SourceURL: "https://example.test/image.png", ContentDigest: strings.Repeat("c", 64)}}
	changed, err := BuildReviewInputManifest(changedInput)
	require.NoError(t, err, "new visual evidence should build")
	service.SetAssessmentContinuation(assessmentAdmissionFixture{pool: pool, manifest: changed, policy: policy, session: metadata.SessionID, snapshot: snapshot.snapshot}, true)
	result, err := service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewRecheck})
	require.NoError(t, err, "new evidence should admit a narrow assessment")
	require.NotNil(t, result.AssessmentID, "admission should allocate assessment identity")
	require.Equal(t, int64(7), result.Schedule.Generation, "assessment generation must not overwrite schedule generation")
	var assessmentGeneration, targetGeneration int64
	err = pool.QueryRow(ctx, `SELECT generation FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, org, *result.AssessmentID).Scan(&assessmentGeneration)
	require.NoError(t, err, "new assessment generation should load")
	require.Equal(t, int64(2), assessmentGeneration, "assessment generation should advance from baseline")
	err = pool.QueryRow(ctx, `SELECT target_generation FROM code_review_requests WHERE org_id=$1 AND assessment_id=$2`, org, *result.AssessmentID).Scan(&targetGeneration)
	require.NoError(t, err, "request target generation should load")
	require.Equal(t, int64(7), targetGeneration, "request target should follow schedule generation")
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
	joined, err := service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "manual request joins active target")
	require.Equal(t, models.CodeReviewRequestJoined, joined.Disposition, "equivalent manual request should join the active session")
	_, err = service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "identical retry preserves request")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed' WHERE org_id=$1 AND session_id=$2`, org, metadata.SessionID)
	require.NoError(t, err, "finish assessment")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
	require.NoError(t, err, "finish reviewer starter")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM code_review_session_metadata WHERE org_id=$1`, org).Scan(&count), "count after manual joins")
	require.Equal(t, 1, count, "manual joining must not duplicate an equivalent assessment")
	// A draft conversion missed by webhooks preserves current review authority.
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='running' WHERE org_id=$1 AND session_id=$2`, org, metadata.SessionID)
	require.NoError(t, err, "represent active attempt before draft conversion")
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.IsDraft = true })
	allowed, err := service.ValidateScheduledExecution(ctx, org, pr, metadata.SessionID)
	require.NoError(t, err, "refresh worker eligibility when webhook was missed")
	require.True(t, allowed, "draft conversion preserves dispatch and publication")
	held, err = service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read observed draft status")
	require.True(t, held.IsDraft, "draft status remains observable without holding review")
	stale, err := store.GetBySessionID(ctx, org, metadata.SessionID)
	require.NoError(t, err, "read invalidated active metadata")
	require.Equal(t, models.CodeReviewSessionStatusRunning, stale.Status, "draft conversion preserves active attempt authority")
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

func testSchedulingSingleConnection(t *testing.T, adminPool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, scenario string) {
	ctx := context.Background()
	config := adminPool.Config()
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "create realistic saturated one-connection pool")
	t.Cleanup(pool.Close)
	service, _, sessionID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	_, err = pool.Exec(ctx, `UPDATE repositories SET installation_id=0 WHERE org_id=$1 AND id=$2`, org, repo)
	require.NoError(t, err, "require database-backed installation fallback")
	_, err = pool.Exec(ctx, `UPDATE integrations SET config='{"installation_id":17}' WHERE org_id=$1`, org)
	require.NoError(t, err, "seed fallback installation identity")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err, "generate isolated signing key")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	tokens, err := ghservice.NewService(42, string(keyPEM))
	require.NoError(t, err, "construct real installation-token provider")
	tokenReady := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/17/access_tokens" {
			require.Equal(t, int32(0), pool.Stat().AcquiredConns(), "all repository and fallback auth database work must finish before lock")
			tokenReady <- struct{}{}
			w.WriteHeader(http.StatusCreated)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"token": "test-installation-token", "expires_at": time.Now().Add(time.Hour)}), "write installation token")
			return
		}
		require.Equal(t, "/repos/test/repo/pulls/17", r.URL.Path, "prepared reader uses captured repository identity")
		require.Equal(t, "token test-installation-token", r.Header.Get("Authorization"), "prepared reader uses fully resolved installation token")
		require.Equal(t, int32(1), pool.Stat().AcquiredConns(), "authoritative snapshot must execute inside the admission transaction")
		probe, err := adminPool.Acquire(r.Context())
		require.NoError(t, err, "probe PR serialization from independent connection")
		defer probe.Release()
		var available bool
		lock := "code_review_pr:" + org.String() + ":" + pr.String()
		require.NoError(t, probe.QueryRow(r.Context(), `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, lock).Scan(&available), "probe admission lock")
		if available {
			_, err := probe.Exec(r.Context(), `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lock)
			require.NoError(t, err, "release unexpectedly available lock")
		}
		require.False(t, available, "snapshot freshness must remain protected by the PR lock")
		current, err := snapshot.GetCodeReviewPullRequestSnapshot(r.Context(), org, repo, 17)
		require.NoError(t, err, "read live provider fixture only after lock acquisition")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"number": 17, "html_url": "https://github.com/test/repo/pull/17", "title": "Scheduling test", "state": "open",
			"head": map[string]any{"sha": current.HeadSHA, "ref": "feature"},
			"base": map[string]any{"sha": strings.Repeat("b", 40), "ref": "main"},
		}), "write authoritative snapshot")
	}))
	t.Cleanup(server.Close)
	tokens.SetBaseURL(server.URL)
	provider := ghservice.NewPRService(tokens, db.NewPullRequestStore(pool), db.NewSessionStore(pool), nil, nil, db.NewRepositoryStore(pool), db.NewJobStore(pool), zerolog.Nop())
	provider.SetBaseURL(server.URL)
	provider.SetIntegrationStore(db.NewIntegrationStore(pool))
	service.SetScheduling(db.NewCodeReviewScheduleStore(pool), provider)
	service.SetRetryDependencies(db.NewPullRequestStore(pool), provider)
	if scenario == "retry" || scenario == "dispute" {
		_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='failed',retryable_failure=true WHERE org_id=$1 AND session_id=$2`, org, sessionID)
		require.NoError(t, err, "terminalize original assessment")
		_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
		require.NoError(t, err, "release original starter")
	}
	// A deadline makes pool re-acquisition fail deterministically instead of
	// hanging the test suite when the transaction owns the sole connection.
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	switch scenario {
	case "wait":
		lockTx, lockErr := adminPool.Begin(ctx)
		require.NoError(t, lockErr, "hold competing admission transaction")
		defer func() { _ = lockTx.Rollback(ctx) }()
		_, lockErr = lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+org.String()+":"+pr.String())
		require.NoError(t, lockErr, "hold PR lock before preparing next request")
		done := make(chan error, 1)
		go func() {
			_, requestErr := service.RequestScheduledReview(bounded, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow})
			done <- requestErr
		}()
		select {
		case <-tokenReady:
		case <-bounded.Done():
			t.Fatal("identity preparation must complete before waiting for PR lock")
		}
		require.Eventually(t, func() bool { return pool.Stat().AcquiredConns() == 1 }, time.Second, time.Millisecond, "request must wait inside admission transaction after preparing identity")
		snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.HeadSHA = strings.Repeat("c", 40) })
		require.NoError(t, lockTx.Commit(ctx), "release competing admission lock after provider target changes")
		err = <-done
		if err == nil {
			state, loadErr := service.GetSchedule(ctx, org, pr)
			require.NoError(t, loadErr, "read target after serialized provider fetch")
			require.Equal(t, strings.Repeat("c", 40), state.HeadSHA, "provider snapshot must reflect changes made while waiting for lock")
		}
	case "schedule":
		_, err = service.RequestScheduledReview(bounded, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow})
	case "retry":
		_, err = service.RetryReview(bounded, RetryReviewInput{OrgID: org, SessionID: sessionID})
	case "dispute":
		_, err = service.HandleReviewChanged(bounded, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, GitHubRepo: "test/repo", GitHubPRNumber: 17, ExplicitRequest: true, ChangeKey: "dispute:pool-test", TriggerSource: models.CodeReviewTriggerSourceSlashCommand})
	case "validation":
		var allowed bool
		allowed, err = service.ValidateScheduledExecution(bounded, org, pr, sessionID)
		if err == nil {
			require.True(t, allowed, "fresh current generation remains valid")
		}
	}
	require.NoError(t, err, "scheduling path cannot reacquire its transaction's saturated pool")
}

func testSchedulingPreparationRecovery(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	ctx := context.Background()
	service, claim, _ := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	_, err := service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow, RequestContext: &ReviewRequestContext{Source: "ui", Body: "Reassess the new issue"}})
	require.NoError(t, err, "persist explicit intent before identity outage")
	before, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read durable pending input")
	snapshot.Lock()
	snapshot.prepareErr = fmt.Errorf("installation token unavailable")
	snapshot.Unlock()
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "preparation errors retain existing provider-outage backoff")
	held, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read pending outage state")
	require.Equal(t, before.PendingInput, held.PendingInput, "preparation failure preserves pending request")
	require.Equal(t, models.CodeReviewWaitContext, held.WaitReason, "preparation failure is reported as unavailable provider context")
	require.NotNil(t, held.RetryAt, "preparation failure durably reschedules without losing work")
	snapshot.Lock()
	snapshot.prepareErr = nil
	snapshot.Unlock()
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.HeadSHA = strings.Repeat("e", 40) })
	require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "recovered identity preparation dispatches current replacement")
	latest, err := db.NewCodeReviewStore(pool).GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read recovery assessment")
	require.Equal(t, strings.Repeat("e", 40), latest.HeadSHA, "recovery uses authoritative current revision")
}

func testSchedulingPreparationFastPaths(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture) {
	ctx := context.Background()
	service, _, sourceID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	request := ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), Mode: models.CodeReviewReviewNow}
	original, err := service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "record explicit request identity")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='failed',retryable_failure=true WHERE org_id=$1 AND session_id=$2`, org, sourceID)
	require.NoError(t, err, "terminalize retry source")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
	require.NoError(t, err, "finish retry source starter")
	retryInput := RetryReviewInput{OrgID: org, SessionID: sourceID}
	replacement, err := service.RetryReview(ctx, retryInput)
	require.NoError(t, err, "record successful retry replacement")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='stale' WHERE org_id=$1 AND session_id=$2`, org, sourceID)
	require.NoError(t, err, "represent obsolete worker after retry")
	snapshot.Lock()
	snapshot.prepareErr = fmt.Errorf("installation credentials temporarily unavailable")
	snapshot.Unlock()
	replay, err := service.RequestScheduledReview(ctx, request)
	require.NoError(t, err, "duplicate request needs no fresh provider credentials")
	require.Equal(t, original.RequestID, replay.RequestID, "explicit delivery identity remains stable during outage")
	require.Equal(t, models.CodeReviewRequestJoined, replay.Disposition, "request replay joins its persisted pending intent")
	retried, err := service.RetryReview(ctx, retryInput)
	require.NoError(t, err, "recorded retry needs no fresh provider credentials")
	require.Equal(t, RetryReviewResult{PreviousSessionID: sourceID, SessionID: replacement.SessionID, MetadataID: replacement.MetadataID}, retried, "retry replay returns persisted replacement without enqueueing a new job")
	allowed, err := service.ValidateScheduledExecution(ctx, org, pr, sourceID)
	require.NoError(t, err, "stale worker revocation needs no fresh provider credentials")
	require.False(t, allowed, "obsolete worker stays denied during credential outage")
}

func testSchedulingDraft(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, scenario string) {
	ctx := context.Background()
	service, claim, sourceID := schedulingLifecycleService(t, pool, org, repo, pr, snapshot)
	before, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read pre-draft generation")
	snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.IsDraft = true })
	canceller := &schedulingThreadCanceller{pool: pool}
	service.SetThreadCanceller(canceller)
	if scenario == "active_missed" || scenario == "active_webhook" {
		_, err = pool.Exec(ctx, `INSERT INTO session_threads(org_id,session_id,agent_type,label,status) VALUES($1,$2,'codex','Code review: draft transition','running')`, org, sourceID)
		require.NoError(t, err, "represent active review during draft conversion")
		if scenario == "active_webhook" {
			_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
			require.NoError(t, err, "observe draft conversion webhook")
		}
		allowed, err := service.ValidateScheduledExecution(ctx, org, pr, sourceID)
		require.NoError(t, err, "validate current draft review before fanout or publication")
		require.True(t, allowed, "draft conversion alone must not revoke execution authority")
		state, err := service.GetSchedule(ctx, org, pr)
		require.NoError(t, err, "read observed draft state")
		require.True(t, state.IsDraft, "draft remains observable without acting as a gate")
		require.Equal(t, before.Generation, state.Generation, "draft conversion does not change review target identity")
		require.Nil(t, canceller.cancelled, "draft conversion never cancels reviewer threads")
		active, err := db.NewCodeReviewStore(pool).GetBySessionID(ctx, org, sourceID)
		require.NoError(t, err, "read unchanged active review")
		require.Equal(t, models.CodeReviewSessionStatusQueued, active.Status, "draft conversion cannot stale an otherwise current review")
		return
	}
	if scenario == "retry" || scenario == "dispute" {
		_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='failed',retryable_failure=true WHERE org_id=$1 AND session_id=$2`, org, sourceID)
	} else {
		_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed' WHERE org_id=$1 AND session_id=$2`, org, sourceID)
		snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.HeadSHA = strings.Repeat("f", 40) })
	}
	require.NoError(t, err, "terminalize prior assessment before requesting draft review")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeRunCodeReview)
	require.NoError(t, err, "finish source starter")
	switch scenario {
	case "retry":
		_, err = service.RetryReview(ctx, RetryReviewInput{OrgID: org, SessionID: sourceID})
		require.NoError(t, err, "retry failed review while PR is draft")
	case "dispute":
		result, err := service.HandleReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, GitHubRepo: "test/repo", GitHubPRNumber: 17, ExplicitRequest: true, ChangeKey: "dispute:draft", TriggerSource: models.CodeReviewTriggerSourceDisputeReassessment})
		require.NoError(t, err, "reassess dispute while PR is draft")
		require.False(t, result.Deferred, "draft does not defer dispute reassessment")
	case "manual", "closed":
		userID := uuid.New()
		_, err = pool.Exec(ctx, `INSERT INTO users(id,org_id,email,name) VALUES($1,$2,$3,'Draft reviewer')`, userID, org, userID.String()+"@example.test")
		require.NoError(t, err, "seed authorized requester")
		if scenario == "closed" {
			snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.State = "closed" })
		}
		_, err = service.RequestScheduledReview(ctx, ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequestID: uuid.New(), RequesterID: &userID, Mode: models.CodeReviewReviewNow})
		if scenario == "closed" {
			require.ErrorIs(t, err, ErrReviewIneligible, "closed draft remains ineligible")
			return
		}
		require.NoError(t, err, "authorized Review now accepts an open draft")
	default:
		_, err = service.QueueReviewChanged(ctx, ReviewChangedInput{OrgID: org, RepositoryID: repo, PullRequestID: pr})
		require.NoError(t, err, "record automatic draft reassessment")
		if scenario == "legacy_hold" {
			_, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET state='paused',wait_reason='draft',eligible_at=NULL,retry_at=now()-interval '1 minute' WHERE org_id=$1 AND pull_request_id=$2`, org, pr)
			require.NoError(t, err, "represent persisted pre-upgrade draft hold")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND job_type=$2`, org, models.JobTypeReconcileCodeReviewSchedule)
			require.NoError(t, err, "represent lost legacy draft wake")
			require.NoError(t, service.scheduling.store.RepairMissingWakes(ctx), "normal repair recovers pending legacy draft hold")
		}
	}
	if scenario != "retry" && scenario != "dispute" {
		require.NoError(t, service.ReconcileSchedule(claim(), models.CodeReviewScheduleWake{OrgID: org, PullRequestID: pr}), "dispatch eligible draft from durable wake")
	}
	latest, err := db.NewCodeReviewStore(pool).GetLatestByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read draft assessment")
	require.NotEqual(t, sourceID, latest.SessionID, "draft request creates requested replacement")
	allowed, err := service.ValidateScheduledExecution(ctx, org, pr, latest.SessionID)
	require.NoError(t, err, "validate new draft review before worker execution")
	require.True(t, allowed, "draft review can fan out and publish normally")
	state, err := service.GetSchedule(ctx, org, pr)
	require.NoError(t, err, "read dispatched draft state")
	require.True(t, state.IsDraft, "draft observation remains available")
	require.Equal(t, models.CodeReviewScheduleRunning, state.State, "draft admission has normal running state")
	require.Equal(t, models.CodeReviewWaitNone, state.WaitReason, "draft leaves no obsolete hold reason")
}
