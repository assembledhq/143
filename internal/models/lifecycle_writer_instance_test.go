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
		{name: "api process", role: LifecycleWriterRoleAPI},
		{name: "worker process", role: LifecycleWriterRoleWorker},
		{name: "combined process", role: LifecycleWriterRoleAll},
		{name: "standalone session executor", role: LifecycleWriterRoleSessionExecutor},
		{name: "empty is invalid", wantErr: `invalid LifecycleWriterRole: ""`},
		{name: "unknown is invalid", role: "other", wantErr: `invalid LifecycleWriterRole: "other"`},
		{name: "role spelling is exact", role: "API", wantErr: `invalid LifecycleWriterRole: "API"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.role.Validate()
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr, "only participating process roles may register observations")
				return
			}
			require.NoError(t, err, "participating process observations should use a typed registered role")
		})
	}
}
