package codereview

import (
	"context"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Called by the isolated, migrated lifecycle suite. Each table case has its
// own org and PR, so it cannot reuse another case's assessment or wake.
func testRefreshUnsentEvidenceAssessment(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, snapshot *schedulingSnapshotFixture, uncertain, intentChanged bool) {
	testAssessmentGenerationAdmission(t, pool, org, repo, pr, snapshot)
	ctx := context.Background()
	assessments := db.NewCodeReviewAssessmentStore(pool)
	before, err := db.NewCodeReviewScheduleStore(pool).GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "read admitted evidence assessment")
	require.Equal(t, models.CodeReviewScopeEvidenceOnly, before.ReviewScope, "fixture should have an evidence assessment")
	manifest, err := decodeAssessmentManifest(before.InputManifest)
	require.NoError(t, err, "decode immutable evidence input")
	input := ReviewInputCapture{Code: manifest.Code, Contract: manifest.Contract, Title: manifest.Title, Description: manifest.Description,
		Visual: manifest.Visual, TextEvidence: manifest.TextEvidence, Request: manifest.Request, Gates: manifest.Gates}
	input.Visual.Images = []ReviewVisualImage{{SourceID: "later-image", SourceURL: "https://example.test/later.png", ContentDigest: strings.Repeat("d", 64)}}
	if intentChanged {
		input.Description = "A materially different purpose\n"
		for i := range input.TextEvidence.Items {
			if input.TextEvidence.Items[i].Surface == "pull_request_description" && input.TextEvidence.Items[i].Section == "full" {
				input.TextEvidence.Items[i].Content = input.Description
				input.TextEvidence.Items[i].ContentDigest = digestBytes(input.Description)
			}
		}
		snapshot.update(func(s *ghservice.CodeReviewPullRequestSnapshot) { s.Body = input.Description })
	}
	changed, err := BuildReviewInputManifest(input)
	require.NoError(t, err, "build changed evidence input")
	store := db.NewCodeReviewStore(pool)
	policy, err := store.ResolvePolicy(ctx, org)
	require.NoError(t, err, "resolve admission policy")
	require.NotNil(t, policy.Policy, "fixture needs a saved policy record")
	service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
	service.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
	service.SetAssessmentContinuation(assessmentAdmissionFixture{pool: pool, manifest: changed, policy: *policy.Policy, session: before.SessionID, snapshot: snapshot.snapshot}, true)
	if uncertain {
		_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='publishing',publication_state='uncertain' WHERE org_id=$1 AND id=$2`, org, before.ID)
		require.NoError(t, err, "mark publication attempt uncertain")
		err = service.RefreshUnsentEvidenceAssessment(ctx, org, before.ID, "evidence changed")
		require.ErrorIs(t, err, db.ErrCodeReviewAssessmentState, "uncertain external publication cannot be retired as unsent")
		after, err := assessments.GetByID(ctx, org, before.ID)
		require.NoError(t, err, "read protected assessment")
		require.Equal(t, models.CodeReviewAssessmentPublishing, after.Status, "uncertain assessment must remain publishing")
		return
	}
	require.NoError(t, service.RefreshUnsentEvidenceAssessment(ctx, org, before.ID, "evidence changed"), "unsent evidence turn should retire and request a fresh assessment")
	old, err := assessments.GetByID(ctx, org, before.ID)
	require.NoError(t, err, "read retired evidence assessment")
	require.Equal(t, models.CodeReviewAssessmentSuperseded, old.Status, "old unsent assessment should be superseded")
	require.Equal(t, "evidence_recheck_queued:evidence changed", *old.FailureDetail, "durable marker should close after replacement admission")
	after, err := db.NewCodeReviewScheduleStore(pool).GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "read replacement assessment")
	require.NotEqual(t, before.ID, after.ID, "refresh should allocate a new immutable assessment")
	require.Equal(t, models.CodeReviewScopeEvidenceOnly, after.ReviewScope, "evidence change should use a narrow turn")
	require.Equal(t, changed.InputDigest, after.InputDigest, "replacement must bind newly captured evidence")
	require.NoError(t, service.RefreshUnsentEvidenceAssessment(ctx, org, before.ID, "evidence changed"), "same refresh should replay idempotently")
	again, err := db.NewCodeReviewScheduleStore(pool).GetLatestAssessment(ctx, org, pr)
	require.NoError(t, err, "read replay result")
	require.Equal(t, after.ID, again.ID, "replay must not create another evidence assessment")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='failed' WHERE org_id=$1 AND dedupe_key=$2`, org, "code_review_recheck:"+before.ID.String())
	require.NoError(t, err, "make retired supervisor terminal before sweep")
	require.NoError(t, db.NewCodeReviewScheduleStore(pool).RepairMissingWakes(ctx), "repair should ignore replacement that was durably queued")
	var restored int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$2 AND status='pending'`, org, "code_review_recheck:"+before.ID.String()).Scan(&restored), "read retired supervisor wake count")
	require.Equal(t, 0, restored, "queued marker must not reawaken the obsolete assessment")
}
