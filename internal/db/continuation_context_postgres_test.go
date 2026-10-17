package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestContinuationContextPostgresRequiresUnfinishedOwnedAdmission(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name, outcome, scope              string
		live, omitted, note, laterOutcome bool
		want                              bool
	}{
		{name: "failed provider task remains context", outcome: "generic provider error", want: true},
		{name: "maintenance interrupted task remains context", outcome: "maintenance", want: true},
		{name: "startup note cannot hide interrupted task", outcome: "generic provider error", note: true, want: true},
		{name: "later resolved turn bounds older interrupted context", outcome: "generic provider error", laterOutcome: true},
		{name: "lost runtime with unfinished phase remains context", want: true},
		{name: "completed task is excluded", outcome: "completed"},
		{name: "completed provider with partial lifecycle is excluded", outcome: "partial completion"},
		{name: "unknown delivery is excluded", outcome: "unknown input"},
		{name: "live owner cannot be adopted", live: true},
		{name: "older unrelated task is excluded", outcome: "generic provider error", omitted: true},
		{name: "tenant scope is enforced", outcome: "generic provider error", scope: "org"},
		{name: "session scope is enforced", outcome: "generic provider error", scope: "session"},
		{name: "thread scope is enforced", outcome: "generic provider error", scope: "thread"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newContinuationRecoveryFixture(t, pool, tt.outcome, false)
			if tt.note {
				require.NoError(t, NewSessionMessageStore(pool).Create(ctx, &models.SessionMessage{OrgID: f.org, SessionID: f.session, ThreadID: &f.thread, Role: models.MessageRoleAssistant, TurnNumber: 2, Content: "Authentication failed before retry execution"}), "retain non-outcome startup note after unfinished task admission")
			}
			if tt.laterOutcome {
				runtime := createSeedRuntime(t, pool, f.org, f.session, f.thread)
				phase, err := NewSessionActivityPhaseStore(pool).StartPhase(ctx, f.org, f.session, f.thread, 2, &runtime.ID, &runtime.LeaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerRecovery}, time.Now())
				require.NoError(t, err, "recover the older task in a subsequent provider turn")
				_, err = NewSessionActivityPhaseStore(pool).CompletePhase(ctx, f.org, phase.ID, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, time.Now())
				require.NoError(t, err, "a later provider outcome must bound old failed-task context")
			}
			if tt.live {
				_, err := pool.Exec(ctx, `UPDATE thread_runtimes SET status='live',lease_expires_at=clock_timestamp()+interval '1 minute' WHERE org_id=$1 AND id=$2`, f.org, f.runtime.ID)
				require.NoError(t, err, "retain valid ownership for an in-flight task")
			}
			switch tt.scope {
			case "org":
				f.org = uuid.New()
			case "session":
				f.session = uuid.New()
			case "thread":
				f.thread = uuid.New()
			}
			requested := []int64{f.message}
			if tt.omitted {
				requested = []int64{f.message + 1000}
			}
			actual, err := NewThreadInboxStore(pool).ListUnsettledAppliedMessageIDs(ctx, f.org, f.session, f.thread, requested)
			require.NoError(t, err, "read unfinished input only from positive scoped admission proof")
			expected := []int64{}
			if tt.want {
				expected = []int64{f.message}
			}
			require.Equal(t, expected, actual, "only unresolved admitted task context may accompany a new follow-up")
		})
	}
}
