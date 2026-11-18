package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/cluster"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNodeLifecycleWriterVersionMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		upgrade bool
	}{
		{name: "fresh full schema"},
		{name: "existing nodes upgrade rollback and reapply", upgrade: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			seedLegacyNode := func() {
				_, err := conn.Exec(ctx, `INSERT INTO nodes(id,mode,host,status,metadata,started_at,last_heartbeat_at,drain_intent,drain_requested_at,drain_budget_expires_at,drain_requested_by,drain_reason)
					VALUES ('legacy','worker','worker.invalid','draining','{"synthetic":true}','2026-01-01','2026-01-01','planned_rollout','2026-01-01','2026-01-02','synthetic-operator','synthetic-reason')`)
				require.NoError(t, err, "legacy registration without the new column must remain valid")
			}
			var before json.RawMessage
			if tt.upgrade {
				require.NoError(t, migration.Migrate(316), "construct the complete existing schema before adding metadata")
				seedLegacyNode()
				require.NoError(t, conn.QueryRow(ctx, `SELECT to_jsonb(n) FROM nodes n WHERE id='legacy'`).Scan(&before), "capture every existing node field before upgrade")
			}
			require.NoError(t, migration.Migrate(317), "apply the real migration chain through passive writer metadata")
			if !tt.upgrade {
				seedLegacyNode()
				require.NoError(t, conn.QueryRow(ctx, `SELECT to_jsonb(n)-'lifecycle_writer_version' FROM nodes n WHERE id='legacy'`).Scan(&before), "capture the legacy row independently of the new column")
			}
			assertBoundary := func(wantVersion uint, hasMetadata bool) {
				version, dirty, err := migration.Version()
				require.NoError(t, err, "inspect the actual migration ledger")
				require.Equal(t, wantVersion, version, "each selected upgrade and rollback boundary must be exact")
				require.False(t, dirty, "successful metadata transitions must leave a clean ledger")
				var after json.RawMessage
				require.NoError(t, conn.QueryRow(ctx, `SELECT to_jsonb(n)-'lifecycle_writer_version' FROM nodes n WHERE id='legacy'`).Scan(&after), "read all legacy node data after schema change")
				require.JSONEq(t, string(before), string(after), "adding or dropping metadata must preserve every existing node field")
				var exists bool
				require.NoError(t, conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='nodes' AND column_name='lifecycle_writer_version')`).Scan(&exists), "inspect the metadata column at each boundary")
				require.Equal(t, hasMetadata, exists, "down must remove only the added metadata column")
				if hasMetadata {
					var actual int
					require.NoError(t, conn.QueryRow(ctx, `SELECT lifecycle_writer_version FROM nodes WHERE id='legacy'`).Scan(&actual), "read defaulted node writer metadata")
					require.Equal(t, 0, actual, "existing and omitted-column legacy inserts must default to version zero")
				}
			}
			assertBoundary(317, true)
			constraints := []struct {
				name, code string
				value      any
			}{
				{name: "negative version", value: -1, code: "23514"},
				{name: "null version", value: nil, code: "23502"},
			}
			// These statements intentionally share one isolated migration fixture;
			// each failed autocommit statement must leave the original row intact.
			for _, constraint := range constraints {
				_, err := conn.Exec(ctx, `UPDATE nodes SET lifecycle_writer_version=$1 WHERE id='legacy'`, constraint.value)
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, constraint.name+" must be rejected by the real database")
				require.Equal(t, constraint.code, pgErr.Code, constraint.name+" must fail for its intended constraint")
			}
			require.NoError(t, migration.Steps(-1), "remove only passive node metadata")
			assertBoundary(316, false)
			require.NoError(t, migration.Migrate(317), "reapply metadata without changing legacy data")
			assertBoundary(317, true)
		})
	}
}

func TestNodeManagerLifecycleWriterVersionFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, mode string
		heartbeat  bool
	}{
		{name: "api registration", mode: "api"},
		{name: "worker registration", mode: "worker"},
		{name: "combined registration", mode: "all"},
		{name: "api heartbeat", mode: "api", heartbeat: true},
		{name: "worker heartbeat", mode: "worker", heartbeat: true},
		{name: "combined heartbeat", mode: "all", heartbeat: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, false)
			require.NoError(t, migration.Migrate(317), "test actual registration against the full migrated schema")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			node := cluster.NewNodeManager(conn, zerolog.Nop(), "node-1", tt.mode)
			node.SetMetadataProvider(func() map[string]any {
				return map[string]any{"preview_capable": true, "preview_rpc_auth_check": true, "preview_internal_base_url": "http://worker.invalid"}
			})
			require.NoError(t, node.Register(ctx, "worker.invalid"), "register an actual node with explicit passive version zero")
			store := db.NewNodeStore(conn)
			registered, err := store.GetByID(ctx, "node-1")
			require.NoError(t, err, "read registration through the real NodeStore projection")
			require.Equal(t, 0, registered.LifecycleWriterVersion, "this binary must never claim floor compatibility")

			// A future nonnegative value is readable, but cannot be preserved as
			// this binary's advertised compatibility when it writes again.
			_, err = conn.Exec(ctx, `UPDATE nodes SET lifecycle_writer_version=7 WHERE id='node-1'`)
			require.NoError(t, err, "seed synthetic future metadata independently of this binary")
			expected := *registered
			expected.LifecycleWriterVersion = 7
			byID, err := store.GetByID(ctx, "node-1")
			require.NoError(t, err, "read an unknown future metadata version")
			require.Equal(t, expected, *byID, "read paths must preserve the complete future-version row without granting authority")
			active, err := store.ListActive(ctx)
			require.NoError(t, err, "passive metadata must not filter active nodes")
			require.Equal(t, []models.Node{expected}, active, "active-node selection must retain existing semantics")
			if tt.mode != "api" {
				latest, err := store.GetLatestByHost(ctx, "worker.invalid")
				require.NoError(t, err, "read the newest worker through the shared node columns")
				require.Equal(t, expected, *latest, "host lookup must include unknown future versions without changing selection")
				probes, err := store.ListPreviewRPCProbeNodes(ctx)
				require.NoError(t, err, "read preview RPC candidates with writer metadata")
				require.Equal(t, []models.Node{expected}, probes, "preview selection must not become a writer-version compatibility gate")
			}
			// Characterize omitted-column writes from a pre-metadata binary.
			// Defaults do not refresh reused IDs: a later floor needs stronger
			// build/instance/executor provenance, not just this observational row.
			_, err = conn.Exec(ctx, `INSERT INTO nodes(id,mode,host,metadata) VALUES ('node-1',$1,'worker.invalid','{}')
				ON CONFLICT(id) DO UPDATE SET last_heartbeat_at=now()`, tt.mode)
			require.NoError(t, err, "a legacy upsert must still work with the additive schema")
			_, err = conn.Exec(ctx, `UPDATE nodes SET last_heartbeat_at=now() WHERE id='node-1'`)
			require.NoError(t, err, "a legacy heartbeat must still work without knowing the metadata column")
			legacy, err := store.GetByID(ctx, "node-1")
			require.NoError(t, err, "inspect legacy reused-ID behavior")
			require.Equal(t, 7, legacy.LifecycleWriterVersion, "legacy writes can retain stale metadata and cannot establish a compatible floor")

			_, err = conn.Exec(ctx, `UPDATE nodes SET status='draining',drain_intent='planned_rollout',drain_requested_at='2026-01-01',drain_budget_expires_at='2026-01-02',drain_requested_by='synthetic-operator',drain_reason='synthetic-reason' WHERE id='node-1'`)
			require.NoError(t, err, "retain operator drain fields while refreshing passive metadata")
			var before json.RawMessage
			const preservedNodeFields = `SELECT to_jsonb(n)-'started_at'-'last_heartbeat_at'-'lifecycle_writer_version' FROM nodes n WHERE id='node-1'`
			require.NoError(t, conn.QueryRow(ctx, preservedNodeFields).Scan(&before), "capture every field registration and heartbeat must preserve during a durable drain")
			if tt.heartbeat {
				require.NoError(t, node.HeartbeatOnce(ctx), "heartbeat must reset stale metadata without losing durable drain intent")
			} else {
				require.NoError(t, node.Register(ctx, "worker.invalid"), "same-ID registration must reset stale metadata without clearing drain intent")
			}
			refreshed, err := store.GetByID(ctx, "node-1")
			require.NoError(t, err, "read the actual refreshed writer version")
			require.Equal(t, 0, refreshed.LifecycleWriterVersion, "current binaries must explicitly advertise only version zero on every refresh")
			var after json.RawMessage
			require.NoError(t, conn.QueryRow(ctx, preservedNodeFields).Scan(&after), "inspect retained node and drain data after metadata refresh")
			require.JSONEq(t, string(before), string(after), "metadata refresh must not change any unrelated node or drain field")
		})
	}
}
