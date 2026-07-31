package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActivityPhaseEnumsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		validate    func() error
		expectedErr string
	}{
		{name: "running status", validate: func() error { return ActivityPhaseStatusRunning.Validate() }},
		{name: "completed status", validate: func() error { return ActivityPhaseStatusCompleted.Validate() }},
		{name: "failed status", validate: func() error { return ActivityPhaseStatusFailed.Validate() }},
		{name: "cancelled status", validate: func() error { return ActivityPhaseStatusCancelled.Validate() }},
		{name: "interrupted status", validate: func() error { return ActivityPhaseStatusInterrupted.Validate() }},
		{name: "invalid status", validate: func() error { return ActivityPhaseStatus("other").Validate() }, expectedErr: `invalid ActivityPhaseStatus: "other"`},
		{name: "final response reason", validate: func() error { return ActivityPhaseBoundaryFinalResponse.Validate() }},
		{name: "human input reason", validate: func() error { return ActivityPhaseBoundaryHumanInput.Validate() }},
		{name: "approval reason", validate: func() error { return ActivityPhaseBoundaryApproval.Validate() }},
		{name: "plan approval reason", validate: func() error { return ActivityPhaseBoundaryPlanApproval.Validate() }},
		{name: "steered reason", validate: func() error { return ActivityPhaseBoundarySteered.Validate() }},
		{name: "maintenance reason", validate: func() error { return ActivityPhaseBoundaryMaintenance.Validate() }},
		{name: "runtime lost reason", validate: func() error { return ActivityPhaseBoundaryRuntimeLost.Validate() }},
		{name: "capacity suspended reason", validate: func() error { return ActivityPhaseBoundaryCapacitySuspended.Validate() }},
		{name: "interrupted reason", validate: func() error { return ActivityPhaseBoundaryInterrupted.Validate() }},
		{name: "stopped reason", validate: func() error { return ActivityPhaseBoundaryStopped.Validate() }},
		{name: "cancelled reason", validate: func() error { return ActivityPhaseBoundaryCancelled.Validate() }},
		{name: "error reason", validate: func() error { return ActivityPhaseBoundaryError.Validate() }},
		{name: "invalid reason", validate: func() error { return ActivityPhaseBoundaryReason("other").Validate() }, expectedErr: `invalid ActivityPhaseBoundaryReason: "other"`},
		{name: "initial trigger", validate: func() error { return ActivityPhaseTriggerInitial.Validate() }},
		{name: "inbox trigger", validate: func() error { return ActivityPhaseTriggerInboxBatch.Validate() }},
		{name: "recovery trigger", validate: func() error { return ActivityPhaseTriggerRecovery.Validate() }},
		{name: "invalid trigger", validate: func() error { return ActivityPhaseTriggerKind("other").Validate() }, expectedErr: `invalid ActivityPhaseTriggerKind: "other"`},
		{name: "acknowledged batch", validate: func() error { return InboxDeliveryBatchAcknowledged.Validate() }},
		{name: "started batch", validate: func() error { return InboxDeliveryBatchStarted.Validate() }},
		{name: "abandoned batch", validate: func() error { return InboxDeliveryBatchAbandoned.Validate() }},
		{name: "invalid batch", validate: func() error { return InboxDeliveryBatchStatus("other").Validate() }, expectedErr: `invalid InboxDeliveryBatchStatus: "other"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.validate()
			if tt.expectedErr != "" {
				require.EqualError(t, err, tt.expectedErr, "invalid typed activity phase value should return the exact validation error")
				return
			}
			require.NoError(t, err, "named typed activity phase value should validate")
		})
	}
}
