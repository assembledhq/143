package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReviewSetupHolderProtectsReleasedTurnPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		status      models.SessionSandboxHolderStatus
		snapshotKey string
		finalState  string
	}{
		{name: "active setup pin without snapshot", status: models.SessionSandboxHolderStatusActive, finalState: "none"},
		{name: "draining setup pin with snapshot", status: models.SessionSandboxHolderStatusDraining, snapshotKey: "verified-snapshot", finalState: "snapshotted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			ctx := context.Background()
			_, err := f.pool.Exec(ctx, `UPDATE sessions
				SET container_id=$1,worker_node_id=$2,turn_holding_container=true,sandbox_state='running',snapshot_key=$3
				WHERE org_id=$4 AND id=$5`, f.params.ContainerID, f.node, tt.snapshotKey, f.org, f.session)
			require.NoError(t, err, "setup container should begin with the shared turn bit held")
			holderID, leaseToken := uuid.New(), uuid.New()
			holders := NewSessionSandboxHolderStore(f.pool)
			holder, err := holders.CreateActive(ctx, f.org, CreateSessionSandboxHolderParams{
				SessionID: f.session, ContainerID: f.params.ContainerID,
				HolderKind: models.SessionSandboxHolderKindOperator, HolderID: holderID,
				OwnerNodeID: f.node, LeaseToken: leaseToken, LeaseDuration: time.Minute,
			})
			require.NoError(t, err, "input preparation should retain its own live operator lease")
			require.Equal(t, models.SessionSandboxHolderKindOperator, holder.HolderKind, "setup protection should use the existing operator holder kind")
			require.Equal(t, leaseToken, holder.LeaseToken, "setup protection should retain the exact creation token")
			_, err = f.pool.Exec(ctx, `UPDATE session_sandbox_holders SET status=$1 WHERE org_id=$2 AND id=$3`, tt.status, f.org, holder.ID)
			require.NoError(t, err, "fixture should exercise the requested live holder status")
			sessions := NewSessionStore(f.pool)
			destroyNow, containerID, err := sessions.ReleaseTurnHold(ctx, f.org, f.session)
			require.NoError(t, err, "a sibling turn should release the shared bit while setup is running")
			require.False(t, destroyNow, "live setup lease must suppress the sibling destroy decision")
			require.Equal(t, f.params.ContainerID, containerID, "releasing a turn should preserve the protected container identity")
			var holding bool
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT turn_holding_container FROM sessions WHERE org_id=$1 AND id=$2`, f.org, f.session).Scan(&holding), "read the shared bit independently of the setup lease")
			require.False(t, holding, "setup protection must still work after the shared turn bit is false")
			cleared, err := sessions.FinalizeContainerDestroy(ctx, f.org, f.session, f.params.ContainerID)
			require.NoError(t, err, "physical destruction should recheck live setup holders")
			require.False(t, cleared, "final destroy CAS must preserve the container while setup owns its lease")
			released, err := holders.ReleaseWithLease(ctx, f.org, f.session, models.SessionSandboxHolderKindOperator, holderID, uuid.New())
			require.NoError(t, err, "a stale setup token should be fenced without a database error")
			require.False(t, released, "a different setup token must not release the live pin")
			released, err = holders.ReleaseWithLease(ctx, uuid.New(), f.session, models.SessionSandboxHolderKindOperator, holderID, leaseToken)
			require.NoError(t, err, "another tenant should be fenced without a database error")
			require.False(t, released, "another tenant must not release an exact-token setup pin")
			cleared, err = sessions.FinalizeContainerDestroy(ctx, f.org, f.session, f.params.ContainerID)
			require.NoError(t, err, "failed lease releases should leave final destruction available to check")
			require.False(t, cleared, "stale-token and cross-tenant releases must leave the setup container protected")
			released, err = holders.ReleaseWithLease(ctx, f.org, f.session, models.SessionSandboxHolderKindOperator, holderID, leaseToken)
			require.NoError(t, err, "successful setup cleanup should release its exact operator lease")
			require.True(t, released, "the exact setup holder and token should release the pin")
			var status models.SessionSandboxHolderStatus
			var releasedAt *time.Time
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT status,released_at FROM session_sandbox_holders WHERE org_id=$1 AND id=$2`, f.org, holder.ID).Scan(&status, &releasedAt), "read the persisted exact setup release")
			require.Equal(t, models.SessionSandboxHolderStatusReleased, status, "the setup pin should record a durable release")
			require.NotNil(t, releasedAt, "the setup pin should record when it relinquished protection")
			destroyNow, containerID, err = sessions.ReleaseTurnHold(ctx, f.org, f.session)
			require.NoError(t, err, "completed setup should permit a new destroy decision")
			require.True(t, destroyNow, "releasing the exact setup lease should enable container destruction")
			require.Equal(t, f.params.ContainerID, containerID, "destroy decision should still name the original protected container")
			cleared, err = sessions.FinalizeContainerDestroy(ctx, f.org, f.session, containerID)
			require.NoError(t, err, "final destruction should succeed after setup relinquishes its pin")
			require.True(t, cleared, "the final CAS should clear the now-unheld setup container")
			var finalContainer, finalNode *string
			var finalState string
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT container_id,worker_node_id,sandbox_state FROM sessions WHERE org_id=$1 AND id=$2`, f.org, f.session).Scan(&finalContainer, &finalNode, &finalState), "read the finalized container ownership")
			require.Nil(t, finalContainer, "finalization should revoke the released container identity")
			require.Nil(t, finalNode, "finalization should revoke the released container owner")
			require.Equal(t, tt.finalState, finalState, "finalization should retain only the verified snapshot state")
		})
	}
}
