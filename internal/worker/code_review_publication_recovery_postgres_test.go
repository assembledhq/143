package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type inlineRecoveryPublisher struct {
	files        []codereviewsvc.PullRequestFile
	fileErr      error
	submitErr    error
	cancelSubmit context.CancelFunc
	fileRequests []codereviewsvc.PullRequestFilesRequest
	requests     []codereviewsvc.SubmitReviewRequest
}

func (p *inlineRecoveryPublisher) ListPullRequestFiles(_ context.Context, request codereviewsvc.PullRequestFilesRequest) ([]codereviewsvc.PullRequestFile, error) {
	p.fileRequests = append(p.fileRequests, request)
	return p.files, p.fileErr
}

func (p *inlineRecoveryPublisher) SubmitReview(_ context.Context, request codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, error) {
	p.requests = append(p.requests, request)
	if p.submitErr != nil {
		if p.cancelSubmit != nil {
			p.cancelSubmit()
		}
		return codereviewsvc.SubmitReviewResult{}, p.submitErr
	}
	comments := make([]codereviewsvc.SubmitReviewPostedComment, 0, len(request.Comments))
	for _, comment := range request.Comments {
		comments = append(comments, codereviewsvc.SubmitReviewPostedComment{ID: 123, Path: comment.Path, Line: comment.Line, Body: comment.Body, DedupeKey: comment.DedupeKey})
	}
	return codereviewsvc.SubmitReviewResult{ID: 77, URL: "https://example.invalid/review/77", Body: request.Body, Comments: comments}, nil
}

//nolint:paralleltest // Concurrent full migration chains exhaust the shared PostgreSQL lock budget.
func TestStagedFullPublicationRecoveryPreservesInlineCommentsPostgres(t *testing.T) {
	// Each case applies the full migration chain to its own schema, serially.
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL recovery proof")
	}
	tests := []struct {
		name             string
		publicationState models.CodeReviewPublicationState
		fileFailure      bool
		submitErr        error
		retired          bool
		cancelOnReject   bool
	}{
		{name: "retry before publication reservation", publicationState: models.CodeReviewPublicationNotStarted},
		{name: "retry after publication reservation", publicationState: models.CodeReviewPublicationReserved},
		{name: "retry after uncertain publication", publicationState: models.CodeReviewPublicationUncertain},
		{name: "diff fetch failure remains retryable", publicationState: models.CodeReviewPublicationUncertain, fileFailure: true},
		{name: "definitive first rejection releases admission", publicationState: models.CodeReviewPublicationReserved, submitErr: fmt.Errorf("%w: %w", codereviewsvc.ErrReviewPublicationRejected, &ghservice.GitHubAPIError{StatusCode: 422}), retired: true},
		{name: "cancellation after rejection still releases admission", publicationState: models.CodeReviewPublicationReserved, submitErr: fmt.Errorf("%w: %w", codereviewsvc.ErrReviewPublicationRejected, &ghservice.GitHubAPIError{StatusCode: 422}), retired: true, cancelOnReject: true},
		{name: "later rejection cannot erase earlier uncertainty", publicationState: models.CodeReviewPublicationUncertain, submitErr: fmt.Errorf("%w: %w", codereviewsvc.ErrReviewPublicationRejected, &ghservice.GitHubAPIError{StatusCode: 422})},
		{name: "first ambiguous send remains blocked", publicationState: models.CodeReviewPublicationReserved, submitErr: errors.New("connection lost after send")},
		{name: "unclassified rejection remains blocked", publicationState: models.CodeReviewPublicationReserved, submitErr: &ghservice.GitHubAPIError{StatusCode: 422}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pool := fullRecoveryPostgresPool(t, ctx)
			org, integration, repo, pr, policy, session, metadata, assessment, findingID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			jobID, lease := uuid.New(), uuid.New()
			repoName, key := "test/inline-recovery", "inline-publication-key"
			manifest := codereviewsvc.ReviewInputManifest{
				InputVersion: codereviewsvc.ReviewInputManifestVersion, ReuseEligible: true,
				InputDigest: "all", CodeDigest: "code", ContractDigest: "contract", IntentDigest: "intent", VisualDigest: "visual", RequestDigest: "request", GateDigest: "gates",
				Code: codereviewsvc.ReviewCodeInput{HeadSHA: "head", BaseSHA: "base", BaseRef: "main"},
			}
			manifestJSON, err := json.Marshal(manifest)
			require.NoError(t, err, "encode immutable assessment inputs")
			finding := models.CodeReviewFinding{Path: stringPtr("file.go"), StartLine: intPtr(347), EndLine: intPtr(354), Severity: models.CodeReviewFindingSeverityHigh, Summary: "Unresolved blocker", Body: "Fix this defect", DedupeKey: "stable-key"}
			body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{Decision: models.CodeReviewDecisionNeedsHumanReview, Findings: []models.CodeReviewFinding{finding}})
			status := models.CodeReviewAssessmentPublishing
			if tt.publicationState == models.CodeReviewPublicationNotStarted {
				status = models.CodeReviewAssessmentRunning
			}
			for _, seed := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO organizations(id,name) VALUES($1,'Inline Recovery Test')`, []any{org}},
				{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, org}},
				{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,1,$4,'https://example.invalid/recovery.git',1)`, []any{repo, org, integration, repoName}},
				{`INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','idle')`, []any{session, org}},
				{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,7,'https://example.invalid/pr/7',$3,'Inline recovery','head','base')`, []any{pr, org, repoName}},
				{`INSERT INTO code_review_policies(id,org_id,repository_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,NULL,1,'approve_acceptable','{}','{}','{}','','','{}')`, []any{policy, org}},
				{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key,additions,deletions) VALUES($1,$2,$3,$4,$5,$6,'base','head','slash_command','failed',$7,1,1)`, []any{metadata, org, session, repo, pr, policy, key}},
				{`INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,coverage_complete,status,result_origin,decision,acceptable,risk_reason_details,structured_outcome,rendered_body,publication_key,publication_state,submitted_commit_sha) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,'base','main','head',$9,'code','contract','intent','visual','request','gates','all',$10,'full','initial_full',false,$11,'executed','needs_human_review',false,'[]','{}',$12,$13,$14,'head')`, []any{assessment, org, repo, repoName, pr, metadata, session, policy, manifest.InputVersion, manifestJSON, status, body, key, tt.publicationState}},
				{`INSERT INTO code_review_findings(id,org_id,session_id,dedupe_key,severity,confidence,path,start_line,end_line,summary,body,selected_for_inline) VALUES($1,$2,$3,'stable-key','high','high','file.go',347,354,'Unresolved blocker','Fix this defect',true)`, []any{findingID, org, session}},
				{`INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id,head_sha,base_sha,base_ref,active_session_id,active_assessment_id,state) VALUES($1,$2,$3,'head','base','main',$4,$5,'running')`, []any{org, repo, pr, session, assessment}},
				{`INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token) VALUES($1,$2,'agent','run_code_review','{}','running',$3)`, []any{jobID, org, lease}},
			} {
				_, err := pool.Exec(ctx, seed.sql, seed.args...)
				require.NoError(t, err, "seed staged publication recovery state")
			}
			files := []codereviewsvc.PullRequestFile{{Filename: "file.go", Patch: "@@ -351 +351 @@\n-old\n+new", Additions: 1, Deletions: 1}}
			publisher := &inlineRecoveryPublisher{files: files, submitErr: tt.submitErr}
			if tt.fileFailure {
				publisher.fileErr = &ghservice.GitHubAPIError{StatusCode: http.StatusServiceUnavailable}
			}
			capture := &fixedRecheckCapture{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{ID: policy}, Files: files}}
			stores := &Stores{CodeReviews: db.NewCodeReviewStore(pool), CodeReviewAssessments: db.NewCodeReviewAssessmentStore(pool), Repositories: db.NewRepositoryStore(pool), PullRequests: db.NewPullRequestStore(pool), ThreadSendTx: pool}
			services := &Services{CodeReviews: publisher, CodeReviewInputCapture: capture}
			job := runCodeReviewPayload{OrgID: org, SessionID: session, MetadataID: metadata, RepositoryID: repo, PullRequestID: pr, PolicyID: policy, PolicyVersion: 1, HeadSHA: "head", OutputKey: key}
			handlerCtx, cancelHandler := context.WithCancel(jobctx.WithDeadLetterHooks(jobctx.WithJobID(jobctx.WithLockToken(ctx, lease), jobID)))
			defer cancelHandler()
			if tt.cancelOnReject {
				publisher.cancelSubmit = cancelHandler
			}
			if tt.retired {
				_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='running' WHERE org_id=$1 AND id=$2`, org, metadata)
				require.NoError(t, err, "start with active metadata to exercise the full terminal hook")
				payload, marshalErr := json.Marshal(job)
				require.NoError(t, marshalErr, "encode full review controller job")
				err = newRunCodeReviewHandler(stores, services, zerolog.Nop())(handlerCtx, "run_code_review", payload)
				var fatal *FatalError
				require.ErrorAs(t, err, &fatal, "definitive rejection should immediately invoke the worker terminal path")
				jobctx.RunDeadLetterHooks(context.WithoutCancel(handlerCtx), err)
				currentMetadata, metadataErr := stores.CodeReviews.GetBySessionID(ctx, org, session)
				require.NoError(t, metadataErr, "read metadata after full review terminal hook")
				require.Equal(t, models.CodeReviewSessionStatusFailed, currentMetadata.Status, "terminal hook must fail the running review metadata")
				terminalAssessment, assessmentErr := stores.CodeReviewAssessments.GetByID(ctx, org, assessment)
				require.NoError(t, assessmentErr, "read assessment before the recovery sweeper")
				require.Equal(t, models.CodeReviewAssessmentFailed, terminalAssessment.Status, "full terminal hook must retire the rejected assessment without relying on a sweep")
				terminalSchedule, scheduleErr := db.NewCodeReviewScheduleStore(pool).Get(ctx, org, pr)
				require.NoError(t, scheduleErr, "read scheduler before the recovery sweeper")
				require.Nil(t, terminalSchedule.ActiveAssessmentID, "full terminal hook must release the scheduler reservation")

			} else {
				var recovered bool
				recovered, err = recoverStagedFullAssessment(handlerCtx, stores, services, job)
				require.True(t, recovered, "staged publication must take the early recovery path")
			}
			if tt.fileFailure {
				var retryable *RetryableError
				require.ErrorAs(t, err, &retryable, "a temporary diff fetch failure must retry instead of publishing without inline comments")
				require.Empty(t, publisher.requests, "no GitHub publication may occur without the required diff")
				return
			}
			if tt.submitErr != nil {
				require.ErrorIs(t, err, tt.submitErr, "original publication failure should reach the controller")
				terminalReason := codeReviewDeadLetterReason(err)
				schedule := db.NewCodeReviewScheduleStore(pool)
				require.NoError(t, schedule.ReconcileTerminalReviews(ctx, org, repo, pr), "reconcile failed controller after publication outcome")
				got, err := stores.CodeReviewAssessments.GetByID(ctx, org, assessment)
				require.NoError(t, err, "read reconciled assessment")
				wantStatus, wantPublication := models.CodeReviewAssessmentPublishing, models.CodeReviewPublicationUncertain
				if tt.retired {
					wantStatus, wantPublication = models.CodeReviewAssessmentFailed, models.CodeReviewPublicationReserved
					require.Equal(t, terminalReason, *got.FailureDetail, "retain the terminal rejection diagnostic")
				}
				require.Equal(t, wantStatus, got.Status, "retire only the definitely rejected assessment")
				require.Equal(t, wantPublication, got.PublicationState, "ambiguous sends must retain their publication fence")
				require.Equal(t, body, *got.RenderedBody, "recovery must preserve the staged review")
				require.Nil(t, got.PublicationReceipt, "rejection must not invent a successful receipt")
				tx, err := pool.Begin(ctx)
				require.NoError(t, err, "begin admission check")
				defer func() { _ = tx.Rollback(ctx) }()
				active, err := db.HasActiveCodeReview(ctx, tx, org, pr, time.Minute)
				require.NoError(t, err, "check replacement admission")
				require.Equal(t, !tt.retired, active, "only a definitive first-attempt rejection should unblock the queued review")
				return
			}
			require.NoError(t, err, "staged publication recovery should complete successfully")
			require.Equal(t, []codereviewsvc.PullRequestFilesRequest{{InstallationID: 1, Repository: repoName, PullNumber: 7}}, publisher.fileRequests, "recovery should restore the missing diff before constructing inline comments")
			require.Equal(t, []codereviewsvc.SubmitReviewRequest{{
				InstallationID: 1, Repository: repoName, PullNumber: 7, HeadSHA: "head", OutputKey: key,
				Decision: codereviewsvc.SubmitReviewDecisionNeedsHumanReview, Body: body, RequirePublicationReceipt: true,
				Comments: []codereviewsvc.SubmitReviewComment{{Path: "file.go", Line: 351, Body: "[P1] Fix this defect", DedupeKey: "stable-key"}},
			}}, publisher.requests, "recovery should retain the exact staged decision and publish the blocking finding at its mapped anchor")
			got, err := stores.CodeReviewAssessments.GetByID(ctx, org, assessment)
			require.NoError(t, err, "load the completed recovered assessment")
			require.Equal(t, models.CodeReviewAssessmentCompleted, got.Status, "recovered assessment should complete")
			require.Equal(t, models.CodeReviewPublicationConfirmed, got.PublicationState, "recovered publication must have a confirmed receipt")
			var commentID *int64
			err = pool.QueryRow(ctx, `SELECT github_comment_id FROM code_review_findings WHERE org_id=$1 AND id=$2`, org, findingID).Scan(&commentID)
			require.NoError(t, err, "load the original finding receipt")
			require.Equal(t, new(int64(123)), commentID, "the reanchored comment receipt should be stored on the original finding")
		})
	}
}

//nolint:paralleltest // Concurrent full migration chains exhaust the shared PostgreSQL lock budget.
func TestConfirmedFullPublicationRecoversBeforeTerminalAndHeadChecksPostgres(t *testing.T) {
	// Full migration chains exceed the disposable server's lock budget when
	// several schemas are migrated concurrently.
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL recovery proof")
	}
	tests := []struct {
		name          string
		wrongIdentity bool
		wantErr       bool
	}{
		{name: "confirmed receipt recovers failed metadata after PR head moves"},
		{name: "controller identity mismatch fails closed", wrongIdentity: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pool := fullRecoveryPostgresPool(t, ctx)
			org, integration, repo, pr, policy, session, metadata, assessment := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			repoName, key, inputDigest := "test/full-recovery", "full-publication-key", strings.Repeat("a", 64)
			for _, seed := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO organizations(id,name) VALUES($1,'Review Recovery Test')`, []any{org}},
				{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, org}},
				{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,1,$4,'https://example.invalid/recovery.git',1)`, []any{repo, org, integration, repoName}},
				{`INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','idle')`, []any{session, org}},
				{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,7,'https://example.invalid/pr/7',$3,'Review recovery','new-head','base')`, []any{pr, org, repoName}},
				{`INSERT INTO code_review_policies(id,org_id,repository_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,NULL,1,'approve_acceptable','{}','{}','{}','','','{}')`, []any{policy, org}},
				{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key,additions,deletions,failure_reason) VALUES($1,$2,$3,$4,$5,$6,'base','old-head','slash_command','failed',$7,17,4,'worker retry exhausted')`, []any{metadata, org, session, repo, pr, policy, key}},
			} {
				_, err := pool.Exec(ctx, seed.sql, seed.args...)
				require.NoError(t, err, "seed recovery parent row")
			}
			reviewID := int64(9876)
			reviewURL := "https://example.invalid/review/9876"
			receipt, err := fullAssessmentPublicationReceipt(assessment, inputDigest, "old-head", &reviewID, &reviewURL, nil, nil)
			require.NoError(t, err, "build immutable confirmed publication receipt")
			outcome := json.RawMessage(`{"coverage_complete":false,"description_assessments":[]}`)
			body := "Published review for old-head"
			_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,coverage_complete,status,result_origin,decision,acceptable,risk_reason_details,structured_outcome,rendered_body,publication_key,publication_state,publication_receipt,github_review_id,github_review_url,submitted_commit_sha) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,'base','main','old-head',2,'code','contract','intent','visual','request','gate',$9,'{}','full','initial_full',false,'publishing','executed','blocked',false,'[]',$10,$11,$12,'confirmed',$13,$14,$15,'old-head')`, assessment, org, repo, repoName, pr, metadata, session, policy, inputDigest, outcome, body, key, receipt, reviewID, reviewURL)
			require.NoError(t, err, "seed staged full assessment with confirmed receipt")
			_, err = pool.Exec(ctx, `INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id,head_sha,base_sha,base_ref,active_session_id,active_assessment_id,state) VALUES($1,$2,$3,'new-head','base','main',$4,$5,'running')`, org, repo, pr, session, assessment)
			require.NoError(t, err, "seed scheduler active assessment on moved PR head")

			capture := &fixedRecheckCapture{}
			publisher := &fakeRecheckPublisher{}
			stores := &Stores{CodeReviews: db.NewCodeReviewStore(pool), CodeReviewAssessments: db.NewCodeReviewAssessmentStore(pool), ThreadSendTx: pool}
			services := &Services{CodeReviewInputCapture: capture, CodeReviews: publisher}
			job := runCodeReviewPayload{OrgID: org, SessionID: session, MetadataID: metadata, RepositoryID: repo, PullRequestID: pr, PolicyID: policy, PolicyVersion: 1, HeadSHA: "old-head", OutputKey: key}
			if tt.wrongIdentity {
				job.OutputKey = "wrong-publication-key"
			}
			payload, err := json.Marshal(job)
			require.NoError(t, err, "encode original full review job")
			err = newRunCodeReviewHandler(stores, services, zerolog.Nop())(ctx, "run_code_review", payload)
			if tt.wantErr {
				require.ErrorIs(t, err, db.ErrCodeReviewAssessmentState, "mismatched controller identity must fail closed")
			} else {
				require.NoError(t, err, "confirmed publication must recover before terminal metadata and changed-head exits")
			}
			require.Equal(t, 0, capture.calls, "confirmed publication recovery must not recapture current PR inputs")
			require.Empty(t, publisher.requests, "confirmed publication recovery must not resend a GitHub review")
			require.Empty(t, publisher.reconciliations, "confirmed receipt must not require network reconciliation")
			var status, decision, finalBody, failureReason string
			var additions, deletions int
			var storedReviewID *int64
			err = pool.QueryRow(ctx, `SELECT status,COALESCE(decision,''),COALESCE(final_review_body,''),COALESCE(failure_reason,''),additions,deletions,github_review_id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2`, org, session).Scan(&status, &decision, &finalBody, &failureReason, &additions, &deletions, &storedReviewID)
			require.NoError(t, err, "load legacy metadata after recovery")
			got, err := stores.CodeReviewAssessments.GetByID(ctx, org, assessment)
			require.NoError(t, err, "load assessment after recovery")
			var currentID, activeID *uuid.UUID
			var scheduleState string
			err = pool.QueryRow(ctx, `SELECT state,current_assessment_id,active_assessment_id FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&scheduleState, &currentID, &activeID)
			require.NoError(t, err, "load settled scheduler state")
			if tt.wantErr {
				require.Equal(t, "failed", status, "identity mismatch must leave terminal metadata unchanged")
				require.Equal(t, models.CodeReviewAssessmentPublishing, got.Status, "identity mismatch must retain staged assessment")
				require.Equal(t, &assessment, activeID, "identity mismatch must not settle scheduler")
				return
			}
			require.Equal(t, "completed", status, "legacy metadata should recover to completed")
			require.Equal(t, "blocked", decision, "staged decision should be preserved")
			require.Equal(t, body, finalBody, "staged review body should be preserved")
			require.Empty(t, failureReason, "terminal failure should be cleared after successful recovery")
			require.Equal(t, 17, additions, "stored additions should not be replaced by current PR changes")
			require.Equal(t, 4, deletions, "stored deletions should not be replaced by current PR changes")
			require.Equal(t, &reviewID, storedReviewID, "confirmed GitHub receipt should remain on legacy metadata")
			require.Equal(t, models.CodeReviewAssessmentCompleted, got.Status, "assessment should complete from staged outcome")
			require.Equal(t, models.CodeReviewPublicationConfirmed, got.PublicationState, "assessment receipt should remain confirmed")
			require.JSONEq(t, string(receipt), string(got.PublicationReceipt), "publication receipt must not be rewritten")
			require.JSONEq(t, string(outcome), string(got.StructuredOutcome), "structured outcome must remain staged")
			require.Nil(t, activeID, "scheduler active assessment should settle")
			require.Equal(t, &assessment, currentID, "scheduler current assessment should advance to recovered result")
			require.Equal(t, "idle", scheduleState, "moved PR head must not be marked covered")
		})
	}
}

func fullRecoveryPostgresPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect disposable PostgreSQL")
	ensureFullReviewPostgresExtensions(t, ctx, admin)
	schema := "full_recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated recovery schema")
	t.Cleanup(func() {
		withFullReviewFixtureDDL(t, ctx, admin, func() {
			_, cleanupErr := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
			require.NoError(t, cleanupErr, "drop only this isolated recovery schema")
		})
		require.NoError(t, admin.Close(ctx), "close PostgreSQL admin connection")
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse PostgreSQL URL")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create isolated recovery pool")
	t.Cleanup(pool.Close)
	migrations, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	require.NoError(t, err, "list migration chain")
	sort.Strings(migrations)
	withFullReviewFixtureDDL(t, ctx, admin, func() {
		for _, path := range migrations {
			up, readErr := os.ReadFile(path)
			require.NoError(t, readErr, "read migration")
			// The dedicated admin connection only holds the shared DDL lock.
			// Actual migrations use pool autocommit connections so concurrent
			// indexes retain their required transaction boundary.
			_, applyErr := pool.Exec(ctx, string(up))
			require.NoError(t, applyErr, "apply migration "+filepath.Base(path))
		}
	})
	return pool
}

// pgcrypto belongs to the database, not an individual fixture schema. Creating
// it under an isolated search_path races other migrations, and dropping that
// schema can delete the extension while another fixture still needs it.
// Serialize only this shared external resource across processes; keep actual
// migration chains, schemas and tenant test cases otherwise isolated.
func ensureFullReviewPostgresExtensions(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "begin shared extension fixture setup")
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('143-worker-test-pgcrypto-setup',0))`)
	require.NoError(t, err, "serialize database-global extension fixture setup")
	_, err = tx.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public`)
	require.NoError(t, err, "install shared test extension outside tenant schemas")
	var namespace string
	err = tx.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pgcrypto'`).Scan(&namespace)
	require.NoError(t, err, "read shared extension schema")
	if namespace != "public" {
		_, err = tx.Exec(ctx, `ALTER EXTENSION pgcrypto SET SCHEMA public`)
		require.NoError(t, err, "normalize existing test extension before isolated schema cleanup")
	}
	require.NoError(t, tx.Commit(ctx), "commit extension fixture setup and release advisory lock")
}

// Full-chain schema setup and cascading teardown share the disposable server's
// lock budget. Hold a session lock on the dedicated admin connection, with short
// autocommit try-lock polls so waiters retain no old MVCC snapshots. A blocking
// advisory-lock SELECT can make CREATE INDEX CONCURRENTLY wait on its snapshot
// while that SELECT waits on the DDL guard. Test data and tenant cases remain
// parallel, migrations remain autocommit, and this lock also fences processes.
func withFullReviewFixtureDDL(t *testing.T, ctx context.Context, conn *pgx.Conn, operation func()) {
	t.Helper()
	const lockSQL = `SELECT pg_try_advisory_lock(hashtextextended('143-worker-test-full-schema-ddl',0))`
	for {
		var acquired bool
		require.NoError(t, conn.QueryRow(ctx, lockSQL).Scan(&acquired), "try shared full-schema DDL fixture guard without retaining a wait snapshot")
		if acquired {
			break
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			require.NoError(t, ctx.Err(), "fixture DDL guard wait must respect cancellation")
		case <-timer.C:
		}
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var released bool
		require.NoError(t, conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended('143-worker-test-full-schema-ddl',0))`).Scan(&released), "always release dedicated connection's DDL guard after operation")
		require.True(t, released, "fixture DDL operation must release the exact acquired session lock")
	}()
	operation()
}
