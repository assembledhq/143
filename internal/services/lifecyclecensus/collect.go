package lifecyclecensus

import (
	"context"
	"errors"
	"net/url"
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
}

type connectFunc func(context.Context, *pgx.ConnConfig) (censusConnection, error)

// Collect opens one direct connection (not a pool) and performs only read-only
// database work. Errors are deliberately static: driver errors can contain DSNs,
// server-provided strings or query text and must not become report diagnostics.
func Collect(ctx context.Context, databaseURL string) (Report, error) {
	return collect(ctx, databaseURL, func(ctx context.Context, config *pgx.ConnConfig) (censusConnection, error) {
		return pgx.ConnectConfig(ctx, config)
	})
}

func collect(parent context.Context, databaseURL string, connect connectFunc) (report Report, resultErr error) {
	ctx, cancel := context.WithTimeout(parent, Budget)
	defer cancel()
	if ctx.Err() != nil {
		return Report{}, collectionError(ctx, "observation canceled")
	}
	if strings.TrimSpace(databaseURL) == "" {
		return Report{}, errors.New("database configuration required")
	}
	// Require an explicit URI target, rather than letting libpq defaults or
	// PGHOST/PGDATABASE select an unintended local or remote database.
	target, err := url.Parse(databaseURL)
	if err != nil || (target.Scheme != "postgres" && target.Scheme != "postgresql") {
		return Report{}, errors.New("invalid database configuration")
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return Report{}, errors.New("invalid database configuration")
	}
	host := target.Hostname()
	if query.Has("host") {
		host = query.Get("host")
	}
	database := strings.TrimPrefix(target.Path, "/")
	if query.Has("dbname") {
		database = query.Get("dbname")
	}
	if query.Has("database") {
		database = query.Get("database")
	}
	if strings.TrimSpace(host) == "" || strings.TrimSpace(database) == "" {
		return Report{}, errors.New("explicit database host and name required")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return Report{}, errors.New("invalid database configuration")
	}
	config.RuntimeParams["application_name"] = "143:lifecycle_census"
	conn, err := connect(ctx, config)
	if err != nil {
		return Report{}, collectionError(ctx, "database connection failed")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		if err := conn.Close(cleanup); err != nil {
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
