package worker

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestCompletedReviewWorkspaceCleanupBeforeEvidenceDispatchPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for completion, evidence reservation and workspace cleanup proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name    string
		running bool
	}{
		{name: "reserved evidence yields workspace to GC"},
		{name: "running supervisor without dispatch yields workspace to GC", running: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, _ := seedConfirmedDrainReview(t, pool, "failed")
			ctx := f.ctx()
			exec := func(query string, args ...any) {
				t.Helper()
				_, err := pool.Exec(ctx, query, args...)
				require.NoError(t, err, "seed exact completed review and retained workspace")
			}
			// Use a captured policy identity so the real admission store can validate
			// the completed full baseline before reserving evidence in its session.
			f.manifest.Contract.PolicyID = f.job.PolicyID
			f.manifest.Contract.PolicyVersion = 1
			f.manifest.Contract.PolicyDigest = "synthetic-policy-digest"
			manifest, err := json.Marshal(f.manifest)
			require.NoError(t, err, "encode baseline's immutable policy identity")
			exec(`UPDATE code_review_revision_assessments SET input_manifest=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, manifest)
			exec(`UPDATE sessions SET container_id='retained-review-workspace',snapshot_key='saved-review-snapshot' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
			thread := uuid.New()
			exec(`INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,execution_mode,filesystem_mode) VALUES($1,$2,$3,'claude_code','Orchestrator','idle','review','read_only')`, thread, f.job.OrgID, f.job.SessionID)
			staged, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "load exact confirmed full assessment before completion")
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "load exact review controller before completion")
			publisher := &confirmedNoNetworkPublisher{}
			err = resumeStagedFullAssessment(ctx, f.stores, &Services{CodeReviews: publisher}, f.job, metadata, staged, nil)
			require.NoError(t, err, "immutable full outcome must complete while its idle workspace remains referenced")
			full, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "load reusable completed full baseline")
			require.Equal(t, models.CodeReviewAssessmentCompleted, full.Status, "completion must settle the original full assessment before evidence admission")
			parent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "load parent after immutable completion")
			require.Equal(t, new("retained-review-workspace"), parent.ContainerID, "completion must leave workspace destruction to GC")
			exec(`UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)

			recheckID := uuid.New()
			evidenceManifest := f.manifest
			evidenceManifest.VisualDigest = "changed-visual"
			evidenceManifest.InputDigest = "new-evidence"
			evidenceManifestJSON, err := json.Marshal(evidenceManifest)
			require.NoError(t, err, "encode the new evidence assessment's captured inputs")
			capture := models.CodeReviewAssessmentCapture{
				ID: recheckID, OrgID: f.job.OrgID, RepositoryID: f.job.RepositoryID,
				PullRequestID: f.job.PullRequestID, PolicyID: f.job.PolicyID, SessionID: f.job.SessionID,
				Generation: 2, SourceAssessmentID: &full.ID, PreviousAssessmentID: &full.ID,
				PreviousPublishedAssessmentID: &full.ID, BaseSHA: "base", BaseRef: "main", HeadSHA: "head",
				InputVersion: f.manifest.InputVersion, CodeDigest: f.manifest.CodeDigest,
				ContractDigest: f.manifest.ContractDigest, IntentDigest: f.manifest.IntentDigest,
				VisualDigest: evidenceManifest.VisualDigest, RequestDigest: f.manifest.RequestDigest,
				GateDigest: f.manifest.GateDigest, InputDigest: evidenceManifest.InputDigest, InputManifest: evidenceManifestJSON,
				ReviewScope: models.CodeReviewScopeEvidenceOnly, RouteReason: models.CodeReviewRouteVisualChanged,
				PublicationKey: "code-review-assessment:" + recheckID.String(),
			}
			// Reproduce admission's durable reservation and supervisor job under its
			// real PR lock, after completion commits but before GC claims the sandbox.
			err = db.NewCodeReviewScheduleStore(pool).WithLockedPR(ctx, f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				_, _, createErr := db.NewCodeReviewAssessmentStore(tx).Create(ctx, capture)
				if createErr != nil {
					return createErr
				}
				dedupe := "code_review_recheck:" + recheckID.String()
				_, enqueueErr := db.NewJobStore(tx).EnqueueWithOpts(ctx, f.job.OrgID, db.EnqueueOpts{Queue: "agent", JobType: models.JobTypeRunCodeReviewRecheck, Payload: map[string]string{"org_id": f.job.OrgID.String(), "assessment_id": recheckID.String()}, DedupeKey: &dedupe, Priority: 5, MaxAttempts: 8})
				if enqueueErr != nil {
					return enqueueErr
				}
				state.ActiveAssessmentID = &recheckID
				state.CurrentAssessmentID = &recheckID
				state.ActiveSessionID = &f.job.SessionID
				state.State = models.CodeReviewScheduleRunning
				return nil
			})
			require.NoError(t, err, "admission must reserve evidence in the completed baseline's session before GC")
			if tt.running {
				require.NoError(t, f.stores.CodeReviewAssessments.MarkRunning(ctx, f.job.OrgID, recheckID, capture.Generation, capture.InputDigest), "supervisor may claim the assessment before its first continuation dispatch")
			}
			var before string
			err = pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(a),to_jsonb(p),(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j WHERE j.org_id=a.org_id))::text FROM code_review_revision_assessments a JOIN code_review_pr_state p ON p.org_id=a.org_id AND p.pull_request_id=a.pull_request_id WHERE a.org_id=$1 AND a.id=$2`, f.job.OrgID, recheckID).Scan(&before)
			require.NoError(t, err, "capture admitted assessment, scheduler and supervisor before cleanup")
			rechecks := db.NewCodeReviewRecheckStore(pool)
			dispatchInput := models.CodeReviewRecheckDispatchInput{OrgID: f.job.OrgID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID, AssessmentID: recheckID, SessionID: f.job.SessionID, ThreadID: thread, ExpectedTurn: 1, Prompt: "Assess the new synthetic evidence."}
			if tt.running {
				_, _, err = rechecks.Dispatch(ctx, dispatchInput)
				require.ErrorIs(t, err, db.ErrCodeReviewRecheckFence, "continuation dispatch must wait for physical workspace ownership to clear")
			}
			cleared, err := f.stores.Sessions.FinalizeIdleCodeReviewContainer(ctx, f.job.OrgID, f.job.SessionID, "retained-review-workspace")
			require.NoError(t, err, "real GC finalization must remain independent of the undispatched evidence reservation")
			require.True(t, cleared, "GC must break the retained-container and first-dispatch dependency cycle")
			var after string
			err = pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(a),to_jsonb(p),(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j WHERE j.org_id=a.org_id))::text FROM code_review_revision_assessments a JOIN code_review_pr_state p ON p.org_id=a.org_id AND p.pull_request_id=a.pull_request_id WHERE a.org_id=$1 AND a.id=$2`, f.job.OrgID, recheckID).Scan(&after)
			require.NoError(t, err, "load reservation and its exact supervisor after cleanup")
			require.Equal(t, before, after, "GC must preserve every admitted assessment, scheduler and supervisor field")
			if !tt.running {
				require.NoError(t, f.stores.CodeReviewAssessments.MarkRunning(ctx, f.job.OrgID, recheckID, capture.Generation, capture.InputDigest), "reserved evidence remains available for its supervisor after cleanup")
			}
			dispatch, replayed, err := rechecks.Dispatch(ctx, dispatchInput)
			require.NoError(t, err, "the same admitted evidence assessment must dispatch after GC clears its predecessor workspace")
			require.False(t, replayed, "cleanup must allow the first exact continuation instead of replaying a fabricated receipt")
			require.Equal(t, models.CodeReviewRecheckDispatchPending, dispatch.Status, "first dispatch must create a pending exact-turn continuation")
			require.Equal(t, recheckID, dispatch.AssessmentID, "continuation must retain the admitted immutable assessment identity")
			preserved, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, full.ID)
			require.NoError(t, err, "load original reusable full outcome after evidence dispatch")
			require.Equal(t, full, preserved, "cleanup and first dispatch must preserve the original full outcome and confirmed receipt")
			require.Empty(t, publisher.requests, "confirmed completion must never publish its historical result again")
		})
	}
}
