package lifecyclecensus

import (
	"context"
	"errors"
	"strings"
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
			const databaseURL = "postgres://synthetic_user:synthetic_password@synthetic.example.test/synthetic_database?application_name=untrusted_name"
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
				require.Equal(t, "143:lifecycle_census", config.RuntimeParams["application_name"], "collector connections must replace arbitrary application naming with the fixed passive name")
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

type observedCensusTestConnection struct {
	mock           pgxmock.PgxConnIface
	observeContext func(string, context.Context)
	cancel         context.CancelFunc
	cancelAt       string
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
	return c.mock.Close(ctx)
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
