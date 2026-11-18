package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestLifecycleScopePhysicalParentBeforeLanesPostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, sessionID, lanes := newLifecycleScopeConcurrencyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	tx := beginSessionLifecycleProbe(t, ctx, pool)
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "the owner must configure the existing transaction before scope acquisition")
	wrapper := &lifecycleScopeConcurrencyTx{Tx: tx}
	wrapper.beforeLaneQuery = func() {
		// This barrier runs BEFORE delegating the actual production lane Query.
		// Positive NOWAIT conflicts prove the parent is already locked while
		// successful lane probes prove no lane has been acquired yet. Inspecting
		// locks only after a SQLSTATE abort could not establish that order.
		probeLifecycleScopeRowLock(t, ctx, pool, orgID, sessionID, nil, true)
		for _, lane := range lanes {
			probeLifecycleScopeRowLock(t, ctx, pool, orgID, sessionID, &lane, false)
		}
	}
	before := lifecycleScopeConcurrencyGraph(t, ctx, tx, orgID, sessionID)
	locked, err := lockLifecycleScopeInTx(ctx, wrapper, orgID, lifecycleScope{sessionID: sessionID, threadIDs: lanes[:2]})
	require.NoError(t, err, "the unchanged production lane SQL must progress after the physical-order probe releases its test locks")
	require.Equal(t, sessionID, locked.session.id, "the successful observation must describe only the requested parent")
	require.Equal(t, 1, wrapper.laneQueries, "all explicit lanes must use one production batch query after the parent lock")
	requireLifecycleScopeNoSettlement(t, wrapper)
	require.NoError(t, tx.Commit(ctx), "only the caller may commit its successfully acquired scope")
	require.Equal(t, before, lifecycleScopeConcurrencyGraph(t, ctx, pool, orgID, sessionID), "committing lock-only observations must not mutate any parent or lane field")
}

func TestLifecycleScopeNowaitContentionRollsBackWholeOwnerPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		parent      bool
		laneQueries int
	}{
		{name: "blocked parent never attempts the lane batch", parent: true},
		{name: "later sorted lane contention discards partial observations", laneQueries: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID, lanes := newLifecycleScopeConcurrencyFixture(t)
			setupCtx, stopSetup := context.WithTimeout(context.Background(), 10*time.Second)
			defer stopSetup()
			blocker := beginSessionLifecycleProbe(t, setupCtx, pool)
			if tt.parent {
				_, err := blocker.Exec(setupCtx, `SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE`, orgID, sessionID)
				require.NoError(t, err, "hold the exact parent before the kernel starts")
			} else {
				_, err := blocker.Exec(setupCtx, `SELECT id FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR NO KEY UPDATE`, orgID, sessionID, lanes[1])
				require.NoError(t, err, "hold the later sorted selected lane before the kernel starts")
			}
			before := lifecycleScopeConcurrencyGraph(t, setupCtx, pool, orgID, sessionID)
			ctx, cancel := context.WithTimeout(setupCtx, LifecycleTransactionTimeout)
			defer cancel()
			tx := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "the owner must establish the bounded READ COMMITTED protocol first")
			_, err := tx.Exec(ctx, `INSERT INTO lifecycle_scope_effect_probe(org_id,attempt) VALUES($1,1)`, orgID)
			require.NoError(t, err, "stage a test-only outer-unit effect before calling the lock-only kernel")
			wrapper := &lifecycleScopeConcurrencyTx{Tx: tx}
			locked, err := lockLifecycleScopeInTx(ctx, wrapper, orgID, lifecycleScope{sessionID: sessionID, threadIDs: []uuid.UUID{lanes[1], lanes[0]}})
			requireSessionLifecycleSQLState(t, err, "55P03", "real row contention must surface as NOWAIT lock-not-available")
			require.Equal(t, lockedLifecycleScope{}, locked, "parent or later lane SQL failure must return no partial locked observation")
			require.Equal(t, tt.laneQueries, wrapper.laneQueries, "a failed parent must prevent the lane statement from being issued at all")
			requireLifecycleScopeNoSettlement(t, wrapper)
			require.Equal(t, byte('E'), tx.Conn().PgConn().TxStatus(), "SQL failure must leave the real outer transaction aborted until its owner rolls it back")
			// SQLSTATE abort can itself release previously acquired row locks.
			// No physical acquisition order is inferred from probes after this error.
			require.NoError(t, tx.Rollback(setupCtx), "the caller must confirm whole-unit rollback after any acquisition failure")
			require.NoError(t, blocker.Rollback(setupCtx), "release only the test's exact contention blocker")
			requireLifecycleScopeEffects(t, setupCtx, pool, orgID, []int{})
			require.Equal(t, before, lifecycleScopeConcurrencyGraph(t, setupCtx, pool, orgID, sessionID), "aborted acquisition must leave all original parent and lane data unchanged")
		})
	}
}

func TestLifecycleScopeSelectedRowsAndStableOrderPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		sessionOnly bool
		duplicates  bool
	}{
		{name: "empty explicit lanes locks only the session", sessionOnly: true},
		{name: "explicit selection leaves other lanes unlocked"},
		{name: "reversed duplicate selection uses the same stable batch", duplicates: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID, lanes := newLifecycleScopeConcurrencyFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			tx := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "prepare an existing bounded transaction without transferring ownership")
			selection := slices.Clone(lanes[:2])
			if tt.sessionOnly {
				selection = nil
			} else if tt.duplicates {
				selection = []uuid.UUID{lanes[1], lanes[0], lanes[1], lanes[0]}
			}
			original := slices.Clone(selection)
			wrapper := &lifecycleScopeConcurrencyTx{Tx: tx}
			locked, err := lockLifecycleScopeInTx(ctx, wrapper, orgID, lifecycleScope{sessionID: sessionID, threadIDs: selection})
			require.NoError(t, err, "the kernel must lock exactly the selected tenant parent and lanes")
			require.Equal(t, original, selection, "normalization must not mutate the caller's reversed or duplicate input slice")
			wantIDs := []uuid.UUID{}
			if !tt.sessionOnly {
				wantIDs = slices.Clone(lanes[:2])
			}
			actualIDs := make([]uuid.UUID, 0, len(locked.threads))
			for _, lane := range locked.threads {
				actualIDs = append(actualIDs, lane.id)
			}
			require.Equal(t, wantIDs, actualIDs, "locked lane observations must have one row per exact selected ID in stable UUID order")
			if tt.sessionOnly {
				require.Equal(t, 0, wrapper.laneQueries, "session-only scope must not discover a primary lane or issue any lane query")
			} else {
				require.Equal(t, 1, wrapper.laneQueries, "all selected lanes must share one batch query")
				require.Equal(t, wantIDs, wrapper.laneIDs, "the physical batch query must receive sorted deduplicated IDs rather than rely only on output sorting")
			}
			probeLifecycleScopeRowLock(t, ctx, pool, orgID, sessionID, nil, true)
			for index, lane := range lanes {
				probeLifecycleScopeRowLock(t, ctx, pool, orgID, sessionID, &lane, !tt.sessionOnly && index < 2)
			}
			if tt.duplicates {
				again, secondErr := lockLifecycleScopeInTx(ctx, tx, orgID, lifecycleScope{sessionID: sessionID, threadIDs: lanes[:2]})
				require.NoError(t, secondErr, "canonical and reversed duplicate requests must compose in the same caller transaction")
				require.Equal(t, locked, again, "normalization must produce the same complete observations as canonical selection")
			}
			requireLifecycleScopeNoSettlement(t, wrapper)
			require.NoError(t, tx.Rollback(ctx), "only the caller may release its successful scope locks")
		})
	}
}

func TestLifecycleScopeOwnerRetainsUnsettledErrorsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		missing bool
	}{
		{name: "caller application rejection after successful acquisition"},
		{name: "missing lane rejects observations without settling the caller", missing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID, lanes := newLifecycleScopeConcurrencyFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			tx := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "configure the outer transaction before staging its own application work")
			_, err := tx.Exec(ctx, `INSERT INTO lifecycle_scope_effect_probe(org_id,attempt) VALUES($1,1)`, orgID)
			require.NoError(t, err, "stage an outer-owner effect that must never auto-commit")
			selection := slices.Clone(lanes[:2])
			if tt.missing {
				selection = append(selection, uuid.New())
			}
			wrapper := &lifecycleScopeConcurrencyTx{Tx: tx}
			locked, err := lockLifecycleScopeInTx(ctx, wrapper, orgID, lifecycleScope{sessionID: sessionID, threadIDs: selection})
			if tt.missing {
				require.ErrorIs(t, err, pgx.ErrNoRows, "a missing explicit lane must fail the complete scope rather than return a successful subset")
				require.Equal(t, lockedLifecycleScope{}, locked, "missing membership must discard every parent and selected-lane observation")
			} else {
				require.NoError(t, err, "lock-only acquisition cannot itself decide the caller's later application policy")
				applicationErr := errors.New("synthetic application policy rejected")
				// A synthetic owner requires running status; the real fixture is
				// idle. The kernel must still return its raw observation, leaving
				// this later application decision and rollback to the owner.
				ownerOutcome := func() error {
					if locked.session.status != models.SessionStatusRunning {
						return applicationErr
					}
					return nil
				}()
				require.ErrorIs(t, ownerOutcome, applicationErr, "the caller remains responsible for rejecting and rolling back its own application unit")
			}
			requireLifecycleScopeNoSettlement(t, wrapper)
			require.Equal(t, byte('T'), tx.Conn().PgConn().TxStatus(), "application and missing-membership outcomes must leave the caller transaction open rather than committed or rolled back")
			var staged int
			require.NoError(t, tx.QueryRow(ctx, `SELECT attempt FROM lifecycle_scope_effect_probe WHERE org_id=$1`, orgID).Scan(&staged), "the caller must still be able to inspect its own staged work after deciding to reject")
			require.Equal(t, 1, staged, "the kernel must neither discard nor settle unrelated outer-owner work")
			requireLifecycleScopeEffects(t, ctx, pool, orgID, []int{})
			probeLifecycleScopeRowLock(t, ctx, pool, orgID, sessionID, nil, true)
			require.NoError(t, tx.Rollback(ctx), "the caller must roll back the complete unsuccessful application unit")
			requireLifecycleScopeEffects(t, ctx, pool, orgID, []int{})
			probeLifecycleScopeRowLock(t, ctx, pool, orgID, sessionID, nil, false)
		})
	}
}

func TestLifecycleScopeRunnerRetriesAfterConfirmedAbortPostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, sessionID, lanes := newLifecycleScopeConcurrencyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	blocker := beginSessionLifecycleProbe(t, ctx, pool)
	_, err := blocker.Exec(ctx, `SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE`, orgID, sessionID)
	require.NoError(t, err, "hold the parent until the runner reaches its fresh second transaction")
	before := lifecycleScopeConcurrencyGraph(t, ctx, pool, orgID, sessionID)
	retryReady := make(chan struct{}, 1)
	releaseRetry := make(chan struct{})
	result := make(chan error, 1)
	finished := make(chan struct{})
	var attempts int
	var transactionIDs []int64
	var firstConflict error
	var locked lockedLifecycleScope
	go func() {
		defer close(finished)
		result <- RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
			attempts++
			locked = lockedLifecycleScope{} // Never retain an aborted attempt's observations.
			var transactionID int64
			if queryErr := tx.QueryRow(workCtx, `SELECT txid_current()`).Scan(&transactionID); queryErr != nil {
				return queryErr
			}
			transactionIDs = append(transactionIDs, transactionID)
			if attempts == 2 {
				// Entering a fresh transaction follows the existing runner's
				// confirmed rollback. Independently prove the first attempt did
				// not commit its probe row before releasing the contention barrier.
				var count int
				if queryErr := tx.QueryRow(workCtx, `SELECT count(*) FROM lifecycle_scope_effect_probe WHERE org_id=$1`, orgID).Scan(&count); queryErr != nil {
					return queryErr
				}
				if count != 0 {
					return errors.New("aborted first scope attempt left a committed probe effect")
				}
				retryReady <- struct{}{}
				select {
				case <-releaseRetry:
				case <-workCtx.Done():
					return workCtx.Err()
				}
			}
			if _, queryErr := tx.Exec(workCtx, `INSERT INTO lifecycle_scope_effect_probe(org_id,attempt) VALUES($1,$2)`, orgID, attempts); queryErr != nil {
				return queryErr
			}
			var lockErr error
			locked, lockErr = lockLifecycleScopeInTx(workCtx, tx, orgID, lifecycleScope{sessionID: sessionID, threadIDs: []uuid.UUID{lanes[1], lanes[0], lanes[1]}})
			if attempts == 1 {
				firstConflict = lockErr
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
	waitForResult(t, ctx, retryReady)
	require.NoError(t, blocker.Rollback(ctx), "release the blocker only after the runner has confirmed abort and the new transaction has proved no committed first-attempt effect")
	close(releaseRetry)
	require.NoError(t, waitForResult(t, ctx, result), "one fresh whole-unit retry must commit after the deterministic contention barrier is released")
	waitForResult(t, ctx, finished)
	requireSessionLifecycleSQLState(t, firstConflict, "55P03", "the first callback must fail on actual production-kernel NOWAIT acquisition")
	require.Equal(t, 2, attempts, "the runner must invoke exactly one aborted callback and one fresh successful callback")
	require.Equal(t, 2, len(transactionIDs), "each whole callback must use a real PostgreSQL transaction")
	require.NotEqual(t, transactionIDs[0], transactionIDs[1], "retry must not reuse an aborted outer transaction or a nested savepoint")
	require.Equal(t, sessionID, locked.session.id, "only the final committed callback's complete observation may remain captured")
	requireLifecycleScopeEffects(t, ctx, pool, orgID, []int{2})
	require.Equal(t, before, lifecycleScopeConcurrencyGraph(t, ctx, pool, orgID, sessionID), "the lock kernel and contention retries must not write lifecycle rows")
}

func newLifecycleScopeConcurrencyFixture(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID, []uuid.UUID) {
	t.Helper()
	pool := newFeedbackLifecyclePool(t)
	orgID, sessionID, primaryID := seedRuntimeSeedThread(t, pool)
	lanes := []uuid.UUID{primaryID}
	// All fixture construction precedes the two-second caller budget. Separate
	// real stores create visible lanes; this does not duplicate proposed lock SQL.
	for range 2 {
		thread := &models.SessionThread{OrgID: orgID, SessionID: sessionID, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic scope lane", Status: models.ThreadStatusIdle}
		require.NoError(t, NewSessionThreadStore(pool).Create(context.Background(), thread, 10), "create an isolated visible lane through the unchanged real store")
		lanes = append(lanes, thread.ID)
	}
	slices.SortFunc(lanes, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	_, err := pool.Exec(context.Background(), `CREATE TABLE lifecycle_scope_effect_probe(org_id uuid NOT NULL,attempt integer NOT NULL,PRIMARY KEY(org_id,attempt))`)
	require.NoError(t, err, "create only a synthetic outer-unit effect probe in the owned disposable database")
	return pool, orgID, sessionID, lanes
}

func probeLifecycleScopeRowLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID, sessionID uuid.UUID, laneID *uuid.UUID, blocked bool) {
	t.Helper()
	probe := beginSessionLifecycleProbe(t, ctx, pool)
	var id uuid.UUID
	var err error
	if laneID == nil {
		err = probe.QueryRow(ctx, `SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE NOWAIT`, orgID, sessionID).Scan(&id)
	} else {
		err = probe.QueryRow(ctx, `SELECT id FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR NO KEY UPDATE NOWAIT`, orgID, sessionID, *laneID).Scan(&id)
	}
	if blocked {
		requireSessionLifecycleSQLState(t, err, "55P03", "the other connection must observe this exact row as physically locked")
	} else {
		require.NoError(t, err, "an unselected or not-yet-acquired row must remain physically lockable")
		want := sessionID
		if laneID != nil {
			want = *laneID
		}
		require.Equal(t, want, id, "the successful probe must lock the exact intended row rather than an empty result")
	}
	require.NoError(t, probe.Rollback(ctx), "release the probe's own lock or aborted transaction before the production query continues")
}

func lifecycleScopeConcurrencyGraph(t *testing.T, ctx context.Context, database DBTX, orgID, sessionID uuid.UUID) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, database.QueryRow(ctx, `SELECT jsonb_build_object('session',(SELECT to_jsonb(s) FROM sessions s WHERE s.org_id=$1 AND s.id=$2),'threads',(SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2))`, orgID, sessionID).Scan(&raw), "capture every scoped parent and lane field independently of the lock observations")
	return raw
}

func requireLifecycleScopeEffects(t *testing.T, ctx context.Context, database DBTX, orgID uuid.UUID, expected []int) {
	t.Helper()
	rows, err := database.Query(ctx, `SELECT attempt FROM lifecycle_scope_effect_probe WHERE org_id=$1 ORDER BY attempt`, orgID)
	require.NoError(t, err, "inspect the complete durable outer-unit effect set for this synthetic tenant")
	actual, err := pgx.CollectRows(rows, pgx.RowTo[int])
	require.NoError(t, err, "collect all committed outer-unit effects")
	require.Equal(t, expected, actual, "only the final committed outer unit may leave any durable probe effect")
}

type lifecycleScopeConcurrencyTx struct {
	pgx.Tx
	laneQueries                            int
	laneIDs                                []uuid.UUID
	beforeLaneQuery                        func()
	beginCalls, commitCalls, rollbackCalls int
}

func (tx *lifecycleScopeConcurrencyTx) Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error) {
	if sql == lifecycleThreadScopeLockQuery {
		tx.laneQueries++
		if len(arguments) == 3 {
			if ids, ok := arguments[2].([]uuid.UUID); ok {
				tx.laneIDs = slices.Clone(ids)
			}
		}
		if tx.beforeLaneQuery != nil {
			tx.beforeLaneQuery()
		}
	}
	return tx.Tx.Query(ctx, sql, arguments...)
}

func (tx *lifecycleScopeConcurrencyTx) Begin(context.Context) (pgx.Tx, error) {
	tx.beginCalls++
	return nil, errors.New("scope kernel must not create a nested test transaction")
}

func (tx *lifecycleScopeConcurrencyTx) Commit(context.Context) error {
	tx.commitCalls++
	return errors.New("scope kernel must not commit the caller's transaction")
}

func (tx *lifecycleScopeConcurrencyTx) Rollback(context.Context) error {
	tx.rollbackCalls++
	return errors.New("scope kernel must not roll back the caller's transaction")
}

func requireLifecycleScopeNoSettlement(t *testing.T, tx *lifecycleScopeConcurrencyTx) {
	t.Helper()
	require.Equal(t, []int{0, 0, 0}, []int{tx.beginCalls, tx.commitCalls, tx.rollbackCalls}, "the lock-only kernel must not begin, settle, retry or transfer transaction ownership")
}
