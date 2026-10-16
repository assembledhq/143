package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestThreadInboxStorePostgresAppliedLookupRequiresExactAdmissionAndScope(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name, scope   string
		ids, expected []int64
	}{
		{name: "applied acknowledgment only", ids: []int64{5, 4, 3, 2, 1, 1}, expected: []int64{1}},
		{name: "requested IDs only", ids: []int64{2, 3}, expected: []int64{}},
		{name: "empty request", ids: []int64{}, expected: []int64{}},
		{name: "tenant isolation", scope: "org", ids: []int64{1}, expected: []int64{}},
		{name: "session isolation", scope: "session", ids: []int64{1}, expected: []int64{}},
		{name: "thread isolation", scope: "thread", ids: []int64{1}, expected: []int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, session, thread := seedRuntimeSeedThread(t, pool)
			for i, state := range []string{"acked", "acked", "unknown_delivery", "delivered", "pending"} {
				seedRuntimeInbox(t, pool, org, session, thread, int64(i+1), state, nil)
			}
			_, err := pool.Exec(context.Background(), `UPDATE thread_inbox_entries SET applied_at=now() WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND message_id IN (1,3,4)`, org, session, thread)
			require.NoError(t, err, "seed distinct acknowledgment and provider-admission outcomes")
			switch tt.scope {
			case "org":
				org = uuid.New()
			case "session":
				session = uuid.New()
			case "thread":
				thread = uuid.New()
			}
			actual, err := NewThreadInboxStore(pool).ListAppliedMessageIDs(context.Background(), org, session, thread, tt.ids)
			require.NoError(t, err, "load only scoped applied user IDs")
			require.Equal(t, tt.expected, actual, "unapplied acknowledgments, unknown writes, and foreign scopes must not count as admitted inputs")
		})
	}
}
