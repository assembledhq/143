package reviewloop

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRetainedCallerIdentityPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source models.ReviewLoopSource
		phase  string
	}{
		{"publication prepared original", models.ReviewLoopSourcePublication, "T0"},
		{"publication created original", models.ReviewLoopSourcePublication, "T1"},
		{"automation prepared original", models.ReviewLoopSourceAutomation, "T0"},
		{"automation created original", models.ReviewLoopSourceAutomation, "T1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../../migrations", 2)
			org, session, publication := retainedPublicationFixture(t, ctx, pool)
			revision, head := int64(1), "synthetic head"
			req := StartReviewLoopRequest{Source: tt.source, MaxPasses: 2, ReviewRequired: true}
			if tt.source == models.ReviewLoopSourcePublication {
				req.ChangesetID = &publication.ChangesetID
				req.WorkspaceRevision = &revision
				req.DesiredHeadSHA = &head
			} else {
				automation, runID := uuid.New(), uuid.New()
				_, err := pool.Exec(ctx, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic caller identity','Retain original startup')`, automation, org)
				require.NoError(t, err, "seed owned automation without provider effects")
				_, err = pool.Exec(ctx, `INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,session_id,triggered_by) VALUES($1,$2,$3,'Synthetic original',$4,'manual')`, runID, org, automation, session.ID)
				require.NoError(t, err, "seed exact automation caller")
				req.AutomationRunID = &runID
			}
			base, err := db.NewThreadSendTransactions(pool)
			require.NoError(t, err, "use existing bounded owner")
			runner := &retainedStartFaultRunner{base: base, phase: tt.phase, commit: true}
			runtime := &retainedStartTestRuntime{pool: pool}
			svc := NewService(db.NewSessionReviewLoopStore(pool), runtime, WithStartTransactions(runner))
			_, err = svc.Start(ctx, org, session.ID, req)
			var pending *StartPendingError
			require.ErrorAs(t, err, &pending, "actual creation commit must retain correlation despite lost acknowledgement")
			require.True(t, runner.fired, "the selected transaction must really commit before suppressing acknowledgement")
			if tt.source == models.ReviewLoopSourcePublication {
				caller, err := db.NewSessionPublicationStore(pool).GetByChangeset(ctx, org, session.ID, publication.ChangesetID)
				require.NoError(t, err, "real caller projection must carry private start identity")
				require.Equal(t, &pending.LoopID, caller.ReviewStartID, "publication must retain exact committed identity")
			} else {
				caller, err := db.NewAutomationRunStore(pool).GetByRunID(ctx, org, *req.AutomationRunID)
				require.NoError(t, err, "real automation projection must carry private start identity")
				require.Equal(t, &pending.LoopID, caller.ReviewStartID, "automation must retain exact committed identity")
			}
			_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key=NULL,sandbox_state='destroyed',agent_type='codex' WHERE org_id=$1 AND id=$2`, org, session.ID)
			require.NoError(t, err, "change live fresh-start inputs after retaining original command")
			_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=false WHERE singleton`)
			require.NoError(t, err, "current disablement must not downgrade an existing S1 owner to legacy")
			req.MaxPasses = 99
			req.AgentType = models.AgentType("invalid current policy")
			req.DesiredHeadSHA = nil
			req.WorkspaceRevision = nil
			recovered, err := svc.Start(ctx, org, session.ID, req)
			if tt.phase == "T0" {
				require.ErrorIs(t, err, ErrReviewLoopStartPending, "the existing schema control blocks T1 installation while disabled without permitting legacy fallback")
				require.Nil(t, recovered, "a retained prepared record is not yet a known-created loop")
				require.Equal(t, 0, runtime.sends, "control refusal cannot authorize a send")
				_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton`)
				require.NoError(t, err, "restore only the isolated test control before resuming the same original")
				recovered, err = svc.Start(ctx, org, session.ID, req)
			}
			require.NoError(t, err, "durable caller identity must recover before every mutable fresh-start preflight")
			require.Equal(t, pending.LoopID, recovered.ID, "changed policy cannot allocate a replacement start")
			require.Equal(t, models.AgentTypeClaudeCode, recovered.AgentType, "original effective reviewer is frozen")
			require.Equal(t, 2, recovered.MaxPasses, "original effective pass budget is frozen")
			require.Equal(t, 1, runtime.reads, "caller recovery must never load fresh session policy")
			require.Equal(t, 1, runtime.sends, "only the original claim may send once")
			var counts [3]int
			require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1),(SELECT count(*) FROM session_review_loops WHERE org_id=$1),(SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1)`, org).Scan(&counts[0], &counts[1], &counts[2]), "read entire original creation graph")
			require.Equal(t, [3]int{1, 1, 1}, counts, "retries preserve one creation, loop and first pass")
		})
	}
}
