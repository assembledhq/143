package main

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestSessionExecutionEpochMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh full schema"},
		{name: "legacy execution records upgrade each table rollback and reapply", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			var fixture executionEpochMigrationFixture
			var before json.RawMessage
			var projection executionEpochMigrationProjection
			if tt.upgrade {
				require.NoError(t, migration.Migrate(325), "construct the preceding full schema including passive request readiness")
				fixture = seedExecutionEpochMigrationFixture(t, ctx, conn)
				before = executionEpochMigrationLegacyRows(t, ctx, conn, fixture)
				projection = executionEpochMigrationLegacyProjection(t, ctx, conn, fixture)
				requireExecutionEpochMigrationBoundary(t, ctx, conn, migration, fixture, before, projection, 325)
				// Each hot table must commit independently; no migration may
				// acquire the other two hot-table ALTER locks in its transaction.
				for _, version := range []uint{326, 327, 328} {
					require.NoError(t, migration.Migrate(version), "apply the next single-table execution metadata migration")
					requireExecutionEpochMigrationBoundary(t, ctx, conn, migration, fixture, before, projection, version)
				}
			} else {
				require.NoError(t, migration.Migrate(328), "apply the real full schema through all three passive execution migrations")
				fixture = seedExecutionEpochMigrationFixture(t, ctx, conn)
				before = executionEpochMigrationLegacyRows(t, ctx, conn, fixture)
				projection = executionEpochMigrationLegacyProjection(t, ctx, conn, fixture)
				requireExecutionEpochMigrationBoundary(t, ctx, conn, migration, fixture, before, projection, 328)
			}
			requireExecutionEpochMigrationUnknown(t, ctx, conn, fixture)
			newFixture := seedExecutionEpochMigrationFixture(t, ctx, conn)
			requireExecutionEpochMigrationUnknown(t, ctx, conn, newFixture)
			requireExecutionEpochMigrationScope(t, ctx, conn, fixture, newFixture)
			requireExecutionEpochMigrationRoundTrip(t, ctx, conn, fixture, projection)
			require.JSONEq(t, string(before), string(executionEpochMigrationLegacyRows(t, ctx, conn, fixture)), "passive metadata writes and observations must preserve the entire legacy graph including writer and request metadata")

			// Schema steps share the same isolated connection and deliberately
			// run sequentially; the independent database fixtures are parallel.
			for _, version := range []uint{327, 326, 325} {
				require.NoError(t, migration.Steps(-1), "roll back only the next table after removing its metadata readers")
				requireExecutionEpochMigrationBoundary(t, ctx, conn, migration, fixture, before, projection, version)
			}
			for _, version := range []uint{326, 327, 328} {
				require.NoError(t, migration.Migrate(version), "reapply one table without inventing historical admission or drain provenance")
				requireExecutionEpochMigrationBoundary(t, ctx, conn, migration, fixture, before, projection, version)
			}
			requireExecutionEpochMigrationUnknown(t, ctx, conn, fixture)
			requireExecutionEpochMigrationUnknown(t, ctx, conn, newFixture)
		})
	}
}

type executionEpochMigrationFixture struct {
	executor                             executorWriterFixture
	runtimeID, expiredRuntimeID, batchID uuid.UUID
	threadlessExecutorID                 uuid.UUID
}

func seedExecutionEpochMigrationFixture(t *testing.T, ctx context.Context, conn *pgx.Conn) executionEpochMigrationFixture {
	t.Helper()
	fixture := executionEpochMigrationFixture{executor: newExecutorWriterFixture(t, ctx, conn), expiredRuntimeID: uuid.New(), batchID: uuid.New(), threadlessExecutorID: uuid.New()}
	f := fixture.executor
	f.insertLegacyExecutor(t, ctx, conn, models.SessionExecutorStatusDraining, models.DrainIntentPlannedRollout)
	runtime, err := db.NewThreadRuntimeStore(conn).CreateStarting(ctx, f.org, db.CreateThreadRuntimeParams{SessionID: f.session, ThreadID: f.thread, AgentType: models.DefaultDefaultAgentType, OwnerNodeID: f.host, LeaseToken: f.token, LeaseDuration: 24 * time.Hour})
	require.NoError(t, err, "create a runtime using unchanged SQL that omits execution epochs and drain proof")
	fixture.runtimeID = runtime.ID
	batch := &pgx.Batch{}
	batch.Queue(`UPDATE sessions SET admission_epoch=23,workspace_generation=17 WHERE org_id=$1 AND id=$2`, f.org, f.session)
	batch.Queue(`UPDATE session_threads SET admission_epoch=31 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.thread)
	batch.Queue(`UPDATE nodes SET lifecycle_writer_version=7 WHERE id=$1`, f.host)
	batch.Queue(`UPDATE session_executors SET lifecycle_writer_version=7 WHERE org_id=$1 AND id=$2`, f.org, f.id)
	batch.Queue(`UPDATE thread_runtimes SET status='draining' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, runtime.ID)
	batch.Queue(`INSERT INTO thread_runtimes(id,org_id,session_id,thread_id,agent_type,status,owner_node_id,lease_token,lease_expires_at,closed_at) VALUES($1,$2,$3,$4,'claude_code','lost',$5,$6,'2000-01-01','2000-01-01')`, fixture.expiredRuntimeID, f.org, f.session, f.thread, f.host, uuid.New())
	batch.Queue(`INSERT INTO session_executors(id,org_id,session_id,job_id,job_type,host_node_id,owner_id,lock_token,status,lease_expires_at,lifecycle_writer_version) VALUES($1,$2,$3,$4,'run_agent',$5,'synthetic-unthreaded-owner',$6,'completed','2000-01-01',11)`, fixture.threadlessExecutorID, f.org, f.session, f.job, f.host, uuid.New())
	batch.Queue(`INSERT INTO thread_inbox_delivery_batches(id,org_id,session_id,thread_id,runtime_id,sequence_start,sequence_end,status,acknowledged_at) VALUES($1,$2,$3,$4,$5,1,2,'acknowledged','2026-01-01')`, fixture.batchID, f.org, f.session, f.thread, runtime.ID)
	require.NoError(t, conn.SendBatch(ctx, batch).Close(), "seed legacy execution records with independent parent epochs writer versions status and lease observations")
	request := models.HumanInputRequest{OrgID: f.org, SessionID: f.session, ThreadID: &f.thread, AgentType: models.DefaultDefaultAgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Synthetic execution metadata fixture", Body: "Provide a synthetic response"}
	require.NoError(t, db.NewSessionHumanInputRequestStore(conn).Create(ctx, &request), "include an existing request in the legacy execution graph")
	_, err = conn.Exec(ctx, `UPDATE session_human_input_requests SET admission_session_epoch=41,admission_thread_epoch=43,legacy_request=false,proof_policy_version=7,enforcement_version=11,classification_version=13,readiness_state='waiting',readiness_deadline_at='2030-01-01',readiness_proof='{"synthetic_receipt":"example"}' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, request.ID)
	require.NoError(t, err, "populate previously migrated request metadata so later schema steps must preserve it exactly")
	return fixture
}

func executionEpochMigrationLegacyRows(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture) json.RawMessage {
	t.Helper()
	var value json.RawMessage
	f := fixture.executor
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
		'session',(SELECT to_jsonb(s) FROM sessions s WHERE s.org_id=$1 AND s.id=$2),
		'threads',(SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2),
		'node',(SELECT to_jsonb(n) FROM nodes n WHERE n.id=$3),
		'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM jobs j WHERE j.org_id=$1 AND j.id=$4),
		'requests',(SELECT jsonb_agg(to_jsonb(q) ORDER BY q.id) FROM session_human_input_requests q WHERE q.org_id=$1 AND q.session_id=$2),
		'runtimes',(SELECT jsonb_agg(to_jsonb(r)-'admission_session_epoch'-'admission_thread_epoch'-'drain_proof' ORDER BY r.id) FROM thread_runtimes r WHERE r.org_id=$1 AND r.session_id=$2),
		'executors',(SELECT jsonb_agg(to_jsonb(e)-'admission_session_epoch'-'admission_thread_epoch' ORDER BY e.id) FROM session_executors e WHERE e.org_id=$1 AND e.session_id=$2),
		'batches',(SELECT jsonb_agg(to_jsonb(b)-'admission_session_epoch'-'admission_thread_epoch' ORDER BY b.id) FROM thread_inbox_delivery_batches b WHERE b.org_id=$1 AND b.session_id=$2))`, f.org, f.session, f.host, f.job).Scan(&value)
	require.NoError(t, err, "capture every legacy execution and parent field including node writer versions and existing request epoch readiness metadata")
	return value
}

type executionEpochMigrationProjection struct {
	Runtime   models.ThreadRuntime
	Executors []models.SessionExecutor
	Batch     models.ThreadInboxDeliveryBatch
}

func executionEpochMigrationLegacyProjection(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture) executionEpochMigrationProjection {
	t.Helper()
	f := fixture.executor
	runtime, err := db.NewThreadRuntimeStore(conn).GetActiveByThread(ctx, f.org, f.thread)
	require.NoError(t, err, "read the complete existing runtime projection independently of passive metadata")
	executors := make([]models.SessionExecutor, 0, 2)
	for _, id := range []uuid.UUID{f.id, fixture.threadlessExecutorID} {
		executor, err := db.NewSessionExecutorStore(conn).GetByID(ctx, id)
		require.NoError(t, err, "read the complete existing threaded and unthreaded executor projections")
		executors = append(executors, executor)
	}
	batch, err := db.NewSessionActivityPhaseStore(conn).GetInboxDeliveryBatch(ctx, f.org, fixture.batchID)
	require.NoError(t, err, "read the complete existing delivery batch projection")
	projection := executionEpochMigrationProjection{Runtime: runtime, Executors: executors, Batch: batch}
	encoded, err := json.Marshal(projection)
	require.NoError(t, err, "serialize the complete legacy execution API projections")
	for _, key := range []string{"admission_session_epoch", "admission_thread_epoch", "drain_proof"} {
		require.NotContains(t, string(encoded), `"`+key+`"`, "passive execution observations must not expand public execution models or API representations")
	}
	return projection
}

func requireExecutionEpochMigrationBoundary(t *testing.T, ctx context.Context, conn *pgx.Conn, migration *migrate.Migrate, fixture executionEpochMigrationFixture, before json.RawMessage, projection executionEpochMigrationProjection, version uint) {
	t.Helper()
	actualVersion, dirty, err := migration.Version()
	require.NoError(t, err, "read the ledger after each independent execution metadata migration")
	require.Equal(t, version, actualVersion, "each execution table must reach exactly its selected schema boundary")
	require.False(t, dirty, "each single-table upgrade and rollback must leave a clean migration ledger")
	require.JSONEq(t, string(before), string(executionEpochMigrationLegacyRows(t, ctx, conn, fixture)), "each schema boundary must preserve all legacy execution parent and request fields")
	require.Equal(t, projection, executionEpochMigrationLegacyProjection(t, ctx, conn, fixture), "each schema boundary must preserve every existing runtime executor and batch API field")
	type column struct {
		Table    string  `db:"table_name"`
		Name     string  `db:"column_name"`
		DataType string  `db:"data_type"`
		Nullable string  `db:"is_nullable"`
		Default  *string `db:"column_default"`
	}
	want := []column{}
	if version >= 327 {
		want = append(want, column{Table: "session_executors", Name: "admission_session_epoch", DataType: "bigint", Nullable: "YES"}, column{Table: "session_executors", Name: "admission_thread_epoch", DataType: "bigint", Nullable: "YES"})
	}
	if version >= 328 {
		want = append(want, column{Table: "thread_inbox_delivery_batches", Name: "admission_session_epoch", DataType: "bigint", Nullable: "YES"}, column{Table: "thread_inbox_delivery_batches", Name: "admission_thread_epoch", DataType: "bigint", Nullable: "YES"})
	}
	if version >= 326 {
		want = append(want, column{Table: "thread_runtimes", Name: "admission_session_epoch", DataType: "bigint", Nullable: "YES"}, column{Table: "thread_runtimes", Name: "admission_thread_epoch", DataType: "bigint", Nullable: "YES"}, column{Table: "thread_runtimes", Name: "drain_proof", DataType: "jsonb", Nullable: "YES"})
	}
	rows, err := conn.Query(ctx, `SELECT table_name,column_name,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name IN ('thread_runtimes','session_executors','thread_inbox_delivery_batches') AND column_name IN ('admission_session_epoch','admission_thread_epoch','drain_proof') ORDER BY table_name,column_name`)
	require.NoError(t, err, "inspect every execution epoch and drain column independently on each hot table")
	actual, err := pgx.CollectRows(rows, pgx.RowToStructByName[column])
	require.NoError(t, err, "collect complete execution metadata types nullability and default expressions")
	require.Equal(t, want, actual, "single-table migrations must expose exactly zero three five or seven nullable default-free columns at their own boundaries")
}

func requireExecutionEpochMigrationUnknown(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture) {
	t.Helper()
	f := fixture.executor
	for _, id := range []uuid.UUID{fixture.runtimeID, fixture.expiredRuntimeID} {
		actual, err := db.NewThreadRuntimeStore(conn).GetLifecycleMetadata(ctx, f.org, f.session, f.thread, id)
		require.NoError(t, err, "observe both draining and expired lost runtimes without applying active-lease visibility")
		require.Equal(t, db.RuntimeLifecycleMetadata{}, actual, "parent epoch status closed time and lease expiry must never infer an execution epoch or physical drain proof")
	}
	targets := []struct {
		id     uuid.UUID
		thread *uuid.UUID
	}{
		{id: f.id, thread: &f.thread},
		{id: fixture.threadlessExecutorID},
	}
	for _, target := range targets {
		actual, err := db.NewSessionExecutorStore(conn).GetLifecycleMetadata(ctx, f.org, f.session, target.thread, target.id)
		require.NoError(t, err, "observe both threaded draining and unthreaded expired completed executors")
		require.Equal(t, db.ExecutionEpochMetadata{}, actual, "legacy allocation must retain unknown provenance independently of parent epochs writer version or executor state")
	}
	batch, err := db.NewSessionActivityPhaseStore(conn).GetInboxBatchLifecycleMetadata(ctx, f.org, f.session, f.thread, fixture.batchID)
	require.NoError(t, err, "observe a legacy acknowledged batch without deriving allocation provenance")
	require.Equal(t, db.ExecutionEpochMetadata{}, batch, "acknowledgment and parent epochs must not supply missing batch provenance")
}

func requireExecutionEpochMigrationScope(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture, other executionEpochMigrationFixture) {
	t.Helper()
	f := fixture.executor
	targets := []struct {
		name  string
		id    uuid.UUID
		empty any
		read  func(uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (any, error)
	}{
		{name: "runtime", id: fixture.runtimeID, empty: db.RuntimeLifecycleMetadata{}, read: func(org, session, thread, id uuid.UUID) (any, error) {
			return db.NewThreadRuntimeStore(conn).GetLifecycleMetadata(ctx, org, session, thread, id)
		}},
		{name: "executor", id: f.id, empty: db.ExecutionEpochMetadata{}, read: func(org, session, thread, id uuid.UUID) (any, error) {
			return db.NewSessionExecutorStore(conn).GetLifecycleMetadata(ctx, org, session, &thread, id)
		}},
		{name: "batch", id: fixture.batchID, empty: db.ExecutionEpochMetadata{}, read: func(org, session, thread, id uuid.UUID) (any, error) {
			return db.NewSessionActivityPhaseStore(conn).GetInboxBatchLifecycleMetadata(ctx, org, session, thread, id)
		}},
	}
	fences := []struct {
		name string
		args func(uuid.UUID) [4]uuid.UUID
	}{
		{name: "foreign organization", args: func(id uuid.UUID) [4]uuid.UUID { return [4]uuid.UUID{other.executor.org, f.session, f.thread, id} }},
		{name: "wrong session", args: func(id uuid.UUID) [4]uuid.UUID { return [4]uuid.UUID{f.org, other.executor.session, f.thread, id} }},
		{name: "wrong lane", args: func(id uuid.UUID) [4]uuid.UUID { return [4]uuid.UUID{f.org, f.session, other.executor.thread, id} }},
		{name: "missing record", args: func(uuid.UUID) [4]uuid.UUID { return [4]uuid.UUID{f.org, f.session, f.thread, uuid.New()} }},
	}
	// Scope cases deliberately share one connection and run sequentially.
	for _, target := range targets {
		for _, fence := range fences {
			args := fence.args(target.id)
			actual, err := target.read(args[0], args[1], args[2], args[3])
			require.ErrorIs(t, err, pgx.ErrNoRows, target.name+" "+fence.name+" must reject a mismatched exact identity")
			require.Equal(t, target.empty, actual, target.name+" "+fence.name+" must not disclose partial metadata")
		}
	}
	threads := []struct {
		name string
		id   uuid.UUID
		lane *uuid.UUID
	}{
		{name: "NULL is not a threaded wildcard", id: f.id},
		{name: "lane is not an unthreaded wildcard", id: fixture.threadlessExecutorID, lane: &f.thread},
		{name: "zero UUID is not SQL NULL", id: fixture.threadlessExecutorID, lane: requestReadinessMigrationPtr(uuid.Nil)},
	}
	for _, target := range threads {
		actual, err := db.NewSessionExecutorStore(conn).GetLifecycleMetadata(ctx, f.org, f.session, target.lane, target.id)
		require.ErrorIs(t, err, pgx.ErrNoRows, target.name+" when reading executor metadata")
		require.Equal(t, db.ExecutionEpochMetadata{}, actual, target.name+" without returning partial execution provenance")
	}
}

func requireExecutionEpochMigrationRoundTrip(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture executionEpochMigrationFixture, projection executionEpochMigrationProjection) {
	t.Helper()
	f := fixture.executor
	tests := []struct {
		name string
		want db.ExecutionEpochMetadata
	}{
		{name: "unknown epochs"},
		{name: "present session zero", want: db.ExecutionEpochMetadata{AdmissionSessionEpoch: requestReadinessMigrationPtr(int64(0))}},
		{name: "present lane zero", want: db.ExecutionEpochMetadata{AdmissionThreadEpoch: requestReadinessMigrationPtr(int64(0))}},
		{name: "both present zeros", want: db.ExecutionEpochMetadata{AdmissionSessionEpoch: requestReadinessMigrationPtr(int64(0)), AdmissionThreadEpoch: requestReadinessMigrationPtr(int64(0))}},
		{name: "large independent epochs", want: db.ExecutionEpochMetadata{AdmissionSessionEpoch: requestReadinessMigrationPtr(int64(math.MaxInt64 - 1)), AdmissionThreadEpoch: requestReadinessMigrationPtr(int64(math.MaxInt64))}},
		{name: "complete signed bigint representation", want: db.ExecutionEpochMetadata{AdmissionSessionEpoch: requestReadinessMigrationPtr(int64(math.MinInt64))}},
		{name: "lane only", want: db.ExecutionEpochMetadata{AdmissionThreadEpoch: requestReadinessMigrationPtr(int64(37))}},
		{name: "unknown epochs restored"},
	}
	// Independent NULL and present-value transitions reuse the same isolated
	// connection; parallelism is across complete database fixtures instead.
	for _, tt := range tests {
		_, err := conn.Exec(ctx, `UPDATE thread_runtimes SET admission_session_epoch=$5,admission_thread_epoch=$6 WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND id=$4`, f.org, f.session, f.thread, fixture.runtimeID, tt.want.AdmissionSessionEpoch, tt.want.AdmissionThreadEpoch)
		require.NoError(t, err, tt.name+" must be representable independently on runtimes")
		_, err = conn.Exec(ctx, `UPDATE session_executors SET admission_session_epoch=$4,admission_thread_epoch=$5 WHERE org_id=$1 AND session_id=$2 AND id=ANY($3::uuid[])`, f.org, f.session, []uuid.UUID{f.id, fixture.threadlessExecutorID}, tt.want.AdmissionSessionEpoch, tt.want.AdmissionThreadEpoch)
		require.NoError(t, err, tt.name+" must be representable on both threaded and unthreaded executors")
		_, err = conn.Exec(ctx, `UPDATE thread_inbox_delivery_batches SET admission_session_epoch=$5,admission_thread_epoch=$6 WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND id=$4`, f.org, f.session, f.thread, fixture.batchID, tt.want.AdmissionSessionEpoch, tt.want.AdmissionThreadEpoch)
		require.NoError(t, err, tt.name+" must be representable independently on delivery batches")
		runtime, err := db.NewThreadRuntimeStore(conn).GetLifecycleMetadata(ctx, f.org, f.session, f.thread, fixture.runtimeID)
		require.NoError(t, err, tt.name+" must round-trip through the real runtime reader")
		require.Equal(t, db.RuntimeLifecycleMetadata{AdmissionSessionEpoch: tt.want.AdmissionSessionEpoch, AdmissionThreadEpoch: tt.want.AdmissionThreadEpoch}, runtime, tt.name+" must retain exact runtime epoch pointer values without a fabricated drain proof")
		for _, target := range []struct {
			id   uuid.UUID
			lane *uuid.UUID
		}{{id: f.id, lane: &f.thread}, {id: fixture.threadlessExecutorID}} {
			executor, err := db.NewSessionExecutorStore(conn).GetLifecycleMetadata(ctx, f.org, f.session, target.lane, target.id)
			require.NoError(t, err, tt.name+" must round-trip for the exact threaded or unthreaded executor")
			require.Equal(t, tt.want, executor, tt.name+" must preserve complete executor bigint precision and independent nullability")
		}
		batch, err := db.NewSessionActivityPhaseStore(conn).GetInboxBatchLifecycleMetadata(ctx, f.org, f.session, f.thread, fixture.batchID)
		require.NoError(t, err, tt.name+" must round-trip for the exact delivery batch")
		require.Equal(t, tt.want, batch, tt.name+" must preserve complete batch bigint precision and independent nullability")
		require.Equal(t, projection, executionEpochMigrationLegacyProjection(t, ctx, conn, fixture), tt.name+" must preserve every existing runtime executor and batch API field")
	}
	proofs := []struct {
		name string
		want json.RawMessage
	}{
		{name: "SQL NULL"},
		{name: "JSON null", want: json.RawMessage(`null`)},
		{name: "opaque future receipt", want: json.RawMessage(`{"synthetic_receipt":"example","version":7}`)},
		{name: "opaque future array", want: json.RawMessage(`["synthetic",7]`)},
		{name: "opaque future scalar", want: json.RawMessage(`false`)},
		{name: "SQL NULL restored"},
	}
	for _, tt := range proofs {
		want := tt.want
		if want != nil {
			var canonical json.RawMessage
			require.NoError(t, conn.QueryRow(ctx, `SELECT $1::jsonb`, want).Scan(&canonical), "canonicalize only the expected opaque JSON representation")
			require.NotNil(t, canonical, tt.name+" must remain present JSON rather than become SQL NULL")
			require.JSONEq(t, string(want), string(canonical), tt.name+" must retain its independent original JSON value during canonicalization")
			want = canonical
		}
		_, err := conn.Exec(ctx, `UPDATE thread_runtimes SET drain_proof=$5 WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND id=$4`, f.org, f.session, f.thread, fixture.runtimeID, want)
		require.NoError(t, err, tt.name+" must remain representable without observing or authorizing any actual provider process")
		actual, err := db.NewThreadRuntimeStore(conn).GetLifecycleMetadata(ctx, f.org, f.session, f.thread, fixture.runtimeID)
		require.NoError(t, err, tt.name+" must round-trip through the passive runtime reader")
		require.Equal(t, db.RuntimeLifecycleMetadata{DrainProof: want}, actual, tt.name+" must retain the exact JSON value and preserve unknown epochs")
		require.Equal(t, projection, executionEpochMigrationLegacyProjection(t, ctx, conn, fixture), tt.name+" must not change any existing execution API field")
	}
}
