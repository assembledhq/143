package agent

import (
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// DefaultOrgSettingsCacheTTL is the default staleness window for the
// org-settings cache. Chosen to absorb bursts of Amp/Pi session starts without
// hammering the DB while keeping the post-settings-update consistency window
// short enough that operators rarely need to think about it.
const DefaultOrgSettingsCacheTTL = 30 * time.Second

// AgentSettingsSnapshot is the slice of org settings the agent env caches for
// hot session-start reads: the per-agent env overrides plus the OpenCode
// routing policy. Kept deliberately small so the cache does not pin unrelated
// org state (PM config, context limits, …) that might be expected to refresh
// promptly.
type AgentSettingsSnapshot struct {
	AgentConfig     models.AgentEnvConfig
	OpenCodeRouting models.OpenCodeRoutingSettings
}

// OrgSettingsCache caches the agent-relevant slice of an org's settings (see
// AgentSettingsSnapshot). It is safe for concurrent use. Entries expire after
// the configured TTL; settings.Update calls Invalidate so changes take effect
// immediately for callers that wire both sides up.
//
// Scope: only AgentSettingsSnapshot is cached — this is the hot path for
// Amp/Pi/OpenCode session starts, and keeping the payload small avoids holding
// on to unrelated org state (PM config, context limits, etc.) that might
// otherwise be expected to refresh promptly.
type OrgSettingsCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[uuid.UUID]orgSettingsCacheEntry
	now     func() time.Time // injectable for tests
}

type orgSettingsCacheEntry struct {
	snapshot  AgentSettingsSnapshot
	expiresAt time.Time
}

// NewOrgSettingsCache returns a cache with the given TTL. A non-positive TTL
// falls back to DefaultOrgSettingsCacheTTL.
func NewOrgSettingsCache(ttl time.Duration) *OrgSettingsCache {
	if ttl <= 0 {
		ttl = DefaultOrgSettingsCacheTTL
	}
	return &OrgSettingsCache{
		ttl:     ttl,
		entries: make(map[uuid.UUID]orgSettingsCacheEntry),
		now:     time.Now,
	}
}

// Get returns the cached AgentSettingsSnapshot for the org, or (zero, false) if
// the entry is missing or expired. A cached snapshot with a nil AgentConfig is
// a legitimate hit: it means "we checked, and this org has no agent_config" —
// callers should not treat that as a miss.
func (c *OrgSettingsCache) Get(orgID uuid.UUID) (AgentSettingsSnapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[orgID]
	if !ok || c.now().After(entry.expiresAt) {
		return AgentSettingsSnapshot{}, false
	}
	return entry.snapshot, true
}

// Set stores the AgentSettingsSnapshot for the org with a fresh TTL. A snapshot
// with a nil AgentConfig is valid and caches the "no agent_config" answer.
func (c *OrgSettingsCache) Set(orgID uuid.UUID, snapshot AgentSettingsSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[orgID] = orgSettingsCacheEntry{
		snapshot:  snapshot,
		expiresAt: c.now().Add(c.ttl),
	}
}

// InvalidateOrg drops the cached entry for a single org. Intended to be
// called from the settings update handler after a successful DB write so the
// next Amp/Pi session start sees the new config without waiting for TTL
// expiry.
//
// This is a soft invalidation, not a barrier: a concurrent reader that races
// the InvalidateOrg call (cache miss → DB read → Set) can re-populate the
// cache with the value committed just before the write, leaving the entry
// stale until the next invalidation or TTL expiry. In practice the window is
// microseconds and the next read wins (last-writer-wins), so no data is
// corrupted — but callers that need a strict happens-before between "settings
// committed" and "next session reads new value" should not rely on this
// alone. For cross-process invalidation, see the comment in cmd/server/main.go
// where the cache is constructed.
func (c *OrgSettingsCache) InvalidateOrg(orgID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, orgID)
}

// Len returns the number of cached entries (including expired ones that
// haven't been overwritten yet). Intended for tests and metrics.
func (c *OrgSettingsCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// SetClockForTest replaces the cache's time source. Only intended for tests —
// production code should never call this.
func (c *OrgSettingsCache) SetClockForTest(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	c.now = now
}
