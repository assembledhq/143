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

type MentionIndexCacheConfig struct {
	Redis         *cache.Client
	Logger        zerolog.Logger
	RedisTTL      time.Duration
	LocalMaxItems int
}

type MentionIndexCache struct {
	redis         *cache.Client
	logger        zerolog.Logger
	redisTTL      time.Duration
	localMaxItems int

	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	sf      singleflight.Group
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
	return &MentionIndexCache{
		redis:         cfg.Redis,
		logger:        cfg.Logger,
		redisTTL:      cfg.RedisTTL,
		localMaxItems: cfg.LocalMaxItems,
		entries:       make(map[string]*list.Element),
		lru:           list.New(),
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
		c.putShared(ctx, key, index)
		return index, nil
	})
	if err != nil {
		return MentionIndex{}, err
	}
	return value.(MentionIndex), nil
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
