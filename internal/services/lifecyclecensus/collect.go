package lifecyclecensus

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/jackc/pgx/v5"
)

// Budget includes connection establishment, transaction setup and both reads.
// It is cooperative, not a hard process deadline. Each cleanup operation gets
// a separate cooperative 250ms context after collection, including on failure.
const Budget = 5 * time.Second

type censusConnection interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	Close(context.Context) error
	CleanupDone() <-chan struct{}
}

type connectFunc func(context.Context, *pgx.ConnConfig) (censusConnection, error)

type directCensusConnection struct{ *pgx.Conn }

func (c directCensusConnection) CleanupDone() <-chan struct{} {
	return c.PgConn().CleanupDone()
}

// Collect opens one direct connection (not a pool) and performs only read-only
// database work. Errors are deliberately static: driver errors can contain DSNs,
// server-provided strings or query text and must not become report diagnostics.
func Collect(ctx context.Context, databaseURL string) (Report, error) {
	return collect(ctx, databaseURL, func(ctx context.Context, config *pgx.ConnConfig) (censusConnection, error) {
		conn, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			return nil, err
		}
		return directCensusConnection{conn}, nil
	})
}

func collect(parent context.Context, databaseURL string, connect connectFunc) (report Report, resultErr error) {
	ctx, cancel := context.WithTimeout(parent, Budget)
	defer cancel()
	if ctx.Err() != nil {
		return Report{}, collectionError(ctx, "observation canceled")
	}
	config, err := censusConfig(databaseURL)
	if err != nil {
		return Report{}, err
	}
	// URI options, PGOPTIONS and differently cased aliases must not override
	// the census's fixed session name and bounds.
	for key := range config.RuntimeParams {
		if strings.EqualFold(key, "application_name") || strings.EqualFold(key, "statement_timeout") || strings.EqualFold(key, "lock_timeout") || strings.EqualFold(key, "options") {
			delete(config.RuntimeParams, key)
		}
	}
	config.RuntimeParams["application_name"] = "143:lifecycle_census"
	// These server-enforced bounds survive a client process exit before pgx's
	// asynchronous cancellation finishes. They bound each statement/lock wait,
	// not total wall time across connection, both statements and cleanup.
	config.RuntimeParams["statement_timeout"] = "5000"
	config.RuntimeParams["lock_timeout"] = "5000"
	conn, err := connect(ctx, config)
	if err != nil {
		return Report{}, collectionError(ctx, "database connection failed")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		closeErr := conn.Close(cleanup)
		// Close can be a no-op after a canceled pgx read: its underlying socket
		// and CancelRequest may still be owned by an asynchronous goroutine.
		// Await that work within the SAME close budget before the CLI can exit.
		done := conn.CleanupDone()
		select {
		case <-done:
		default:
			select {
			case <-done:
			case <-cleanup.Done():
				closeErr = cleanup.Err()
			}
		}
		if closeErr != nil {
			report = Report{}
			resultErr = errors.Join(resultErr, errors.New("database connection cleanup failed"))
		}
	}()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Report{}, collectionError(ctx, "read-only transaction setup failed")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		if err := tx.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			report = Report{}
			resultErr = errors.Join(resultErr, errors.New("read-only transaction cleanup failed"))
		}
	}()
	store := db.NewLifecycleCensusStore(tx)
	writers, err := store.Writers(ctx)
	if err != nil {
		return Report{}, collectionError(ctx, "writer observation failed")
	}
	connections, err := store.Connections(ctx)
	if err != nil {
		return Report{}, collectionError(ctx, "connection observation failed")
	}
	if ctx.Err() != nil {
		return Report{}, collectionError(ctx, "observation canceled")
	}
	return assemble(writers, connections), nil
}

func collectionError(ctx context.Context, stage string) error {
	// Only the context's standard cause is safe to forward; never wrap driver
	// errors, which may contain arbitrary server- or configuration-derived text.
	return errors.Join(errors.New(stage), ctx.Err())
}
