package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// newFleetCapacityPostgres provisions an isolated schema with the nodes
// columns FleetSandboxCapacity reads and returns a pool bound to it. Skips
// without TEST_DATABASE_URL, which CI's backend-test job exports. pgxmock can
// only prove scanning; the host grouping and generation selection need a real
// planner.
func newFleetCapacityPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for the fleet capacity grouping proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	require.NoError(t, err, "connect disposable PostgreSQL")
	schema := "fleet_capacity_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated test schema")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "remove isolated schema")
		require.NoError(t, admin.Close(ctx), "close database connection")
	})
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err, "parse pool settings")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TABLE nodes (
			id                text        PRIMARY KEY,
			mode              text        NOT NULL DEFAULT 'all',
			host              text,
			started_at        timestamptz NOT NULL DEFAULT now(),
			last_heartbeat_at timestamptz NOT NULL DEFAULT now(),
			status            text        NOT NULL DEFAULT 'active',
			drain_intent      text        NOT NULL DEFAULT 'none',
			metadata          jsonb
		)`)
	require.NoError(t, err, "create nodes table")
	return pool
}

type fleetCapacityNode struct {
	id            string
	mode          string
	status        string
	drainIntent   string
	startedAgo    time.Duration
	heartbeatAgo  time.Duration
	hostID        string
	capacity      string
	live          string
	reserved      string
	liveCountErr  string
	omitHostField bool
}

func seedFleetCapacityNode(t *testing.T, pool *pgxpool.Pool, n fleetCapacityNode) {
	t.Helper()
	metadata := map[string]any{
		"max_active_sandboxes":     n.capacity,
		"live_sandbox_count":       n.live,
		"reserved_sandbox_count":   n.reserved,
		"live_sandbox_count_error": n.liveCountErr,
	}
	if !n.omitHostField {
		metadata["sandbox_host_id"] = n.hostID
	}
	mode := n.mode
	if mode == "" {
		mode = "worker"
	}
	status := n.status
	if status == "" {
		status = "active"
	}
	drainIntent := n.drainIntent
	if drainIntent == "" {
		drainIntent = "none"
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO nodes (id, mode, started_at, last_heartbeat_at, status, drain_intent, metadata)
		VALUES ($1, $2, now() - $3::interval, now() - $4::interval, $5, $6, $7)`,
		n.id, mode, n.startedAgo.String(), n.heartbeatAgo.String(), status, drainIntent, metadata)
	require.NoError(t, err, "seed node %s", n.id)
}

func TestJobStore_FleetSandboxCapacity_Postgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		nodes []fleetCapacityNode
		want  FleetSandboxCapacity
	}{
		{
			name: "host with overlapping generations counts the newest capacity once and merges occupancy",
			nodes: []fleetCapacityNode{
				{id: "host-a-old", hostID: "host-a", status: "draining", drainIntent: "drain", startedAgo: 2 * time.Hour, heartbeatAgo: 5 * time.Second, capacity: "2", live: "1", reserved: "1"},
				{id: "host-a-new", hostID: "host-a", startedAgo: 5 * time.Minute, heartbeatAgo: 5 * time.Second, capacity: "4", live: "1", reserved: "0"},
			},
			want: FleetSandboxCapacity{Workers: 1, MaxSandboxes: 4, LiveSandboxes: 1, ReservedSandboxes: 1},
		},
		{
			name: "host whose newest generation is draining contributes nothing even if an older generation is active",
			nodes: []fleetCapacityNode{
				{id: "host-b-old", hostID: "host-b", startedAgo: 2 * time.Hour, heartbeatAgo: 5 * time.Second, capacity: "8", live: "0", reserved: "0"},
				{id: "host-b-new", hostID: "host-b", status: "draining", drainIntent: "drain", startedAgo: 5 * time.Minute, heartbeatAgo: 5 * time.Second, capacity: "8", live: "0", reserved: "0"},
			},
			want: FleetSandboxCapacity{},
		},
		{
			name: "nodes without a host id are keyed by node id",
			nodes: []fleetCapacityNode{
				{id: "legacy-1", omitHostField: true, startedAgo: time.Hour, heartbeatAgo: 5 * time.Second, capacity: "3", live: "2", reserved: "0"},
				{id: "legacy-2", omitHostField: true, startedAgo: time.Hour, heartbeatAgo: 5 * time.Second, capacity: "3", live: "0", reserved: "1"},
			},
			want: FleetSandboxCapacity{Workers: 2, MaxSandboxes: 6, LiveSandboxes: 2, ReservedSandboxes: 1},
		},
		{
			name: "stale, erroring, zero-capacity, and non-worker nodes are excluded",
			nodes: []fleetCapacityNode{
				{id: "stale", hostID: "host-stale", startedAgo: time.Hour, heartbeatAgo: 10 * time.Minute, capacity: "4", live: "0", reserved: "0"},
				{id: "erroring", hostID: "host-err", startedAgo: time.Hour, heartbeatAgo: 5 * time.Second, capacity: "4", live: "0", reserved: "0", liveCountErr: "docker unavailable"},
				{id: "unsized", hostID: "host-unsized", startedAgo: time.Hour, heartbeatAgo: 5 * time.Second, capacity: "0", live: "0", reserved: "0"},
				{id: "app-only", mode: "app", hostID: "host-app", startedAgo: time.Hour, heartbeatAgo: 5 * time.Second, capacity: "4", live: "0", reserved: "0"},
				{id: "healthy", hostID: "host-ok", startedAgo: time.Hour, heartbeatAgo: 5 * time.Second, capacity: "2", live: "1", reserved: "0"},
			},
			want: FleetSandboxCapacity{Workers: 1, MaxSandboxes: 2, LiveSandboxes: 1, ReservedSandboxes: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := newFleetCapacityPostgres(t)
			for _, n := range tt.nodes {
				seedFleetCapacityNode(t, pool, n)
			}

			got, err := NewJobStore(pool).FleetSandboxCapacity(context.Background())
			require.NoError(t, err, "FleetSandboxCapacity should query the seeded fleet")
			require.Equal(t, tt.want, got, "FleetSandboxCapacity should aggregate per physical host")
		})
	}
}
