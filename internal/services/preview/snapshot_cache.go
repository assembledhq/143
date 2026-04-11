package preview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// =============================================================================
// Constants
// =============================================================================

const (
	// DefaultMaxCacheBytes is 20 GB per worker.
	DefaultMaxCacheBytes int64 = 20 * 1024 * 1024 * 1024

	// snapshotTmpFile is the temporary path inside the sandbox where the
	// snapshot tar.gz is staged during create/restore.
	snapshotTmpFile = "/tmp/snapshot.tar.gz"

	// tarExcludeFlags are the directories excluded from the workspace snapshot.
	// .git is reconstructed from the repo clone; node_modules/.cache and other
	// build caches are regenerated cheaply by the package manager.
	tarExcludeFlags = `--exclude=.git --exclude='node_modules/.cache' --exclude='.next/cache' --exclude='__pycache__' --exclude='.pytest_cache'`
)

// =============================================================================
// Interfaces
// =============================================================================

// SnapshotExecutor defines the sandbox exec primitives needed by SnapshotCache.
// This is a subset of the full sandbox provider interface, scoped to what
// snapshot operations require.
type SnapshotExecutor interface {
	// Exec runs a command inside the sandbox, writing stdout/stderr to the
	// provided writers. Returns the exit code.
	Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error)

	// ReadFile reads a file from the sandbox filesystem.
	ReadFile(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error)

	// WriteFile writes data to a file inside the sandbox filesystem.
	WriteFile(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error
}

// =============================================================================
// Types
// =============================================================================

// SnapshotMetadata carries contextual information recorded alongside the
// snapshot for debugging and cache management.
type SnapshotMetadata struct {
	OrgID  uuid.UUID
	RepoID uuid.UUID
}

// CacheHit is returned by FindSnapshot when a matching snapshot exists on this
// worker's local disk.
type CacheHit struct {
	Entry    models.PreviewStartupCache
	BlobPath string // absolute path to the tar.gz on the worker
}

// SnapshotCache manages filesystem snapshots for fast preview startup.
//
// After a preview starts successfully, the workspace filesystem is archived
// into a tar.gz on the worker's local disk. On subsequent starts with the same
// lockfile, base commit, and preview config, the snapshot is restored into the
// sandbox instead of rebuilding from scratch.
//
// An LRU eviction policy keeps total disk usage under a configurable limit
// (default 20 GB per worker).
type SnapshotCache struct {
	store         *db.PreviewStore
	executor      SnapshotExecutor
	logger        zerolog.Logger
	workerNodeID  string
	cacheDir      string // local disk path for snapshot storage, e.g. /var/cache/143-preview
	maxCacheBytes int64  // default 20 GB
}

// SnapshotCacheConfig holds initialization options for SnapshotCache.
type SnapshotCacheConfig struct {
	Store         *db.PreviewStore
	Executor      SnapshotExecutor
	Logger        zerolog.Logger
	WorkerNodeID  string
	CacheDir      string
	MaxCacheBytes int64
}

// NewSnapshotCache creates a new SnapshotCache.
//
// If MaxCacheBytes is zero, DefaultMaxCacheBytes (20 GB) is used.
// The cache directory is created if it does not exist.
func NewSnapshotCache(cfg SnapshotCacheConfig) (*SnapshotCache, error) {
	if cfg.CacheDir == "" {
		return nil, fmt.Errorf("snapshot cache: cache directory must be specified")
	}
	if cfg.WorkerNodeID == "" {
		return nil, fmt.Errorf("snapshot cache: worker node ID must be specified")
	}

	maxBytes := cfg.MaxCacheBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxCacheBytes
	}

	// Ensure cache directory exists with restrictive permissions.
	if err := os.MkdirAll(cfg.CacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("snapshot cache: create cache dir: %w", err)
	}

	return &SnapshotCache{
		store:         cfg.Store,
		executor:      cfg.Executor,
		logger:        cfg.Logger.With().Str("component", "snapshot_cache").Logger(),
		workerNodeID:  cfg.WorkerNodeID,
		cacheDir:      cfg.CacheDir,
		maxCacheBytes: maxBytes,
	}, nil
}

// =============================================================================
// ComputeSnapshotKey
// =============================================================================

// ComputeSnapshotKey produces a deterministic SHA-256 hex digest from the
// lockfile contents, base commit SHA, and preview config digest. Two previews
// with the same key are guaranteed to have identical workspace state after the
// build step.
func ComputeSnapshotKey(lockfileContents []byte, baseCommit string, configDigest string) string {
	h := sha256.New()
	h.Write(lockfileContents)
	h.Write([]byte{0}) // separator
	h.Write([]byte(baseCommit))
	h.Write([]byte{0})
	h.Write([]byte(configDigest))
	return hex.EncodeToString(h.Sum(nil))
}

// =============================================================================
// CreateSnapshot
// =============================================================================

// CreateSnapshot archives the sandbox workspace into a tar.gz on the worker's
// local disk and records the metadata in the database.
//
// This should be called after a preview has started successfully. The snapshot
// captures the fully-built workspace (installed dependencies, compiled assets,
// etc.) so that future previews with the same key can skip the build step.
//
// The method is safe to call concurrently for different sandboxes. Each
// snapshot is written to a unique file path derived from the snapshot key.
func (sc *SnapshotCache) CreateSnapshot(
	ctx context.Context,
	sb *agent.Sandbox,
	snapshotKey string,
	metadata SnapshotMetadata,
) error {
	log := sc.logger.With().
		Str("snapshot_key", snapshotKey).
		Str("sandbox_id", sb.ID).
		Logger()

	log.Info().Msg("creating filesystem snapshot")
	start := time.Now()

	// 1. Create the tar.gz inside the sandbox.
	//    Using shell tar is significantly faster than Go's archive/tar for
	//    large node_modules trees (parallel I/O, kernel-level buffering).
	tarCmd := fmt.Sprintf(
		"tar czf %s -C %s %s .",
		snapshotTmpFile,
		sb.WorkDir,
		tarExcludeFlags,
	)

	var stderr bytes.Buffer
	exitCode, err := sc.executor.Exec(ctx, sb, tarCmd, io.Discard, &stderr)
	if err != nil {
		return fmt.Errorf("snapshot create: exec tar: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("snapshot create: tar exited %d: %s", exitCode, stderr.String())
	}

	// 2. Read the tar.gz out of the sandbox.
	tarData, err := sc.executor.ReadFile(ctx, sb, snapshotTmpFile)
	if err != nil {
		return fmt.Errorf("snapshot create: read tar from sandbox: %w", err)
	}

	// 3. Write to worker local disk.
	blobPath := sc.blobPath(snapshotKey)
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o750); err != nil {
		return fmt.Errorf("snapshot create: mkdir: %w", err)
	}

	if err := atomicWriteFile(blobPath, tarData, 0o640); err != nil {
		return fmt.Errorf("snapshot create: write blob: %w", err)
	}

	// Compute and store a SHA-256 checksum alongside the blob for integrity
	// verification on restore.
	checksum := sha256.Sum256(tarData)
	checksumHex := hex.EncodeToString(checksum[:])
	checksumPath := blobPath + ".sha256"
	if err := atomicWriteFile(checksumPath, []byte(checksumHex), 0o640); err != nil {
		return fmt.Errorf("snapshot create: write checksum: %w", err)
	}

	sizeBytes := int64(len(tarData))

	// 4. Record in database.
	entry := &models.PreviewStartupCache{
		OrgID:        metadata.OrgID,
		RepoID:       metadata.RepoID,
		SnapshotKey:  snapshotKey,
		BlobPath:     blobPath,
		SizeBytes:    sizeBytes,
		WorkerNodeID: sc.workerNodeID,
	}
	if err := sc.store.UpsertStartupCache(ctx, entry); err != nil {
		// Best-effort cleanup of the blob if DB write fails.
		_ = os.Remove(blobPath)
		return fmt.Errorf("snapshot create: upsert db: %w", err)
	}

	// 5. Clean up the temporary file inside the sandbox.
	_, _ = sc.executor.Exec(ctx, sb, fmt.Sprintf("rm -f %s", snapshotTmpFile), io.Discard, io.Discard)

	// 6. Evict old entries if we are over the size limit.
	if evictErr := sc.EvictLRU(ctx); evictErr != nil {
		log.Warn().Err(evictErr).Msg("post-snapshot LRU eviction failed")
	}

	log.Info().
		Int64("size_bytes", sizeBytes).
		Dur("elapsed_ms", time.Since(start)).
		Str("blob_path", blobPath).
		Msg("filesystem snapshot created")

	return nil
}

// =============================================================================
// FindSnapshot
// =============================================================================

// FindSnapshot checks whether a snapshot matching the given key exists in the
// database and on this worker's local disk. Returns nil (not an error) when no
// matching snapshot is found.
func (sc *SnapshotCache) FindSnapshot(
	ctx context.Context,
	orgID, repoID uuid.UUID,
	snapshotKey string,
) (*CacheHit, error) {
	entry, err := sc.store.FindMatchingCache(ctx, orgID, repoID, snapshotKey)
	if err != nil {
		// pgx returns ErrNoRows when no match; treat as cache miss.
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, fmt.Errorf("snapshot find: query db: %w", err)
	}

	// Verify the entry belongs to this worker. Snapshots are local files and
	// are not portable across workers.
	if entry.WorkerNodeID != sc.workerNodeID {
		sc.logger.Debug().
			Str("snapshot_key", snapshotKey).
			Str("entry_worker", entry.WorkerNodeID).
			Msg("snapshot exists but on different worker")
		return nil, nil
	}

	// Verify the blob still exists on disk. It could have been manually
	// deleted or lost due to ephemeral storage.
	if _, err := os.Stat(entry.BlobPath); os.IsNotExist(err) {
		sc.logger.Warn().
			Str("blob_path", entry.BlobPath).
			Msg("snapshot blob missing from disk; cleaning up stale DB entry")
		_ = os.Remove(entry.BlobPath + ".sha256") // best-effort cleanup of checksum file
		_ = sc.store.DeleteCache(ctx, entry.OrgID, entry.ID)
		return nil, nil
	}

	return &CacheHit{
		Entry:    *entry,
		BlobPath: entry.BlobPath,
	}, nil
}

// =============================================================================
// RestoreSnapshot
// =============================================================================

// RestoreSnapshot extracts a cached tar.gz into the sandbox workspace. The
// caller should call this instead of running the full build when FindSnapshot
// returns a CacheHit.
//
// After restoration, the caller still needs to:
//   - Reconstruct .git (the snapshot excludes it)
//   - Inject fresh credentials
//   - Start application services
func (sc *SnapshotCache) RestoreSnapshot(
	ctx context.Context,
	sb *agent.Sandbox,
	hit *CacheHit,
) error {
	log := sc.logger.With().
		Str("snapshot_key", hit.Entry.SnapshotKey).
		Str("sandbox_id", sb.ID).
		Logger()

	log.Info().Msg("restoring filesystem snapshot")
	start := time.Now()

	// 1. Read the blob from worker local disk.
	tarData, err := os.ReadFile(hit.BlobPath)
	if err != nil {
		return fmt.Errorf("snapshot restore: read blob: %w", err)
	}

	// Verify SHA-256 checksum if a checksum file exists alongside the blob.
	checksumPath := hit.BlobPath + ".sha256"
	if expectedHex, readErr := os.ReadFile(checksumPath); readErr == nil { // #nosec G304 -- checksumPath is derived from validated BlobPath with a fixed suffix
		actualSum := sha256.Sum256(tarData)
		actualHex := hex.EncodeToString(actualSum[:])
		if actualHex != strings.TrimSpace(string(expectedHex)) {
			log.Error().
				Str("expected", strings.TrimSpace(string(expectedHex))).
				Str("actual", actualHex).
				Msg("snapshot checksum mismatch — deleting corrupted entry")
			_ = os.Remove(hit.BlobPath)
			_ = os.Remove(checksumPath)
			_ = sc.store.DeleteCache(ctx, hit.Entry.OrgID, hit.Entry.ID)
			return fmt.Errorf("snapshot restore: checksum mismatch for key %s", hit.Entry.SnapshotKey)
		}
	}

	// 2. Write the tar.gz into the sandbox.
	if err := sc.executor.WriteFile(ctx, sb, snapshotTmpFile, tarData); err != nil {
		return fmt.Errorf("snapshot restore: write tar to sandbox: %w", err)
	}

	// 3. Extract inside the sandbox.
	extractCmd := fmt.Sprintf("tar xzf %s -C %s", snapshotTmpFile, sb.WorkDir)

	var stderr bytes.Buffer
	exitCode, err := sc.executor.Exec(ctx, sb, extractCmd, io.Discard, &stderr)
	if err != nil {
		return fmt.Errorf("snapshot restore: exec tar: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("snapshot restore: tar exited %d: %s", exitCode, stderr.String())
	}

	// 4. Clean up the temporary file inside the sandbox.
	_, _ = sc.executor.Exec(ctx, sb, fmt.Sprintf("rm -f %s", snapshotTmpFile), io.Discard, io.Discard)

	// 5. Touch the cache entry to update LRU ordering.
	if err := sc.store.TouchCache(ctx, hit.Entry.OrgID, hit.Entry.ID); err != nil {
		log.Warn().Err(err).Msg("failed to touch cache entry after restore")
	}

	log.Info().
		Int64("size_bytes", hit.Entry.SizeBytes).
		Dur("elapsed_ms", time.Since(start)).
		Msg("filesystem snapshot restored")

	return nil
}

// =============================================================================
// ApplyPartialInvalidation
// =============================================================================

// ApplyPartialInvalidation handles the case where the lockfile and config are
// unchanged but the base commit has changed. It restores the cached snapshot
// and then applies a git diff on top to bring the workspace files up to date.
//
// This is faster than a full rebuild because node_modules and built assets are
// preserved — only the source files that changed between commits are patched.
//
// The gitDiff parameter should be the output of `git diff <old_commit>..<new_commit>`
// in unified diff format.
func (sc *SnapshotCache) ApplyPartialInvalidation(
	ctx context.Context,
	sb *agent.Sandbox,
	hit *CacheHit,
	gitDiff []byte,
) error {
	log := sc.logger.With().
		Str("snapshot_key", hit.Entry.SnapshotKey).
		Str("sandbox_id", sb.ID).
		Logger()

	log.Info().Int("diff_bytes", len(gitDiff)).Msg("applying partial invalidation")
	start := time.Now()

	// 1. Restore the base snapshot.
	if err := sc.RestoreSnapshot(ctx, sb, hit); err != nil {
		return fmt.Errorf("partial invalidation: restore base: %w", err)
	}

	// 2. If there is no diff, we are done (lockfile same, commit same after
	//    squash or rebase).
	if len(bytes.TrimSpace(gitDiff)) == 0 {
		log.Info().Msg("empty diff; snapshot is up to date")
		return nil
	}

	// 3. Write the diff into the sandbox.
	const diffTmpPath = "/tmp/partial.diff"
	if err := sc.executor.WriteFile(ctx, sb, diffTmpPath, gitDiff); err != nil {
		return fmt.Errorf("partial invalidation: write diff: %w", err)
	}

	// 4. Apply the diff. We use --allow-empty and ignore whitespace issues
	//    to be resilient to minor formatting changes. The -p1 strip count
	//    matches the standard `git diff` output format (a/path b/path).
	applyCmd := fmt.Sprintf(
		"cd %s && git apply --stat %s && git apply --allow-empty %s",
		sb.WorkDir,
		diffTmpPath,
		diffTmpPath,
	)

	var stderr bytes.Buffer
	exitCode, err := sc.executor.Exec(ctx, sb, applyCmd, io.Discard, &stderr)
	if err != nil {
		return fmt.Errorf("partial invalidation: exec git apply: %w", err)
	}
	if exitCode != 0 {
		// If git apply fails, the caller should fall back to a full rebuild.
		return fmt.Errorf("partial invalidation: git apply exited %d: %s", exitCode, stderr.String())
	}

	// 5. Clean up.
	_, _ = sc.executor.Exec(ctx, sb, fmt.Sprintf("rm -f %s", diffTmpPath), io.Discard, io.Discard)

	log.Info().
		Dur("elapsed_ms", time.Since(start)).
		Msg("partial invalidation applied")

	return nil
}

// =============================================================================
// EvictLRU
// =============================================================================

// EvictLRU removes the least-recently-used snapshot entries until the total
// cache size for this worker is within the configured limit. Both the local
// blob file and the database record are deleted.
//
// Entries are sorted by last_used_at ascending (oldest first) so the most
// stale snapshots are evicted first.
func (sc *SnapshotCache) EvictLRU(ctx context.Context) error {
	entries, err := sc.store.ListCacheByWorker(ctx, sc.workerNodeID)
	if err != nil {
		return fmt.Errorf("evict lru: list entries: %w", err)
	}

	total := totalSize(entries)
	if total <= sc.maxCacheBytes {
		return nil
	}

	// ListCacheByWorker returns entries sorted by last_used_at ASC, so the
	// first entries are the oldest. Sort defensively in case the store
	// implementation changes.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastUsedAt.Before(entries[j].LastUsedAt)
	})

	evicted := 0
	for _, entry := range entries {
		if total <= sc.maxCacheBytes {
			break
		}

		sc.logger.Info().
			Str("snapshot_key", entry.SnapshotKey).
			Int64("size_bytes", entry.SizeBytes).
			Time("last_used_at", entry.LastUsedAt).
			Msg("evicting snapshot")

		// Remove the blob and its checksum file from disk. Ignore errors
		// for files that are already gone (idempotent).
		if err := os.Remove(entry.BlobPath); err != nil && !os.IsNotExist(err) {
			sc.logger.Warn().Err(err).Str("blob_path", entry.BlobPath).Msg("failed to remove blob")
		}
		_ = os.Remove(entry.BlobPath + ".sha256") // best-effort cleanup of checksum file

		if err := sc.store.DeleteCache(ctx, entry.OrgID, entry.ID); err != nil {
			sc.logger.Warn().Err(err).Str("id", entry.ID.String()).Msg("failed to delete cache entry")
			continue
		}

		total -= entry.SizeBytes
		evicted++
	}

	if evicted > 0 {
		sc.logger.Info().
			Int("evicted", evicted).
			Int64("total_bytes_after", total).
			Msg("LRU eviction complete")
	}

	return nil
}

// =============================================================================
// TotalCacheSize
// =============================================================================

// TotalCacheSize returns the sum of SizeBytes for all cache entries belonging
// to this worker.
func (sc *SnapshotCache) TotalCacheSize(ctx context.Context) (int64, error) {
	entries, err := sc.store.ListCacheByWorker(ctx, sc.workerNodeID)
	if err != nil {
		return 0, fmt.Errorf("total cache size: %w", err)
	}
	return totalSize(entries), nil
}

// =============================================================================
// Helpers
// =============================================================================

// blobPath returns the local filesystem path for a snapshot blob.
// Snapshots are organized by the first two hex chars of the key to avoid
// putting too many files in a single directory.
func (sc *SnapshotCache) blobPath(snapshotKey string) string {
	prefix := snapshotKey[:2]
	return filepath.Join(sc.cacheDir, prefix, snapshotKey+".tar.gz")
}

// totalSize sums the SizeBytes of all entries.
func totalSize(entries []models.PreviewStartupCache) int64 {
	var total int64
	for _, e := range entries {
		total += e.SizeBytes
	}
	return total
}

// atomicWriteFile writes data to a file atomically by first writing to a
// temporary file in the same directory and then renaming. This prevents
// partially-written snapshots from being used if the process crashes.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".snapshot-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Ensure cleanup on error.
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp to final: %w", err)
	}

	// Rename succeeded — prevent deferred cleanup from removing the final file.
	tmpPath = ""
	return nil
}
