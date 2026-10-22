package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHumanInputReadinessState_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		state   HumanInputReadinessState
		wantErr string
	}{
		{name: "unclassified", state: HumanInputReadinessStateUnclassified},
		{name: "ready", state: HumanInputReadinessStateReady},
		{name: "waiting", state: HumanInputReadinessStateWaiting},
		{name: "recovery required", state: HumanInputReadinessStateRecoveryRequired},
		{name: "absent is not a classification", wantErr: `invalid HumanInputReadinessState: ""`},
		{name: "unknown classification", state: "future_state", wantErr: `invalid HumanInputReadinessState: "future_state"`},
		{name: "case must match", state: "READY", wantErr: `invalid HumanInputReadinessState: "READY"`},
		{name: "whitespace is not normalized", state: " ready ", wantErr: `invalid HumanInputReadinessState: " ready "`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.state.Validate()
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr, "validation must reject values outside the named readiness classifications")
				return
			}
			require.NoError(t, err, "each named readiness classification must validate without interpreting its authority")
		})
	}
}
