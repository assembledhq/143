package worker

import (
	"context"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // One schema-local fault trigger is installed before tenant-parallel cases.
func TestConfirmedFullPublicationCompletionAtomicPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for atomic confirmed completion proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	_, err := pool.Exec(ctx, `
 CREATE TABLE reject_confirmed_completion_orgs(org_id uuid PRIMARY KEY);
 CREATE FUNCTION reject_confirmed_completion_after_claim() RETURNS trigger LANGUAGE plpgsql AS $$
 DECLARE parent_status text; parent_owner uuid;
 BEGIN
  IF NEW.status='completed' AND EXISTS(SELECT 1 FROM reject_confirmed_completion_orgs WHERE org_id=NEW.org_id) THEN
   SELECT status,code_review_owner_pr_id INTO parent_status,parent_owner FROM sessions WHERE org_id=NEW.org_id AND id=NEW.session_id;
   IF parent_status<>'completed' OR parent_owner IS DISTINCT FROM NEW.pull_request_id THEN
    RAISE EXCEPTION 'parent completion and ownership were not atomic';
   END IF;
   RAISE EXCEPTION 'injected assessment completion failure after parent claimed';
  END IF;
  RETURN NEW;
 END $$;
 CREATE TRIGGER reject_confirmed_completion_after_claim BEFORE UPDATE OF status ON code_review_revision_assessments FOR EACH ROW EXECUTE FUNCTION reject_confirmed_completion_after_claim();`)
	require.NoError(t, err, "install isolated failure after the guarded parent claim")
	tests := []struct {
		name          string
		reject, retry bool
	}{
		{name: "commit completes failed parent before controller reconciliation"},
		{name: "failure after claim rolls back entire outcome", reject: true},
		{name: "retry after rollback commits once and remains idempotent", reject: true, retry: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, _ := seedConfirmedDrainReview(t, pool, "failed")
			ctx := f.ctx()
			exec := func(q string, args ...any) {
				t.Helper()
				_, err := pool.Exec(ctx, q, args...)
				require.NoError(t, err, "seed isolated completion transaction state")
			}
			exec(`UPDATE sessions SET completed_at=now()-interval '1 hour',error='old timeout',failure_category='timeout',failure_next_steps=ARRAY['obsolete retry guidance'],failure_retry_advised=true WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
			if tt.reject {
				exec(`INSERT INTO reject_confirmed_completion_orgs(org_id) VALUES($1)`, f.job.OrgID)
			}
			snapshot := func() string {
				t.Helper()
				var state string
				err := pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(s),to_jsonb(m),to_jsonb(a),to_jsonb(p))::text FROM sessions s JOIN code_review_session_metadata m ON m.org_id=s.org_id AND m.session_id=s.id JOIN code_review_revision_assessments a ON a.org_id=m.org_id AND a.metadata_id=m.id JOIN code_review_pr_state p ON p.org_id=a.org_id AND p.pull_request_id=a.pull_request_id WHERE s.org_id=$1 AND s.id=$2`, f.job.OrgID, f.job.SessionID).Scan(&state)
				require.NoError(t, err, "snapshot parent, ownership, metadata, assessment and scheduler together")
				return state
			}
			before := snapshot()
			staged, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read pinned immutable staged outcome")
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read original terminal metadata")
			publisher := &fakeRecheckPublisher{}
			// Call completion without the handler's after-commit, best-effort parent
			// reconciliation to prove durability does not depend on that later write.
			complete := func() error {
				return resumeStagedFullAssessment(ctx, f.stores, &Services{CodeReviews: publisher}, f.job, metadata, staged, nil)
			}
			err = complete()
			if tt.reject {
				require.ErrorContains(t, err, "injected assessment completion failure after parent claimed", "failure must occur after atomic parent completion and ownership claim")
				require.Equal(t, before, snapshot(), "failed assessment completion must roll back parent, ownership, metadata and scheduler together")
				if !tt.retry {
					require.Empty(t, publisher.requests, "rolled-back confirmed completion must not resend review")
					return
				}
				exec(`DELETE FROM reject_confirmed_completion_orgs WHERE org_id=$1`, f.job.OrgID)
				err = complete()
			}
			require.NoError(t, err, "confirmed completion transaction must commit parent and full outcome together")
			parent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read durable completed parent without controller reconciliation")
			require.Equal(t, models.SessionStatusCompleted, parent.Status, "successful claim must never leave a failed PR owner")
			require.NotNil(t, parent.CompletedAt, "recovered parent completion must have a durable timestamp")
			require.Nil(t, parent.Error, "successful historical completion clears obsolete error")
			require.Nil(t, parent.FailureExplanation, "successful historical completion clears obsolete explanation")
			require.Nil(t, parent.FailureCategory, "successful historical completion clears obsolete category")
			require.Nil(t, parent.FailureNextSteps, "successful historical completion clears obsolete retry guidance")
			require.False(t, parent.FailureRetryAdvised, "completed parent should not advise retrying execution")
			var owner *uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT code_review_owner_pr_id FROM sessions WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID).Scan(&owner), "read committed exact ownership")
			require.Equal(t, &f.job.PullRequestID, owner, "completion retains exact assessment PR ownership")
			a, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read committed full assessment")
			require.Equal(t, models.CodeReviewAssessmentCompleted, a.Status, "full assessment commits with recovered parent")
			require.Equal(t, staged.PublicationReceipt, a.PublicationReceipt, "atomic completion must retain immutable receipt")
			require.Equal(t, staged.StructuredOutcome, a.StructuredOutcome, "atomic completion must retain immutable outcome")
			committed := snapshot()
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin idempotent owner claim")
			defer func() { _ = tx.Rollback(ctx) }()
			claimed, err := db.NewCodeReviewRecheckStore(pool).ClaimFullAssessmentOwner(ctx, tx, f.job.OrgID, f.job.SessionID, f.job.PullRequestID)
			require.NoError(t, err, "completed owner must retain ordinary owner-claim semantics")
			require.True(t, claimed, "idempotent ownership claim should retain exact PR")
			require.NoError(t, tx.Commit(ctx), "commit idempotent claim")
			recovered, err := recoverStagedFullAssessment(ctx, f.stores, &Services{CodeReviews: publisher}, f.job)
			require.NoError(t, err, "completed assessment has no further recovery work")
			require.False(t, recovered, "completed assessment must not replay staged completion")
			require.Equal(t, committed, snapshot(), "repeated owner claim and recovery must not rewrite any durable outcome")
			require.Empty(t, publisher.requests, "confirmed completion and repeated recovery must never resend publication")
			require.Empty(t, publisher.reconciliations, "exact confirmed receipt does not need a network lookup")
		})
	}
}
