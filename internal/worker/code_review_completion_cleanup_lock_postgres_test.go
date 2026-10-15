package worker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFullAssessmentCompletionWaitsForCleanupLockPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for completion and cleanup lock ordering proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	for _, route := range []string{"complete", "reconcile"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			f, _ := seedConfirmedDrainReview(t, pool, "failed")
			_, err := pool.Exec(ctx, `UPDATE sessions SET container_id='retained-review-workspace' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "retain an idle workspace shared by completion and cleanup")
			if route == "reconcile" {
				_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='completed',decision='blocked',acceptable=false,final_review_body='immutable saved review' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "model reconciliation of completed legacy metadata")
			}
			staged, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the exact staged outcome before concurrent completion")
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read completion provenance before cleanup takes its lock")
			cleanup, err := pool.Begin(ctx)
			require.NoError(t, err, "begin cleanup ownership transaction")
			defer func() { _ = cleanup.Rollback(ctx) }()
			_, err = cleanup.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+f.job.OrgID.String()+":"+f.job.PullRequestID.String())
			require.NoError(t, err, "cleanup owns the same PR admission lock as its real finalizer")
			done := make(chan error, 1)
			go func() {
				if route == "reconcile" {
					done <- reconcileCompletedFullAssessment(ctx, f.stores, metadata)
					return
				}
				done <- completeFullAssessment(ctx, pool, staged, db.CompleteCodeReviewParams{
					SessionID: f.job.SessionID, Decision: *staged.Decision, Acceptable: *staged.Acceptable,
					GitHubReviewID: staged.GitHubReviewID, GitHubReviewURL: staged.GitHubReviewURL,
					FinalReviewBody: *staged.RenderedBody, RiskReasonDetails: []models.CodeReviewRiskReason{},
				}, staged.StructuredOutcome, true, *staged.RenderedBody)
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				err := pool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_locks waiting JOIN pg_locks owner USING(locktype,database,classid,objid,objsubid)
 WHERE owner.pid=$1 AND owner.locktype='advisory' AND owner.granted AND NOT waiting.granted
)`, cleanup.Conn().PgConn().PID()).Scan(&waiting)
				return err == nil && waiting
			}, 3*time.Second, 10*time.Millisecond, "completion must contend with cleanup's PR lock")
			var sessionID uuid.UUID
			// NOWAIT turns the reverse lock order into a deterministic regression
			// failure instead of relying on PostgreSQL's deadlock timeout.
			rowErr := cleanup.QueryRow(ctx, `SELECT id FROM sessions WHERE org_id=$1 AND id=$2 AND container_id='retained-review-workspace' FOR UPDATE NOWAIT`, f.job.OrgID, f.job.SessionID).Scan(&sessionID)
			require.NoError(t, cleanup.Rollback(ctx), "release cleanup's locks so completion can finish even when the regression fails")
			require.NoError(t, <-done, "completion must commit after cleanup yields its PR lock")
			require.NoError(t, rowErr, "completion must wait for the PR lock before locking the retained workspace session")
			require.Equal(t, f.job.SessionID, sessionID, "cleanup must still own the exact retained workspace while completion waits")
		})
	}
}
