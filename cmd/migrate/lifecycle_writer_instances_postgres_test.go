package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestLifecycleWriterInstanceMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh full schema"},
		{name: "existing executor upgrade rollback and reapply", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			boundary := uint(319)
			if tt.upgrade {
				boundary = 318
			}
			require.NoError(t, migration.Migrate(boundary), "apply the complete real schema to the selected writer-instance boundary")
			fixture := newExecutorWriterFixture(t, ctx, conn)
			fixture.insertLegacyExecutor(t, ctx, conn, models.SessionExecutorStatusDraining, models.DrainIntentPlannedRollout)
			_, err := conn.Exec(ctx, `UPDATE session_executors SET lifecycle_writer_version=7 WHERE id=$1`, fixture.id)
			require.NoError(t, err, "retain synthetic future executor metadata across the additive process registry migration")
			before := executorWriterRowJSON(t, ctx, conn, fixture.id, false)
			if tt.upgrade {
				require.NoError(t, migration.Migrate(319), "upgrade the complete existing schema without changing executor data")
			}
			assertBoundary := func(wantVersion uint, registryPresent bool) {
				version, dirty, err := migration.Version()
				require.NoError(t, err, "inspect the actual writer-instance migration ledger")
				require.Equal(t, wantVersion, version, "registry upgrade and rollback must stop at their exact historical boundaries")
				require.False(t, dirty, "a successful registry transition must leave the migration ledger clean")
				require.JSONEq(t, string(before), string(executorWriterRowJSON(t, ctx, conn, fixture.id, false)), "registry transitions must preserve every existing executor field including future metadata")
				var exists bool
				require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass('public.lifecycle_writer_instances') IS NOT NULL`).Scan(&exists), "inspect registry existence independently of its store")
				require.Equal(t, registryPresent, exists, "rollback must remove only the newly added process registry")
			}
			assertBoundary(319, true)
			assertLifecycleWriterInstanceSchema(t, ctx, conn)
			require.NoError(t, migration.Steps(-1), "roll back only the writer-instance registry")
			assertBoundary(318, false)
			require.NoError(t, migration.Migrate(319), "reapply the process registry after an application-first rollback")
			assertBoundary(319, true)
			assertLifecycleWriterInstanceSchema(t, ctx, conn)
		})
	}
}

func assertLifecycleWriterInstanceSchema(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	var columns []string
	require.NoError(t, conn.QueryRow(ctx, `SELECT array_agg(column_name::text || ':' || data_type || ':' || is_nullable ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name='lifecycle_writer_instances'`).Scan(&columns), "inspect every registry column and its nullability in the real schema")
	require.Equal(t, []string{
		"instance_id:uuid:NO", "role:text:NO", "build_sha:text:NO", "lifecycle_writer_version:integer:NO", "last_heartbeat:timestamp with time zone:NO", "expires_at:timestamp with time zone:NO",
	}, columns, "the global registry must contain only immutable process identity and passive lease observations")
	var defaultVersion string
	require.NoError(t, conn.QueryRow(ctx, `SELECT column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='lifecycle_writer_instances' AND column_name='lifecycle_writer_version'`).Scan(&defaultVersion), "inspect the registry's compatibility default")
	require.Equal(t, "0", defaultVersion, "omitted writer metadata must default to observational version zero")
	var primaryKeyColumns []string
	require.NoError(t, conn.QueryRow(ctx, `SELECT array_agg(a.attname::text ORDER BY key.ordinality) FROM pg_constraint c CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY key(attnum,ordinality) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=key.attnum WHERE c.conrelid='lifecycle_writer_instances'::regclass AND c.contype='p'`).Scan(&primaryKeyColumns), "inspect the actual process identity primary key")
	require.Equal(t, []string{"instance_id"}, primaryKeyColumns, "a process UUID, not a reusable node or allocation identity, must own each observation")
	var indexedColumns []string
	require.NoError(t, conn.QueryRow(ctx, `SELECT array_agg(a.attname::text ORDER BY key.ordinality) FROM pg_index i CROSS JOIN LATERAL unnest(i.indkey::smallint[]) WITH ORDINALITY key(attnum,ordinality) JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=key.attnum WHERE i.indrelid='lifecycle_writer_instances'::regclass AND NOT i.indisprimary`).Scan(&indexedColumns), "inspect every non-primary registry index column")
	require.Equal(t, []string{"expires_at", "instance_id"}, indexedColumns, "bounded expired-row cleanup must have one deterministic expiry-and-identity index")
	var foreignKeys int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='lifecycle_writer_instances'::regclass AND contype='f'`).Scan(&foreignKeys), "inspect whether the global observation table invents tenant or executor ownership")
	require.Equal(t, 0, foreignKeys, "process observations must not become an executor or tenant execution ledger")
	id := uuid.New()
	_, err := conn.Exec(ctx, `INSERT INTO lifecycle_writer_instances(instance_id,role,build_sha,last_heartbeat,expires_at) VALUES($1,'session_executor','synthetic-build',statement_timestamp(),statement_timestamp()+interval '2 minutes')`, id)
	require.NoError(t, err, "legacy omitted-version inserts must remain valid in the additive registry")
	before := lifecycleWriterInstanceRowJSON(t, ctx, conn, id)
	constraints := []struct {
		name, set, code string
	}{
		{name: "zero process identity", set: "instance_id='00000000-0000-0000-0000-000000000000'", code: "23514"},
		{name: "null process identity", set: "instance_id=NULL", code: "23502"},
		{name: "negative writer version", set: "lifecycle_writer_version=-1", code: "23514"},
		{name: "null writer version", set: "lifecycle_writer_version=NULL", code: "23502"},
		{name: "null role", set: "role=NULL", code: "23502"},
		{name: "empty role", set: "role=''", code: "23514"},
		{name: "blank role", set: "role='   '", code: "23514"},
		{name: "oversized role", set: "role=repeat('r',65)", code: "23514"},
		{name: "oversized multibyte role", set: "role=repeat(chr(233),33)", code: "23514"},
		{name: "null build", set: "build_sha=NULL", code: "23502"},
		{name: "empty build", set: "build_sha=''", code: "23514"},
		{name: "blank build", set: "build_sha='   '", code: "23514"},
		{name: "oversized build", set: "build_sha=repeat('b',129)", code: "23514"},
		{name: "oversized multibyte build", set: "build_sha=repeat(chr(233),65)", code: "23514"},
		{name: "null heartbeat", set: "last_heartbeat=NULL", code: "23502"},
		{name: "null expiry", set: "expires_at=NULL", code: "23502"},
		{name: "equal heartbeat and expiry", set: "expires_at=last_heartbeat", code: "23514"},
		{name: "expiry before heartbeat", set: "expires_at=last_heartbeat-interval '1 second'", code: "23514"},
	}
	// Constraint probes intentionally share one row within an isolated database;
	// each rejected autocommit statement must leave the entire row unchanged.
	for _, constraint := range constraints {
		_, err := conn.Exec(ctx, `UPDATE lifecycle_writer_instances SET `+constraint.set+` WHERE instance_id=$1`, id)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, constraint.name+" must be rejected by PostgreSQL rather than only by application validation")
		require.Equal(t, constraint.code, pgErr.Code, constraint.name+" must fail for its intended database constraint")
		require.JSONEq(t, string(before), string(lifecycleWriterInstanceRowJSON(t, ctx, conn, id)), constraint.name+" must preserve the complete original observation")
	}
}

func TestLifecycleWriterInstanceObserveFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, false)
	require.NoError(t, migration.Migrate(319), "run actual observation SQL against the complete schema")
	config := conn.Config().Copy()
	tests := []struct {
		name, build, conflict string
		restart               bool
	}{
		{name: "published binary identity", build: "synthetic-build"},
		{name: "development binary identity", build: "dev"},
		{name: "distinct process restart identity", build: "synthetic-build", restart: true},
		{name: "immutable role mismatch", build: "synthetic-build", conflict: "role"},
		{name: "immutable build mismatch", build: "synthetic-build", conflict: "build_sha"},
		{name: "future version mismatch", build: "synthetic-build", conflict: "lifecycle_writer_version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			actor := newLifecycleWriterInstanceConnection(t, ctx, config)
			store := db.NewLifecycleWriterInstanceStore(actor)
			id := uuid.New()
			var beforeClock time.Time
			require.NoError(t, actor.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&beforeClock), "sample the database clock before this process observation")
			require.NoError(t, store.Observe(ctx, id, models.LifecycleWriterRoleSessionExecutor, tt.build), "publish a standalone executor's actual process identity at fixed version zero")
			assertLifecycleWriterInstanceObservation(t, ctx, actor, id, tt.build, beforeClock)
			if tt.conflict != "" {
				switch tt.conflict {
				case "role":
					_, err := actor.Exec(ctx, `UPDATE lifecycle_writer_instances SET role='synthetic_future_role' WHERE instance_id=$1`, id)
					require.NoError(t, err, "seed a valid future role without interpreting it as a current binary role")
				case "build_sha":
					_, err := actor.Exec(ctx, `UPDATE lifecycle_writer_instances SET build_sha='synthetic-other-build' WHERE instance_id=$1`, id)
					require.NoError(t, err, "seed a different immutable process build")
				case "lifecycle_writer_version":
					_, err := actor.Exec(ctx, `UPDATE lifecycle_writer_instances SET lifecycle_writer_version=7 WHERE instance_id=$1`, id)
					require.NoError(t, err, "seed unknown nonnegative metadata without advertising it from this binary")
				}
				before := lifecycleWriterInstanceRowJSON(t, ctx, actor, id)
				err := store.Observe(ctx, id, models.LifecycleWriterRoleSessionExecutor, tt.build)
				require.ErrorIs(t, err, db.ErrLifecycleWriterInstanceIdentityMismatch, "a reused process identity must not overwrite a different role, build, or writer version")
				require.JSONEq(t, string(before), string(lifecycleWriterInstanceRowJSON(t, ctx, actor, id)), "identity mismatch must preserve every field including heartbeat and expiry")
				return
			}
			_, err := actor.Exec(ctx, `UPDATE lifecycle_writer_instances SET last_heartbeat=statement_timestamp()-interval '8 days 2 minutes',expires_at=statement_timestamp()-interval '8 days' WHERE instance_id=$1`, id)
			require.NoError(t, err, "expire the matching process observation without changing immutable identity")
			require.NoError(t, actor.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&beforeClock), "bound lease renewal by a fresh database-clock sample")
			require.NoError(t, store.Observe(ctx, id, models.LifecycleWriterRoleSessionExecutor, tt.build), "a matching process may renew an expired observation without an execution-lifecycle gate")
			assertLifecycleWriterInstanceObservation(t, ctx, actor, id, tt.build, beforeClock)
			ids := []uuid.UUID{id}
			if tt.restart {
				restartedID := uuid.New()
				require.NoError(t, store.Observe(ctx, restartedID, models.LifecycleWriterRoleSessionExecutor, tt.build), "a restarted process must create its own UUID rather than take over the previous process row")
				ids = append(ids, restartedID)
			}
			var count int
			require.NoError(t, actor.QueryRow(ctx, `SELECT count(*) FROM lifecycle_writer_instances WHERE instance_id=ANY($1::uuid[])`, ids).Scan(&count), "count only this case's independently owned process rows")
			require.Equal(t, len(ids), count, "same-process renewal must retain one row and a restart must retain both separate identities")
		})
	}
}

func TestLifecycleWriterInstanceCleanupFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		locked bool
	}{
		{name: "bounded retention preserves recent expired and live observations"},
		{name: "concurrent renewal row lock is skipped", locked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, actor := newContentMigrationPostgres(t, false)
			require.NoError(t, migration.Migrate(319), "run actual retention SQL against a fresh complete schema")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			store := db.NewLifecycleWriterInstanceStore(actor)
			if tt.locked {
				id := uuid.New()
				_, err := actor.Exec(ctx, `INSERT INTO lifecycle_writer_instances(instance_id,role,build_sha,last_heartbeat,expires_at) VALUES($1,'session_executor','synthetic-build',statement_timestamp()-interval '8 days 2 minutes',statement_timestamp()-interval '8 days')`, id)
				require.NoError(t, err, "seed one old expired observation eligible for retention cleanup")
				locker := newLifecycleWriterInstanceConnection(t, ctx, actor.Config())
				tx, err := locker.Begin(ctx)
				require.NoError(t, err, "open a separate process renewal transaction")
				defer func() {
					closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer closeCancel()
					err := tx.Rollback(closeCtx)
					if err != nil {
						require.ErrorIs(t, err, pgx.ErrTxClosed, "a completed renewal must not leave an open transaction")
					}
				}()
				require.NoError(t, db.NewLifecycleWriterInstanceStore(tx).Observe(ctx, id, models.LifecycleWriterRoleSessionExecutor, "synthetic-build"), "hold the row lock with a real uncommitted process renewal")
				attemptCtx, attemptCancel := context.WithTimeout(ctx, 2*time.Second)
				deleted, err := store.DeleteExpired(attemptCtx)
				attemptCancel()
				require.NoError(t, err, "cleanup must skip the locked old row instead of waiting for concurrent renewal")
				require.Equal(t, int64(0), deleted, "an old snapshot must not delete a concurrently renewed observation")
				require.NoError(t, tx.Commit(ctx), "make the process renewal visible after cleanup has skipped its row lock")
				before := lifecycleWriterInstanceRowJSON(t, ctx, actor, id)
				deleted, err = store.DeleteExpired(ctx)
				require.NoError(t, err, "cleanup must remain valid after the concurrent renewal commits")
				require.Equal(t, int64(0), deleted, "the committed live lease must no longer qualify for expired retention")
				require.JSONEq(t, string(before), string(lifecycleWriterInstanceRowJSON(t, ctx, actor, id)), "cleanup must preserve the complete renewed observation")
				return
			}
			_, err := actor.Exec(ctx, `INSERT INTO lifecycle_writer_instances(instance_id,role,build_sha,lifecycle_writer_version,last_heartbeat,expires_at) SELECT md5('synthetic-old-process-'||n::text)::uuid,'session_executor','synthetic-build',CASE WHEN n=1 THEN 7 ELSE 0 END,statement_timestamp()-interval '8 days 2 minutes',statement_timestamp()-interval '8 days' FROM generate_series(1,1001) n`)
			require.NoError(t, err, "seed more eligible old rows than one cleanup sweep may delete, including observational future metadata")
			preserved := []uuid.UUID{uuid.New(), uuid.New()}
			_, err = actor.Exec(ctx, `INSERT INTO lifecycle_writer_instances(instance_id,role,build_sha,lifecycle_writer_version,last_heartbeat,expires_at) VALUES($1,'session_executor','synthetic-recent',0,statement_timestamp()-interval '6 days 2 minutes',statement_timestamp()-interval '6 days'),($2,'synthetic_future_role','synthetic-live',7,statement_timestamp(),statement_timestamp()+interval '2 minutes')`, preserved[0], preserved[1])
			require.NoError(t, err, "retain one recently expired observation and one live unknown-version observation")
			before := []json.RawMessage{lifecycleWriterInstanceRowJSON(t, ctx, actor, preserved[0]), lifecycleWriterInstanceRowJSON(t, ctx, actor, preserved[1])}
			for round, wantDeleted := range []int64{1000, 1, 0} {
				deleted, err := store.DeleteExpired(ctx)
				require.NoError(t, err, "bounded retention sweep must complete without interpreting passive compatibility metadata")
				require.Equal(t, wantDeleted, deleted, "each sweep must delete only its bounded share of seven-day-expired observations")
				var remaining int
				require.NoError(t, actor.QueryRow(ctx, `SELECT count(*) FROM lifecycle_writer_instances`).Scan(&remaining), "count every remaining observation after the bounded sweep")
				wantRemaining := 2
				if round == 0 {
					wantRemaining = 3
				}
				require.Equal(t, wantRemaining, remaining, "retention must keep every ineligible row while deleting no more than 1000 eligible rows")
			}
			for i, id := range preserved {
				require.JSONEq(t, string(before[i]), string(lifecycleWriterInstanceRowJSON(t, ctx, actor, id)), "cleanup must preserve the entire recent or live row including unknown role and version metadata")
			}
		})
	}
}

func TestLifecycleWriterInstanceBackpressureFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, false)
	require.NoError(t, migration.Migrate(319), "test cancellation and pool saturation on the complete registry schema")
	config := conn.Config().Copy()
	tests := []struct {
		name, operation string
		cancelled       bool
		rowLocked       bool
	}{
		{name: "cancelled observation", operation: "observe", cancelled: true},
		{name: "cancelled cleanup", operation: "cleanup", cancelled: true},
		{name: "saturated ordinary pool observation", operation: "observe"},
		{name: "saturated ordinary pool cleanup", operation: "cleanup"},
		{name: "cancelled row-lock observation releases acquired client", operation: "observe", rowLocked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			actor := newLifecycleWriterInstanceConnection(t, ctx, config)
			id := uuid.New()
			require.NoError(t, db.NewLifecycleWriterInstanceStore(actor).Observe(ctx, id, models.LifecycleWriterRoleSessionExecutor, "synthetic-build"), "seed a real observation before bounded backpressure")
			before := lifecycleWriterInstanceRowJSON(t, ctx, actor, id)
			poolConfig, err := pgxpool.ParseConfig(actor.Config().ConnString())
			require.NoError(t, err, "configure a separate ordinary database pool for this case")
			poolConfig.MaxConns, poolConfig.MinConns = 1, 0
			pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
			require.NoError(t, err, "construct an isolated one-client ordinary pool")
			t.Cleanup(pool.Close)
			var backendPID int
			require.NoError(t, pool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID), "prewarm the ordinary pool so backpressure probes reach an established database connection")
			require.Positive(t, backendPID, "the isolated ordinary client must have a real PostgreSQL backend")
			var held *pgxpool.Conn
			var lockTx pgx.Tx
			var observer *pgx.Conn
			if tt.rowLocked {
				observer = newLifecycleWriterInstanceConnection(t, ctx, config)
				lockTx, err = actor.Begin(ctx)
				require.NoError(t, err, "hold a real row lock before an observation acquires its pool client")
				var lockedID uuid.UUID
				require.NoError(t, lockTx.QueryRow(ctx, `SELECT instance_id FROM lifecycle_writer_instances WHERE instance_id=$1 FOR UPDATE`, id).Scan(&lockedID), "synchronize the blocked observation with a confirmed row lock")
				require.Equal(t, id, lockedID, "the blocking transaction must own only this process observation")
				defer func() {
					closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer closeCancel()
					err := lockTx.Rollback(closeCtx)
					if err != nil {
						require.ErrorIs(t, err, pgx.ErrTxClosed, "a cancelled observation must not leak the blocking transaction")
					}
				}()
			} else if !tt.cancelled {
				held, err = pool.Acquire(ctx)
				require.NoError(t, err, "reserve the sole client before testing ordinary-pool saturation")
				defer held.Release()
			}
			attemptCtx, attemptCancel := context.WithTimeout(ctx, 200*time.Millisecond)
			if tt.cancelled {
				attemptCancel()
			}
			started := time.Now()
			store := db.NewLifecycleWriterInstanceStore(pool)
			if tt.operation == "observe" {
				err = store.Observe(attemptCtx, id, models.LifecycleWriterRoleSessionExecutor, "synthetic-build")
			} else {
				var deleted int64
				deleted, err = store.DeleteExpired(attemptCtx)
				require.Equal(t, int64(0), deleted, "a cancelled cleanup attempt must not claim any completed deletions")
			}
			attemptCancel()
			wantErr := context.DeadlineExceeded
			if tt.cancelled {
				wantErr = context.Canceled
			}
			require.ErrorIs(t, err, wantErr, "the caller's bounded attempt context must interrupt passive observation work")
			require.Less(t, time.Since(started), 3*time.Second, "passive telemetry must not remain blocked behind an ordinary pool or row lock")
			if held != nil {
				held.Release()
			}
			// pgx destroys a cancelled connection asynchronously. Prove the sole
			// pool slot becomes reusable within a deadline instead of assuming its
			// acquired-resource counter drops synchronously with Release.
			recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 3*time.Second)
			recovered, err := pool.Acquire(recoveryCtx)
			require.NoError(t, err, "the sole ordinary-pool slot must become reusable while any blocking row lock remains held")
			var one int
			err = recovered.QueryRow(recoveryCtx, `SELECT 1`).Scan(&one)
			recovered.Release()
			recoveryCancel()
			require.NoError(t, err, "a replacement or returned client must execute independent work before the blocker is released")
			require.Equal(t, 1, one, "bounded pool recovery must return the exact synthetic query result")
			require.Equal(t, int32(0), pool.Stat().AcquiredConns(), "the successful replacement-client verification must return its acquired resource")
			if lockTx != nil {
				// Cancellation may close the client asynchronously. Keep the row
				// locked until PostgreSQL confirms the timed-out statement is no
				// longer active, so releasing our lock cannot race cancellation.
				cancelledCtx, cancelledCancel := context.WithTimeout(ctx, 2*time.Second)
				for {
					var active bool
					require.NoError(t, observer.QueryRow(cancelledCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND state='active')`, backendPID).Scan(&active), "confirm the timed-out statement has left its PostgreSQL backend before releasing the row lock")
					if !active {
						break
					}
				}
				cancelledCancel()
				require.NoError(t, lockTx.Rollback(ctx), "release only this case's row lock after the observation has returned")
			}
			require.JSONEq(t, string(before), string(lifecycleWriterInstanceRowJSON(t, ctx, actor, id)), "a cancelled or saturated-pool attempt must not refresh or corrupt the process observation")
		})
	}
}

func newLifecycleWriterInstanceConnection(t *testing.T, ctx context.Context, config *pgx.ConnConfig) *pgx.Conn {
	t.Helper()
	conn, err := pgx.ConnectConfig(ctx, config.Copy())
	require.NoError(t, err, "open a connection owned only by this writer-instance probe")
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		require.NoError(t, conn.Close(closeCtx), "close only this probe's process-observation connection")
	})
	return conn
}

func lifecycleWriterInstanceRowJSON(t *testing.T, ctx context.Context, conn *pgx.Conn, id uuid.UUID) json.RawMessage {
	t.Helper()
	var value json.RawMessage
	require.NoError(t, conn.QueryRow(ctx, `SELECT to_jsonb(w) FROM lifecycle_writer_instances w WHERE instance_id=$1`, id).Scan(&value), "inspect the complete process observation independently of application interpretation")
	return value
}

func assertLifecycleWriterInstanceObservation(t *testing.T, ctx context.Context, conn *pgx.Conn, id uuid.UUID, build string, beforeClock time.Time) {
	t.Helper()
	var actualID uuid.UUID
	var role, actualBuild string
	var version int
	var heartbeat, expiry, afterClock time.Time
	require.NoError(t, conn.QueryRow(ctx, `SELECT instance_id,role,build_sha,lifecycle_writer_version,last_heartbeat,expires_at,clock_timestamp() FROM lifecycle_writer_instances WHERE instance_id=$1`, id).Scan(&actualID, &role, &actualBuild, &version, &heartbeat, &expiry, &afterClock), "inspect every field of the database-written process observation")
	require.Equal(t, id, actualID, "observation must retain the caller's per-process UUID")
	require.Equal(t, string(models.LifecycleWriterRoleSessionExecutor), role, "observation must truthfully retain the standalone-executor process role")
	require.Equal(t, build, actualBuild, "observation must retain the actual binary build including the literal development build")
	require.Equal(t, 0, version, "this binary must advertise only passive writer version zero")
	require.Equal(t, heartbeat.Add(2*time.Minute), expiry, "the database clock must anchor the exact two-minute observation TTL")
	require.False(t, heartbeat.Before(beforeClock), "observation timestamp must not predate its database-clock lower bound")
	require.False(t, heartbeat.After(afterClock), "observation timestamp must not exceed its database-clock upper bound")
}
