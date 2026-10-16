package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionExecutionPurposeValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose SessionExecutionPurpose
		valid   bool
	}{
		{"ordinary continuation", SessionExecutionPurposeContinue, true},
		{"explicit checkpoint retry", SessionExecutionPurposeCheckpointRetry, true},
		{"trusted interrupted job recovery", SessionExecutionPurposeCheckpointRecovery, true},
		{"implicit default must be normalized", "", false},
		{"unknown purpose", "replay_unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.purpose.Validate()
			if tt.valid {
				require.NoError(t, err, "supported execution purpose must validate")
			} else {
				require.Error(t, err, "unknown purpose cannot grant replay authority")
			}
		})
	}
}
