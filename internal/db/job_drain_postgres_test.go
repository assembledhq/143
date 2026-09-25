package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestJobAdmissionHonorsDrainIntentPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, status, intent, targetIntent string
		pinned, expected                   bool
	}{
		{"healthy worker claims", "active", "none", "none", false, true},
		{"rollout intent blocks active worker", "active", "planned_rollout", "none", false, false},
		{"maintenance intent blocks active worker", "active", "host_maintenance", "none", false, false},
		{"draining status blocks worker", "draining", "none", "none", false, false},
		{"dead worker cannot claim", "dead", "none", "none", false, false},
		{"healthy pinned target retained", "active", "none", "none", true, false},
		{"rollout target allows failover despite active status", "active", "none", "planned_rollout", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, org, _, _ := newSchedulingPostgres(t)
			ctx := context.Background()
			_, err := pool.Exec(ctx, `CREATE TABLE nodes(id text PRIMARY KEY,mode text,host text,status text,drain_intent text,metadata jsonb,started_at timestamptz DEFAULT now(),last_heartbeat_at timestamptz DEFAULT now(),drain_requested_at timestamptz,drain_budget_expires_at timestamptz,drain_requested_by text DEFAULT '',drain_reason text DEFAULT ''); ALTER TABLE jobs ADD COLUMN target_node_id text, ADD COLUMN retry_window_started_at timestamptz`)
			require.NoError(t, err, "create admission and routing dependencies")
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode,host,status,drain_intent,metadata) VALUES ('claimer','worker','host',$1,$2,'{"max_active_sandboxes":10,"live_sandbox_count":0,"reserved_sandbox_count":0}'),('target','worker','host','active',$3,'{}')`, tt.status, tt.intent, tt.targetIntent)
			require.NoError(t, err, "seed independently recorded status and drain intent")
			jobID := uuid.New()
			var target *string
			if tt.pinned {
				value := "target"
				target = &value
			}
			_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,priority,target_node_id) VALUES($1,$2,'agent','run_agent','{}',0,$3)`, jobID, org, target)
			require.NoError(t, err, "enqueue pending job")
			store := NewJobStore(pool)
			job, err := store.ClaimNextRunnable(ctx, "claimer", "owner", uuid.New(), time.Minute)
			require.NoError(t, err, "claim job with durable drain admission")
			if tt.expected {
				require.NotNil(t, job, "healthy worker should claim eligible job")
				require.Equal(t, jobID, job.ID, "claim should return the enqueued job")
			} else {
				require.Nil(t, job, "draining workers and healthy remote targets must not admit this claim")
			}
			var actualStatus string
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id=$1 AND org_id=$2`, jobID, org).Scan(&actualStatus), "read persisted claim outcome")
			expectedStatus := "pending"
			if tt.expected {
				expectedStatus = "running"
			}
			require.Equal(t, expectedStatus, actualStatus, "rejected admission must leave job pending")

			eligible := tt.status == "active" && tt.intent == "none"
			healthy, err := store.IsHealthyWorkerNode(ctx, "claimer")
			require.NoError(t, err, "read placement eligibility")
			require.Equal(t, eligible, healthy, "placement must agree with drain admission")
			known, available, err := store.WorkerSandboxCapacity(ctx, "claimer")
			require.NoError(t, err, "read capacity eligibility")
			require.Equal(t, []bool{eligible, eligible}, []bool{known, available}, "drained workers must not advertise usable capacity")
			selected, err := store.SelectWorkerWithSandboxCapacity(ctx, "target")
			require.NoError(t, err, "select capacity outside the pinned target")
			var expectedNode *string
			if eligible {
				value := "claimer"
				expectedNode = &value
			}
			require.Equal(t, expectedNode, selected, "capacity routing must exclude drained generations")
			nodes, err := NewNodeStore(pool).ListActive(ctx)
			require.NoError(t, err, "list preview routing candidates")
			ids := []string{}
			for _, node := range nodes {
				ids = append(ids, node.ID)
			}
			expectedIDs := []string{}
			if eligible {
				expectedIDs = append(expectedIDs, "claimer")
			}
			if tt.targetIntent == "none" {
				expectedIDs = append(expectedIDs, "target")
			}
			require.Equal(t, expectedIDs, ids, "preview routing must exclude generations with drain intent")
			summary, err := store.SandboxCapacitySummary(ctx)
			require.NoError(t, err, "summarize eligible capacity")
			expectedSummary := SandboxCapacitySummary{FreshWorkers: len(expectedIDs)}
			if eligible {
				expectedSummary.WorkersWithSlots = 1
				expectedSummary.MaxSandboxes = 10
			}
			require.Equal(t, expectedSummary, summary, "prewarm admission must not count drained capacity")
		})
	}
}
