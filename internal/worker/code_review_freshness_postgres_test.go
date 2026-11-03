package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type freshnessPublicationPublisher struct {
	inlineRecoveryPublisher
	removed  []codereviewsvc.RequestedReviewersRequest
	onSubmit func()
}

type missingCheckpointDiscoverer struct {
	discovery models.CodeReviewVisualEvidenceDiscovery
	calls     int
}

func (d *missingCheckpointDiscoverer) DiscoverCodeReviewVisualEvidence(context.Context, uuid.UUID, uuid.UUID, int) (models.CodeReviewVisualEvidenceDiscovery, error) {
	d.calls++
	return d.discovery, nil
}

func TestMissingVisualCheckpointCannotRedefineAssessmentPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for actual missing visual checkpoint recovery proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	f := seedStrandedReview(t, pool, "idle")
	f.job.HeadSHA = strings.Repeat("a", 40)
	manifest := f.manifest
	manifest.Code.HeadSHA = f.job.HeadSHA
	source := models.CodeReviewVisualEvidenceSource{SourceID: "description-image", ProviderObjectID: "pr-9", SourceURL: "https://github.com/test/saved-review/pull/9", ImageURL: "https://127.0.0.1/unavailable.png", ImageIndex: 1, Surface: models.CodeReviewEvidenceSurfaceDescription, AuthorType: models.CodeReviewEvidenceAuthorTypeUser, Untrusted: true}
	baseline := models.CodeReviewVisualEvidenceSnapshot{AssessmentID: &f.assessment, Version: 1, RepositoryID: f.job.RepositoryID, Repository: "test/saved-review", PullRequestNumber: 9, HeadSHA: f.job.HeadSHA, CapturedAt: time.Now().UTC(), Complete: true, Evidence: []models.CodeReviewVisualEvidence{{Source: source, ContentSHA256: strings.Repeat("b", 64), Status: models.CodeReviewVisualEvidenceFetchStatusAvailable}}}
	manifest.Visual = codereviewsvc.AssessmentVisualInput(baseline)
	raw, err := json.Marshal(manifest)
	require.NoError(t, err, "encode original substantive evidence without a prompt checkpoint")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET head_sha=$3 WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID, f.job.HeadSHA)
	require.NoError(t, err, "retain exact original metadata identity")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET head_sha=$3,input_manifest=$4,result_origin='executed',decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{}',rendered_body='original result without checkpoint' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, f.job.HeadSHA, raw)
	require.NoError(t, err, "stage the immutable original result without its missing visual record")
	discoverer := &missingCheckpointDiscoverer{discovery: models.CodeReviewVisualEvidenceDiscovery{Version: 1, RepositoryID: f.job.RepositoryID, Repository: "test/saved-review", PullRequestNumber: 9, HeadSHA: f.job.HeadSHA, CapturedAt: baseline.CapturedAt, Sources: []models.CodeReviewVisualEvidenceSource{source}, SourceCount: 1}}
	visual := codereviewsvc.NewVisualEvidenceService(discoverer, f.stores.CodeReviews, f.stores.Repositories, nil, storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files"), zerolog.Nop())
	pr, err := f.stores.PullRequests.GetByID(ctx, f.job.OrgID, f.job.PullRequestID)
	require.NoError(t, err, "retain the original PR number for checkpoint capture")
	fresh := codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}, PullRequest: pr, VisualEvidence: baseline}
	fresh.Manifest.Visual.Images = append([]codereviewsvc.ReviewVisualImage(nil), manifest.Visual.Images...)
	fresh.Manifest.Visual.Images[0].ContentDigest, fresh.Manifest.VisualDigest = "unavailable", "unavailable"
	fresh.VisualEvidence.Evidence = append([]models.CodeReviewVisualEvidence(nil), baseline.Evidence...)
	fresh.VisualEvidence.Evidence[0].Status = models.CodeReviewVisualEvidenceFetchStatusUnavailable
	publisher := &inlineRecoveryPublisher{}
	services := &Services{CodeReviewInputCapture: &assessmentFreshnessCapture{result: fresh}, CodeReviewVisualEvidence: visual, CodeReviews: publisher}
	var records int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_prompt_records WHERE org_id=$1`, f.job.OrgID).Scan(&records), "prove the immutable visual service record is actually missing")
	require.Equal(t, 0, records, "the regression must enter the real missing-record rediscovery path")
	for i := 0; i < 2; i++ {
		recovered, err := recoverStagedFullAssessment(f.ctx(), f.stores, services, f.job)
		require.True(t, recovered, "the original saved result must retain recovery ownership")
		require.ErrorIs(t, err, errFullAssessmentFreshnessInconclusive, "rediscovered unavailable bytes cannot redefine immutable assessment evidence")
	}
	require.Equal(t, 1, discoverer.calls, "the first missing record is written once and subsequent retries restore it")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_prompt_records WHERE org_id=$1`, f.job.OrgID).Scan(&records), "read the actual visual service checkpoint created after absence")
	require.Equal(t, 1, records, "Fresh=false must exercise the actual rediscovery and checkpoint-write path")
	assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read retained original immutable assessment")
	var retained codereviewsvc.ReviewInputManifest
	require.NoError(t, json.Unmarshal(assessment.InputManifest, &retained), "decode the retained baseline after missing-record recovery")
	require.Equal(t, manifest, retained, "newly written unavailable checkpoint must never replace the original captured baseline")
	changed := fresh
	changed.Manifest.Visual.Images = append([]codereviewsvc.ReviewVisualImage(nil), fresh.Manifest.Visual.Images...)
	changed.Manifest.Visual.Images[0].ContentDigest = strings.Repeat("c", 64)
	changed.VisualEvidence.Evidence = append([]models.CodeReviewVisualEvidence(nil), fresh.VisualEvidence.Evidence...)
	changed.VisualEvidence.Evidence[0].Status = models.CodeReviewVisualEvidenceFetchStatusAvailable
	services.CodeReviewInputCapture = &assessmentFreshnessCapture{result: changed}
	require.ErrorIs(t, verifyFullAssessmentFreshness(ctx, services, f.job, assessment), errFullAssessmentInputsChanged, "different available bytes must invalidate even after a missing checkpoint was rediscovered")
	require.Empty(t, publisher.requests, "missing or rediscovered evidence must never publish unavailable or changed bytes")
	require.Equal(t, models.CodeReviewPublicationNotStarted, assessment.PublicationState, "missing evidence cannot start an external publication attempt")
}

func (p *freshnessPublicationPublisher) RemoveRequestedReviewers(_ context.Context, request codereviewsvc.RequestedReviewersRequest) error {
	p.removed = append(p.removed, request)
	return nil
}

func (p *freshnessPublicationPublisher) SubmitReview(ctx context.Context, request codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, error) {
	if p.onSubmit != nil {
		p.onSubmit()
	}
	return p.inlineRecoveryPublisher.SubmitReview(ctx, request)
}

func TestMainFullAssessmentFreshnessRecoveryPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for main-controller freshness recovery proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	for _, tt := range []struct {
		name   string
		cancel bool
	}{{name: "restored bytes publishes prepared saved outcome"}, {name: "parent cancellation prevents unsent publication", cancel: true}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			seedSavedReviewResults(t, pool, f)
			f.job.RequestedReviewerLogin = "review-bot"
			// Completed model results enter the normal handler's final decision path,
			// while preserving real parent cancellation checks.
			f.stores.SessionLogs = nil
			for _, label := range []string{"Reviewer", "Orchestrator"} {
				_, err := pool.Exec(ctx, `INSERT INTO session_threads(org_id,session_id,agent_type,label,status,execution_mode,filesystem_mode) VALUES($1,$2,'codex',$3,'idle','review','read_only')`, f.job.OrgID, f.job.SessionID, label)
				require.NoError(t, err, "retain original completed-model thread identities")
			}
			finding := uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO code_review_findings(id,org_id,session_id,severity,confidence,path,start_line,end_line,summary,body,dedupe_key) VALUES($1,$2,$3,'high','high','file.go',7,7,'Unresolved defect','Fix the original defect','original-finding')`, finding, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "save a real initially unselected inline finding")
			files := []codereviewsvc.PullRequestFile{{Filename: "file.go", Patch: "@@ -7 +7 @@\n-old\n+new", Additions: 4, Deletions: 2}}
			visual := models.CodeReviewVisualEvidenceSnapshot{Version: 1, RepositoryID: f.job.RepositoryID, PullRequestNumber: 9, AssessmentID: &f.assessment, Complete: true, HeadSHA: "head", Evidence: []models.CodeReviewVisualEvidence{{Source: models.CodeReviewVisualEvidenceSource{SourceID: "image-1", SourceURL: "https://example.invalid/image"}, ContentSHA256: "original", Status: models.CodeReviewVisualEvidenceFetchStatusAvailable}}}
			manifest := f.manifest
			manifest.Visual = codereviewsvc.AssessmentVisualInput(visual)
			raw, err := json.Marshal(manifest)
			require.NoError(t, err, "encode the captured original visual input")
			_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET input_manifest=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, raw)
			require.NoError(t, err, "retain an unstaged assessment before its first final decision")
			fresh := codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}, VisualEvidence: visual, Files: files}
			fresh.Manifest.Visual.Images = append([]codereviewsvc.ReviewVisualImage(nil), manifest.Visual.Images...)
			fresh.Manifest.Visual.Images[0].ContentDigest, fresh.Manifest.VisualDigest = "unavailable", "unavailable"
			fresh.VisualEvidence.Evidence = append([]models.CodeReviewVisualEvidence(nil), visual.Evidence...)
			fresh.VisualEvidence.Evidence[0].Status = models.CodeReviewVisualEvidenceFetchStatusUnavailable
			capture := &assessmentFreshnessCapture{result: fresh}
			publisher := &freshnessPublicationPublisher{inlineRecoveryPublisher: inlineRecoveryPublisher{files: files}}
			services := &Services{CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviewVisualEvidence: &assessmentVisualCheckpoint{snapshot: visual}, CodeReviews: publisher}
			handler := newRunCodeReviewHandler(f.stores, services, zerolog.Nop())
			payload, err := json.Marshal(f.job)
			require.NoError(t, err, "encode the original main review controller")
			var originalThreads, originalResults []uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY id) FROM session_threads WHERE org_id=$1`, f.job.OrgID).Scan(&originalThreads), "snapshot original thread identities")
			require.NoError(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY id) FROM code_review_agent_results WHERE org_id=$1`, f.job.OrgID).Scan(&originalResults), "snapshot original model result identities")
			err = handler(f.ctx(), "run_code_review", payload)
			require.ErrorIs(t, err, errFullAssessmentFreshnessInconclusive, "the first main-controller publication check must retry unavailable evidence")
			var retry *RetryableError
			require.ErrorAs(t, err, &retry, "the first freshness failure must retain its bounded typed retry")
			staged, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the outcome staged by the actual main handler")
			require.NotNil(t, staged.RenderedBody, "main execution must save the original synthesized body")
			var selected bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT selected_for_inline FROM code_review_findings WHERE org_id=$1 AND id=$2`, f.job.OrgID, finding).Scan(&selected), "read publication preparation before retry")
			require.True(t, selected, "inline selection must be durable before freshness failure enters recovery")
			capture.result = codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}, VisualEvidence: visual, Files: files}
			if tt.cancel {
				_, err = pool.Exec(ctx, `UPDATE sessions SET status='cancelled' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "cancel the parent while verification is waiting")
			} else {
				publisher.onSubmit = func() {
					metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
					require.NoError(t, err, "inspect operational phase at the external send")
					require.Equal(t, new(models.CodeReviewPhasePublishing), metadata.Phase, "recovered publication must restore its publishing phase")
				}
			}
			require.NoError(t, handler(f.ctx(), "run_code_review", payload), "the same controller must finish recovery without rerunning models")
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read terminal main-controller metadata")
			assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read original terminal assessment")
			if tt.cancel {
				require.Empty(t, publisher.requests, "parent cancellation during verification must prevent publication")
				require.Equal(t, models.CodeReviewSessionStatusCancelled, metadata.Status, "cancelled parent must retire the review metadata")
				require.Equal(t, models.CodeReviewAssessmentCancelled, assessment.Status, "cancelled unsent assessment must settle immediately")
			} else {
				require.Len(t, publisher.requests, 1, "restored evidence must publish once through the original controller")
				require.Equal(t, *staged.RenderedBody, publisher.requests[0].Body, "recovery must publish the exact saved synthesized body")
				require.Equal(t, f.job.OutputKey, publisher.requests[0].OutputKey, "recovery must preserve its original external publication identity")
				require.Equal(t, []codereviewsvc.SubmitReviewComment{{Path: "file.go", Line: 7, Body: "[P1] Fix the original defect", DedupeKey: "original-finding"}}, publisher.requests[0].Comments, "recovery must publish the original inline finding")
				var additions, deletions *int
				require.NoError(t, pool.QueryRow(ctx, `SELECT additions,deletions FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID).Scan(&additions, &deletions), "read final line change metrics")
				require.Equal(t, new(4), additions, "recovery must retain line addition metrics")
				require.Equal(t, new(2), deletions, "recovery must retain line deletion metrics")
				require.Equal(t, models.CodeReviewSessionStatusCompleted, metadata.Status, "recovered review must complete metadata")
				require.Equal(t, models.CodeReviewAssessmentCompleted, assessment.Status, "recovery must complete the original assessment")
				parent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "read parent session after successful recovery")
				require.Equal(t, models.SessionStatusCompleted, parent.Status, "successful recovery must reconcile parent session success")
			}
			require.Equal(t, []codereviewsvc.RequestedReviewersRequest{{InstallationID: 1, Repository: "test/saved-review", PullNumber: 9, Reviewers: []string{"review-bot"}}}, publisher.removed, "both publication and cancellation must clean up the requested reviewer")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
			require.NoError(t, err, "finish the same original controller after recovery")
			schedule := db.NewCodeReviewScheduleStore(pool)
			for i := 0; i < 3; i++ {
				require.NoError(t, schedule.ReconcileTerminalReviews(ctx, f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID), "terminal recovery must remain idempotent")
				require.NoError(t, schedule.RepairMissingWakes(ctx), "repair must not recreate cancelled or completed publication")
			}
			state, err := schedule.Get(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read settled PR scheduling state")
			require.Nil(t, state.ActiveSessionID, "terminal recovery must release scheduling ownership")
			var threads, results []uuid.UUID
			var counts []int
			require.NoError(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY id) FROM session_threads WHERE org_id=$1`, f.job.OrgID).Scan(&threads), "read retained thread identities")
			require.NoError(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY id) FROM code_review_agent_results WHERE org_id=$1`, f.job.OrgID).Scan(&results), "read retained model result identities")
			require.Equal(t, originalThreads, threads, "freshness recovery cannot create new model threads")
			require.Equal(t, originalResults, results, "freshness recovery cannot create new model results")
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM code_review_revision_assessments WHERE org_id=$1),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type='run_code_review')]`, f.job.OrgID).Scan(&counts), "count original review work after repair")
			require.Equal(t, []int{1, 1}, counts, "recovery and cancellation must keep a single assessment and controller")
		})
	}
}

func TestUncertainFullAssessmentFreshnessReconcilesWithoutSendPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for uncertain publication verification proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	f := seedStrandedReview(t, pool, "idle")
	visual := models.CodeReviewVisualEvidenceSnapshot{AssessmentID: &f.assessment, Complete: true, HeadSHA: "head", Evidence: []models.CodeReviewVisualEvidence{{Source: models.CodeReviewVisualEvidenceSource{SourceID: "image-1", SourceURL: "https://example.invalid/image"}, ContentSHA256: "original", Status: "available"}}}
	manifest := f.manifest
	manifest.Visual = codereviewsvc.AssessmentVisualInput(visual)
	raw, err := json.Marshal(manifest)
	require.NoError(t, err, "encode immutable uncertain publication input")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET input_manifest=$3,status='publishing',publication_state='uncertain',submitted_commit_sha='head',result_origin='executed',decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{}',rendered_body='saved uncertain outcome' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, raw)
	require.NoError(t, err, "retain the durable uncertain send fence")
	fresh := codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}, VisualEvidence: visual}
	fresh.Manifest.Visual.Images = append([]codereviewsvc.ReviewVisualImage(nil), manifest.Visual.Images...)
	fresh.Manifest.Visual.Images[0].ContentDigest, fresh.Manifest.VisualDigest = "unavailable", "unavailable"
	fresh.VisualEvidence.Evidence = append([]models.CodeReviewVisualEvidence(nil), visual.Evidence...)
	fresh.VisualEvidence.Evidence[0].Status = "unavailable"
	publisher := &fakeRecheckPublisher{}
	services := &Services{CodeReviewInputCapture: &assessmentFreshnessCapture{result: fresh}, CodeReviewVisualEvidence: &assessmentVisualCheckpoint{snapshot: visual}, CodeReviews: publisher}
	for i := 0; i < 2; i++ {
		recovered, err := recoverStagedFullAssessment(f.ctx(), f.stores, services, f.job)
		require.True(t, recovered, "uncertain saved outcome must retain publication recovery ownership")
		require.ErrorContains(t, err, "pending reconciliation", "missing receipt must retain uncertainty instead of scheduling a new analysis")
		require.False(t, errors.Is(err, errCodeReviewPublicationSuperseded), "freshness loss must not supersede an uncertain external send")
	}
	require.Empty(t, publisher.requests, "uncertain publication must never send again while fresh evidence is unavailable")
	require.Len(t, publisher.reconciliations, 2, "each retry must use read-only receipt reconciliation")
	assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read retained publication fence")
	require.Equal(t, models.CodeReviewAssessmentPublishing, assessment.Status, "uncertain assessment must preserve its publishing state")
	require.Equal(t, models.CodeReviewPublicationUncertain, assessment.PublicationState, "unverified send must retain durable uncertainty")
}

func TestFullAssessmentFreshnessRetryLifecyclePostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for freshness retry lifecycle proof")
	}
	ctx := context.Background()
	tests := []struct {
		name    string
		expire  bool
		recover bool
	}{{name: "attempt exhaustion"}, {name: "durable deadline", expire: true}, {name: "restored original bytes", recover: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// A parallel case's schema-wide sweep can re-enqueue this controller
			// between dead-lettering and the hook failing its assessment.
			// Isolate schemas so each case observes only its own repair sweeps.
			pool := fullRecoveryPostgresPool(t, ctx)
			f := seedStrandedReview(t, pool, "idle")
			seedSavedReviewResults(t, pool, f)
			visual := models.CodeReviewVisualEvidenceSnapshot{AssessmentID: &f.assessment, Complete: true, HeadSHA: "head", Evidence: []models.CodeReviewVisualEvidence{{Source: models.CodeReviewVisualEvidenceSource{SourceID: "image-1", SourceURL: "https://example.invalid/image"}, ContentSHA256: "original", Status: models.CodeReviewVisualEvidenceFetchStatusAvailable}}}
			manifest := f.manifest
			manifest.Visual = codereviewsvc.AssessmentVisualInput(visual)
			raw, err := json.Marshal(manifest)
			require.NoError(t, err, "encode original immutable visual evidence")
			_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET input_manifest=$3,result_origin='executed',decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{}',rendered_body='original saved outcome' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, raw)
			require.NoError(t, err, "stage the original completed reviewer outcome")
			node := "freshness-" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register a dedicated test worker")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='pending',lock_token=NULL,attempts=0,max_attempts=2,priority=100000,target_node_id=$3,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, node)
			require.NoError(t, err, "schedule only the existing controller with a bounded attempt budget")
			if tt.expire {
				_, err = pool.Exec(ctx, `UPDATE jobs SET retry_window_started_at=now()-interval '6 minutes' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
				require.NoError(t, err, "retain an older external GitHub retry phase")
			}
			for _, label := range []string{"Reviewer", "Orchestrator"} {
				_, err = pool.Exec(ctx, `INSERT INTO session_threads(org_id,session_id,agent_type,label,status,execution_mode,filesystem_mode) VALUES($1,$2,'codex',$3,'idle','review','read_only')`, f.job.OrgID, f.job.SessionID, label)
				require.NoError(t, err, "retain the original idle review threads")
			}
			fresh := codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}, VisualEvidence: visual}
			fresh.Manifest.Visual.Images = append([]codereviewsvc.ReviewVisualImage(nil), manifest.Visual.Images...)
			fresh.Manifest.Visual.Images[0].ContentDigest = "temporarily-unavailable"
			fresh.Manifest.VisualDigest = "temporarily-unavailable"
			fresh.VisualEvidence.Evidence = append([]models.CodeReviewVisualEvidence(nil), visual.Evidence...)
			fresh.VisualEvidence.Evidence[0].Status = models.CodeReviewVisualEvidenceFetchStatusUnavailable
			capture, checkpoint := &assessmentFreshnessCapture{result: fresh}, &assessmentVisualCheckpoint{snapshot: visual}
			publisher := &inlineRecoveryPublisher{}
			services := &Services{CodeReviewInputCapture: capture, CodeReviewVisualEvidence: checkpoint, CodeReviews: publisher}
			worker := f.newWorker(t, pool, node)
			worker.Register(models.JobTypeRunCodeReview, newRunCodeReviewHandler(f.stores, services, zerolog.Nop()))
			worker.poll(ctx)
			var jobStatus string
			var attempts int
			var retryStart *time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts,(payload->>'_code_review_freshness_retry_started_at')::timestamptz FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&jobStatus, &attempts, &retryStart), "read the durable phase-specific verification retry")
			require.Equal(t, "pending", jobStatus, "temporary fetch loss must retry the same controller")
			require.Equal(t, 1, attempts, "the first verification failure must consume one attempt")
			require.NotNil(t, retryStart, "the recovery deadline must start durably")
			if tt.expire {
				require.WithinDuration(t, time.Now(), *retryStart, 5*time.Second, "the older external deadline must not consume a new freshness phase")
				_, err = pool.Exec(ctx, `UPDATE jobs SET max_attempts=8,payload=jsonb_set(payload,'{_code_review_freshness_retry_started_at}',to_jsonb(now()-interval '6 minutes')) WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
				require.NoError(t, err, "expire the durable verification budget")
				worker = f.newWorker(t, pool, node)
				worker.Register(models.JobTypeRunCodeReview, newRunCodeReviewHandler(f.stores, services, zerolog.Nop()))
			}
			if tt.recover {
				capture.result = codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}, VisualEvidence: visual}
			}
			_, err = pool.Exec(ctx, `UPDATE jobs SET run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
			require.NoError(t, err, "make the next test retry runnable without waiting")
			worker.poll(ctx)
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&jobStatus), "read exhausted controller")
			wantJobStatus, wantAssessment := "dead_letter", models.CodeReviewAssessmentFailed
			if tt.recover {
				wantJobStatus, wantAssessment = "succeeded", models.CodeReviewAssessmentCompleted
			}
			require.Equal(t, wantJobStatus, jobStatus, "verification must complete on restored bytes or stop after exhausting its budget")
			assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read terminal assessment")
			require.Equal(t, wantAssessment, assessment.Status, "verification must retain the same assessment through recovery or exhaustion")
			if !tt.recover {
				require.NotContains(t, *assessment.FailureDetail, "full_review:", "failure must not advertise permission to recreate reviewer work")
			}
			require.Equal(t, "original saved outcome", *assessment.RenderedBody, "verification retries must preserve the original model result")
			if tt.recover {
				require.Len(t, publisher.requests, 1, "restored original bytes must publish the saved review exactly once")
				require.Equal(t, "original saved outcome", publisher.requests[0].Body, "successful recovery must use the saved model output")
				require.Equal(t, f.job.OutputKey, publisher.requests[0].OutputKey, "successful recovery must preserve publication identity")
			} else {
				require.Empty(t, publisher.requests, "fresh unavailable bytes must never cause an external publication")
			}
			schedule := db.NewCodeReviewScheduleStore(pool)
			for i := 0; i < 3; i++ {
				require.NoError(t, schedule.ReconcileTerminalReviews(ctx, f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID), "repeated terminal reconciliation must remain quiet")
				require.NoError(t, schedule.RepairMissingWakes(ctx), "repeated repair must not resurrect exhausted verification")
			}
			var counts []int
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM code_review_revision_assessments WHERE org_id=$1),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type IN ('run_code_review','run_code_review_recheck','run_agent','continue_session')),(SELECT count(*)::int FROM session_threads WHERE org_id=$1),(SELECT count(*)::int FROM code_review_agent_results WHERE org_id=$1)]`, f.job.OrgID).Scan(&counts), "count durable reviewer work after repeated repair")
			require.Equal(t, []int{1, 1, 2, 2}, counts, "verification exhaustion cannot create assessments, review jobs, threads, or rerun reviewer results")
		})
	}
}
