package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDisputeReassessmentTerminalProjectionPostgres(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Parallel tenant fixtures share a bounded pool; no observer/admin
	// connections remain open while the projection transactions execute.
	pool := testpostgres.NewPool(t, ctx, "../../migrations", 4)
	approved := models.CodeReviewDecisionApproved
	tests := []struct {
		name, fence    string
		status         models.CodeReviewSessionStatus
		expectedStatus models.CodeReviewDisputeReassessmentStatus
		published      bool
	}{
		{name: "stale owner", status: models.CodeReviewSessionStatusStale, expectedStatus: models.CodeReviewDisputeReassessmentSuperseded},
		{name: "stale owner keeps published reply", status: models.CodeReviewSessionStatusStale, expectedStatus: models.CodeReviewDisputeReassessmentSuperseded, published: true},
		{name: "failed owner", status: models.CodeReviewSessionStatusFailed, expectedStatus: models.CodeReviewDisputeReassessmentFailed},
		{name: "cancelled owner", status: models.CodeReviewSessionStatusCancelled, expectedStatus: models.CodeReviewDisputeReassessmentFailed},
		{name: "completed owner", status: models.CodeReviewSessionStatusCompleted, expectedStatus: models.CodeReviewDisputeReassessmentCompleted},
		{name: "queued owner stays active", status: models.CodeReviewSessionStatusQueued},
		{name: "running owner stays active", status: models.CodeReviewSessionStatusRunning},
		{name: "different tenant stays unchanged", status: models.CodeReviewSessionStatusStale, fence: "org"},
		{name: "different owner stays unchanged", status: models.CodeReviewSessionStatusStale, fence: "owner"},
		{name: "live capture handoff stays active", status: models.CodeReviewSessionStatusStale, fence: "handoff"},
		{name: "already settled dispute stays unchanged", status: models.CodeReviewSessionStatusStale, fence: "settled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedCaptureHandoffSettlement(t, pool)
			if tt.fence != "handoff" {
				_, err := pool.Exec(ctx, `DELETE FROM code_review_requests WHERE org_id=$1 AND id=$2`, f.orgID, f.requestID)
				require.NoError(t, err, "isolate an ordinary callback from captured-identity handoff protection")
			}
			if tt.published {
				_, err := pool.Exec(ctx, `UPDATE code_review_decision_disputes SET reply_status='published',reply_comment_id=41 WHERE org_id=$1 AND id=$2`, f.orgID, f.disputeID)
				require.NoError(t, err, "retain the existing provider reply receipt")
			}
			if tt.fence == "settled" {
				_, err := pool.Exec(ctx, `UPDATE code_review_decision_disputes SET reassessment_status='completed' WHERE org_id=$1 AND id=$2`, f.orgID, f.disputeID)
				require.NoError(t, err, "seed a terminal reassessment that a delayed callback cannot rewrite")
			}
			store := NewCodeReviewDisputeStore(pool)
			before, err := store.GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read every field before the terminal callback")
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin the isolated projection transaction")
			t.Cleanup(func() { require.NoError(t, tx.Rollback(ctx), "roll back the projection transaction after readback") })
			var transitionAt time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT now()`).Scan(&transitionAt), "capture the exact projection timestamp")
			queryOrgID, ownerID := f.orgID, f.sourceSessionID
			if tt.fence == "org" {
				queryOrgID = uuid.New()
			}
			if tt.fence == "owner" {
				ownerID = f.replacementSessionID
			}
			transitioned, err := NewCodeReviewDisputeStore(tx).CompleteReassessmentOnce(ctx, queryOrgID, f.disputeID, ownerID, tt.status, &approved, "  Synthetic terminal detail.  ")
			require.NoError(t, err, "project terminal status through the real tenant and owner fences")
			require.Equal(t, tt.expectedStatus != "", transitioned, "only the matching terminal owner without a live handoff may transition")
			after, err := NewCodeReviewDisputeStore(tx).GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read durable projected reassessment fields")
			expected := before
			if tt.expectedStatus != "" {
				expected.ReassessmentStatus = tt.expectedStatus
				expected.ReassessmentDecision, expected.ReassessmentFlipped = nil, nil
				if tt.status == models.CodeReviewSessionStatusCompleted {
					flipped := true
					expected.ReassessmentDecision, expected.ReassessmentFlipped = &approved, &flipped
				}
				detail := "Synthetic terminal detail."
				expected.StatusDetail = &detail
				if !tt.published {
					expected.ReplyStatus = models.CodeReviewDisputeReplyPending
				}
				expected.UpdatedAt, expected.Version = transitionAt, before.Version+1
			}
			require.Equal(t, expected, after, "projection must preserve exact owner, receipts and unrelated dispute fields without inventing a verdict")
			_, err = NewCodeReviewDisputeStore(tx).CompleteReassessmentOnce(ctx, queryOrgID, f.disputeID, ownerID, tt.status, &approved, "Repeated callback.")
			require.NoError(t, err, "the same callback may be delivered repeatedly")
			repeated, err := NewCodeReviewDisputeStore(tx).GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read reassessment after repeated callback")
			require.Equal(t, after, repeated, "repeat delivery must preserve all settled or protected fields")
		})
	}
}

func TestDisputeReassessmentSupersededMigrationPostgres(t *testing.T) {
	t.Parallel()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for dispute supersession migration proof")
	}
	tests := []struct {
		name, ownerStatus, fence, detail string
		initialStatus, expectedStatus    models.CodeReviewDisputeReassessmentStatus
	}{
		{name: "matching stale owner", ownerStatus: "stale", expectedStatus: models.CodeReviewDisputeReassessmentSuperseded},
		{name: "replacement start failure survives", ownerStatus: "stale", detail: "The reassessment was stopped before a replacement review could start. The objection remains recorded for a policy owner.", expectedStatus: models.CodeReviewDisputeReassessmentFailed},
		{name: "unrelated stale reason stays failed", ownerStatus: "stale", fence: "reason", expectedStatus: models.CodeReviewDisputeReassessmentFailed},
		{name: "failed owner stays failed", ownerStatus: "failed", expectedStatus: models.CodeReviewDisputeReassessmentFailed},
		{name: "cancelled owner stays failed", ownerStatus: "cancelled", expectedStatus: models.CodeReviewDisputeReassessmentFailed},
		{name: "other tenant owner stays failed", ownerStatus: "stale", fence: "org", expectedStatus: models.CodeReviewDisputeReassessmentFailed},
		{name: "other pull request owner stays failed", ownerStatus: "stale", fence: "pr", expectedStatus: models.CodeReviewDisputeReassessmentFailed},
		{name: "other repository owner stays failed", ownerStatus: "stale", fence: "repo", expectedStatus: models.CodeReviewDisputeReassessmentFailed},
		{name: "missing owner stays failed", ownerStatus: "stale", fence: "owner", expectedStatus: models.CodeReviewDisputeReassessmentFailed},
		{name: "queued reassessment stays queued", ownerStatus: "stale", initialStatus: models.CodeReviewDisputeReassessmentQueued, expectedStatus: models.CodeReviewDisputeReassessmentQueued},
		{name: "running reassessment stays running", ownerStatus: "stale", initialStatus: models.CodeReviewDisputeReassessmentRunning, expectedStatus: models.CodeReviewDisputeReassessmentRunning},
		{name: "completed reassessment stays completed", ownerStatus: "stale", initialStatus: models.CodeReviewDisputeReassessmentCompleted, expectedStatus: models.CodeReviewDisputeReassessmentCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			tx, orgID, disputeID := newDisputeTriagePostgres(t, databaseURL, models.CodeReviewDisputeIntakeTriaged)
			_, err := tx.Exec(ctx, `ALTER TABLE code_review_session_metadata ADD COLUMN session_id uuid, ADD COLUMN pull_request_id uuid, ADD COLUMN repository_id uuid, ADD COLUMN status text, ADD COLUMN failure_reason text`)
			require.NoError(t, err, "add only the owner identity fields read by the real migration")
			var prID, repoID uuid.UUID
			require.NoError(t, tx.QueryRow(ctx, `SELECT pull_request_id,repository_id FROM code_review_decision_disputes WHERE org_id=$1 AND id=$2`, orgID, disputeID).Scan(&prID, &repoID), "load the owning synthetic dispute identity")
			ownerID := uuid.New()
			_, err = tx.Exec(ctx, `INSERT INTO sessions(id,org_id) VALUES($1,$2)`, ownerID, orgID)
			require.NoError(t, err, "seed the referenced reassessment session")
			initialStatus := tt.initialStatus
			if initialStatus == "" {
				initialStatus = models.CodeReviewDisputeReassessmentFailed
			}
			_, err = tx.Exec(ctx, `UPDATE code_review_decision_disputes SET reassessment_status=$5,reassessment_session_id=$3,reassessment_decision='approved',reassessment_flipped=true,reply_status='published',reply_comment_id=41,status_detail=NULLIF($4,'') WHERE org_id=$1 AND id=$2`, orgID, disputeID, ownerID, tt.detail, initialStatus)
			require.NoError(t, err, "seed the historical failed row and immutable publication receipt")
			ownerOrgID, ownerPRID, ownerRepoID, metadataSessionID := orgID, prID, repoID, ownerID
			switch tt.fence {
			case "org":
				ownerOrgID = uuid.New()
			case "pr":
				ownerPRID = uuid.New()
			case "repo":
				ownerRepoID = uuid.New()
			case "owner":
				metadataSessionID = uuid.New()
			}
			reason := "review scheduling target superseded"
			if tt.fence == "reason" {
				reason = "Synthetic unrelated retirement reason."
			}
			_, err = tx.Exec(ctx, `INSERT INTO code_review_session_metadata(org_id,session_id,pull_request_id,repository_id,status,trigger_source,failure_reason) VALUES($1,$2,$3,$4,$5,'app_reviewer',$6)`, ownerOrgID, metadataSessionID, ownerPRID, ownerRepoID, tt.ownerStatus, reason)
			require.NoError(t, err, "seed a matching or explicitly unrelated terminal owner")
			store := NewCodeReviewDisputeStore(tx)
			before, err := store.GetByID(ctx, orgID, disputeID)
			require.NoError(t, err, "read the entire historical record before migration")
			up, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000345_dispute_reassessment_superseded.up.sql"))
			require.NoError(t, err, "read the production migration")
			_, err = tx.Exec(ctx, string(up))
			require.NoError(t, err, "apply the real status expansion and exact-owner backfill")
			after, err := store.GetByID(ctx, orgID, disputeID)
			require.NoError(t, err, "read the backfilled historical dispute")
			expected := before
			if tt.expectedStatus == models.CodeReviewDisputeReassessmentSuperseded {
				expected.ReassessmentStatus = tt.expectedStatus
				expected.ReassessmentDecision, expected.ReassessmentFlipped = nil, nil
				if tt.detail == "" {
					detail := "The reassessment was superseded because the review target changed. The objection remains recorded for a policy owner."
					expected.StatusDetail = &detail
				}
				expected.Version++
			}
			require.Equal(t, expected, after, "backfill must touch only exact stale owners and preserve every publication and unrelated field")
			down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000345_dispute_reassessment_superseded.down.sql"))
			require.NoError(t, err, "read the reversible status migration")
			_, err = tx.Exec(ctx, string(down))
			require.NoError(t, err, "rollback must translate superseded rows before restoring the old constraint")
			rolledBack, err := store.GetByID(ctx, orgID, disputeID)
			require.NoError(t, err, "read the status after rollback")
			if tt.expectedStatus == models.CodeReviewDisputeReassessmentSuperseded {
				expected.ReassessmentStatus = models.CodeReviewDisputeReassessmentFailed
				expected.Version++
			}
			require.Equal(t, expected, rolledBack, "rollback must preserve receipts, owner and retained explanation without inventing a verdict")
		})
	}
}
