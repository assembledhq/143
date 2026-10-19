package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/services/lifecyclecensus"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestLifecycleCensusWritersFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, seed string
		count      int
		truncated  bool
	}{
		{name: "empty observations retain the database clock"},
		{name: "expired rows are excluded by the statement clock", seed: "mixed", count: 1},
		{name: "exact transfer boundary", seed: "bulk", count: 1000},
		{name: "overflow is explicit and bounded", seed: "bulk", count: 1001, truncated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, actor := newContentMigrationPostgres(t, true)
			require.NoError(t, migration.Up(), "apply the entire unchanged migration chain before reading passive writer leases")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if tt.seed == "mixed" {
				_, err := actor.Exec(ctx, `INSERT INTO lifecycle_writer_instances(instance_id,role,build_sha,lifecycle_writer_version,last_heartbeat,expires_at) VALUES ($1,'api','dev',0,statement_timestamp()-interval '1 minute',statement_timestamp()+interval '1 minute'),($2,'worker','synthetic-expired',7,statement_timestamp()-interval '2 minutes',statement_timestamp()-interval '1 minute')`, uuid.New(), uuid.New())
				require.NoError(t, err, "seed a live passive lease and an expired future-version lease without changing the schema")
			} else if tt.seed == "bulk" {
				_, err := actor.Exec(ctx, `INSERT INTO lifecycle_writer_instances(instance_id,role,build_sha,lifecycle_writer_version,last_heartbeat,expires_at) SELECT md5('synthetic-census-writer-'||n::text)::uuid,'api','dev',0,statement_timestamp(),statement_timestamp()+interval '2 minutes' FROM generate_series(1,$1::int) n`, tt.count)
				require.NoError(t, err, "seed the exact boundary or one additional observation in the complete registry schema")
			}
			var before, after time.Time
			require.NoError(t, actor.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&before), "capture a database-clock lower bound independent of the census store")
			got, err := db.NewLifecycleCensusStore(actor).Writers(ctx)
			require.NoError(t, err, "sample passive writer leases from the full migrated schema")
			require.NoError(t, actor.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&after), "capture the database-clock upper bound after collection")
			expected := make([]db.LifecycleCensusWriter, min(tt.count, db.LifecycleCensusLimit))
			for i := range expected {
				expected[i] = db.LifecycleCensusWriter{Role: "api", BuildSHA: "dev", Version: 0}
			}
			require.Equal(t, expected, got.Rows, "return every expected field of only the bounded, unexpired writer observations")
			require.Equal(t, tt.truncated, got.Truncated, "only an actual extra row may mark the sample as truncated")
			require.False(t, got.ObservedAt.Before(before), "the reported observation clock must not precede the database lower bound")
			require.False(t, got.ObservedAt.After(after), "the reported observation clock must not exceed the database upper bound")
		})
	}
}

func TestLifecycleCensusStatementClockFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	migration, actor := newContentMigrationPostgres(t, false)
	require.NoError(t, migration.Up(), "apply all repository migrations before checking the observation clock")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := actor.BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err, "begin an intentionally older transaction to distinguish its clock from the statement clock")
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO lifecycle_writer_instances(instance_id,role,build_sha,last_heartbeat,expires_at) VALUES($1,'api','dev',transaction_timestamp()-interval '1 minute',transaction_timestamp()+interval '100 milliseconds')`, uuid.New())
	require.NoError(t, err, "seed a lease that is live at transaction start but expires before census collection")
	_, err = tx.Exec(ctx, `SELECT pg_sleep(0.15)`)
	require.NoError(t, err, "advance the database statement clock beyond the short synthetic lease")
	got, err := db.NewLifecycleCensusStore(tx).Writers(ctx)
	require.NoError(t, err, "read passive leases within the older transaction")
	require.Equal(t, []db.LifecycleCensusWriter{}, got.Rows, "an expired lease must not remain live because the collector transaction began earlier")
	require.False(t, got.Truncated, "an empty lease observation must not claim a bounded-source overflow")
	var expiredAt time.Time
	require.NoError(t, tx.QueryRow(ctx, `SELECT expires_at FROM lifecycle_writer_instances`).Scan(&expiredAt), "inspect the synthetic expiry independently of the store")
	require.True(t, got.ObservedAt.After(expiredAt), "the census must report the later statement clock rather than the earlier transaction clock")
}

func TestLifecycleCensusConnectionsFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	migration, actor := newContentMigrationPostgres(t, false)
	require.NoError(t, migration.Up(), "apply the full schema before observing PostgreSQL connections")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	selfName := "synthetic-census-self-" + uuid.NewString()
	_, err := actor.Exec(ctx, `SELECT set_config('application_name',$1,false)`, selfName)
	require.NoError(t, err, "identify the collector backend solely for a self-exclusion test")
	apiName := "143:api:" + strings.Repeat("a", 40)
	fixtures := []struct {
		name, application string
		transaction       bool
	}{
		{name: "tagged client transaction", application: apiName, transaction: true},
		{name: "spoofed duplicate label", application: apiName},
		{name: "malformed client label", application: "synthetic-census-malformed"},
		{name: "untagged client", application: ""},
	}
	var transactionStarted time.Time
	for _, fixture := range fixtures {
		config := actor.Config().Copy()
		config.RuntimeParams["application_name"] = fixture.application
		conn := newLifecycleWriterInstanceConnection(t, ctx, config)
		if fixture.transaction {
			_, err := conn.Exec(ctx, `BEGIN; SELECT 1`)
			require.NoError(t, err, fixture.name+" must hold a real open PostgreSQL transaction")
			require.NoError(t, conn.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&transactionStarted), "record the fixture transaction's own start independently of pg_stat_activity")
		}
	}
	otherConfig := actor.Config().Copy()
	otherConfig.Database = "postgres"
	otherConfig.RuntimeParams["application_name"] = "synthetic-census-other-database"
	newLifecycleWriterInstanceConnection(t, ctx, otherConfig)
	got, err := db.NewLifecycleCensusStore(actor).Connections(ctx)
	require.NoError(t, err, "observe client connections independently of passive process leases")
	require.Equal(t, ptrLifecycleCensus(true), got.FullVisibility, "the full-schema administrator must have full PostgreSQL statistics visibility")
	require.False(t, got.Truncated, "a small isolated connection sample must not overflow")
	byName := map[string][]db.LifecycleCensusConnection{}
	for _, row := range got.Rows {
		byName[row.ApplicationName] = append(byName[row.ApplicationName], row)
	}
	require.NotContains(t, byName, selfName, "the census must exclude its own PostgreSQL backend")
	require.NotContains(t, byName, "synthetic-census-other-database", "the census must not observe a connection belonging to another database")
	for _, name := range []string{apiName, "synthetic-census-malformed", ""} {
		want := 1
		if name == apiName {
			want = 2
		}
		// The migration driver also has an untagged client. Its presence is not
		// stable across migration-driver versions, so count that source directly.
		if name == "" {
			require.NoError(t, actor.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend' AND application_name=''`).Scan(&want), "count untagged clients independently without assuming migration-driver pool lifetime")
		}
		require.Len(t, byName[name], want, "tagged, spoofed, malformed and untagged client rows must remain separate observations")
	}
	var open, idle []db.LifecycleCensusConnection
	for _, row := range byName[apiName] {
		if row.XactStart != nil {
			open = append(open, row)
		} else {
			idle = append(idle, row)
		}
	}
	require.Len(t, open, 1, "only the fixture that began a transaction may have a known transaction start")
	require.Len(t, idle, 1, "the spoofed label must not invent an open transaction")
	require.Equal(t, db.LifecycleCensusConnection{ApplicationName: apiName, BackendType: ptrLifecycleCensus("client backend"), State: ptrLifecycleCensus("idle in transaction"), XactStart: &transactionStarted, DetailsVisible: true}, open[0], "visible transaction fields must match the independently observed fixture transaction exactly")
	require.Equal(t, db.LifecycleCensusConnection{ApplicationName: apiName, BackendType: ptrLifecycleCensus("client backend"), State: ptrLifecycleCensus("idle"), DetailsVisible: true}, idle[0], "a spoofable matching label is an idle observation, not an identity assertion")
}

func TestLifecycleCensusVisibilityFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		inherited bool
	}{
		{name: "restricted activity preserves hidden backend fields"},
		{name: "inherited read-all-stats supplies effective visibility", inherited: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, actor := newContentMigrationPostgres(t, false)
			require.NoError(t, migration.Up(), "apply the complete schema before testing real PostgreSQL privilege masking")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			observerRole := newLifecycleCensusRole(t, ctx, actor)
			otherRole := newLifecycleCensusRole(t, ctx, actor)
			if tt.inherited {
				groupRole := newLifecycleCensusRole(t, ctx, actor)
				_, err := actor.Exec(ctx, "GRANT pg_read_all_stats TO "+pgx.Identifier{groupRole}.Sanitize())
				require.NoError(t, err, "grant statistics visibility to a synthetic intermediary role")
				_, err = actor.Exec(ctx, "GRANT "+pgx.Identifier{groupRole}.Sanitize()+" TO "+pgx.Identifier{observerRole}.Sanitize())
				require.NoError(t, err, "inherit statistics visibility instead of checking only direct role membership")
			}
			_, err := actor.Exec(ctx, "GRANT SELECT ON lifecycle_writer_instances TO "+pgx.Identifier{observerRole}.Sanitize())
			require.NoError(t, err, "allow the synthetic observer to read only the passive writer table needed for a standalone collection")
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				_, err := actor.Exec(cleanupCtx, "REVOKE SELECT ON lifecycle_writer_instances FROM "+pgx.Identifier{observerRole}.Sanitize())
				require.NoError(t, err, "remove only the generated observer's table grant before dropping its role")
			})
			observerConfig := actor.Config().Copy()
			observerConfig.User, observerConfig.Password = observerRole, "synthetic-census-only-password"
			observer := newLifecycleWriterInstanceConnection(t, ctx, observerConfig)
			otherConfig := actor.Config().Copy()
			otherConfig.User, otherConfig.Password = otherRole, "synthetic-census-only-password"
			otherConfig.RuntimeParams["application_name"] = "143:worker:dev"
			other := newLifecycleWriterInstanceConnection(t, ctx, otherConfig)
			_, err = other.Exec(ctx, `BEGIN; SELECT 1`)
			require.NoError(t, err, "hold another user's actual transaction while the restricted observer samples activity")
			var transactionStarted time.Time
			require.NoError(t, other.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&transactionStarted), "record the other user's transaction start without relying on census output")
			got, err := db.NewLifecycleCensusStore(observer).Connections(ctx)
			require.NoError(t, err, "collect activity through the effective observer privileges")
			require.Equal(t, ptrLifecycleCensus(tt.inherited), got.FullVisibility, "full visibility must follow effective inherited pg_read_all_stats privileges")
			rows := []db.LifecycleCensusConnection{}
			for _, row := range got.Rows {
				if row.ApplicationName == "143:worker:dev" {
					rows = append(rows, row)
				}
			}
			if !tt.inherited {
				require.Equal(t, []db.LifecycleCensusConnection{{ApplicationName: "143:worker:dev"}}, rows, "another user's masked backend type must not make its connection disappear or turn hidden fields into idle/no-transaction")
			} else {
				require.Equal(t, []db.LifecycleCensusConnection{{ApplicationName: "143:worker:dev", BackendType: ptrLifecycleCensus("client backend"), State: ptrLifecycleCensus("idle in transaction"), XactStart: &transactionStarted, DetailsVisible: true}}, rows, "every visible field must match the independent fixture under inherited privileges")
			}
			observerURL := lifecycleCensusRoleURL(t, observerConfig)
			report, err := lifecyclecensus.Collect(ctx, observerURL)
			require.NoError(t, err, "collect and assemble a standalone report with actual restricted or inherited observer privileges")
			wantVisibility := lifecyclecensus.VisibilityRestricted
			wantGroup := lifecyclecensus.ConnectionGroup{Role: lifecyclecensus.RoleWorker, Build: "dev", State: lifecyclecensus.StateUnknown, Count: 1, UnknownBackendType: 1, UnknownTransactionDetails: 1}
			if tt.inherited {
				wantVisibility = lifecyclecensus.VisibilityFull
				wantGroup = lifecyclecensus.ConnectionGroup{Role: lifecyclecensus.RoleWorker, Build: "dev", State: lifecyclecensus.StateIdleInTransaction, Count: 1, ObservedOpenTransactions: 1, OldestObservedTransactionAt: ptrLifecycleCensus(transactionStarted.UTC())}
			}
			require.Equal(t, wantVisibility, report.Connections.Visibility, "the end-to-end report must retain the real observer's effective statistics visibility")
			groups := []lifecyclecensus.ConnectionGroup{}
			for _, group := range report.Connections.Groups {
				if group.Role == lifecyclecensus.RoleWorker {
					groups = append(groups, group)
				}
			}
			require.Equal(t, []lifecyclecensus.ConnectionGroup{wantGroup}, groups, "the real other-user transaction must remain unknown under restriction and become exactly visible only under inherited statistics privileges")
			encoded, err := json.Marshal(report)
			require.NoError(t, err, "serialize only the safe standalone observation summary")
			for _, excluded := range []string{observerRole, otherRole, observerURL, "synthetic-census-only-password", `"pid"`, `"instance_id"`, `"application_name"`} {
				require.False(t, strings.Contains(string(encoded), excluded), "the end-to-end observation must not disclose role identities, configuration or raw connection identifiers")
			}
		})
	}
}

func TestLifecycleCensusDisabledActivityFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	migration, actor := newContentMigrationPostgres(t, false)
	require.NoError(t, migration.Up(), "apply the entire migration chain before observing disabled activity tracking")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config := actor.Config().Copy()
	config.RuntimeParams["application_name"], config.RuntimeParams["track_activities"] = "143:worker:dev", "off"
	other := newLifecycleWriterInstanceConnection(t, ctx, config)
	_, err := other.Exec(ctx, `BEGIN; SELECT 1`)
	require.NoError(t, err, "open a real transaction whose activity tracking has deliberately been disabled")
	got, err := db.NewLifecycleCensusStore(actor).Connections(ctx)
	require.NoError(t, err, "read a visible backend with disabled activity tracking")
	rows := []db.LifecycleCensusConnection{}
	for _, row := range got.Rows {
		if row.ApplicationName == "143:worker:dev" {
			rows = append(rows, row)
		}
	}
	require.Equal(t, []db.LifecycleCensusConnection{{ApplicationName: "143:worker:dev", BackendType: ptrLifecycleCensus("client backend"), State: ptrLifecycleCensus("disabled"), DetailsVisible: true}}, rows, "full statistics privileges must not fabricate transaction details when activity tracking is disabled")
}

func TestLifecycleCensusReadOnlyFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	migration, actor := newContentMigrationPostgres(t, false)
	require.NoError(t, migration.Up(), "apply the full schema before proving both census queries work in a read-only transaction")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := actor.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	require.NoError(t, err, "open an explicitly database-enforced read-only transaction")
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = db.NewLifecycleCensusStore(tx).Writers(ctx)
	require.NoError(t, err, "the writer census must require no schema or data mutation")
	_, err = db.NewLifecycleCensusStore(tx).Connections(ctx)
	require.NoError(t, err, "the activity census must require no schema, role or data mutation")
	_, err = tx.Exec(ctx, `INSERT INTO lifecycle_writer_instances(instance_id,role,build_sha,last_heartbeat,expires_at) VALUES($1,'api','dev',statement_timestamp(),statement_timestamp()+interval '2 minutes')`, uuid.New())
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "PostgreSQL rather than a fixture must reject a write in this transaction")
	require.Equal(t, "25006", pgErr.Code, "the write must be rejected specifically because the transaction is read-only")
}

func TestLifecycleCensusCancellationFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		manual bool
		cause  error
	}{
		{name: "caller cancellation interrupts a confirmed lock wait", manual: true, cause: context.Canceled},
		{name: "fixed collection budget expires with the lock still held", cause: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, actor := newContentMigrationPostgres(t, false)
			require.NoError(t, migration.Up(), "apply the full schema before testing a blocked standalone census")
			ctx, cancel := context.WithTimeout(context.Background(), 3*lifecyclecensus.Budget)
			defer cancel()
			lockTx, err := actor.Begin(ctx)
			require.NoError(t, err, "hold a real schema-table lock independently of the collector connection")
			defer func() { _ = lockTx.Rollback(ctx) }()
			_, err = lockTx.Exec(ctx, `LOCK TABLE lifecycle_writer_instances IN ACCESS EXCLUSIVE MODE`)
			require.NoError(t, err, "force the writer read to block after the standalone collector begins its transaction")
			observer := newLifecycleWriterInstanceConnection(t, ctx, actor.Config())
			attemptCtx, attemptCancel := context.WithCancel(ctx)
			type outcome struct {
				report lifecyclecensus.Report
				err    error
			}
			completed := make(chan outcome, 1)
			joined := make(chan struct{})
			// Timing bounds allow a cooperative cleanup margin without replacing
			// the collector's real fixed budget or the caller's cancellation.
			cooperativeMargin := lifecyclecensus.Budget / 2
			defer func() {
				attemptCancel()
				select {
				case <-joined:
				case <-time.After(lifecyclecensus.Budget + cooperativeMargin):
					t.Error("the collector goroutine must stop before the test releases its database fixtures")
				}
			}()
			started := time.Now()
			go func() {
				defer close(joined)
				report, err := lifecyclecensus.Collect(attemptCtx, actor.Config().ConnString())
				completed <- outcome{report: report, err: err}
			}()
			blockedCtx, blockedCancel := context.WithTimeout(ctx, cooperativeMargin)
			defer blockedCancel()
			for {
				var blocked bool
				require.NoError(t, observer.QueryRow(blockedCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name='143:lifecycle_census' AND state='active' AND wait_event_type='Lock')`).Scan(&blocked), "prove the standalone collector actually reached its blocked table read before cancellation or budget expiry")
				if blocked {
					break
				}
				select {
				case <-blockedCtx.Done():
					t.Fatal("the standalone collector must reach its real table lock before the cancellation probe proceeds")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if tt.manual {
				attemptCancel()
			}
			var actual outcome
			select {
			case actual = <-completed:
			case <-time.After(lifecyclecensus.Budget + cooperativeMargin):
				t.Fatal("a cancelled or expired passive census must return while the independent table lock remains held")
			}
			elapsed := time.Since(started)
			if tt.manual {
				require.ErrorIs(t, actual.err, tt.cause, "caller cancellation must preserve only the standard cancellation cause")
			} else {
				require.Error(t, actual.err, "the blocked collector must fail rather than return a success report when either bounded timeout wins")
				require.True(t, errors.Is(actual.err, tt.cause) || actual.err.Error() == "writer observation failed", "the client deadline or the sanitized PostgreSQL timeout may win the fixed-budget race")
			}
			require.ErrorContains(t, actual.err, "writer observation failed", "the blocked writer read must fail with a sanitized stage diagnostic rather than return success")
			require.Equal(t, lifecyclecensus.Report{}, actual.report, "a failed read must not return a successful partial observation report")
			require.Less(t, elapsed, lifecyclecensus.Budget+cooperativeMargin, "a blocked passive census must stop within its fixed budget plus cooperative cleanup margin without releasing the independent lock")
			if !tt.manual {
				require.GreaterOrEqual(t, elapsed, lifecyclecensus.Budget, "the budget probe must exercise the actual fixed five-second deadline rather than an earlier caller cancellation")
			}
			recoveryCtx, recoveryCancel := context.WithTimeout(ctx, cooperativeMargin)
			defer recoveryCancel()
			for {
				var clients int
				require.NoError(t, observer.QueryRow(recoveryCtx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND pid<>$1 AND application_name='143:lifecycle_census'`, actor.PgConn().PID()).Scan(&clients), "inspect whether cancellation left any standalone collector connection behind while the table remains locked")
				if clients == 0 {
					break
				}
				select {
				case <-recoveryCtx.Done():
					t.Fatal("cancelled census must close its own database connection and transaction before the blocker is released")
				case <-time.After(10 * time.Millisecond):
				}
			}
			require.NoError(t, lockTx.Rollback(ctx), "release the test lock only after the collector has stopped and returned no success report")
			recovered, err := lifecyclecensus.Collect(ctx, actor.Config().ConnString())
			require.NoError(t, err, "a later standalone census must succeed after the cancelled transaction has been cleaned up")
			require.Equal(t, 1, recovered.SchemaVersion, "the recovered standalone collection must return the exact versioned observation contract")
		})
	}
}

func TestLifecycleCensusCommandExitFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for standalone command-exit proofs")
	}
	// Build the real command outside the checkout, before measuring its runtime.
	// In particular, this is not a test-helper process that keeps the caller's
	// pgx cleanup goroutine alive after the standalone command should have exited.
	binary := filepath.Join(t.TempDir(), "lifecycle-census")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), time.Minute)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-trimpath", "-o", binary, "../lifecycle-census")
	if err := build.Run(); err != nil {
		t.Fatal("build the actual standalone census command before starting its runtime proof")
	}
	tests := []struct {
		name   string
		signal syscall.Signal
	}{
		{name: "fixed budget exits with the independent table lock still held"},
		{name: "SIGTERM joins cancellation before command exit", signal: syscall.SIGTERM},
		{name: "SIGKILL leaves server timeouts to clear the disconnected backend", signal: syscall.SIGKILL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, actor := newContentMigrationPostgres(t, false)
			require.NoError(t, migration.Up(), "apply the unchanged full migration chain before the standalone-process lock probe")
			ctx, cancel := context.WithTimeout(context.Background(), 4*lifecyclecensus.Budget)
			defer cancel()
			lockTx, err := actor.Begin(ctx)
			require.NoError(t, err, "open the independent transaction that will outlive the standalone census command")
			defer func() { _ = lockTx.Rollback(ctx) }()
			_, err = lockTx.Exec(ctx, `LOCK TABLE lifecycle_writer_instances IN ACCESS EXCLUSIVE MODE`)
			require.NoError(t, err, "hold the writer table lock until after child exit and independently verified backend cleanup")
			observer := newLifecycleWriterInstanceConnection(t, ctx, actor.Config())
			childCtx, childCancel := context.WithTimeout(ctx, 2*lifecyclecensus.Budget)
			child := exec.CommandContext(childCtx, binary)
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "DATABASE_URL=") {
					child.Env = append(child.Env, value)
				}
			}
			child.Env = append(child.Env, "DATABASE_URL="+actor.Config().ConnString())
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			started := time.Now()
			if err := child.Start(); err != nil {
				childCancel()
				t.Fatal("start only the newly built standalone census command against its generated local database")
			}
			completed := make(chan error, 1)
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				completed <- child.Wait()
			}()
			cooperativeMargin := lifecyclecensus.Budget / 2
			defer func() {
				childCancel()
				select {
				case <-joined:
				case <-time.After(lifecyclecensus.Budget + cooperativeMargin):
					t.Error("the standalone child must be joined before database fixture cleanup")
				}
			}()
			blockedCtx, blockedCancel := context.WithTimeout(ctx, cooperativeMargin)
			defer blockedCancel()
			for {
				var blocked bool
				require.NoError(t, observer.QueryRow(blockedCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name='143:lifecycle_census' AND state='active' AND wait_event_type='Lock')`).Scan(&blocked), "confirm that the actual child reached its read-only transaction's table-lock wait")
				if blocked {
					break
				}
				select {
				case <-blockedCtx.Done():
					t.Fatal("the actual standalone command must reach the confirmed table-lock wait before a signal or timeout proof proceeds")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if tt.signal != 0 {
				require.NoError(t, child.Process.Signal(tt.signal), "send only the selected signal to the test-owned standalone command after the confirmed lock wait")
			}
			var childErr error
			select {
			case childErr = <-completed:
			case <-time.After(lifecyclecensus.Budget + cooperativeMargin):
				t.Fatal("the actual standalone command must exit within its cooperative runtime bound while the external lock remains held")
			}
			elapsed := time.Since(started)
			var exitErr *exec.ExitError
			require.ErrorAs(t, childErr, &exitErr, "the blocked standalone command must not exit successfully or return a success report")
			require.Zero(t, stdout.Len(), "the blocked or signaled standalone command must emit no report bytes")
			require.Less(t, elapsed, lifecyclecensus.Budget+cooperativeMargin, "the standalone command must exit within its actual fixed budget plus cooperative margin")
			if tt.signal == 0 {
				require.GreaterOrEqual(t, elapsed, lifecyclecensus.Budget, "the natural exit must exercise the real fixed budget without an earlier caller signal")
			}
			if tt.signal == syscall.SIGKILL {
				require.Equal(t, -1, exitErr.ExitCode(), "SIGKILL must terminate the child rather than let it report an application exit status")
				require.Zero(t, stderr.Len(), "a force-killed blocked child must not invent a completed diagnostic event")
			} else {
				require.Equal(t, 1, exitErr.ExitCode(), "normal cancellation or bounded timeout must exit through the command's failure path")
				var diagnostic map[string]string
				if err := json.Unmarshal(stderr.Bytes(), &diagnostic); err != nil {
					t.Fatal("standalone failure diagnostics must be exactly one structured JSON event")
				}
				messages := map[string]string{"collection_failed": "could not collect census observations", "collection_timed_out": "census collection exceeded its time budget"}
				if tt.signal == syscall.SIGTERM {
					messages = map[string]string{"collection_canceled": "census collection was canceled"}
				}
				message, allowed := messages[diagnostic["code"]]
				require.True(t, allowed, "the child must emit only a static allowlisted cancellation or bounded-timeout code")
				// Do not print captured diagnostics, URLs or executable paths even
				// on assertion failure: these tests also run in public CI logs.
				require.True(t, maps.Equal(map[string]string{"level": "error", "code": diagnostic["code"], "message": message}, diagnostic), "the child diagnostic must contain exactly its static safe fields without driver messages or private configuration")
			}
			// This observer is in a different process from the now-exited CLI.
			// SIGKILL additionally proves cleanup without any surviving client-side
			// pgx cancellation goroutine; server timeouts must bound its lock wait.
			cleanupCtx, cleanupCancel := context.WithTimeout(ctx, lifecyclecensus.Budget+cooperativeMargin)
			defer cleanupCancel()
			for {
				var backends int
				require.NoError(t, observer.QueryRow(cleanupCtx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name='143:lifecycle_census'`).Scan(&backends), "independently verify that the exited standalone child left no backend or open transaction while the lock remains held")
				if backends == 0 {
					break
				}
				select {
				case <-cleanupCtx.Done():
					t.Fatal("the exited standalone child must leave no PostgreSQL backend or transaction before the independent table lock is released")
				case <-time.After(10 * time.Millisecond):
				}
			}
			require.NoError(t, lockTx.Rollback(ctx), "release the independent lock only after actual command exit and database-confirmed cleanup")
		})
	}
}

func lifecycleCensusRoleURL(t *testing.T, config *pgx.ConnConfig) string {
	t.Helper()
	target, err := url.Parse(config.ConnString())
	require.NoError(t, err, "parse the original explicit test URI without emitting its configuration")
	require.Contains(t, []string{"postgres", "postgresql"}, target.Scheme, "the standalone test must retain an explicit PostgreSQL URI")
	query := target.Query()
	for _, overridden := range []string{"user", "password", "dbname", "database"} {
		query.Del(overridden)
	}
	target.User = url.UserPassword(config.User, config.Password)
	target.Path, target.RawPath = "/"+config.Database, ""
	query.Set("host", config.Host)
	query.Set("port", strconv.Itoa(int(config.Port)))
	target.RawQuery = query.Encode()
	return target.String()
}

func newLifecycleCensusRole(t *testing.T, ctx context.Context, actor *pgx.Conn) string {
	t.Helper()
	role := "census_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{role}.Sanitize()
	_, err := actor.Exec(ctx, "CREATE ROLE "+identifier+" LOGIN PASSWORD 'synthetic-census-only-password'")
	require.NoError(t, err, "create only a uniquely named synthetic local observer role")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, err := actor.Exec(cleanupCtx, "DROP ROLE "+identifier)
		require.NoError(t, err, fmt.Sprintf("remove only the generated statistics-test role %s after its clients close", role))
	})
	return role
}

func ptrLifecycleCensus[T any](value T) *T { return &value }
