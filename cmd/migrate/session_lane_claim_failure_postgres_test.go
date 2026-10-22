package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgx/v5"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestSessionLaneClaimMigrationLockFailurePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, table string
		version     uint
	}{
		{name: "dispatch observation columns", table: "session_input_dispatches", version: 341},
		{name: "lane pointer and immediate constraints", table: "session_threads", version: 343},
		{name: "independent constraint validation", table: "session_threads", version: 344},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			require.NoError(t, migration.Migrate(tt.version-1), "construct the exact clean full-schema boundary before the lock-timeout failure")
			requireLaneClaimMigrationCatalog(t, ctx, conn, tt.version-1, false)
			_, actor := lifecycleReferenceProbeConnection(t, conn)
			locker, err := actor.Begin(ctx)
			require.NoError(t, err, "begin only the owned synthetic transaction that blocks this isolated migration")
			defer func() { _ = locker.Rollback(ctx) }()
			_, err = locker.Exec(ctx, `LOCK TABLE `+pgx.Identifier{"public", tt.table}.Sanitize()+` IN ACCESS EXCLUSIVE MODE`)
			require.NoError(t, err, "hold the real target-table lock before attempting the migration without sleeps or lock-timing guesses")
			err = migration.Migrate(tt.version)
			var migrationErr database.Error
			require.ErrorAs(t, err, &migrationErr, "the real migration must expose its failed DDL rather than report a successful clean boundary")
			var lockErr *pq.Error
			require.ErrorAs(t, migrationErr.OrigErr, &lockErr, "preserve the migration driver's original PostgreSQL lock-timeout cause")
			require.Equal(t, pq.ErrorCode("55P03"), lockErr.Code, "the existing five-second lock budget must fail through PostgreSQL lock_not_available rather than transaction-aborted or unrelated errors")
			require.Equal(t, "canceling statement due to lock timeout", lockErr.Message, "the real server must identify lock_timeout as the bounded failure rather than a client cancellation or NOWAIT shortcut")
			require.NoError(t, locker.Rollback(ctx), "release only this case's known synthetic blocker before checking failed-migration diagnostics")
			version, dirty, err := migration.Version()
			require.NoError(t, err, "the migration's own dedicated connection must remain readable after failed DDL rather than stay inside an aborted explicit transaction")
			require.Equal(t, tt.version, version, "the readable ledger must retain the exact attempted migration version")
			require.True(t, dirty, "lock failure must remain visibly dirty instead of falsely reporting clean rollback success")
			requireLaneClaimMigrationCatalog(t, ctx, conn, tt.version-1, false)
			recorder := &lifecycleReferenceDirtyRepairRecorder{Migrate: migration}
			repairedVersion, repaired, err := repairKnownDirtyMigration(recorder)
			require.EqualError(t, err, fmt.Sprintf("database is dirty at version %d; refusing repair because only versions 267 and 281 are allowlisted", tt.version), "known-dirty repair must refuse every new lane-claim DDL failure")
			require.Equal(t, uint(0), repairedVersion, "repair refusal must not fabricate a repaired migration version")
			require.False(t, repaired, "failed lane DDL must require explicit investigation rather than automatic repair")
			require.Equal(t, 0, recorder.forceCalls, "repair refusal must never call Force against the real failed ledger")
			var retryErr migrate.ErrDirty
			require.ErrorAs(t, migration.Migrate(tt.version), &retryErr, "ordinary retry must also preserve the readable dirty state without rerunning DDL")
			require.Equal(t, int(tt.version), retryErr.Version, "retry refusal must refer to the same exact failed lane-claim migration")
			version, dirty, err = migration.Version()
			require.NoError(t, err, "failed repair and retry must leave diagnostics readable on the same migration connection")
			require.Equal(t, tt.version, version, "no diagnostic or refusal path may move the attempted migration boundary")
			require.True(t, dirty, "diagnostic reads and refusal paths must not clear the real failure marker")
			requireLaneClaimMigrationCatalog(t, ctx, conn, tt.version-1, false)
		})
	}
}
