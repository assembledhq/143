package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

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

type strandedReviewFixture struct {
	job                      runCodeReviewPayload
	assessment, jobID, token uuid.UUID
	stores                   *Stores
	manifest                 codereviewsvc.ReviewInputManifest
}

func seedStrandedReview(t *testing.T, pool *pgxpool.Pool, parent string) strandedReviewFixture {
	t.Helper()
	ctx := context.Background()
	org, integration, repo, pr, policy, session, metadata, assessment := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	jobID, token := uuid.New(), uuid.New()
	key := "saved-review:" + session.String()
	job := runCodeReviewPayload{OrgID: org, SessionID: session, MetadataID: metadata, RepositoryID: repo, PullRequestID: pr, PolicyID: policy, PolicyVersion: 1, HeadSHA: "head", OutputKey: key, FromFork: true, TriggeringDisputeID: new(uuid.New())}
	payload, err := json.Marshal(job)
	require.NoError(t, err, "encode original review provenance")
	manifest := codereviewsvc.ReviewInputManifest{InputVersion: codereviewsvc.ReviewInputManifestVersion, ReuseEligible: true, InputDigest: "all", CodeDigest: "code", ContractDigest: "contract", IntentDigest: "intent", VisualDigest: "visual", RequestDigest: "request", GateDigest: "gates", Code: codereviewsvc.ReviewCodeInput{HeadSHA: "head", BaseSHA: "base", BaseRef: "main"}}
	manifestJSON, err := json.Marshal(manifest)
	require.NoError(t, err, "encode captured inputs")
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations(id,name) VALUES($1,'Saved Review Test')`, []any{org}},
		{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, org}},
		{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0) & 9223372036854775807),'test/saved-review','https://example.invalid/repo.git',1)`, []any{repo, org, integration}},
		{`INSERT INTO sessions(id,org_id,repository_id,origin,status,failure_explanation) VALUES($1,$2,$3,'code_review',$4,'original execution timeout')`, []any{session, org, repo, parent}},
		{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,9,'https://example.invalid/pr/9','test/saved-review','Saved review','head','base')`, []any{pr, org}},
		{`INSERT INTO code_review_policies(id,org_id,repository_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,NULL,1,'approve_acceptable','{}','{}','{"reviewers":["claude_code"],"reviewer_count":1}','','','{}')`, []any{policy, org}},
		{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,phase,review_output_key) VALUES($1,$2,$3,$4,$5,$6,'base','head','slash_command','running','syncing_github',$7)`, []any{metadata, org, session, repo, pr, policy, key}},
		{`INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,status,publication_key) VALUES($1,$2,$3,'test/saved-review',$4,$5,$6,$7,1,'base','main','head',$8,'code','contract','intent','visual','request','gates','all',$9,'full','initial_full','running',$10)`, []any{assessment, org, repo, pr, metadata, session, policy, manifest.InputVersion, manifestJSON, key}},
		{`INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id,head_sha,base_sha,base_ref,active_session_id,state) VALUES($1,$2,$3,'head','base','main',$4,'running')`, []any{org, repo, pr, session}},
		{`INSERT INTO jobs(id,org_id,queue,job_type,payload,dedupe_key,status,lock_token) VALUES($1,$2,'agent','run_code_review',$3,$4,'running',$5)`, []any{jobID, org, payload, "code_review:" + key, token}},
	} {
		_, err := pool.Exec(ctx, row.sql, row.args...)
		require.NoError(t, err, "seed stranded controller state")
	}
	return strandedReviewFixture{job: job, assessment: assessment, jobID: jobID, token: token, manifest: manifest, stores: &Stores{CodeReviewWorkspaces: db.NewCodeReviewWorkspaceStore(pool), CodeReviews: db.NewCodeReviewStore(pool), CodeReviewAssessments: db.NewCodeReviewAssessmentStore(pool), Sessions: db.NewSessionStore(pool), Repositories: db.NewRepositoryStore(pool), PullRequests: db.NewPullRequestStore(pool), Jobs: db.NewJobStore(pool), SessionThreads: db.NewSessionThreadStore(pool), SessionMessages: db.NewSessionMessageStore(pool), SessionLogs: db.NewSessionLogStore(pool), ThreadSendTx: pool}}
}

func (f strandedReviewFixture) ctx() context.Context {
	return jobctx.WithJobID(jobctx.WithLockToken(context.Background(), f.token), f.jobID)
}

func seedSavedReviewResults(t *testing.T, pool *pgxpool.Pool, f strandedReviewFixture) {
	t.Helper()
	reviewer, err := json.Marshal(codeReviewReviewerStructuredResult{ReviewerKey: codeReviewReviewerKey(0, models.AgentTypeClaudeCode), ReadOnly: true})
	require.NoError(t, err, "encode completed reviewer")
	synthesis, err := json.Marshal(codeReviewOrchestratorStructuredResult{SynthesisValidated: true, Synthesis: codeReviewOrchestratorSynthesis{Summary: "Saved synthesis", ReviewSummary: "Saved summary", DescriptionAssessments: []codeReviewDescriptionAssessment{}, Findings: []codeReviewOrchestratorFinding{}, HumanReviewReasons: []codeReviewOrchestratorHumanReviewReason{}}})
	require.NoError(t, err, "encode validated synthesis")
	_, err = pool.Exec(context.Background(), `INSERT INTO code_review_agent_results(org_id,session_id,agent_provider,role,status,raw_output,structured_result) VALUES($1,$2,'claude_code','reviewer','completed','original reviewer output',$3),($1,$2,'claude_code','orchestrator','completed','original synthesis output',$4)`, f.job.OrgID, f.job.SessionID, reviewer, synthesis)
	require.NoError(t, err, "save validated reviewer and synthesis results")
}

type legacyReceiptPublisher struct {
	inlineRecoveryPublisher
	found     bool
	result    codereviewsvc.SubmitReviewResult
	lookupErr error
	lookups   []codereviewsvc.SubmitReviewRequest
}

func (p *legacyReceiptPublisher) ReconcileAssessmentPublication(_ context.Context, r codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, bool, error) {
	p.lookups = append(p.lookups, r)
	return p.result, p.found, p.lookupErr
}

//nolint:paralleltest // Migrate once before parallel cases to stay within PostgreSQL's lock budget.
func TestStrandedLegacyReceiptRecoveryPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for recovery proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name                                                                           string
		missing, wrongID, wrongHead, lookupError, noLegacy, updatedSummary, noApproval bool
	}{
		{name: "expired receipt completes without resending"}, {name: "missing marker remains fenced", missing: true}, {name: "wrong legacy ID remains fenced", wrongID: true}, {name: "wrong commit remains fenced", wrongHead: true}, {name: "lookup error retains fence", lookupError: true}, {name: "no legacy receipt remains paused", noLegacy: true}, {name: "updated summary uses verified separate approval", updatedSummary: true}, {name: "missing formal approval remains paused", noApproval: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "completed")
			ctx := f.ctx()
			_, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='publishing',publication_state='uncertain',result_origin='executed',decision='approved',acceptable=true,risk_reason_details='[]',structured_outcome='{}',rendered_body='immutable saved review',created_at=now()-interval '3 hours',failure_detail=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, db.CodeReviewPublicationOperatorRequired)
			require.NoError(t, err, "seed expired staged publication")
			if !tt.noLegacy {
				_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET github_review_id=77 WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "retain legacy review receipt")
			}
			pub := &legacyReceiptPublisher{found: !tt.missing, result: codereviewsvc.SubmitReviewResult{ID: 77, URL: "https://example.invalid/review/77", SubmittedCommitSHA: "head", ReviewState: "APPROVED", FormalApprovalID: new(int64(77))}}
			if tt.wrongID {
				pub.result.ID = 88
			}
			if tt.wrongHead {
				pub.result.SubmittedCommitSHA = "different-head"
			}
			if tt.updatedSummary {
				f.job.ExistingGitHubReviewID = new(int64(77))
				pub.result.SubmittedCommitSHA = "original-summary-head"
			}
			if tt.noApproval {
				pub.result.FormalApprovalID = nil
			}
			if tt.lookupError {
				pub.lookupErr = errors.New("temporary lookup failure")
			}
			recovered, err := recoverStagedFullAssessment(ctx, f.stores, &Services{CodeReviews: pub}, f.job)
			require.True(t, recovered, "staged result must use recovery")
			success := !tt.missing && !tt.wrongID && !tt.wrongHead && !tt.lookupError && !tt.noLegacy && !tt.noApproval
			if success {
				require.NoError(t, err, "verified receipt should complete")
			} else if tt.lookupError {
				require.ErrorIs(t, err, pub.lookupErr, "lookup failure should retry the same bounded controller")
			} else {
				require.ErrorIs(t, err, errCodeReviewPublicationPaused, "unverified publication must remain paused")
			}
			require.Empty(t, pub.requests, "legacy receipt recovery must never send a review")
			a, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read recovered assessment")
			if success {
				require.Equal(t, models.CodeReviewAssessmentCompleted, a.Status, "verified saved review completes")
				require.Equal(t, models.CodeReviewPublicationConfirmed, a.PublicationState, "receipt confirmation should be durable")
			} else {
				require.Equal(t, models.CodeReviewAssessmentPublishing, a.Status, "unverified send retains active assessment")
				require.Nil(t, a.PublicationReceipt, "unverified send cannot invent receipt")
			}
			st, err := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read legacy session reservation")
			if success {
				require.Nil(t, st.ActiveSessionID, "verified completion releases legacy reservation")
				require.Equal(t, &f.assessment, st.CurrentAssessmentID, "confirmed outcome becomes current")
			} else {
				require.Equal(t, &f.job.SessionID, st.ActiveSessionID, "unverified send keeps reservation")
			}
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
			require.NoError(t, err, "finish original controller")
			require.NoError(t, db.NewCodeReviewScheduleStore(pool).RepairMissingWakes(ctx), "run repair after completed lookup")
			var wakes int
			err = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND status='pending'`, f.job.OrgID).Scan(&wakes)
			require.NoError(t, err, "count controller repair wakes")
			require.Equal(t, 0, wakes, "sweeper must not repeat a completed or unverified legacy receipt lookup forever")
		})
	}
}

//nolint:paralleltest // Full-schema migration setup is serial; isolated tenants run in parallel afterward.
func TestEndedFullReviewRecoveryPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for recovery proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name, parent, change       string
		saved, wantResume, wantErr bool
	}{
		{name: "completed parent retains validated saved work", parent: "completed", saved: true, wantResume: true},
		{name: "failed parent settles original timeout", parent: "failed"},
		{name: "incomplete completed parent is failed", parent: "completed"},
		{name: "running result prevents saved continuation", parent: "completed", saved: true, change: `UPDATE code_review_agent_results SET status='running' WHERE org_id=$1 AND role='orchestrator'`},
		{name: "unvalidated completed synthesis cannot resume", parent: "completed", saved: true, change: `UPDATE code_review_agent_results SET structured_result='{}' WHERE org_id=$1 AND role='orchestrator'`},
		{name: "failed parent preserves running fallback evidence", parent: "failed", saved: true, change: `UPDATE code_review_agent_results SET status='running' WHERE org_id=$1 AND role='orchestrator'`},
		{name: "live runtime remains blocker", parent: "failed", change: `WITH t AS (INSERT INTO session_threads(org_id,session_id,agent_type,status,label) VALUES($1,$2,'claude_code','idle','Saved reviewer') RETURNING id) INSERT INTO thread_runtimes(org_id,session_id,thread_id,agent_type,status,owner_node_id,lease_token,lease_expires_at) SELECT $1,$2,t.id,'claude_code','live','test-node',gen_random_uuid(),now()-interval '1 hour' FROM t`, wantErr: true},
		{name: "queued agent job remains blocker", parent: "failed", change: `INSERT INTO jobs(org_id,queue,job_type,payload) VALUES($1,'agent','continue_session',jsonb_build_object('session_id',$2::text))`, wantErr: true},
		{name: "new parent turn fails closed", parent: "failed", change: `UPDATE sessions SET current_turn=2 WHERE org_id=$1 AND id=$2`, wantErr: true},
		{name: "conflicting scheduler session remains untouched", parent: "failed", change: `UPDATE code_review_pr_state SET active_session_id=NULL WHERE org_id=$1`, wantErr: true},
		{name: "publication receipt protects unsent result", parent: "failed", change: `UPDATE code_review_revision_assessments SET github_review_id=99 WHERE org_id=$1`, wantErr: true},
		{name: "missing job lease fails closed", parent: "failed", change: `UPDATE jobs SET lock_token=NULL WHERE org_id=$1`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, tt.parent)
			ctx := f.ctx()
			if tt.saved {
				seedSavedReviewResults(t, pool, f)
			}
			before, err := f.stores.CodeReviews.ListAgentResults(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "snapshot saved results")
			if tt.change != "" { // Bind only referenced parameters; pgx rejects extra arguments.
				args := []any{f.job.OrgID}
				if strings.Contains(tt.change, "$2") {
					args = append(args, f.job.SessionID)
				}
				_, err = pool.Exec(ctx, tt.change, args...)
				require.NoError(t, err, "apply ownership regression")
				before, err = f.stores.CodeReviews.ListAgentResults(ctx, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "snapshot changed results")
			}
			policy, err := f.stores.CodeReviews.GetPolicyByID(ctx, f.job.OrgID, f.job.PolicyID)
			require.NoError(t, err, "load original policy")
			in := db.EndedFullReviewParams{RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID, SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, PolicyID: f.job.PolicyID, JobID: f.jobID, JobLockToken: f.token, HeadSHA: f.job.HeadSHA, OutputKey: f.job.OutputKey, SessionStatus: models.SessionStatus(tt.parent), SessionTurn: 0}
			resumed, err := db.NewCodeReviewScheduleStore(pool).ReconcileEndedFullReview(ctx, f.job.OrgID, in, func(results []models.CodeReviewAgentResult) bool {
				return codeReviewSavedResultsComplete(policy.Config(), results)
			})
			if tt.wantErr {
				require.Error(t, err, "live ownership or changed identity must fence recovery")
			} else {
				require.NoError(t, err, "drained ended review should reconcile")
			}
			require.Equal(t, tt.wantResume, resumed, "only validated complete saved work may continue")
			after, err := f.stores.CodeReviews.ListAgentResults(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read preserved reviewer results")
			require.Equal(t, before, after, "recovery must preserve all saved evidence and failures")
			m, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read recovered metadata")
			a, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read recovered assessment")
			if tt.wantErr || tt.wantResume {
				require.Equal(t, models.CodeReviewSessionStatusRunning, m.Status, "fenced or resumable metadata must remain active")
				require.Equal(t, models.CodeReviewAssessmentRunning, a.Status, "fenced or resumable assessment must remain active")
			} else {
				require.Equal(t, models.CodeReviewSessionStatusFailed, m.Status, "incomplete ended review must fail durably")
				require.Equal(t, "original execution timeout", *m.FailureReason, "preserve original parent diagnostic")
				require.Equal(t, models.CodeReviewAssessmentFailed, a.Status, "assessment must retire atomically")
				require.Equal(t, "original execution timeout", *a.FailureDetail, "assessment should explain original failure")
				st, err := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.job.OrgID, f.job.PullRequestID)
				require.NoError(t, err, "load settled scheduler")
				require.Nil(t, st.ActiveSessionID, "failed review should release its exact legacy session reservation")
			}
		})
	}
}

//nolint:paralleltest // The migration chain uses the shared PostgreSQL lock budget.
func TestSavedCompletedReviewHandlerDoesNotDispatchPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for saved controller proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	f := seedStrandedReview(t, pool, "completed")
	seedSavedReviewResults(t, pool, f)
	policy, err := f.stores.CodeReviews.GetPolicyByID(ctx, f.job.OrgID, f.job.PolicyID)
	require.NoError(t, err, "load captured review policy")
	capture := &fixedRecheckCapture{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: f.manifest, Policy: policy}}
	publisher := &inlineRecoveryPublisher{}
	services := &Services{CodeReviewVisualEvidence: &codeReviewVisualEvidenceProviderStub{snapshot: models.CodeReviewVisualEvidenceSnapshot{Version: 1, RepositoryID: f.job.RepositoryID, PullRequestNumber: 9, HeadSHA: "head", Complete: true}}, CodeReviewWorkspacePreparationEnabled: true, CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviews: publisher}
	before, err := f.stores.CodeReviews.ListAgentResults(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "snapshot completed work")
	payload, err := json.Marshal(f.job)
	require.NoError(t, err, "encode original controller")
	err = newRunCodeReviewHandler(f.stores, services, zerolog.Nop())(f.ctx(), "run_code_review", payload)
	require.NoError(t, err, "saved results should complete through normal freshness and publication")
	a, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "load final assessment")
	require.Equal(t, models.CodeReviewAssessmentCompleted, a.Status, "saved controller must finish assessment")
	after, err := f.stores.CodeReviews.ListAgentResults(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read completed evidence")
	require.Equal(t, before, after, "controller should link saved evidence without rerunning agents")
	require.Greater(t, capture.calls, 0, "saved work must still pass fresh input validation")
	var dispatches int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type IN ('run_agent','continue_session','fork_session_thread','prepare_code_review_workspace')`, f.job.OrgID).Scan(&dispatches)
	require.NoError(t, err, "read dispatch count")
	require.Equal(t, 0, dispatches, "saved recovery must bypass every workspace and model dispatch")
}

//nolint:paralleltest // Full schema migrations use the shared server lock budget.
func TestEndedReviewGuardSurvivesDeadLetterPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for guard exhaustion proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	f := seedStrandedReview(t, pool, "failed")
	ctx := jobctx.WithDeadLetterHooks(f.ctx())
	_, err := pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload) VALUES($1,'agent','continue_session',jsonb_build_object('session_id',$2::text))`, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "seed still-active execution owner")
	payload, err := json.Marshal(f.job)
	require.NoError(t, err, "encode original controller")
	handlerErr := newRunCodeReviewHandler(f.stores, &Services{}, zerolog.Nop())(ctx, "run_code_review", payload)
	require.ErrorIs(t, handlerErr, errCodeReviewEndedParentRecovery, "ownership rejection must retain its non-destructive guard identity")
	jobctx.RunDeadLetterHooks(ctx, handlerErr)
	m, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read metadata after exhausted guard")
	require.Equal(t, models.CodeReviewSessionStatusRunning, m.Status, "exhausted guard must not fail metadata behind live execution")
	a, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read guarded assessment")
	require.Equal(t, models.CodeReviewAssessmentRunning, a.Status, "exhausted guard must not settle assessment")
	parent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read parent diagnostic")
	require.Equal(t, "original execution timeout", *parent.FailureExplanation, "exhausted guard must not rewrite parent failure")
}

//nolint:paralleltest // A schema-local trigger injects the transactional enqueue failure.
func TestEndedReviewFailureRollsBackWithStatusEnqueuePostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for atomic failure proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	f := seedStrandedReview(t, pool, "failed")
	ctx := f.ctx()
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_terminal_status() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.job_type='sync_code_review_status_comment' THEN RAISE EXCEPTION 'injected status enqueue failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_terminal_status BEFORE INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION reject_terminal_status()`)
	require.NoError(t, err, "inject status enqueue failure")
	_, _, err = recoverEndedCodeReviewParent(ctx, f.stores, f.job, models.CodeReviewPolicyConfig{})
	require.ErrorContains(t, err, "injected status enqueue failure", "failed durable enqueue should abort failure settlement")
	m, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read metadata after rollback")
	require.Equal(t, models.CodeReviewSessionStatusRunning, m.Status, "metadata failure must roll back with missing status wake")
	a, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read assessment after rollback")
	require.Equal(t, models.CodeReviewAssessmentRunning, a.Status, "assessment failure must roll back atomically")
	st, err := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.job.OrgID, f.job.PullRequestID)
	require.NoError(t, err, "read scheduler after rollback")
	require.Equal(t, &f.job.SessionID, st.ActiveSessionID, "reservation must remain until terminal status is durable")
}

//nolint:paralleltest // Full migration chain setup is serialized.
func TestEndedReviewWithoutAssessmentRetainsLegacyPathPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for legacy compatibility proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	f := seedStrandedReview(t, pool, "completed")
	ctx := f.ctx()
	_, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
	require.NoError(t, err, "seed legacy review without assessment")
	resumed, settled, err := recoverEndedCodeReviewParent(ctx, f.stores, f.job, models.CodeReviewPolicyConfig{})
	require.NoError(t, err, "legacy review should not require an assessment")
	require.Equal(t, []bool{false, false}, []bool{resumed, settled}, "legacy path should remain responsible for its work")
	f.stores.CodeReviewAssessments = nil
	resumed, settled, err = recoverEndedCodeReviewParent(ctx, f.stores, f.job, models.CodeReviewPolicyConfig{})
	require.NoError(t, err, "disabled assessments should retain legacy path")
	require.Equal(t, []bool{false, false}, []bool{resumed, settled}, "disabled assessments should not enter full recovery")
}

//nolint:paralleltest // Full schema migrations run before tenant-isolated parallel cases.
func TestStrandedFullControllerSweepPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for bounded sweep proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name, parent string
		receipt      bool
	}{{name: "completed saved work", parent: "completed"}, {name: "failed execution", parent: "failed"}, {name: "legacy receipt attempt is bounded before callback", parent: "completed", receipt: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, tt.parent)
			ctx := f.ctx()
			original, err := json.Marshal(f.job)
			require.NoError(t, err, "encode expected original payload")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
			require.NoError(t, err, "end original controller")
			if tt.receipt {
				_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='publishing',publication_state='uncertain',result_origin='executed',decision='approved',acceptable=true,risk_reason_details='[]',structured_outcome='{}',rendered_body='immutable review',created_at=now()-interval '3 hours',failure_detail=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, db.CodeReviewPublicationOperatorRequired)
				require.NoError(t, err, "seed expired known legacy publication")
				_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET github_review_id=77 WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "save legacy receipt")
				_, err = pool.Exec(ctx, `UPDATE repositories SET installation_id=0 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.RepositoryID)
				require.NoError(t, err, "force a failure before the publication callback")
			}
			sweep := db.NewCodeReviewScheduleStore(pool)
			require.NoError(t, sweep.RepairMissingWakes(ctx), "restore ended original controller")
			require.NoError(t, sweep.RepairMissingWakes(ctx), "sweep should be idempotent")
			var payload json.RawMessage
			var repairedID uuid.UUID
			err = pool.QueryRow(ctx, `SELECT id,payload FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND status='pending'`, f.job.OrgID).Scan(&repairedID, &payload)
			require.NoError(t, err, "read repaired controller")
			require.JSONEq(t, string(original), string(payload), "sweep must preserve fork, dispute, policy and original publication provenance")
			if tt.receipt {
				_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, repairedID, f.token)
				require.NoError(t, err, "claim bounded repair job")
				pub := &legacyReceiptPublisher{}
				_, err = recoverStagedFullAssessment(jobctx.WithJobID(ctx, repairedID), f.stores, &Services{CodeReviews: pub}, f.job)
				require.ErrorContains(t, err, "no GitHub installation id", "failed dependency should occur before callback")
				require.Empty(t, pub.lookups, "pre-callback failure should not reach network reconciliation")
				_, err = pool.Exec(ctx, `UPDATE jobs SET status='dead_letter' WHERE org_id=$1 AND id=$2`, f.job.OrgID, repairedID)
				require.NoError(t, err, "exhaust bounded repair")
				require.NoError(t, sweep.RepairMissingWakes(ctx), "repeat sweep after pre-callback exhaustion")
				var count int
				err = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND status='pending'`, f.job.OrgID).Scan(&count)
				require.NoError(t, err, "count repeated repair jobs")
				require.Equal(t, 0, count, "atomic repair marker must prevent unbounded recreation")
			}
		})
	}
}

type parentEndAtWorkspaceCheckpointDB struct {
	db.DBTX
	beforeGeneration func()
}

func (d parentEndAtWorkspaceCheckpointDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "SELECT workspace_generation FROM sessions") {
		d.beforeGeneration()
	}
	return d.DBTX.QueryRow(ctx, sql, args...)
}

//nolint:paralleltest // Migrate once; then exercise the two independent gate races in parallel.
func TestWorkspaceStoppedRaceKeepsDeadLetterGuardPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for workspace race proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name  string
		early bool
	}{{name: "preparation checkpoint gate", early: true}, {name: "post-preflight workspace gate"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "running")
			seedSavedReviewResults(t, pool, f)
			ctx := jobctx.WithDeadLetterHooks(f.ctx())
			endParent := func() {
				_, err := pool.Exec(ctx, `UPDATE sessions SET status='completed' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "end parent after initial terminal-state check")
			}
			f.stores.CodeReviewWorkspaces = db.NewCodeReviewWorkspaceStore(pool)
			services := &Services{CodeReviewWorkspacePreparationEnabled: true, CodeReviewVisualEvidence: &codeReviewVisualEvidenceProviderStub{snapshot: models.CodeReviewVisualEvidenceSnapshot{Version: 1, RepositoryID: f.job.RepositoryID, PullRequestNumber: 9, HeadSHA: "head", Complete: true}}, CodeReviews: &inlineRecoveryPublisher{}}
			if tt.early {
				_, err := pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,dedupe_key,status) VALUES($1,'agent','prepare_code_review_workspace','{}',$2,'succeeded')`, f.job.OrgID, codeReviewWorkspacePreparationKey(f.job.MetadataID, f.job.SessionID, 0))
				require.NoError(t, err, "seed first workspace checkpoint")
				f.stores.Sessions = db.NewSessionStore(parentEndAtWorkspaceCheckpointDB{DBTX: pool, beforeGeneration: endParent})
			} else {
				services.PR = &stubPRService{syncPullRequestStateFn: func(context.Context, uuid.UUID, uuid.UUID) error { endParent(); return nil }}
			}
			payload, err := json.Marshal(f.job)
			require.NoError(t, err, "encode original controller")
			handlerErr := newRunCodeReviewHandler(f.stores, services, zerolog.Nop())(ctx, "run_code_review", payload)
			require.ErrorIs(t, handlerErr, errCodeReviewWorkspaceStopped, "race must stop at the intended workspace gate")
			require.ErrorIs(t, handlerErr, errCodeReviewEndedParentRecovery, "both workspace exits must retain protected recovery identity")
			jobctx.RunDeadLetterHooks(ctx, handlerErr)
			m, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read state after final-attempt race")
			require.Equal(t, models.CodeReviewSessionStatusRunning, m.Status, "workspace race must not trigger unguarded terminal mutation")
		})
	}
}
