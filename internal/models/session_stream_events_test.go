package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionStreamEventType_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionStreamEventType
		expectErr bool
	}{
		{name: "thread inbox queued", value: SessionStreamEventThreadInboxQueued},
		{name: "thread inbox cleared", value: SessionStreamEventThreadInboxCleared},
		{name: "thread runtime updated", value: SessionStreamEventThreadRuntimeUpdated},
		{name: "workspace generation changed", value: SessionStreamEventWorkspaceGenerationChanged},
		{name: "invalid", value: SessionStreamEventType("bad"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown session stream event types")
				return
			}
			require.NoError(t, err, "Validate should accept known session stream event types")
		})
	}
}
