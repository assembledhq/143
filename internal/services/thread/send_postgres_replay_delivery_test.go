package thread

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// This snapshot excludes only the current replay disposition and inbox progress:
// every original accepted field, including the retained logical claim identity,
// remains historical receipt data rather than permission to execute or settle.
type sendPostgresOriginalAdmission struct {
	orgID, dispatchID, jobID, holderID uuid.UUID
	fence                              db.AdmissionFence
	ingress                            db.LifecycleIngress
	disposition                        db.MessageAdmissionDisposition
	message                            models.SessionMessage
	comments                           []models.SessionReviewComment
	claim                              db.AdmissionAuthority
	hasJob, hasHolder, hasClaim        bool
	options                            db.MessageAdmissionDispatchOptions
	targetNodeID                       *string
	threadStatus                       models.ThreadStatus
	reason                             string
}

func originalSendPostgresAdmission(a db.Admission) sendPostgresOriginalAdmission {
	job, hasJob := a.JobID()
	holder, hasHolder := a.LeaseHolderID()
	claim, hasClaim := a.Authority()
	return sendPostgresOriginalAdmission{
		orgID: a.OrgID(), dispatchID: a.DispatchID(), jobID: job, holderID: holder,
		fence: a.Fence(), ingress: a.Ingress(), disposition: a.OriginalDisposition(),
		message: a.Message(), comments: a.ResolvedComments(), claim: claim,
		hasJob: hasJob, hasHolder: hasHolder, hasClaim: hasClaim,
		options: a.DispatchOptions(), targetNodeID: a.TargetNodeID(),
		threadStatus: a.ThreadStatus(), reason: a.Reason(),
	}
}

func TestThreadSendReplayRetainsAdmissionAndReadsCurrentInboxPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		acked bool
		state models.ThreadInboxDeliveryState
	}{
		{name: "current delivered marker remains unapplied", state: models.ThreadInboxDeliveryStateDelivered},
		{name: "current acknowledged seed exposes applied timestamp", acked: true, state: models.ThreadInboxDeliveryStateAcked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, true, 1)
			ctx := context.Background()
			service, effects := f.service(t, true)
			input := f.input(0, "synthetic-replay-current-inbox")
			accepted, err := service.SendMessage(ctx, input)
			require.NoError(t, err, "commit the actual active send before observing later durable inbox progress")
			require.NotNil(t, accepted.Admission, "accepted active input must expose its original canonical receipt")
			require.NotNil(t, accepted.InboxEntry, "accepted active input must expose its exact durable inbox association")
			require.Equal(t, db.MessageAdmissionNewJob, accepted.Admission.Disposition(), "the original acceptance must bind one ordinary input job")
			require.Equal(t, models.ThreadInboxDeliveryStatePending, accepted.DeliveryState, "the original receipt must not claim delivery before any durable progress writer runs")
			original := originalSendPostgresAdmission(*accepted.Admission)
			originalInbox := *accepted.InboxEntry
			require.Nil(t, originalInbox.AppliedAt, "new acceptance cannot manufacture an applied-input receipt")
			require.NotNil(t, originalInbox.ClientMessageID, "current inbox readback must use the exact originally accepted scoped client identity")
			var runtime models.ThreadRuntime
			require.NoError(t, db.RunLifecycleTransaction(ctx, f.pool, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				runtime, err = db.NewThreadRuntimeStore(tx).CreateStarting(ctx, f.org, db.CreateThreadRuntimeParams{
					SessionID: f.session, ThreadID: f.lanes[0], SandboxID: uuid.New(), AgentType: models.DefaultDefaultAgentType,
					OwnerNodeID: "synthetic-replay-delivery-worker", LeaseToken: uuid.New(), LeaseDuration: time.Minute,
				})
				return err
			}), "install a current synthetic leased runtime without invoking a provider or claiming launch authority")
			inboxStore := db.NewThreadInboxStore(f.pool)
			messageIDs := []int64{accepted.Message.ID}
			// Use the actual lease-fenced SQL progress writers, not hand-edited
			// delivery columns. This proves their durable rows and replay readback;
			// no provider process or external delivery is exercised by this test.
			changed, err := inboxStore.MarkDeliveringForMessages(ctx, f.org, f.lanes[0], runtime.ID, uuid.New(), runtime.OwnerNodeID, messageIDs)
			require.NoError(t, err, "stale runtime ownership must be a clean progress refusal")
			require.Zero(t, changed, "an unrelated lease token cannot attach the admitted inbox entry")
			changed, err = inboxStore.MarkDeliveringForMessages(ctx, f.org, f.lanes[0], runtime.ID, runtime.LeaseToken, runtime.OwnerNodeID, messageIDs)
			require.NoError(t, err, "the actual delivering writer must retain the exact current runtime and admitted message association")
			require.Equal(t, int64(1), changed, "only the exact accepted inbox entry may enter the delivering state")
			changed, err = inboxStore.MarkDeliveredForEntry(ctx, f.org, f.lanes[0], runtime.ID, runtime.LeaseToken, runtime.OwnerNodeID, originalInbox.ID, originalInbox.SequenceNo)
			require.NoError(t, err, "the actual delivered writer must preserve the exact tenant lane runtime entry and sequence association")
			require.Equal(t, int64(1), changed, "only the current leased runtime may record the exact admitted entry's delivered marker")
			if tt.acked {
				changed, err = inboxStore.MarkAckedForSeedMessages(ctx, f.org, f.lanes[0], runtime.ID, runtime.LeaseToken, messageIDs)
				require.NoError(t, err, "the actual seed acknowledgment writer must preserve the exact admitted instruction under its live runtime lease")
				require.Equal(t, int64(1), changed, "the current seed writer must actually persist one acknowledged and applied input")
			}
			currentInbox, err := inboxStore.GetByClientMessageID(ctx, f.org, f.lanes[0], *originalInbox.ClientMessageID)
			require.NoError(t, err, "read the sole current inbox truth through the existing tenant/lane-scoped store")
			require.Equal(t, []uuid.UUID{f.org, f.session, f.lanes[0], originalInbox.ID}, []uuid.UUID{currentInbox.OrgID, currentInbox.SessionID, currentInbox.ThreadID, currentInbox.ID}, "delivery progress cannot replace the accepted inbox's tenant session lane or identity")
			require.Equal(t, tt.state, currentInbox.DeliveryState, "current durable delivery state must come from the actual progress writers")
			require.NotNil(t, currentInbox.DeliveredAt, "a durable delivered marker must retain its actual timestamp")
			if tt.acked {
				require.NotNil(t, currentInbox.AppliedAt, "the acknowledged input must expose the actual durable applied timestamp")
				require.NotNil(t, currentInbox.AckedAt, "the acknowledged input must expose the actual durable acknowledgment timestamp")
			} else {
				require.Nil(t, currentInbox.AppliedAt, "delivered progress alone cannot be represented as applied input")
				require.Nil(t, currentInbox.AckedAt, "delivered progress alone cannot manufacture acknowledgment")
			}
			before := sendPostgresReadGraph(t, f)
			lifecycleCalls, jobCalls := len(effects.lifecycle), len(effects.jobs)
			legacyCalls := []int{effects.claims, effects.pending, effects.threadStatus, effects.parentStatus}
			replayed, err := service.SendMessage(ctx, input)
			require.NoError(t, err, "identical ingress must replay accepted input after current inbox delivery progress")
			require.NotNil(t, replayed.Admission, "historical acceptance must retain its canonical receipt")
			require.Equal(t, db.MessageAdmissionReplayed, replayed.Admission.Disposition(), "current delivery cannot turn the identical ingress into new admission")
			require.Equal(t, original, originalSendPostgresAdmission(*replayed.Admission), "replay must retain every original accepted scope identity outcome reason option routing message and claim field")
			require.Equal(t, currentInbox, replayed.Admission.InboxEntry(), "the receipt must expose the complete current inbox row rather than its historical pending snapshot")
			require.Equal(t, &currentInbox, replayed.InboxEntry, "the service response must use the same sole current inbox truth as its canonical receipt")
			require.Equal(t, currentInbox.DeliveryState, replayed.DeliveryState, "the service response cannot report historical pending delivery after durable progress")
			require.Equal(t, accepted.ThreadStatus, replayed.ThreadStatus, "thread status remains the original admission outcome, independently of current inbox delivery truth")
			require.Equal(t, before, sendPostgresReadGraph(t, f), "exact replay cannot create another message inbox dispatch job claim or pending input")
			require.Equal(t, []int{lifecycleCalls, jobCalls}, []int{len(effects.lifecycle), len(effects.jobs)}, "receipt replay cannot repeat postcommit effects or job notifications")
			require.Equal(t, legacyCalls, []int{effects.claims, effects.pending, effects.threadStatus, effects.parentStatus}, "receipt replay cannot invoke legacy claim pending or status publishers")
		})
	}
}
