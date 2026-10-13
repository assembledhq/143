package db

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewWorkspaceSupersessionFencePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, setup string
		foreignOrg  bool
		stale       bool
	}{
		{name: "current moved-ref preparation", stale: true},
		{name: "newer workspace generation", setup: "UPDATE sessions SET workspace_generation=1"},
		{name: "changed metadata head", setup: "UPDATE code_review_session_metadata SET head_sha='new-head'"},
		{name: "already cancelled metadata", setup: "UPDATE code_review_session_metadata SET status='cancelled'"},
		{name: "other tenant", foreignOrg: true},
		{name: "uncertain publication", setup: "INSERT INTO code_review_revision_assessments SELECT org_id,id,session_id,'uncertain',NULL,NULL,NULL FROM code_review_session_metadata"},
		{name: "confirmed receipt", setup: "INSERT INTO code_review_revision_assessments SELECT org_id,id,session_id,'not_started','{}',NULL,NULL FROM code_review_session_metadata"},
		{name: "staged reusable result", setup: "INSERT INTO code_review_revision_assessments SELECT org_id,id,session_id,'not_started',NULL,NULL,'full_review' FROM code_review_session_metadata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			pr := extendWorkspaceFailureFixture(t, f)
			if tt.setup != "" {
				_, err := f.pool.Exec(context.Background(), tt.setup)
				require.NoError(t, err, "seed competing generation, revision or protected publication")
			}
			org := f.org
			if tt.foreignOrg {
				org = uuid.New()
			}
			reason := "PR head moved during workspace preparation; live revision was not reconciled within its original budget."
			actual, err := NewCodeReviewStore(f.pool).SupersedeCodeReviewWorkspacePreparation(context.Background(), org, SupersedeCodeReviewWorkspacePreparationParams{ReviewID: f.review, SessionID: f.session, PullRequestID: pr, HeadSHA: "head-1", ExpectedGeneration: 0, Reason: reason})
			if !tt.stale {
				require.ErrorIs(t, err, pgx.ErrNoRows, "moved-ref settlement cannot replace another tenant, generation, revision or publication")
				return
			}
			require.NoError(t, err, "the exact unsent moved-ref review should settle under its atomic ownership fence")
			require.Equal(t, models.CodeReviewSessionStatusStale, actual.Status, "head movement must produce stale metadata rather than worker_failed")
			require.True(t, actual.Stale, "the old preparation result must never be reusable")
			require.Equal(t, &reason, actual.FailureReason, "stale outcome should preserve a safe specific reconciliation reason")
			require.Nil(t, actual.StatusCode, "revision movement cannot be misclassified as an operational failure")
			require.Equal(t, models.CodeReviewDecisionBlocked, *actual.Decision, "stale setup may not approve the pinned revision")
		})
	}
}
