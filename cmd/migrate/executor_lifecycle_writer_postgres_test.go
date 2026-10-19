package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestExecutorLifecycleWriterVersionMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh full schema"},
		{name: "existing executor upgrade rollback and reapply", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if tt.upgrade {
				require.NoError(t, migration.Migrate(317), "construct the complete schema before passive executor metadata")
			} else {
				require.NoError(t, migration.Migrate(318), "apply the complete schema through passive executor metadata")
			}
			fixture := newExecutorWriterFixture(t, ctx, conn)
			fixture.insertLegacyExecutor(t, ctx, conn, models.SessionExecutorStatusDraining, models.DrainIntentPlannedRollout)
			before := executorWriterRowJSON(t, ctx, conn, fixture.id, true)
			var legacyColumns []string
			require.NoError(t, conn.QueryRow(ctx, `SELECT array_agg(column_name::text ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name='session_executors' AND column_name <> 'lifecycle_writer_version'`).Scan(&legacyColumns), "capture every pre-existing executor column")
			if tt.upgrade {
				require.NoError(t, migration.Migrate(318), "upgrade existing executor data through the actual migration chain")
			}
			assertBoundary := func(wantVersion uint, hasMetadata bool) {
				version, dirty, err := migration.Version()
				require.NoError(t, err, "inspect the real migration ledger")
				require.Equal(t, wantVersion, version, "upgrade and rollback must stop at the pinned historical boundary")
				require.False(t, dirty, "successful metadata transitions must leave a clean migration ledger")
				require.JSONEq(t, string(before), string(executorWriterRowJSON(t, ctx, conn, fixture.id, true)), "schema transitions must preserve every existing executor field")
				var actualColumns []string
				require.NoError(t, conn.QueryRow(ctx, `SELECT array_agg(column_name::text ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name='session_executors'`).Scan(&actualColumns), "inspect all executor columns at each migration boundary")
				wantColumns := append([]string(nil), legacyColumns...)
				if hasMetadata {
					wantColumns = append(wantColumns, "lifecycle_writer_version")
				}
				require.Equal(t, wantColumns, actualColumns, "down must remove only the added metadata column")
				if hasMetadata {
					var actual int
					require.NoError(t, conn.QueryRow(ctx, `SELECT lifecycle_writer_version FROM session_executors WHERE id=$1`, fixture.id).Scan(&actual), "read defaulted executor metadata")
					require.Equal(t, 0, actual, "existing and omitted-column executor inserts must default to version zero")
					var dataType, nullable, defaultValue string
					require.NoError(t, conn.QueryRow(ctx, `SELECT data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='session_executors' AND column_name='lifecycle_writer_version'`).Scan(&dataType, &nullable, &defaultValue), "inspect metadata type, nullability and default")
					require.Equal(t, []string{"integer", "NO", "0"}, []string{dataType, nullable, defaultValue}, "passive metadata must remain a non-null default-zero integer")
				}
			}
			assertBoundary(318, true)
			constraints := []struct {
				name, code string
				value      any
			}{
				{name: "negative version", value: -1, code: "23514"},
				{name: "null version", value: nil, code: "23502"},
			}
			// Constraint failures share this isolated migration fixture and run
			// serially; each autocommit statement must preserve the original row.
			for _, constraint := range constraints {
				_, err := conn.Exec(ctx, `UPDATE session_executors SET lifecycle_writer_version=$1 WHERE id=$2`, constraint.value, fixture.id)
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, constraint.name+" must be rejected by PostgreSQL")
				require.Equal(t, constraint.code, pgErr.Code, constraint.name+" must fail for its intended constraint")
			}
			require.NoError(t, migration.Steps(-1), "roll back only passive executor metadata")
			assertBoundary(317, false)
			require.NoError(t, migration.Migrate(318), "reapply metadata without modifying legacy executor data")
			assertBoundary(318, true)
		})
	}
}

func TestSessionExecutorLifecycleWriterVersionFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	// One complete migration fixture serves independent per-case connections
	// and rows. Its t.Cleanup callbacks run only after all parallel children.
	migration, conn := newContentMigrationPostgres(t, false)
	require.NoError(t, migration.Migrate(318), "test actual executor SQL against the full migrated schema")
	config := conn.Config().Copy()
	tests := []struct {
		name         string
		operation    string
		status       models.SessionExecutorStatus
		intent       models.DrainIntent
		cancel       string
		fence        string
		wantAccepted bool
	}{
		{name: "allocation and future reads", operation: "create"},
		{name: "boot from starting with expired lease", operation: "boot", status: models.SessionExecutorStatusStarting, intent: models.DrainIntentPlannedRollout, wantAccepted: true},
		{name: "boot from running with expired lease", operation: "boot", status: models.SessionExecutorStatusRunning, intent: models.DrainIntentRuntimeCeiling, wantAccepted: true},
		{name: "heartbeat starting without cancellation", operation: "heartbeat", status: models.SessionExecutorStatusStarting, intent: models.DrainIntentNone, wantAccepted: true},
		{name: "heartbeat running with requested cancellation", operation: "heartbeat", status: models.SessionExecutorStatusRunning, intent: models.DrainIntentHostMaintenance, cancel: "requested", wantAccepted: true},
		{name: "heartbeat draining with cancelled thread", operation: "heartbeat", status: models.SessionExecutorStatusDraining, intent: models.DrainIntentDeployBudgetExpired, cancel: "cancelled", wantAccepted: true},
		{name: "boot rejects draining", operation: "boot", status: models.SessionExecutorStatusDraining},
		{name: "boot rejects requeued", operation: "boot", status: models.SessionExecutorStatusRequeued},
		{name: "boot rejects completed", operation: "boot", status: models.SessionExecutorStatusCompleted},
		{name: "boot rejects failed", operation: "boot", status: models.SessionExecutorStatusFailed},
		{name: "boot rejects lost", operation: "boot", status: models.SessionExecutorStatusLost},
		{name: "heartbeat rejects requeued", operation: "heartbeat", status: models.SessionExecutorStatusRequeued},
		{name: "heartbeat rejects completed", operation: "heartbeat", status: models.SessionExecutorStatusCompleted},
		{name: "heartbeat rejects failed", operation: "heartbeat", status: models.SessionExecutorStatusFailed},
		{name: "heartbeat rejects lost", operation: "heartbeat", status: models.SessionExecutorStatusLost},
		{name: "boot rejects foreign organization", operation: "boot", status: models.SessionExecutorStatusStarting, fence: "org"},
		{name: "boot rejects wrong executor id", operation: "boot", status: models.SessionExecutorStatusStarting, fence: "id"},
		{name: "boot rejects wrong token", operation: "boot", status: models.SessionExecutorStatusStarting, fence: "token"},
		{name: "heartbeat rejects foreign organization", operation: "heartbeat", status: models.SessionExecutorStatusRunning, fence: "org"},
		{name: "heartbeat rejects wrong executor id", operation: "heartbeat", status: models.SessionExecutorStatusRunning, fence: "id"},
		{name: "heartbeat rejects wrong token", operation: "heartbeat", status: models.SessionExecutorStatusRunning, fence: "token"},
		{name: "legacy omitted-column writes retain stale metadata", operation: "legacy", status: models.SessionExecutorStatusRunning},
		{name: "container write does not advertise a writer", operation: "container", status: models.SessionExecutorStatusStarting},
		{name: "drain write does not advertise a writer", operation: "drain", status: models.SessionExecutorStatusRunning},
		{name: "terminal write does not advertise a writer", operation: "terminal", status: models.SessionExecutorStatusRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			actor, err := pgx.ConnectConfig(ctx, config.Copy())
			require.NoError(t, err, "open an independent connection for this executor fixture")
			t.Cleanup(func() {
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer closeCancel()
				require.NoError(t, actor.Close(closeCtx), "close only this case's executor connection")
			})
			fixture := newExecutorWriterFixture(t, ctx, actor)
			store := db.NewSessionExecutorStore(actor)
			if tt.operation == "create" {
				id, err := store.CreateStarting(ctx, fixture.org, fixture.params())
				require.NoError(t, err, "allocate an executor using the actual dispatcher-owned insert")
				actual, err := store.GetByID(ctx, id)
				require.NoError(t, err, "read every executor field through the shared projection and scanner")
				stamp := actual.CreatedAt
				lease := stamp.Add(time.Minute)
				deadline := fixture.deadline.In(stamp.Location())
				want := models.SessionExecutor{ID: id, OrgID: fixture.org, SessionID: fixture.session, ThreadID: &fixture.thread, JobID: fixture.job, JobType: "run_agent", HostNodeID: fixture.host, OwnerID: "synthetic-owner", LockToken: fixture.token, Status: models.SessionExecutorStatusStarting, Image: "synthetic-image", BuildSHA: "synthetic-build", HeartbeatAt: &stamp, LeaseExpiresAt: &lease, RuntimeDeadlineAt: &deadline, DrainIntent: models.DrainIntentNone, StartedAt: stamp, CreatedAt: stamp, UpdatedAt: stamp, LifecycleWriterVersion: 0}
				require.Equal(t, want, actual, "allocation must preserve every supplied field and existing status, timestamp and lease default")
				requireExecutorWriterJSON(t, actual, 0)
				_, err = actor.Exec(ctx, `UPDATE session_executors SET lifecycle_writer_version=7 WHERE id=$1`, id)
				require.NoError(t, err, "seed synthetic future metadata without changing the current binary")
				want.LifecycleWriterVersion = 7
				future, err := store.GetByID(ctx, id)
				require.NoError(t, err, "read an unknown nonnegative writer version")
				require.Equal(t, want, future, "the full executor read must retain future metadata without interpreting it")
				requireExecutorWriterJSON(t, future, 7)
				return
			}
			intent := tt.intent
			if intent == "" {
				intent = models.DrainIntentPlannedRollout
			}
			fixture.insertLegacyExecutor(t, ctx, actor, tt.status, intent)
			_, err = actor.Exec(ctx, `UPDATE session_executors SET lifecycle_writer_version=7 WHERE id=$1`, fixture.id)
			require.NoError(t, err, "seed future metadata so rejected and unrelated writes cannot silently refresh it")
			if tt.cancel == "requested" {
				_, err = actor.Exec(ctx, `UPDATE session_threads SET cancel_requested_at='2026-01-01' WHERE org_id=$1 AND id=$2`, fixture.org, fixture.thread)
				require.NoError(t, err, "request cancellation on this executor's actual tenant-scoped thread")
			} else if tt.cancel == "cancelled" {
				_, err = actor.Exec(ctx, `UPDATE session_threads SET status='cancelled' WHERE org_id=$1 AND id=$2`, fixture.org, fixture.thread)
				require.NoError(t, err, "characterize cancellation signaled by the thread status")
			}
			before := executorWriterRowJSON(t, ctx, actor, fixture.id, false)
			org, id, token := fixture.org, fixture.id, fixture.token
			switch tt.fence {
			case "org":
				org = uuid.New()
			case "id":
				id = uuid.New()
			case "token":
				token = uuid.New()
			}
			const leaseDuration = 75 * time.Second
			switch tt.operation {
			case "boot":
				accepted, err := store.MarkRunningWithLease(ctx, org, id, token, leaseDuration)
				require.NoError(t, err, "executor boot must preserve the existing fenced update result")
				require.Equal(t, tt.wantAccepted, accepted, "writer metadata must not widen the boot tenant, identity, token or status predicate")
			case "heartbeat":
				accepted, gotIntent, gotCancel, err := store.HeartbeatWithLease(ctx, org, id, token, leaseDuration)
				require.NoError(t, err, "executor heartbeat must preserve the existing fenced update result")
				require.Equal(t, tt.wantAccepted, accepted, "writer metadata must not widen the heartbeat tenant, identity, token or status predicate")
				wantIntent, wantCancel := models.DrainIntentNone, false
				if tt.wantAccepted {
					wantIntent, wantCancel = intent, tt.cancel != ""
				}
				require.Equal(t, wantIntent, gotIntent, "heartbeat must return the exact pre-existing drain intent only for an accepted writer")
				require.Equal(t, wantCancel, gotCancel, "heartbeat must retain both cancellation signals and its rejected-write false result")
			case "legacy":
				_, err = actor.Exec(ctx, `UPDATE session_executors SET heartbeat_at=now(),lease_expires_at=now()+interval '75 seconds',updated_at=now() WHERE org_id=$1 AND id=$2 AND lock_token=$3`, org, id, token)
				require.NoError(t, err, "a legacy omitted-column heartbeat must remain valid")
			case "container":
				accepted, err := store.RecordContainerIDWithLease(ctx, org, id, token, "synthetic-replacement-container")
				require.NoError(t, err, "container recording must retain its existing behavior")
				require.True(t, accepted, "the matching starting executor must still accept container recording")
			case "drain":
				accepted, err := store.MarkDrainingWithLease(ctx, org, id, token)
				require.NoError(t, err, "drain mutation must retain its existing behavior")
				require.True(t, accepted, "the matching active executor must still accept a drain request")
			case "terminal":
				code := 0
				accepted, err := store.MarkTerminalWithLease(ctx, org, id, token, models.SessionExecutorStatusCompleted, &code, "")
				require.NoError(t, err, "terminal mutation must retain its existing behavior")
				require.True(t, accepted, "the matching active executor must still accept completion")
			}
			after := executorWriterRowJSON(t, ctx, actor, fixture.id, false)
			actual, err := store.GetByID(ctx, fixture.id)
			require.NoError(t, err, "inspect the actual executor after the attempted write")
			if tt.operation != "boot" && tt.operation != "heartbeat" {
				require.Equal(t, 7, actual.LifecycleWriterVersion, "legacy and unrelated lifecycle writes must not advertise current-writer compatibility")
				return
			}
			if !tt.wantAccepted {
				require.JSONEq(t, string(before), string(after), "rejected writers must preserve the entire row including future metadata")
				return
			}
			require.Equal(t, 0, actual.LifecycleWriterVersion, "each accepted executor-owned boot or heartbeat must explicitly reset future metadata to zero")
			wantStatus := tt.status
			if tt.operation == "boot" {
				wantStatus = models.SessionExecutorStatusRunning
			}
			require.Equal(t, wantStatus, actual.Status, "metadata refresh must preserve each writer's existing status transition")
			require.NotNil(t, actual.HeartbeatAt, "accepted writer must retain the existing heartbeat timestamp update")
			require.NotNil(t, actual.LeaseExpiresAt, "accepted writer must retain the existing lease renewal")
			require.Equal(t, actual.HeartbeatAt.Add(leaseDuration), *actual.LeaseExpiresAt, "existing lease duration must remain anchored to the same statement timestamp")
			require.Equal(t, *actual.HeartbeatAt, actual.UpdatedAt, "heartbeat and updated timestamps must still come from the same SQL statement")
			require.Greater(t, actual.HeartbeatAt.Unix(), fixture.deadline.Add(-24*time.Hour).Unix(), "existing expired leases must remain renewable by a matching writer")
			var beforeFields, afterFields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(before, &beforeFields), "decode the complete executor before refresh")
			require.NoError(t, json.Unmarshal(after, &afterFields), "decode the complete executor after refresh")
			for _, column := range []string{"status", "heartbeat_at", "lease_expires_at", "updated_at", "lifecycle_writer_version"} {
				delete(beforeFields, column)
				delete(afterFields, column)
			}
			require.Equal(t, beforeFields, afterFields, "writer metadata must not alter any other identity, container, deadline, drain, completion or error field")
		})
	}
}

type executorWriterFixture struct {
	id, org, session, thread, job, token uuid.UUID
	host                                 string
	deadline                             time.Time
}

func newExecutorWriterFixture(t *testing.T, ctx context.Context, conn *pgx.Conn) executorWriterFixture {
	t.Helper()
	f := executorWriterFixture{id: uuid.New(), org: uuid.New(), session: uuid.New(), thread: uuid.New(), job: uuid.New(), token: uuid.New(), deadline: time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)}
	f.host = "synthetic-worker-" + f.id.String()
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO organizations(id,name) VALUES($1,'Synthetic executor metadata fixture')`, f.org)
	batch.Queue(`INSERT INTO nodes(id,mode,host) VALUES($1,'worker','worker.invalid')`, f.host)
	batch.Queue(`INSERT INTO sessions(id,org_id) VALUES($1,$2)`, f.session, f.org)
	batch.Queue(`INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status) VALUES($1,$2,$3,'claude_code','Synthetic executor thread','running')`, f.thread, f.org, f.session)
	batch.Queue(`INSERT INTO jobs(id,org_id,queue,job_type,status,lock_token,owner_kind) VALUES($1,$2,'default','run_agent','running',$3,'session_executor')`, f.job, f.org, f.token)
	require.NoError(t, conn.SendBatch(ctx, batch).Close(), "seed independent real-schema executor parents for this case")
	return f
}

func (f executorWriterFixture) params() models.CreateSessionExecutorParams {
	return models.CreateSessionExecutorParams{SessionID: f.session, ThreadID: &f.thread, JobID: f.job, JobType: "run_agent", HostNodeID: f.host, OwnerID: "synthetic-owner", LockToken: f.token, Image: "synthetic-image", BuildSHA: "synthetic-build", RuntimeDeadlineAt: &f.deadline}
}

func (f executorWriterFixture) insertLegacyExecutor(t *testing.T, ctx context.Context, conn *pgx.Conn, status models.SessionExecutorStatus, intent models.DrainIntent) {
	t.Helper()
	_, err := conn.Exec(ctx, `INSERT INTO session_executors(id,org_id,session_id,thread_id,job_id,job_type,host_node_id,owner_id,lock_token,status,container_id,image,build_sha,heartbeat_at,lease_expires_at,runtime_deadline_at,drain_intent,drain_requested_at,drain_deadline_at,started_at,completed_at,exit_code,last_error,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,'run_agent',$6,'synthetic-owner',$7,$8,'synthetic-container','synthetic-image','synthetic-build','2026-01-01','2026-01-01',$9,$10,'2026-01-01','2026-01-02','2026-01-01','2026-01-01',19,'synthetic-existing-error','2026-01-01','2026-01-01')`, f.id, f.org, f.session, f.thread, f.job, f.host, f.token, status, f.deadline, intent)
	require.NoError(t, err, "legacy omitted-column insertion must preserve every pre-existing executor field")
}

func executorWriterRowJSON(t *testing.T, ctx context.Context, conn *pgx.Conn, id uuid.UUID, omitMetadata bool) json.RawMessage {
	t.Helper()
	query := `SELECT to_jsonb(se) FROM session_executors se WHERE id=$1`
	if omitMetadata {
		query = `SELECT to_jsonb(se)-'lifecycle_writer_version' FROM session_executors se WHERE id=$1`
	}
	var value json.RawMessage
	require.NoError(t, conn.QueryRow(ctx, query, id).Scan(&value), "capture the complete raw executor independently of the store scanner")
	return value
}

func requireExecutorWriterJSON(t *testing.T, executor models.SessionExecutor, version int) {
	t.Helper()
	encoded, err := json.Marshal(executor)
	require.NoError(t, err, "serialize executor passive metadata without interpreting it")
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields), "decode the executor JSON contract")
	actual, exists := fields["lifecycle_writer_version"]
	require.True(t, exists, "JSON must explicitly include lifecycle writer version even when it is zero")
	expected, err := json.Marshal(version)
	require.NoError(t, err, "encode the expected passive writer integer")
	require.Equal(t, expected, []byte(actual), "JSON must preserve zero and unknown future nonnegative versions exactly")
}
