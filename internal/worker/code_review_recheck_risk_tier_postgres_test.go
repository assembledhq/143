package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
)

func TestCodeReviewRecheckPublicationRereadsRiskTierPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for the staged recheck risk-tier proof")
	}
	tests := []struct {
		name          string
		liveTier      string
		expectPublish bool
	}{
		{name: "unchanged live low publishes the staged approval", liveTier: "low", expectPublish: true},
		{name: "live rescore to high withholds the staged approval on retry", liveTier: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPool(t, ctx)
			org, integration, repo, pr, policy, session, metadata, baseline, recheck := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			repoName := "test/risk-tier-recheck"
			for _, stmt := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO organizations(id,name) VALUES($1,'Risk Tier Recheck Test')`, []any{org}},
				{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, org}},
				{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,1,$4,'https://example.invalid/recheck.git',1)`, []any{repo, org, integration, repoName}},
				{`INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','idle')`, []any{session, org}},
				{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title) VALUES($1,$2,7,'https://example.invalid/pr/7',$3,'Large docs change')`, []any{pr, org, repoName}},
				{`INSERT INTO code_review_policies(id,org_id,repository_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,NULL,1,'approve_acceptable','{}','{}','{}','','','{"enabled":true}')`, []any{policy, org}},
				{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($1,$2,$3,$4,$5,$6,'base',$7,'slash_command','completed','baseline-output')`, []any{metadata, org, session, repo, pr, policy, workerRiskTierHead}},
			} {
				_, err := pool.Exec(ctx, stmt.sql, stmt.args...)
				require.NoError(t, err, "seed staged recheck parents")
			}
			manifest, err := codereviewsvc.BuildReviewInputManifest(codereviewsvc.ReviewInputCapture{
				Code:     codereviewsvc.ReviewCodeInput{OrgID: org, RepositoryID: repo, PullRequestID: pr, HeadSHA: workerRiskTierHead, BaseSHA: "base", BaseRef: "main", FilesComplete: true},
				Contract: codereviewsvc.ReviewContractInput{PolicyID: policy, PolicyVersion: 1, PolicyDigest: strings.Repeat("a", 64), RosterDigest: strings.Repeat("b", 64), ModelConfigurationDigest: strings.Repeat("c", 64), PromptContractVersion: "1", PromptContentDigest: strings.Repeat("d", 64), InstructionsDigest: strings.Repeat("e", 64), ExternalInputsComplete: true},
				Title:    "Large docs change", Description: "Expands the guide.",
				Visual: codereviewsvc.ReviewVisualInput{CaptureComplete: true, SourceProvenanceComplete: true},
				TextEvidence: codereviewsvc.ReviewTextInput{Complete: true, SourceProvenanceComplete: true, UnclassifiedDigest: strings.Repeat("0", 64), Items: []codereviewsvc.ReviewTextEvidence{
					{EvidenceID: "pr-body", Surface: "pull_request_description", ProviderObjectID: "7", SourceURL: "https://example.invalid/pr/7", Section: "full", Content: "Expands the guide.", ContentDigest: fmt.Sprintf("%x", sha256.Sum256([]byte("Expands the guide.")))},
				}},
				Gates: codereviewsvc.ReviewGateInput{SnapshotDigest: strings.Repeat("1", 64), EligibilityDigest: strings.Repeat("2", 64), ChecksDigest: strings.Repeat("3", 64), DynamicDigest: strings.Repeat("4", 64), ChecksVerified: true, Complete: true},
			})
			require.NoError(t, err, "build staged input manifest")
			manifestJSON, err := json.Marshal(manifest)
			require.NoError(t, err, "encode staged input manifest")
			assessmentSQL := `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,source_assessment_id,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,coverage_complete,status,result_origin,decision,acceptable,structured_outcome,publication_key,publication_state,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'base','main',$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31)`
			_, err = pool.Exec(ctx, assessmentSQL, baseline, org, repo, repoName, pr, metadata, session, policy, 1, nil, workerRiskTierHead, manifest.InputVersion, manifest.CodeDigest, manifest.ContractDigest, manifest.IntentDigest, manifest.VisualDigest, manifest.RequestDigest, manifest.GateDigest, manifest.InputDigest, manifestJSON, "full", "initial_full", true, "completed", "executed", "needs_human_review", false, json.RawMessage(`{}`), "baseline-publication", "not_required", time.Now().UTC())
			require.NoError(t, err, "seed completed full baseline")
			_, err = pool.Exec(ctx, assessmentSQL, recheck, org, repo, repoName, pr, metadata, session, policy, 2, baseline, workerRiskTierHead, manifest.InputVersion, manifest.CodeDigest, manifest.ContractDigest, manifest.IntentDigest, manifest.VisualDigest, manifest.RequestDigest, manifest.GateDigest, manifest.InputDigest, manifestJSON, "evidence_only", "evidence_changed", true, "publishing", "evidence_only", "approved", true, json.RawMessage(`{}`), "recheck-publication", "reserved", nil)
			require.NoError(t, err, "seed reserved evidence recheck")

			riskPolicy := workerRiskTierPolicy(workerRiskTierAppID).RiskPolicy
			checkpointSignal := workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse)
			staged := checkpointSignal.SizeSafeguardDecision(models.CodeReviewSizeMeasurement{Actual: 6, Limit: 5, Exceeded: true}, models.CodeReviewSizeMeasurement{Actual: 420, Limit: 300, Exceeded: true}, models.CodeReviewSizeMeasurement{Actual: 6, Limit: 300})
			reasons, err := json.Marshal([]models.CodeReviewRiskReason{{Code: models.CodeReviewRiskReasonSizeSafeguard, SizeSafeguard: &staged}})
			require.NoError(t, err, "encode staged waiver record")
			_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET risk_reason_details=$3, rendered_body='Approved after evidence recheck', submitted_commit_sha=head_sha WHERE org_id=$1 AND id=$2`, org, recheck, reasons)
			require.NoError(t, err, "stage the approval with its waived size safeguard")
			checkpointJSON, err := json.Marshal(checkpointSignal)
			require.NoError(t, err, "encode checkpoint")
			stores := &Stores{CodeReviews: db.NewCodeReviewStore(pool), CodeReviewAssessments: db.NewCodeReviewAssessmentStore(pool), CodeReviewRechecks: db.NewCodeReviewRecheckStore(pool), SessionThreads: db.NewSessionThreadStore(pool), SessionMessages: db.NewSessionMessageStore(pool), ThreadSendTx: pool, Repositories: db.NewRepositoryStore(pool), PullRequests: db.NewPullRequestStore(pool)}
			checkpointRecord := models.CodeReviewPromptRecord{OrgID: org, SessionID: session, RecordKey: codeReviewRiskTierRecordKey(session, workerRiskTierHead, *riskPolicy.RiskTierCheck), Role: codeReviewRiskTierPromptRole, Content: "checkpoint", Metadata: checkpointJSON}
			_, err = stores.CodeReviews.CreatePromptRecordIfAbsent(ctx, &checkpointRecord)
			require.NoError(t, err, "persist the low checkpoint the recheck was staged under")

			capture := &fixedRecheckCapture{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{Enabled: true, ApprovalMode: models.CodeReviewApprovalModeApproveAcceptable, RiskPolicy: riskPolicy}}}
			publisher := &fakeRecheckPublisher{}
			lifecycle := &fakeRecheckLifecycle{assessments: stores.CodeReviewAssessments, txStarter: pool}
			live := &fakeRiskTierCheckRuns{runs: []models.CodeReviewCheckRun{workerRiskTierRun(5, workerRiskTierAppID, workerRiskTierHead, tt.liveTier, "false")}}
			services := &Services{CodeReviewInputCapture: capture, CodeReviewRechecksEnabled: true, CodeReviews: publisher, CodeReviewLifecycle: lifecycle, CodeReviewRiskTier: live}
			handler := newRunCodeReviewRecheckHandler(stores, services, zerolog.Nop())
			jobJSON, err := json.Marshal(codeReviewRecheckJob{OrgID: org, AssessmentID: recheck})
			require.NoError(t, err, "encode recheck job")
			jobID, lease := uuid.New(), uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token) VALUES($1,$2,'agent','run_code_review_recheck',$3,'running',$4)`, jobID, org, jobJSON, lease)
			require.NoError(t, err, "lease the publication retry job")

			err = handler(jobctx.WithJobID(jobctx.WithLockToken(ctx, lease), jobID), "run_code_review_recheck", jobJSON)
			require.NoError(t, err, "the publication retry should settle either way")
			after, err := stores.CodeReviewAssessments.GetByID(ctx, org, recheck)
			require.NoError(t, err, "read the retried recheck")
			require.Len(t, live.calls, 1, "the retry should re-read the live risk tier inside the publication lock")
			if tt.expectPublish {
				require.Len(t, publisher.requests, 1, "an unchanged authoritative low tier should keep the staged approval")
				require.Equal(t, models.CodeReviewAssessmentCompleted, after.Status, "the staged approval should complete")
				require.Empty(t, lifecycle.refreshes, "an unchanged tier should not refresh the recheck")
				return
			}
			require.Empty(t, publisher.requests, "a live rescore to high must not publish the stale approval")
			require.Equal(t, models.CodeReviewAssessmentSuperseded, after.Status, "the stale staged approval should be superseded")
			require.NotNil(t, after.FailureDetail, "the supersession should record why")
			require.Contains(t, *after.FailureDetail, "evidence_recheck:risk-tier result changed before publication", "the supersession should name the risk-tier change")
			require.Contains(t, *after.FailureDetail, "checkpoint authoritative/low/false", "the supersession should record the checkpoint observation")
			require.Contains(t, *after.FailureDetail, "live authoritative/high/false", "the supersession should record the live observation")
			require.Equal(t, []uuid.UUID{recheck}, lifecycle.refreshes, "the recheck should fall back to a fresh evaluation")
		})
	}
}
