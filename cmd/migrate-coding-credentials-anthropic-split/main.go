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
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

const sentinelName = db.AnthropicSplitSentinel

// errDualSetWithoutAck signals that a batch encountered a dual-set Anthropic
// row (both APIKey and Subscription set) but --allow-dual-set was not passed.
// runBatch returns this before issuing any UPDATE so the deferred Rollback
// reverts the in-flight tx; main.go translates it into exit code 3.
var errDualSetWithoutAck = errors.New("dual-set anthropic row encountered without --allow-dual-set; aborting before any rewrite")

func main() {
	var (
		batchSize    int
		dryRun       bool
		stmtTOMs     int
		allowDualSet bool
	)
	flag.IntVar(&batchSize, "batch", 500, "rows per transaction")
	flag.BoolVar(&dryRun, "dry-run", false, "decrypt and inspect rows without writing")
	flag.IntVar(&stmtTOMs, "row-timeout-ms", 5000, "per-row statement_timeout in ms")
	flag.BoolVar(&allowDualSet, "allow-dual-set", false,
		"acknowledge that dual-set Anthropic rows (both APIKey and Subscription) "+
			"will have their APIKey dropped during the split. Required when any "+
			"such row is encountered; without it the migration aborts with exit 3 "+
			"so the operator can decide explicitly.")
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

	var totalProcessed, totalSplit, totalDualSet, totalSkipped int
	var lastCreatedAt time.Time
	var lastID uuid.UUID
	for {
		select {
		case <-ctx.Done():
			fmt.Println("[anthropic-split] interrupted; partial progress preserved")
			return
		default:
		}
		processed, splits, dualSet, skipped, lastCA, lastI, err := runBatch(ctx, pool, cryptoSvc, batchSize, stmtTOMs, dryRun, allowDualSet, lastCreatedAt, lastID)
		if err != nil {
			if errors.Is(err, errDualSetWithoutAck) {
				fmt.Fprintf(os.Stderr, "[anthropic-split] %v\n", err)
				fmt.Fprintf(os.Stderr, "[anthropic-split] no rewrites committed in the aborted batch; cumulative committed processed=%d split=%d dual_set=%d skipped=%d. Re-run with --allow-dual-set to acknowledge the API-key drop.\n",
					totalProcessed, totalSplit, totalDualSet, totalSkipped)
				os.Exit(3)
			}
			fail("batch: %v", err)
		}
		totalProcessed += processed
		totalSplit += splits
		totalDualSet += dualSet
		totalSkipped += skipped
		fmt.Printf("[anthropic-split] processed=%d split=%d dual_set=%d skipped=%d (cumulative processed=%d split=%d dual_set=%d skipped=%d)\n",
			processed, splits, dualSet, skipped, totalProcessed, totalSplit, totalDualSet, totalSkipped)
		if processed == 0 {
			break
		}
		lastCreatedAt, lastID = lastCA, lastI
	}

	if dryRun {
		fmt.Printf("[anthropic-split] dry-run complete; would split %d / %d rows (%d dual-set, %d skipped)\n", totalSplit, totalProcessed, totalDualSet, totalSkipped)
		if totalSkipped > 0 {
			os.Exit(2)
		}
		return
	}

	// Withhold the sentinel when any row was skipped: the post-step gate at
	// app boot reads this sentinel as "every anthropic row is in the new
	// shape", which is no longer true. The operator must inspect the skipped
	// rows (decrypt/unmarshal failures logged per-row) and either fix them
	// in-place or hand-write the sentinel after explicit acknowledgement.
	if totalSkipped > 0 {
		fmt.Fprintf(os.Stderr, "[anthropic-split] %d row(s) skipped due to per-row errors; sentinel NOT written. Inspect the per-row warnings above, fix the rows, and re-run. Exit 2.\n", totalSkipped)
		os.Exit(2)
	}

	if err := writeSentinel(ctx, pool); err != nil {
		fail("write sentinel: %v", err)
	}
	fmt.Printf("[anthropic-split] done; processed=%d split=%d dual_set=%d sentinel written\n", totalProcessed, totalSplit, totalDualSet)
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
// (createdAtCursor, idCursor). Returns (processed, splits, dualSet, skipped,
// newLastCreatedAt, newLastID, error). dualSet counts rows that carried both
// APIKey and Subscription. skipped counts rows whose decrypt/unmarshal/encrypt
// failed — they are logged per-row to stderr with row provenance and left in
// place for the operator to inspect; the cursor still advances past them so
// later rows are not blocked, and the caller withholds the sentinel when
// skipped > 0 so the boot gate stays closed until the operator acks.
// txBeginner is the narrow interface runBatch needs from a pool. *pgxpool.Pool
// satisfies it, and pgxmock.PgxPoolIface satisfies it in tests so the dual-set
// gate can be exercised without a real database.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

func runBatch(
	ctx context.Context,
	pool txBeginner,
	cryptoSvc *crypto.Service,
	batchSize, stmtTOMs int,
	dryRun bool,
	allowDualSet bool,
	createdAtCursor time.Time,
	idCursor uuid.UUID,
) (int, int, int, int, time.Time, uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, 0, 0, createdAtCursor, idCursor, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", stmtTOMs)); err != nil {
		return 0, 0, 0, 0, createdAtCursor, idCursor, fmt.Errorf("set statement_timeout: %w", err)
	}

	// Cursor pagination: keyset on (created_at, id) so a single stuck row can
	// be skipped (operator manually advances the cursor and re-runs) without
	// rescanning earlier rows.
	// Only active config versions are split. Inactive rows are immutable
	// version history under the insert-only schema (migration 000164) and
	// must keep the (provider, config) pair they were written with. The
	// active filter also keeps the (created_at, id) keyset unique — created_at
	// is carried across versions of the same logical id.
	rows, err := tx.Query(ctx, `
		SELECT id, org_id, user_id, label, config, priority, status, created_by, last_verified_at, created_at, updated_at
		FROM coding_credentials
		WHERE provider = 'anthropic'
		  AND active = true
		  AND (created_at, id) > ($1, $2)
		ORDER BY created_at, id
		LIMIT $3`,
		createdAtCursor, idCursor, batchSize,
	)
	if err != nil {
		return 0, 0, 0, 0, createdAtCursor, idCursor, fmt.Errorf("query batch: %w", err)
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
			return 0, 0, 0, 0, createdAtCursor, idCursor, fmt.Errorf("scan row: %w", err)
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, 0, 0, createdAtCursor, idCursor, err
	}

	if len(batch) == 0 {
		return 0, 0, 0, 0, createdAtCursor, idCursor, nil
	}

	splits, dualSet, skipped := 0, 0, 0
	for _, r := range batch {
		outcome, err := evaluateRowForSplit(cryptoSvc, r.Config)
		if err != nil {
			// Per-row failure: log with provenance and continue. Without
			// this the entire run aborts on a single decrypt failure (e.g.
			// key-rotation skew) leaving the operator with no path forward
			// short of hand-cursor manipulation. The sentinel is withheld
			// at the top level when skipped > 0 so the boot gate keeps the
			// app safely down until the row is fixed.
			skipped++
			fmt.Fprintf(os.Stderr,
				"[anthropic-split] SKIP row id=%s org_id=%s created_by=%s created_at=%s: %v\n",
				r.ID, r.OrgID, formatPtrUUID(r.CreatedBy), r.CreatedAt.Format(time.RFC3339), err,
			)
			continue
		}
		if outcome.Skip {
			continue
		}
		if outcome.HadDualSet {
			dualSet++
			fmt.Fprintf(os.Stderr,
				"[anthropic-split] WARNING: dual-set anthropic row id=%s org_id=%s created_by=%s created_at=%s carries both APIKey and Subscription; preserving subscription, dropping API key\n",
				r.ID, r.OrgID, formatPtrUUID(r.CreatedBy), r.CreatedAt.Format(time.RFC3339),
			)
			if !allowDualSet {
				// Fail closed: abort before any UPDATE in this tx so the
				// deferred Rollback reverts everything we did in this batch.
				// Earlier batches already committed are kept as-is — the
				// operator restarts with --allow-dual-set after deciding the
				// API-key drop is acceptable. dryRun also gates here so the
				// dry run reports the abort instead of advertising a clean
				// split count that hides the dual-set problem.
				return 0, 0, dualSet, skipped, createdAtCursor, idCursor, errDualSetWithoutAck
			}
		}

		if dryRun {
			splits++
			continue
		}

		tag, err := tx.Exec(ctx,
			`UPDATE coding_credentials
			 SET provider = 'anthropic_subscription', config = $1, updated_at = now()
			 WHERE id = $2 AND provider = 'anthropic' AND active = true`,
			outcome.NewCipher, r.ID,
		)
		if err != nil {
			return 0, 0, 0, 0, createdAtCursor, idCursor, fmt.Errorf("rewrite %s: %w", r.ID, err)
		}
		if tag.RowsAffected() == 0 {
			// Concurrent rewrite — skip without splitting.
			continue
		}
		splits++
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, 0, 0, createdAtCursor, idCursor, fmt.Errorf("commit: %w", err)
	}

	last := batch[len(batch)-1]
	return len(batch), splits, dualSet, skipped, last.CreatedAt, last.ID, nil
}

// formatPtrUUID renders an optional uuid for log lines without crashing on nil.
func formatPtrUUID(u *uuid.UUID) string {
	if u == nil {
		return "<nil>"
	}
	return u.String()
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

// splitOutcome describes what runBatch should do with a single row's
// encrypted config. Decoupling the decision from the DB write keeps the
// per-row JSON / dual-set logic unit-testable without a database.
type splitOutcome struct {
	// Skip is true for pure API-key rows that don't need rewriting. NewCipher
	// and HadDualSet are zero-valued in that case.
	Skip bool
	// NewCipher is the re-encrypted AnthropicSubscriptionConfig payload that
	// replaces the legacy AnthropicConfig blob on a subscription row.
	NewCipher []byte
	// HadDualSet flags rows that carried both APIKey and Subscription. The
	// rewrite preserves the subscription and drops the key; runBatch logs a
	// warning so the drop is visible in deploy telemetry.
	HadDualSet bool
}

// evaluateRowForSplit decides what to do with one anthropic row's blob.
// Pure over (cryptoSvc, config bytes); returns the outcome the caller should
// apply. Errors wrap the underlying decrypt / unmarshal / re-encrypt failure.
func evaluateRowForSplit(cryptoSvc *crypto.Service, config []byte) (splitOutcome, error) {
	plaintext, err := decryptCfg(cryptoSvc, config)
	if err != nil {
		return splitOutcome{}, fmt.Errorf("decrypt: %w", err)
	}
	var cfg models.AnthropicConfig
	if err := json.Unmarshal(plaintext, &cfg); err != nil {
		return splitOutcome{}, fmt.Errorf("unmarshal: %w", err)
	}
	if cfg.Subscription == nil {
		// Pure API-key row — leave alone.
		return splitOutcome{Skip: true}, nil
	}
	// Defensive: AnthropicConfig.Validate has rejected dual-set rows
	// (APIKey != "" && Subscription != nil) since the validator landed, so
	// this branch should be unreachable in healthy data. If we still find
	// one (e.g. a row written by a buggy code path before validation, or
	// hand-edited DB state), the rewrite preserves the subscription half but
	// drops the API-key half — the design splits each method into its own
	// row, and writing a phantom API-key row the user never explicitly
	// added would surprise the operator more than the drop. The caller logs
	// loudly so the drop is visible in deploy telemetry; an operator can
	// recover the lost key from a backup if needed.
	hadDualSet := cfg.APIKey != ""
	newCfg := models.FromAnthropicSubscription(*cfg.Subscription)
	newPlain, err := json.Marshal(newCfg)
	if err != nil {
		return splitOutcome{}, fmt.Errorf("marshal split: %w", err)
	}
	newCipher, err := encryptCfg(cryptoSvc, newPlain)
	if err != nil {
		return splitOutcome{}, fmt.Errorf("encrypt split: %w", err)
	}
	return splitOutcome{NewCipher: newCipher, HadDualSet: hadDualSet}, nil
}
