package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInputWorkspaceCompatibilityState_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		state   InputWorkspaceCompatibilityState
		wantErr string
	}{
		{name: "certified", state: InputWorkspaceCompatibilityStateCertified},
		{name: "pending", state: InputWorkspaceCompatibilityStatePending},
		{name: "unavailable", state: InputWorkspaceCompatibilityStateUnavailable},
		{name: "unprovable", state: InputWorkspaceCompatibilityStateUnprovable},
		{name: "missing state is not inferred unprovable", wantErr: `invalid InputWorkspaceCompatibilityState: ""`},
		{name: "unknown state", state: "future_state", wantErr: `invalid InputWorkspaceCompatibilityState: "future_state"`},
		{name: "case is not normalized", state: "CERTIFIED", wantErr: `invalid InputWorkspaceCompatibilityState: "CERTIFIED"`},
		{name: "whitespace is not normalized", state: " certified ", wantErr: `invalid InputWorkspaceCompatibilityState: " certified "`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.state.Validate()
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr, "validation should reject values outside the four named compatibility states")
				return
			}
			require.NoError(t, err, "named metadata states should validate without establishing a certificate or admission authority")
		})
	}
}
