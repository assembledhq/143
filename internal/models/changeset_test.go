package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChangesetStatusValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    ChangesetStatus
		expectErr bool
	}{
		{name: "planned", status: ChangesetStatusPlanned},
		{name: "needs restack", status: ChangesetStatusNeedsRestack},
		{name: "external update", status: ChangesetStatusExternalUpdateDetected},
		{name: "invalid", status: ChangesetStatus("unknown"), expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid changeset status should be rejected")
				return
			}
			require.NoError(t, err, "known changeset status should be accepted")
		})
	}
}
