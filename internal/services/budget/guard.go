// Package budget provides a deployment-wide kill switch on LLM token spend.
//
// It exists for open-signup hosted deployments: anyone can create an account
// and start agents, so per-org concurrency caps bound how much any single org
// runs in parallel but not the aggregate bill across an unbounded number of
// orgs. Guard caps the total tokens consumed across the whole deployment per
// UTC day and refuses new work once that ceiling is hit. It is intentionally
// coarse — a blast-radius limiter, not an accounting or per-user quota system.
package budget

import (
	"context"
	"sync"
	"time"
)

// defaultCacheTTL is how long a daily-total reading is reused before the guard
// re-queries. The budget is a soft daily ceiling, so brief staleness only lets
// a small overshoot through — well worth keeping the sum query off the hot
// path of every session create.
const defaultCacheTTL = 60 * time.Second

// TokenSummer reports the total LLM tokens consumed across the deployment since
// a given instant. *db.SessionMessageStore satisfies this via SumTokensSince.
type TokenSummer interface {
	SumTokensSince(ctx context.Context, since time.Time) (int64, error)
}

// Decision is the outcome of a budget check.
type Decision struct {
	// Allowed reports whether a new session may start.
	Allowed bool
	// Used is the day's token total observed when the decision was made.
	Used int64
	// Budget is the configured per-UTC-day ceiling.
	Budget int64
}

// Guard enforces a global per-UTC-day token budget. The zero value is not
// usable; construct with New. A nil *Guard is valid and always allows, so
// callers can hold an optional guard and call Check unconditionally.
type Guard struct {
	budget   int64
	summer   TokenSummer
	cacheTTL time.Duration
	now      func() time.Time

	mu        sync.Mutex
	cachedSum int64
	cachedAt  time.Time
	haveCache bool
}

// New returns a Guard enforcing the given per-UTC-day token budget, or nil when
// the kill switch is disabled (budget <= 0) or no summer is wired. A nil return
// is intentional and safe: Check is a no-op on a nil *Guard.
func New(budget int64, summer TokenSummer) *Guard {
	if budget <= 0 || summer == nil {
		return nil
	}
	return &Guard{
		budget:   budget,
		summer:   summer,
		cacheTTL: defaultCacheTTL,
		now:      time.Now,
	}
}

// Check reports whether a new session may start under the daily budget.
//
// A non-nil error means the daily total could not be read. Callers should fail
// OPEN (allow the session) in that case rather than block all work on a
// transient dependency outage — a kill switch that hard-fails closed on its own
// DB error causes a worse outage than the overspend it guards against. The
// returned Decision is still populated with Allowed=true so a caller that
// ignores the error stays safe.
func (g *Guard) Check(ctx context.Context) (Decision, error) {
	if g == nil {
		return Decision{Allowed: true}, nil
	}
	used, err := g.dailyTotal(ctx)
	if err != nil {
		return Decision{Allowed: true, Budget: g.budget}, err
	}
	return Decision{
		Allowed: used < g.budget,
		Used:    used,
		Budget:  g.budget,
	}, nil
}

// dailyTotal returns the tokens consumed since the start of the current UTC
// day, served from a short-lived cache. The lock is held across the query so a
// burst of concurrent creates collapses to a single sum query (the others wait
// and read the fresh cache) rather than stampeding the database.
func (g *Guard) dailyTotal(ctx context.Context) (int64, error) {
	now := g.now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	g.mu.Lock()
	defer g.mu.Unlock()

	// Reuse the cached reading only when it was taken within this UTC day and
	// hasn't aged past the TTL. The day check makes the total reset at midnight
	// even if no create has happened to refresh it.
	if g.haveCache && !g.cachedAt.Before(startOfDay) && now.Sub(g.cachedAt) < g.cacheTTL {
		return g.cachedSum, nil
	}

	sum, err := g.summer.SumTokensSince(ctx, startOfDay)
	if err != nil {
		return 0, err
	}
	g.cachedSum = sum
	g.cachedAt = now
	g.haveCache = true
	return sum, nil
}
