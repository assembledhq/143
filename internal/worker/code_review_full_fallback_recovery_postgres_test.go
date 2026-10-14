package worker

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fullFallbackSnapshot struct{}

func (fullFallbackSnapshot) PrepareCodeReviewPullRequestSnapshot(context.Context, uuid.UUID, uuid.UUID) (ghservice.CodeReviewPullRequestSnapshotReader, error) {
	return func(context.Context, int) (ghservice.CodeReviewPullRequestSnapshot, error) {
		return ghservice.CodeReviewPullRequestSnapshot{Number: 7, State: "open", Title: "Fallback recovery", HeadSHA: "head", BaseSHA: "base", BaseRef: "main", HTMLURL: "https://example.test/pr/7"}, nil
	}, nil
}

func TestFullAssessmentFallbackSupervisorRecoveryPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for full fallback recovery proof")
	}
	ctx := context.Background()
	// The shared helper installs pgcrypto outside fixture schemas under a
	// database advisory lock; independent tenant cases can remain parallel.
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name        string
		status      models.CodeReviewAssessmentStatus
		publication models.CodeReviewPublicationState
		protected   bool
		receipt     bool
		reviewID    bool
	}{
		{"superseded full review", models.CodeReviewAssessmentSuperseded, models.CodeReviewPublicationReserved, false, false, false},
		{"failed full review", models.CodeReviewAssessmentFailed, models.CodeReviewPublicationNotStarted, false, false, false},
		{"uncertain publication", models.CodeReviewAssessmentSuperseded, models.CodeReviewPublicationUncertain, true, false, false},
		{"confirmed publication", models.CodeReviewAssessmentFailed, models.CodeReviewPublicationConfirmed, true, false, false},
		{"publication receipt", models.CodeReviewAssessmentFailed, models.CodeReviewPublicationReserved, true, true, false},
		{"GitHub review id", models.CodeReviewAssessmentSuperseded, models.CodeReviewPublicationReserved, true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, integration, repo, pr, session, metadata, assessment, controller := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Fallback recovery')`, org)
			require.NoError(t, err, "seed isolated fallback organization")
			store := db.NewCodeReviewStore(pool)
			policy, err := store.SavePolicy(ctx, org, models.DefaultCodeReviewPolicyConfig(), nil)
			require.NoError(t, err, "save the replacement review policy")
			// Parallel cases sweep this shared schema, so publication protection
			// must be visible as soon as the assessment is inserted.
			for _, seed := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, org}},
				{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,$4,'test/fallback','https://example.test/fallback.git',1)`, []any{repo, org, integration, int64(repo.ID())}},
				{`INSERT INTO sessions(id,org_id,origin,status,revision_context) VALUES($1,$2,'code_review','completed','{}')`, []any{session, org}},
				{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,7,'https://example.test/pr/7','test/fallback','Fallback recovery','head','base')`, []any{pr, org}},
				{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($1,$2,$3,$4,$5,$6,'base','head','slash_command','failed','fallback-output')`, []any{metadata, org, session, repo, pr, policy.ID}},
				{`INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,status,publication_key,publication_state,failure_detail,completed_at,publication_receipt,github_review_id) VALUES($1,$2,$3,'test/fallback',$4,$5,$6,$7,1,'base','main','head',1,'code','contract','intent','visual','request','gates','input','{}','full','initial_full',$8,'fallback-publication',$9,'full_review:inputs changed before publication',now(),CASE WHEN $10 THEN '{}'::jsonb ELSE NULL END,CASE WHEN $11 THEN 123 ELSE NULL END)`, []any{assessment, org, repo, pr, metadata, session, policy.ID, tt.status, tt.publication, tt.receipt, tt.reviewID}},
				{`INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id,head_sha,base_sha,base_ref,active_session_id,active_assessment_id,state) VALUES($1,$2,$3,'head','base','main',$4,$5,'running')`, []any{org, repo, pr, session, assessment}},
				{`INSERT INTO jobs(id,org_id,queue,job_type,payload,dedupe_key,status,max_attempts,attempts) VALUES($1,$2,'agent','run_code_review',jsonb_build_object('session_id',$3::text,'review_output_key','fallback-publication'),'code_review:fallback-publication','dead_letter',8,8)`, []any{controller, org, session}},
			} {
				_, err := pool.Exec(ctx, seed.sql, seed.args...)
				require.NoError(t, err, "seed interrupted full fallback after original controller exhaustion")
			}
			schedules := db.NewCodeReviewScheduleStore(pool)
			require.NoError(t, schedules.RepairMissingWakes(ctx), "sweep should recreate the lost full fallback recovery wake")
			var recoveryJob uuid.UUID
			var payload json.RawMessage
			if tt.protected {
				var pending int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='run_code_review_recheck' AND status='pending'`, org).Scan(&pending), "count repair wakes for protected terminal publication")
				require.Equal(t, 0, pending, "repair must not enqueue fallback for protected terminal publication")
				payload, err = json.Marshal(map[string]string{"org_id": org.String(), "assessment_id": assessment.String()})
				require.NoError(t, err, "encode legacy supervisor retained before the repair fix")
				recoveryJob = uuid.New()
				_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,dedupe_key,status,max_attempts) VALUES($1,$2,'agent','run_code_review_recheck',$3,$4,'pending',8)`, recoveryJob, org, payload, "code_review_recheck:"+assessment.String())
				require.NoError(t, err, "seed a supervisor queued before publication became protected")
			} else {
				require.NoError(t, pool.QueryRow(ctx, `SELECT id,payload FROM jobs WHERE org_id=$1 AND job_type='run_code_review_recheck' AND status='pending'`, org).Scan(&recoveryJob, &payload), "read the repair-generated supervisor job")
			}
			lifecycle := codereviewsvc.NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), codereviewsvc.Config{})
			lifecycle.SetScheduling(schedules, fullFallbackSnapshot{})
			stores := &Stores{CodeReviews: store, CodeReviewAssessments: db.NewCodeReviewAssessmentStore(pool), CodeReviewRechecks: db.NewCodeReviewRecheckStore(pool), ThreadSendTx: pool}
			capture := &fixedRecheckCapture{}
			handler := newRunCodeReviewRecheckHandler(stores, &Services{CodeReviewLifecycle: lifecycle, CodeReviewInputCapture: capture}, zerolog.Nop())
			before, err := stores.CodeReviewAssessments.GetByID(ctx, org, assessment)
			require.NoError(t, err, "read assessment before supervisor recovery")
			err = handler(ctx, "run_code_review_recheck", payload)
			if tt.protected {
				require.ErrorContains(t, err, "requires reconciliation", "publication uncertainty or receipt must prevent a replacement")
				require.ErrorContains(t, handler(ctx, "run_code_review_recheck", payload), "requires reconciliation", "legacy supervisor retry must preserve publication protection")
				_, err = pool.Exec(ctx, `UPDATE jobs SET status='dead_letter',attempts=max_attempts WHERE org_id=$1 AND id=$2`, org, recoveryJob)
				require.NoError(t, err, "exhaust the legacy supervisor after protected publication rejection")
				require.NoError(t, schedules.RepairMissingWakes(ctx), "repair must not resurrect the exhausted protected fallback")
				require.NoError(t, schedules.RepairMissingWakes(ctx), "repeated repair must remain quiet for protected fallback")
				var jobStatuses []string
				require.NoError(t, pool.QueryRow(ctx, `SELECT array_agg(status ORDER BY created_at,id) FROM jobs WHERE org_id=$1 AND job_type='run_code_review_recheck'`, org).Scan(&jobStatuses), "read all retained fallback supervisor jobs")
				require.Equal(t, []string{"dead_letter"}, jobStatuses, "only the exhausted legacy job may remain after repeated sweeps")
				after, err := stores.CodeReviewAssessments.GetByID(ctx, org, assessment)
				require.NoError(t, err, "read protected assessment after recovery attempts")
				require.Equal(t, before, after, "fallback rejection and repair must preserve the entire protected assessment")
				var requests int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_requests WHERE org_id=$1`, org).Scan(&requests), "count protected fallback requests")
				require.Equal(t, 0, requests, "protected publication must not enqueue a fresh review")
				return
			}
			require.NoError(t, err, "terminal full fallback must recover before the evidence-only scope check")
			require.NoError(t, handler(ctx, "run_code_review_recheck", payload), "queued full fallback replay should finish idempotently")
			got, err := stores.CodeReviewAssessments.GetByID(ctx, org, assessment)
			require.NoError(t, err, "read durable replacement marker")
			require.Equal(t, "full_review_queued:inputs changed before publication", *got.FailureDetail, "successful recovery must close the durable fallback marker")
			state, err := schedules.Get(ctx, org, pr)
			require.NoError(t, err, "read recovered scheduler intent")
			require.NotNil(t, state.PendingInput, "the replacement must remain durably scheduled")
			require.Nil(t, state.ActiveAssessmentID, "terminal full assessment must release active ownership")
			var requests int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_requests WHERE org_id=$1 AND pull_request_id=$2`, org, pr).Scan(&requests), "count idempotent fallback requests")
			require.Equal(t, 1, requests, "recovery and replay must share one replacement request")
			require.Equal(t, 0, capture.calls, "terminal full fallback cannot execute evidence capture")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND id=$2`, org, recoveryJob)
			require.NoError(t, err, "finish recovered supervisor job")
			require.NoError(t, schedules.RepairMissingWakes(ctx), "repeat the repair sweep after replacement admission")
			var pending int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='run_code_review_recheck' AND status='pending'`, org).Scan(&pending), "count replayed recovery wakes")
			require.Equal(t, 0, pending, "queued marker must prevent recurring fallback recovery")
		})
	}
}
