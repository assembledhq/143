package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreviewVerificationEnumsValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		valid    bool
		validate func() error
	}{
		{name: "running status", valid: true, validate: PreviewVerificationStatusRunning.Validate},
		{name: "human intervention status", valid: true, validate: PreviewVerificationStatusHumanInterventionRequired.Validate},
		{name: "invalid status", valid: false, validate: PreviewVerificationStatus("unknown").Validate},
		{name: "automatic trigger", valid: true, validate: PreviewVerificationTriggerAutomatic.Validate},
		{name: "invalid trigger", valid: false, validate: PreviewVerificationTrigger("unknown").Validate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.validate()
			if tt.valid {
				require.NoError(t, err, "known enum value should validate")
				return
			}
			require.Error(t, err, "unknown enum value should fail validation")
		})
	}
}
