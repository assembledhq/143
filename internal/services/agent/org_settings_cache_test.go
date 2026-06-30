package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestOrgSettingsCache_MissOnEmpty(t *testing.T) {
	t.Parallel()
	c := NewOrgSettingsCache(time.Minute)
	_, ok := c.Get(uuid.New())
	require.False(t, ok, "empty cache must miss")
}

func TestOrgSettingsCache_SetAndGet(t *testing.T) {
	t.Parallel()
	c := NewOrgSettingsCache(time.Minute)
	orgID := uuid.New()
	snapshot := AgentSettingsSnapshot{
		AgentConfig:     models.AgentEnvConfig{"amp": {"AMP_MODE": "deep"}},
		OpenCodeRouting: models.OpenCodeRoutingSettings{RequireOpenRouter: true},
	}

	c.Set(orgID, snapshot)

	got, ok := c.Get(orgID)
	require.True(t, ok, "set should be readable")
	require.Equal(t, snapshot, got, "snapshot round-trips agent config and routing policy")
}

func TestOrgSettingsCache_NilConfigIsCached(t *testing.T) {
	t.Parallel()
	c := NewOrgSettingsCache(time.Minute)
	orgID := uuid.New()

	// Caching nil is a legitimate "no agent_config" answer and must be a hit,
	// not a miss — otherwise orgs without agent_config thrash the DB on every
	// session start.
	c.Set(orgID, AgentSettingsSnapshot{})

	got, ok := c.Get(orgID)
	require.True(t, ok, "cached empty snapshot must register as a hit")
	require.Nil(t, got.AgentConfig)
}

func TestOrgSettingsCache_Expiry(t *testing.T) {
	t.Parallel()
	c := NewOrgSettingsCache(time.Minute)

	base := time.Now()
	current := base
	c.SetClockForTest(func() time.Time { return current })

	orgID := uuid.New()
	c.Set(orgID, AgentSettingsSnapshot{AgentConfig: models.AgentEnvConfig{"amp": {"AMP_MODE": "smart"}}})

	// Advance just under the TTL — still a hit.
	current = base.Add(59 * time.Second)
	_, ok := c.Get(orgID)
	require.True(t, ok, "within-TTL read must hit")

	// Advance past the TTL — miss.
	current = base.Add(time.Minute + time.Second)
	_, ok = c.Get(orgID)
	require.False(t, ok, "post-TTL read must miss")
}

func TestOrgSettingsCache_InvalidateOrg(t *testing.T) {
	t.Parallel()
	c := NewOrgSettingsCache(time.Minute)
	orgID := uuid.New()
	other := uuid.New()

	c.Set(orgID, AgentSettingsSnapshot{AgentConfig: models.AgentEnvConfig{"amp": {"AMP_MODE": "deep"}}})
	c.Set(other, AgentSettingsSnapshot{AgentConfig: models.AgentEnvConfig{"amp": {"AMP_MODE": "rush"}}})

	c.InvalidateOrg(orgID)

	_, ok := c.Get(orgID)
	require.False(t, ok, "invalidated org must miss")

	_, ok = c.Get(other)
	require.True(t, ok, "invalidation must not affect other orgs")
}

func TestOrgSettingsCache_DefaultTTL(t *testing.T) {
	t.Parallel()
	c := NewOrgSettingsCache(0)
	require.Equal(t, DefaultOrgSettingsCacheTTL, c.ttl,
		"non-positive TTL should fall back to the default")
}

// TestOrgSettingsCache_ConcurrentAccess drives the cache from many
// goroutines to catch data races under -race. It doesn't assert anything
// beyond "no race, no panic" — correctness of a single read/write is
// covered by the other tests.
func TestOrgSettingsCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	c := NewOrgSettingsCache(time.Minute)
	orgs := make([]uuid.UUID, 16)
	for i := range orgs {
		orgs[i] = uuid.New()
	}

	var wg sync.WaitGroup
	const iterations = 200
	for _, orgID := range orgs {
		orgID := orgID
		wg.Add(3)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				c.Set(orgID, AgentSettingsSnapshot{AgentConfig: models.AgentEnvConfig{"amp": {"AMP_MODE": "large"}}})
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_, _ = c.Get(orgID)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if i%5 == 0 {
					c.InvalidateOrg(orgID)
				}
			}
		}()
	}
	wg.Wait()

	require.LessOrEqual(t, c.Len(), len(orgs),
		"cache must not grow beyond the number of distinct orgs written")
}
