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
	// snapshot archive is staged during create/restore. tar auto-detects the
	// compression on extract, so the name is cosmetic — it does not have to
	// match the actual (zstd or legacy gzip) format of the bytes.
	snapshotTmpFile = "/tmp/snapshot.tar.zst"
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

	// WriteFileFromReader streams data to a file inside the sandbox filesystem.
	WriteFileFromReader(ctx context.Context, sb *agent.Sandbox, path string, reader io.Reader, sizeBytes int64) error
}

// =============================================================================
// Types
// =============================================================================

// SnapshotMetadata carries contextual information recorded alongside the
// snapshot for debugging and cache management. BaseKey and CommitSHA enable
// partial invalidation: a later start at a different commit with the same
// base key restores this snapshot and applies a git diff on top.
type SnapshotMetadata struct {
	OrgID     uuid.UUID
	RepoID    uuid.UUID
	BaseKey   string
	CommitSHA string
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
	if cfg.Executor == nil {
		return nil, fmt.Errorf("snapshot cache: executor must be non-nil")
	}
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

// ComputeSnapshotBaseKey is ComputeSnapshotKey without the commit: it hashes
// only the lockfile contents and config digest. Two previews with the same
// base key differ at most in source files, so a base snapshot plus the git
// diff between their commits reproduces the newer workspace.
func ComputeSnapshotBaseKey(lockfileContents []byte, configDigest string) string {
	h := sha256.New()
	h.Write(lockfileContents)
	h.Write([]byte{0}) // separator
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
	excludePaths []string,
) error {
	log := sc.logger.With().
		Str("snapshot_key", snapshotKey).
		Str("sandbox_id", sb.ID).
		Logger()

	log.Info().Msg("creating filesystem snapshot")
	start := time.Now()

	// 1. Create the archive inside the sandbox.
	//    Using shell tar is significantly faster than Go's archive/tar for
	//    large node_modules trees (parallel I/O, kernel-level buffering).
	//    Compression and the base exclude list are shared with the
	//    session-checkpoint path via the agent package; excludeGit=true because
	//    preview workspaces rebuild .git from the clone. excludePaths are the
	//    caller's per-config additions (runtime secret-file destinations and
	//    separately-restored build caches — see previewSnapshotExcludePaths).
	tarCmd := fmt.Sprintf(
		"tar -c %s -f %s -C %s %s%s -- .",
		agent.SnapshotTarCompressFlag,
		snapshotTmpFile,
		shellQuote(sb.WorkDir),
		agent.SnapshotTarExcludeFlags(true),
		snapshotExtraExcludeFlags(excludePaths),
	)

	var stderr bytes.Buffer
	exitCode, err := sc.executor.Exec(ctx, sb, tarCmd, io.Discard, &stderr)
	if err != nil {
		return fmt.Errorf("snapshot create: exec tar: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("snapshot create: tar exited %d: %s", exitCode, stderr.String())
	}

	// 2. Stream the tar.gz from the sandbox directly into a worker-local
	//    temp file. We tee through a SHA-256 hasher so the checksum is
	//    computed without a second pass, and through a counting writer so
	//    we can enforce the max blob size mid-stream rather than after
	//    buffering the whole archive in memory.
	const maxCreateBlobBytes int64 = 2 * 1024 * 1024 * 1024 // 2 GB (matches restore limit)

	blobPath, err := sc.blobPath(snapshotKey)
	if err != nil {
		return fmt.Errorf("snapshot create: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o750); err != nil {
		return fmt.Errorf("snapshot create: mkdir: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(blobPath), ".snapshot-*.tmp")
	if err != nil {
		return fmt.Errorf("snapshot create: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanupTmp := func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}
	defer cleanupTmp()

	hasher := sha256.New()
	counter := &cappedCountingWriter{limit: maxCreateBlobBytes}
	var tarStderr bytes.Buffer
	// Multiwriter fans out each chunk to: temp file, SHA-256 hasher, size
	// counter. If the counter trips, its Write returns an error that the
	// executor will surface back here, short-circuiting the stream.
	stream := io.MultiWriter(tmp, hasher, counter)

	readCmd := fmt.Sprintf("cat %s", shellQuote(snapshotTmpFile))
	exitCode, err = sc.executor.Exec(ctx, sb, readCmd, stream, &tarStderr)
	// Close the tmp file regardless of outcome so we can rename (or remove) it.
	if syncErr := tmp.Sync(); syncErr != nil && err == nil {
		err = fmt.Errorf("sync temp blob: %w", syncErr)
	}
	if closeErr := tmp.Close(); closeErr != nil && err == nil {
		err = fmt.Errorf("close temp blob: %w", closeErr)
	}
	if err != nil {
		return fmt.Errorf("snapshot create: stream tar from sandbox: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("snapshot create: cat exited %d: %s", exitCode, tarStderr.String())
	}
	if counter.exceeded {
		return fmt.Errorf("snapshot create: tar too large (>%d bytes max)", maxCreateBlobBytes)
	}

	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("snapshot create: chmod: %w", err)
	}
	if err := os.Rename(tmpPath, blobPath); err != nil {
		return fmt.Errorf("snapshot create: rename temp to final: %w", err)
	}
	// Rename succeeded — prevent deferred cleanup from removing the final file.
	tmpPath = ""

	checksumHex := hex.EncodeToString(hasher.Sum(nil))
	checksumPath := blobPath + ".sha256"
	if err := atomicWriteFile(checksumPath, []byte(checksumHex), 0o600); err != nil {
		// If checksum write fails, remove the blob so future lookups don't
		// return an unverifiable entry.
		_ = os.Remove(blobPath)
		return fmt.Errorf("snapshot create: write checksum: %w", err)
	}

	sizeBytes := counter.count

	// 4. Record in database.
	entry := &models.PreviewStartupCache{
		OrgID:        metadata.OrgID,
		RepoID:       metadata.RepoID,
		SnapshotKey:  snapshotKey,
		BaseKey:      metadata.BaseKey,
		CommitSHA:    metadata.CommitSHA,
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
	entry, err := sc.store.FindMatchingCache(ctx, orgID, repoID, snapshotKey, sc.workerNodeID)
	if err != nil {
		// pgx returns ErrNoRows when no match; treat as cache miss.
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, fmt.Errorf("snapshot find: query db: %w", err)
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
// FindBaseSnapshot
// =============================================================================

// FindBaseSnapshot checks whether a snapshot with the same base key (lockfiles
// + config digest) but a different commit exists on this worker's local disk.
// Returns nil (not an error) when no candidate is found. The caller restores
// the hit and applies the git diff from the entry's commit to the current one.
func (sc *SnapshotCache) FindBaseSnapshot(
	ctx context.Context,
	orgID, repoID uuid.UUID,
	baseKey, excludeCommitSHA string,
) (*CacheHit, error) {
	if baseKey == "" {
		return nil, nil
	}
	entry, err := sc.store.FindLatestCacheByBaseKey(ctx, orgID, repoID, baseKey, sc.workerNodeID, excludeCommitSHA)
	if err != nil {
		// pgx returns ErrNoRows when no match; treat as cache miss.
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, fmt.Errorf("snapshot base find: query db: %w", err)
	}

	// Verify the blob still exists on disk, mirroring FindSnapshot.
	if _, err := os.Stat(entry.BlobPath); os.IsNotExist(err) {
		sc.logger.Warn().
			Str("blob_path", entry.BlobPath).
			Msg("base snapshot blob missing from disk; cleaning up stale DB entry")
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

	// 1. Read the blob from worker local disk. The blob may have been evicted
	//    between FindSnapshot and this call (TOCTOU). Return a descriptive error
	//    so the caller can fall back to a full build.
	//
	//    Guard against reading excessively large files into memory by checking
	//    the file size against the DB-recorded size and a hard cap.
	const maxRestoreBlobBytes int64 = 2 * 1024 * 1024 * 1024 // 2 GB
	fi, statErr := os.Stat(hit.BlobPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			log.Warn().Str("blob_path", hit.BlobPath).Msg("snapshot blob missing, likely evicted between find and restore")
			_ = os.Remove(hit.BlobPath + ".sha256")
			_ = sc.store.DeleteCache(ctx, hit.Entry.OrgID, hit.Entry.ID)
			return fmt.Errorf("snapshot restore: blob evicted (key=%s), fall back to full build", hit.Entry.SnapshotKey)
		}
		return fmt.Errorf("snapshot restore: stat blob: %w", statErr)
	}
	if fi.Size() > maxRestoreBlobBytes {
		return fmt.Errorf("snapshot restore: blob too large (%d bytes, max %d)", fi.Size(), maxRestoreBlobBytes)
	}
	// Verify SHA-256 checksum if a checksum file exists alongside the blob.
	checksumPath := hit.BlobPath + ".sha256"
	expectedHex, readErr := os.ReadFile(checksumPath) // #nosec G304 -- checksumPath is derived from validated BlobPath with a fixed suffix
	if readErr == nil {
		actualHex, checksumErr := checksumFile(hit.BlobPath)
		if checksumErr != nil {
			if os.IsNotExist(checksumErr) {
				log.Warn().Str("blob_path", hit.BlobPath).Msg("snapshot blob missing, likely evicted during checksum")
				_ = os.Remove(checksumPath)
				_ = sc.store.DeleteCache(ctx, hit.Entry.OrgID, hit.Entry.ID)
				return fmt.Errorf("snapshot restore: blob evicted (key=%s), fall back to full build", hit.Entry.SnapshotKey)
			}
			return fmt.Errorf("snapshot restore: checksum blob: %w", checksumErr)
		}
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
	blob, err := os.Open(hit.BlobPath) // #nosec G304 -- BlobPath was validated by lookup and stat above
	if err != nil {
		if os.IsNotExist(err) {
			log.Warn().Str("blob_path", hit.BlobPath).Msg("snapshot blob missing, likely evicted before stream")
			_ = os.Remove(checksumPath)
			_ = sc.store.DeleteCache(ctx, hit.Entry.OrgID, hit.Entry.ID)
			return fmt.Errorf("snapshot restore: blob evicted (key=%s), fall back to full build", hit.Entry.SnapshotKey)
		}
		return fmt.Errorf("snapshot restore: open blob: %w", err)
	}
	defer func() {
		if closeErr := blob.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("blob_path", hit.BlobPath).Msg("failed to close snapshot blob")
		}
	}()
	if err := sc.executor.WriteFileFromReader(ctx, sb, snapshotTmpFile, blob, fi.Size()); err != nil {
		return fmt.Errorf("snapshot restore: stream tar to sandbox: %w", err)
	}

	// 3. Remove existing workspace files (except .git which is excluded from
	//    snapshots and reconstructed separately). Without this, files deleted
	//    after the snapshot was taken would survive the restore.
	cleanCmd := fmt.Sprintf(
		"find %s -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +",
		shellQuote(sb.WorkDir),
	)
	var cleanStderr bytes.Buffer
	if cleanExit, cleanErr := sc.executor.Exec(ctx, sb, cleanCmd, io.Discard, &cleanStderr); cleanErr != nil || cleanExit != 0 {
		log.Warn().Err(cleanErr).Int("exit_code", cleanExit).Str("stderr", cleanStderr.String()).
			Msg("failed to clean workspace before restore — proceeding with overlay extraction")
	}

	// 4. Extract inside the sandbox. `xf` (not `xzf`) so tar auto-detects the
	//    compression — restores both new zstd archives and pre-switch gzip ones.
	extractCmd := fmt.Sprintf("tar xf %s -C %s", snapshotTmpFile, shellQuote(sb.WorkDir))

	var stderr bytes.Buffer
	exitCode, err := sc.executor.Exec(ctx, sb, extractCmd, io.Discard, &stderr)
	if err != nil {
		return fmt.Errorf("snapshot restore: exec tar: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("snapshot restore: tar exited %d: %s", exitCode, stderr.String())
	}

	// 5. Clean up the temporary file inside the sandbox.
	_, _ = sc.executor.Exec(ctx, sb, fmt.Sprintf("rm -f %s", snapshotTmpFile), io.Discard, io.Discard)

	// 6. Touch the cache entry to update LRU ordering.
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
		shellQuote(sb.WorkDir),
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
func (sc *SnapshotCache) blobPath(snapshotKey string) (string, error) {
	// Validate key to prevent path traversal.
	if strings.Contains(snapshotKey, "..") || strings.ContainsAny(snapshotKey, "/\\") {
		return "", fmt.Errorf("invalid snapshot key %q: contains path traversal characters", snapshotKey)
	}
	prefix := "xx"
	if len(snapshotKey) >= 2 {
		prefix = snapshotKey[:2]
	}
	return filepath.Join(sc.cacheDir, prefix, snapshotKey+".tar.gz"), nil
}

// shellQuote wraps a string in single quotes, escaping any embedded single
// quotes. This prevents shell metacharacter injection when interpolating
// user-controlled paths into shell commands.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// snapshotExtraExcludeFlags renders additional `--exclude=` flags for the
// snapshot tar, appended after the shared base flags. It returns a string that
// is either empty or begins with a leading space, so it can be concatenated
// directly onto agent.SnapshotTarExcludeFlags(...).
//
// The archive is created with `tar -c -C workdir -- .`, so members are stored
// with a leading "./". Each path is emitted in both its bare ("foo/bar") and
// "./"-rooted ("./foo/bar") forms so the exclude matches regardless of how the
// tar implementation anchors patterns. Each flag is shell-quoted as a whole so
// any glob metacharacters in the path reach tar literally instead of being
// expanded by the surrounding `sh -c`.
func snapshotExtraExcludeFlags(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range paths {
		clean := strings.TrimSpace(p)
		if clean == "" {
			continue
		}
		clean = strings.TrimPrefix(clean, "./")
		if clean == "" || clean == "." {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(shellQuote("--exclude=" + clean))
		b.WriteByte(' ')
		b.WriteString(shellQuote("--exclude=./" + clean))
	}
	return b.String()
}

// totalSize sums the SizeBytes of all entries.
func totalSize(entries []models.PreviewStartupCache) int64 {
	var total int64
	for _, e := range entries {
		total += e.SizeBytes
	}
	return total
}

func checksumFile(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- callers pass cache-managed blob paths
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// cappedCountingWriter is an io.Writer that counts bytes written and fails
// the Write once the cumulative total exceeds limit. This lets the caller
// abort a streaming copy mid-flight instead of buffering the full payload
// just to measure it.
type cappedCountingWriter struct {
	count    int64
	limit    int64
	exceeded bool
}

func (w *cappedCountingWriter) Write(p []byte) (int, error) {
	w.count += int64(len(p))
	if w.limit > 0 && w.count > w.limit {
		w.exceeded = true
		return len(p), fmt.Errorf("snapshot blob exceeds max size %d bytes", w.limit)
	}
	return len(p), nil
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
