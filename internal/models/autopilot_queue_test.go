package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutopilotQueueEnumValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		validate  func() error
		expectErr bool
	}{
		{name: "valid queued run state", validate: func() error { return AutopilotRunStateQueued.Validate() }},
		{name: "valid start run action", validate: func() error { return AutopilotQueueActionStartRun.Validate() }},
		{name: "valid auto trigger mode", validate: func() error { return AutopilotTriggerModeAuto.Validate() }},
		{name: "valid target-only preview status", validate: func() error { return AutopilotPreviewStatusTargetCreated.Validate() }},
		{name: "invalid run state", validate: func() error { return AutopilotRunState("bogus").Validate() }, expectErr: true},
		{name: "invalid action", validate: func() error { return AutopilotQueueAction("bogus").Validate() }, expectErr: true},
		{name: "invalid trigger mode", validate: func() error { return AutopilotTriggerMode("bogus").Validate() }, expectErr: true},
		{name: "invalid preview status", validate: func() error { return AutopilotPreviewStatus("bogus").Validate() }, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.validate()
			if tt.expectErr {
				require.Error(t, err, "invalid enum values should fail validation")
				return
			}
			require.NoError(t, err, "valid enum values should pass validation")
		})
	}
}
