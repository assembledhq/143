package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestLifecycleCutoverRunnerRetriesFreshWholeUnitPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		exclusive bool
	}{
		{name: "shared observation retries after actual advisory false"},
		{name: "exclusive acquisition retries after actual advisory false", exclusive: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID := newLifecycleCutoverPostgresFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			want := lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID, version: 7, state: models.LifecycleCutoverStateShadow}
			insertLifecycleCutoverPostgresRow(t, ctx, pool, want)
			_, err := pool.Exec(ctx, `CREATE TABLE lifecycle_cutover_retry_effect_probe(org_id uuid NOT NULL,attempt integer NOT NULL,PRIMARY KEY(org_id,attempt))`)
			require.NoError(t, err, "create a DB-only effect probe inside the generated owned fixture database")
			blocker := beginSessionLifecycleProbe(t, ctx, pool)
			// The independent literal key/SQL establishes a real external owner;
			// no helper agreement, sleeps or elapsed-time inference creates the conflict.
			key := "session_lifecycle_cutover:" + orgID.String() + ":" + sessionID.String()
			var acquired bool
			require.NoError(t, blocker.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text, 0))`, key).Scan(&acquired), "hold the exact independently specified exclusive protocol key")
			require.True(t, acquired, "the test controller must own the exclusive blocker before starting the real public runner")
			before := lifecycleCutoverPostgresRows(t, ctx, blocker, orgID, sessionID)
			retryReady := make(chan struct{}, 1)
			releaseRetry := make(chan struct{})
			result := make(chan error, 1)
			finished := make(chan struct{})
			var attempts int
			var firstTx pgx.Tx
			var firstBusy error
			var observations lifecycleCutoverObservation
			var identities [][2]string
			var deadlines []time.Time
			var policies [][4]string
			go func() {
				// Always deliver completion even if a future fixture helper invokes
				// Goexit. Assertions below run only after joining this goroutine.
				outcome := errors.New("cutover retry runner exited without returning")
				defer close(finished)
				defer func() { result <- outcome }()
				outcome = RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
					attempts++
					observations = lifecycleCutoverObservation{} // Discard every aborted attempt's capture.
					if attempts > 2 {
						return errors.New("cutover retry unexpectedly needed a third callback after blocker release")
					}
					deadline, ok := workCtx.Deadline()
					if !ok {
						return errors.New("cutover retry callback lost its whole-unit deadline")
					}
					deadlines = append(deadlines, deadline)
					var identity [2]string
					if readErr := tx.QueryRow(workCtx, `SELECT pg_backend_pid()::text,txid_current()::text`).Scan(&identity[0], &identity[1]); readErr != nil {
						return fmt.Errorf("read fresh retry transaction identity: %w", readErr)
					}
					identities = append(identities, identity)
					var policy [4]string
					if readErr := tx.QueryRow(workCtx, `SELECT current_setting('transaction_isolation'),COALESCE(current_setting('lifecycle143.writer_version',true),''),current_setting('lock_timeout'),current_setting('statement_timeout')`).Scan(&policy[0], &policy[1], &policy[2], &policy[3]); readErr != nil {
						return fmt.Errorf("read existing retry protocol policy: %w", readErr)
					}
					policies = append(policies, policy)
					if attempts == 1 {
						firstTx = tx
					} else {
						// A fresh callback is not sufficient proof by itself: inspect
						// the prior pgx owner and committed effects BEFORE staging any
						// second-attempt effect or allowing the blocker to be released.
						if _, closedErr := firstTx.Exec(workCtx, "SELECT 1"); !errors.Is(closedErr, pgx.ErrTxClosed) {
							return fmt.Errorf("first cutover attempt was not closed before retry: %v", closedErr)
						}
						var prior []int
						if readErr := tx.QueryRow(workCtx, `SELECT COALESCE(array_agg(attempt ORDER BY attempt),ARRAY[]::integer[]) FROM lifecycle_cutover_retry_effect_probe WHERE org_id=$1`, orgID).Scan(&prior); readErr != nil {
							return fmt.Errorf("read committed first-attempt effects: %w", readErr)
						}
						if len(prior) != 0 {
							return fmt.Errorf("first cutover attempt left committed effects: %v", prior)
						}
						retryReady <- struct{}{}
						select {
						case <-releaseRetry:
						case <-workCtx.Done():
							return workCtx.Err()
						}
					}
					if _, stageErr := tx.Exec(workCtx, `INSERT INTO lifecycle_cutover_retry_effect_probe(org_id,attempt) VALUES($1,$2)`, orgID, attempts); stageErr != nil {
						return fmt.Errorf("stage current whole-unit effect: %w", stageErr)
					}
					var lockErr error
					if tt.exclusive {
						lockErr = tryLockLifecycleCutoverExclusiveInTx(workCtx, tx, orgID, sessionID)
					}
					if lockErr == nil {
						observations, lockErr = observeLifecycleCutoverInTx(workCtx, tx, orgID, sessionID)
					}
					if attempts == 1 {
						firstBusy = lockErr
					}
					return lockErr
				})
			}()
			t.Cleanup(func() {
				cancel()
				cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				waitForResult(t, cleanupCtx, finished)
			})
			select {
			case <-retryReady:
			case outcome := <-result:
				waitForResult(t, ctx, finished)
				require.NoError(t, outcome, "the real runner must reach its fresh second callback before completing")
				t.Fatal("cutover runner completed without proving rollback at the retry barrier")
				return
			case <-ctx.Done():
				t.Fatal("cutover runner did not reach its retry barrier within the bounded test context")
				return
			}
			// The second callback is parked. Only this joined controller releases
			// its blocker; callback code never settles another owner's transaction.
			require.Equal(t, byte('T'), blocker.Conn().PgConn().TxStatus(), "the independent blocker must remain open until the fresh retry proves prior rollback")
			require.Equal(t, before, lifecycleCutoverPostgresRows(t, ctx, blocker, orgID, sessionID), "the first failed attempt must not change passive cutover metadata")
			require.NoError(t, blocker.Rollback(ctx), "release the independently owned blocker only after the retry barrier is reached")
			close(releaseRetry)
			require.NoError(t, waitForResult(t, ctx, result), "exactly one fresh whole-unit retry must commit after the real advisory blocker is released")
			waitForResult(t, ctx, finished)
			require.Equal(t, 2, attempts, "only the first aborted unit and the second successful unit may run")
			requireLifecycleCutoverPostgresBusy(t, firstBusy)
			require.Equal(t, 2, len(identities), "both callbacks must expose their actual backend and transaction identity")
			require.NotEqual(t, identities[0], identities[1], "retry must use a distinct PostgreSQL transaction; backend connection reuse is allowed")
			require.Equal(t, []time.Time{deadlines[0], deadlines[0]}, deadlines, "all callbacks must share the exact original whole-unit deadline rather than receive reset budgets")
			policy := [4]string{"read committed", "1", "250ms", "1500ms"}
			require.Equal(t, [][4]string{policy, policy}, policies, "both real transactions must retain the same exact bounded protocol settings")
			require.Equal(t, want, observations, "only the final committed callback may retain its complete passive metadata observation")
			var effects []int
			require.NoError(t, pool.QueryRow(ctx, `SELECT COALESCE(array_agg(attempt ORDER BY attempt),ARRAY[]::integer[]) FROM lifecycle_cutover_retry_effect_probe WHERE org_id=$1`, orgID).Scan(&effects), "read durable effects only after the runner and held blocker have settled")
			require.Equal(t, []int{2}, effects, "the rolled-back first attempt must leave no effect while exactly the final unit commits")
			require.Equal(t, before, lifecycleCutoverPostgresRows(t, ctx, pool, orgID, sessionID), "successful shared or exclusive retry must not activate or otherwise mutate cutover metadata")
			// Exclusive acquisition here is only an activation-style lock probe,
			// not an activation controller, readiness certificate or production caller.
		})
	}
}
