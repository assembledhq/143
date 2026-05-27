package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type recoveryNodeStoreStub struct {
	markDeadFn func(ctx context.Context, staleBefore time.Time) (int64, error)
}

func (s *recoveryNodeStoreStub) MarkStaleNodesDead(ctx context.Context, staleBefore time.Time) (int64, error) {
	return s.markDeadFn(ctx, staleBefore)
}

type recoveryJobStoreStub struct {
	reclaimFn func(ctx context.Context, staleBefore time.Time, limit int) (int64, error)
}

func (s *recoveryJobStoreStub) ReclaimLostRunningJobs(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
	return s.reclaimFn(ctx, staleBefore, limit)
}

type recoverySessionExecutorStoreStub struct {
	calls     int
	reclaimFn func(ctx context.Context, staleBefore time.Time, limit int) (int64, error)
}

func (s *recoverySessionExecutorStoreStub) ReclaimLost(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
	s.calls++
	return s.reclaimFn(ctx, staleBefore, limit)
}

type recoveryPreviewRuntimeStoreStub struct {
	calls  int
	markFn func(ctx context.Context, cutoff time.Time, reason string) (int64, error)
}

func (s *recoveryPreviewRuntimeStoreStub) MarkExpiredPreviewRuntimesLost(ctx context.Context, cutoff time.Time, reason string) (int64, error) {
	s.calls++
	return s.markFn(ctx, cutoff, reason)
}

func TestRecoveryLoop_RunOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		nodes     *recoveryNodeStoreStub
		jobs      *recoveryJobStoreStub
		expectErr bool
	}{
		{
			name: "marks stale nodes dead and reclaims lost jobs",
			nodes: &recoveryNodeStoreStub{
				markDeadFn: func(ctx context.Context, staleBefore time.Time) (int64, error) {
					return 2, nil
				},
			},
			jobs: &recoveryJobStoreStub{
				reclaimFn: func(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
					if limit != 100 {
						return 0, errors.New("unexpected reclaim batch size")
					}
					return 3, nil
				},
			},
		},
		{
			name: "node marking failures bubble up",
			nodes: &recoveryNodeStoreStub{
				markDeadFn: func(ctx context.Context, staleBefore time.Time) (int64, error) {
					return 0, errors.New("update failed")
				},
			},
			jobs: &recoveryJobStoreStub{
				reclaimFn: func(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
					t.Fatal("reclaim should not run after node update failure")
					return 0, nil
				},
			},
			expectErr: true,
		},
		{
			name: "reclaim failures bubble up",
			nodes: &recoveryNodeStoreStub{
				markDeadFn: func(ctx context.Context, staleBefore time.Time) (int64, error) {
					return 1, nil
				},
			},
			jobs: &recoveryJobStoreStub{
				reclaimFn: func(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
					return 0, errors.New("reclaim failed")
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			loop := NewRecoveryLoop(tt.nodes, tt.jobs, zerolog.Nop(), 90*time.Second, 100)
			err := loop.runOnce(context.Background(), time.Now())
			if tt.expectErr {
				require.Error(t, err, "runOnce should return an error")
				return
			}
			require.NoError(t, err, "runOnce should not return an error")
		})
	}
}

func TestRecoveryLoop_MarksExpiredPreviewRuntimesBeforeJobs(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	order := []string{}
	nodes := &recoveryNodeStoreStub{
		markDeadFn: func(ctx context.Context, staleBefore time.Time) (int64, error) {
			order = append(order, "nodes")
			return 1, nil
		},
	}
	previews := &recoveryPreviewRuntimeStoreStub{
		markFn: func(ctx context.Context, cutoff time.Time, reason string) (int64, error) {
			order = append(order, "previews")
			require.Equal(t, now, cutoff, "preview runtime sweep should use the current recovery time")
			require.NotEmpty(t, reason, "preview runtime sweep should persist a recovery reason")
			return 2, nil
		},
	}
	jobs := &recoveryJobStoreStub{
		reclaimFn: func(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
			order = append(order, "jobs")
			return 3, nil
		},
	}
	loop := NewRecoveryLoop(nodes, jobs, zerolog.Nop(), 90*time.Second, 100)
	loop.SetPreviewRuntimes(previews)

	err := loop.runOnce(context.Background(), now)

	require.NoError(t, err, "runOnce should not return an error")
	require.Equal(t, []string{"nodes", "previews", "jobs"}, order, "preview runtime recovery should run before job reclaim")
	require.Equal(t, 1, previews.calls, "preview runtime recovery should run once")
}

func TestRecoveryLoop_ReclaimsLostSessionExecutorsBeforeJobs(t *testing.T) {
	t.Parallel()

	order := []string{}
	nodes := &recoveryNodeStoreStub{
		markDeadFn: func(ctx context.Context, staleBefore time.Time) (int64, error) {
			order = append(order, "nodes")
			return 1, nil
		},
	}
	executors := &recoverySessionExecutorStoreStub{
		reclaimFn: func(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
			order = append(order, "executors")
			require.Equal(t, 100, limit, "executor reclaim should use the recovery batch size")
			return 2, nil
		},
	}
	jobs := &recoveryJobStoreStub{
		reclaimFn: func(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
			order = append(order, "jobs")
			return 3, nil
		},
	}

	loop := NewRecoveryLoop(nodes, jobs, zerolog.Nop(), 90*time.Second, 100)
	loop.SetSessionExecutors(executors)

	err := loop.runOnce(context.Background(), time.Now())
	require.NoError(t, err, "runOnce should reclaim stale executors")
	require.Equal(t, []string{"nodes", "executors", "jobs"}, order, "executor reclaim should run before generic job reclaim")
	require.Equal(t, 1, executors.calls, "runOnce should invoke executor reclaim once")
}

func TestRecoveryLoop_Start_StopsOnCancelAfterTick(t *testing.T) {
	t.Parallel()

	ticked := make(chan struct{}, 1)
	nodes := &recoveryNodeStoreStub{
		markDeadFn: func(ctx context.Context, staleBefore time.Time) (int64, error) {
			ticked <- struct{}{}
			return 1, nil
		},
	}
	jobs := &recoveryJobStoreStub{
		reclaimFn: func(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
			return 1, nil
		},
	}

	loop := NewRecoveryLoop(nodes, jobs, zerolog.Nop(), 90*time.Second, 100)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		loop.Start(ctx, 5*time.Millisecond)
		close(done)
	}()

	select {
	case <-ticked:
		cancel()
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Start should execute runOnce on the configured interval")
	}

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Start should return after the context is cancelled")
	}
}
