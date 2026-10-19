package lifecyclecensus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCollectDatabaseLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		failure        string
		shortDeadline  bool
		expiredParent  bool
		cancelAt       string
		rollbackError  bool
		closeError     bool
		alreadyClosed  bool
		wantOperations []string
		wantErrors     []string
		wantContextErr error
	}{
		{name: "successful collection shares one bounded context", wantOperations: []string{"connect", "begin", "writers", "connections", "rollback", "close"}},
		{name: "shorter parent deadline bounds every database operation", shortDeadline: true, wantOperations: []string{"connect", "begin", "writers", "connections", "rollback", "close"}},
		{name: "connection failure does not retry or start work", failure: "connect", wantOperations: []string{"connect"}, wantErrors: []string{"database connection failed"}},
		{name: "transaction setup failure closes the sole connection", failure: "begin", wantOperations: []string{"connect", "begin", "close"}, wantErrors: []string{"read-only transaction setup failed"}},
		{name: "writer failure skips activity then rolls back and closes", failure: "writers", wantOperations: []string{"connect", "begin", "writers", "rollback", "close"}, wantErrors: []string{"writer observation failed"}},
		{name: "activity failure discards the earlier writer snapshot", failure: "connections", wantOperations: []string{"connect", "begin", "writers", "connections", "rollback", "close"}, wantErrors: []string{"connection observation failed"}},
		{name: "rollback failure invalidates an otherwise successful report", rollbackError: true, wantOperations: []string{"connect", "begin", "writers", "connections", "rollback", "close"}, wantErrors: []string{"read-only transaction cleanup failed"}},
		{name: "close failure invalidates an otherwise successful report", closeError: true, wantOperations: []string{"connect", "begin", "writers", "connections", "rollback", "close"}, wantErrors: []string{"database connection cleanup failed"}},
		{name: "both cleanup failures are preserved without private driver messages", rollbackError: true, closeError: true, wantOperations: []string{"connect", "begin", "writers", "connections", "rollback", "close"}, wantErrors: []string{"read-only transaction cleanup failed", "database connection cleanup failed"}},
		{name: "already closed transaction permits a completed report", alreadyClosed: true, wantOperations: []string{"connect", "begin", "writers", "connections", "rollback", "close"}},
		{name: "query and cleanup errors all invalidate partial observations", failure: "writers", rollbackError: true, closeError: true, wantOperations: []string{"connect", "begin", "writers", "rollback", "close"}, wantErrors: []string{"writer observation failed", "read-only transaction cleanup failed", "database connection cleanup failed"}},
		{name: "canceled parent preserves cancellation without driver data", cancelAt: "connect", wantOperations: []string{"connect"}, wantErrors: []string{"database connection failed"}, wantContextErr: context.Canceled},
		{name: "expired parent preserves deadline without attempting a connection", expiredParent: true, wantOperations: []string{}, wantErrors: []string{"observation canceled"}, wantContextErr: context.DeadlineExceeded},
		{name: "activity cancellation still uses independent cleanup contexts", cancelAt: "connections", wantOperations: []string{"connect", "begin", "writers", "connections", "rollback", "close"}, wantErrors: []string{"connection observation failed"}, wantContextErr: context.Canceled},
		{name: "cancellation after the final read cannot return a successful report", cancelAt: "after connections", wantOperations: []string{"connect", "begin", "writers", "connections", "rollback", "close"}, wantErrors: []string{"observation canceled"}, wantContextErr: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const databaseURL = "postgres://synthetic_user:synthetic_password@synthetic.example.test/synthetic_database?application_name=untrusted_name&statement_timeout=0&lock_timeout=0&APPLICATION_NAME=untrusted_uppercase&STATEMENT_TIMEOUT=0&LOCK_TIMEOUT=0&options=-c%20statement_timeout%3D0&OPTIONS=-c%20lock_timeout%3D0"
			privateFailure := errors.New("synthetic driver failure: " + databaseURL + " SELECT synthetic_private_query")
			mock, err := pgxmock.NewConn()
			require.NoError(t, err, "create an isolated direct-connection census fixture")
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.shortDeadline || tt.expiredParent {
				deadline := time.Now().Add(3 * time.Second)
				if tt.expiredParent {
					deadline = time.Now().Add(-time.Second)
				}
				var cancelDeadline context.CancelFunc
				parent, cancelDeadline = context.WithDeadline(parent, deadline)
				defer cancelDeadline()
			}
			writerClock := time.Date(2026, time.October, 19, 12, 0, 0, 0, time.UTC)
			activityClock := writerClock.Add(250 * time.Millisecond)
			if tt.failure != "connect" && tt.cancelAt != "connect" && !tt.expiredParent {
				begin := mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
				if tt.failure == "begin" {
					begin.WillReturnError(privateFailure)
				} else {
					writers := mock.ExpectQuery(`^SELECT statement_timestamp\(\) AS observed_at,`)
					if tt.failure == "writers" {
						writers.WillReturnError(privateFailure)
					} else {
						writers.WillReturnRows(pgxmock.NewRows([]string{"observed_at", "present", "role", "build_sha", "version"}).
							AddRow(writerClock, false, nil, nil, nil)).RowsWillBeClosed()
						connections := mock.ExpectQuery(`^WITH observation AS MATERIALIZED \(`)
						if tt.failure == "connections" || tt.cancelAt == "connections" {
							connections.WillReturnError(privateFailure)
							if tt.cancelAt == "connections" {
								connections.WillDelayFor(time.Second)
							}
						} else {
							fullVisibility := false
							connections.WillReturnRows(pgxmock.NewRows([]string{"observed_at", "full_visibility", "present", "application_name", "backend_type", "state", "xact_start", "details_visible"}).
								AddRow(activityClock, &fullVisibility, false, nil, nil, nil, nil, false)).RowsWillBeClosed()
						}
					}
					rollback := mock.ExpectRollback()
					if tt.rollbackError {
						rollback.WillReturnError(privateFailure)
					} else if tt.alreadyClosed {
						rollback.WillReturnError(pgx.ErrTxClosed)
					}
				}
				closeExpectation := mock.ExpectClose()
				if tt.closeError {
					closeExpectation.WillReturnError(privateFailure)
				}
			}
			operations := []string{}
			var collectionContext context.Context
			observeContext := func(operation string, ctx context.Context) {
				operations = append(operations, operation)
				deadline, hasDeadline := ctx.Deadline()
				require.True(t, hasDeadline, "each database and cleanup operation must have a cooperative deadline")
				if operation == "rollback" || operation == "close" {
					require.NoError(t, ctx.Err(), "cleanup must remain usable even after the collection context is canceled")
					require.NotSame(t, collectionContext, ctx, "each cleanup must use its own context rather than reuse the collection budget")
					require.LessOrEqual(t, time.Until(deadline), 250*time.Millisecond, "cleanup must retain its separate 250ms cooperative bound")
					return
				}
				if operation == "connect" {
					collectionContext = ctx
					require.LessOrEqual(t, time.Until(deadline), 5*time.Second, "connect and both reads must share at most the five-second collection budget")
					if tt.shortDeadline || tt.expiredParent {
						parentDeadline, ok := parent.Deadline()
						require.True(t, ok, "the fixture must supply the shorter parent deadline")
						require.Equal(t, parentDeadline, deadline, "collection must preserve the parent's earlier deadline")
					}
				} else {
					require.Same(t, collectionContext, ctx, "setup and both sequential reads must share the exact connection-establishment context")
				}
			}
			connection := &observedCensusTestConnection{mock: mock, observeContext: observeContext, cancel: cancel, cancelAt: tt.cancelAt}
			actual, err := collect(parent, databaseURL, func(ctx context.Context, config *pgx.ConnConfig) (censusConnection, error) {
				observeContext("connect", ctx)
				require.Equal(t, "synthetic.example.test", config.Host, "the explicit URI must select the supplied synthetic host")
				require.Equal(t, "synthetic_database", config.Database, "the explicit URI must select the supplied synthetic database")
				require.Equal(t, uint16(5432), config.Port, "an omitted URI port must use the census's pinned default rather than PGPORT")
				require.Equal(t, "143:lifecycle_census", config.RuntimeParams["application_name"], "collector connections must replace arbitrary application naming with the fixed passive name")
				require.Equal(t, "5000", config.RuntimeParams["statement_timeout"], "the server-side statement budget must override an unbounded URI setting before connecting")
				require.Equal(t, "5000", config.RuntimeParams["lock_timeout"], "the server-side lock budget must override an unbounded URI setting before connecting")
				for _, key := range []string{"APPLICATION_NAME", "STATEMENT_TIMEOUT", "LOCK_TIMEOUT", "options", "OPTIONS"} {
					require.NotContains(t, config.RuntimeParams, key, "case variants and startup options must not override the collector's fixed attribution or server timeouts")
				}
				if tt.cancelAt == "connect" {
					cancel()
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if tt.failure == "connect" {
					return nil, privateFailure
				}
				return connection, nil
			})
			if len(tt.wantErrors) == 0 {
				require.NoError(t, err, "successful read-only collection and cleanup should return one observation report")
				require.Equal(t, emptyCensusCollectionReport(writerClock, activityClock), actual, "successful collection must retain both independent database clocks and empty-source uncertainty exactly")
			} else {
				require.Error(t, err, "failed collection or cleanup must not report success")
				for _, message := range tt.wantErrors {
					require.ErrorContains(t, err, message, "every failing stage must retain its sanitized diagnostic")
				}
				require.Equal(t, Report{}, actual, "any collection or cleanup failure must discard every partial or complete observation")
				require.NotErrorIs(t, err, privateFailure, "raw driver errors must not remain discoverable through the sanitized error chain")
				for _, privateValue := range []string{databaseURL, "synthetic_password", "synthetic.example.test", "synthetic_private_query"} {
					require.NotContains(t, err.Error(), privateValue, "diagnostics must not expose DSNs credentials addresses or SQL from private driver messages")
				}
			}
			if tt.wantContextErr != nil {
				require.ErrorIs(t, err, tt.wantContextErr, "actual parent cancellation or deadline expiry must remain classifiable after error sanitization")
			}
			require.Equal(t, tt.wantOperations, operations, "collection must open once read sequentially and roll back before closing without retries or commits")
			require.NoError(t, mock.ExpectationsWereMet(), "collection must use exact read-only transaction options and complete only its expected reads and cleanup")
		})
	}
}

func TestCollectRejectsImplicitOrInvalidDatabaseConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, databaseURL, wantErr string
	}{
		{name: "missing configuration", wantErr: "database configuration required"},
		{name: "whitespace configuration", databaseURL: " \t\n ", wantErr: "database configuration required"},
		{name: "keyword configuration cannot inherit environment targets", databaseURL: "host=synthetic.example.test dbname=synthetic", wantErr: "invalid database configuration"},
		{name: "unsupported scheme", databaseURL: "mysql://synthetic.example.test/synthetic", wantErr: "invalid database configuration"},
		{name: "malformed URI escape", databaseURL: "postgres://synthetic.example.test/%", wantErr: "invalid database configuration"},
		{name: "malformed query escape", databaseURL: "postgres://synthetic.example.test/synthetic?host=%", wantErr: "invalid database configuration"},
		{name: "missing explicit host", databaseURL: "postgres:///synthetic", wantErr: "explicit database host and name required"},
		{name: "missing explicit database", databaseURL: "postgres://synthetic.example.test", wantErr: "explicit database host and name required"},
		{name: "empty query host must not fall back to URI or environment", databaseURL: "postgres://synthetic.example.test/synthetic?host=", wantErr: "explicit database host and name required"},
		{name: "empty query dbname must not fall back to URI or environment", databaseURL: "postgres://synthetic.example.test/synthetic?dbname=", wantErr: "explicit database host and name required"},
		{name: "empty database alias must not fall back to URI or environment", databaseURL: "postgres://synthetic.example.test/synthetic?database=", wantErr: "explicit database host and name required"},
		{name: "comma only URI host cannot inherit a default host", databaseURL: "postgres://,/synthetic", wantErr: "explicit database host and name required"},
		{name: "URI host list with empty first target is rejected", databaseURL: "postgres://,synthetic.example.test/synthetic", wantErr: "explicit database host and name required"},
		{name: "URI host list with empty final target is rejected", databaseURL: "postgres://synthetic.example.test,/synthetic", wantErr: "explicit database host and name required"},
		{name: "URI host list with only explicit targets remains out of scope", databaseURL: "postgres://synthetic.example.test,other.example.test/synthetic", wantErr: "explicit database host and name required"},
		{name: "empty query host list component cannot inherit a default host", databaseURL: "postgres:///synthetic?host=synthetic.example.test%2C", wantErr: "explicit database host and name required"},
		{name: "query host list with explicit targets remains out of scope", databaseURL: "postgres:///synthetic?host=synthetic.example.test%2Cother.example.test", wantErr: "explicit database host and name required"},
		{name: "slash only database path cannot inherit a database", databaseURL: "postgres://synthetic.example.test//", wantErr: "explicit database host and name required"},
		{name: "decoded slash only database path cannot inherit a database", databaseURL: "postgres://synthetic.example.test/%2F%2F", wantErr: "explicit database host and name required"},
		{name: "empty dbname with database alias is ambiguous", databaseURL: "postgres://synthetic.example.test/synthetic?dbname=&database=other", wantErr: "explicit database host and name required"},
		{name: "empty database alias with dbname is ambiguous", databaseURL: "postgres://synthetic.example.test/synthetic?dbname=other&database=", wantErr: "explicit database host and name required"},
		{name: "conflicting database aliases are rejected", databaseURL: "postgres://synthetic.example.test/synthetic?dbname=first&database=second", wantErr: "explicit database host and name required"},
		{name: "equal database aliases are still rejected", databaseURL: "postgres://synthetic.example.test/synthetic?dbname=same&database=same", wantErr: "explicit database host and name required"},
		{name: "duplicate dbname keys cannot select nondeterministic targets", databaseURL: "postgres://synthetic.example.test/synthetic?dbname=first&dbname=second", wantErr: "explicit database host and name required"},
		{name: "duplicate database keys cannot select nondeterministic targets", databaseURL: "postgres://synthetic.example.test/synthetic?database=first&database=second", wantErr: "explicit database host and name required"},
		{name: "duplicate host keys are rejected even when equal", databaseURL: "postgres:///synthetic?host=synthetic.example.test&host=synthetic.example.test", wantErr: "explicit database host and name required"},
		{name: "empty query port cannot inherit PGPORT", databaseURL: "postgres://synthetic.example.test/synthetic?port=", wantErr: "explicit database host and name required"},
		{name: "empty URI port cannot inherit PGPORT", databaseURL: "postgres://synthetic.example.test:/synthetic", wantErr: "explicit database host and name required"},
		{name: "empty bracketed IPv6 URI port cannot inherit PGPORT", databaseURL: "postgres://[2001:db8::1]:/synthetic", wantErr: "explicit database host and name required"},
		{name: "zero query port is not an implicit default", databaseURL: "postgres://synthetic.example.test/synthetic?port=0", wantErr: "explicit database host and name required"},
		{name: "query port list is not a supported single target", databaseURL: "postgres://synthetic.example.test/synthetic?port=5432%2C6432", wantErr: "explicit database host and name required"},
		{name: "duplicate port keys are rejected even when equal", databaseURL: "postgres://synthetic.example.test/synthetic?port=5432&port=5432", wantErr: "explicit database host and name required"},
		{name: "URI and query ports are rejected even when equal", databaseURL: "postgres://synthetic.example.test:5432/synthetic?port=5432", wantErr: "explicit database host and name required"},
		{name: "URI and query conflicting ports are rejected", databaseURL: "postgres://synthetic.example.test:5432/synthetic?port=6432", wantErr: "explicit database host and name required"},
		{name: "pgx configuration failure is sanitized", databaseURL: "postgres://synthetic.example.test/synthetic?connect_timeout=not-a-number", wantErr: "invalid database configuration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			connections := 0
			actual, err := collect(context.Background(), tt.databaseURL, func(context.Context, *pgx.ConnConfig) (censusConnection, error) {
				connections++
				return nil, errors.New("unexpected synthetic connection")
			})
			require.EqualError(t, err, tt.wantErr, "invalid or implicit targets must fail with a static configuration error")
			require.Equal(t, Report{}, actual, "rejected configuration must not create a successful observation report")
			require.Equal(t, 0, connections, "configuration validation must finish before attempting any database connection")
		})
	}
}

//nolint:paralleltest // t.Setenv owns process-wide libpq defaults, so parent and subtests must remain serial.
func TestCollectPinsExplicitDatabaseTargetsAndServerTimeouts(t *testing.T) {
	// This test cannot run in parallel: adversarial libpq environment defaults
	// are process-wide, so keep all of its subtests serial while t.Setenv owns them.
	t.Setenv("PGHOST", "environment.example.test")
	t.Setenv("PGDATABASE", "environment_database")
	t.Setenv("PGPORT", "6543")
	t.Setenv("PGOPTIONS", "-c statement_timeout=0 -c lock_timeout=0 -c application_name=synthetic_environment_override")
	tests := []struct {
		name, databaseURL, wantHost, wantDatabase string
		wantPort                                  uint16
	}{
		{name: "ordinary URI pins the default port", databaseURL: "postgres://synthetic_user@synthetic.example.test/synthetic_database", wantHost: "synthetic.example.test", wantDatabase: "synthetic_database", wantPort: 5432},
		{name: "postgresql scheme is explicit", databaseURL: "postgresql://synthetic_user@synthetic.example.test/synthetic_database", wantHost: "synthetic.example.test", wantDatabase: "synthetic_database", wantPort: 5432},
		{name: "explicit URI port is retained", databaseURL: "postgres://synthetic_user@synthetic.example.test:6432/synthetic_database", wantHost: "synthetic.example.test", wantDatabase: "synthetic_database", wantPort: 6432},
		{name: "bracketed IPv6 URI pins the default port", databaseURL: "postgres://synthetic_user@[2001:db8::1]/synthetic_database", wantHost: "2001:db8::1", wantDatabase: "synthetic_database", wantPort: 5432},
		{name: "bracketed IPv6 URI explicit port is retained", databaseURL: "postgres://synthetic_user@[2001:db8::1]:6432/synthetic_database", wantHost: "2001:db8::1", wantDatabase: "synthetic_database", wantPort: 6432},
		{name: "explicit query port is retained", databaseURL: "postgres://synthetic_user@synthetic.example.test/synthetic_database?port=7432", wantHost: "synthetic.example.test", wantDatabase: "synthetic_database", wantPort: 7432},
		{name: "single dbname alias selects the explicit database", databaseURL: "postgres://synthetic_user@synthetic.example.test/?dbname=synthetic_database", wantHost: "synthetic.example.test", wantDatabase: "synthetic_database", wantPort: 5432},
		{name: "single database alias selects the explicit database", databaseURL: "postgres://synthetic_user@synthetic.example.test/?database=synthetic_database", wantHost: "synthetic.example.test", wantDatabase: "synthetic_database", wantPort: 5432},
		{name: "explicit socket path is not replaced by environment host", databaseURL: "postgres://synthetic_user@/synthetic_database?host=%2Fsynthetic%2Fsocket", wantHost: "/synthetic/socket", wantDatabase: "synthetic_database", wantPort: 5432},
		{name: "explicit query host is not replaced by environment host", databaseURL: "postgres:///synthetic_database?host=synthetic.example.test&user=synthetic_user", wantHost: "synthetic.example.test", wantDatabase: "synthetic_database", wantPort: 5432},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No t.Parallel while this parent owns process-wide libpq defaults.
			calls := 0
			actual, err := collect(context.Background(), tt.databaseURL, func(_ context.Context, config *pgx.ConnConfig) (censusConnection, error) {
				calls++
				require.Equal(t, tt.wantHost, config.Host, "the parsed collector must use only its validated explicit host")
				require.Equal(t, tt.wantDatabase, config.Database, "the parsed collector must use only its validated explicit database")
				require.Equal(t, tt.wantPort, config.Port, "the parsed collector must use its explicit port or pinned 5432, never PGPORT")
				for _, fallback := range config.Fallbacks {
					require.Equal(t, tt.wantHost, fallback.Host, "TLS fallback must never introduce an implicit database host")
					require.Equal(t, tt.wantPort, fallback.Port, "TLS fallback must never introduce an implicit database port")
				}
				require.Equal(t, "5000", config.RuntimeParams["statement_timeout"], "every explicit-target form must install its server-side statement timeout before connecting")
				require.Equal(t, "5000", config.RuntimeParams["lock_timeout"], "every explicit-target form must install its server-side lock timeout before connecting")
				require.Equal(t, "143:lifecycle_census", config.RuntimeParams["application_name"], "PGOPTIONS must not override the fixed collector label")
				require.NotContains(t, config.RuntimeParams, "options", "PGOPTIONS must not override the fixed statement and lock timeouts at server startup")
				return nil, errors.New("synthetic connection intentionally not established")
			})
			require.EqualError(t, err, "database connection failed", "valid target configuration should reach exactly one sanitized connection attempt")
			require.Equal(t, Report{}, actual, "the target-validation fixture must not fabricate a successful collection")
			require.Equal(t, 1, calls, "valid explicit configuration must be parsed once and attempted without retries")
		})
	}
}

func TestCollectWaitsForAsynchronousConnectionCleanup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		neverDone     bool
		closeDelay    time.Duration
		cancelQuery   bool
		wantErrorPart string
	}{
		{name: "successful report waits for cleanup completion"},
		{name: "canceled observation still waits with usable independent cleanup", cancelQuery: true, wantErrorPart: "connection observation failed"},
		{name: "uncompleted cleanup invalidates successful observations", neverDone: true, wantErrorPart: "database connection cleanup failed"},
		{name: "close and asynchronous wait share one cleanup deadline", neverDone: true, closeDelay: 200 * time.Millisecond, wantErrorPart: "database connection cleanup failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewConn()
			require.NoError(t, err, "create an isolated asynchronous cleanup fixture")
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			writerClock := time.Date(2026, time.October, 19, 13, 0, 0, 0, time.UTC)
			activityClock := writerClock.Add(time.Millisecond)
			mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
			mock.ExpectQuery(`^SELECT statement_timestamp\(\) AS observed_at,`).
				WillReturnRows(pgxmock.NewRows([]string{"observed_at", "present", "role", "build_sha", "version"}).
					AddRow(writerClock, false, nil, nil, nil)).RowsWillBeClosed()
			activity := mock.ExpectQuery(`^WITH observation AS MATERIALIZED \(`)
			if tt.cancelQuery {
				activity.WillReturnError(context.Canceled)
				activity.WillDelayFor(time.Second)
			} else {
				fullVisibility := false
				activity.WillReturnRows(pgxmock.NewRows([]string{"observed_at", "full_visibility", "present", "application_name", "backend_type", "state", "xact_start", "details_visible"}).
					AddRow(activityClock, &fullVisibility, false, nil, nil, nil, nil, false)).RowsWillBeClosed()
			}
			mock.ExpectRollback()
			mock.ExpectClose().WillDelayFor(tt.closeDelay)
			cleanupDone := make(chan struct{})
			release := sync.OnceFunc(func() { close(cleanupDone) })
			defer release()
			cleanupAsked := make(chan struct{}, 1)
			closeContext := make(chan context.Context, 1)
			connection := &observedCensusTestConnection{
				mock: mock, observeContext: func(string, context.Context) {}, cancel: cancel,
				cleanupDone: cleanupDone, cleanupAsked: cleanupAsked, closedContext: closeContext,
			}
			if tt.cancelQuery {
				connection.cancelAt = "connections"
			}
			type result struct {
				report Report
				err    error
			}
			results := make(chan result, 1)
			go func() {
				report, err := collect(parent, "postgres://synthetic_user@synthetic.example.test/synthetic_database", func(context.Context, *pgx.ConnConfig) (censusConnection, error) {
					return connection, nil
				})
				results <- result{report: report, err: err}
			}()
			var closing context.Context
			select {
			case closing = <-closeContext:
			case <-time.After(2 * time.Second):
				t.Fatal("collector must reach bounded close after its expected observations and rollback")
			}
			require.NoError(t, closing.Err(), "close must receive an independent usable context even if observation was canceled")
			closeDeadline, ok := closing.Deadline()
			require.True(t, ok, "close must have a deadline for connection and asynchronous cleanup")
			require.LessOrEqual(t, time.Until(closeDeadline), 250*time.Millisecond, "close and cleanup confirmation must have at most one 250ms budget")
			select {
			case <-cleanupAsked:
			case <-time.After(2 * time.Second):
				t.Fatal("collector must request asynchronous cleanup confirmation after closing")
			}
			if !tt.neverDone {
				select {
				case <-results:
					t.Fatal("collector returned before the asynchronous cleanup completion channel closed")
				case <-time.After(15 * time.Millisecond):
				}
				release()
			}
			var actual result
			select {
			case actual = <-results:
			case <-time.After(2 * time.Second):
				t.Fatal("asynchronous cleanup must complete or return a bounded sanitized failure")
			}
			if tt.wantErrorPart == "" {
				require.NoError(t, actual.err, "completed asynchronous cleanup should preserve successful observations")
				require.Equal(t, emptyCensusCollectionReport(writerClock, activityClock), actual.report, "successful observations must not be returned until asynchronous cleanup is confirmed")
			} else {
				require.ErrorContains(t, actual.err, tt.wantErrorPart, "observation and asynchronous cleanup failures must remain sanitized and visible")
				require.Equal(t, Report{}, actual.report, "cancellation or unconfirmed cleanup must discard every observation")
			}
			if tt.cancelQuery {
				require.ErrorIs(t, actual.err, context.Canceled, "waiting for asynchronous cleanup must not erase the observation cancellation cause")
			}
			if tt.neverDone {
				require.ErrorIs(t, closing.Err(), context.DeadlineExceeded, "the same close context must bound the asynchronous cleanup wait")
				require.Less(t, time.Since(closeDeadline), 150*time.Millisecond, "a slow close must not receive a fresh second 250ms asynchronous-cleanup budget")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "asynchronous cleanup must follow exactly one read-only observation transaction, rollback and close")
		})
	}
}

type observedCensusTestConnection struct {
	mock           pgxmock.PgxConnIface
	observeContext func(string, context.Context)
	cancel         context.CancelFunc
	cancelAt       string
	cleanupDone    <-chan struct{}
	cleanupAsked   chan<- struct{}
	closedContext  chan<- context.Context
}

func (c *observedCensusTestConnection) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	c.observeContext("begin", ctx)
	tx, err := c.mock.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &observedCensusTestTransaction{Tx: tx, observeContext: c.observeContext, cancel: c.cancel, cancelAt: c.cancelAt}, nil
}

func (c *observedCensusTestConnection) Close(ctx context.Context) error {
	c.observeContext("close", ctx)
	if c.closedContext != nil {
		c.closedContext <- ctx
	}
	return c.mock.Close(ctx)
}

func (c *observedCensusTestConnection) CleanupDone() <-chan struct{} {
	if c.cleanupAsked != nil {
		c.cleanupAsked <- struct{}{}
	}
	if c.cleanupDone != nil {
		return c.cleanupDone
	}
	closed := make(chan struct{})
	close(closed)
	return closed
}

type observedCensusTestTransaction struct {
	pgx.Tx
	observeContext func(string, context.Context)
	cancel         context.CancelFunc
	cancelAt       string
}

func (tx *observedCensusTestTransaction) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	operation := "writers"
	if strings.Contains(sql, "pg_stat_activity") {
		operation = "connections"
	}
	tx.observeContext(operation, ctx)
	if tx.cancelAt == operation {
		tx.cancel()
	}
	rows, err := tx.Tx.Query(ctx, sql, args...)
	if tx.cancelAt == "after connections" && operation == "connections" {
		tx.cancel()
	}
	return rows, err
}

func (tx *observedCensusTestTransaction) Rollback(ctx context.Context) error {
	tx.observeContext("rollback", ctx)
	return tx.Tx.Rollback(ctx)
}

func emptyCensusCollectionReport(writerClock, activityClock time.Time) Report {
	return Report{
		SchemaVersion: 1,
		Limitations: []string{
			"passive partial observations; not a compatibility, liveness, drain or rollout verdict",
			"writer leases and activity have separate statement clocks; not an atomic snapshot or instance-to-backend join",
			"role/build labels are self-reported and spoofable; transaction-pooling proxies may not preserve them",
			"current database only; collector excluded; masked backend types are retained as unknown",
			"null, disabled or hidden activity/transaction details remain unknown, not evidence of no transaction",
			"at most 1000 rows per source; truncated counts describe only the retained sample, not source totals",
		},
		Writers: WriterSummary{ObservedAt: writerClock, CountsScope: CountsObserved, Groups: []WriterGroup{}},
		Connections: ConnectionSummary{ObservedAt: activityClock, Visibility: VisibilityRestricted,
			CountsScope: CountsObserved, Groups: []ConnectionGroup{}},
	}
}
