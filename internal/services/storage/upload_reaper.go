package storage

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
)

// UploadReaper periodically cleans up old uploaded files that are past their
// max age. For FileUploadStore it walks the local directory; for S3UploadStore
// the reaper is a no-op (use S3 lifecycle rules instead).
type UploadReaper struct {
	store    UploadStore
	maxAge   time.Duration
	interval time.Duration
	logger   zerolog.Logger
}

// NewUploadReaper creates a reaper that runs every interval and deletes
// uploads older than maxAge.
func NewUploadReaper(store UploadStore, maxAge, interval time.Duration, logger zerolog.Logger) *UploadReaper {
	return &UploadReaper{
		store:    store,
		maxAge:   maxAge,
		interval: interval,
		logger:   logger,
	}
}

// Run starts the reaper loop. It blocks until ctx is cancelled.
func (r *UploadReaper) Run(ctx context.Context) {
	fileStore, ok := r.store.(*FileUploadStore)
	if !ok {
		r.logger.Info().Msg("upload reaper: S3 mode — use S3 lifecycle rules for cleanup; reaper disabled")
		return
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info().
		Dur("interval", r.interval).
		Dur("max_age", r.maxAge).
		Msg("upload reaper started")

	for {
		select {
		case <-ctx.Done():
			r.logger.Info().Msg("upload reaper stopped")
			return
		case <-ticker.C:
			r.reapFiles(ctx, fileStore)
		}
	}
}

func (r *UploadReaper) reapFiles(_ context.Context, store *FileUploadStore) {
	// Skip if the upload directory hasn't been created yet (no uploads have occurred).
	if _, err := os.Stat(store.baseDir); os.IsNotExist(err) {
		return
	}

	cutoff := time.Now().Add(-r.maxAge)
	var deleted, errors int

	err := filepath.Walk(store.baseDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if removeErr := os.Remove(path); removeErr != nil {
				r.logger.Warn().Err(removeErr).Str("path", path).Msg("upload reaper: failed to delete file")
				errors++
			} else {
				deleted++
			}
		}
		return nil
	})

	if err != nil {
		r.logger.Error().Err(err).Msg("upload reaper: failed to walk upload directory")
		return
	}

	if deleted > 0 || errors > 0 {
		r.logger.Info().Int("deleted", deleted).Int("errors", errors).Msg("upload reaper: cleanup complete")
	}

	// Clean up empty directories left behind after file deletion.
	r.cleanEmptyDirs(store.baseDir)
}

// cleanEmptyDirs removes empty directories under baseDir (bottom-up).
func (r *UploadReaper) cleanEmptyDirs(baseDir string) {
	// Walk in reverse order (deepest first) to handle nested empties.
	var dirs []string
	_ = filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && path != baseDir {
			dirs = append(dirs, path)
		}
		return nil
	})

	// Remove in reverse (deepest first).
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(dirs[i])
		if err != nil {
			continue
		}
		if len(entries) == 0 {
			if err := os.Remove(dirs[i]); err != nil {
				r.logger.Warn().Err(err).Str("dir", dirs[i]).Msg("upload reaper: failed to remove empty dir")
			}
		}
	}
}
