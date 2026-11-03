package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
)

// The source revert keeps the migration history but removes participating
// writers. Refuse active or retained protocol state before upgrading an existing
// full lifecycle schema. Fresh/older installations are checked after migration,
// before the deployment can start the application. Activation must remain
// excluded throughout deployment; this read-only snapshot is not a global lock.
func verifyRuntimeRevertBeforeUp(ctx context.Context, ledger interface{ Version() (uint, bool, error) }, rawURL string) error {
	version, dirty, err := ledger.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read migration ledger: %w", err)
	}
	if dirty {
		return errors.New("dirty migration ledger")
	}
	if version > 393 {
		return errors.New("inactive runtime supports schema through 393 only")
	}
	if version < 392 {
		return nil
	}
	return verifyInactiveRuntimeSchema(ctx, rawURL, version)
}
