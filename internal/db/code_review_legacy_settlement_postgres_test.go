package db

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestLegacyFullAssessmentSettlementPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, status, scope                         string
		newSession, newAssessment, pending, foreign bool
		release                                     bool
	}{
		{name: "completed full review releases legacy session", status: "completed", scope: "full", release: true},
		{name: "failed full review releases legacy session", status: "failed", scope: "full", release: true},
		{name: "newer session is preserved", status: "completed", scope: "full", newSession: true},
		{name: "explicit assessment pointer is preserved", status: "completed", scope: "full", newAssessment: true},
		{name: "evidence assessment cannot release legacy full session", status: "completed", scope: "evidence_only"},
		{name: "pending request survives settlement", status: "completed", scope: "full", pending: true, release: true},
		{name: "foreign org cannot settle reservation", status: "failed", scope: "full", foreign: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, org, repo, pr := newSchedulingPostgres(t)
			ctx := context.Background()
			session, otherSession, a, otherA := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,status) VALUES($1,$3,'completed'),($2,$3,'completed')`, session, otherSession, org)
			require.NoError(t, err, "seed owned legacy and replacement sessions")
			_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,session_id,review_scope,status) VALUES($1,$2,$3,$4,$5,$6,$7),($8,$2,$3,$4,$9,'full','running')`, a, org, repo, pr, session, tt.scope, tt.status, otherA, otherSession)
			require.NoError(t, err, "seed terminal and replacement assessments")
			store := NewCodeReviewScheduleStore(pool)
			require.NoError(t, store.WithLockedPR(ctx, org, repo, pr, func(_ pgx.Tx, st *models.CodeReviewPRState) error {
				st.ActiveSessionID = &session
				st.State = models.CodeReviewScheduleRunning
				if tt.newSession {
					st.ActiveSessionID = &otherSession
				}
				if tt.newAssessment {
					st.ActiveAssessmentID = &otherA
				}
				if tt.pending {
					st.PendingInput = json.RawMessage(`{"reason":"new request"}`)
				}
				return nil
			}), "seed session-only legacy reservation")
			before, err := store.Get(ctx, org, pr)
			require.NoError(t, err, "snapshot scheduler")
			queryOrg := org
			if tt.foreign {
				queryOrg = uuid.New()
			}
			err = store.SettleAssessment(ctx, queryOrg, a)
			if tt.foreign {
				require.ErrorIs(t, err, pgx.ErrNoRows, "foreign tenant must not discover assessment")
			} else {
				require.NoError(t, err, "settle exact assessment")
				require.NoError(t, store.SettleAssessment(ctx, queryOrg, a), "settlement should be idempotent")
			}
			after, err := store.Get(ctx, org, pr)
			require.NoError(t, err, "read settled scheduler")
			require.Equal(t, before.PendingInput, after.PendingInput, "new pending request must be preserved exactly")
			if tt.release {
				require.Nil(t, after.ActiveSessionID, "terminal full review releases its own legacy session")
				require.Nil(t, after.ActiveAssessmentID, "legacy reservation has no residual active assessment")
				if tt.status == "completed" {
					require.Equal(t, &a, after.CurrentAssessmentID, "completed full review becomes current")
				}
			} else {
				require.Equal(t, before.ActiveSessionID, after.ActiveSessionID, "unrelated reservation must retain session")
				require.Equal(t, before.ActiveAssessmentID, after.ActiveAssessmentID, "new explicit assessment cannot be cleared")
				require.Equal(t, before.CurrentAssessmentID, after.CurrentAssessmentID, "unowned result cannot replace current assessment")
			}
		})
	}
}
