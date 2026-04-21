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

// OrgSettingsCache caches the parsed agent_config section of an org's
// settings. It is safe for concurrent use. Entries expire after the
// configured TTL; settings.Update calls Invalidate so changes take effect
// immediately for callers that wire both sides up.
//
// Scope: only the AgentEnvConfig slice of org settings is cached — this is
// the hot path for Amp/Pi session starts, and keeping the payload small
// avoids holding on to unrelated org state (PM config, context limits, etc.)
// that might otherwise be expected to refresh promptly.
type OrgSettingsCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[uuid.UUID]orgSettingsCacheEntry
	now     func() time.Time // injectable for tests
}

type orgSettingsCacheEntry struct {
	config    models.AgentEnvConfig
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

// Get returns the cached AgentEnvConfig for the org, or (nil, false) if the
// entry is missing or expired. A cached nil AgentConfig is a legitimate hit:
// it means "we checked, and this org has no agent_config" — callers should
// not treat that as a miss.
func (c *OrgSettingsCache) Get(orgID uuid.UUID) (models.AgentEnvConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[orgID]
	if !ok || c.now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.config, true
}

// Set stores the AgentEnvConfig for the org with a fresh TTL. Calling Set
// with a nil config is valid and caches the "no agent_config" answer.
func (c *OrgSettingsCache) Set(orgID uuid.UUID, config models.AgentEnvConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[orgID] = orgSettingsCacheEntry{
		config:    config,
		expiresAt: c.now().Add(c.ttl),
	}
}

// InvalidateOrg drops the cached entry for a single org. Intended to be
// called from the settings update handler after a successful DB write so the
// next Amp/Pi session start sees the new config without waiting for TTL
// expiry.
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
