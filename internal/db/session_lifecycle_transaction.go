package db

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

const (
	// LifecycleTransactionTimeout covers acquisition, setup, work, retries and
	// commit together. Rollback has a separate bounded cleanup context.
	LifecycleTransactionTimeout = 2 * time.Second

	lifecycleTransactionAttempts = 3
	lifecycleRollbackTimeout     = 250 * time.Millisecond
	lifecycleRetryMinimum        = 25 * time.Millisecond
	lifecycleRetryMaximum        = 100 * time.Millisecond
	lifecycleWriterVersion       = "1"
	lifecycleLockTimeout         = "250ms"
	lifecycleStatementTimeout    = "1500ms"

	lifecycleTransactionSettings = `SELECT
		set_config('lifecycle143.writer_version', '` + lifecycleWriterVersion + `', true),
		set_config('lock_timeout', '` + lifecycleLockTimeout + `', true),
		set_config('statement_timeout', '` + lifecycleStatementTimeout + `', true)`

	lifecycleTransactionPolicyQuery = `SELECT
		current_setting('transaction_isolation'),
		COALESCE(current_setting('lifecycle143.writer_version', true), ''),
		current_setting('lock_timeout'),
		current_setting('statement_timeout')`
)

// lifecycleTxStarter is private for deterministic tests. A structural BeginTx
// interface is unsafe as the public boundary: an already-owned pgx.Conn also
// satisfies it, and BEGIN there could reuse and later commit its outer unit.
type lifecycleTxStarter interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// ErrLifecycleContention identifies an exhausted, confirmed-aborted lock unit.
var ErrLifecycleContention = errors.New("lifecycle transaction contended")

// LifecycleContentionError preserves the last PostgreSQL contention error. It
// means no attempt committed; it does not authorize replay of external effects.
type LifecycleContentionError struct {
	Attempts int
	Cause    error
}

func (e *LifecycleContentionError) Error() string {
	return fmt.Sprintf("%s after %d attempts: %v", ErrLifecycleContention, e.Attempts, e.Cause)
}

func (e *LifecycleContentionError) Unwrap() error { return e.Cause }

func (e *LifecycleContentionError) Is(target error) bool { return target == ErrLifecycleContention }

// ConfigureLifecycleTransactionInTx validates a supplied READ COMMITTED
// transaction and installs transaction-local protocol settings before work.
// It never begins, commits, rolls back or retries. The owner must roll back if
// setup fails. The version setting is only a compatibility marker, not tenant,
// actor, owner, epoch, lease or activation authority.
func ConfigureLifecycleTransactionInTx(ctx context.Context, tx pgx.Tx) error {
	if ctx == nil {
		return errors.New("configure lifecycle transaction: nil context")
	}
	if nilLifecycleDependency(tx) {
		return errors.New("configure lifecycle transaction: nil transaction")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("configure lifecycle transaction: %w", err)
	}
	var isolation string
	if err := tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&isolation); err != nil {
		return fmt.Errorf("read lifecycle transaction isolation: %w", err)
	}
	if isolation != string(pgx.ReadCommitted) {
		return fmt.Errorf("configure lifecycle transaction: READ COMMITTED required, got %q", isolation)
	}
	if _, err := tx.Exec(ctx, lifecycleTransactionSettings); err != nil {
		return fmt.Errorf("configure lifecycle transaction settings: %w", err)
	}
	return nil
}

// requireLifecycleScopeTransactionInTx observes the effective policy for the
// private scope-lock kernel; it never installs settings or creates a budget.
// Its caller validates nonnil ctx/tx. Effective settings cannot establish how
// they were configured, and a remaining deadline cannot prove transaction age.
// The transaction owner must establish the whole-unit budget before acquisition.
func requireLifecycleScopeTransactionInTx(ctx context.Context, tx pgx.Tx) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lifecycle scope transaction context: %w", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > LifecycleTransactionTimeout {
		return errors.New("lifecycle scope requires an existing deadline within the lifecycle transaction budget")
	}
	var actual [4]string
	if err := tx.QueryRow(ctx, lifecycleTransactionPolicyQuery).Scan(&actual[0], &actual[1], &actual[2], &actual[3]); err != nil {
		return fmt.Errorf("read lifecycle scope transaction policy: %w", err)
	}
	expected := [4]string{string(pgx.ReadCommitted), lifecycleWriterVersion, lifecycleLockTimeout, lifecycleStatementTimeout}
	if actual != expected {
		return errors.New("lifecycle scope requires the configured READ COMMITTED lifecycle transaction policy")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lifecycle scope transaction context after policy read: %w", err)
	}
	return nil
}

// RunLifecycleTransaction acquires a connection from pool for one bounded,
// top-level DB-only unit of work. It deliberately accepts only a pool, not a raw
// connection or structural BeginTx interface that might already own a transaction.
// Work may run
// more than once and must re-read/revalidate durable authority in each fresh
// transaction. It must use the supplied context, must not begin, commit or roll
// back transactions, and must not call providers, publish, notify or mutate any
// externally observable state. Reset/overwrite captured results/effects on EVERY
// invocation; never accumulate aborted-attempt values. Only the final committed
// attempt's values are valid after this function returns nil. Discard all values
// on any error or panic.
//
// Only setup/work SQLSTATE 40P01 and 55P03 can replay, after an explicit successful
// whole-transaction rollback. Begin and commit errors never replay, including a
// commit error reporting rollback: callers must resolve the exact command receipt
// rather than infer permission to resubmit. Cancellation cannot bound a callback
// that ignores its context, but an expired unit is never knowingly committed.
// A live context before Commit does not reserve time for it; deadline expiry or
// connection loss during commit remains an ambiguous outcome requiring receipt
// resolution, never replay.
// This passive helper is not a writer census, consumer registration, cutover
// interlock or proof that the lifecycle protocol is active.
func RunLifecycleTransaction(ctx context.Context, pool *pgxpool.Pool, work func(context.Context, pgx.Tx) error) error {
	if pool == nil {
		return errors.New("run lifecycle transaction: nil pool")
	}
	return runLifecycleTransaction(ctx, pool, work)
}

func runLifecycleTransaction(ctx context.Context, starter lifecycleTxStarter, work func(context.Context, pgx.Tx) error) error {
	if ctx == nil {
		return errors.New("run lifecycle transaction: nil context")
	}
	if nilLifecycleDependency(starter) {
		return errors.New("run lifecycle transaction: nil starter")
	}
	if work == nil {
		return errors.New("run lifecycle transaction: nil work")
	}
	unitCtx, cancel := context.WithTimeout(ctx, LifecycleTransactionTimeout)
	defer cancel()

	for attempt := 1; attempt <= lifecycleTransactionAttempts; attempt++ {
		if err := unitCtx.Err(); err != nil {
			return fmt.Errorf("lifecycle transaction budget: %w", err)
		}
		tx, err := starter.BeginTx(unitCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return fmt.Errorf("begin lifecycle transaction: %w", err)
		}
		if nilLifecycleDependency(tx) {
			return errors.New("begin lifecycle transaction: nil transaction")
		}

		workErr, commitAttempted := runLifecycleAttempt(unitCtx, tx, work)
		if workErr == nil {
			return nil
		}
		if rollbackErr := rollbackLifecycleTransaction(unitCtx, tx); rollbackErr != nil {
			return errors.Join(workErr, fmt.Errorf("rollback lifecycle transaction: %w", rollbackErr))
		}
		if unitErr := unitCtx.Err(); unitErr != nil {
			return errors.Join(workErr, fmt.Errorf("lifecycle transaction budget: %w", unitErr))
		}
		if commitAttempted || !retryableLifecycleTransaction(workErr) {
			return workErr
		}
		if attempt == lifecycleTransactionAttempts {
			return &LifecycleContentionError{Attempts: attempt, Cause: workErr}
		}
		timer := time.NewTimer(lifecycleRetryDelay())
		select {
		case <-unitCtx.Done():
			timer.Stop()
			return errors.Join(fmt.Errorf("lifecycle transaction retry budget: %w", unitCtx.Err()), workErr)
		case <-timer.C:
		}
	}
	panic("unreachable lifecycle transaction attempt")
}

func lifecycleRetryDelay() time.Duration {
	return lifecycleRetryMinimum + time.Duration(rand.Int64N(int64(lifecycleRetryMaximum-lifecycleRetryMinimum)+1)) // #nosec G404 -- bounded contention backoff only; never credentials, identity or authorization
}

func runLifecycleAttempt(ctx context.Context, tx pgx.Tx, work func(context.Context, pgx.Tx) error) (error, bool) {
	returned := false
	defer func() {
		// Both panic and runtime.Goexit unwind defers without returning normally.
		// Do not recover: cleanup must preserve the original abnormal exit.
		if !returned {
			if rollbackErr := rollbackLifecycleTransaction(ctx, tx); rollbackErr != nil {
				zerolog.Ctx(ctx).Warn().Err(rollbackErr).Msg("failed to roll back interrupted lifecycle transaction")
			}
		}
	}()
	err, commitAttempted := invokeLifecycleAttempt(ctx, tx, work)
	returned = true
	return err, commitAttempted
}

func invokeLifecycleAttempt(ctx context.Context, tx pgx.Tx, work func(context.Context, pgx.Tx) error) (error, bool) {
	if err := ConfigureLifecycleTransactionInTx(ctx, tx); err != nil {
		return err, false
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lifecycle transaction budget before work: %w", err), false
	}
	if err := work(ctx, tx); err != nil {
		return err, false
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lifecycle transaction budget before commit: %w", err), false
	}
	return tx.Commit(ctx), true
}

func rollbackLifecycleTransaction(ctx context.Context, tx pgx.Tx) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleRollbackTimeout)
	defer cancel()
	// pgx closes a connection whose rollback fails; pgxpool then releases it.
	// Never call tx.Conn().Close here: a pooled transaction may already have
	// released that connection for another borrower, notably after Commit.
	return tx.Rollback(cleanupCtx)
}

func retryableLifecycleTransaction(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// A joined error may also contain a network/application failure. One nested
	// SQLSTATE must not hide that ambiguity and grant permission to replay.
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if _, joined := cause.(interface{ Unwrap() []error }); joined {
			return false
		}
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "55P03")
}

func nilLifecycleDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
