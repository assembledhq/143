package workspace

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"golang.org/x/sync/singleflight"
)

const defaultMentionIndexRedisTTL = 24 * time.Hour
const defaultMentionIndexLocalMaxItems = 128
const defaultMentionIndexRefreshTimeout = 60 * time.Second

// mentionIndexRefreshFailureBackoff suppresses repeated background rebuild
// attempts for a key after one fails. Without it, every stale serve against
// a workspace that can no longer be read (e.g. a reaped container) would
// spawn a doomed rebuild. Keyed by the exact cache key, which churns each
// turn, so a new turn always gets a fresh attempt.
const mentionIndexRefreshFailureBackoff = 30 * time.Second

type MentionIndexCacheConfig struct {
	Redis         *cache.Client
	Logger        zerolog.Logger
	RedisTTL      time.Duration
	LocalMaxItems int
	// RefreshTimeout bounds the background rebuilds GetOrBuildStale kicks
	// off when it serves a stale index.
	RefreshTimeout time.Duration
}

type MentionIndexCache struct {
	redis          *cache.Client
	logger         zerolog.Logger
	redisTTL       time.Duration
	localMaxItems  int
	refreshTimeout time.Duration

	mu              sync.Mutex
	entries         map[string]*list.Element
	lru             *list.List
	refreshFailures map[string]time.Time
	sf              singleflight.Group
}

type mentionIndexCacheEntry struct {
	key   string
	index MentionIndex
}

func NewMentionIndexCache(cfg MentionIndexCacheConfig) *MentionIndexCache {
	if cfg.RedisTTL <= 0 {
		cfg.RedisTTL = defaultMentionIndexRedisTTL
	}
	if cfg.LocalMaxItems <= 0 {
		cfg.LocalMaxItems = defaultMentionIndexLocalMaxItems
	}
	if cfg.RefreshTimeout <= 0 {
		cfg.RefreshTimeout = defaultMentionIndexRefreshTimeout
	}
	return &MentionIndexCache{
		redis:           cfg.Redis,
		logger:          cfg.Logger,
		redisTTL:        cfg.RedisTTL,
		localMaxItems:   cfg.LocalMaxItems,
		refreshTimeout:  cfg.RefreshTimeout,
		entries:         make(map[string]*list.Element),
		lru:             list.New(),
		refreshFailures: make(map[string]time.Time),
	}
}

func (c *MentionIndexCache) GetOrBuild(ctx context.Context, key string, build func(context.Context) (MentionIndex, error)) (MentionIndex, error) {
	if key == "" {
		return MentionIndex{}, errors.New("mention index cache key is required")
	}
	if build == nil {
		return MentionIndex{}, errors.New("mention index builder is required")
	}

	if index, ok := c.getLocal(key); ok {
		return index, nil
	}

	value, err, _ := c.sf.Do(key, func() (interface{}, error) {
		if index, ok := c.getLocal(key); ok {
			return index, nil
		}
		if index, ok := c.getShared(ctx, key); ok {
			c.putLocal(key, index)
			return index, nil
		}
		index, err := build(ctx)
		if err != nil {
			return MentionIndex{}, err
		}
		c.putLocal(key, index)
		if err := c.putShared(ctx, key, index); err != nil {
			c.logger.Warn().Err(err).Str("key", key).Msg("failed to write rebuilt mention index to shared cache")
		}
		return index, nil
	})
	if err != nil {
		return MentionIndex{}, err
	}
	return value.(MentionIndex), nil
}

// GetOrBuildStale is GetOrBuild with stale-while-revalidate semantics. When
// the exact key misses but staleKey (a session-scoped alias maintained across
// turns) holds a previously built index, the stale index is returned
// immediately and a background rebuild repopulates the exact key. The bool
// result reports whether the returned index was stale.
func (c *MentionIndexCache) GetOrBuildStale(ctx context.Context, key, staleKey string, build func(context.Context) (MentionIndex, error)) (MentionIndex, bool, error) {
	if key == "" {
		return MentionIndex{}, false, errors.New("mention index cache key is required")
	}
	if build == nil {
		return MentionIndex{}, false, errors.New("mention index builder is required")
	}

	if index, ok := c.getLocal(key); ok {
		return index, false, nil
	}

	type lookupResult struct {
		index MentionIndex
		stale bool
	}

	value, err, _ := c.sf.Do(key, func() (interface{}, error) {
		if index, ok := c.getLocal(key); ok {
			return lookupResult{index: index}, nil
		}
		if index, ok := c.getShared(ctx, key); ok {
			c.putLocal(key, index)
			return lookupResult{index: index}, nil
		}
		if staleKey != "" {
			if index, ok := c.getLocal(staleKey); ok {
				c.refreshInBackground(key, staleKey, build)
				return lookupResult{index: index, stale: true}, nil
			}
			if index, ok := c.getShared(ctx, staleKey); ok {
				c.putLocal(staleKey, index)
				c.refreshInBackground(key, staleKey, build)
				return lookupResult{index: index, stale: true}, nil
			}
		}
		index, err := build(ctx)
		if err != nil {
			return lookupResult{}, err
		}
		c.store(ctx, key, staleKey, index)
		return lookupResult{index: index}, nil
	})
	if err != nil {
		return MentionIndex{}, false, err
	}
	result := value.(lookupResult)
	return result.index, result.stale, nil
}

// refreshInBackground rebuilds the index for key on a detached context and
// stores it under both key and staleKey. Concurrent refreshes for the same
// key coalesce via singleflight, the rebuild is skipped entirely when the
// key is already cached (locally or in Redis), and a failed rebuild backs
// off for mentionIndexRefreshFailureBackoff — so callers may invoke this on
// every stale serve without stampeding the workspace reader.
func (c *MentionIndexCache) refreshInBackground(key, staleKey string, build func(context.Context) (MentionIndex, error)) {
	if c == nil || key == "" || build == nil {
		return
	}
	if c.refreshRecentlyFailed(key, time.Now()) {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				c.logger.Warn().Interface("panic", r).Str("key", key).Msg("panic refreshing mention index in background")
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), c.refreshTimeout)
		defer cancel()
		_, _, _ = c.sf.Do("refresh:"+key, func() (interface{}, error) {
			if _, ok := c.getLocal(key); ok {
				return nil, nil
			}
			if index, ok := c.getShared(ctx, key); ok {
				c.putLocal(key, index)
				return nil, nil
			}
			index, err := build(ctx)
			if err != nil {
				c.logger.Warn().Err(err).Str("key", key).Msg("failed to refresh mention index in background")
				c.recordRefreshFailure(key, time.Now())
				return nil, nil
			}
			c.clearRefreshFailure(key)
			c.store(ctx, key, staleKey, index)
			return nil, nil
		})
	}()
}

func (c *MentionIndexCache) refreshRecentlyFailed(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	failedAt, ok := c.refreshFailures[key]
	return ok && now.Sub(failedAt) < mentionIndexRefreshFailureBackoff
}

func (c *MentionIndexCache) recordRefreshFailure(key string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Prune expired entries while we hold the lock so the map stays bounded
	// by the number of keys that failed within the backoff window.
	for k, failedAt := range c.refreshFailures {
		if now.Sub(failedAt) >= mentionIndexRefreshFailureBackoff {
			delete(c.refreshFailures, k)
		}
	}
	c.refreshFailures[key] = now
}

func (c *MentionIndexCache) clearRefreshFailure(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.refreshFailures, key)
}

// store writes the index under the exact key and, when set, the stale alias,
// in both the local LRU and the shared Redis cache. Shared-cache write
// failures are logged but never surfaced — the caller already has the index.
func (c *MentionIndexCache) store(ctx context.Context, key, staleKey string, index MentionIndex) {
	c.putLocal(key, index)
	if err := c.putShared(ctx, key, index); err != nil {
		c.logger.Warn().Err(err).Str("key", key).Msg("failed to write rebuilt mention index to shared cache")
	}
	if staleKey == "" || staleKey == key {
		return
	}
	c.putLocal(staleKey, index)
	if err := c.putShared(ctx, staleKey, index); err != nil {
		c.logger.Warn().Err(err).Str("key", staleKey).Msg("failed to write rebuilt mention index to shared cache")
	}
}

func (c *MentionIndexCache) Warm(ctx context.Context, key string, index MentionIndex) error {
	if key == "" {
		return errors.New("mention index cache key is required")
	}
	c.putLocal(key, index)
	if err := c.putShared(ctx, key, index); err != nil {
		return err
	}
	return nil
}

func (c *MentionIndexCache) getLocal(key string) (MentionIndex, bool) {
	if c == nil {
		return MentionIndex{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[key]
	if !ok {
		return MentionIndex{}, false
	}
	c.lru.MoveToFront(element)
	return element.Value.(*mentionIndexCacheEntry).index, true
}

func (c *MentionIndexCache) putLocal(key string, index MentionIndex) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.entries[key]; ok {
		existing.Value.(*mentionIndexCacheEntry).index = index
		c.lru.MoveToFront(existing)
		return
	}

	element := c.lru.PushFront(&mentionIndexCacheEntry{key: key, index: index})
	c.entries[key] = element
	for c.lru.Len() > c.localMaxItems {
		last := c.lru.Back()
		if last == nil {
			break
		}
		c.lru.Remove(last)
		delete(c.entries, last.Value.(*mentionIndexCacheEntry).key)
	}
}

func (c *MentionIndexCache) getShared(ctx context.Context, key string) (MentionIndex, bool) {
	if c == nil || c.redis == nil || !c.redis.Available() {
		return MentionIndex{}, false
	}
	data, err := c.redis.GetBytes(ctx, key)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return MentionIndex{}, false
		}
		c.logger.Warn().Err(err).Str("key", key).Msg("failed to read mention index from Redis")
		return MentionIndex{}, false
	}
	var index MentionIndex
	if err := json.Unmarshal(data, &index); err != nil {
		c.logger.Warn().Err(err).Str("key", key).Msg("failed to decode mention index from Redis")
		return MentionIndex{}, false
	}
	return index, true
}

func (c *MentionIndexCache) putShared(ctx context.Context, key string, index MentionIndex) error {
	if c == nil || c.redis == nil || !c.redis.Available() {
		return nil
	}
	data, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("marshal mention index: %w", err)
	}
	if err := c.redis.SetBytes(ctx, key, data, c.redisTTL); err != nil {
		c.logger.Warn().Err(err).Str("key", key).Msg("failed to write mention index to Redis")
		return err
	}
	return nil
}
