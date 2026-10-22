package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestSessionExecutionEpochLegacyRuntimePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		populated bool
		reclaim   bool
	}{
		{name: "terminal close leaves absent proof absent"},
		{name: "terminal close preserves raw metadata", populated: true},
		{name: "expired lease never creates drain proof", reclaim: true},
		{name: "expired lease preserves raw metadata", populated: true, reclaim: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, conn := newExecutionEpochLegacyDatabase(t)
			fixture := seedEpochMigrationFixture(t, ctx, conn)
			runtime := createExecutionEpochLegacyRuntime(t, ctx, conn, fixture)
			requireExecutionEpochLegacyMetadata(t, ctx, conn, "thread_runtimes", fixture.orgID, runtime.ID, false)
			before, err := db.NewThreadRuntimeStore(conn).GetActiveByThread(ctx, fixture.orgID, fixture.threadID)
			require.NoError(t, err, "read the complete legacy runtime projection after allocation")
			require.Equal(t, runtime, before, "legacy allocation and active-runtime readers must agree on every model field")
			if tt.populated {
				seedExecutionEpochLegacyMetadata(t, ctx, conn, "thread_runtimes", fixture.orgID, runtime.ID)
			}
			after, err := db.NewThreadRuntimeStore(conn).GetActiveByThread(ctx, fixture.orgID, fixture.threadID)
			require.NoError(t, err, "read the legacy runtime after changing only passive metadata")
			require.Equal(t, before, after, "passive metadata must not change any runtime model field")
			requireExecutionEpochLegacyJSON(t, before, after)

			tx, stamp := executionEpochLegacyTransaction(t, ctx, conn)
			defer func() { _ = tx.Rollback(ctx) }()
			store := db.NewThreadRuntimeStore(tx)
			accepted, err := store.MarkLiveWithLease(ctx, fixture.orgID, runtime.ID, runtime.LeaseToken, "synthetic-provider-handle", time.Minute)
			require.NoError(t, err, "legacy runtime start must accept unchanged metadata columns")
			require.True(t, accepted, "the original runtime lease must still authorize the legacy live transition")
			accepted, err = store.HeartbeatWithLease(ctx, fixture.orgID, runtime.ID, runtime.LeaseToken, 75*time.Second)
			require.NoError(t, err, "legacy runtime heartbeat must continue renewing the original lease")
			require.True(t, accepted, "the matching live runtime must still accept heartbeat")
			accepted, err = store.AdvanceDeliveryWithLease(ctx, fixture.orgID, runtime.ID, runtime.LeaseToken, 4, 3)
			require.NoError(t, err, "legacy delivery-watermark advancement must remain available")
			require.True(t, accepted, "the existing runtime lease must still authorize watermark advancement")
			want := before
			at := stamp.UTC()
			lease := at.Add(75 * time.Second)
			want.Status, want.RuntimeHandleID = models.ThreadRuntimeStatusLive, "synthetic-provider-handle"
			want.HeartbeatAt, want.LeaseExpiresAt, want.UpdatedAt = &at, &lease, at
			want.LastDeliveredSequence, want.LastAckedSequence = 4, 3
			actual, err := store.GetActiveByThread(ctx, fixture.orgID, fixture.threadID)
			require.NoError(t, err, "reload the full legacy runtime after its unchanged lease and delivery writes")
			require.Equal(t, want, actual, "legacy runtime writes must retain exact identity, model, timestamps and watermarks")
			requireExecutionEpochLegacyJSON(t, want, actual)
			prior := executionEpochLegacyRow(t, ctx, tx, "thread_runtimes", fixture.orgID, runtime.ID)
			wantStatus, wantReason, wantError := "closed", "synthetic-close", "synthetic-terminal-detail"
			if tt.reclaim {
				_, err = tx.Exec(ctx, `UPDATE thread_runtimes SET lease_expires_at=now()-interval '1 minute' WHERE org_id=$1 AND id=$2`, fixture.orgID, runtime.ID)
				require.NoError(t, err, "expire only this isolated runtime lease without waiting or changing production timeouts")
				result, reclaimErr := store.ReclaimExpiredLeases(ctx, stamp, 10)
				require.NoError(t, reclaimErr, "the existing reclaimer must accept nullable epoch metadata")
				require.Equal(t, db.ThreadRuntimeReclaimResult{LostRuntimes: 1}, result, "the isolated reclaimer must lose exactly its runtime without inventing holder or inbox work")
				wantStatus, wantReason, wantError = "lost", "lease_expired", "runtime lease expired before terminal close"
			} else {
				accepted, err = store.MarkTerminalWithLease(ctx, fixture.orgID, runtime.ID, runtime.LeaseToken, models.ThreadRuntimeStatusClosed, wantReason, wantError)
				require.NoError(t, err, "legacy terminal close must remain an exact-lease operation")
				require.True(t, accepted, "the live runtime's matching owner must still close it")
			}
			var status, reason, detail string
			var closedAt time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT status,closed_at,stop_reason,last_error FROM thread_runtimes WHERE org_id=$1 AND id=$2`, fixture.orgID, runtime.ID).Scan(&status, &closedAt, &reason, &detail), "read every terminal field from the actual legacy transition")
			require.Equal(t, []string{wantStatus, wantReason, wantError}, []string{status, reason, detail}, "the terminal or reclaim operation must preserve its exact preexisting outcome")
			require.True(t, closedAt.Equal(stamp), "terminal time must come from this transaction rather than a new proof clock")
			requireExecutionEpochLegacyUnchangedExcept(t, prior, executionEpochLegacyRow(t, ctx, tx, "thread_runtimes", fixture.orgID, runtime.ID), "status", "closed_at", "stop_reason", "last_error", "updated_at", "lease_expires_at")
			requireExecutionEpochLegacyMetadata(t, ctx, tx, "thread_runtimes", fixture.orgID, runtime.ID, tt.populated)
			require.NoError(t, tx.Commit(ctx), "commit the unchanged runtime lifecycle sequence")
			requireExecutionEpochLegacyMetadata(t, ctx, conn, "thread_runtimes", fixture.orgID, runtime.ID, tt.populated)
		})
	}
}

func TestSessionExecutionEpochLegacyExecutorPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		threadless, populated bool
		reclaim               bool
	}{
		{name: "threaded terminal null metadata"},
		{name: "threaded terminal populated metadata", populated: true},
		{name: "threadless terminal null metadata", threadless: true},
		{name: "threadless terminal populated metadata", threadless: true, populated: true},
		{name: "threaded reclaim null metadata", reclaim: true},
		{name: "threaded reclaim populated metadata", populated: true, reclaim: true},
		{name: "threadless reclaim null metadata", threadless: true, reclaim: true},
		{name: "threadless reclaim populated metadata", threadless: true, populated: true, reclaim: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, conn := newExecutionEpochLegacyDatabase(t)
			fixture := newExecutorWriterFixture(t, ctx, conn)
			params := fixture.params()
			if tt.threadless {
				params.ThreadID = nil
			}
			store := db.NewSessionExecutorStore(conn)
			id, err := store.CreateStarting(ctx, fixture.org, params)
			require.NoError(t, err, "allocate through the unchanged executor insert for the exact threaded or unthreaded scope")
			requireExecutionEpochLegacyMetadata(t, ctx, conn, "session_executors", fixture.org, id, false)
			before, err := store.GetByID(ctx, id)
			require.NoError(t, err, "read all legacy executor fields before seeding passive metadata")
			require.Equal(t, params.ThreadID, before.ThreadID, "legacy allocation must preserve an exact thread or SQL NULL lane")
			if tt.populated {
				seedExecutionEpochLegacyMetadata(t, ctx, conn, "session_executors", fixture.org, id)
			}
			after, err := store.GetByID(ctx, id)
			require.NoError(t, err, "reload the legacy executor after changing passive metadata only")
			require.Equal(t, before, after, "every field of the legacy executor model must ignore passive epoch columns")
			requireExecutionEpochLegacyJSON(t, before, after)
			tx, stamp := executionEpochLegacyTransaction(t, ctx, conn)
			defer func() { _ = tx.Rollback(ctx) }()
			actor := db.NewSessionExecutorStore(tx)
			accepted, err := actor.MarkRunningWithLease(ctx, fixture.org, id, fixture.token, time.Minute)
			require.NoError(t, err, "executor boot must accept the unchanged lease and scope")
			require.True(t, accepted, "the original executor owner must still transition starting to running")
			accepted, intent, cancelled, err := actor.HeartbeatWithLease(ctx, fixture.org, id, fixture.token, 75*time.Second)
			require.NoError(t, err, "executor heartbeat must preserve legacy lease behavior")
			require.Equal(t, []bool{true, false}, []bool{accepted, cancelled}, "heartbeat must accept this executor without inventing cancellation in either lane shape")
			require.Equal(t, models.DrainIntentNone, intent, "passive epochs must not create a drain request")
			accepted, err = actor.MarkDrainingWithLease(ctx, fixture.org, id, fixture.token)
			require.NoError(t, err, "legacy executor drain must remain available")
			require.True(t, accepted, "the running executor owner must still request draining")
			want := before
			lease := stamp.Add(75 * time.Second)
			want.Status, want.HeartbeatAt, want.LeaseExpiresAt, want.UpdatedAt = models.SessionExecutorStatusDraining, &stamp, &lease, stamp
			want.DrainIntent, want.DrainRequestedAt = models.DrainIntentHostMaintenance, &stamp
			actual, err := actor.GetByID(ctx, id)
			require.NoError(t, err, "read all legacy executor fields after boot heartbeat and drain")
			require.Equal(t, want, actual, "legacy executor transitions must preserve every unrelated identity, timestamp and metadata field")
			requireExecutionEpochLegacyJSON(t, want, actual)
			if tt.reclaim {
				past := stamp.Add(-time.Minute)
				_, err = tx.Exec(ctx, `UPDATE session_executors SET heartbeat_at=$3,lease_expires_at=$3 WHERE org_id=$1 AND id=$2`, fixture.org, id, past)
				require.NoError(t, err, "age only this synthetic executor for deterministic reclaim")
				_, err = tx.Exec(ctx, `UPDATE jobs SET lease_expires_at=$3 WHERE org_id=$1 AND id=$2`, fixture.org, fixture.job, past)
				require.NoError(t, err, "expire the matching synthetic job lease without changing production timeout constants")
				count, reclaimErr := actor.ReclaimLostForSession(ctx, fixture.org, fixture.session, stamp, 10)
				require.NoError(t, reclaimErr, "session-scoped executor recovery must accept the unchanged schema")
				require.Equal(t, int64(1), count, "reclaim must settle only the exact stale executor")
				detail := "executor heartbeat lost; queued for bounded recovery"
				want.Status, want.HeartbeatAt, want.LeaseExpiresAt, want.CompletedAt, want.LastError = models.SessionExecutorStatusLost, &past, &past, &stamp, &detail
				var jobStatus, owner string
				var token *uuid.UUID
				require.NoError(t, tx.QueryRow(ctx, `SELECT status,owner_kind,lock_token FROM jobs WHERE org_id=$1 AND id=$2`, fixture.org, fixture.job).Scan(&jobStatus, &owner, &token), "read the actual legacy requeue disposition")
				require.Equal(t, []string{"pending", "worker"}, []string{jobStatus, owner}, "the old reclaim path must still hand its job back to a worker")
				require.Nil(t, token, "legacy recovery must release the previous executor job token")
			} else {
				code, detail := 0, "synthetic-terminal-detail"
				accepted, err = actor.MarkTerminalWithLease(ctx, fixture.org, id, fixture.token, models.SessionExecutorStatusCompleted, &code, detail)
				require.NoError(t, err, "legacy executor terminal settlement must remain available")
				require.True(t, accepted, "the draining executor's exact lease must still authorize terminal settlement")
				want.Status, want.CompletedAt, want.ExitCode, want.LastError = models.SessionExecutorStatusCompleted, &stamp, &code, &detail
			}
			actual, err = actor.GetByID(ctx, id)
			require.NoError(t, err, "reload the complete terminal executor using its unchanged positional scanner")
			require.Equal(t, want, actual, "terminal and reclaim paths must retain the entire legacy executor contract")
			requireExecutionEpochLegacyJSON(t, want, actual)
			requireExecutionEpochLegacyMetadata(t, ctx, tx, "session_executors", fixture.org, id, tt.populated)
			require.NoError(t, tx.Commit(ctx), "commit the unchanged executor lifecycle sequence")
			requireExecutionEpochLegacyMetadata(t, ctx, conn, "session_executors", fixture.org, id, tt.populated)
		})
	}
}

func TestSessionExecutionEpochLegacyBatchPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, operation string
		populated       bool
	}{
		{name: "seed batch null metadata", operation: "seed"},
		{name: "seed batch populated metadata", operation: "seed", populated: true},
		{name: "acknowledged batch start null metadata", operation: "start"},
		{name: "acknowledged batch start preserves metadata", operation: "start", populated: true},
		{name: "acknowledged batch abandonment null metadata", operation: "abandon"},
		{name: "acknowledged batch abandonment preserves metadata", operation: "abandon", populated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, conn := newExecutionEpochLegacyDatabase(t)
			fixture := seedEpochMigrationFixture(t, ctx, conn)
			runtime := createExecutionEpochLegacyRuntime(t, ctx, conn, fixture)
			message := &models.SessionMessage{OrgID: fixture.orgID, SessionID: fixture.sessionID, ThreadID: &fixture.threadID, Role: models.MessageRoleUser, Content: "synthetic execution epoch compatibility input", TurnNumber: 1}
			require.NoError(t, db.NewSessionMessageStore(conn).Create(ctx, message), "persist an actual scoped user message for the unchanged inbox APIs")
			inbox := db.NewThreadInboxStore(conn)
			entry, err := inbox.AppendForMessage(ctx, fixture.orgID, db.AppendThreadInboxEntryParams{SessionID: fixture.sessionID, ThreadID: fixture.threadID, MessageID: message.ID, EntryType: models.ThreadInboxEntryTypeUserMessage})
			require.NoError(t, err, "append the actual message through the legacy inbox sequence allocator")
			phases := db.NewSessionActivityPhaseStore(conn)
			var batch models.ThreadInboxDeliveryBatch
			if tt.operation == "seed" {
				_, seed, seedErr := phases.StartSeedPhase(ctx, fixture.orgID, fixture.sessionID, fixture.threadID, 1, runtime.ID, runtime.LeaseToken, []int64{message.ID}, time.Now().UTC())
				require.NoError(t, seedErr, "the real seed transaction must create and start an omitted-column batch")
				require.NotNil(t, seed, "an exact persisted seed message must produce its durable batch")
				batch = *seed
				require.Equal(t, models.InboxDeliveryBatchStarted, batch.Status, "seed admission must preserve its existing immediately-started batch outcome")
			} else {
				entries, claimErr := inbox.ClaimDeliverableAfter(ctx, fixture.orgID, fixture.threadID, runtime.ID, runtime.LeaseToken, runtime.OwnerNodeID, 0, 10)
				require.NoError(t, claimErr, "claim the actual inbox message before the legacy acknowledgement path")
				ids := make([]uuid.UUID, len(entries))
				for i, claimed := range entries {
					ids[i] = claimed.ID
				}
				require.Equal(t, []uuid.UUID{entry.ID}, ids, "claim must select exactly the fixture's original inbox entry")
				count, deliveredErr := inbox.MarkDeliveredForEntry(ctx, fixture.orgID, fixture.threadID, runtime.ID, runtime.LeaseToken, runtime.OwnerNodeID, entry.ID, entry.SequenceNo)
				require.NoError(t, deliveredErr, "record delivery through the unchanged runtime lease fence")
				require.Equal(t, int64(1), count, "the original message must receive exactly one delivery receipt")
				batch, err = phases.AcknowledgeInboxBatch(ctx, fixture.orgID, fixture.sessionID, fixture.threadID, runtime.ID, runtime.LeaseToken, nil, entry.SequenceNo, time.Now().UTC())
				require.NoError(t, err, "the actual acknowledgement transaction must create an omitted-column batch")
			}
			requireExecutionEpochLegacyMetadata(t, ctx, conn, "thread_inbox_delivery_batches", fixture.orgID, batch.ID, false)
			before, err := phases.GetInboxDeliveryBatch(ctx, fixture.orgID, batch.ID)
			require.NoError(t, err, "read every legacy batch column through its unchanged named projection")
			require.Equal(t, canonicalExecutionEpochLegacyBatch(batch), canonicalExecutionEpochLegacyBatch(before), "batch creation and its legacy reader must return the same complete model after normalizing timestamp locations")
			if tt.populated {
				seedExecutionEpochLegacyMetadata(t, ctx, conn, "thread_inbox_delivery_batches", fixture.orgID, batch.ID)
			}
			after, err := phases.GetInboxDeliveryBatch(ctx, fixture.orgID, batch.ID)
			require.NoError(t, err, "reload the batch after changing passive fields only")
			require.Equal(t, before, after, "new passive fields must not leak into or change any batch model field")
			requireExecutionEpochLegacyJSON(t, before, after)
			if tt.operation != "seed" {
				tx, stamp := executionEpochLegacyTransaction(t, ctx, conn)
				defer func() { _ = tx.Rollback(ctx) }()
				actor := db.NewSessionActivityPhaseStore(tx)
				want := before
				want.UpdatedAt = stamp
				if tt.operation == "start" {
					_, err = actor.StartPhase(ctx, fixture.orgID, fixture.sessionID, fixture.threadID, 1, &runtime.ID, &runtime.LeaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInboxBatch, BatchID: &batch.ID}, stamp)
					require.NoError(t, err, "the legacy phase owner must still start its exact acknowledged batch")
					want.Status, want.StartedAt = models.InboxDeliveryBatchStarted, &stamp
				} else {
					_, err = actor.AbandonInboxBatch(ctx, fixture.orgID, batch.ID, stamp)
					require.NoError(t, err, "the legacy abandonment path must still settle its exact acknowledged batch")
					want.Status, want.AbandonedAt = models.InboxDeliveryBatchAbandoned, &stamp
				}
				actual, readErr := actor.GetInboxDeliveryBatch(ctx, fixture.orgID, batch.ID)
				require.NoError(t, readErr, "read the full batch after its unchanged start or abandonment write")
				require.Equal(t, want, actual, "batch transitions must preserve all old identities, sequences and timestamps exactly")
				requireExecutionEpochLegacyJSON(t, want, actual)
				requireExecutionEpochLegacyMetadata(t, ctx, tx, "thread_inbox_delivery_batches", fixture.orgID, batch.ID, tt.populated)
				require.NoError(t, tx.Commit(ctx), "commit the caller-owned legacy batch transition")
			}
			requireExecutionEpochLegacyMetadata(t, ctx, conn, "thread_inbox_delivery_batches", fixture.orgID, batch.ID, tt.populated)
		})
	}
}

func newExecutionEpochLegacyDatabase(t *testing.T) (context.Context, *pgx.Conn) {
	t.Helper()
	migration, conn := newContentMigrationPostgres(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, migration.Migrate(328), "exercise legacy APIs on the full schema through all three passive execution migrations")
	return ctx, conn
}

func createExecutionEpochLegacyRuntime(t *testing.T, ctx context.Context, conn *pgx.Conn, fixture epochMigrationFixture) models.ThreadRuntime {
	t.Helper()
	runtime, err := db.NewThreadRuntimeStore(conn).CreateStarting(ctx, fixture.orgID, db.CreateThreadRuntimeParams{
		SessionID: fixture.sessionID, ThreadID: fixture.threadID, SandboxID: uuid.New(), ContainerID: "synthetic-container",
		AgentType: models.DefaultDefaultAgentType, Model: "synthetic-model", OwnerNodeID: "synthetic-worker", LeaseToken: uuid.New(),
		BaseWorkspaceGeneration: 2, CurrentWorkspaceGeneration: 3, LeaseDuration: time.Minute,
	})
	require.NoError(t, err, "allocate through the unchanged runtime insert that omits all new metadata columns")
	return runtime
}

func executionEpochLegacyTransaction(t *testing.T, ctx context.Context, conn *pgx.Conn) (pgx.Tx, time.Time) {
	t.Helper()
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "begin an explicit test transaction to make legacy server timestamps deterministic")
	var stamp time.Time
	require.NoError(t, tx.QueryRow(ctx, "SELECT now()").Scan(&stamp), "capture the exact transaction timestamp used by unchanged legacy SQL")
	return tx, stamp
}

func seedExecutionEpochLegacyMetadata(t *testing.T, ctx context.Context, database db.DBTX, table string, orgID, id uuid.UUID) {
	t.Helper()
	proof := ""
	if table == "thread_runtimes" {
		proof = `,drain_proof='{"synthetic":true,"receipt":"opaque-not-authority"}'::jsonb`
	}
	_, err := database.Exec(ctx, fmt.Sprintf(`UPDATE %s SET admission_session_epoch=$3,admission_thread_epoch=0%s WHERE org_id=$1 AND id=$2`, table, proof), orgID, id, int64(1<<62)+7)
	require.NoError(t, err, "seed synthetic passive metadata without changing any legacy lifecycle field")
}

func requireExecutionEpochLegacyMetadata(t *testing.T, ctx context.Context, database db.DBTX, table string, orgID, id uuid.UUID, populated bool) {
	t.Helper()
	proof := "NULL::jsonb"
	if table == "thread_runtimes" {
		proof = "drain_proof"
	}
	var sessionEpoch, threadEpoch *int64
	var actualProof json.RawMessage
	err := database.QueryRow(ctx, fmt.Sprintf(`SELECT admission_session_epoch,admission_thread_epoch,%s FROM %s WHERE org_id=$1 AND id=$2`, proof, table), orgID, id).Scan(&sessionEpoch, &threadEpoch, &actualProof)
	require.NoError(t, err, "read every passive metadata column independently of legacy model projections")
	var wantSession, wantThread *int64
	if populated {
		session, thread := int64(1<<62)+7, int64(0)
		wantSession, wantThread = &session, &thread
	}
	require.Equal(t, wantSession, sessionEpoch, "legacy writes must preserve the exact nullable session bigint without float64 precision loss")
	require.Equal(t, wantThread, threadEpoch, "legacy writes must preserve SQL NULL separately from the exact present zero thread epoch")
	if populated && table == "thread_runtimes" {
		require.NotNil(t, actualProof, "populated raw drain metadata must survive legacy writes")
		require.JSONEq(t, `{"synthetic":true,"receipt":"opaque-not-authority"}`, string(actualProof), "legacy writes must preserve the raw opaque drain object without interpreting it")
	} else {
		require.Nil(t, actualProof, "legacy terminal and expired-lease observations must retain SQL NULL proof rather than manufacture even a JSON null receipt")
	}
}

func requireExecutionEpochLegacyJSON(t *testing.T, want, actual any) {
	t.Helper()
	// The unchanged seed API returns its supplied UTC timestamp while pgx batch
	// reads can use time.Local. Compare the full logical model without changing
	// that preexisting response formatting policy in this passive schema slice.
	if batch, ok := want.(models.ThreadInboxDeliveryBatch); ok {
		want = canonicalExecutionEpochLegacyBatch(batch)
	}
	if batch, ok := actual.(models.ThreadInboxDeliveryBatch); ok {
		actual = canonicalExecutionEpochLegacyBatch(batch)
	}
	wantJSON, err := json.Marshal(want)
	require.NoError(t, err, "serialize the complete expected legacy public model")
	actualJSON, err := json.Marshal(actual)
	require.NoError(t, err, "serialize the complete actual legacy public model")
	require.JSONEq(t, string(wantJSON), string(actualJSON), "passive columns must leave the entire legacy JSON response unchanged")
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(actualJSON, &fields), "inspect the legacy public response field names")
	// One immutable serialized value is checked sequentially; no shared test state
	// or database connection is accessed by parallel child assertions.
	for _, field := range []string{"admission_session_epoch", "admission_thread_epoch", "drain_proof"} {
		require.NotContains(t, fields, field, "passive execution metadata must not become a public model field")
	}
}

func canonicalExecutionEpochLegacyBatch(batch models.ThreadInboxDeliveryBatch) models.ThreadInboxDeliveryBatch {
	batch.AcknowledgedAt, batch.CreatedAt, batch.UpdatedAt = batch.AcknowledgedAt.UTC(), batch.CreatedAt.UTC(), batch.UpdatedAt.UTC()
	if batch.StartedAt != nil {
		stamp := batch.StartedAt.UTC()
		batch.StartedAt = &stamp
	}
	if batch.AbandonedAt != nil {
		stamp := batch.AbandonedAt.UTC()
		batch.AbandonedAt = &stamp
	}
	return batch
}

func executionEpochLegacyRow(t *testing.T, ctx context.Context, database db.DBTX, table string, orgID, id uuid.UUID) map[string]json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	err := database.QueryRow(ctx, fmt.Sprintf(`SELECT to_jsonb(row) FROM %s row WHERE org_id=$1 AND id=$2`, table), orgID, id).Scan(&raw)
	require.NoError(t, err, "capture every legacy and passive field independently of model scanners")
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields), "decode the full persisted runtime row for exact preservation checks")
	return fields
}

func requireExecutionEpochLegacyUnchangedExcept(t *testing.T, before, after map[string]json.RawMessage, changed ...string) {
	t.Helper()
	// These maps belong to one test case; filtering explicitly listed legacy
	// transition fields must not discard passive metadata or unrelated columns.
	for _, field := range changed {
		delete(before, field)
		delete(after, field)
	}
	require.Equal(t, before, after, "terminal or reclaim writes must preserve every field outside their explicit legacy mutation set")
}
