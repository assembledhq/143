package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestPullRequestHealthSweepCheckpointResumePostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, _, _ := newSchedulingPostgres(t)
	ctx := context.Background()
	jobID, token := uuid.New(), uuid.New()
	original := map[string]any{
		"org_id": orgID.String(), "limit": 50,
		"unrelated": map[string]any{"retained": true},
	}
	originalJSON, err := json.Marshal(original)
	require.NoError(t, err, "encode an existing periodic job payload")
	runAt := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	expiredAt := runAt.Add(-time.Hour)
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token,attempts,run_at,lease_expires_at,last_error)
		VALUES($1,$2,'default','reconcile_pull_request_state',$3,'running',$4,2,$5,$6,'retained error')`, jobID, orgID, originalJSON, token, runAt, expiredAt)
	require.NoError(t, err, "seed an owned periodic job with unrelated payload and queue state")
	store := NewJobStore(pool)
	missing, err := store.GetPullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, token)
	require.NoError(t, err, "a valid owned job may have no selection yet")
	require.Nil(t, missing, "only an absent health_sweep key permits initial candidate discovery")

	initial := newPullRequestHealthSweepCheckpoint()
	require.NoError(t, store.SavePullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, token, initial), "current fencing token may save even before an expired lease has been reclaimed")
	got, err := store.GetPullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, token)
	require.NoError(t, err, "reload the frozen selection from PostgreSQL JSONB")
	require.Equal(t, &initial, got, "reload must retain the exact ordered remainder and admission metadata")
	var unrelatedJSON []byte
	var state struct {
		Status     string
		Token      uuid.UUID
		Attempts   int
		RunAt      time.Time
		LeaseUntil time.Time
		LastError  string
	}
	err = pool.QueryRow(ctx, `SELECT payload - 'health_sweep',status,lock_token,attempts,run_at,lease_expires_at,last_error
		FROM jobs WHERE org_id=$1 AND id=$2`, orgID, jobID).
		Scan(&unrelatedJSON, &state.Status, &state.Token, &state.Attempts, &state.RunAt, &state.LeaseUntil, &state.LastError)
	require.NoError(t, err, "read unrelated payload and all mutable queue ownership fields together")
	require.JSONEq(t, string(originalJSON), string(unrelatedJSON), "checkpoint updates must preserve the entire unrelated payload")
	state.RunAt, state.LeaseUntil = state.RunAt.UTC(), state.LeaseUntil.UTC()
	require.Equal(t, struct {
		Status     string
		Token      uuid.UUID
		Attempts   int
		RunAt      time.Time
		LeaseUntil time.Time
		LastError  string
	}{"running", token, 2, runAt, expiredAt, "retained error"}, state, "checkpoint updates must preserve status, attempts, token, scheduling, lease, and error state")

	remaining := initial
	remaining.RemainingPullRequestIDs = []uuid.UUID{initial.RemainingPullRequestIDs[1]}
	require.NoError(t, store.SavePullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, token, remaining), "save only the unfinished selected pull request")
	owned, err := store.RetryWithLease(ctx, jobID, token, "retry unfinished selection", runAt.Add(time.Minute))
	require.NoError(t, err, "requeue through the ordinary worker retry transition")
	require.True(t, owned, "current worker should own the retry transition")
	_, err = store.GetPullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, token)
	require.ErrorIs(t, err, ErrPullRequestHealthSweepLeaseLost, "pending jobs must not permit the former owner to resume reads")
	newToken := uuid.New()
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$3,attempts=attempts+1 WHERE org_id=$1 AND id=$2`, orgID, jobID, newToken)
	require.NoError(t, err, "simulate a claim with a replacement fencing token")
	got, err = store.GetPullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, newToken)
	require.NoError(t, err, "replacement owner should resume saved work")
	require.Equal(t, &remaining, got, "reclaim must retain only unfinished selected identities and never discover a fresh batch")
	_, err = store.GetPullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, token)
	require.ErrorIs(t, err, ErrPullRequestHealthSweepLeaseLost, "replaced owner cannot read a usable checkpoint")
	require.ErrorIs(t, store.SavePullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, token, initial), ErrPullRequestHealthSweepLeaseLost, "replaced owner cannot expand the durable remainder back to its old selection")
	completed := remaining
	completed.RemainingPullRequestIDs = []uuid.UUID{}
	require.ErrorIs(t, store.SavePullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, token, completed), ErrPullRequestHealthSweepLeaseLost, "replaced owner cannot overwrite replacement progress with completion")
	require.NoError(t, store.SavePullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, newToken, completed), "replacement owner should durably complete the frozen selection")
	got, err = store.GetPullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, newToken)
	require.NoError(t, err, "reload an explicit completed selection")
	require.Equal(t, &completed, got, "a present empty remainder must survive JSONB round trips and remain distinct from an absent checkpoint")
}

func TestPullRequestHealthSweepCheckpointOwnershipPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		foreignOrg bool
		wrongToken bool
		status     string
		queue      string
		jobType    string
	}{
		{name: "foreign tenant", foreignOrg: true},
		{name: "replacement fencing token", wrongToken: true},
		{name: "pending job", status: "pending"},
		{name: "succeeded job", status: "succeeded"},
		{name: "dead letter job", status: "dead_letter"},
		{name: "other queue", queue: "agent"},
		{name: "other job type", jobType: "sync_pull_request_state"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, orgID, _, _ := newSchedulingPostgres(t)
			ctx := context.Background()
			jobID, token := uuid.New(), uuid.New()
			status, queue, jobType := tt.status, tt.queue, tt.jobType
			if status == "" {
				status = "running"
			}
			if queue == "" {
				queue = "default"
			}
			if jobType == "" {
				jobType = "reconcile_pull_request_state"
			}
			initial := newPullRequestHealthSweepCheckpoint()
			payloadJSON, err := json.Marshal(map[string]any{"health_sweep": initial, "retained": "payload"})
			require.NoError(t, err, "encode a saved selection for the ownership boundary")
			_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token) VALUES($1,$2,$3,$4,$5,$6,$7)`, jobID, orgID, queue, jobType, payloadJSON, status, token)
			require.NoError(t, err, "seed a job outside one ownership condition")
			claimedOrg, claimedToken := orgID, token
			if tt.foreignOrg {
				claimedOrg = uuid.New()
			}
			if tt.wrongToken {
				claimedToken = uuid.New()
			}
			store := NewJobStore(pool)
			got, err := store.GetPullRequestHealthSweepCheckpointWithLease(ctx, claimedOrg, jobID, claimedToken)
			require.ErrorIs(t, err, ErrPullRequestHealthSweepLeaseLost, "every ownership predicate must fence checkpoint reads")
			require.Nil(t, got, "unowned jobs must not expose a usable selection")
			completed := initial
			completed.RemainingPullRequestIDs = []uuid.UUID{}
			require.ErrorIs(t, store.SavePullRequestHealthSweepCheckpointWithLease(ctx, claimedOrg, jobID, claimedToken, completed), ErrPullRequestHealthSweepLeaseLost, "every ownership predicate must fence checkpoint writes")
			var retainedJSON []byte
			err = pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, orgID, jobID).Scan(&retainedJSON)
			require.NoError(t, err, "inspect saved work through the original tenant")
			require.JSONEq(t, string(payloadJSON), string(retainedJSON), "rejected checkpoint writes must preserve all saved work")
		})
	}
}

func TestPullRequestHealthSweepCheckpointReclaimRacePostgres(t *testing.T) {
	t.Parallel()
	pool, orgID, _, _ := newSchedulingPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jobID, token, newToken := uuid.New(), uuid.New(), uuid.New()
	initial := newPullRequestHealthSweepCheckpoint()
	payloadJSON, err := json.Marshal(map[string]any{"health_sweep": initial})
	require.NoError(t, err, "encode work before the owner replacement race")
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token) VALUES($1,$2,'default','reconcile_pull_request_state',$3,'running',$4)`, jobID, orgID, payloadJSON, token)
	require.NoError(t, err, "seed the running owner")
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err, "hold the claim update open while the stale worker attempts progress")
	defer func() { _ = tx.Rollback(ctx) }()
	remaining := initial
	remaining.RemainingPullRequestIDs = []uuid.UUID{initial.RemainingPullRequestIDs[1]}
	remainingJSON, err := json.Marshal(remaining)
	require.NoError(t, err, "encode replacement progress")
	_, err = tx.Exec(ctx, `UPDATE jobs SET lock_token=$3,payload=jsonb_set(payload,'{health_sweep}',$4::jsonb) WHERE org_id=$1 AND id=$2`, orgID, jobID, newToken, remainingJSON)
	require.NoError(t, err, "replace ownership and save a smaller remainder under the row lock")
	staleConnection, err := pool.Acquire(ctx)
	require.NoError(t, err, "reserve a dedicated connection for the stale writer")
	defer staleConnection.Release()
	stalePID := staleConnection.Conn().PgConn().PID()
	replacementPID := tx.Conn().PgConn().PID()
	staleWrite := make(chan error, 1)
	go func() {
		staleWrite <- NewJobStore(staleConnection).SavePullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, token, initial)
	}()
	require.Eventually(t, func() bool {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT $1::integer = ANY(pg_blocking_pids($2::integer))`, replacementPID, stalePID).Scan(&blocked)
		return err == nil && blocked
	}, 3*time.Second, 10*time.Millisecond, "stale update must be waiting on the replacement owner's row lock before commit")
	require.NoError(t, tx.Commit(ctx), "commit replacement ownership before allowing the stale update to resolve")
	require.ErrorIs(t, <-staleWrite, ErrPullRequestHealthSweepLeaseLost, "a blocked stale update must recheck the replacement token after the row lock releases")
	got, err := NewJobStore(pool).GetPullRequestHealthSweepCheckpointWithLease(ctx, orgID, jobID, newToken)
	require.NoError(t, err, "replacement owner should retain a usable checkpoint after the race")
	require.Equal(t, &remaining, got, "stale progress cannot expand or overwrite the replacement owner's remaining work")
}
