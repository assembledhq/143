package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/automations"
)

type fakeTargetSweeper struct {
	orgs     []uuid.UUID
	listErr  error
	sweepErr error
	swept    []uuid.UUID
}

func (f *fakeTargetSweeper) ListOrgsWithWork(context.Context) ([]uuid.UUID, error) {
	return f.orgs, f.listErr
}

func (f *fakeTargetSweeper) Sweep(_ context.Context, orgID uuid.UUID) (automations.SweepReport, error) {
	f.swept = append(f.swept, orgID)
	return automations.SweepReport{}, f.sweepErr
}

type fakeSweepLock struct {
	acquired bool
	err      error
	releases int
}

func (l *fakeSweepLock) TryAcquire(context.Context) (bool, error) { return l.acquired, l.err }
func (l *fakeSweepLock) Release(context.Context) error {
	l.releases++
	return nil
}

func TestScheduler_SweepAutomationTargetsOnce(t *testing.T) {
	t.Parallel()
	orgA := uuid.New()
	orgB := uuid.New()
	tests := []struct {
		name         string
		sweeper      *fakeTargetSweeper
		lock         *fakeSweepLock
		wantSwept    []uuid.UUID
		wantReleases int
	}{
		{name: "every org with work is swept under the lock", sweeper: &fakeTargetSweeper{orgs: []uuid.UUID{orgA, orgB}}, lock: &fakeSweepLock{acquired: true}, wantSwept: []uuid.UUID{orgA, orgB}, wantReleases: 1},
		{name: "a sweep error does not stop the other orgs", sweeper: &fakeTargetSweeper{orgs: []uuid.UUID{orgA, orgB}, sweepErr: errors.New("boom")}, lock: &fakeSweepLock{acquired: true}, wantSwept: []uuid.UUID{orgA, orgB}, wantReleases: 1},
		{name: "another replica holding the lock skips the tick", sweeper: &fakeTargetSweeper{orgs: []uuid.UUID{orgA}}, lock: &fakeSweepLock{}, wantReleases: 0},
		{name: "a lock error skips the tick", sweeper: &fakeTargetSweeper{orgs: []uuid.UUID{orgA}}, lock: &fakeSweepLock{err: errors.New("db down")}, wantReleases: 0},
		{name: "a listing error releases the lock", sweeper: &fakeTargetSweeper{listErr: errors.New("boom")}, lock: &fakeSweepLock{acquired: true}, wantReleases: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &Scheduler{logger: zerolog.Nop()}
			s.SetAutomationTargetSweeps(tt.sweeper, tt.lock)
			s.sweepAutomationTargetsOnce(context.Background())
			require.Equal(t, tt.wantSwept, tt.sweeper.swept, "orgs swept")
			require.Equal(t, tt.wantReleases, tt.lock.releases, "lock releases")
		})
	}
}

func TestScheduler_StartAutomationTargetSweeps(t *testing.T) {
	t.Parallel()
	t.Run("returns at once without a sweeper", func(t *testing.T) {
		t.Parallel()
		s := &Scheduler{logger: zerolog.Nop()}
		done := make(chan struct{})
		go func() {
			s.StartAutomationTargetSweeps(context.Background(), time.Millisecond)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("the loop should not run without a sweeper")
		}
	})
	t.Run("sweeps on each tick until the context ends", func(t *testing.T) {
		t.Parallel()
		sweeper := &fakeTargetSweeper{orgs: []uuid.UUID{uuid.New()}}
		s := &Scheduler{logger: zerolog.Nop()}
		s.SetAutomationTargetSweeps(sweeper, &fakeSweepLock{acquired: true})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		s.StartAutomationTargetSweeps(ctx, 5*time.Millisecond)
		require.NotEmpty(t, sweeper.swept, "at least one tick swept")
	})
}
