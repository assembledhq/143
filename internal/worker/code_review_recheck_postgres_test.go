package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fixedRecheckCapture struct {
	result          codereviewsvc.AssessmentInputCaptureResult
	changedManifest *codereviewsvc.ReviewInputManifest
	flipOnCall      int
	calls           int
	captureErr      error
}

func (f *fixedRecheckCapture) CaptureAssessmentInputs(context.Context, codereviewsvc.AssessmentInputCaptureRequest) (codereviewsvc.AssessmentInputCaptureResult, error) {
	f.calls++
	if f.captureErr != nil {
		return codereviewsvc.AssessmentInputCaptureResult{}, f.captureErr
	}
	result := f.result
	if f.changedManifest != nil && f.calls >= f.flipOnCall {
		result.Manifest = *f.changedManifest
	}
	return result, nil
}

type fakeRecheckPublisher struct {
	requests        []codereviewsvc.SubmitReviewRequest
	reconciliations []codereviewsvc.SubmitReviewRequest
}

type fakeRecheckLifecycle struct {
	fallbacks []uuid.UUID
}

func (*fakeRecheckLifecycle) QueueReviewChanged(context.Context, codereviewsvc.ReviewChangedInput) (codereviewsvc.ReviewRequestedResult, error) {
	return codereviewsvc.ReviewRequestedResult{}, nil
}

func (*fakeRecheckLifecycle) HandleReviewChanged(context.Context, codereviewsvc.ReviewChangedInput) (codereviewsvc.ReviewRequestedResult, error) {
	return codereviewsvc.ReviewRequestedResult{}, nil
}

func (f *fakeRecheckLifecycle) FallbackAssessmentToFull(_ context.Context, _ uuid.UUID, assessmentID uuid.UUID, _ string) error {
	f.fallbacks = append(f.fallbacks, assessmentID)
	return nil
}

func (f *fakeRecheckPublisher) SubmitReview(_ context.Context, req codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, error) {
	f.requests = append(f.requests, req)
	return codereviewsvc.SubmitReviewResult{ID: 77, URL: "https://example.invalid/review/77", SubmittedCommitSHA: req.HeadSHA}, nil
}

func (f *fakeRecheckPublisher) ReconcileAssessmentPublication(_ context.Context, req codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, bool, error) {
	f.reconciliations = append(f.reconciliations, req)
	return codereviewsvc.SubmitReviewResult{}, false, nil
}

// This fixture deliberately uses the complete migration chain. It checks the
// supervisor against real assessment, dispatch, message, inbox, and job rows;
// adapter execution and GitHub publication are separate boundaries.
func TestCodeReviewRecheckSupervisorPostgres(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL supervisor proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect disposable PostgreSQL")
	schema := "review_supervisor_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated supervisor schema")
	t.Cleanup(func() {
		_, cleanupErr := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "drop isolated supervisor schema")
		require.NoError(t, admin.Close(ctx), "close database connection")
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse PostgreSQL URL")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create isolated supervisor pool")
	t.Cleanup(pool.Close)
	migrations, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	require.NoError(t, err, "list actual migrations")
	sort.Strings(migrations)
	for _, path := range migrations {
		up, readErr := os.ReadFile(path)
		require.NoError(t, readErr, "read actual migration")
		_, applyErr := pool.Exec(ctx, string(up))
		require.NoError(t, applyErr, "apply actual migration "+filepath.Base(path))
	}
	org, integration, repo, pr, policy, session, thread, metadata, baseline, recheck := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repoName := "test/recheck"
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations(id,name) VALUES($1,'Review Test')`, []any{org}},
		{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, org}},
		{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,1,$4,'https://example.invalid/recheck.git',1)`, []any{repo, org, integration, repoName}},
		{`INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','idle')`, []any{session, org}},
		{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title) VALUES($1,$2,7,'https://example.invalid/pr/7',$3,'Visual change')`, []any{pr, org, repoName}},
		{`INSERT INTO code_review_policies(id,org_id,repository_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,NULL,1,'approve_acceptable','{}','{}','{}','','', '{"enabled":true}')`, []any{policy, org}},
		{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($1,$2,$3,$4,$5,$6,'base','head','slash_command','completed','baseline-output')`, []any{metadata, org, session, repo, pr, policy}},
		{`INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,execution_mode,filesystem_mode) VALUES($1,$2,$3,'codex','Orchestrator','idle','review','read_only')`, []any{thread, org, session}},
	} {
		_, err = pool.Exec(ctx, stmt.sql, stmt.args...)
		require.NoError(t, err, "seed complete supervisor parent")
	}
	visualReq := models.CodeReviewDescriptionRequirement{Key: "screenshot", Title: "Screenshot", Prompt: "Show UI", Required: true, EvidenceKind: models.CodeReviewDescriptionEvidenceKindVisual}
	policyConfig := models.DefaultCodeReviewPolicyConfig()
	policyConfig.ContinuationPolicy = &models.CodeReviewContinuationPolicy{Enabled: true}
	policyConfig.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	policyConfig.AgentRoster.RequireReviewerQuorum = 1
	policyConfig.AgentRoster.Reviewers = []models.AgentType{models.AgentTypeCodex}
	policyConfig.DescriptionPolicy.Requirements = []models.CodeReviewDescriptionRequirement{visualReq}
	manifestInput := codereviewsvc.ReviewInputCapture{
		Code:     codereviewsvc.ReviewCodeInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, HeadSHA: "head", BaseSHA: "base", BaseRef: "main", FilesComplete: true},
		Contract: codereviewsvc.ReviewContractInput{PolicyID: policy, PolicyVersion: 1, PolicyDigest: strings.Repeat("a", 64), RosterDigest: strings.Repeat("b", 64), ModelConfigurationDigest: strings.Repeat("c", 64), PromptContractVersion: "1", PromptContentDigest: strings.Repeat("d", 64), InstructionsDigest: strings.Repeat("e", 64), ExternalInputsComplete: true},
		Title:    "Visual change", Description: "Adds a screenshot for the UI change.", Visual: codereviewsvc.ReviewVisualInput{CaptureComplete: true, SourceProvenanceComplete: true, Images: []codereviewsvc.ReviewVisualImage{{SourceID: "image-1", SourceURL: "https://example.invalid/image.png", ContentDigest: strings.Repeat("f", 64)}}}, Gates: codereviewsvc.ReviewGateInput{SnapshotDigest: strings.Repeat("1", 64), EligibilityDigest: strings.Repeat("2", 64), ChecksDigest: strings.Repeat("3", 64), DynamicDigest: strings.Repeat("4", 64), ChecksVerified: true, Complete: true},
	}
	manifestInput.TextEvidence = codereviewsvc.ReviewTextInput{Complete: true, SourceProvenanceComplete: true, UnclassifiedDigest: strings.Repeat("0", 64), Items: []codereviewsvc.ReviewTextEvidence{
		{EvidenceID: "pr-body", Surface: "pull_request_description", ProviderObjectID: "7", SourceURL: "https://example.invalid/pr/7", Section: "full", Content: manifestInput.Description, ContentDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(manifestInput.Description)))},
		{EvidenceID: "test-log", Surface: "pull_request_comment", ProviderObjectID: "77", SourceURL: "https://example.invalid/pr/7#issuecomment-77", Section: "testing", Content: "The browser test passes with the changed UI.", ContentDigest: fmt.Sprintf("%x", sha256.Sum256([]byte("The browser test passes with the changed UI.")))},
	}}
	baselineInput := manifestInput
	baselineInput.Visual.Images = nil
	baselineInput.TextEvidence.Items = append([]codereviewsvc.ReviewTextEvidence(nil), manifestInput.TextEvidence.Items[:1]...)
	baselineManifest, err := codereviewsvc.BuildReviewInputManifest(baselineInput)
	require.NoError(t, err, "build prior full-review input manifest")
	baselineManifestJSON, err := json.Marshal(baselineManifest)
	require.NoError(t, err, "encode prior full-review input manifest")
	manifest, err := codereviewsvc.BuildReviewInputManifest(manifestInput)
	require.NoError(t, err, "build comparable immutable input manifest")
	manifestJSON, err := json.Marshal(manifest)
	require.NoError(t, err, "encode captured input manifest")
	assessmentSQL := `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,source_assessment_id,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,coverage_complete,status,result_origin,decision,acceptable,structured_outcome,publication_key,publication_state,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'base','main','head',$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)`
	_, err = pool.Exec(ctx, assessmentSQL, baseline, org, repo, repoName, pr, metadata, session, policy, 1, nil, baselineManifest.InputVersion, baselineManifest.CodeDigest, baselineManifest.ContractDigest, baselineManifest.IntentDigest, baselineManifest.VisualDigest, baselineManifest.RequestDigest, baselineManifest.GateDigest, baselineManifest.InputDigest, baselineManifestJSON, "full", "initial_full", true, "completed", "executed", "needs_human_review", false, json.RawMessage(`{}`), "baseline-publication", "not_required", time.Now().UTC())
	require.NoError(t, err, "seed complete full baseline assessment")
	_, err = pool.Exec(ctx, assessmentSQL, recheck, org, repo, repoName, pr, metadata, session, policy, 2, baseline, manifest.InputVersion, manifest.CodeDigest, manifest.ContractDigest, manifest.IntentDigest, manifest.VisualDigest, manifest.RequestDigest, manifest.GateDigest, manifest.InputDigest, manifestJSON, "evidence_only", "visual_changed", false, "running", nil, nil, nil, nil, "recheck-publication", "not_started", nil)
	require.NoError(t, err, "seed running evidence-only assessment")
	synthesis := codeReviewOrchestratorSynthesis{Summary: "Code is sound", ReviewSummary: "Visual evidence missing", DescriptionAssessments: []codeReviewDescriptionAssessment{{Key: "screenshot", Status: codeReviewDescriptionAssessmentMissing, EvidenceBasis: models.CodeReviewDescriptionEvidenceBasisMissing, Reason: "No screenshot yet"}}, Findings: []codeReviewOrchestratorFinding{}, HumanReviewReasons: []codeReviewOrchestratorHumanReviewReason{}}
	structured, err := json.Marshal(codeReviewOrchestratorStructuredResult{ThreadID: thread.String(), Synthesis: synthesis, SynthesisValidated: true, ReadOnly: true})
	require.NoError(t, err, "encode baseline orchestrator evidence")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,agent_provider,role,status,structured_result,assessment_id) VALUES($1,$2,$3,'codex','orchestrator','completed',$4,$5)`, uuid.New(), org, session, structured, baseline)
	require.NoError(t, err, "seed one completed baseline orchestrator")
	visual := models.CodeReviewVisualEvidenceSnapshot{Complete: true, Evidence: []models.CodeReviewVisualEvidence{{EvidenceID: "image-1", Source: models.CodeReviewVisualEvidenceSource{SourceID: "image-1"}, Status: models.CodeReviewVisualEvidenceFetchStatusAvailable, StoredURL: "https://example.invalid/image.png", ContentSHA256: strings.Repeat("f", 64)}}}
	capture := &fixedRecheckCapture{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, VisualEvidence: visual, Policy: models.CodeReviewPolicyRecord{ContinuationPolicy: policyConfig.ContinuationPolicy, DescriptionPolicy: policyConfig.DescriptionPolicy, ApprovalMode: policyConfig.ApprovalMode, AgentRoster: policyConfig.AgentRoster, Enabled: true}}}
	stores := &Stores{CodeReviews: db.NewCodeReviewStore(pool), CodeReviewAssessments: db.NewCodeReviewAssessmentStore(pool), CodeReviewRechecks: db.NewCodeReviewRecheckStore(pool), SessionThreads: db.NewSessionThreadStore(pool), SessionMessages: db.NewSessionMessageStore(pool), ThreadSendTx: pool, Repositories: db.NewRepositoryStore(pool), PullRequests: db.NewPullRequestStore(pool)}
	publisher := &fakeRecheckPublisher{}
	services := &Services{CodeReviewInputCapture: capture, CodeReviewRechecksEnabled: true, CodeReviews: publisher, FrontendURL: "https://app.example.invalid"}
	handler := newRunCodeReviewRecheckHandler(stores, services, zerolog.Nop())
	jobJSON, err := json.Marshal(codeReviewRecheckJob{OrgID: org, AssessmentID: recheck})
	require.NoError(t, err, "encode recheck job")
	err = handler(ctx, "run_code_review_recheck", jobJSON)
	require.Error(t, err, "supervisor should wait for its bounded orchestrator turn")
	dispatch, err := stores.CodeReviewRechecks.Get(ctx, org, recheck)
	require.NoError(t, err, "one orchestrator dispatch should be durable")
	require.Equal(t, models.CodeReviewRecheckDispatchPending, dispatch.Status, "supervisor should await exact orchestrator receipt")
	var reviewerJobs, continuationJobs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE job_type='run_code_review'),COUNT(*) FILTER (WHERE job_type='continue_session') FROM jobs WHERE org_id=$1`, org).Scan(&reviewerJobs, &continuationJobs), "count reviewer and continuation jobs")
	require.Equal(t, 0, reviewerJobs, "visual-only recheck must not queue reviewers")
	require.Equal(t, 1, continuationJobs, "visual-only recheck should queue one orchestrator continuation")
	lease := uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$2 WHERE org_id=$1 AND id=$3`, org, lease, dispatch.JobID)
	require.NoError(t, err, "lease orchestrator continuation")
	owned, err := stores.CodeReviewRechecks.Claim(ctx, org, recheck, dispatch.JobID, lease, session, thread, dispatch.ExpectedTurn, dispatch.MessageID)
	require.NoError(t, err, "claim exact orchestrator turn")
	require.True(t, owned, "same job lease should own orchestrator turn")
	_, err = stores.CodeReviewRechecks.Complete(ctx, models.CodeReviewRecheckTurnCompletion{OrgID: org, AssessmentID: recheck, SessionID: session, ThreadID: thread, JobID: dispatch.JobID, LockToken: lease, ExpectedTurn: dispatch.ExpectedTurn, SessionTurn: 1, Summary: `{"wrong":"shape"}`, Result: &models.SessionResult{}, ProviderSessionID: "test-provider", SnapshotKey: "test-snapshot"})
	require.NoError(t, err, "persist synthetically malformed exact assistant turn")
	err = handler(ctx, "run_code_review_recheck", jobJSON)
	require.NoError(t, err, "malformed output should fail the assessment without publishing")
	afterMalformed, err := stores.CodeReviewAssessments.GetByID(ctx, org, recheck)
	require.NoError(t, err, "read malformed assessment")
	require.Equal(t, models.CodeReviewAssessmentFailed, afterMalformed.Status, "malformed output must retain baseline blockers and fail closed")
	require.Nil(t, afterMalformed.Decision, "malformed output cannot publish an approval")
	var totalJobs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1`, org).Scan(&totalJobs), "count all jobs after malformed response")
	require.Equal(t, 1, totalJobs, "malformed result must not enqueue a reviewer or another orchestrator")
	changedAssessment := uuid.New()
	changedInput := manifestInput
	changedInput.Visual.Images = []codereviewsvc.ReviewVisualImage{{SourceID: "image-1", SourceURL: "https://example.invalid/image.png", ContentDigest: strings.Repeat("9", 64)}}
	changedManifest, err := codereviewsvc.BuildReviewInputManifest(changedInput)
	require.NoError(t, err, "build valid changed screenshot capture")
	changedManifestJSON, err := json.Marshal(changedManifest)
	require.NoError(t, err, "encode changed captured manifest")
	_, err = pool.Exec(ctx, assessmentSQL, changedAssessment, org, repo, repoName, pr, metadata, session, policy, 3, baseline, changedManifest.InputVersion, changedManifest.CodeDigest, changedManifest.ContractDigest, changedManifest.IntentDigest, changedManifest.VisualDigest, changedManifest.RequestDigest, changedManifest.GateDigest, changedManifest.InputDigest, changedManifestJSON, "evidence_only", "visual_changed", false, "running", nil, nil, nil, nil, "changed-publication", "not_started", nil)
	require.NoError(t, err, "seed changed-input assessment")
	changedJob, err := json.Marshal(codeReviewRecheckJob{OrgID: org, AssessmentID: changedAssessment})
	require.NoError(t, err, "encode changed-input assessment job")
	err = handler(ctx, "run_code_review_recheck", changedJob)
	require.Error(t, err, "changed input should request full-review fallback when lifecycle service is absent")
	afterChange, err := stores.CodeReviewAssessments.GetByID(ctx, org, changedAssessment)
	require.NoError(t, err, "read changed-input assessment")
	require.Equal(t, models.CodeReviewAssessmentFailed, afterChange.Status, "changed input must close evidence-only assessment")
	require.Contains(t, *afterChange.FailureDetail, "full_review:inputs changed", "changed input should carry full-review route reason")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1`, org).Scan(&totalJobs), "count jobs after changed input")
	require.Equal(t, 1, totalJobs, "changed input must not queue an unsafe continuation")
	reviewerResultID, findingID := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,agent_provider,role,status,assessment_id) VALUES($1,$2,$3,'codex','reviewer','completed',$4)`, reviewerResultID, org, session, baseline)
	require.NoError(t, err, "seed one usable original reviewer result")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_findings(id,org_id,session_id,agent_result_id,assessment_id,dedupe_key,severity,confidence,summary,body) VALUES($1,$2,$3,$4,$5,'missing-test-proof','high','high','Test coverage unproven','No current test output was present')`, findingID, org, session, reviewerResultID, baseline)
	require.NoError(t, err, "seed original high-severity finding with durable assessment identity")
	_, err = pool.Exec(ctx, `UPDATE pull_requests SET head_sha='head',base_sha='base',merge_state='clean',has_conflicts=false,failing_test_count=0 WHERE org_id=$1 AND id=$2`, org, pr)
	require.NoError(t, err, "seed current clean PR health")
	capture.result.PullRequest, err = stores.PullRequests.GetByID(ctx, org, pr)
	require.NoError(t, err, "load exact current PR for fresh capture")
	happyAssessment := uuid.New()
	_, err = pool.Exec(ctx, assessmentSQL, happyAssessment, org, repo, repoName, pr, metadata, session, policy, 4, baseline, manifest.InputVersion, manifest.CodeDigest, manifest.ContractDigest, manifest.IntentDigest, manifest.VisualDigest, manifest.RequestDigest, manifest.GateDigest, manifest.InputDigest, manifestJSON, "evidence_only", "visual_changed", false, "running", nil, nil, nil, nil, "happy-publication", "not_started", nil)
	require.NoError(t, err, "seed exact-current-input assessment")
	happyJob, err := json.Marshal(codeReviewRecheckJob{OrgID: org, AssessmentID: happyAssessment})
	require.NoError(t, err, "encode exact-current-input assessment")
	err = handler(ctx, "run_code_review_recheck", happyJob)
	require.Error(t, err, "supervisor should wait for exact second orchestrator turn")
	happyDispatch, err := stores.CodeReviewRechecks.Get(ctx, org, happyAssessment)
	require.NoError(t, err, "read second exact dispatch")
	happyLease := uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$2 WHERE org_id=$1 AND id=$3`, org, happyLease, happyDispatch.JobID)
	require.NoError(t, err, "lease exact second continuation")
	owned, err = stores.CodeReviewRechecks.Claim(ctx, org, happyAssessment, happyDispatch.JobID, happyLease, session, thread, happyDispatch.ExpectedTurn, happyDispatch.MessageID)
	require.NoError(t, err, "claim exact second turn")
	require.True(t, owned, "second continuation owns current turn")
	noEscalation := false
	requirements := []models.CodeReviewRequirementReassessment{{Key: "screenshot", Status: models.CodeReviewRequirementSatisfied, EvidenceCitations: []models.CodeReviewEvidenceCitation{{EvidenceID: "image-1"}}, Reason: "The current screenshot shows the changed UI"}}
	findings := []models.CodeReviewFindingReassessment{{FindingID: findingID, Status: models.CodeReviewFindingResolved, Reason: "Current passing browser test addresses the original coverage concern", EvidenceCitations: []models.CodeReviewEvidenceCitation{{EvidenceID: "test-log", Quote: "The browser test passes with the changed UI."}}}}
	response, err := json.Marshal(codeReviewRecheckResponse{BaselineAssessmentID: baseline, InputDigest: manifest.InputDigest, EscalateFullReview: &noEscalation, Requirements: &requirements, Findings: &findings})
	require.NoError(t, err, "encode exact evidence answer")
	baselineFindings, err := stores.CodeReviewAssessments.ListFindings(ctx, org, baseline)
	require.NoError(t, err, "read original finding rows by assessment")
	validated, err := validateCodeReviewRecheckResponse(codeReviewRecheckValidationInput{Raw: string(response), BaselineID: baseline, InputDigest: manifest.InputDigest, Requirements: []models.CodeReviewDescriptionRequirement{visualReq}, BaselineSynthesis: synthesis, BaselineFindings: baselineFindings, BaselineManifest: baselineManifest, CurrentManifest: manifest, VisualEvidence: visual})
	require.NoError(t, err, "exact response should pass strict validation")
	require.Empty(t, validated.EscalationReason, "current screenshot should not trigger full review")
	_, err = codeReviewDescriptionEvaluationFromSynthesis(capture.result.Policy.Config(), nil, validated.Synthesis, visual)
	require.NoError(t, err, "merged description evidence should remain valid")
	_, err = stores.CodeReviewRechecks.Complete(ctx, models.CodeReviewRecheckTurnCompletion{OrgID: org, AssessmentID: happyAssessment, SessionID: session, ThreadID: thread, JobID: happyDispatch.JobID, LockToken: happyLease, ExpectedTurn: happyDispatch.ExpectedTurn, SessionTurn: 2, Summary: string(response), Result: &models.SessionResult{}, ProviderSessionID: "test-provider", SnapshotKey: "test-snapshot-2"})
	require.NoError(t, err, "persist synthetic exact current assistant turn")
	publicationJobID, publicationLease := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token) VALUES($1,$2,'agent','run_code_review_recheck','{}','running',$3)`, publicationJobID, org, publicationLease)
	require.NoError(t, err, "lease separate publication supervisor job")
	publicationCtx := jobctx.WithJobID(jobctx.WithLockToken(ctx, publicationLease), publicationJobID)
	err = handler(publicationCtx, "run_code_review_recheck", happyJob)
	require.NoError(t, err, "exact current response should finalize through staged publication")
	approved, err := stores.CodeReviewAssessments.GetByID(ctx, org, happyAssessment)
	require.NoError(t, err, "read published evidence assessment")
	require.Equal(t, models.CodeReviewAssessmentCompleted, approved.Status, "exact response should complete assessment")
	require.Equal(t, models.CodeReviewDecisionApproved, *approved.Decision, "only validated current evidence may approve")
	require.Equal(t, 1, len(publisher.requests), "one formal publication should occur")
	priorApproved, err := stores.CodeReviews.HasApprovedByPullRequest(ctx, org, pr)
	require.NoError(t, err, "read current PR approval projection")
	require.True(t, priorApproved, "evidence-only approval must stop later automatic review spending")
	retainedAssessment := uuid.New()
	_, err = pool.Exec(ctx, assessmentSQL, retainedAssessment, org, repo, repoName, pr, metadata, session, policy, 10, baseline, manifest.InputVersion, manifest.CodeDigest, manifest.ContractDigest, manifest.IntentDigest, manifest.VisualDigest, manifest.RequestDigest, manifest.GateDigest, manifest.InputDigest, manifestJSON, "evidence_only", "visual_changed", false, "running", nil, nil, nil, nil, "retained-publication", "not_started", nil)
	require.NoError(t, err, "seed a separate assessment retaining the original high finding")
	retainedJob, err := json.Marshal(codeReviewRecheckJob{OrgID: org, AssessmentID: retainedAssessment})
	require.NoError(t, err, "encode retained-finding assessment")
	err = handler(ctx, "run_code_review_recheck", retainedJob)
	require.Error(t, err, "retained-finding assessment should await one exact continuation")
	retainedDispatch, err := stores.CodeReviewRechecks.Get(ctx, org, retainedAssessment)
	require.NoError(t, err, "read retained-finding dispatch")
	retainedLease := uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$2 WHERE org_id=$1 AND id=$3`, org, retainedLease, retainedDispatch.JobID)
	require.NoError(t, err, "lease retained-finding continuation")
	owned, err = stores.CodeReviewRechecks.Claim(ctx, org, retainedAssessment, retainedDispatch.JobID, retainedLease, session, thread, retainedDispatch.ExpectedTurn, retainedDispatch.MessageID)
	require.NoError(t, err, "claim retained-finding turn")
	require.True(t, owned, "retained-finding turn should have one lease owner")
	retainedFindings := []models.CodeReviewFindingReassessment{{FindingID: findingID, Status: models.CodeReviewFindingRetained, Reason: "The original high finding still needs review"}}
	retainedResponse, err := json.Marshal(codeReviewRecheckResponse{BaselineAssessmentID: baseline, InputDigest: manifest.InputDigest, EscalateFullReview: &noEscalation, Requirements: &requirements, Findings: &retainedFindings})
	require.NoError(t, err, "encode explicit retained blocker")
	_, err = stores.CodeReviewRechecks.Complete(ctx, models.CodeReviewRecheckTurnCompletion{OrgID: org, AssessmentID: retainedAssessment, SessionID: session, ThreadID: thread, JobID: retainedDispatch.JobID, LockToken: retainedLease, ExpectedTurn: retainedDispatch.ExpectedTurn, SessionTurn: 3, Summary: string(retainedResponse), Result: &models.SessionResult{}, ProviderSessionID: "test-provider", SnapshotKey: "test-snapshot-3"})
	require.NoError(t, err, "persist exact retained-finding assistant turn")
	retainedPublicationJobID, retainedPublicationLease := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token) VALUES($1,$2,'agent','run_code_review_recheck','{}','running',$3)`, retainedPublicationJobID, org, retainedPublicationLease)
	require.NoError(t, err, "lease retained-finding publication")
	err = handler(jobctx.WithJobID(jobctx.WithLockToken(ctx, retainedPublicationLease), retainedPublicationJobID), "run_code_review_recheck", retainedJob)
	require.NoError(t, err, "retained blocker should complete with a human-review decision")
	retainedOutcome, err := stores.CodeReviewAssessments.GetByID(ctx, org, retainedAssessment)
	require.NoError(t, err, "read retained blocker outcome")
	require.Equal(t, models.CodeReviewDecisionNeedsHumanReview, *retainedOutcome.Decision, "original high finding must remain a blocker when retained")
	staleAssessment := uuid.New()
	_, err = pool.Exec(ctx, assessmentSQL, staleAssessment, org, repo, repoName, pr, metadata, session, policy, 5, baseline, manifest.InputVersion, manifest.CodeDigest, manifest.ContractDigest, manifest.IntentDigest, manifest.VisualDigest, manifest.RequestDigest, manifest.GateDigest, manifest.InputDigest, manifestJSON, "evidence_only", "visual_changed", false, "running", nil, nil, nil, nil, "stale-publication", "not_started", nil)
	require.NoError(t, err, "seed evidence assessment whose inputs change at publication")
	staleJob, err := json.Marshal(codeReviewRecheckJob{OrgID: org, AssessmentID: staleAssessment})
	require.NoError(t, err, "encode publication-race assessment")
	err = handler(ctx, "run_code_review_recheck", staleJob)
	require.Error(t, err, "third orchestrator turn should be durably dispatched")
	staleDispatch, err := stores.CodeReviewRechecks.Get(ctx, org, staleAssessment)
	require.NoError(t, err, "read publication-race dispatch")
	staleLease := uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$2 WHERE org_id=$1 AND id=$3`, org, staleLease, staleDispatch.JobID)
	require.NoError(t, err, "lease publication-race continuation")
	owned, err = stores.CodeReviewRechecks.Claim(ctx, org, staleAssessment, staleDispatch.JobID, staleLease, session, thread, staleDispatch.ExpectedTurn, staleDispatch.MessageID)
	require.NoError(t, err, "claim publication-race turn")
	require.True(t, owned, "publication-race continuation should own exact turn")
	_, err = stores.CodeReviewRechecks.Complete(ctx, models.CodeReviewRecheckTurnCompletion{OrgID: org, AssessmentID: staleAssessment, SessionID: session, ThreadID: thread, JobID: staleDispatch.JobID, LockToken: staleLease, ExpectedTurn: staleDispatch.ExpectedTurn, SessionTurn: 3, Summary: string(response), Result: &models.SessionResult{}, ProviderSessionID: "test-provider", SnapshotKey: "test-snapshot-3"})
	require.NoError(t, err, "persist exact turn before publication-only input change")
	stalePublicationJobID, stalePublicationLease := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token) VALUES($1,$2,'agent','run_code_review_recheck','{}','running',$3)`, stalePublicationJobID, org, stalePublicationLease)
	require.NoError(t, err, "lease publication-race supervisor")
	lifecycle := &fakeRecheckLifecycle{}
	services.CodeReviewLifecycle = lifecycle
	capture.changedManifest = &changedManifest
	capture.flipOnCall = capture.calls + 3 // first two captures build the result; third occurs under the publication lock.
	err = handler(jobctx.WithJobID(jobctx.WithLockToken(ctx, stalePublicationLease), stalePublicationJobID), "run_code_review_recheck", staleJob)
	require.NoError(t, err, "changed input at the locked publication boundary should settle and route full review")
	staleOutcome, err := stores.CodeReviewAssessments.GetByID(ctx, org, staleAssessment)
	require.NoError(t, err, "read settled publication-race assessment")
	require.Equal(t, models.CodeReviewAssessmentSuperseded, staleOutcome.Status, "unsent staged approval must be superseded when input changes under publication lock")
	require.Equal(t, []uuid.UUID{staleAssessment}, lifecycle.fallbacks, "one full-review fallback should be requested")
	require.Equal(t, 2, len(publisher.requests), "changed input must not send another formal review")
	capture.changedManifest = nil
	services.CodeReviewLifecycle = nil
	incompatibleAssessment := uuid.New()
	_, err = pool.Exec(ctx, assessmentSQL, incompatibleAssessment, org, repo, repoName, pr, metadata, session, policy, 6, baseline, manifest.InputVersion, manifest.CodeDigest, manifest.ContractDigest, manifest.IntentDigest, manifest.VisualDigest, manifest.RequestDigest, manifest.GateDigest, manifest.InputDigest, manifestJSON, "evidence_only", "visual_changed", false, "running", nil, nil, nil, nil, "incompatible-publication", "not_started", nil)
	require.NoError(t, err, "seed recheck with source inputs that cannot establish reuse")
	incompatibleJob, err := json.Marshal(codeReviewRecheckJob{OrgID: org, AssessmentID: incompatibleAssessment})
	require.NoError(t, err, "encode incompatible assessment job")
	capture.captureErr = codereviewsvc.ErrAssessmentReuseUnavailable
	services.CodeReviewLifecycle = lifecycle
	err = handler(ctx, "run_code_review_recheck", incompatibleJob)
	require.NoError(t, err, "deterministic reuse incompatibility should settle and request a full review")
	incompatibleOutcome, err := stores.CodeReviewAssessments.GetByID(ctx, org, incompatibleAssessment)
	require.NoError(t, err, "read incompatible recheck assessment")
	require.Equal(t, models.CodeReviewAssessmentFailed, incompatibleOutcome.Status, "reuse incompatibility must fail the bounded recheck")
	require.Equal(t, "full_review:assessment inputs cannot establish reusable coverage", *incompatibleOutcome.FailureDetail, "fallback reason should survive retries")
	require.Equal(t, []uuid.UUID{staleAssessment, incompatibleAssessment}, lifecycle.fallbacks, "incompatibility should queue one additional full review")
	capture.captureErr = nil
	services.CodeReviewLifecycle = nil
	escalatedAssessment := uuid.New()
	_, err = pool.Exec(ctx, assessmentSQL, escalatedAssessment, org, repo, repoName, pr, metadata, session, policy, 7, baseline, manifest.InputVersion, manifest.CodeDigest, manifest.ContractDigest, manifest.IntentDigest, manifest.VisualDigest, manifest.RequestDigest, manifest.GateDigest, manifest.InputDigest, manifestJSON, "evidence_only", "visual_changed", false, "running", nil, nil, nil, nil, "escalated-publication", "not_started", nil)
	require.NoError(t, err, "seed evidence assessment with a new model concern")
	escalatedJob, err := json.Marshal(codeReviewRecheckJob{OrgID: org, AssessmentID: escalatedAssessment})
	require.NoError(t, err, "encode escalated assessment job")
	err = handler(ctx, "run_code_review_recheck", escalatedJob)
	require.Error(t, err, "new concern assessment should await exact orchestrator turn")
	escalatedDispatch, err := stores.CodeReviewRechecks.Get(ctx, org, escalatedAssessment)
	require.NoError(t, err, "read escalated assessment dispatch")
	escalatedLease := uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$2 WHERE org_id=$1 AND id=$3`, org, escalatedLease, escalatedDispatch.JobID)
	require.NoError(t, err, "lease escalation continuation")
	owned, err = stores.CodeReviewRechecks.Claim(ctx, org, escalatedAssessment, escalatedDispatch.JobID, escalatedLease, session, thread, escalatedDispatch.ExpectedTurn, escalatedDispatch.MessageID)
	require.NoError(t, err, "claim exact escalation turn")
	require.True(t, owned, "escalation continuation should own exact turn")
	escalationConcern := "Screenshot reveals a broken save action"
	yesEscalate := true
	escalatedResponse, err := json.Marshal(codeReviewRecheckResponse{BaselineAssessmentID: baseline, InputDigest: manifest.InputDigest, EscalateFullReview: &yesEscalate, EscalationReason: escalationConcern})
	require.NoError(t, err, "encode bounded escalation with concrete concern")
	_, err = stores.CodeReviewRechecks.Complete(ctx, models.CodeReviewRecheckTurnCompletion{OrgID: org, AssessmentID: escalatedAssessment, SessionID: session, ThreadID: thread, JobID: escalatedDispatch.JobID, LockToken: escalatedLease, ExpectedTurn: escalatedDispatch.ExpectedTurn, SessionTurn: 4, Summary: string(escalatedResponse), Result: &models.SessionResult{}, ProviderSessionID: "test-provider", SnapshotKey: "test-snapshot-4"})
	require.NoError(t, err, "persist exact escalation turn")
	services.CodeReviewLifecycle = lifecycle
	err = handler(ctx, "run_code_review_recheck", escalatedJob)
	require.NoError(t, err, "concrete new concern should route one full review")
	escalatedOutcome, err := stores.CodeReviewAssessments.GetByID(ctx, org, escalatedAssessment)
	require.NoError(t, err, "read durable escalation reason")
	require.Equal(t, "full_review:new evidence requires full review: "+escalationConcern, *escalatedOutcome.FailureDetail, "assessment must retain model concern for full-review fallback")
	require.Equal(t, []uuid.UUID{staleAssessment, incompatibleAssessment, escalatedAssessment}, lifecycle.fallbacks, "escalation should request exactly one additional full review")
	services.CodeReviewLifecycle = nil
	replacementSession, replacementMetadata, replacementAssessment := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','idle')`, replacementSession, org)
	require.NoError(t, err, "seed separate force-fresh review conversation")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($1,$2,$3,$4,$5,$6,'base','head','slash_command','completed','replacement-output')`, replacementMetadata, org, replacementSession, repo, pr, policy)
	require.NoError(t, err, "seed completed replacement metadata")
	_, err = pool.Exec(ctx, assessmentSQL, replacementAssessment, org, repo, repoName, pr, replacementMetadata, replacementSession, policy, 8, nil, manifest.InputVersion, manifest.CodeDigest, manifest.ContractDigest, manifest.IntentDigest, manifest.VisualDigest, manifest.RequestDigest, manifest.GateDigest, manifest.InputDigest, manifestJSON, "full", "force_fresh", true, "completed", "executed", "approved", true, json.RawMessage(`{}`), "replacement-publication", "not_required", time.Now().UTC())
	require.NoError(t, err, "seed newer completed full assessment")
	retireTx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin old conversation retirement")
	released, err := stores.CodeReviewRechecks.RetireOwnerForCompletedReplacement(ctx, retireTx, org, session, pr, replacementAssessment)
	require.NoError(t, err, "retire drained old review conversation")
	require.True(t, released, "new completed full assessment should release drained old owner")
	require.NoError(t, retireTx.Commit(ctx), "commit old conversation retirement")
	completedDispatch, err := stores.CodeReviewRechecks.Get(ctx, org, happyAssessment)
	require.NoError(t, err, "completed receipt should remain readable after retirement")
	require.Equal(t, models.CodeReviewRecheckDispatchCompleted, completedDispatch.Status, "retirement should preserve completed dispatch receipt")
	var retiredOwner *uuid.UUID
	err = pool.QueryRow(ctx, `SELECT code_review_owner_pr_id FROM sessions WHERE org_id=$1 AND id=$2`, org, session).Scan(&retiredOwner)
	require.NoError(t, err, "read retired session owner")
	require.Nil(t, retiredOwner, "completed replacement should release the drained old session")
	uncertainAssessment := uuid.New()
	_, err = pool.Exec(ctx, assessmentSQL, uncertainAssessment, org, repo, repoName, pr, metadata, session, policy, 9, baseline, manifest.InputVersion, manifest.CodeDigest, manifest.ContractDigest, manifest.IntentDigest, manifest.VisualDigest, manifest.RequestDigest, manifest.GateDigest, manifest.InputDigest, manifestJSON, "evidence_only", "visual_changed", false, "running", nil, nil, nil, nil, "uncertain-publication", "not_started", nil)
	require.NoError(t, err, "seed assessment with a prior unresolved external send")
	uncertainCompletion := models.CodeReviewAssessmentCompletion{ResultOrigin: models.CodeReviewResultEvidenceOnly, CoverageComplete: true, Decision: models.CodeReviewDecisionNeedsHumanReview, Acceptable: false, StructuredOutcome: json.RawMessage(`{"synthesis":{}}`), RenderedBody: "staged review body"}
	require.NoError(t, stores.CodeReviewAssessments.StageOutcome(ctx, org, uncertainAssessment, 9, manifest.InputDigest, uncertainCompletion), "stage immutable uncertain publication")
	require.NoError(t, stores.CodeReviewAssessments.ReservePublication(ctx, org, uncertainAssessment, 9, manifest.InputDigest, "head"), "reserve uncertain publication")
	require.NoError(t, stores.CodeReviewAssessments.MarkPublicationAttemptUncertain(ctx, org, uncertainAssessment, 9, manifest.InputDigest), "record durable send intent")
	uncertainJobID, uncertainLease := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token) VALUES($1,$2,'agent','run_code_review_recheck','{}','running',$3)`, uncertainJobID, org, uncertainLease)
	require.NoError(t, err, "lease uncertain publication reconciliation")
	uncertainJob, err := json.Marshal(codeReviewRecheckJob{OrgID: org, AssessmentID: uncertainAssessment})
	require.NoError(t, err, "encode uncertain assessment job")
	capture.changedManifest = &changedManifest
	capture.flipOnCall = capture.calls + 1
	err = handler(jobctx.WithJobID(jobctx.WithLockToken(ctx, uncertainLease), uncertainJobID), "run_code_review_recheck", uncertainJob)
	require.Error(t, err, "unresolved uncertain publication should request another reconciliation")
	uncertainOutcome, err := stores.CodeReviewAssessments.GetByID(ctx, org, uncertainAssessment)
	require.NoError(t, err, "read uncertain assessment after changed input")
	require.Equal(t, models.CodeReviewAssessmentPublishing, uncertainOutcome.Status, "uncertain external send cannot be declared unsent")
	require.Equal(t, models.CodeReviewPublicationUncertain, uncertainOutcome.PublicationState, "uncertain receipt must remain unresolved")
	require.Equal(t, "review marker not found after input change; pending reconciliation", *uncertainOutcome.FailureDetail, "persist actionable no-marker detail for operators")
	require.Equal(t, 1, len(publisher.reconciliations), "changed input should only read the GitHub review marker")
	require.Equal(t, 2, len(publisher.requests), "changed input must not retry an uncertain write")
}
