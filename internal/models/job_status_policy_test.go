package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJobStatusLifecyclePolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   JobStatus
		valid    bool
		expected [4]bool
	}{
		{name: "pending is runnable and owns input", status: JobStatusPending, valid: true, expected: [4]bool{true, false, true, false}},
		{name: "running is lease owned and owns input", status: JobStatusRunning, valid: true, expected: [4]bool{false, true, true, false}},
		{name: "parked owns input without runnable work or lease", status: JobStatus("awaiting_start_control"), valid: true, expected: [4]bool{false, false, true, false}},
		{name: "succeeded is terminal only", status: JobStatusSucceeded, valid: true, expected: [4]bool{false, false, false, true}},
		{name: "failed is terminal only", status: JobStatusFailed, valid: true, expected: [4]bool{false, false, false, true}},
		{name: "cancelled is terminal only", status: JobStatusCancelled, valid: true, expected: [4]bool{false, false, false, true}},
		{name: "dead letter is terminal only", status: JobStatusDeadLetter, valid: true, expected: [4]bool{false, false, false, true}},
		{name: "unset must be rejected before interpretation"},
		{name: "future status must be rejected before interpretation", status: JobStatus("future")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.status.Validate()
			if tt.valid {
				require.NoError(t, err, "the exact supported status must have a defined lifecycle policy")
			} else {
				require.Error(t, err, "unknown status must not silently acquire a lifecycle policy")
			}
			actual := [4]bool{tt.status.IsRunnable(), tt.status.IsLeaseOwned(), tt.status.IsNonterminalInputConsumer(), tt.status.IsTerminal()}
			require.Equal(t, tt.expected, actual, "runnable, lease, input ownership and terminal policy must remain separate")
		})
	}
}
