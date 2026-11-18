package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycleCutoverState_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		state   LifecycleCutoverState
		wantErr string
	}{
		{name: "shadow", state: LifecycleCutoverStateShadow},
		{name: "normalizing", state: LifecycleCutoverStateNormalizing},
		{name: "active", state: LifecycleCutoverStateActive},
		{name: "missing state is not shadow", wantErr: `invalid LifecycleCutoverState: ""`},
		{name: "future state requires an explicit model update", state: "future_state", wantErr: `invalid LifecycleCutoverState: "future_state"`},
		{name: "case is not normalized", state: "ACTIVE", wantErr: `invalid LifecycleCutoverState: "ACTIVE"`},
		{name: "whitespace is not normalized", state: " active ", wantErr: `invalid LifecycleCutoverState: " active "`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.state.Validate()
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr, "state validation should reject values outside the named cutover states")
				return
			}
			require.NoError(t, err, "each named cutover state should validate without interpreting activation authority")
		})
	}
}
