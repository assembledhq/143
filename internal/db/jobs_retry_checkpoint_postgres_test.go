package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestJobRetryCheckpointWindowPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                        string
		reset, wrongLease, terminal bool
	}{
		{name: "checkpoint resets atomically", reset: true},
		{name: "ordinary retry retains deadline"},
		{name: "lost lease cannot reset", reset: true, wrongLease: true},
		{name: "terminal job cannot reset", reset: true, terminal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, org, _, _ := newSchedulingPostgres(t)
			ctx := context.Background()
			_, err := pool.Exec(ctx, `ALTER TABLE jobs ADD COLUMN retry_window_started_at timestamptz`)
			require.NoError(t, err, "add persisted dependency window to isolated fixture")
			id, token := uuid.New(), uuid.New()
			oldStart := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
			runAt := oldStart.Add(3 * time.Hour)
			status := "running"
			if tt.terminal {
				status = "succeeded"
			}
			_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lock_token,attempts,retry_window_started_at,run_at) VALUES($1,$2,'agent','run_code_review','{}',$3,$4,2,$5,$5)`, id, org, status, token, oldStart)
			require.NoError(t, err, "seed an old dependency window")
			claimedToken := token
			if tt.wrongLease {
				claimedToken = uuid.New()
			}
			store := NewJobStore(pool)
			var ok bool
			if tt.reset {
				ok, err = store.RetryWithoutConsumingAttemptWithLeaseAndResetWindow(ctx, id, claimedToken, "checkpoint", runAt)
			} else {
				ok, err = store.RetryWithoutConsumingAttemptWithLease(ctx, id, claimedToken, "checkpoint", runAt)
			}
			require.NoError(t, err, "fenced requeue should complete")
			owned := !tt.wrongLease && !tt.terminal
			require.Equal(t, owned, ok, "only current running owner can modify the retry window")
			type state struct {
				Status   string
				Attempts int
				Start    *time.Time
				Token    *uuid.UUID
				RunAt    time.Time
			}
			var got state
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts,retry_window_started_at,lock_token,run_at FROM jobs WHERE id=$1 AND org_id=$2`, id, org).Scan(&got.Status, &got.Attempts, &got.Start, &got.Token, &got.RunAt), "read all checkpoint effects together")
			got.RunAt = got.RunAt.UTC()
			if got.Start != nil {
				utc := got.Start.UTC()
				got.Start = &utc
			}
			want := state{Status: status, Attempts: 2, Start: &oldStart, Token: &token, RunAt: oldStart}
			if owned {
				want.Status, want.Attempts, want.Token, want.RunAt = "pending", 1, nil, runAt
				if tt.reset {
					want.Start = nil
				}
			}
			require.Equal(t, want, got, "requeue and reset must be atomic and preserve unrelated job state")
			if !owned {
				return
			}
			// Reclaim with a new fencing token and establish the next window.
			newToken := uuid.New()
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$3,attempts=attempts+1 WHERE id=$1 AND org_id=$2`, id, org, newToken)
			require.NoError(t, err, "simulate claim under a new worker lease")
			start, owned, err := store.EnsureRetryWindowStartedAtWithLease(ctx, id, newToken, runAt)
			require.NoError(t, err, "persist next dependency window")
			require.True(t, owned, "new lease should establish retry window")
			wantStart := oldStart
			if tt.reset {
				wantStart = runAt
			}
			require.Equal(t, wantStart, start.UTC(), "only an explicit checkpoint should renew the dependency budget")
			again, owned, err := store.EnsureRetryWindowStartedAtWithLease(ctx, id, newToken, runAt.Add(time.Hour))
			require.NoError(t, err, "poll existing dependency episode")
			require.True(t, owned, "poll retains its current lease")
			require.Equal(t, start.UTC(), again.UTC(), "later polls must not extend the persisted deadline")
		})
	}
}
