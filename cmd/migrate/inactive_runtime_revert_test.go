package main

import (
	"context"
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

func TestRuntimeRevertMigrationPreflight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ledger  fakeDirtyMigrationRepairer
		refusal string
	}{
		{name: "fresh database", ledger: fakeDirtyMigrationRepairer{versionErr: migrate.ErrNilVersion}},
		{name: "older schema checked after upgrade", ledger: fakeDirtyMigrationRepairer{version: 345}},
		{name: "partial clean schema checked after upgrade", ledger: fakeDirtyMigrationRepairer{version: 391}},
		{name: "dirty schema rejected before writes", ledger: fakeDirtyMigrationRepairer{version: 392, dirty: true}, refusal: "dirty migration ledger"},
		{name: "unknown newer schema", ledger: fakeDirtyMigrationRepairer{version: 394}, refusal: "schema through 393 only"},
		{name: "ledger error", ledger: fakeDirtyMigrationRepairer{versionErr: errors.New("synthetic read failure")}, refusal: "synthetic read failure"},
		{name: "392 requires real inspection", ledger: fakeDirtyMigrationRepairer{version: 392}, refusal: "invalid rollback database connection settings"},
		{name: "393 requires real inspection", ledger: fakeDirtyMigrationRepairer{version: 393}, refusal: "invalid rollback database connection settings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := verifyRuntimeRevertBeforeUp(context.Background(), &tt.ledger, "postgres://invalid:%zz@localhost/test")
			if tt.refusal == "" {
				require.NoError(t, err, "only fresh or older clean schema may defer full inspection until after upgrade")
			} else {
				require.ErrorContains(t, err, tt.refusal, "unsupported or unreadable state must fail before migration")
			}
			require.False(t, tt.ledger.forceInvoked, "compatibility checks must never repair the migration ledger")
		})
	}
}
