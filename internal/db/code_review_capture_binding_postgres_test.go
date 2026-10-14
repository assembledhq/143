package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestCaptureReassessmentBindingOwnershipPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	ctx := context.Background()
	tests := []struct {
		name, sourceStatus, requestStatus, legacyStatus, owner string
		noHistory, replacement, settle, foreign, missingMarker bool
		bind                                                   bool
	}{
		{name: "ordinary first unbound owner starts", sourceStatus: "running", noHistory: true, owner: "null", bind: true},
		{name: "ordinary completed target starts despite history", sourceStatus: "completed", requestStatus: "satisfied", owner: "null", bind: true},
		{name: "genuine replacement acquires the old owner", sourceStatus: "stale", requestStatus: "pending", replacement: true, bind: true},
		{name: "stale source cannot steal replacement owner", sourceStatus: "stale", requestStatus: "satisfied", owner: "replacement"},
		{name: "failed source with cancelled history cannot bind", sourceStatus: "failed", requestStatus: "cancelled"},
		{name: "settled reassessment cannot be revived by old bind", sourceStatus: "stale", requestStatus: "cancelled", settle: true},
		{name: "succeeded legacy starter history fences the stale source", sourceStatus: "stale", noHistory: true, legacyStatus: "succeeded"},
		{name: "dead-lettered legacy starter history fences failed source", sourceStatus: "failed", noHistory: true, legacyStatus: "dead_letter"},
		{name: "legacy dispatch before retirement fences original running source", sourceStatus: "running", noHistory: true, legacyStatus: "pending", owner: "legacy-dispatch"},
		{name: "unrelated dispute marker cannot block ordinary binding", sourceStatus: "stale", requestStatus: "cancelled", missingMarker: true, bind: true},
		{name: "foreign organization cannot bind source", sourceStatus: "running", noHistory: true, foreign: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedCaptureHandoffSettlement(t, pool)
			_, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET status=$3 WHERE org_id=$1 AND session_id=$2`, f.orgID, f.sourceSessionID, tt.sourceStatus)
			require.NoError(t, err, "set the original review's actual binding lifecycle")
			if tt.noHistory {
				_, err = pool.Exec(ctx, `DELETE FROM code_review_requests WHERE org_id=$1 AND id=$2`, f.orgID, f.requestID)
			} else {
				_, err = pool.Exec(ctx, `UPDATE code_review_requests SET status=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.requestID, tt.requestStatus)
			}
			require.NoError(t, err, "retain or remove the exact historical scheduled capture handoff")
			if tt.legacyStatus != "" {
				_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,dedupe_key,status) VALUES($1,'agent','start_code_review_reassessment',jsonb_build_object('org_id',$1::uuid::text,'triggering_dispute_id',$2::uuid::text),$3,$4)`, f.orgID, f.disputeID, "code_review_reassessment:"+f.pullRequestID.String()+":capture_identity:"+f.sourceSessionID.String(), tt.legacyStatus)
				require.NoError(t, err, "retain exact legacy starter history after its work becomes terminal")
			}
			switch tt.owner {
			case "null":
				_, err = pool.Exec(ctx, `UPDATE code_review_decision_disputes SET reassessment_session_id=NULL,reassessment_status='queued' WHERE org_id=$1 AND id=$2`, f.orgID, f.disputeID)
			case "replacement":
				_, err = pool.Exec(ctx, `UPDATE code_review_decision_disputes SET reassessment_session_id=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.disputeID, f.replacementSessionID)
			}
			require.NoError(t, err, "establish the exact current reassessment owner")
			if tt.owner == "legacy-dispatch" {
				dispatchTx, err := pool.Begin(ctx)
				require.NoError(t, err, "begin actual legacy replacement dispatch before source retirement")
				defer func() { _ = dispatchTx.Rollback(ctx) }()
				require.NoError(t, NewCodeReviewDisputeStore(dispatchTx).MarkReassessmentStarted(ctx, f.orgID, f.disputeID, f.replacementSessionID), "actual legacy replacement bind must run while source metadata is still running")
				require.NoError(t, dispatchTx.Commit(ctx), "commit replacement ownership before the old source retirement callback")
				var sourceStatus string
				require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2`, f.orgID, f.sourceSessionID).Scan(&sourceStatus), "read the original source lifecycle at legacy replacement bind time")
				require.Equal(t, "running", sourceStatus, "the legacy regression must bind replacement before original source retirement")
			}
			if tt.missingMarker {
				_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET triggering_dispute_id=NULL WHERE org_id=$1 AND session_id=$2`, f.orgID, f.sourceSessionID)
				require.NoError(t, err, "remove matching metadata provenance from the unrelated historical capture source")
				_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context='{"triggering_dispute_id":"not-a-uuid"}' WHERE org_id=$1 AND id=$2`, f.orgID, f.sourceSessionID)
				require.NoError(t, err, "retain an unrelated malformed legacy marker without unsafe UUID casting")
			}
			if tt.settle {
				tx, err := pool.Begin(ctx)
				require.NoError(t, err, "begin actual abandoned capture handoff settlement")
				defer func() { _ = tx.Rollback(ctx) }()
				require.NoError(t, SettleRetiredCapturedIdentityReassessments(ctx, tx, f.orgID, f.pullRequestID), "settle the abandoned reassessment before delayed bind replay")
				require.NoError(t, tx.Commit(ctx), "commit terminal reassessment settlement")
			}
			store := NewCodeReviewDisputeStore(pool)
			before, err := store.GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read every dispute field before binding")
			callOrgID, targetSessionID := f.orgID, f.sourceSessionID
			if tt.foreign {
				callOrgID = uuid.New()
			}
			if tt.replacement {
				targetSessionID = f.replacementSessionID
			}
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin the caller's dispatch transaction for a nested binding savepoint")
			defer func() { _ = tx.Rollback(ctx) }()
			var boundAt time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT now()`).Scan(&boundAt), "capture the transaction timestamp for exact binding comparison")
			require.NoError(t, NewCodeReviewDisputeStore(tx).MarkReassessmentStarted(ctx, callOrgID, f.disputeID, targetSessionID), "binding must preserve ordinary starts and harmless stale or foreign callbacks")
			require.NoError(t, tx.Commit(ctx), "commit the actual nested binding operation")
			after, err := store.GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read every durable dispute field after binding")
			expected := before
			if tt.bind {
				detail := "Reassessment started."
				expected.ReassessmentSessionID = &targetSessionID
				expected.ReassessmentStatus = models.CodeReviewDisputeReassessmentRunning
				expected.StatusDetail, expected.UpdatedAt, expected.Version = &detail, boundAt, before.Version+1
			}
			require.Equal(t, expected, after, "retired source callbacks must preserve the complete current owner while ordinary and replacement binds retain existing semantics")
		})
	}
}

func TestDelayedCaptureSourceBindingRereadsUnderPRLockPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := seedCaptureHandoffSettlement(t, pool)
	_, err := pool.Exec(ctx, `DELETE FROM code_review_requests WHERE org_id=$1 AND id=$2`, f.orgID, f.requestID)
	require.NoError(t, err, "begin with no historical capture handoff before the original bind starts")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET status='running' WHERE org_id=$1 AND session_id=$2`, f.orgID, f.sourceSessionID)
	require.NoError(t, err, "the original target must be active before its delayed bind")
	ownerTx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin the replacement dispatch owner's actual PR transaction")
	defer func() { _ = ownerTx.Rollback(context.Background()) }()
	_, err = ownerTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+f.orgID.String()+":"+f.pullRequestID.String())
	require.NoError(t, err, "hold the existing PR scheduling lock before the original binder starts")
	contender, err := pool.Acquire(ctx)
	require.NoError(t, err, "reserve a separate backend for the delayed original binder")
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		done <- NewCodeReviewDisputeStore(contender).MarkReassessmentStarted(ctx, f.orgID, f.disputeID, f.sourceSessionID)
	}()
	defer func() {
		cancel()
		if err := ownerTx.Rollback(context.Background()); err != nil && err != pgx.ErrTxClosed {
			t.Errorf("release only this test's replacement transaction: %v", err)
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("delayed original binder did not finish after cancellation and lock release")
		}
		contender.Release()
	}()
	pid := contender.Conn().PgConn().PID()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)`, pid).Scan(&waiting)
		return err == nil && waiting
	}, 3*time.Second, 10*time.Millisecond, "the actual original bind must block on the PR advisory lock before recovery changes its target")
	_, err = ownerTx.Exec(ctx, `UPDATE code_review_session_metadata SET status='stale' WHERE org_id=$1 AND session_id=$2`, f.orgID, f.sourceSessionID)
	require.NoError(t, err, "retire the source while its original bind is waiting on the PR lock")
	_, reused, err := RecordCodeReviewRequest(ctx, ownerTx, f.orgID, f.repositoryID, f.pullRequestID, "github", "capture_identity:"+f.sourceSessionID.String(), models.CodeReviewReviewNow, "synthetic-capture", 1, nil)
	require.NoError(t, err, "admit the actual durable capture recovery request under the held PR lock")
	require.False(t, reused, "the delayed binder must have started before any capture handoff existed")
	require.NoError(t, NewCodeReviewDisputeStore(ownerTx).MarkReassessmentStarted(ctx, f.orgID, f.disputeID, f.replacementSessionID), "replacement dispatch must bind through a nested transaction while holding the same PR lock")
	require.NoError(t, ResolveCodeReviewRequests(ctx, ownerTx, f.orgID, f.pullRequestID, 1, &f.replacementSessionID, "satisfied"), "resolve the actual recovery request after replacement ownership is durable in the transaction")
	bound, err := NewCodeReviewDisputeStore(ownerTx).GetByID(ctx, f.orgID, f.disputeID)
	require.NoError(t, err, "read the complete replacement-bound dispute before releasing the old binder")
	require.Equal(t, &f.replacementSessionID, bound.ReassessmentSessionID, "replacement dispatch must own reassessment before the delayed original bind resumes")
	require.NoError(t, ownerTx.Commit(ctx), "commit replacement ownership and release the waiting original binder")
	select {
	case err := <-done:
		require.NoError(t, err, "delayed original bind must finish as a harmless historical source callback")
	case <-ctx.Done():
		t.Fatal("delayed original bind did not finish after replacement dispatch committed")
	}
	after, err := NewCodeReviewDisputeStore(pool).GetByID(ctx, f.orgID, f.disputeID)
	require.NoError(t, err, "read replacement ownership after the delayed original callback completes")
	require.Equal(t, bound, after, "the delayed original binder must reread capture retirement after acquiring the PR lock and preserve every replacement field")
}
