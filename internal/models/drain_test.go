package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDrainIntent_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		intent    DrainIntent
		expectErr bool
	}{
		{name: "none", intent: DrainIntentNone},
		{name: "planned rollout", intent: DrainIntentPlannedRollout},
		{name: "deploy budget expired", intent: DrainIntentDeployBudgetExpired},
		{name: "runtime ceiling", intent: DrainIntentRuntimeCeiling},
		{name: "human input checkpoint", intent: DrainIntentHumanInputCheckpoint},
		{name: "host maintenance", intent: DrainIntentHostMaintenance},
		{name: "emergency force", intent: DrainIntentEmergencyForce},
		{name: "unknown", intent: DrainIntent("bad"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.intent.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown drain intents")
				return
			}
			require.NoError(t, err, "Validate should accept known drain intents")
		})
	}
}
