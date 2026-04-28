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

func (c *teamKeyAllowlistCache) put(orgID uuid.UUID, allow map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[uuid.UUID]teamKeyCacheEntry)
	}
	c.entries[orgID] = teamKeyCacheEntry{
		allow:     allow,
		expiresAt: time.Now().Add(teamKeyCacheTTL),
	}
}

func (c *teamKeyAllowlistCache) invalidate(orgID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, orgID)
}
