package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSessionActivityPhaseStorePostgresRestartRetiresOnlyTerminalOwnedPhase(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name, scope                        string
		status                             models.ThreadRuntimeStatus
		runtimeLess, currentOwner, expired bool
		success                            bool
	}{
		{name: "closed old provider", status: models.ThreadRuntimeStatusClosed, success: true},
		{name: "failed old provider", status: models.ThreadRuntimeStatusFailed, success: true},
		{name: "lost old provider", status: models.ThreadRuntimeStatusLost, success: true},
		{name: "current live phase is preserved", currentOwner: true},
		{name: "runtime-less history remains ambiguous", runtimeLess: true, status: models.ThreadRuntimeStatusClosed},
		{name: "stale new owner cannot retire history", status: models.ThreadRuntimeStatusClosed, expired: true},
		{name: "foreign tenant owner cannot prove retirement", status: models.ThreadRuntimeStatusClosed, scope: "org"},
		{name: "foreign session owner cannot prove retirement", status: models.ThreadRuntimeStatusClosed, scope: "session"},
		{name: "foreign thread owner cannot prove retirement", status: models.ThreadRuntimeStatusClosed, scope: "thread"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			org, session, thread := seedRuntimeSeedThread(t, pool)
			old := createSeedRuntime(t, pool, org, session, thread)
			seedRuntimeInbox(t, pool, org, session, thread, 1, "acked", &old.ID)
			_, err := pool.Exec(ctx, `UPDATE thread_inbox_entries SET applied_at=now() WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, org, session, thread)
			require.NoError(t, err, "retain already admitted task input across worker restart")
			var oldID, oldToken *uuid.UUID
			if !tt.runtimeLess {
				oldID, oldToken = &old.ID, &old.LeaseToken
			}
			oldPhase, err := NewSessionActivityPhaseStore(pool).StartPhase(ctx, org, session, thread, 1, oldID, oldToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, time.Now().Add(-time.Second))
			require.NoError(t, err, "create historical running provider phase")
			current := old
			if !tt.currentOwner {
				changed, err := NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, org, old.ID, old.LeaseToken, tt.status, "worker ended", "")
				require.NoError(t, err, "terminalize previous runtime without relying on periodic phase reconciliation")
				require.True(t, changed, "previous owner must be durably terminal")
				current = createSeedRuntime(t, pool, org, session, thread)
			}
			if tt.scope != "" {
				foreignOrg, foreignSession, foreignThread := seedRuntimeSeedThread(t, pool)
				switch tt.scope {
				case "session":
					foreignOrg = org
				case "thread":
					foreignOrg, foreignSession = org, session
				}
				// Foreign runtime ownership is intentionally malformed historical data;
				// admission must not infer its terminal status authorizes this thread.
				_, err = pool.Exec(ctx, `UPDATE thread_runtimes SET org_id=$2,session_id=$3,thread_id=$4 WHERE org_id=$1 AND id=$5`, org, foreignOrg, foreignSession, foreignThread, old.ID)
				require.NoError(t, err, "seed historical owner outside exact scope")
			}
			if tt.expired {
				_, err = pool.Exec(ctx, `UPDATE thread_runtimes SET lease_expires_at=now()-interval '1 second' WHERE org_id=$1 AND id=$2`, org, current.ID)
				require.NoError(t, err, "expire current owner before recovery")
			}
			ids, err := NewThreadInboxStore(pool).ListAppliedMessageIDs(ctx, org, session, thread, []int64{1})
			require.NoError(t, err, "lookup admission history before recovery")
			require.Equal(t, []int64{1}, ids, "applied seed is retained as history rather than admitted again")
			next, err := NewSessionActivityPhaseStore(pool).StartPhase(ctx, org, session, thread, 2, &current.ID, &current.LeaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerRecovery}, time.Now())
			if tt.success {
				require.NoError(t, err, "fresh fenced owner should synchronously retire only terminal predecessor's phase")
				require.Equal(t, models.ActivityPhaseStatusRunning, next.Status, "new recovery phase should start without waiting for periodic reaper")
			} else {
				require.Error(t, err, "ambiguous or still active predecessor must block new provider execution")
			}
			var status models.ActivityPhaseStatus
			var reason *string
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,boundary_reason FROM session_activity_phases WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND id=$4`, org, session, thread, oldPhase.ID).Scan(&status, &reason), "read preserved historical phase outcome")
			if tt.success {
				require.Equal(t, models.ActivityPhaseStatusInterrupted, status, "terminal predecessor must remain interrupted history")
				require.NotNil(t, reason, "retired phase records exact runtime loss boundary")
				require.Equal(t, "runtime_lost", *reason, "retirement must preserve truthful interrupted semantics")
			} else {
				require.Equal(t, models.ActivityPhaseStatusRunning, status, "failed recovery admission cannot mutate predecessor history")
				require.Nil(t, reason, "blocked predecessor remains unchanged")
			}
		})
	}
}
