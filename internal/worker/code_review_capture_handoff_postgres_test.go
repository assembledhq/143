package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewStartupCaptureHandoffPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for persisted startup capture handoff proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name     string
		existing bool
		retire   bool
		change   string
	}{
		{name: "new assessment supplies one authoritative startup capture"},
		{name: "existing assessment still refreshes live health", existing: true},
		{name: "ordinary lifecycle reconciliation refreshes resumed assessment state", existing: true, retire: true},
		{name: "captured head drift cannot create an assessment", change: "head"},
		{name: "captured base drift cannot create an assessment", change: "base"},
		{name: "captured policy drift cannot create an assessment", change: "policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			if !tt.existing {
				_, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
				require.NoError(t, err, "start before an immutable assessment has been captured")
			}
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read the exact queued review identity")
			manifest := f.manifest
			manifest.Code.OrgID, manifest.Code.RepositoryID, manifest.Code.PullRequestID = f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID
			manifest.Contract.PolicyID = f.job.PolicyID
			capture := &captureHandoffProbe{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID, Version: f.job.PolicyVersion}, Snapshot: ghservice.CodeReviewPullRequestSnapshot{HeadSHA: f.job.HeadSHA, BaseSHA: metadata.BaseSHA}}}
			switch tt.change {
			case "head":
				capture.result.Snapshot.HeadSHA = "changed-head"
			case "base":
				capture.result.Snapshot.BaseSHA = "changed-base"
			case "policy":
				capture.result.Policy.ID = uuid.New()
			}
			validator := &captureHandoffLifecycle{valid: true}
			capture.onCall = func(context.Context) {
				require.Equal(t, 1, len(validator.requests), "scheduled ownership must precede initial provider capture")
			}
			syncs := 0
			services := &Services{CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviewLifecycle: validator, PR: &stubPRService{syncPullRequestStateFn: func(context.Context, uuid.UUID, uuid.UUID) error {
				syncs++
				if tt.retire {
					return f.stores.CodeReviewAssessments.Supersede(ctx, f.job.OrgID, f.assessment, 1, "all", "closed during ordinary sync")
				}
				return nil
			}}}
			assessment, captured, err := prepareCodeReviewPreflight(ctx, f.stores, services, zerolog.Nop(), f.job, metadata)
			if tt.change != "" {
				var changed *fullAssessmentQueuedIdentityChanged
				require.ErrorAs(t, err, &changed, "authoritative queued identity drift must retain the existing replacement handoff")
				require.Equal(t, &capture.result, captured, "replacement admission must retain the captured source rather than fetching it again")
				require.Nil(t, assessment, "drift cannot establish an assessment under obsolete queued inputs")
				var count int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_revision_assessments WHERE org_id=$1`, f.job.OrgID).Scan(&count), "read only this tenant's assessment inventory")
				require.Equal(t, 0, count, "queued identity drift must stop before assessment creation")
				require.Equal(t, 1, len(capture.requests), "identity drift must capture the provider once")
				require.Equal(t, 0, syncs, "identity drift must not repeat the completed source refresh")
				return
			}
			require.NoError(t, err, "startup handoff should retain an immutable persisted assessment")
			require.NotNil(t, assessment, "startup must return the owning assessment")
			if tt.existing {
				require.Nil(t, captured, "existing assessment is not authority to skip the independent startup refresh")
				require.Equal(t, 0, len(capture.requests), "existing assessment inputs must not be recaptured at startup")
				require.Equal(t, 1, syncs, "existing assessment must retain exactly one ordinary startup refresh")
				wantStatus := models.CodeReviewAssessmentRunning
				if tt.retire {
					wantStatus = models.CodeReviewAssessmentSuperseded
				}
				require.Equal(t, wantStatus, assessment.Status, "ordinary sync lifecycle changes must be visible before resumed controller work")
				return
			}
			require.Equal(t, &capture.result, captured, "the startup handoff must preserve the exact captured files and evidence")
			require.Equal(t, 0, syncs, "a complete fresh capture must suppress exactly one duplicate ordinary startup refresh")
			expectedRequest := codereviewsvc.AssessmentInputCaptureRequest{OrgID: f.job.OrgID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID, SessionID: f.job.SessionID, AssessmentID: assessment.ID, RequestContext: f.job.RequestContext}
			require.Equal(t, []codereviewsvc.AssessmentInputCaptureRequest{expectedRequest}, capture.requests, "handoff must preserve the tenant, PR, session and immutable capture identity")
			retained, secondCapture, err := prepareCodeReviewPreflight(ctx, f.stores, services, zerolog.Nop(), f.job, metadata)
			require.NoError(t, err, "a later controller attempt should resume the persisted assessment")
			require.Equal(t, assessment.ID, retained.ID, "retry must retain the original assessment")
			require.Nil(t, secondCapture, "startup capture authority must not survive across attempts")
			require.Equal(t, 1, len(capture.requests), "retry must not create duplicate immutable captures")
			require.Equal(t, 1, syncs, "a later attempt must independently refresh live health even on the same head")
			require.Equal(t, 2, len(validator.requests), "each attempt must independently validate scheduled ownership")
		})
	}
}

func TestFullAssessmentPublicationCaptureHandoffPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for locked publication capture handoff proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 4)
	// Reserve unrelated worker capacity so publication and its independent
	// observer/read must complete with exactly two available connections.
	for range 2 {
		conn, err := pool.Acquire(ctx)
		require.NoError(t, err, "reserve shared worker pool capacity")
		t.Cleanup(conn.Release)
	}
	// Model one worker's shared publication admission instead of creating an
	// independent publication slot for every tenant in this shared pool.
	reviews := db.NewCodeReviewStore(pool)
	providerFailure := errors.New("fresh provider capture failed")
	tests := []struct {
		name     string
		mode     string
		local    bool
		change   string
		want     error
		captures int
		sends    int
	}{
		{name: "main controller captures only under publication lock", mode: "main", captures: 1, sends: 1},
		{name: "staged recovery captures only under publication lock", captures: 1, sends: 1},
		{name: "deterministic early stop captures only under publication lock", mode: "early", captures: 1, sends: 1},
		{name: "publisher absent still verifies before local completion", local: true, captures: 1},
		{name: "publisher absent still rejects changed inputs", local: true, change: "inputs", want: errCodeReviewPublicationSuperseded, captures: 1},
		{name: "unstaged outcome cannot acquire publication authority", mode: "unstaged", want: db.ErrCodeReviewAssessmentState},
		{name: "provider failure keeps original unsent state", change: "provider", want: providerFailure, captures: 1},
		{name: "changed inputs supersede without sending", change: "inputs", want: errCodeReviewPublicationSuperseded, captures: 1},
		{name: "lost job lease blocks capture and reservation", change: "lease", want: pgx.ErrNoRows},
		{name: "generation mismatch blocks capture and reservation", change: "generation_before", want: db.ErrCodeReviewAssessmentState},
		{name: "submitted head mismatch blocks capture and reservation", change: "head_before", want: db.ErrCodeReviewAssessmentState},
		{name: "post-capture generation change blocks reservation", change: "generation_during", want: db.ErrCodeReviewAssessmentState, captures: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			f.stores.CodeReviews = reviews
			f.stores.SessionLogs = nil
			seedSavedReviewResults(t, pool, f)
			capture := &captureHandoffProbe{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: f.manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}, Files: []codereviewsvc.PullRequestFile{}}}
			publisher := &freshnessPublicationPublisher{}
			lifecycle := &fakeRecheckLifecycle{}
			services := &Services{CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviewVisualEvidence: &assessmentVisualCheckpoint{snapshot: models.CodeReviewVisualEvidenceSnapshot{Version: 1, Complete: true, RepositoryID: f.job.RepositoryID, PullRequestNumber: 9, HeadSHA: f.job.HeadSHA, AssessmentID: &f.assessment}}, CodeReviews: publisher, CodeReviewLifecycle: lifecycle}
			if tt.local {
				services.CodeReviews = nil
			}
			if tt.mode != "main" && tt.mode != "early" && tt.mode != "unstaged" {
				_, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET result_origin='executed',decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{}',rendered_body='original saved review' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
				require.NoError(t, err, "stage the immutable original outcome before publication recovery")
			}
			capture.onCall = func(lockCtx context.Context) {
				lockCtx, cancel := context.WithTimeout(lockCtx, 5*time.Second)
				defer cancel()
				tx, err := pool.Begin(lockCtx)
				require.NoError(t, err, "open an independent lock observer")
				defer func() { _ = tx.Rollback(ctx) }()
				var acquired bool
				key := fmt.Sprintf("code_review_status_comment:%s:%s", f.job.OrgID, f.job.PullRequestID)
				require.NoError(t, tx.QueryRow(lockCtx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, key).Scan(&acquired), "observe the actual per-PR publication advisory lock")
				require.Equal(t, tt.local, acquired, "only publisher-absent completion may capture outside the publication lock")
				// The observer has proved lock ownership. Release its connection
				// before the independently committed state read needs pool headroom.
				require.NoError(t, tx.Rollback(lockCtx), "release the observer before borrowing another pool connection")
				before, err := f.stores.CodeReviewAssessments.GetByID(lockCtx, f.job.OrgID, f.assessment)
				require.NoError(t, err, "read independently committed state while capture runs")
				require.Equal(t, models.CodeReviewAssessmentRunning, before.Status, "a first capture must precede publication reservation")
				require.Equal(t, models.CodeReviewPublicationNotStarted, before.PublicationState, "a provider wait must retain the original unsent fence")
				if tt.change == "generation_during" {
					_, err = pool.Exec(lockCtx, `UPDATE code_review_revision_assessments SET generation=generation+1 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
					require.NoError(t, err, "simulate loss of exact assessment authority during provider capture")
				}
			}
			publisher.onSubmit = func() {
				before, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
				require.NoError(t, err, "observe the independently committed send fence")
				require.Equal(t, models.CodeReviewAssessmentPublishing, before.Status, "publication must be reserved before sending")
				require.Equal(t, models.CodeReviewPublicationUncertain, before.PublicationState, "uncertainty must commit before the external send")
			}
			callCtx, cancel := context.WithTimeout(f.ctx(), 15*time.Second)
			defer cancel()
			switch tt.change {
			case "provider":
				capture.cause = providerFailure
			case "inputs":
				capture.result.Manifest.CodeDigest = "changed-code"
			case "lease":
				callCtx = jobctx.WithLockToken(callCtx, uuid.New())
			case "head_before":
				f.job.HeadSHA = "obsolete-controller-head"
			}
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "retain the original controller metadata")
			assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "retain the original immutable assessment generation")
			if tt.change == "generation_before" {
				_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET generation=generation+1 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
				require.NoError(t, err, "retire the caller's expected generation before entering publication")
			}
			switch tt.mode {
			case "main":
				payload, encodeErr := json.Marshal(f.job)
				require.NoError(t, encodeErr, "encode the existing controller without adding a job")
				err = newRunCodeReviewHandler(f.stores, services, zerolog.Nop())(callCtx, models.JobTypeRunCodeReview, payload)
			case "early":
				err = completeCodeReviewAfterStableDeterministicFailure(callCtx, f.stores, services, zerolog.Nop(), f.job, metadata, models.DefaultCodeReviewPolicyConfig(), models.PullRequest{}, []codereviewsvc.PullRequestFile{}, models.CodeReviewRiskEvaluation{Acceptable: false}, &assessment)
			default:
				if tt.change == "lease" || tt.change == "generation_before" || tt.change == "generation_during" || tt.change == "head_before" || tt.mode == "unstaged" {
					_, _, err = submitFullReviewWithAssessment(callCtx, f.stores, services, f.job, metadata, &assessment, models.CodeReviewDecisionNeedsHumanReview, "original saved review", []codereviewsvc.PullRequestFile{})
				} else {
					err = resumeStagedFullAssessment(callCtx, f.stores, services, f.job, metadata, assessment, []codereviewsvc.PullRequestFile{})
				}
			}
			if tt.want == nil {
				require.NoError(t, err, "one fresh capture must preserve successful full-review completion")
			} else {
				require.ErrorIs(t, err, tt.want, "publication must retain the original failure and ownership semantics")
			}
			require.Equal(t, tt.captures, len(capture.requests), "each publication phase must capture exactly once after its ownership gates")
			require.Equal(t, tt.sends, len(publisher.requests), "capture consolidation must neither duplicate nor bypass an external review")
			if tt.captures != 0 {
				require.True(t, capture.requests[0].Fresh, "final publication checks must fetch fresh evidence rather than restore startup authority")
				require.Equal(t, f.assessment, capture.requests[0].AssessmentID, "final capture must preserve exact immutable assessment ownership")
			}
			after, readErr := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, readErr, "read this tenant's final publication state")
			if tt.want == nil {
				require.Equal(t, models.CodeReviewAssessmentCompleted, after.Status, "successful consolidation must complete the original assessment")
				wantPublication := models.CodeReviewPublicationConfirmed
				if tt.local {
					wantPublication = models.CodeReviewPublicationNotRequired
				}
				require.Equal(t, wantPublication, after.PublicationState, "publisher absence must retain the explicit not_required outcome")
			} else if tt.change == "inputs" {
				require.Equal(t, models.CodeReviewAssessmentSuperseded, after.Status, "changed unsent inputs must retain the replacement path")
				require.Equal(t, []uuid.UUID{f.assessment}, lifecycle.fallbacks, "changed unsent inputs must admit replacement exactly once")
			} else {
				require.Equal(t, models.CodeReviewAssessmentRunning, after.Status, "failed capture or lost authority must preserve the original unsent assessment")
				require.Equal(t, models.CodeReviewPublicationNotStarted, after.PublicationState, "failed capture or reservation must not create an ambiguous-send fence")
			}
		})
	}
}
