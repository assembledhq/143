package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewRecheckDispatchStatusValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status CodeReviewRecheckDispatchStatus
		valid  bool
	}{
		{"pending", CodeReviewRecheckDispatchPending, true},
		{"running", CodeReviewRecheckDispatchRunning, true},
		{"completed", CodeReviewRecheckDispatchCompleted, true},
		{"failed", CodeReviewRecheckDispatchFailed, true},
		{"cancelled", CodeReviewRecheckDispatchCancelled, true},
		{"empty", "", false},
		{"unknown", "other", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.status.Validate()
			if tt.valid {
				require.NoError(t, err, "declared dispatch status should validate")
			} else {
				require.Error(t, err, "unknown dispatch status should be rejected")
			}
		})
	}
}
