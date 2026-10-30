package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewLoopAdmissionState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state ReviewLoopAdmissionState
		valid bool
	}{
		{ReviewLoopAdmissionPending, true}, {ReviewLoopAdmissionAttempting, true}, {ReviewLoopAdmissionUnknown, true},
		{ReviewLoopAdmissionAttention, true}, {ReviewLoopAdmissionAccepted, true}, {ReviewLoopAdmissionRefused, true},
		{ReviewLoopAdmissionCancelled, true}, {"", false}, {"running", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			t.Parallel()
			err := tt.state.Validate()
			if tt.valid {
				require.NoError(t, err, "every supported startup admission state must validate")
			} else {
				require.Error(t, err, "ordinary loop status and missing values are not admission states")
			}
			if !tt.valid {
				return
			}
			raw, err := json.Marshal(SessionReviewLoop{AdmissionState: &tt.state})
			require.NoError(t, err, "admission projection must serialize as a finite public enum")
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields), "response must remain a JSON object")
			require.Equal(t, string(tt.state), fields["admission_state"], "the public field must expose exactly the durable state")
		})
	}
}
