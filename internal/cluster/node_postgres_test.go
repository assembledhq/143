package cluster

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNodeDrainSurvivesRecoveryPostgres(t *testing.T) {
	t.Parallel()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for durable node drain proof")
	}
	tests := []struct {
		name, intent string
		register     bool
		expected     string
		shutdown     bool
	}{
		{"rollout survives heartbeat gap", "planned_rollout", false, "draining", false},
		{"maintenance survives heartbeat gap", "host_maintenance", false, "draining", false},
		{"rollout survives same generation restart", "planned_rollout", true, "draining", false},
		{"maintenance survives same generation restart", "host_maintenance", true, "draining", false},
		{"healthy generation recovers heartbeat", "none", false, "active", false},
		{"healthy generation can restart", "none", true, "active", false},
		{"ordinary process shutdown can restart same id", "none", true, "active", true},
		{"shutdown preserves operator rollout intent", "planned_rollout", true, "draining", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			conn, err := pgx.Connect(ctx, url)
			require.NoError(t, err, "connect to disposable PostgreSQL")
			defer func() { require.NoError(t, conn.Close(ctx), "close test connection") }()
			schema := "node_drain_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			_, err = conn.Exec(ctx, "CREATE SCHEMA "+schema)
			require.NoError(t, err, "create isolated schema")
			defer func() {
				_, err := conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
				require.NoError(t, err, "remove isolated schema")
			}()
			_, err = conn.Exec(ctx, "SET search_path TO "+schema)
			require.NoError(t, err, "use isolated schema")
			_, err = conn.Exec(ctx, `CREATE TABLE nodes(id text PRIMARY KEY,mode text,host text,started_at timestamptz,last_heartbeat_at timestamptz,status text,metadata jsonb,drain_intent text NOT NULL DEFAULT 'none',drain_requested_at timestamptz,drain_budget_expires_at timestamptz,drain_requested_by text DEFAULT '',drain_reason text DEFAULT '')`)
			require.NoError(t, err, "create node table")
			_, err = conn.Exec(ctx, `CREATE TABLE worker_deploy_events(deploy_id text,node_id text,host text,event_type text,drain_intent text,requested_by text,reason text,metadata jsonb)`)
			require.NoError(t, err, "create drain audit table")
			nm := NewNodeManager(conn, zerolog.Nop(), "old-generation", "worker")
			require.NoError(t, nm.Register(ctx, "worker-host"), "register worker generation")
			_, err = conn.Exec(ctx, `UPDATE nodes SET status='draining',drain_intent=$1,drain_requested_at='2026-09-25T19:00:00Z',drain_budget_expires_at='2026-09-25T20:00:00Z',drain_requested_by='deploy',drain_reason='rollout',last_heartbeat_at=now()-interval '5 minutes'`, tt.intent)
			require.NoError(t, err, "persist drain before missed heartbeats")
			if tt.shutdown {
				require.NoError(t, nm.RequestDrain(ctx, time.Now()), "drain process before shutdown")
			}
			count, err := nm.MarkStaleNodesDead(ctx, time.Now().Add(-time.Minute))
			require.NoError(t, err, "mark stale generation dead")
			require.Equal(t, int64(1), count, "exercise draining to dead transition")
			if tt.register {
				nm = NewNodeManager(conn, zerolog.Nop(), "old-generation", "worker")
				require.NoError(t, nm.Register(ctx, "worker-host"), "restart same generation")
			} else {
				require.NoError(t, nm.HeartbeatOnce(ctx), "recover heartbeat")
			}
			var status, intent, requestedBy, reason string
			var preservedTimes bool
			err = conn.QueryRow(ctx, `SELECT status,drain_intent,drain_requested_by,drain_reason,COALESCE(drain_requested_at='2026-09-25T19:00:00Z' AND drain_budget_expires_at='2026-09-25T20:00:00Z',false) FROM nodes WHERE id='old-generation'`).Scan(&status, &intent, &requestedBy, &reason, &preservedTimes)
			require.NoError(t, err, "read recovered generation")
			expectedBy, expectedReason := "deploy", "rollout"
			preserve := !(tt.register && tt.intent == "none")
			if !preserve {
				expectedBy, expectedReason = "", ""
			}
			require.Equal(t, []string{tt.expected, tt.intent, expectedBy, expectedReason}, []string{status, intent, requestedBy, reason}, "recovery must preserve operator drain intent and clear transient drain attribution")
			require.Equal(t, preserve, preservedTimes, "healthy process restart must clear obsolete drain times")
			if tt.intent != "none" {
				store := db.NewNodeStore(conn)
				params := db.ResumeNodeParams{NodeID: "old-generation", Reason: "replacement failed", RequestedBy: "operator"}
				_, err = conn.Exec(ctx, `ALTER TABLE worker_deploy_events ADD CONSTRAINT reject_event CHECK(false)`)
				require.NoError(t, err, "simulate audit storage failure")
				require.Error(t, store.ResumeOnRestart(ctx, params), "audit failure must roll back drain clearing")
				require.NoError(t, conn.QueryRow(ctx, `SELECT drain_intent FROM nodes WHERE id='old-generation'`).Scan(&intent), "read drain after audit failure")
				require.Equal(t, tt.intent, intent, "failed audit must preserve operator drain")
				_, err = conn.Exec(ctx, `ALTER TABLE worker_deploy_events DROP CONSTRAINT reject_event`)
				require.NoError(t, err, "restore audit writes")
				require.NoError(t, store.ResumeOnRestart(ctx, params), "authorize admission on the next restart")
				require.NoError(t, nm.HeartbeatOnce(ctx), "existing process remains draining after intent is cleared")
				require.NoError(t, conn.QueryRow(ctx, `SELECT status FROM nodes WHERE id='old-generation'`).Scan(&status), "read status before restart")
				require.Equal(t, "draining", status, "resume must not advertise a live latched process as admitting")
				restarted := NewNodeManager(conn, zerolog.Nop(), "old-generation", "worker")
				require.NoError(t, restarted.Register(ctx, "worker-host"), "restart explicitly resumed generation")
				require.NoError(t, conn.QueryRow(ctx, `SELECT status,drain_intent FROM nodes WHERE id='old-generation'`).Scan(&status, &intent), "read resumed admission")
				require.Equal(t, []string{"active", "none"}, []string{status, intent}, "resumed generation should admit only after restart")
				var eventCount int
				require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM worker_deploy_events WHERE node_id='old-generation' AND event_type='node_drain_cleared' AND requested_by='operator' AND reason='replacement failed' AND metadata='{"restart_required":true}'::jsonb`).Scan(&eventCount), "read exact resume audit receipt")
				require.Equal(t, 1, eventCount, "successful resume should have one attributed audit receipt")
				require.Error(t, store.ResumeOnRestart(ctx, params), "repeated resume must not record another drain clear")
			}
			fresh := NewNodeManager(conn, zerolog.Nop(), "new-generation", "worker")
			require.NoError(t, fresh.Register(ctx, "worker-host"), "register replacement on same host")
			err = conn.QueryRow(ctx, `SELECT status,drain_intent FROM nodes WHERE id='new-generation'`).Scan(&status, &intent)
			require.NoError(t, err, "read replacement generation")
			require.Equal(t, []string{"active", "none"}, []string{status, intent}, "draining an old generation must not drain its replacement")
		})
	}
}
