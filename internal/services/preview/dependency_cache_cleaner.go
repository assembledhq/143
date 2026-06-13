package preview

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/storage"
)

const defaultDependencyCacheCleanupBatchSize = 500

type DependencyCacheCleanerConfig struct {
	Store             *db.PreviewStore
	BlobStore         storage.SnapshotStore
	Logger            zerolog.Logger
	Retention         time.Duration
	Interval          time.Duration
	BatchSize         int
	KeepNewestPerRepo int
}

type DependencyCacheCleaner struct {
	store             *db.PreviewStore
	blobStore         storage.SnapshotStore
	logger            zerolog.Logger
	retention         time.Duration
	interval          time.Duration
	batchSize         int
	keepNewestPerRepo int
}

func NewDependencyCacheCleaner(cfg DependencyCacheCleanerConfig) *DependencyCacheCleaner {
	interval := cfg.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = defaultDependencyCacheCleanupBatchSize
	}
	return &DependencyCacheCleaner{
		store:             cfg.Store,
		blobStore:         cfg.BlobStore,
		logger:            cfg.Logger.With().Str("component", "preview_dependency_cache_cleaner").Logger(),
		retention:         cfg.Retention,
		interval:          interval,
		batchSize:         batchSize,
		keepNewestPerRepo: cfg.KeepNewestPerRepo,
	}
}

func (c *DependencyCacheCleaner) Run(ctx context.Context) {
	if c == nil || c.store == nil || c.blobStore == nil || c.retention <= 0 {
		return
	}
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		if err := c.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			c.logger.Warn().Err(err).Msg("preview dependency cache cleanup failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *DependencyCacheCleaner) RunOnce(ctx context.Context) error {
	if c == nil || c.store == nil || c.blobStore == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var entries []models.PreviewDependencyCache
	if c.retention > 0 {
		cutoff := time.Now().Add(-c.retention)
		expired, err := c.store.ListExpiredDependencyCaches(ctx, cutoff, c.batchSize)
		if err != nil {
			return err
		}
		for _, entry := range expired {
			seen[entry.ID.String()] = struct{}{}
			entries = append(entries, entry)
		}
	}
	if c.keepNewestPerRepo > 0 {
		overLimit, err := c.store.ListDependencyCachesOverLimit(ctx, c.keepNewestPerRepo, c.batchSize)
		if err != nil {
			return err
		}
		for _, entry := range overLimit {
			if _, ok := seen[entry.ID.String()]; ok {
				continue
			}
			seen[entry.ID.String()] = struct{}{}
			entries = append(entries, entry)
		}
	}
	for _, entry := range entries {
		blobKey := entry.BlobKey
		if blobKey == "" {
			c.logger.Warn().Str("cache_key", entry.CacheKey).Msg("expired dependency cache entry has no blob key")
			if err := c.store.DeleteDependencyCache(ctx, entry.OrgID, entry.ID); err != nil {
				c.logger.Warn().Err(err).Str("cache_key", entry.CacheKey).Msg("failed to delete expired dependency cache metadata")
			}
			continue
		}
		if err := c.blobStore.Delete(ctx, blobKey); err != nil && !errors.Is(err, storage.ErrSnapshotNotFound) {
			c.logger.Warn().Err(err).Str("blob_key", blobKey).Msg("failed to delete expired dependency cache blob")
			continue
		}
		if err := c.blobStore.Delete(ctx, blobKey+".sha256"); err != nil && !errors.Is(err, storage.ErrSnapshotNotFound) {
			c.logger.Warn().Err(err).Str("blob_key", blobKey+".sha256").Msg("failed to delete expired dependency cache checksum")
		}
		if err := c.store.DeleteDependencyCache(ctx, entry.OrgID, entry.ID); err != nil {
			c.logger.Warn().Err(err).Str("cache_key", entry.CacheKey).Msg("failed to delete expired dependency cache metadata")
		}
	}
	if c.retention > 0 {
		cutoff := time.Now().Add(-c.retention)
		deleted, err := c.store.DeleteExpiredDependencyCacheLocations(ctx, cutoff)
		if err != nil {
			return err
		}
		if deleted > 0 {
			c.logger.Debug().Int64("deleted_locations", deleted).Msg("deleted expired dependency cache location hints")
		}
	}
	return nil
}
