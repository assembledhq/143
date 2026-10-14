package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPullRequestCheckSyncBurstPostgres(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name              string
		predecessorStatus string
		successorStatus   string
		seedSuccessor     bool
		wantNew           int
	}{
		{name: "pending first job retains deadline", predecessorStatus: "pending"},
		{name: "running job gets exactly one successor", predecessorStatus: "running", wantNew: 1},
		{name: "pending successor survives base completion", predecessorStatus: "succeeded", successorStatus: "pending", seedSuccessor: true},
		{name: "claimed successor waits for running predecessor", predecessorStatus: "running", successorStatus: "running", seedSuccessor: true},
		{name: "terminal predecessor permits new work", predecessorStatus: "succeeded", wantNew: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, _, prID := newSchedulingPostgres(t)
			ctx := context.Background()
			store := NewJobStore(pool)
			key := "sync_pull_request_state:" + prID.String() + ":checks"
			deadline := time.Date(2026, 1, 2, 10, 0, 15, 0, time.UTC)
			payload := map[string]string{"org_id": orgID.String(), "pull_request_id": prID.String(), "sync_reason": "incomplete_check_set"}
			opts := EnqueueOpts{Queue: "default", JobType: "sync_pull_request_state", Payload: payload, Priority: 6, DedupeKey: &key, RunAt: &deadline}
			first, err := store.EnqueuePullRequestCheckSync(ctx, orgID, prID, opts)
			require.NoError(t, err, "seed first durable check request")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status=$3 WHERE org_id=$1 AND id=$2`, orgID, first, "running")
			require.NoError(t, err, "simulate first authoritative read starting")
			var successor uuid.UUID
			if tt.seedSuccessor {
				successor, err = store.EnqueuePullRequestCheckSync(ctx, orgID, prID, opts)
				require.NoError(t, err, "seed one durable successor")
				_, err = pool.Exec(ctx, `UPDATE jobs SET status=$3 WHERE org_id=$1 AND id=$2`, orgID, successor, tt.successorStatus)
				require.NoError(t, err, "simulate successor claim state")
			}
			_, err = pool.Exec(ctx, `UPDATE jobs SET status=$3 WHERE org_id=$1 AND id=$2`, orgID, first, tt.predecessorStatus)
			require.NoError(t, err, "establish predecessor completion state")
			foreignOrg := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,status,dedupe_key,run_at) VALUES($1,'other','sync_pull_request_state',$2,'pending',$3,$4)`, foreignOrg, fmt.Sprintf(`{"pull_request_id":%q}`, prID.String()), key, deadline)
			require.NoError(t, err, "seed foreign tenant sentinel")
			const requests = 32
			ids, errs := make([]uuid.UUID, requests), make([]error, requests)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for index := range requests {
				wg.Go(func() {
					<-start
					later := deadline.Add(time.Duration(index+1) * time.Second)
					next := opts
					next.RunAt = &later
					ids[index], errs[index] = store.EnqueuePullRequestCheckSync(ctx, orgID, prID, next)
				})
			}
			close(start)
			wg.Wait()
			created := 0
			for index, id := range ids {
				require.NoError(t, errs[index], "concurrent event must leave or join durable work")
				if id != uuid.Nil {
					created++
					successor = id
				}
			}
			require.Equal(t, tt.wantNew, created, "a burst must create at most one pending successor")
			var firstDeadline time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT run_at FROM jobs WHERE org_id=$1 AND id=$2`, orgID, first).Scan(&firstDeadline), "read first bounded deadline")
			require.Equal(t, deadline, firstDeadline.UTC(), "later events cannot extend the first deadline")
			var active int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND queue='default' AND status IN ('pending','running')`, orgID).Scan(&active), "count the complete active lane")
			wantActive := 1
			if tt.predecessorStatus == "running" {
				wantActive = 2
			}
			require.Equal(t, wantActive, active, "completed base slots cannot produce extra active jobs")
			if tt.predecessorStatus == "running" {
				var predecessor string
				require.NoError(t, pool.QueryRow(ctx, `SELECT payload->>'after_job_id' FROM jobs WHERE org_id=$1 AND id=$2`, orgID, successor).Scan(&predecessor), "read successor dependency")
				require.Equal(t, first.String(), predecessor, "successor must wait for the exact active predecessor")
			}
			var foreignDeadline time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT run_at FROM jobs WHERE org_id=$1 AND dedupe_key=$2`, foreignOrg, key).Scan(&foreignDeadline), "inspect tenant sentinel")
			require.Equal(t, deadline, foreignDeadline.UTC(), "coalescing cannot modify another tenant's jobs")
		})
	}
}

func TestPullRequestCheckSyncCompletionRacePostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, _, prID := newSchedulingPostgres(t)
	ctx := context.Background()
	store := NewJobStore(pool)
	key := "sync_pull_request_state:" + prID.String() + ":checks"
	deadline := time.Date(2026, 1, 2, 10, 0, 15, 0, time.UTC)
	opts := EnqueueOpts{Queue: "default", JobType: "sync_pull_request_state", Payload: map[string]string{"pull_request_id": prID.String()}, DedupeKey: &key, RunAt: &deadline}
	first, err := store.EnqueuePullRequestCheckSync(ctx, orgID, prID, opts)
	require.NoError(t, err, "create initial lane job")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "hold pull request lock during predecessor completion")
	defer func() { _ = tx.Rollback(ctx) }()
	var locked uuid.UUID
	require.NoError(t, tx.QueryRow(ctx, `SELECT id FROM pull_requests WHERE org_id=$1 AND id=$2 FOR UPDATE`, orgID, prID).Scan(&locked), "serialize an incoming enqueue behind completion")
	result := make(chan error, 1)
	go func() {
		id, err := store.EnqueuePullRequestCheckSync(ctx, orgID, prID, opts)
		if err == nil && id == uuid.Nil {
			err = fmt.Errorf("event lost after predecessor completion")
		}
		result <- err
	}()
	_, err = tx.Exec(ctx, `UPDATE jobs SET status='succeeded' WHERE org_id=$1 AND id=$2`, orgID, first)
	require.NoError(t, err, "finish predecessor before incoming scheduler can read the lane")
	require.NoError(t, tx.Commit(ctx), "release completed predecessor atomically")
	require.NoError(t, <-result, "a completion race must enqueue uncovered work")
}
