package db

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestLifecycleConnectionApplicationNamePostgres(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for actual connection-label proof")
	}
	const buildSHA = "0123456789aBcDeF0123456789aBcDeF01234567"
	tests := []struct {
		name     string
		role     models.LifecycleWriterRole
		operator bool
		expected string
	}{
		{name: "api", role: models.LifecycleWriterRoleAPI, expected: "143:api:" + buildSHA},
		{name: "worker", role: models.LifecycleWriterRoleWorker, expected: "143:worker:" + buildSHA},
		{name: "all", role: models.LifecycleWriterRoleAll, expected: "143:all:" + buildSHA},
		{name: "session_executor", role: models.LifecycleWriterRoleSessionExecutor, expected: "143:session_executor:" + buildSHA},
		{name: "connection-only operator", operator: true, expected: "143:operator:" + buildSHA},
		{name: "unattributed caller preserves DSN name", expected: "synthetic-existing-name"},
		{name: "unsupported role preserves DSN name", role: models.LifecycleWriterRole("unsupported"), expected: "synthetic-existing-name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			admin, err := pgx.Connect(ctx, databaseURL)
			require.NoError(t, err, "connect to disposable PostgreSQL for PID-scoped connection evidence")
			defer func() {
				require.NoError(t, admin.Close(context.Background()), "close this case's inspection connection")
			}()
			var serverVersion string
			require.NoError(t, admin.QueryRow(ctx, "SHOW server_version_num").Scan(&serverVersion), "read the actual PostgreSQL server version")
			versionNumber, err := strconv.Atoi(serverVersion)
			require.NoError(t, err, "PostgreSQL must report its numeric server version")
			require.GreaterOrEqual(t, versionNumber, 150000, "connection-label proof requires PostgreSQL 15 or newer")

			isolatedURL, err := url.Parse(databaseURL)
			require.NoError(t, err, "parse the disposable PostgreSQL URL")
			query := isolatedURL.Query()
			query.Set("application_name", "synthetic-existing-name")
			query.Set("pool_min_conns", "0")
			query.Set("pool_min_idle_conns", "0")
			isolatedURL.RawQuery = query.Encode()
			applicationName := LifecycleConnectionApplicationName(tt.role, buildSHA)
			if tt.operator {
				applicationName = LifecycleOperatorConnectionApplicationName(buildSHA)
			}

			// Holding both acquisitions at once forces two distinct physical
			// connections, without relying on background pool maintenance.
			func() {
				pool, err := NewPoolWithOptions(ctx, isolatedURL.String(), PoolOptions{MaxConns: 2, ApplicationName: applicationName})
				require.NoError(t, err, "open the actual production pool with passive attribution")
				defer pool.Close()
				first, err := pool.Acquire(ctx)
				require.NoError(t, err, "acquire the first production-pool connection")
				defer first.Release()
				second, err := pool.Acquire(ctx)
				require.NoError(t, err, "force a second fresh production-pool connection")
				defer second.Release()
				firstPID := requireLifecycleConnectionState(t, ctx, admin, first, tt.expected)
				secondPID := requireLifecycleConnectionState(t, ctx, admin, second, tt.expected)
				require.NotEqual(t, firstPID, secondPID, "simultaneous acquisitions must establish two distinct attributed backends")
			}()

			// A one-connection pool makes reuse deterministic. Reset retires
			// the idle backend, so the following acquire proves startup labels
			// are applied to replacement connections as well as reused ones.
			pool, err := NewPoolWithOptions(ctx, isolatedURL.String(), PoolOptions{MaxConns: 1, ApplicationName: applicationName})
			require.NoError(t, err, "open a one-connection production pool for deterministic reuse")
			defer pool.Close()
			first, err := pool.Acquire(ctx)
			require.NoError(t, err, "acquire the warmed single-pool connection")
			defer first.Release()
			firstPID := requireLifecycleConnectionState(t, ctx, admin, first, tt.expected)
			first.Release()
			reused, err := pool.Acquire(ctx)
			require.NoError(t, err, "reacquire the only available physical connection")
			defer reused.Release()
			reusedPID := requireLifecycleConnectionState(t, ctx, admin, reused, tt.expected)
			require.Equal(t, firstPID, reusedPID, "release and reacquire must preserve the same backend and its passive name")
			reused.Release()
			pool.Reset()
			replacement, err := pool.Acquire(ctx)
			require.NoError(t, err, "establish a replacement connection after resetting the pool")
			defer replacement.Release()
			replacementPID := requireLifecycleConnectionState(t, ctx, admin, replacement, tt.expected)
			require.NotEqual(t, firstPID, replacementPID, "pool reset must create a fresh backend with the same startup label")
		})
	}
}

func requireLifecycleConnectionState(t *testing.T, ctx context.Context, admin *pgx.Conn, connection *pgxpool.Conn, expectedName string) int32 {
	t.Helper()
	var pid int32
	var applicationName string
	var writerVersion *string
	err := connection.QueryRow(ctx, `SELECT pg_backend_pid(), current_setting('application_name'), current_setting('lifecycle143.writer_version', true)`).Scan(&pid, &applicationName, &writerVersion)
	require.NoError(t, err, "read startup name and writer-version state on the actual acquired connection")
	require.Equal(t, expectedName, applicationName, "the connection must advertise exactly the selected passive or inherited name")
	require.Nil(t, writerVersion, "passive connection attribution must not inject a transaction writer-version setting")
	var observedName string
	err = admin.QueryRow(ctx, `SELECT application_name FROM pg_stat_activity WHERE datname=current_database() AND pid=$1`, pid).Scan(&observedName)
	require.NoError(t, err, "inspect only this acquired backend rather than every process sharing its role and build")
	require.Equal(t, expectedName, observedName, "PostgreSQL activity must expose the exact passive label for this backend PID")
	return pid
}
