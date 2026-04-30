package linear

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// teamKeyCacheTTL bounds how long a stale entry can survive after another
// process (e.g. the refresh_linear_team_keys cron on a different node) writes
// to linear_team_keys. Detection misses on a freshly-created team are
// acceptable for this window; the alternative — Listen/Notify or a synchronous
// bust — is overkill for a list that changes O(once a quarter).
const teamKeyCacheTTL = 60 * time.Second

// teamKeyAllowlistCache is an in-process TTL cache of the per-org team-key
// allowlist. Hot-path callers (session create) hit this instead of the DB on
// every request. The map values are pre-built lookup tables so detection's
// inner loop stays branch-free.
type teamKeyAllowlistCache struct {
	mu      sync.RWMutex
	entries map[uuid.UUID]teamKeyCacheEntry
}

type teamKeyCacheEntry struct {
	allow     map[string]bool
	expiresAt time.Time
}

func (c *teamKeyAllowlistCache) get(orgID uuid.UUID) (map[string]bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[orgID]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.allow, true
}

// put inserts allow under orgID and opportunistically evicts other expired
// entries. Returns the number of expired entries swept so the caller can
// surface eviction activity in logs (operators verifying the TTL is firing
// in production look for non-zero values in the breadcrumb).
func (c *teamKeyAllowlistCache) put(orgID uuid.UUID, allow map[string]bool) int {
	// Defensive copy of the inbound map so callers can't retain a reference
	// to the cached storage and mutate it later. The caller in
	// TeamKeyAllowlist already builds a fresh map per miss, but a future
	// caller passing an aliased slice/map would silently corrupt every other
	// org's lookup tables. Pay the small allocation here so the cache's
	// invariants don't depend on caller behavior.
	stored := make(map[string]bool, len(allow))
	for k, v := range allow {
		stored[k] = v
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[uuid.UUID]teamKeyCacheEntry)
	}
	// Sweep expired entries opportunistically so abandoned orgs don't leak
	// indefinitely. The TTL window is short and put() is cold (cache miss
	// path), so the O(n) walk is fine.
	now := time.Now()
	evicted := 0
	for id, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, id)
			evicted++
		}
	}
	c.entries[orgID] = teamKeyCacheEntry{
		allow:     stored,
		expiresAt: now.Add(teamKeyCacheTTL),
	}
	return evicted
}

func (c *teamKeyAllowlistCache) invalidate(orgID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, orgID)
}
