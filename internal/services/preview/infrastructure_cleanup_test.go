package preview

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestManagerResolveInfrastructureCleanup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		configured bool
		queryError bool
		eligible   bool
		expectErr  bool
	}{
		{name: "eligible decision returned", configured: true, eligible: true},
		{name: "active ownership protected", configured: true},
		{name: "lookup error fails closed", configured: true, queryError: true, expectErr: true},
		{name: "unconfigured manager fails closed", expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "initialize manager ownership query mock")
			defer mock.Close()
			owner := InfrastructureOwner{OrgID: uuid.New(), PreviewID: uuid.New(), SessionID: uuid.New(), Handle: "handle-1", WorkerNodeID: "worker-current"}
			invalid := owner
			invalid.PreviewID = uuid.Nil
			mgr := NewManager(ManagerConfig{Logger: zerolog.Nop()})
			if tt.configured {
				mgr.store = db.NewPreviewStore(mock)
				mgr.workerNodeID = "worker-current"
				expected := mock.ExpectQuery("SELECT c.owner_index").WithArgs(managerAnyArgs(7)...)
				if tt.queryError {
					expected.WillReturnError(errors.New("ownership lookup failed"))
				} else {
					expected.WillReturnRows(pgxmock.NewRows([]string{"owner_index", "eligible"}).AddRow(int64(1), tt.eligible))
				}
			}
			actual, err := mgr.ResolveInfrastructureCleanup(context.Background(), []InfrastructureOwner{owner, invalid})
			if tt.expectErr {
				require.Error(t, err, "failed resolver must surface its failure")
			} else {
				require.NoError(t, err, "configured ownership resolver should complete")
			}
			require.Equal(t, map[InfrastructureOwner]bool{owner: tt.eligible, invalid: false}, actual, "resolver must explicitly protect invalid identities and failed lookups")
			require.NoError(t, mock.ExpectationsWereMet(), "manager should issue at most one ownership query")
		})
	}
}

func managerAnyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}
