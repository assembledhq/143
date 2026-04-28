// migrate-coding-credentials-anthropic-split is the encrypted-blob post-step
// for the unified-coding-credentials migration. See
// docs/design/future/65-unified-coding-credentials.md § "Why a Go-side
// post-step for Anthropic split".
//
// For each row in coding_credentials with provider='anthropic':
//   - Decrypt the config blob.
//   - JSON-parse into AnthropicConfig.
//   - If Subscription != nil, write a new AnthropicSubscriptionConfig row
//     with the OAuth/PKCE fields and rewrite the original row's provider to
//     'anthropic_subscription' with a config that holds *only* the
//     subscription fields (no api_key carry-over). When a row had both an
//     APIKey AND a Subscription set (legal in the legacy schema), the API
//     key is dropped — the design splits each method into its own row, and
//     duplicating the API key here would create a phantom row the user
//     never explicitly added.
//   - If Subscription == nil, leave it (already an API-key row).
//
// Idempotent: rows already at provider='anthropic_subscription' are skipped.
// Batched by (created_at, id) so partial completion is safe to resume.
// Writes a sentinel ('anthropic_split', now()) to coding_credentials_migrations
// on completion; the main app refuses to serve traffic until that sentinel
// lands for the current schema version.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

const sentinelName = "anthropic_split"

func main() {
	var (
		batchSize int
		dryRun    bool
		stmtTOMs  int
	)
	flag.IntVar(&batchSize, "batch", 500, "rows per transaction")
	flag.BoolVar(&dryRun, "dry-run", false, "decrypt and inspect rows without writing")
	flag.IntVar(&stmtTOMs, "row-timeout-ms", 5000, "per-row statement_timeout in ms")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable" // #nosec G101 -- dev default
	}
	masterKey := os.Getenv("ENCRYPTION_MASTER_KEY")

	cryptoSvc, err := crypto.NewService(masterKey)
	if err != nil {
		fail("init crypto: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fail("connect: %v", err)
	}
	defer pool.Close()

	// Single-writer guard: take a session-scoped advisory lock so two
	// operators kicking off the migration concurrently don't double-scan
	// every row. Each row's UPDATE is already idempotent (WHERE provider =
	// 'anthropic'), but the duplicated decrypt/encrypt work is wasteful and
	// the second runner's progress messages would be confusing. The lock
	// hashes a stable string into the bigint argument pg_advisory_lock
	// expects.
	if err := acquireRunLock(ctx, pool); err != nil {
		fail("acquire run lock: %v", err)
	}

	if alreadyDone, err := isComplete(ctx, pool); err != nil {
		fail("check sentinel: %v", err)
	} else if alreadyDone {
		fmt.Println("[anthropic-split] sentinel already present; nothing to do")
		return
	}

	var totalProcessed, totalSplit int
	var lastCreatedAt time.Time
	var lastID uuid.UUID
	for {
		select {
		case <-ctx.Done():
			fmt.Println("[anthropic-split] interrupted; partial progress preserved")
			return
		default:
		}
		processed, splits, lastCA, lastI, err := runBatch(ctx, pool, cryptoSvc, batchSize, stmtTOMs, dryRun, lastCreatedAt, lastID)
		if err != nil {
			fail("batch: %v", err)
		}
		totalProcessed += processed
		totalSplit += splits
		fmt.Printf("[anthropic-split] processed=%d split=%d (cumulative processed=%d split=%d)\n",
			processed, splits, totalProcessed, totalSplit)
		if processed == 0 {
			break
		}
		lastCreatedAt, lastID = lastCA, lastI
	}

	if dryRun {
		fmt.Printf("[anthropic-split] dry-run complete; would split %d / %d rows\n", totalSplit, totalProcessed)
		return
	}

	if err := writeSentinel(ctx, pool); err != nil {
		fail("write sentinel: %v", err)
	}
	fmt.Printf("[anthropic-split] done; processed=%d split=%d sentinel written\n", totalProcessed, totalSplit)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[anthropic-split] error: "+format+"\n", args...)
	os.Exit(1)
}

// acquireRunLock takes a non-blocking session-scoped advisory lock. Returns
// an error if another runner already holds it. The lock is released when the
// connection closes (pgxpool returns the conn on shutdown).
func acquireRunLock(ctx context.Context, pool *pgxpool.Pool) error {
	var got bool
	if err := pool.QueryRow(ctx,
		`SELECT pg_try_advisory_lock(hashtextextended($1, 0))`,
		"migrate-coding-credentials-"+sentinelName,
	).Scan(&got); err != nil {
		return fmt.Errorf("query advisory lock: %w", err)
	}
	if !got {
		return fmt.Errorf("another runner holds the migration lock; wait for it to finish or release the session")
	}
	return nil
}

func isComplete(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	// completed_at has a NOT NULL DEFAULT now() at the schema level so any
	// row whose name matches indicates completion; we don't need to inspect
	// the timestamp itself.
	var name string
	err := pool.QueryRow(ctx,
		`SELECT name FROM coding_credentials_migrations WHERE name = $1`,
		sentinelName,
	).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func writeSentinel(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO coding_credentials_migrations (name) VALUES ($1)
		 ON CONFLICT (name) DO UPDATE SET completed_at = now()`,
		sentinelName,
	)
	return err
}

// runBatch processes one batch of anthropic rows after the cursor
// (createdAtCursor, idCursor). Returns (processed, splits, newLastCreatedAt,
// newLastID, error).
func runBatch(
	ctx context.Context,
	pool *pgxpool.Pool,
	cryptoSvc *crypto.Service,
	batchSize, stmtTOMs int,
	dryRun bool,
	createdAtCursor time.Time,
	idCursor uuid.UUID,
) (int, int, time.Time, uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, createdAtCursor, idCursor, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", stmtTOMs)); err != nil {
		return 0, 0, createdAtCursor, idCursor, fmt.Errorf("set statement_timeout: %w", err)
	}

	// Cursor pagination: keyset on (created_at, id) so a single stuck row can
	// be skipped (operator manually advances the cursor and re-runs) without
	// rescanning earlier rows.
	rows, err := tx.Query(ctx, `
		SELECT id, org_id, user_id, label, config, priority, status, created_by, last_verified_at, created_at, updated_at
		FROM coding_credentials
		WHERE provider = 'anthropic'
		  AND (created_at, id) > ($1, $2)
		ORDER BY created_at, id
		LIMIT $3`,
		createdAtCursor, idCursor, batchSize,
	)
	if err != nil {
		return 0, 0, createdAtCursor, idCursor, fmt.Errorf("query batch: %w", err)
	}

	type batchRow struct {
		ID             uuid.UUID
		OrgID          uuid.UUID
		UserID         *uuid.UUID
		Label          string
		Config         []byte
		Priority       int
		Status         string
		CreatedBy      *uuid.UUID
		LastVerifiedAt *time.Time
		CreatedAt      time.Time
		UpdatedAt      time.Time
	}
	var batch []batchRow
	for rows.Next() {
		var r batchRow
		if err := rows.Scan(&r.ID, &r.OrgID, &r.UserID, &r.Label, &r.Config, &r.Priority, &r.Status, &r.CreatedBy, &r.LastVerifiedAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
			rows.Close()
			return 0, 0, createdAtCursor, idCursor, fmt.Errorf("scan row: %w", err)
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, createdAtCursor, idCursor, err
	}

	if len(batch) == 0 {
		return 0, 0, createdAtCursor, idCursor, nil
	}

	splits := 0
	for _, r := range batch {
		plaintext, err := decryptCfg(cryptoSvc, r.Config)
		if err != nil {
			return 0, 0, createdAtCursor, idCursor, fmt.Errorf("decrypt %s: %w", r.ID, err)
		}
		var cfg models.AnthropicConfig
		if err := json.Unmarshal(plaintext, &cfg); err != nil {
			return 0, 0, createdAtCursor, idCursor, fmt.Errorf("unmarshal %s: %w", r.ID, err)
		}
		if cfg.Subscription == nil {
			// Pure API-key row — leave alone.
			continue
		}

		// Subscription row: rewrite provider+config in place.
		newCfg := models.FromAnthropicSubscription(*cfg.Subscription)
		newPlain, err := json.Marshal(newCfg)
		if err != nil {
			return 0, 0, createdAtCursor, idCursor, fmt.Errorf("marshal split %s: %w", r.ID, err)
		}
		newCipher, err := encryptCfg(cryptoSvc, newPlain)
		if err != nil {
			return 0, 0, createdAtCursor, idCursor, fmt.Errorf("encrypt split %s: %w", r.ID, err)
		}

		if dryRun {
			splits++
			continue
		}

		tag, err := tx.Exec(ctx,
			`UPDATE coding_credentials
			 SET provider = 'anthropic_subscription', config = $1, updated_at = now()
			 WHERE id = $2 AND provider = 'anthropic'`,
			newCipher, r.ID,
		)
		if err != nil {
			return 0, 0, createdAtCursor, idCursor, fmt.Errorf("rewrite %s: %w", r.ID, err)
		}
		if tag.RowsAffected() == 0 {
			// Concurrent rewrite — skip without splitting.
			continue
		}
		splits++
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, createdAtCursor, idCursor, fmt.Errorf("commit: %w", err)
	}

	last := batch[len(batch)-1]
	return len(batch), splits, last.CreatedAt, last.ID, nil
}

func decryptCfg(svc *crypto.Service, data []byte) ([]byte, error) {
	if svc != nil {
		return svc.Decrypt(data)
	}
	return crypto.DevDecrypt(data)
}

func encryptCfg(svc *crypto.Service, data []byte) ([]byte, error) {
	if svc != nil {
		return svc.Encrypt(data)
	}
	return crypto.DevEncrypt(data), nil
}
