package cluster

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

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
			err = conn.QueryRow(ctx, `SELECT status,drain_intent,drain_requested_by,drain_reason,drain_requested_at='2026-09-25T19:00:00Z' AND drain_budget_expires_at='2026-09-25T20:00:00Z' FROM nodes WHERE id='old-generation'`).Scan(&status, &intent, &requestedBy, &reason, &preservedTimes)
			require.NoError(t, err, "read recovered generation")
			require.Equal(t, []string{tt.expected, tt.intent, "deploy", "rollout"}, []string{status, intent, requestedBy, reason}, "recovery must preserve operator drain intent and attribution")
			require.True(t, preservedTimes, "recovery must not reset drain timing")
			fresh := NewNodeManager(conn, zerolog.Nop(), "new-generation", "worker")
			require.NoError(t, fresh.Register(ctx, "worker-host"), "register replacement on same host")
			err = conn.QueryRow(ctx, `SELECT status,drain_intent FROM nodes WHERE id='new-generation'`).Scan(&status, &intent)
			require.NoError(t, err, "read replacement generation")
			require.Equal(t, []string{"active", "none"}, []string{status, intent}, "draining an old generation must not drain its replacement")
		})
	}
}
