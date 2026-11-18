package db

import (
	"context"
	"errors"
	"io"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

const expectedLifecycleSettings = `SELECT
	set_config('lifecycle143.writer_version', '1', true),
	set_config('lock_timeout', '250ms', true),
	set_config('statement_timeout', '1500ms', true)`

func TestRunLifecycleTransactionRequiresOwnedPool(t *testing.T) {
	t.Parallel()
	// The exact function type is a compile-time regression guard: changing the
	// public API back to a structural BeginTx interface would admit an already
	// owned raw connection and could commit the caller's outer transaction.
	var _ func(context.Context, *pgxpool.Pool, func(context.Context, pgx.Tx) error) error = RunLifecycleTransaction
	err := RunLifecycleTransaction(context.Background(), nil, func(context.Context, pgx.Tx) error {
		t.Fatal("nil pool must not invoke the callback")
		return nil
	})
	require.ErrorContains(t, err, "nil pool", "public runner should reject a missing owned pool")
}

func expectLifecycleSetup(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery("^SHOW transaction_isolation$").
		WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow("read committed"))
	mock.ExpectExec(regexp.QuoteMeta(expectedLifecycleSettings)).WillReturnResult(pgxmock.NewResult("SELECT", 1))
}

func TestConfigureLifecycleTransactionInTx(t *testing.T) {
	t.Parallel()
	queryErr := errors.New("isolation lookup failed")
	settingsErr := errors.New("local settings failed")
	tests := []struct {
		name      string
		isolation string
		queryErr  error
		execErr   error
		wantErr   error
		wantText  string
	}{
		{name: "read committed installs only local settings", isolation: "read committed"},
		{name: "repeatable read is rejected", isolation: "repeatable read", wantText: "READ COMMITTED required"},
		{name: "serializable is rejected", isolation: "serializable", wantText: "READ COMMITTED required"},
		{name: "read uncommitted is rejected", isolation: "read uncommitted", wantText: "READ COMMITTED required"},
		{name: "isolation failure is preserved", queryErr: queryErr, wantErr: queryErr},
		{name: "settings failure is preserved", isolation: "read committed", execErr: settingsErr, wantErr: settingsErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "transaction mock should initialize")
			defer mock.Close()
			query := mock.ExpectQuery("^SHOW transaction_isolation$")
			if tt.queryErr != nil {
				query.WillReturnError(tt.queryErr)
			} else {
				query.WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(tt.isolation))
			}
			if tt.queryErr == nil && tt.isolation == "read committed" {
				exec := mock.ExpectExec(regexp.QuoteMeta(expectedLifecycleSettings))
				if tt.execErr != nil {
					exec.WillReturnError(tt.execErr)
				} else {
					exec.WillReturnResult(pgxmock.NewResult("SELECT", 1))
				}
			}
			err = ConfigureLifecycleTransactionInTx(context.Background(), mock)
			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr, "configurator should preserve its database error")
			case tt.wantText != "":
				require.ErrorContains(t, err, tt.wantText, "incompatible isolation should be rejected before settings")
			default:
				require.NoError(t, err, "read committed transaction should receive local settings")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "configurator should not begin, commit, roll back or retry")
		})
	}
}

func TestRunLifecycleTransactionOrdersTopLevelSetupBeforeWork(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "transaction mock should initialize")
	defer mock.Close()
	mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	expectLifecycleSetup(mock)
	mock.ExpectExec("^SELECT 42$").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectCommit()
	err = runLifecycleTransaction(context.Background(), mock, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT 42")
		return err
	})
	require.NoError(t, err, "configured top-level transaction should commit once after work")
	require.NoError(t, mock.ExpectationsWereMet(), "explicit isolation and settings must precede the callback")
}

func TestRunLifecycleTransactionRetriesOnlyAbortedSetupOrWorkContention(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		code  string
		phase string
	}{
		{name: "setup lock unavailable", code: "55P03", phase: "setup"},
		{name: "setup deadlock", code: "40P01", phase: "setup"},
		{name: "settings lock unavailable", code: "55P03", phase: "settings"},
		{name: "work lock unavailable", code: "55P03", phase: "work"},
		{name: "work deadlock", code: "40P01", phase: "work"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "transaction mock should initialize")
			defer mock.Close()
			contention := &pgconn.PgError{Code: tt.code}
			mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if tt.phase == "setup" {
				mock.ExpectQuery("^SHOW transaction_isolation$").WillReturnError(contention)
			} else if tt.phase == "settings" {
				mock.ExpectQuery("^SHOW transaction_isolation$").
					WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow("read committed"))
				mock.ExpectExec(regexp.QuoteMeta(expectedLifecycleSettings)).WillReturnError(contention)
			} else {
				expectLifecycleSetup(mock)
				mock.ExpectExec("^SELECT 42$").WillReturnError(contention)
			}
			mock.ExpectRollback()
			mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			expectLifecycleSetup(mock)
			mock.ExpectExec("^SELECT 42$").WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectCommit()
			err = runLifecycleTransaction(context.Background(), mock, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "SELECT 42")
				return err
			})
			require.NoError(t, err, "confirmed rollback should permit a fresh configured transaction")
			require.NoError(t, mock.ExpectationsWereMet(), "whole rollback must precede any new BeginTx")
		})
	}
}

func TestRunLifecycleTransactionDoesNotRetryOtherFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		phase string
		cause error
	}{
		{name: "begin lock error", phase: "begin", cause: &pgconn.PgError{Code: "55P03"}},
		{name: "begin deadlock error", phase: "begin", cause: &pgconn.PgError{Code: "40P01"}},
		{name: "begin network error", phase: "begin", cause: io.EOF},
		{name: "statement canceled", phase: "work", cause: &pgconn.PgError{Code: "57014"}},
		{name: "serialization failure", phase: "work", cause: &pgconn.PgError{Code: "40001"}},
		{name: "application failure", phase: "work", cause: errors.New("authority rejected")},
		{name: "work network error", phase: "work", cause: io.EOF},
		{name: "work cancellation", phase: "work", cause: context.Canceled},
		{name: "work deadline", phase: "work", cause: context.DeadlineExceeded},
		{name: "contention combined with network uncertainty", phase: "work", cause: errors.Join(&pgconn.PgError{Code: "55P03"}, io.EOF)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "transaction mock should initialize")
			defer mock.Close()
			begin := mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if tt.phase == "begin" {
				begin.WillReturnError(tt.cause)
			} else {
				expectLifecycleSetup(mock)
				mock.ExpectRollback()
			}
			calls := 0
			err = runLifecycleTransaction(context.Background(), mock, func(context.Context, pgx.Tx) error {
				calls++
				return tt.cause
			})
			require.ErrorIs(t, err, tt.cause, "non-replayable failure should retain its original cause")
			wantCalls := 1
			if tt.phase == "begin" {
				wantCalls = 0
			}
			require.Equal(t, wantCalls, calls, "begin failure must not invoke work and work failure must not replay")
			require.NoError(t, mock.ExpectationsWereMet(), "failure must not start another transaction")
		})
	}
}

func TestRunLifecycleTransactionCommitErrorsNeverReplay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "network commit uncertainty", err: io.EOF},
		{name: "commit lock error", err: &pgconn.PgError{Code: "55P03"}},
		{name: "commit deadlock error", err: &pgconn.PgError{Code: "40P01"}},
		{name: "commit reports transaction rollback", err: pgx.ErrTxCommitRollback},
		{name: "commit closed transaction", err: pgx.ErrTxClosed},
		{name: "commit cancellation", err: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cleanupTests := []struct {
				name string
				err  error
			}{
				{name: "rollback reports closed", err: pgx.ErrTxClosed},
				{name: "even a successful rollback never authorizes replay"},
			}
			for _, cleanup := range cleanupTests {
				t.Run(cleanup.name, func(t *testing.T) {
					t.Parallel()
					mock, err := pgxmock.NewPool()
					require.NoError(t, err, "transaction mock should initialize")
					defer mock.Close()
					mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
					expectLifecycleSetup(mock)
					mock.ExpectCommit().WillReturnError(tt.err)
					mock.ExpectRollback().WillReturnError(cleanup.err)
					calls := 0
					err = runLifecycleTransaction(context.Background(), mock, func(context.Context, pgx.Tx) error {
						calls++
						return nil
					})
					require.ErrorIs(t, err, tt.err, "commit failure should remain discoverable without inference from cleanup")
					if cleanup.err != nil {
						require.ErrorIs(t, err, cleanup.err, "closed cleanup must not be reported as confirmed abort")
					}
					require.Equal(t, 1, calls, "no commit error can authorize another callback invocation")
					require.NoError(t, mock.ExpectationsWereMet(), "commit failure must never begin another transaction")
				})
			}
		})
	}
}

func TestRunLifecycleTransactionRollbackFailuresNeverReplay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "network rollback failure", err: io.EOF},
		{name: "already closed is not abort proof", err: pgx.ErrTxClosed},
		{name: "rollback context expires", err: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "transaction mock should initialize")
			defer mock.Close()
			cause := &pgconn.PgError{Code: "55P03"}
			mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			expectLifecycleSetup(mock)
			mock.ExpectRollback().WillReturnError(tt.err)
			err = runLifecycleTransaction(context.Background(), mock, func(context.Context, pgx.Tx) error { return cause })
			require.ErrorIs(t, err, cause, "original contention should survive a failed abort")
			require.ErrorIs(t, err, tt.err, "failed rollback must be propagated, including ErrTxClosed")
			require.NotErrorIs(t, err, ErrLifecycleContention, "uncertain abort is not safe contention exhaustion")
			require.NoError(t, mock.ExpectationsWereMet(), "failed rollback must prevent a fresh transaction")
		})
	}
}

func TestRunLifecycleTransactionExhaustionPreservesCause(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "transaction mock should initialize")
	defer mock.Close()
	cause := &pgconn.PgError{Code: "40P01", Message: "synthetic deadlock"}
	for range 3 {
		mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		expectLifecycleSetup(mock)
		mock.ExpectRollback()
	}
	calls := 0
	err = runLifecycleTransaction(context.Background(), mock, func(context.Context, pgx.Tx) error {
		calls++
		return cause
	})
	var contention *LifecycleContentionError
	require.ErrorAs(t, err, &contention, "bounded exhaustion should expose the typed contention result")
	require.ErrorIs(t, err, ErrLifecycleContention, "callers should be able to recognize contention without parsing text")
	require.ErrorIs(t, err, cause, "typed contention should preserve the final database cause")
	require.Equal(t, 3, contention.Attempts, "attempt cap should include the original transaction")
	require.Equal(t, 3, calls, "contention should revalidate at most three times")
	require.Contains(t, err.Error(), "after 3 attempts", "typed contention should describe its bounded outcome")
	require.NoError(t, mock.ExpectationsWereMet(), "every exhausted attempt should explicitly roll back")
}

type lifecycleTransactionStarterFunc func(context.Context, pgx.TxOptions) (pgx.Tx, error)

func (f lifecycleTransactionStarterFunc) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	return f(ctx, options)
}

type lifecycleTransactionTestTx struct {
	pgx.Tx
	queryRow func(context.Context, string) pgx.Row
	exec     func(context.Context, string) (pgconn.CommandTag, error)
	commit   func(context.Context) error
	rollback func(context.Context) error
}

func (tx *lifecycleTransactionTestTx) QueryRow(ctx context.Context, sql string, _ ...any) pgx.Row {
	return tx.queryRow(ctx, sql)
}

func (tx *lifecycleTransactionTestTx) Exec(ctx context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	return tx.exec(ctx, sql)
}

func (tx *lifecycleTransactionTestTx) Commit(ctx context.Context) error { return tx.commit(ctx) }

func (tx *lifecycleTransactionTestTx) Rollback(ctx context.Context) error { return tx.rollback(ctx) }

type lifecycleTransactionIsolationRow struct{}

func (lifecycleTransactionIsolationRow) Scan(dest ...any) error {
	*dest[0].(*string) = "read committed"
	return nil
}

func newLifecycleTransactionTestTx() *lifecycleTransactionTestTx {
	return &lifecycleTransactionTestTx{
		queryRow: func(context.Context, string) pgx.Row { return lifecycleTransactionIsolationRow{} },
		exec: func(context.Context, string) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("SELECT 1"), nil
		},
		commit:   func(context.Context) error { return nil },
		rollback: func(context.Context) error { return nil },
	}
}

func TestRunLifecycleTransactionOneBudgetAcrossFreshTransactions(t *testing.T) {
	t.Parallel()
	started := time.Now()
	var expectedDeadline time.Time
	var contexts []context.Context
	var transactions []pgx.Tx
	var events []string
	starter := lifecycleTransactionStarterFunc(func(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
		require.Equal(t, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, options, "every fresh transaction must use explicit READ COMMITTED")
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "pool acquisition must be bounded before the transaction begins")
		if expectedDeadline.IsZero() {
			expectedDeadline = deadline
			require.WithinDuration(t, started.Add(2*time.Second), deadline, 50*time.Millisecond, "top-level budget should be two seconds")
		}
		require.Equal(t, expectedDeadline, deadline, "retry must not reset the original acquisition budget")
		contexts = append(contexts, ctx)
		tx := newLifecycleTransactionTestTx()
		tx.queryRow = func(got context.Context, _ string) pgx.Row {
			require.Equal(t, ctx, got, "isolation setup should use the original bounded context")
			events = append(events, "isolation")
			return lifecycleTransactionIsolationRow{}
		}
		tx.exec = func(got context.Context, sql string) (pgconn.CommandTag, error) {
			require.Equal(t, ctx, got, "local settings should share the unit context")
			require.Equal(t, strings.Fields(expectedLifecycleSettings), strings.Fields(sql), "each attempt must install the complete transaction-local protocol")
			events = append(events, "settings")
			return pgconn.NewCommandTag("SELECT 1"), nil
		}
		tx.rollback = func(context.Context) error { events = append(events, "rollback"); return nil }
		tx.commit = func(got context.Context) error {
			require.Equal(t, ctx, got, "commit should share acquisition and work budget")
			events = append(events, "commit")
			return nil
		}
		transactions = append(transactions, tx)
		events = append(events, "begin")
		return tx, nil
	})
	calls := 0
	err := runLifecycleTransaction(context.Background(), starter, func(ctx context.Context, tx pgx.Tx) error {
		require.Equal(t, contexts[len(contexts)-1], ctx, "work must use the same bounded context as its fresh BeginTx")
		require.Same(t, transactions[len(transactions)-1], tx, "work should receive exactly the newly acquired transaction")
		events = append(events, "work")
		calls++
		if calls < 2 {
			return &pgconn.PgError{Code: "55P03"}
		}
		return nil
	})
	require.NoError(t, err, "a second fresh transaction should commit after confirmed abort")
	require.NotSame(t, transactions[0], transactions[1], "retry must not reuse the aborted transaction")
	require.Equal(t, []string{"begin", "isolation", "settings", "work", "rollback", "begin", "isolation", "settings", "work", "commit"}, events, "fresh acquisition and revalidation must follow successful rollback")
}

func TestLifecycleRetryDelayBounds(t *testing.T) {
	t.Parallel()
	for range 1024 {
		delay := lifecycleRetryDelay()
		require.GreaterOrEqual(t, delay, 25*time.Millisecond, "retry jitter must not fall below the minimum contention backoff")
		require.LessOrEqual(t, delay, 100*time.Millisecond, "retry jitter must not exceed the maximum contention backoff")
	}
}

func TestRunLifecycleTransactionWaitsAfterRollbackBeforeRetry(t *testing.T) {
	t.Parallel()
	begins, calls, commits, rollbacks := 0, 0, 0, 0
	var rollbackFinished time.Time
	var retryWait time.Duration
	tx := newLifecycleTransactionTestTx()
	tx.rollback = func(context.Context) error {
		rollbacks++
		rollbackFinished = time.Now()
		return nil
	}
	tx.commit = func(context.Context) error { commits++; return nil }
	starter := lifecycleTransactionStarterFunc(func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
		begins++
		if begins == 2 {
			retryWait = time.Since(rollbackFinished)
		}
		return tx, nil
	})
	err := runLifecycleTransaction(context.Background(), starter, func(context.Context, pgx.Tx) error {
		calls++
		if calls == 1 {
			return &pgconn.PgError{Code: "55P03"}
		}
		return nil
	})
	require.NoError(t, err, "a retried unit should commit after confirmed rollback and backoff")
	require.Equal(t, 2, begins, "one contended attempt should acquire exactly one replacement transaction")
	require.Equal(t, 2, calls, "the callback should revalidate exactly once after contention")
	require.Equal(t, 1, commits, "only the successful retry should commit")
	require.Equal(t, 1, rollbacks, "only the contended attempt should roll back")
	require.False(t, rollbackFinished.IsZero(), "retry must follow an observed completed rollback")
	require.GreaterOrEqual(t, retryWait, 25*time.Millisecond, "fresh acquisition must wait at least the minimum backoff after rollback completes")
}

func TestRunLifecycleTransactionCancellationAndCleanup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		phase  string
		result error
	}{
		{name: "cancel before acquisition", phase: "before", result: context.Canceled},
		{name: "cancel during acquisition", phase: "begin", result: context.Canceled},
		{name: "cancel in callback prevents commit", phase: "work", result: context.Canceled},
		{name: "expired caller budget prevents acquisition", phase: "expired", result: context.DeadlineExceeded},
		{name: "cancel contention prevents retry", phase: "contention", result: context.Canceled},
		{name: "cancel during rollback prevents retry", phase: "rollback", result: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), lifecycleTestContextKey{}, "preserved"))
			defer cancel()
			if tt.phase == "before" {
				cancel()
			}
			if tt.phase == "expired" {
				deadlineCtx, deadlineCancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer deadlineCancel()
				ctx = deadlineCtx
			}
			begins, commits, rollbacks := 0, 0, 0
			tx := newLifecycleTransactionTestTx()
			tx.commit = func(context.Context) error { commits++; return nil }
			tx.rollback = func(cleanup context.Context) error {
				rollbacks++
				if tt.phase == "rollback" {
					cancel()
				}
				require.NoError(t, cleanup.Err(), "rollback must remain usable after the unit is canceled")
				require.Equal(t, "preserved", cleanup.Value(lifecycleTestContextKey{}), "cleanup should preserve useful context values")
				deadline, ok := cleanup.Deadline()
				require.True(t, ok, "detached rollback must still be bounded")
				require.InDelta(t, 250, time.Until(deadline).Milliseconds(), 50, "rollback cleanup should have its own 250ms bound")
				return nil
			}
			starter := lifecycleTransactionStarterFunc(func(unit context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
				begins++
				if tt.phase == "begin" {
					cancel()
					return nil, unit.Err()
				}
				return tx, nil
			})
			err := runLifecycleTransaction(ctx, starter, func(context.Context, pgx.Tx) error {
				if tt.phase == "rollback" {
					return &pgconn.PgError{Code: "55P03"}
				}
				cancel()
				if tt.phase == "contention" {
					return errors.Join(&pgconn.PgError{Code: "55P03"}, context.Canceled)
				}
				return nil
			})
			require.ErrorIs(t, err, tt.result, "cancellation or an expired caller deadline should remain observable")
			require.Equal(t, 0, commits, "a known canceled transaction must never knowingly commit")
			wantBegins, wantRollbacks := 1, 1
			switch tt.phase {
			case "before", "expired":
				wantBegins, wantRollbacks = 0, 0
			case "begin":
				wantRollbacks = 0
			}
			require.Equal(t, wantBegins, begins, "cancellation must not cause additional acquisition attempts")
			require.Equal(t, wantRollbacks, rollbacks, "any opened transaction must receive bounded cleanup")
		})
	}
}

type lifecycleTestContextKey struct{}

func TestRunLifecycleTransactionDeadlineBoundsEveryPhase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		phase string
	}{
		{name: "pool acquisition", phase: "begin"},
		{name: "local settings", phase: "setup"},
		{name: "callback work", phase: "work"},
		{name: "commit", phase: "commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			tx := newLifecycleTransactionTestTx()
			begins, works, commits, rollbacks := 0, 0, 0, 0
			tx.exec = func(unit context.Context, _ string) (pgconn.CommandTag, error) {
				if tt.phase == "setup" {
					<-unit.Done()
					return pgconn.CommandTag{}, unit.Err()
				}
				return pgconn.NewCommandTag("SELECT 1"), nil
			}
			tx.commit = func(unit context.Context) error {
				commits++
				<-unit.Done()
				return unit.Err()
			}
			tx.rollback = func(cleanup context.Context) error {
				rollbacks++
				require.NoError(t, cleanup.Err(), "deadline expiry should not cancel the separate cleanup context")
				return nil
			}
			starter := lifecycleTransactionStarterFunc(func(unit context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
				begins++
				if tt.phase == "begin" {
					<-unit.Done()
					return nil, unit.Err()
				}
				return tx, nil
			})
			err := runLifecycleTransaction(ctx, starter, func(unit context.Context, _ pgx.Tx) error {
				works++
				if tt.phase == "work" {
					<-unit.Done()
					return unit.Err()
				}
				return nil
			})
			require.ErrorIs(t, err, context.DeadlineExceeded, "the shared deadline should bound the selected blocking phase")
			require.Equal(t, 1, begins, "a deadline must never cause acquisition replay")
			wantWorks, wantCommits, wantRollbacks := 1, 0, 1
			switch tt.phase {
			case "begin":
				wantWorks, wantRollbacks = 0, 0
			case "setup":
				wantWorks = 0
			case "commit":
				wantCommits = 1
			}
			require.Equal(t, wantWorks, works, "deadline should not invoke any later callback")
			require.Equal(t, wantCommits, commits, "no known expired unit should enter commit")
			require.Equal(t, wantRollbacks, rollbacks, "opened transaction should be cleaned up after deadline failure")
		})
	}
}

func TestRunLifecycleTransactionPanicRollsBackThenPropagates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rollbackErr error
	}{
		{name: "successful cleanup"},
		{name: "failed cleanup retains original panic", rollbackErr: io.EOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tx := newLifecycleTransactionTestTx()
			rollbacks := 0
			tx.rollback = func(ctx context.Context) error {
				rollbacks++
				require.NoError(t, ctx.Err(), "panic cleanup should use a live bounded context")
				return tt.rollbackErr
			}
			starter := lifecycleTransactionStarterFunc(func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil })
			require.PanicsWithValue(t, "synthetic callback panic", func() {
				runLifecycleTransaction(context.Background(), starter, func(context.Context, pgx.Tx) error {
					panic("synthetic callback panic")
				})
			}, "cleanup must not swallow or replace the original panic")
			require.Equal(t, 1, rollbacks, "a panicking attempt should receive exactly one rollback")
		})
	}
}

func TestRunLifecycleTransactionGoexitRollsBackWithoutRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rollbackErr error
	}{
		{name: "successful cleanup"},
		{name: "failed cleanup preserves goroutine exit", rollbackErr: io.EOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), lifecycleTestContextKey{}, "preserved"))
			defer cancel()
			begins, calls, commits, rollbacks := 0, 0, 0, 0
			returned := false
			var cleanupErr error
			var cleanupValue any
			var cleanupRemaining time.Duration
			var cleanupBounded bool
			tx := newLifecycleTransactionTestTx()
			tx.commit = func(context.Context) error { commits++; return nil }
			tx.rollback = func(cleanup context.Context) error {
				rollbacks++
				cleanupErr = cleanup.Err()
				cleanupValue = cleanup.Value(lifecycleTestContextKey{})
				deadline, bounded := cleanup.Deadline()
				cleanupBounded = bounded
				cleanupRemaining = time.Until(deadline)
				return tt.rollbackErr
			}
			starter := lifecycleTransactionStarterFunc(func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
				begins++
				return tx, nil
			})
			done := make(chan struct{})
			go func() {
				defer close(done)
				runLifecycleTransaction(ctx, starter, func(context.Context, pgx.Tx) error {
					calls++
					cancel()
					runtime.Goexit()
					return nil
				})
				returned = true
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("goroutine exit must complete bounded transaction cleanup")
			}
			require.False(t, returned, "transaction cleanup must not convert Goexit into a normal return")
			require.Equal(t, 1, begins, "goroutine exit must not acquire a replacement transaction")
			require.Equal(t, 1, calls, "goroutine exit must not replay callback work")
			require.Equal(t, 0, commits, "goroutine exit must never commit the interrupted callback")
			require.Equal(t, 1, rollbacks, "goroutine exit must trigger exactly one rollback")
			require.NoError(t, cleanupErr, "cancellation before Goexit must not cancel detached cleanup")
			require.Equal(t, "preserved", cleanupValue, "detached cleanup should retain context values")
			require.True(t, cleanupBounded, "Goexit cleanup must have a separate finite deadline")
			require.Positive(t, cleanupRemaining, "Goexit cleanup should start with usable time")
			require.LessOrEqual(t, cleanupRemaining, 250*time.Millisecond, "Goexit cleanup must not exceed its fixed rollback bound")
		})
	}
}

func TestLifecycleTransactionNilDependencies(t *testing.T) {
	t.Parallel()
	var typedNilStarter lifecycleTransactionStarterFunc
	var typedNilTx *lifecycleTransactionTestTx
	validStarter := lifecycleTransactionStarterFunc(func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return nil, nil })
	work := func(context.Context, pgx.Tx) error { return nil }
	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{name: "runner nil context", run: func() error { return runLifecycleTransaction(nil, validStarter, work) }, want: "nil context"}, //nolint:staticcheck // SA1012: nil is intentional input to the defensive-validation regression.
		{name: "nil starter", run: func() error { return runLifecycleTransaction(context.Background(), nil, work) }, want: "nil starter"},
		{name: "typed nil starter", run: func() error { return runLifecycleTransaction(context.Background(), typedNilStarter, work) }, want: "nil starter"},
		{name: "nil callback", run: func() error { return runLifecycleTransaction(context.Background(), validStarter, nil) }, want: "nil work"},
		{name: "starter returns nil transaction", run: func() error { return runLifecycleTransaction(context.Background(), validStarter, work) }, want: "nil transaction"},
		{name: "configurator nil context", run: func() error { return ConfigureLifecycleTransactionInTx(nil, typedNilTx) }, want: "nil context"}, //nolint:staticcheck // SA1012: nil is intentional input to the defensive-validation regression.
		{name: "configurator nil transaction", run: func() error { return ConfigureLifecycleTransactionInTx(context.Background(), nil) }, want: "nil transaction"},
		{name: "configurator typed nil transaction", run: func() error { return ConfigureLifecycleTransactionInTx(context.Background(), typedNilTx) }, want: "nil transaction"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorContains(t, tt.run(), tt.want, "invalid dependencies should return a clear error without invoking work")
		})
	}
}
