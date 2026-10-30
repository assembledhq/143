package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// The hook runs at the deterministic dependency boundary AFTER the actual
// worker's outside lookup and BEFORE the existing real Run begins. Fixture
// writes are ordinary guarded PostgreSQL statements, not application SQL
// answers or fabricated transaction results. This is not network/process
// concurrency, a lost-ACK test, or evidence of independent writer scheduling.
type assessmentOwnerRecheckBoundary struct {
	base   db.CodeReviewMetadataTransactions
	before func(context.Context)
	calls  int
	writes []string
}

func (r *assessmentOwnerRecheckBoundary) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	r.calls++
	if r.calls == 1 && r.before != nil {
		r.before(ctx)
	}
	return r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		return work(unitCtx, &assessmentOwnerRecheckTx{Tx: tx, writes: &r.writes})
	})
}

// Forward every statement unchanged. Recording attempted application DML is
// necessary: a later owner-claim refusal could otherwise roll back an already
// attempted Link/child mutation and falsely look like early scope rejection.
type assessmentOwnerRecheckTx struct {
	pgx.Tx
	writes *[]string
}

func (tx *assessmentOwnerRecheckTx) record(query string) {
	upper := strings.ToUpper(strings.TrimSpace(query))
	if strings.HasPrefix(upper, "UPDATE ") || strings.HasPrefix(upper, "INSERT ") || strings.HasPrefix(upper, "DELETE ") ||
		strings.HasPrefix(upper, "WITH ") && (strings.Contains(upper, "UPDATE CODE_REVIEW_") || strings.Contains(upper, "INSERT INTO CODE_REVIEW_") || strings.Contains(upper, "DELETE FROM CODE_REVIEW_")) {
		*tx.writes = append(*tx.writes, query)
	}
}
func (tx *assessmentOwnerRecheckTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.record(query)
	return tx.Tx.Exec(ctx, query, args...)
}
func (tx *assessmentOwnerRecheckTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.record(query)
	return tx.Tx.Query(ctx, query, args...)
}
func (tx *assessmentOwnerRecheckTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	tx.record(query)
	return tx.Tx.QueryRow(ctx, query, args...)
}

type assessmentOwnerRecheckGraph struct {
	original fullAssessmentDispatchGraph
	domain   json.RawMessage
}

func assessmentOwnerRecheckSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture) assessmentOwnerRecheckGraph {
	t.Helper()
	g := assessmentOwnerRecheckGraph{original: fullAssessmentDispatchSnapshot(t, ctx, pool, f)}
	require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_object(
		'metadata',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM code_review_session_metadata m WHERE m.org_id=$1 AND m.session_id=$2),
		'assessments',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM code_review_revision_assessments a WHERE a.org_id=$1 AND a.session_id=$2),
		'pull_requests',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM pull_requests p WHERE p.org_id=$1),
		'scheduler',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM code_review_pr_state s WHERE s.org_id=$1))`, f.job.OrgID, f.job.SessionID).Scan(&g.domain),
		"snapshot both actual metadata/assessment associations and every original/sibling PR domain without omitting business fields")
	return g
}

func assessmentOwnerRecheckMetadata(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, id, pr uuid.UUID, key string) {
	t.Helper()
	_, err := pool.Exec(ctx, `INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,
		base_sha,head_sha,trigger_source,status,decision,acceptable,review_output_key,github_review_id,github_review_url,final_review_body,completed_at)
		SELECT $3,org_id,session_id,repository_id,$4,policy_id,base_sha,head_sha,trigger_source,'completed',decision,acceptable,
		$5,github_review_id,github_review_url,final_review_body,completed_at
		FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID, id, pr, key)
	require.NoError(t, err, "ordinary fixture insertion must satisfy every actual tenant/session/repository/PR/policy FK for the sibling completed metadata")
}

func assessmentOwnerRecheckAssessment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, original models.CodeReviewAssessment, id, metadata, pr uuid.UUID, key string) {
	t.Helper()
	receipt, err := fullAssessmentPublicationReceipt(id, original.InputDigest, original.HeadSHA, original.GitHubReviewID, original.GitHubReviewURL, nil, nil)
	require.NoError(t, err, "retain a correctly self-bound original publication receipt for the separate legal completed assessment")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,
		pull_request_id,metadata_id,session_id,policy_id,generation,base_sha,base_ref,head_sha,input_version,
		code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,
		review_scope,route_reason,result_origin,coverage_complete,status,decision,acceptable,risk_reason_details,
		structured_outcome,rendered_body,publication_key,publication_state,publication_receipt,github_review_id,
		github_review_url,submitted_commit_sha,created_at,completed_at)
		SELECT $3,org_id,repository_id,repository_full_name,$5,$4,session_id,policy_id,generation+1,
		base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,
		gate_digest,input_digest,input_manifest,review_scope,route_reason,result_origin,coverage_complete,'completed',
		decision,acceptable,risk_reason_details,structured_outcome,rendered_body,$6,publication_state,$7::jsonb,
		github_review_id,github_review_url,submitted_commit_sha,created_at+interval '1 minute',
		COALESCE(completed_at,created_at)+interval '1 minute'
		FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, original.ID, id, metadata, pr, key, []byte(receipt))
	require.NoError(t, err, "insert a deterministic latest full completed assessment using real composite FKs and finite legal retained history, not a mocked lookup")
}

func TestFullAssessmentCompletionOwnerRecheckedInTransactionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, route string
		status      models.CodeReviewAssessmentStatus
	}{
		{"completed owner rebound after lookup", "already completed", models.CodeReviewAssessmentCompleted},
		{"publishing owner rebound after lookup", "reconcile", models.CodeReviewAssessmentPublishing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Every deterministic boundary schedule owns a full-schema database and at most two pool slots.
			pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 2)
			f, _ := fullAssessmentDispatchFixture(t, pool, tt.route, true)
			ctx := f.ctx()
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "retain the independently supplied completed metadata before actual reconciliation")
			original, err := f.stores.CodeReviewAssessments.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "the outside real lookup must initially select an exact valid association")
			require.Equal(t, []uuid.UUID{f.assessment, f.job.OrgID, f.job.SessionID, f.job.MetadataID, f.job.PullRequestID},
				[]uuid.UUID{original.ID, original.OrgID, original.SessionID, original.MetadataID, original.PullRequestID}, "the pre-boundary lookup must match the complete independently retained owner rather than fail the outer guard")
			require.Equal(t, models.CodeReviewScopeFull, original.ReviewScope, "the initial real association must be a full assessment")
			require.Equal(t, tt.status, original.Status, "each schedule must reach the intended actual worker reconciliation branch")
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve one checkout so both fixture boundary writes and actual owner work have only one remaining connection")
			t.Cleanup(reserved.Release)
			actualRunner := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			boundary := &assessmentOwnerRecheckBoundary{base: actualRunner, writes: []string{}}
			var expected assessmentOwnerRecheckGraph
			hookCompleted := false
			boundary.before = func(boundaryCtx context.Context) {
				sibling, decoy := uuid.New(), uuid.New()
				assessmentOwnerRecheckMetadata(t, boundaryCtx, pool, f, sibling, f.job.PullRequestID, "synthetic-rebound:"+sibling.String())
				tag, err := pool.Exec(boundaryCtx, `UPDATE code_review_revision_assessments SET metadata_id=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, original.ID, sibling)
				require.NoError(t, err, "the actual retained assessment must be legally rebound through ordinary SQL under all original guards")
				require.Equal(t, int64(1), tag.RowsAffected(), "the boundary must actually change the retained row, not rely on a failed/no-op fixture write")
				changed, err := f.stores.CodeReviewAssessments.GetByID(boundaryCtx, f.job.OrgID, original.ID)
				require.NoError(t, err, "read back the actual rebound retained identity before delegating to the real runner")
				want := original
				want.MetadataID = sibling
				require.Equal(t, want, changed, "the fixture changes only the legal metadata association; original status, coverage, publication and immutable business tuple remain exact")
				// A newer valid match must not be adopted to repair the old ID.
				// In the publishing schedule it is completed to respect the actual
				// one-active-assessment-per-PR index, not to forge another publisher.
				assessmentOwnerRecheckAssessment(t, boundaryCtx, pool, f, original, decoy, f.job.MetadataID, f.job.PullRequestID, "synthetic-decoy:"+decoy.String())
				latest, err := f.stores.CodeReviewAssessments.GetBySessionID(boundaryCtx, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "verify the tempting newer matching association using the actual latest-full lookup")
				require.Equal(t, []uuid.UUID{decoy, f.job.MetadataID}, []uuid.UUID{latest.ID, latest.MetadataID}, "the latest matching decoy must be real, distinct from the frozen original assessment ID")
				expected = assessmentOwnerRecheckSnapshot(t, boundaryCtx, pool, f)
				hookCompleted = true
			}
			f.stores.ThreadSendDependencies.Transactions = boundary
			err = reconcileCompletedFullAssessment(ctx, f.stores, metadata)
			require.True(t, hookCompleted, "the exact-association outside guard must pass and the real SQL boundary mutation must finish before inner revalidation is tested")
			require.Equal(t, 1, boundary.calls, "actual reconciliation must enter its supplied Run boundary exactly once after outside lookup")
			fullAssessmentDispatchAssertRun(t, actualRunner)
			require.Equal(t, []string{}, boundary.writes, "the in-unit association mismatch must reject BEFORE any actual Link, pending-child or business DML attempt, not merely roll those writes back later")
			require.Equal(t, expected, assessmentOwnerRecheckSnapshot(t, ctx, pool, f), "after the deliberate boundary fixture, inner refusal must leave the complete original+sibling assessment, coverage, metadata and caller graph exact")
			require.ErrorIs(t, err, db.ErrCodeReviewAssessmentState, "actual bounded in-unit revalidation must reject the rebound fixed assessment ID, not rediscover or switch to the valid latest decoy")
		})
	}
}

func TestFullAssessmentCompletionRejectsDifferentPRDomainPostgres(t *testing.T) {
	t.Parallel() // This exact outer-guard regression owns an isolated two-checkout database.
	pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 2)
	f, _ := fullAssessmentDispatchFixture(t, pool, "already completed", true)
	ctx := f.ctx()
	metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "retain the original completed metadata before another legal PR domain reuses the session")
	original, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "retain original assessment provenance before introducing a distinct PR")
	pr, sibling, assessment := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha)
		VALUES($1,$2,10,'https://example.invalid/pr/10','test/saved-review','Synthetic sibling PR','head','base')`, pr, f.job.OrgID)
	require.NoError(t, err, "create a real distinct same-tenant PR rather than forging an inconsistent assessment foreign key")
	key := "synthetic-other-pr:" + assessment.String()
	assessmentOwnerRecheckMetadata(t, ctx, pool, f, sibling, pr, key)
	assessmentOwnerRecheckAssessment(t, ctx, pool, f, original, assessment, sibling, pr, key)
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET assessment_id=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, sibling, assessment)
	require.NoError(t, err, "link the sibling metadata only to its own FK-valid completed assessment")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id,head_sha,base_sha,base_ref,current_assessment_id,state)
		VALUES($1,$2,$3,'head','base','main',$4,'covered')`, f.job.OrgID, f.job.RepositoryID, pr, assessment)
	require.NoError(t, err, "retain an actual independent scheduler domain for the sibling PR")
	latest, err := f.stores.CodeReviewAssessments.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "reach the actual latest full completed assessment in the other PR domain")
	require.Equal(t, []uuid.UUID{assessment, sibling, pr}, []uuid.UUID{latest.ID, latest.MetadataID, latest.PullRequestID}, "the regression must exercise legal distinct metadata and PR associations in the same session/repository")
	expected := assessmentOwnerRecheckSnapshot(t, ctx, pool, f)
	actualRunner := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
	boundary := &assessmentOwnerRecheckBoundary{base: actualRunner, writes: []string{}}
	f.stores.ThreadSendDependencies.Transactions = boundary
	err = reconcileCompletedFullAssessment(ctx, f.stores, metadata)
	require.Equal(t, 0, boundary.calls, "a latest assessment in another PR domain must be rejected BEFORE Run can take the wrong PR lock or authorize any cleanup")
	require.Equal(t, 0, actualRunner.calls, "a failed exact outside association guard must not acquire a transaction in either PR domain")
	require.Equal(t, expected, assessmentOwnerRecheckSnapshot(t, ctx, pool, f), "the original and sibling PR, scheduler, metadata, assessment and pending/business graphs must all remain unchanged")
	require.ErrorIs(t, err, db.ErrCodeReviewAssessmentState, "same-session reuse must not substitute the latest different-PR assessment for the independently retained completed metadata")
}
