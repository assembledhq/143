package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// This proves the existing runner and provenance resolver can compose inside
// an enforced read-only transaction. It is not a caller acceptance observer,
// receipt comparison, authorization check or proof that a send was aborted.
func TestLifecycleObserverReadOnlyTransactionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		injectWrite bool
		wantSQLCode string
	}{
		{name: "mode observation commits without writes"},
		{name: "accidental mutation is rejected and rolled back", injectWrite: true, wantSQLCode: "25006"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Each leaf owns its full-schema database and one physical connection.
			// Migration/setup finishes before the unchanged two-second unit begins.
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 1)
			orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			before := lifecycleObserverReadOnlyRows(t, ctx, pool, orgID, sessionID)
			beforePID := backendPID(t, ctx, pool)
			var duringPID int32
			var mode ThreadSendMode
			var readOnly string
			var attempts int
			err := RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
				attempts++
				// The runner has already installed its transaction-local policy.
				// This must precede the observer's first source-data statement.
				if _, err := tx.Exec(workCtx, "SET TRANSACTION READ ONLY"); err != nil {
					return err
				}
				if err := tx.QueryRow(workCtx, "SELECT pg_backend_pid(), current_setting('transaction_read_only')").Scan(&duringPID, &readOnly); err != nil {
					return err
				}
				var err error
				mode, err = ObserveThreadSendModeInTx(workCtx, tx, orgID, sessionID)
				if err != nil {
					return err
				}
				if tt.injectWrite {
					_, err = tx.Exec(workCtx, "UPDATE sessions SET status='failed' WHERE org_id=$1 AND id=$2", orgID, sessionID)
				}
				return err
			})
			if tt.wantSQLCode == "" {
				require.NoError(t, err, "the real mode resolver must succeed after the runner configures an enforced read-only transaction")
			} else {
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, "a mutation injected after observation must preserve the PostgreSQL read-only violation")
				require.Equal(t, tt.wantSQLCode, pgErr.Code, "the database itself must reject observer writes rather than depend on a mock or convention")
			}
			require.Equal(t, 1, attempts, "successful observation and nonretryable read-only violations must each use only one bounded transaction")
			require.Equal(t, "on", readOnly, "mode observation must execute with effective transaction_read_only enabled")
			require.Equal(t, ThreadSendModeLegacy, mode, "the real shared advisory interlock and resolver must compose without writable row-locking")
			require.Equal(t, before, lifecycleObserverReadOnlyRows(t, ctx, pool, orgID, sessionID), "observation or its rejected mutation must leave all retained scope, messages, inbox entries and jobs byte-identical")
			var afterReadOnly string
			require.NoError(t, pool.QueryRow(ctx, "SHOW transaction_read_only").Scan(&afterReadOnly), "the next pool borrower must be able to inspect its independent transaction policy")
			require.Equal(t, "off", afterReadOnly, "observer read-only policy must not leak onto a subsequent borrower")
			require.Equal(t, []int32{beforePID, beforePID}, []int32{duringPID, backendPID(t, ctx, pool)}, "composition and cleanup must reuse exactly one physical connection without a hidden nested pool checkout")
		})
	}
}

func lifecycleObserverReadOnlyRows(t *testing.T, ctx context.Context, database DBTX, orgID, sessionID uuid.UUID) json.RawMessage {
	t.Helper()
	var snapshot json.RawMessage
	err := database.QueryRow(ctx, `SELECT jsonb_build_object(
		'session',(SELECT to_jsonb(s) FROM sessions s WHERE s.org_id=$1 AND s.id=$2),
		'threads',COALESCE((SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2),'[]'::jsonb),
		'cutovers',COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.session_id) FROM session_lifecycle_cutovers c WHERE c.org_id=$1 AND c.session_id=$2),'[]'::jsonb),
		'messages',COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM session_messages m WHERE m.org_id=$1 AND m.session_id=$2),'[]'::jsonb),
		'inbox',COALESCE((SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM thread_inbox_entries i WHERE i.org_id=$1 AND i.session_id=$2),'[]'::jsonb),
		'jobs',COALESCE((SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j WHERE j.org_id=$1),'[]'::jsonb))`, orgID, sessionID).Scan(&snapshot)
	require.NoError(t, err, "capture complete synthetic retained scope and effect rows with explicit tenant filters")
	return snapshot
}
