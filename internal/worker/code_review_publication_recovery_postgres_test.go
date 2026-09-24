package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

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
	schema := "full_recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated recovery schema")
	t.Cleanup(func() {
		_, cleanupErr := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "drop isolated recovery schema")
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
	for _, path := range migrations {
		up, readErr := os.ReadFile(path)
		require.NoError(t, readErr, "read migration")
		_, applyErr := pool.Exec(ctx, string(up))
		require.NoError(t, applyErr, "apply migration "+filepath.Base(path))
	}
	return pool
}
