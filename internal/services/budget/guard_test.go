package budget

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeSummer is a controllable TokenSummer that records how many times it was
// queried so tests can assert the caching behavior.
type fakeSummer struct {
	mu    sync.Mutex
	total int64
	err   error
	calls int
	since []time.Time
}

func (f *fakeSummer) SumTokensSince(_ context.Context, since time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.since = append(f.since, since)
	return f.total, f.err
}

func (f *fakeSummer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestNew_DisabledReturnsNil(t *testing.T) {
	t.Parallel()

	if g := New(0, &fakeSummer{}); g != nil {
		t.Fatalf("New(0, summer) = %v, want nil (kill switch disabled)", g)
	}
	if g := New(-5, &fakeSummer{}); g != nil {
		t.Fatalf("New(negative, summer) = %v, want nil", g)
	}
	if g := New(1000, nil); g != nil {
		t.Fatalf("New(budget, nil) = %v, want nil (no summer)", g)
	}
}

func TestGuard_NilIsAllowAll(t *testing.T) {
	t.Parallel()

	var g *Guard
	decision, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("nil guard Check returned error: %v", err)
	}
	if !decision.Allowed {
		t.Fatal("nil guard should always allow")
	}
}

func TestGuard_AllowsUnderBudgetAndBlocksAtCeiling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		used        int64
		wantAllowed bool
	}{
		{name: "well under budget", used: 100, wantAllowed: true},
		{name: "just under budget", used: 999, wantAllowed: true},
		{name: "exactly at budget", used: 1000, wantAllowed: false},
		{name: "over budget", used: 5000, wantAllowed: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			summer := &fakeSummer{total: tt.used}
			g := New(1000, summer)
			decision, err := g.Check(context.Background())
			if err != nil {
				t.Fatalf("Check returned error: %v", err)
			}
			if decision.Allowed != tt.wantAllowed {
				t.Fatalf("Allowed = %v, want %v (used=%d budget=1000)", decision.Allowed, tt.wantAllowed, tt.used)
			}
			if decision.Used != tt.used {
				t.Fatalf("Used = %d, want %d", decision.Used, tt.used)
			}
			if decision.Budget != 1000 {
				t.Fatalf("Budget = %d, want 1000", decision.Budget)
			}
		})
	}
}

func TestGuard_FailsOpenOnSummerError(t *testing.T) {
	t.Parallel()

	summer := &fakeSummer{err: errors.New("db down")}
	g := New(1000, summer)

	decision, err := g.Check(context.Background())
	if err == nil {
		t.Fatal("expected error to surface for logging")
	}
	if !decision.Allowed {
		t.Fatal("must fail OPEN: a transient summer error must not block session creation")
	}
}

func TestGuard_CachesWithinTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	summer := &fakeSummer{total: 500}
	g := New(1000, summer)
	g.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if _, err := g.Check(context.Background()); err != nil {
			t.Fatalf("Check %d returned error: %v", i, err)
		}
	}
	if got := summer.callCount(); got != 1 {
		t.Fatalf("summer queried %d times, want 1 (cached within TTL)", got)
	}
}

func TestGuard_RefreshesAfterTTL(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	current := base
	summer := &fakeSummer{total: 500}
	g := New(1000, summer)
	g.now = func() time.Time { return current }

	if _, err := g.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Advance past the cache TTL; the next check must re-query.
	current = base.Add(defaultCacheTTL + time.Second)
	if _, err := g.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := summer.callCount(); got != 2 {
		t.Fatalf("summer queried %d times, want 2 (refresh after TTL)", got)
	}
}

func TestGuard_QueriesFromStartOfUTCDay(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 15, 30, 45, 0, time.UTC)
	summer := &fakeSummer{total: 0}
	g := New(1000, summer)
	g.now = func() time.Time { return now }

	if _, err := g.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantSince := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	if len(summer.since) != 1 || !summer.since[0].Equal(wantSince) {
		t.Fatalf("queried since %v, want start of UTC day %v", summer.since, wantSince)
	}
}

func TestGuard_CacheInvalidatedAtDayBoundary(t *testing.T) {
	t.Parallel()

	// A cached reading taken late on one UTC day must not be reused after
	// midnight, even if it is still within the TTL window, so the daily total
	// resets correctly.
	current := time.Date(2026, 6, 30, 23, 59, 30, 0, time.UTC)
	summer := &fakeSummer{total: 999}
	g := New(1000, summer)
	g.now = func() time.Time { return current }

	if _, err := g.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Cross midnight, still within defaultCacheTTL of the first reading.
	current = time.Date(2026, 7, 1, 0, 0, 15, 0, time.UTC)
	if _, err := g.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := summer.callCount(); got != 2 {
		t.Fatalf("summer queried %d times, want 2 (cache must reset at UTC midnight)", got)
	}
	if len(summer.since) == 2 {
		wantSince := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		if !summer.since[1].Equal(wantSince) {
			t.Fatalf("second query since %v, want %v (new day)", summer.since[1], wantSince)
		}
	}
}
