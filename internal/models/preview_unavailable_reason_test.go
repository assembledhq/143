package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreviewUnavailableReason_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		reason    PreviewUnavailableReason
		expectErr bool
	}{
		{name: "none", reason: PreviewUnavailableReasonNone},
		{name: "owner lost", reason: PreviewUnavailableReasonOwnerLost},
		{name: "deploy drain timeout", reason: PreviewUnavailableReasonDeployDrainTimeout},
		{name: "host maintenance", reason: PreviewUnavailableReasonHostMaintenance},
		{name: "emergency force", reason: PreviewUnavailableReasonEmergencyForce},
		{name: "lease expired", reason: PreviewUnavailableReasonLeaseExpired},
		{name: "endpoint unreachable", reason: PreviewUnavailableReasonEndpointUnreachable},
		{name: "unknown", reason: PreviewUnavailableReason("bad"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.reason.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown preview unavailable reasons")
				return
			}
			require.NoError(t, err, "Validate should accept known preview unavailable reasons")
		})
	}
}
