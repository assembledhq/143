package agent

import "time"

// SetHydrateRetryBackoffForTest overrides the hydrate retry backoff so external
// tests don't sleep through real backoffs, and returns a function that restores
// the previous value. Call it only from non-parallel tests: it mutates a
// package-global, so concurrent use would race.
func SetHydrateRetryBackoffForTest(d time.Duration) (restore func()) {
	prev := hydrateRetryBackoff
	hydrateRetryBackoff = d
	return func() { hydrateRetryBackoff = prev }
}
