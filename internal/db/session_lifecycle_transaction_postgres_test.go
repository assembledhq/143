package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These tests exercise the production transaction runner, not a proposed SQL
// classifier. The small isolated fixture proves transaction mechanics only;
// the separate full-schema case covers real SessionStore composition.
func TestLifecycleTransactionPostgresSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		isolation    pgx.TxIsoLevel
		absentMarker bool
	}{
		{name: "read committed default", isolation: pgx.ReadCommitted},
		{name: "repeatable read default", isolation: pgx.RepeatableRead},
		{name: "serializable default", isolation: pgx.Serializable},
		{name: "marker initially absent", isolation: pgx.ReadCommitted, absentMarker: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, _, _ := newSessionLifecycleLockProbe(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			config := fixture.Config()
			config.MaxConns = 1
			config.MinConns = 0
			config.ConnConfig.RuntimeParams["default_transaction_isolation"] = string(tt.isolation)
			config.ConnConfig.RuntimeParams["lifecycle143.writer_version"] = "0"
			if tt.absentMarker {
				delete(config.ConnConfig.RuntimeParams, "lifecycle143.writer_version")
			}
			config.ConnConfig.RuntimeParams["lock_timeout"] = "0"
			config.ConnConfig.RuntimeParams["statement_timeout"] = "0"
			pool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err, "open a single-connection pool with deliberately different session defaults")
			t.Cleanup(pool.Close)
			beforePID := backendPID(t, ctx, pool)
			var beforeMarker *string
			err = pool.QueryRow(ctx, "SELECT current_setting('lifecycle143.writer_version', true)").Scan(&beforeMarker)
			require.NoError(t, err, "inspect the marker before this physical connection has configured a lifecycle transaction")
			var expectedBeforeMarker *string
			if !tt.absentMarker {
				value := "0"
				expectedBeforeMarker = &value
			}
			require.Equal(t, expectedBeforeMarker, beforeMarker, "a connection without a startup marker should initially report SQL NULL rather than a fabricated default")
			var duringPID int32
			var actual []string
			err = RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
				duringPID = backendPID(t, workCtx, tx)
				return tx.QueryRow(workCtx, "SELECT ARRAY[current_setting('transaction_isolation'),current_setting('lifecycle143.writer_version'),current_setting('lock_timeout'),current_setting('statement_timeout')]").Scan(&actual)
			})
			require.NoError(t, err, "the top-level runner should override connection defaults for its own transaction")
			require.Equal(t, []string{"read committed", "1", "250ms", "1500ms"}, actual, "work should observe every configured protocol setting before its first mutation")
			var after []string
			err = pool.QueryRow(ctx, "SELECT ARRAY[current_setting('transaction_isolation'),current_setting('lifecycle143.writer_version'),current_setting('lock_timeout'),current_setting('statement_timeout')]").Scan(&after)
			require.NoError(t, err, "inspect settings on the next borrower of the same physical connection")
			expectedAfterMarker := "0"
			if tt.absentMarker {
				// PostgreSQL retains an empty custom-GUC placeholder after SET
				// LOCAL. NULL and empty both mean unset, never writer version 1.
				expectedAfterMarker = ""
			}
			require.Equal(t, []string{string(tt.isolation), expectedAfterMarker, "0", "0"}, after, "transaction-local protocol settings must not leak into the pooled session, including PostgreSQL's empty custom-GUC placeholder")
			require.Equal(t, []int32{beforePID, beforePID}, []int32{duringPID, backendPID(t, ctx, pool)}, "the locality check must reuse one physical connection rather than compare unrelated sessions")
		})
	}
}

func TestConfigureLifecycleTransactionPostgresIsolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		isolation pgx.TxIsoLevel
		accepted  bool
	}{
		{name: "read committed", isolation: pgx.ReadCommitted, accepted: true},
		{name: "repeatable read", isolation: pgx.RepeatableRead},
		{name: "serializable", isolation: pgx.Serializable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, _, _, _ := newSessionLifecycleLockProbe(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: tt.isolation})
			require.NoError(t, err, "the caller should own its explicitly selected transaction")
			defer func() { _ = tx.Rollback(context.Background()) }()
			err = ConfigureLifecycleTransactionInTx(ctx, tx)
			if tt.accepted {
				require.NoError(t, err, "the supplied transaction should accept the supported isolation protocol")
				var version string
				require.NoError(t, tx.QueryRow(ctx, "SELECT current_setting('lifecycle143.writer_version')").Scan(&version), "the configurator must leave the caller transaction open")
				require.Equal(t, "1", version, "the compatibility marker should be installed without committing the caller")
			} else {
				require.ErrorContains(t, err, "READ COMMITTED", "a stale-snapshot isolation mode cannot participate in the future activation interlock")
				var actual string
				require.NoError(t, tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&actual), "rejection should not close or secretly replace the caller transaction")
				require.Equal(t, string(tt.isolation), actual, "configuration must not silently downgrade a supplied transaction")
			}
			require.NoError(t, tx.Rollback(ctx), "only the caller may settle the supplied transaction")
		})
	}
}

func TestLifecycleTransactionPostgresEffects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		reject   bool
		expected []string
	}{
		{name: "commit", expected: []string{"staged"}},
		{name: "application rejection", reject: true, expected: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, _, _ := newSessionLifecycleLockProbe(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			rejected := errors.New("reject staged lifecycle operation")
			err := RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
				if _, err := tx.Exec(workCtx, "INSERT INTO probe_effects(org_id,label) VALUES ($1,'staged')", orgID); err != nil {
					return err
				}
				if tt.reject {
					return rejected
				}
				return nil
			})
			if tt.reject {
				require.ErrorIs(t, err, rejected, "ordinary rejection must retain its cause and abort all earlier database writes")
			} else {
				require.NoError(t, err, "successful work should become durable only after the outer commit")
			}
			rows, err := pool.Query(ctx, "SELECT label FROM probe_effects WHERE org_id=$1 ORDER BY label", orgID)
			require.NoError(t, err, "inspect this tenant's durable effects after runner settlement")
			actual, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err, "collect all durable effects rather than count partial results")
			require.Equal(t, tt.expected, actual, "an aborted callback must publish none of its staged database changes")
		})
	}
}

func TestLifecycleTransactionPostgresPreservesExistingTransaction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		commit   bool
		expected []string
	}{
		{name: "owner rolls back", expected: []string{}},
		{name: "owner commits", commit: true, expected: []string{"outer"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture, orgID, _, _ := newSessionLifecycleLockProbe(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			config := fixture.Config()
			config.MaxConns = 1
			config.MinConns = 0
			pool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err, "open an independently owned single-connection pool")
			t.Cleanup(pool.Close)
			outer, err := pool.Begin(ctx)
			require.NoError(t, err, "the caller should exclusively own the pool's existing transaction")
			defer func() { _ = outer.Rollback(context.Background()) }()
			_, err = outer.Exec(ctx, "INSERT INTO probe_effects(org_id,label) VALUES ($1,'outer')", orgID)
			require.NoError(t, err, "stage caller-owned work before the new runner tries to acquire another connection")
			workCtx, workCancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer workCancel()
			called := false
			err = RunLifecycleTransaction(workCtx, pool, func(context.Context, pgx.Tx) error {
				called = true
				return nil
			})
			require.ErrorIs(t, err, context.DeadlineExceeded, "pool acquisition must time out instead of borrowing and settling the caller's active transaction")
			require.False(t, called, "work cannot enter without an independently acquired transaction")
			var inside []string
			require.NoError(t, outer.QueryRow(ctx, "SELECT ARRAY_AGG(label ORDER BY label) FROM probe_effects WHERE org_id=$1", orgID).Scan(&inside), "the caller's transaction must remain open and usable after runner rejection")
			require.Equal(t, []string{"outer"}, inside, "the rejected runner must leave the caller's staged writes untouched")
			if tt.commit {
				require.NoError(t, outer.Commit(ctx), "the caller alone should decide to commit its transaction")
			} else {
				require.NoError(t, outer.Rollback(ctx), "the caller alone should decide to roll back its transaction")
			}
			rows, err := pool.Query(ctx, "SELECT label FROM probe_effects WHERE org_id=$1 ORDER BY label", orgID)
			require.NoError(t, err, "inspect persistence only after the caller has settled its own transaction")
			actual, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err, "collect the caller's complete durable effect set")
			require.Equal(t, tt.expected, actual, "runner rejection must not change the caller's chosen settlement")
		})
	}
}

func TestLifecycleTransactionPostgresRetryAfterConflict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		implicit bool
	}{
		{name: "explicit NOWAIT"},
		{name: "implicit foreign key", implicit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, sessionID, childID := newSessionLifecycleLockProbe(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			holder := beginSessionLifecycleProbe(t, ctx, pool)
			_, err := holder.Exec(ctx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", orgID, sessionID)
			require.NoError(t, err, "hold the exact parent until the retry has entered a fresh transaction")
			retryReady := make(chan struct{}, 1)
			releaseRetry := make(chan struct{})
			result := make(chan error, 1)
			var attempts int
			var transactionIDs []int64
			var firstConflict error
			go func() {
				result <- RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
					attempts++
					var transactionID int64
					if err := tx.QueryRow(workCtx, "SELECT txid_current()").Scan(&transactionID); err != nil {
						return err
					}
					transactionIDs = append(transactionIDs, transactionID)
					if _, err := tx.Exec(workCtx, "INSERT INTO probe_effects(org_id,label) VALUES ($1,$2)", orgID, fmt.Sprintf("attempt-%d", attempts)); err != nil {
						return err
					}
					if attempts == 2 {
						retryReady <- struct{}{}
						select {
						case <-releaseRetry:
						case <-workCtx.Done():
							return workCtx.Err()
						}
					}
					var err error
					if tt.implicit {
						_, err = tx.Exec(workCtx, "INSERT INTO session_human_input_requests(id,org_id,session_id) VALUES ($1,$2,$3)", childID, orgID, sessionID)
					} else {
						_, err = tx.Exec(workCtx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE NOWAIT", orgID, sessionID)
					}
					if attempts == 1 {
						firstConflict = err
					}
					return err
				})
			}()
			waitForResult(t, ctx, retryReady)
			require.NoError(t, holder.Rollback(ctx), "release the parent only after the runner has aborted and opened its second transaction")
			close(releaseRetry)
			require.NoError(t, waitForResult(t, ctx, result), "a bounded fresh attempt should progress after the observed conflict is released")
			requireSessionLifecycleSQLState(t, firstConflict, "55P03", "both NOWAIT and implicit FK waits should produce classified contention")
			require.Equal(t, 2, attempts, "one conflicting attempt and one fresh successful callback should run")
			require.Len(t, transactionIDs, 2, "each complete callback invocation should have one actual PostgreSQL transaction")
			require.NotEqual(t, transactionIDs[0], transactionIDs[1], "retry must not reuse an aborted transaction or nested savepoint")
			rows, err := pool.Query(ctx, "SELECT label FROM probe_effects WHERE org_id=$1 ORDER BY label", orgID)
			require.NoError(t, err, "inspect staged writes after the retry commits")
			actual, err := pgx.CollectRows(rows, pgx.RowTo[string])
			require.NoError(t, err, "collect the complete retry effect set")
			require.Equal(t, []string{"attempt-2"}, actual, "the aborted first attempt must leave no durable write")
		})
	}
}

func TestLifecycleTransactionPostgresRetryAfterDeadlock(t *testing.T) {
	t.Parallel()
	pool, orgID, firstID, _ := newSessionLifecycleLockProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	secondID := uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO sessions(id,org_id) VALUES ($1,$2)", secondID, orgID)
	require.NoError(t, err, "seed a second row so two independent transactions can form a real row-lock cycle")
	competitor := beginSessionLifecycleProbe(t, ctx, pool)
	_, err = competitor.Exec(ctx, "SET LOCAL deadlock_timeout = '5s'")
	var privilegeErr *pgconn.PgError
	if errors.As(err, &privilegeErr) && privilegeErr.Code == "42501" {
		t.Skip("the actual-deadlock victim probe requires permission to SET deadlock_timeout")
	}
	require.NoError(t, err, "the competitor must not detect the cycle before the runner's deliberately shorter deadlock timer")
	_, err = competitor.Exec(ctx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", orgID, secondID)
	require.NoError(t, err, "the competitor should hold the second row before the runner locks the first")
	competitorPID := backendPID(t, ctx, competitor)
	observer, err := pool.Acquire(ctx)
	require.NoError(t, err, "reserve an independent connection to observe the actual server-side wait graph")
	t.Cleanup(observer.Release)

	runnerReady := make(chan int32, 1)
	createCycle := make(chan struct{})
	retryReady := make(chan struct{}, 1)
	releaseRetry := make(chan struct{})
	result := make(chan error, 1)
	runnerFinished := make(chan struct{})
	var attempts int
	var transactionIDs []int64
	var firstConflict error
	go func() {
		defer close(runnerFinished)
		result <- RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
			attempts++
			var transactionID int64
			if err := tx.QueryRow(workCtx, "SELECT txid_current()").Scan(&transactionID); err != nil {
				return err
			}
			transactionIDs = append(transactionIDs, transactionID)
			if attempts == 1 {
				// Detect this real cycle before the runner's normal 250ms lock
				// timeout; the competitor's detector remains at five seconds.
				if _, err := tx.Exec(workCtx, "SET LOCAL deadlock_timeout = '25ms'"); err != nil {
					return err
				}
			} else {
				retryReady <- struct{}{}
				select {
				case <-releaseRetry:
				case <-workCtx.Done():
					return workCtx.Err()
				}
			}
			if _, err := tx.Exec(workCtx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", orgID, firstID); err != nil {
				return err
			}
			if _, err := tx.Exec(workCtx, "INSERT INTO probe_effects(org_id,label) VALUES ($1,$2)", orgID, fmt.Sprintf("attempt-%d", attempts)); err != nil {
				return err
			}
			if attempts == 1 {
				var runnerPID int32
				if err := tx.QueryRow(workCtx, "SELECT pg_backend_pid()").Scan(&runnerPID); err != nil {
					return err
				}
				runnerReady <- runnerPID
				select {
				case <-createCycle:
				case <-workCtx.Done():
					return workCtx.Err()
				}
			}
			_, err := tx.Exec(workCtx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", orgID, secondID)
			if attempts == 1 {
				firstConflict = err
			}
			return err
		})
	}()
	defer func() {
		cancel()
		<-runnerFinished
	}()
	runnerPID := waitForResult(t, ctx, runnerReady)
	competitorDone := make(chan error, 1)
	competitorFinished := make(chan struct{})
	go func() {
		defer close(competitorFinished)
		_, lockErr := competitor.Exec(ctx, "SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE", orgID, firstID)
		if lockErr == nil {
			lockErr = competitor.Commit(ctx)
		}
		competitorDone <- lockErr
	}()
	// Function defers cancel and join both workers before fixture cleanup can
	// roll back their transactions or release shared connections on t.Fatal.
	defer func() {
		cancel()
		<-competitorFinished
	}()
	// Only complete the second edge after PostgreSQL reports the first one.
	// An elapsed sleep cannot establish either edge of an actual deadlock.
	require.Eventually(t, func() bool {
		var blocked bool
		err := observer.QueryRow(ctx, "SELECT $2::integer = ANY(pg_blocking_pids($1))", competitorPID, runnerPID).Scan(&blocked)
		require.NoError(t, err, "observe the competitor waiting on this runner through an independent server connection")
		return blocked
	}, time.Second, 5*time.Millisecond, "the server must report the competitor blocked on the runner before the runner requests the second row")
	close(createCycle)
	waitForResult(t, ctx, retryReady)
	require.NoError(t, waitForResult(t, ctx, competitorDone), "the deadlock victim's abort should release the first row and let the competitor settle")
	rows, err := observer.Query(ctx, "SELECT label FROM probe_effects WHERE org_id=$1 ORDER BY label", orgID)
	require.NoError(t, err, "inspect durable effects after the first rollback but before releasing the fresh retry")
	beforeRetry, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect the entire effect set at the confirmed-abort barrier")
	require.Equal(t, []string{}, beforeRetry, "the deadlock victim must leave no earlier staged write after abort")
	close(releaseRetry)
	require.NoError(t, waitForResult(t, ctx, result), "a fresh whole-transaction retry should commit after the observed deadlock clears")
	requireSessionLifecycleSQLState(t, firstConflict, "40P01", "the first callback must be PostgreSQL's real deadlock victim rather than a synthetic error or lock timeout")
	require.Equal(t, 2, attempts, "one confirmed deadlock abort should permit exactly one successful fresh retry")
	require.Len(t, transactionIDs, 2, "the deadlock scenario should execute exactly two transaction callbacks")
	require.NotEqual(t, transactionIDs[0], transactionIDs[1], "deadlock retry must acquire a new top-level PostgreSQL transaction")
	rows, err = observer.Query(ctx, "SELECT label FROM probe_effects WHERE org_id=$1 ORDER BY label", orgID)
	require.NoError(t, err, "inspect the complete durable effect set after the fresh retry commits")
	afterRetry, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect the durable effects after deadlock recovery")
	require.Equal(t, []string{"attempt-2"}, afterRetry, "only the one successful fresh attempt should publish a durable write")
}

func TestLifecycleTransactionPostgresGoexitCleanup(t *testing.T) {
	t.Parallel()
	fixture, orgID, _, _ := newSessionLifecycleLockProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	config := fixture.Config()
	config.MaxConns = 1
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "open a one-connection pool so a leaked Goexit transaction cannot hide behind another connection")
	t.Cleanup(pool.Close)
	exited := make(chan struct{})
	returned := make(chan error, 1)
	staged := make(chan struct{}, 1)
	callbackTransaction := make(chan pgx.Tx, 1)
	go func() {
		defer close(exited)
		err := RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
			callbackTransaction <- tx
			if _, err := tx.Exec(workCtx, "INSERT INTO probe_effects(org_id,label) VALUES ($1,'before-goexit')", orgID); err != nil {
				return err
			}
			staged <- struct{}{}
			runtime.Goexit()
			return errors.New("Goexit unexpectedly returned")
		})
		returned <- err
	}()
	waitForResult(t, ctx, exited)
	select {
	case tx := <-callbackTransaction:
		// If this regression fails, settle its own captured transaction before
		// pool.Close rather than leaving cleanup hung on the deliberately tiny
		// pool. ErrTxClosed is expected after successful runner cleanup.
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
			defer cleanupCancel()
			rollbackErr := tx.Rollback(cleanupCtx)
			if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				require.NoError(t, rollbackErr, "cleanup should settle only this regression's captured unfinished transaction")
			}
		})
	default:
		t.Fatal("the Goexit regression must enter its independently acquired transaction")
	}
	select {
	case err := <-returned:
		t.Fatalf("the runner must not return normally after callback Goexit; returned %v", err)
	default:
	}
	select {
	case <-staged:
	default:
		t.Fatal("the callback must stage its earlier write before invoking Goexit")
	}
	acquireCtx, acquireCancel := context.WithTimeout(ctx, time.Second)
	defer acquireCancel()
	conn, err := pool.Acquire(acquireCtx)
	require.NoError(t, err, "detached Goexit rollback must release the only pooled connection before bounded reacquisition expires")
	defer conn.Release()
	rows, err := conn.Query(ctx, "SELECT label FROM probe_effects WHERE org_id=$1 ORDER BY label", orgID)
	require.NoError(t, err, "the next borrower should have a usable connection after Goexit cleanup")
	actual, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "collect all durable writes after the callback goroutine exits")
	require.Equal(t, []string{}, actual, "Goexit cleanup must abort the callback's earlier staged write rather than leak or commit it")
}

func TestLifecycleTransactionFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for full-schema lifecycle transaction tests")
	}
	versionCtx, versionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer versionCancel()
	conn, err := pgx.Connect(versionCtx, raw)
	require.NoError(t, err, "connect to the explicit disposable test server for its version prerequisite")
	var serverVersion int
	err = conn.QueryRow(versionCtx, "SELECT current_setting('server_version_num')::integer").Scan(&serverVersion)
	closeErr := conn.Close(versionCtx)
	versionCancel()
	require.NoError(t, err, "read the actual server version before applying the complete unmodified migration chain")
	require.NoError(t, closeErr, "close the version-check connection")
	if serverVersion < 150000 {
		t.Skip("the unmodified production migration chain requires PostgreSQL 15+; isolated transaction tests are not full-schema proof")
	}
	// Reuse the package's complete-migration fixture, including functions bound
	// to public. Do not rewrite production migrations into a synthetic schema.
	pool := newFeedbackLifecyclePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	orgID := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO organizations(id,name,settings) VALUES ($1,'lifecycle-transaction-fixture','{}')", orgID)
	require.NoError(t, err, "seed an isolated tenant on the fully migrated schema")
	session := &models.Session{OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(pool).Create(ctx, session), "create a legacy session through its real store and primary-thread setup")
	rejected := errors.New("reject full-schema mutation")
	err = RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
		if err := NewSessionStore(tx).UpdateStatus(workCtx, orgID, session.ID, models.SessionStatusFailed); err != nil {
			return err
		}
		return rejected
	})
	require.ErrorIs(t, err, rejected, "a real store mutation should remain inside the runner's rollback boundary")
	actual, err := NewSessionStore(pool).GetByID(ctx, orgID, session.ID)
	require.NoError(t, err, "reload the actual session after the aborted store call")
	require.Equal(t, models.SessionStatusIdle, actual.Status, "the callback must not independently commit the store mutation")
	require.NoError(t, RunLifecycleTransaction(ctx, pool, func(workCtx context.Context, tx pgx.Tx) error {
		return NewSessionStore(tx).UpdateStatus(workCtx, orgID, session.ID, models.SessionStatusFailed)
	}), "a real store can compose with the runner without an inner transaction")
	actual, err = NewSessionStore(pool).GetByID(ctx, orgID, session.ID)
	require.NoError(t, err, "reload the actual session after outer commit")
	require.Equal(t, models.SessionStatusFailed, actual.Status, "the outer commit alone should publish the composed store mutation")
}
