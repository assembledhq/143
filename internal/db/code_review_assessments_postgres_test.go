package db

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// The actual migration is applied to a disposable schema. It proves both the
// composite relationships and the store's immutable/idempotent write contract.
func TestCodeReviewAssessmentsPostgres(t *testing.T) {
	t.Parallel()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for assessment migration and FK proof")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err, "connect disposable PostgreSQL")
	defer func() { require.NoError(t, conn.Close(ctx), "close test database") }()
	schema := "review_assessment_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated schema")
	defer func() {
		_, err := conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "remove isolated schema")
	}()
	_, err = conn.Exec(ctx, `SET search_path TO `+schema+`,public`)
	require.NoError(t, err, "select isolated schema")
	_, err = conn.Exec(ctx, `
 CREATE TABLE organizations(id uuid PRIMARY KEY);
 CREATE TABLE repositories(id uuid PRIMARY KEY,org_id uuid NOT NULL,full_name text NOT NULL);
 CREATE TABLE pull_requests(id uuid PRIMARY KEY,org_id uuid NOT NULL,github_repo text NOT NULL);
 CREATE TABLE code_review_policies(id uuid PRIMARY KEY,org_id uuid NOT NULL,repository_id uuid);
 CREATE TABLE code_review_session_metadata(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,repository_id uuid NOT NULL,pull_request_id uuid NOT NULL,policy_id uuid NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
 CREATE TABLE code_review_agent_results(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
 CREATE TABLE code_review_findings(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,dedupe_key text NOT NULL);
 CREATE UNIQUE INDEX idx_code_review_findings_dedupe ON code_review_findings(org_id,session_id,dedupe_key);
 CREATE TABLE code_review_prompt_records(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,record_key text NOT NULL DEFAULT '',role text NOT NULL DEFAULT '',metadata jsonb NOT NULL DEFAULT '{}'::jsonb,created_at timestamptz NOT NULL DEFAULT now());`)
	require.NoError(t, err, "create existing table shapes")
	up, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000293_code_review_assessments.up.sql"))
	require.NoError(t, err, "read assessment migration")
	_, err = conn.Exec(ctx, string(up))
	require.NoError(t, err, "apply actual assessment migration")
	org, otherOrg, repo, otherRepo, pr, session, policy := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seed := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations VALUES($1),($2)`, []any{org, otherOrg}},
		{`INSERT INTO repositories VALUES($1,$3,'test/repo'),($2,$3,'other/repo')`, []any{repo, otherRepo, org}},
		{`INSERT INTO pull_requests VALUES($1,$2,'test/repo')`, []any{pr, org}},
		{`INSERT INTO code_review_policies VALUES($1,$2,NULL)`, []any{policy, org}},
		{`INSERT INTO code_review_session_metadata VALUES($1,$2,$3,$4,$5,$6,now(),NULL)`, []any{uuid.New(), org, session, repo, pr, policy}},
	}
	for _, row := range seed {
		_, err = conn.Exec(ctx, row.sql, row.args...)
		require.NoError(t, err, "seed matching org, repo, PR, policy and metadata")
	}
	capture := testAssessmentCapture()
	capture.OrgID, capture.RepositoryID, capture.PullRequestID, capture.PolicyID, capture.SessionID = org, repo, pr, policy, session
	store := NewCodeReviewAssessmentStore(conn)
	a, reused, err := store.Create(ctx, capture)
	require.NoError(t, err, "create full baseline assessment")
	require.False(t, reused, "first capture should insert")
	require.Equal(t, models.CodeReviewAssessmentReserved, a.Status, "new capture should await dispatch")
	a, reused, err = store.Create(ctx, capture)
	require.NoError(t, err, "same capture should be idempotent")
	require.True(t, reused, "same immutable capture should reuse its row")
	capture.VisualDigest = "changed"
	_, _, err = store.Create(ctx, capture)
	require.ErrorIs(t, err, ErrCodeReviewAssessmentConflict, "same identity must reject changed evidence")
	capture.VisualDigest = "visual"
	wrong := capture
	wrong.ID = uuid.New()
	wrong.Generation = 2
	wrong.PublicationKey = "other-key"
	wrong.RepositoryID = otherRepo
	_, _, err = store.Create(ctx, wrong)
	require.ErrorIs(t, err, pgx.ErrNoRows, "repository mismatch must not capture assessment")
	wrong.RepositoryID = repo
	wrong.SessionID = uuid.New()
	_, _, err = store.Create(ctx, wrong)
	require.ErrorIs(t, err, pgx.ErrNoRows, "session mismatch must not capture assessment")
	wrong.SessionID = session
	wrong.PolicyID = uuid.New()
	_, _, err = store.Create(ctx, wrong)
	require.ErrorIs(t, err, pgx.ErrNoRows, "policy mismatch must not capture assessment")
	wrong.PolicyID = policy
	wrong.OrgID = otherOrg
	_, _, err = store.Create(ctx, wrong)
	require.ErrorIs(t, err, pgx.ErrNoRows, "tenant mismatch must not capture assessment")
	_, err = conn.Exec(ctx, `UPDATE code_review_revision_assessments SET repository_id=$2 WHERE id=$1`, a.ID, otherRepo)
	require.True(t, pgConstraint(err, "23503"), "composite PR/repository FK must reject mismatched repository")
	_, err = conn.Exec(ctx, `UPDATE code_review_revision_assessments SET metadata_id=$2 WHERE id=$1`, a.ID, uuid.New())
	require.True(t, pgConstraint(err, "23503"), "composite metadata FK must reject mismatched session relationship")
	foreignSession := uuid.New()
	resultID, findingID, promptID := uuid.New(), uuid.New(), uuid.New()
	_, err = conn.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,assessment_id) VALUES($1,$2,$3,NULL)`, resultID, org, foreignSession)
	require.NoError(t, err, "legacy result with no assessment remains valid")
	_, err = conn.Exec(ctx, `UPDATE code_review_agent_results SET assessment_id=$2 WHERE id=$1`, resultID, a.ID)
	require.True(t, pgConstraint(err, "23503"), "result cannot attach an assessment from another session")
	_, err = conn.Exec(ctx, `INSERT INTO code_review_findings(id,org_id,session_id,dedupe_key,assessment_id) VALUES($1,$2,$3,'legacy',NULL)`, findingID, org, foreignSession)
	require.NoError(t, err, "legacy finding with no assessment remains valid")
	_, err = conn.Exec(ctx, `UPDATE code_review_findings SET assessment_id=$2 WHERE id=$1`, findingID, a.ID)
	require.True(t, pgConstraint(err, "23503"), "finding cannot attach an assessment from another session")
	_, err = conn.Exec(ctx, `INSERT INTO code_review_prompt_records(id,org_id,session_id,assessment_id) VALUES($1,$2,$3,NULL)`, promptID, org, foreignSession)
	require.NoError(t, err, "legacy prompt with no assessment remains valid")
	_, err = conn.Exec(ctx, `UPDATE code_review_prompt_records SET assessment_id=$2 WHERE id=$1`, promptID, a.ID)
	require.True(t, pgConstraint(err, "23503"), "prompt cannot attach an assessment from another session")
	require.NoError(t, store.MarkRunning(ctx, org, a.ID, 1, "all"), "reserved assessment should become running")
	require.ErrorIs(t, store.MarkRunning(ctx, org, a.ID, 1, "all"), ErrCodeReviewAssessmentState, "duplicate start must be fenced")
	_, err = conn.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,assessment_id) VALUES($1,$2,$3,NULL)`, uuid.New(), org, session)
	require.NoError(t, err, "seed full-review result before assessment link")
	_, err = conn.Exec(ctx, `INSERT INTO code_review_findings(id,org_id,session_id,dedupe_key,assessment_id) VALUES($1,$2,$3,'full-finding',NULL)`, uuid.New(), org, session)
	require.NoError(t, err, "seed full-review finding before assessment link")
	_, err = conn.Exec(ctx, `INSERT INTO code_review_prompt_records(id,org_id,session_id,assessment_id) VALUES($1,$2,$3,NULL)`, uuid.New(), org, session)
	require.NoError(t, err, "seed full-review prompt before assessment link")
	foreignCaptureID := uuid.New()
	_, err = conn.Exec(ctx, `INSERT INTO code_review_prompt_records(id,org_id,session_id,record_key,role,metadata) VALUES($1,$2,$3,$4,'visual_evidence',$5)`, uuid.New(), org, session, "code-review-prompts/"+session.String()+"/assessments/"+foreignCaptureID.String()+"/head/visual-evidence-v1", json.RawMessage(`{"assessment_id":"`+foreignCaptureID.String()+`"}`))
	require.NoError(t, err, "seed a candidate visual checkpoint from another assessment")
	require.NoError(t, store.LinkFullReviewEvidence(ctx, org, a.ID, session), "link complete full session evidence")
	for _, table := range []string{"code_review_session_metadata", "code_review_agent_results", "code_review_findings", "code_review_prompt_records"} {
		var count int
		err = conn.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE org_id=$1 AND session_id=$2 AND assessment_id=$3`, org, session, a.ID).Scan(&count)
		require.NoError(t, err, "read linked full-review evidence")
		require.Equal(t, 1, count, "one new full-session row should be linked to its assessment")
	}
	var wronglyLinked int
	err = conn.QueryRow(ctx, `SELECT count(*) FROM code_review_prompt_records WHERE org_id=$1 AND session_id=$2 AND metadata->>'assessment_id'=$3 AND assessment_id IS NOT NULL`, org, session, foreignCaptureID.String()).Scan(&wronglyLinked)
	require.NoError(t, err, "read other assessment checkpoint link")
	require.Equal(t, 0, wronglyLinked, "full evidence link must not capture another assessment's prompt")
	visualKey := "code-review-prompts/" + session.String() + "/assessments/" + a.ID.String() + "/head/visual-evidence-v1"
	_, err = conn.Exec(ctx, `INSERT INTO code_review_prompt_records(id,org_id,session_id,record_key,role,metadata) VALUES($1,$2,$3,$4,'visual_evidence',$5)`, uuid.New(), org, session, visualKey, json.RawMessage(`{"assessment_id":"`+a.ID.String()+`"}`))
	require.NoError(t, err, "seed matching assessment visual checkpoint")
	require.NoError(t, store.LinkVisualEvidence(ctx, org, a.ID), "link matching visual checkpoint")
	require.NoError(t, store.LinkVisualEvidence(ctx, org, a.ID), "matching checkpoint link should be idempotent")
	result := models.CodeReviewAssessmentCompletion{ResultOrigin: models.CodeReviewResultExecuted, CoverageComplete: true, Decision: models.CodeReviewDecisionBlocked, Acceptable: false, StructuredOutcome: json.RawMessage(`{"reason":"visual_missing"}`), RenderedBody: "body"}
	require.NoError(t, store.StageOutcome(ctx, org, a.ID, 1, "all", result), "stage decision before publication")
	require.NoError(t, store.StageOutcome(ctx, org, a.ID, 1, "all", result), "identical stage retry should reuse committed decision")
	changedResult := result
	changedResult.RenderedBody = "different body"
	require.ErrorIs(t, store.StageOutcome(ctx, org, a.ID, 1, "all", changedResult), ErrCodeReviewAssessmentConflict, "staged outcome is immutable")
	require.NoError(t, store.MarkPublicationNotRequired(ctx, org, a.ID, 1, "all"), "no-publication decision can be finalized")
	require.NoError(t, store.Complete(ctx, org, a.ID, 1, "all", result), "complete assessment")
	require.ErrorIs(t, store.Fail(ctx, org, a.ID, 1, "all", "later failure"), ErrCodeReviewAssessmentState, "terminal assessment must not be overwritten")
	require.ErrorIs(t, store.Complete(ctx, org, a.ID, 1, "all", result), ErrCodeReviewAssessmentState, "terminal result must remain immutable")
	got, err := store.GetBySessionID(ctx, org, session)
	require.NoError(t, err, "read full baseline by session")
	require.Equal(t, a.ID, got.ID, "session lookup should find full baseline")
	got, err = store.GetLatestForPR(ctx, org, pr)
	require.NoError(t, err, "read latest PR assessment")
	require.Equal(t, a.ID, got.ID, "latest PR assessment should match captured baseline")
	recheck := capture
	recheck.ID = uuid.New()
	recheck.Generation = 2
	recheck.ReviewScope = models.CodeReviewScopeEvidenceOnly
	recheck.SourceAssessmentID = &a.ID
	recheck.PreviousAssessmentID = &a.ID
	recheck.PreviousPublishedAssessmentID = nil // The full baseline had no GitHub publication.
	recheck.VisualDigest = "new-visual"
	recheck.InputDigest = "new-all"
	recheck.InputManifest = json.RawMessage(`{"version":1,"visual":"new"}`)
	recheck.RouteReason = "visual_changed"
	recheck.PublicationKey = "assessment:" + recheck.ID.String()
	check, reused, err := store.Create(ctx, recheck)
	require.NoError(t, err, "complete full baseline can support an evidence-only assessment")
	require.False(t, reused, "new evidence creates a distinct assessment")
	require.Equal(t, &a.ID, check.SourceAssessmentID, "new assessment retains direct code baseline")
	got, err = store.GetBySessionID(ctx, org, session)
	require.NoError(t, err, "read session baseline after recheck")
	require.Equal(t, a.ID, got.ID, "session baseline should not change to evidence-only recheck")
	got, err = store.GetCurrentForPR(ctx, org, pr)
	require.NoError(t, err, "read current assessment")
	require.Equal(t, check.ID, got.ID, "current PR assessment should be new evidence capture")
	current, active, failed, err := store.ListCurrentForPRs(ctx, org, []uuid.UUID{pr, uuid.New()})
	require.NoError(t, err, "read batched assessment summaries")
	require.Equal(t, a.ID, current[pr].ID, "current summary should retain the completed full baseline")
	require.Equal(t, check.ID, active[pr].ID, "active summary should identify the reserved recheck")
	require.Equal(t, 1, len(current), "unrelated PR should have no completed assessment")
	require.Equal(t, 1, len(active), "unrelated PR should have no active assessment")
	require.Empty(t, failed, "no failed assessment should be projected before a failure")
	foreignCurrent, foreignActive, foreignFailed, err := store.ListCurrentForPRs(ctx, otherOrg, []uuid.UUID{pr})
	require.NoError(t, err, "read summaries for another organization")
	require.Empty(t, foreignCurrent, "another tenant must not see completed assessment")
	require.Empty(t, foreignActive, "another tenant must not see active assessment")
	require.Empty(t, foreignFailed, "another tenant must not see failed assessment")
	wrongSource := recheck
	wrongSource.ID = uuid.New()
	wrongSource.Generation = 3
	wrongSource.PublicationKey = "different"
	wrongSource.HeadSHA = "changed-code"
	_, _, err = store.Create(ctx, wrongSource)
	require.ErrorIs(t, err, ErrCodeReviewAssessmentConflict, "evidence-only assessment cannot reuse code coverage from a different head")
	require.NoError(t, store.MarkRunning(ctx, org, check.ID, check.Generation, check.InputDigest), "new assessment should enter running state")
	require.NoError(t, store.StageOutcome(ctx, org, check.ID, check.Generation, check.InputDigest, result), "new assessment should stage an immutable outcome")
	require.NoError(t, store.ReservePublication(ctx, org, check.ID, check.Generation, check.InputDigest, check.HeadSHA), "new assessment should reserve publication")
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "begin isolated unsent supersede proof")
	txStore := NewCodeReviewAssessmentStore(tx)
	require.NoError(t, txStore.SupersedeUnsentPublication(ctx, org, check.ID, check.Generation, check.InputDigest, "full_review:inputs changed"), "reserved publication can be superseded before any send")
	unsent, err := txStore.GetByID(ctx, org, check.ID)
	require.NoError(t, err, "read unsent terminal assessment")
	require.Equal(t, models.CodeReviewAssessmentSuperseded, unsent.Status, "unsent publication should be terminal")
	require.Equal(t, result.RenderedBody, *unsent.RenderedBody, "supersede must preserve staged outcome")
	require.NoError(t, tx.Rollback(ctx), "restore reserved state for attempted-send proof")
	require.NoError(t, store.MarkPublicationAttemptUncertain(ctx, org, check.ID, check.Generation, check.InputDigest), "send attempt must durably leave reserved state")
	require.ErrorIs(t, store.SupersedeUnsentPublication(ctx, org, check.ID, check.Generation, check.InputDigest, "full_review:inputs changed"), ErrCodeReviewAssessmentState, "uncertain publication cannot be assumed unsent")
}

func pgConstraint(err error, code string) bool {
	if e, ok := err.(*pgconn.PgError); ok {
		return e.Code == code
	}
	return false
}
