package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycleWriterRole_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		role    LifecycleWriterRole
		wantErr string
	}{
		{name: "standalone session executor", role: LifecycleWriterRoleSessionExecutor},
		{name: "empty is invalid", wantErr: `invalid LifecycleWriterRole: ""`},
		{name: "unknown is invalid", role: "other", wantErr: `invalid LifecycleWriterRole: "other"`},
		{name: "unwired api is invalid", role: "api", wantErr: `invalid LifecycleWriterRole: "api"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.role.Validate()
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr, "only participating process roles may register observations")
				return
			}
			require.NoError(t, err, "standalone executor observations should use the typed registered role")
		})
	}
}
