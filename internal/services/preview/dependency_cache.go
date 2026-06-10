package preview

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/storage"
)

const dependencyCacheMaxBlobBytes int64 = 2 * 1024 * 1024 * 1024
const dependencyCacheTouchInterval = 10 * time.Minute

type DependencyCache interface {
	Find(ctx context.Context, orgID, repoID uuid.UUID, cacheKey string) (*DependencyCacheHit, error)
	Restore(ctx context.Context, sb *agent.Sandbox, hit *DependencyCacheHit) error
	Save(ctx context.Context, sb *agent.Sandbox, cacheKey string, paths []string, metadata DependencyCacheMetadata) (DependencyCacheSaveResult, error)
}

type DependencyCacheHit struct {
	Entry   models.PreviewDependencyCache
	BlobKey string
}

type DependencyCacheSaveResult struct {
	SizeBytes int64
}

type DependencyCacheMetadata struct {
	OrgID               uuid.UUID                   `json:"org_id"`
	RepoID              uuid.UUID                   `json:"repo_id"`
	SessionID           uuid.UUID                   `json:"session_id"`
	PlacementKey        string                      `json:"placement_key"`
	InstallCommand      []string                    `json:"install_command"`
	EffectivePaths      []string                    `json:"effective_paths"`
	LockfileHashes      map[string]string           `json:"lockfile_hashes"`
	ChecksumSHA256      string                      `json:"checksum_sha256"`
	Lockfiles           []PreviewInstallLockfileKey `json:"lockfiles,omitempty"`
	ArchiveBytes        int64                       `json:"archive_bytes,omitempty"`
	ArchivePayloadBytes int64                       `json:"archive_payload_bytes,omitempty"`
	ArchiveFileCount    int64                       `json:"archive_file_count,omitempty"`
}

type DependencyCacheConfig struct {
	Store         *db.PreviewStore
	Executor      SnapshotExecutor
	BlobStore     storage.SnapshotStore
	Logger        zerolog.Logger
	WorkerNodeID  string
	Prefix        string
	LocalDir      string
	LocalMaxBytes int64
}

type SharedDependencyCache struct {
	store         *db.PreviewStore
	executor      SnapshotExecutor
	blobStore     storage.SnapshotStore
	logger        zerolog.Logger
	workerNodeID  string
	prefix        string
	localDir      string
	localMaxBytes int64
}

type dependencyCacheStdinExecutor interface {
	ExecWithStdin(ctx context.Context, sb *agent.Sandbox, cmd string, stdin io.Reader, stdout, stderr io.Writer) (int, error)
}

type dependencyCacheStagedBlob struct {
	path      string
	sizeBytes int64
	checksum  string
	fromLocal bool
	file      *os.File
	cleanup   func()
}

func (b *dependencyCacheStagedBlob) rewind() error {
	if b.file == nil {
		return fmt.Errorf("dependency cache staged blob reader is not available")
	}
	if _, err := b.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind dependency cache staged blob: %w", err)
	}
	return nil
}

func NewDependencyCache(cfg DependencyCacheConfig) (*SharedDependencyCache, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("dependency cache: store must be non-nil")
	}
	if cfg.Executor == nil {
		return nil, fmt.Errorf("dependency cache: executor must be non-nil")
	}
	if cfg.BlobStore == nil {
		return nil, fmt.Errorf("dependency cache: blob store must be non-nil")
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	if prefix == "" {
		prefix = "preview-dependency-cache"
	}
	if cfg.LocalDir != "" {
		if err := os.MkdirAll(cfg.LocalDir, 0o750); err != nil {
			return nil, fmt.Errorf("dependency cache: create local dir: %w", err)
		}
	}
	return &SharedDependencyCache{
		store:         cfg.Store,
		executor:      cfg.Executor,
		blobStore:     cfg.BlobStore,
		logger:        cfg.Logger.With().Str("component", "preview_dependency_cache").Logger(),
		workerNodeID:  cfg.WorkerNodeID,
		prefix:        prefix,
		localDir:      cfg.LocalDir,
		localMaxBytes: cfg.LocalMaxBytes,
	}, nil
}

func (c *SharedDependencyCache) Find(ctx context.Context, orgID, repoID uuid.UUID, cacheKey string) (*DependencyCacheHit, error) {
	entry, err := c.store.FindDependencyCache(ctx, orgID, repoID, cacheKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if entry.BlobKey == "" {
		entry.BlobKey = c.blobKey(orgID, repoID, cacheKey)
	}
	return &DependencyCacheHit{Entry: *entry, BlobKey: entry.BlobKey}, nil
}

func (c *SharedDependencyCache) Restore(ctx context.Context, sb *agent.Sandbox, hit *DependencyCacheHit) error {
	if hit == nil {
		return fmt.Errorf("dependency cache restore: hit is required")
	}
	var metadata DependencyCacheMetadata
	if err := json.Unmarshal(hit.Entry.Metadata, &metadata); err != nil {
		return fmt.Errorf("dependency cache restore: parse metadata: %w", err)
	}
	paths := sortedNormalizedDependencyPaths(metadata.EffectivePaths)
	if len(paths) == 0 {
		return fmt.Errorf("dependency cache restore: metadata has no effective paths")
	}
	for _, p := range paths {
		if _, err := cleanDependencyCacheRepoPath(p, true); err != nil {
			return fmt.Errorf("dependency cache restore: invalid metadata path %q: %w", p, err)
		}
	}
	if hit.Entry.SizeBytes > dependencyCacheMaxBlobBytes {
		return fmt.Errorf("dependency cache restore: blob too large (%d bytes, max %d); narrow preview.install.cache.paths or disable dependency caching for this preview", hit.Entry.SizeBytes, dependencyCacheMaxBlobBytes)
	}
	blob, err := c.stageBlob(ctx, hit)
	if err != nil {
		if errors.Is(err, storage.ErrSnapshotNotFound) {
			if deleteErr := c.store.DeleteDependencyCache(ctx, hit.Entry.OrgID, hit.Entry.ID); deleteErr != nil {
				c.logger.Warn().Err(deleteErr).Str("cache_key", hit.Entry.CacheKey).Msg("failed to delete stale dependency cache metadata after missing blob")
			}
		}
		return err
	}
	defer blob.cleanup()
	if metadata.ChecksumSHA256 != "" && !strings.EqualFold(metadata.ChecksumSHA256, blob.checksum) {
		if blob.fromLocal {
			c.removeLocalBlob(ctx, hit.Entry.CacheKey)
		}
		if deleteErr := c.store.DeleteDependencyCache(ctx, hit.Entry.OrgID, hit.Entry.ID); deleteErr != nil {
			c.logger.Warn().Err(deleteErr).Str("cache_key", hit.Entry.CacheKey).Msg("failed to delete corrupted dependency cache metadata")
		}
		return fmt.Errorf("dependency cache restore: checksum mismatch")
	}
	if err := blob.rewind(); err != nil {
		return fmt.Errorf("dependency cache restore: %w", err)
	}
	stats, err := validateDependencyCacheArchiveReader(blob.file, paths)
	if err != nil {
		if blob.fromLocal {
			c.removeLocalBlob(ctx, hit.Entry.CacheKey)
		}
		return fmt.Errorf("dependency cache restore: validate archive: %w", err)
	}
	c.logger.Debug().
		Str("cache_key", hit.Entry.CacheKey).
		Int64("archive_payload_bytes", stats.payloadBytes).
		Int64("archive_file_count", stats.fileCount).
		Msg("dependency cache restore archive validated")
	cleanArgs := make([]string, 0, len(paths))
	for _, p := range paths {
		clean, err := cleanDependencyCacheRepoPath(p, true)
		if err != nil {
			return fmt.Errorf("dependency cache restore: clean path %q: %w", p, err)
		}
		cleanArgs = append(cleanArgs, dependencyCacheShellPathArg(clean))
	}
	cleanCmd := fmt.Sprintf("cd %s && rm -rf -- %s", shellQuote(sb.WorkDir), strings.Join(cleanArgs, " "))
	if exitCode, err := c.executor.Exec(ctx, sb, cleanCmd, io.Discard, io.Discard); err != nil || exitCode != 0 {
		return fmt.Errorf("dependency cache restore: remove existing paths exited %d: %w", exitCode, err)
	}
	if err := blob.rewind(); err != nil {
		return fmt.Errorf("dependency cache restore: %w", err)
	}
	if exitCode, err := c.extractSandboxArchive(ctx, sb, blob.file); err != nil || exitCode != 0 {
		return fmt.Errorf("dependency cache restore: extract exited %d: %w", exitCode, err)
	}
	if !blob.fromLocal {
		c.writeLocalBlobFromFile(ctx, hit, blob.path, blob.sizeBytes, blob.checksum)
	}
	if time.Since(hit.Entry.LastUsedAt) >= dependencyCacheTouchInterval {
		if err := c.store.TouchDependencyCache(ctx, hit.Entry.OrgID, hit.Entry.ID); err != nil {
			c.logger.Warn().Err(err).Str("cache_key", hit.Entry.CacheKey).Msg("failed to touch dependency cache")
		}
	}
	return nil
}

func (c *SharedDependencyCache) Save(ctx context.Context, sb *agent.Sandbox, cacheKey string, paths []string, metadata DependencyCacheMetadata) (DependencyCacheSaveResult, error) {
	effective := sortedNormalizedDependencyPaths(paths)
	if len(effective) == 0 {
		return DependencyCacheSaveResult{}, nil
	}
	existing := make([]string, 0, len(effective))
	for _, p := range effective {
		clean, err := cleanDependencyCacheRepoPath(p, true)
		if err != nil {
			return DependencyCacheSaveResult{}, fmt.Errorf("dependency cache save: invalid path %q: %w", p, err)
		}
		existsCmd := "test -e " + dependencyCacheShellPathArg(filepath.ToSlash(clean))
		if strings.Contains(clean, "*") {
			existsCmd = "find " + shellQuote(sb.WorkDir) + " -path " + shellQuote(filepath.ToSlash(filepath.Join(sb.WorkDir, clean))) + " -print -quit | grep -q ."
		}
		exitCode, err := c.executor.Exec(ctx, sb, existsCmd, io.Discard, io.Discard)
		if err == nil && exitCode == 0 {
			existing = append(existing, clean)
		}
	}
	if len(existing) == 0 {
		c.logger.Debug().Str("cache_key", cacheKey).Msg("dependency cache save skipped: no effective paths exist")
		return DependencyCacheSaveResult{}, nil
	}
	args := make([]string, 0, len(existing))
	for _, p := range existing {
		args = append(args, dependencyCacheShellPathArg(p))
	}
	archiveCmd := fmt.Sprintf("cd %s && tar czf - -- %s", shellQuote(sb.WorkDir), strings.Join(args, " "))
	var stderr bytes.Buffer
	staged, err := c.stageSandboxArchive(ctx, sb, archiveCmd, &stderr)
	if err != nil {
		return DependencyCacheSaveResult{}, err
	}
	defer staged.cleanup()
	stats, err := validateDependencyCacheArchive(staged.path, existing)
	if err != nil {
		return DependencyCacheSaveResult{}, fmt.Errorf("dependency cache save: validate archive: %w", err)
	}
	metadata.EffectivePaths = existing
	metadata.ChecksumSHA256 = staged.checksum
	metadata.ArchiveBytes = staged.sizeBytes
	metadata.ArchivePayloadBytes = stats.payloadBytes
	metadata.ArchiveFileCount = stats.fileCount
	if metadata.PlacementKey == "" {
		placementKey, err := ComputePreviewDependencyCachePlacementKey(metadata.OrgID, metadata.RepoID, "", "", &models.PreviewInstallConfig{Command: metadata.InstallCommand}, existing)
		if err == nil {
			metadata.PlacementKey = placementKey
		}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return DependencyCacheSaveResult{}, fmt.Errorf("dependency cache save: marshal metadata: %w", err)
	}
	blobKey := c.blobKeyForChecksum(metadata.OrgID, metadata.RepoID, cacheKey, staged.checksum)
	// Concurrent saves for the same key are intentionally lock-free. Blob
	// objects are checksum-addressed so each DB upsert points at the exact
	// payload whose checksum is recorded in metadata.
	file, err := os.Open(staged.path) // #nosec G304 -- staged path was created by this process.
	if err != nil {
		return DependencyCacheSaveResult{}, fmt.Errorf("dependency cache save: open staged blob: %w", err)
	}
	if err := c.blobStore.Save(ctx, blobKey, file); err != nil {
		_ = file.Close()
		return DependencyCacheSaveResult{}, fmt.Errorf("dependency cache save: upload blob: %w", err)
	}
	if err := file.Close(); err != nil {
		return DependencyCacheSaveResult{}, fmt.Errorf("dependency cache save: close staged blob: %w", err)
	}
	if err := c.blobStore.Save(ctx, blobKey+".sha256", strings.NewReader(staged.checksum)); err != nil {
		return DependencyCacheSaveResult{}, fmt.Errorf("dependency cache save: upload checksum: %w", err)
	}
	entry := &models.PreviewDependencyCache{
		OrgID:        metadata.OrgID,
		RepoID:       metadata.RepoID,
		CacheKey:     cacheKey,
		PlacementKey: metadata.PlacementKey,
		BlobKey:      blobKey,
		SizeBytes:    staged.sizeBytes,
		Metadata:     metadataJSON,
	}
	if err := c.store.UpsertDependencyCache(ctx, entry); err != nil {
		return DependencyCacheSaveResult{}, fmt.Errorf("dependency cache save: upsert db: %w", err)
	}
	c.writeLocalBlobFromFile(ctx, &DependencyCacheHit{Entry: *entry, BlobKey: blobKey}, staged.path, staged.sizeBytes, staged.checksum)
	return DependencyCacheSaveResult{SizeBytes: staged.sizeBytes}, nil
}

func (c *SharedDependencyCache) stageBlob(ctx context.Context, hit *DependencyCacheHit) (*dependencyCacheStagedBlob, error) {
	if c.localDir != "" {
		localPath := c.localBlobPath(hit.Entry.CacheKey)
		if blob, err := c.stageLocalBlob(localPath); err == nil {
			if err := os.Chtimes(localPath, time.Now(), time.Now()); err != nil {
				c.logger.Warn().Err(err).Str("path", localPath).Msg("failed to touch dependency cache local blob")
			}
			return blob, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			c.logger.Warn().Err(err).Str("path", localPath).Msg("failed to read dependency cache local blob; falling back to object storage")
		}
	}
	dir, err := os.MkdirTemp("", "preview-dependency-cache-*")
	if err != nil {
		return nil, fmt.Errorf("dependency cache restore: temp dir: %w", err)
	}
	path := filepath.Join(dir, "blob.tar.gz")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- path is under a private temp dir.
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("dependency cache restore: temp blob: %w", err)
	}
	hasher := sha256.New()
	counter := &cappedCountingWriter{limit: dependencyCacheMaxBlobBytes}
	writer := io.MultiWriter(file, hasher, counter)
	loadErr := c.blobStore.Load(ctx, hit.BlobKey, writer)
	closeErr := file.Close()
	if loadErr != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("dependency cache restore: load blob: %w", loadErr)
	}
	if closeErr != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("dependency cache restore: close temp blob: %w", closeErr)
	}
	if counter.exceeded {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("dependency cache restore: blob too large (>%d bytes max)", dependencyCacheMaxBlobBytes)
	}
	readFile, err := os.Open(path) // #nosec G304 -- path is under a private temp dir.
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("dependency cache restore: open staged blob: %w", err)
	}
	return &dependencyCacheStagedBlob{
		path:      path,
		sizeBytes: counter.count,
		checksum:  hex.EncodeToString(hasher.Sum(nil)),
		fromLocal: false,
		file:      readFile,
		cleanup: func() {
			_ = readFile.Close()
			_ = os.RemoveAll(dir)
		},
	}, nil
}

func (c *SharedDependencyCache) stageLocalBlob(path string) (*dependencyCacheStagedBlob, error) {
	file, err := os.Open(path) // #nosec G304 -- path is derived from localBlobPath.
	if err != nil {
		return nil, err
	}
	fi, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if fi.Size() > dependencyCacheMaxBlobBytes {
		_ = file.Close()
		return nil, fmt.Errorf("dependency cache restore: local blob too large (%d bytes, max %d)", fi.Size(), dependencyCacheMaxBlobBytes)
	}
	hasher := sha256.New()
	counter := &cappedCountingWriter{limit: dependencyCacheMaxBlobBytes}
	if _, err := io.Copy(io.MultiWriter(hasher, counter), file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if counter.exceeded {
		_ = file.Close()
		return nil, fmt.Errorf("dependency cache restore: local blob too large (>%d bytes max)", dependencyCacheMaxBlobBytes)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("dependency cache restore: rewind local blob: %w", err)
	}
	return &dependencyCacheStagedBlob{
		path:      path,
		sizeBytes: counter.count,
		checksum:  hex.EncodeToString(hasher.Sum(nil)),
		fromLocal: true,
		file:      file,
		cleanup:   func() { _ = file.Close() },
	}, nil
}

func (c *SharedDependencyCache) stageSandboxArchive(ctx context.Context, sb *agent.Sandbox, archiveCmd string, stderr io.Writer) (*dependencyCacheStagedBlob, error) {
	dir, err := os.MkdirTemp("", "preview-dependency-cache-save-*")
	if err != nil {
		return nil, fmt.Errorf("dependency cache save: temp dir: %w", err)
	}
	path := filepath.Join(dir, "blob.tar.gz")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- path is under a private temp dir.
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("dependency cache save: temp blob: %w", err)
	}
	hasher := sha256.New()
	counter := &cappedCountingWriter{limit: dependencyCacheMaxBlobBytes}
	stream := io.MultiWriter(file, hasher, counter)
	exitCode, execErr := c.executor.Exec(ctx, sb, archiveCmd, stream, stderr)
	closeErr := file.Close()
	if execErr != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("dependency cache save: archive stream exited %d: %w", exitCode, execErr)
	}
	if exitCode != 0 {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("dependency cache save: archive stream exited %d", exitCode)
	}
	if closeErr != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("dependency cache save: close temp blob: %w", closeErr)
	}
	if counter.exceeded {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("dependency cache save: archive too large (>%d bytes max)", dependencyCacheMaxBlobBytes)
	}
	return &dependencyCacheStagedBlob{
		path:      path,
		sizeBytes: counter.count,
		checksum:  hex.EncodeToString(hasher.Sum(nil)),
		fromLocal: false,
		cleanup:   func() { _ = os.RemoveAll(dir) },
	}, nil
}

type dependencyCacheArchiveStats struct {
	payloadBytes int64
	fileCount    int64
}

func validateDependencyCacheArchive(localPath string, paths []string) (dependencyCacheArchiveStats, error) {
	file, err := os.Open(localPath) // #nosec G304 -- localPath is staged by dependency cache.
	if err != nil {
		return dependencyCacheArchiveStats{}, err
	}
	defer file.Close()
	return validateDependencyCacheArchiveReader(file, paths)
}

func validateDependencyCacheArchiveReader(reader io.Reader, paths []string) (dependencyCacheArchiveStats, error) {
	gzr, err := gzip.NewReader(reader)
	if err != nil {
		return dependencyCacheArchiveStats{}, fmt.Errorf("open gzip stream: %w", err)
	}
	defer gzr.Close()
	allowed := sortedNormalizedDependencyPaths(paths)
	if len(allowed) == 0 {
		return dependencyCacheArchiveStats{}, fmt.Errorf("no effective paths")
	}
	tr := tar.NewReader(gzr)
	var stats dependencyCacheArchiveStats
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return dependencyCacheArchiveStats{}, fmt.Errorf("read tar header: %w", err)
		}
		name, err := cleanDependencyCacheArchiveName(header.Name)
		if err != nil {
			return dependencyCacheArchiveStats{}, err
		}
		if dependencyCachePathTargetsPreviewInstallMarkers(name) {
			return dependencyCacheArchiveStats{}, fmt.Errorf("archive entry %q must not target preview install markers", header.Name)
		}
		if !dependencyCacheArchiveNameAllowed(name, allowed) {
			return dependencyCacheArchiveStats{}, fmt.Errorf("archive entry %q is outside effective cache paths", header.Name)
		}
		if header.Size > 0 {
			stats.payloadBytes += header.Size
		}
		if header.Typeflag == tar.TypeReg {
			stats.fileCount++
		}
	}
	return stats, nil
}

func cleanDependencyCacheArchiveName(raw string) (string, error) {
	name := filepath.ToSlash(strings.TrimSpace(raw))
	if name == "" {
		return "", fmt.Errorf("archive entry has empty path")
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("archive entry %q uses an absolute path", raw)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("archive entry %q escapes the repo root", raw)
	}
	return clean, nil
}

func dependencyCacheArchiveNameAllowed(name string, allowed []string) bool {
	for _, candidate := range allowed {
		if dependencyCacheArchiveNameMatchesPath(name, candidate) {
			return true
		}
	}
	return false
}

func dependencyCacheArchiveNameMatchesPath(name, allowed string) bool {
	if !strings.Contains(allowed, "*") {
		return name == allowed || strings.HasPrefix(name, allowed+"/")
	}
	parts := strings.Split(name, "/")
	for i := 1; i <= len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		if ok, err := path.Match(allowed, prefix); err == nil && ok {
			return true
		}
	}
	return false
}

func (c *SharedDependencyCache) extractSandboxArchive(ctx context.Context, sb *agent.Sandbox, reader io.Reader) (int, error) {
	executor, ok := c.executor.(dependencyCacheStdinExecutor)
	if !ok {
		return -1, fmt.Errorf("executor does not support streaming dependency cache restore")
	}
	cmd := fmt.Sprintf("tar xzf - -C %s", shellQuote(sb.WorkDir))
	return executor.ExecWithStdin(ctx, sb, cmd, reader, io.Discard, io.Discard)
}

func (c *SharedDependencyCache) writeLocalBlobFromFile(ctx context.Context, hit *DependencyCacheHit, sourcePath string, sizeBytes int64, checksum string) {
	if c.localDir == "" || c.workerNodeID == "" || hit == nil {
		return
	}
	path := c.localBlobPath(hit.Entry.CacheKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		c.logger.Warn().Err(err).Msg("failed to create dependency cache local dir")
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dependency-cache-*.tmp")
	if err != nil {
		c.logger.Warn().Err(err).Msg("failed to create dependency cache local temp blob")
		return
	}
	tmpPath := tmp.Name()
	source, err := os.Open(sourcePath) // #nosec G304 -- sourcePath is staged by dependency cache.
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		c.logger.Warn().Err(err).Msg("failed to open dependency cache source blob")
		return
	}
	_, copyErr := io.Copy(tmp, source)
	sourceCloseErr := source.Close()
	closeErr := tmp.Close()
	if copyErr != nil || sourceCloseErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		c.logger.Warn().Err(errors.Join(copyErr, sourceCloseErr, closeErr)).Msg("failed to write dependency cache local blob")
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(tmpPath); removeErr != nil {
			c.logger.Warn().Err(removeErr).Str("tmp_path", tmpPath).Msg("failed to remove dependency cache temp blob")
		}
		c.logger.Warn().Err(err).Msg("failed to publish dependency cache local blob")
		return
	}
	if checksum != "" {
		if err := atomicWriteFile(path+".sha256", []byte(checksum), 0o600); err != nil {
			c.logger.Warn().Err(err).Msg("failed to write dependency cache local checksum")
		}
	}
	location := &models.PreviewDependencyCacheLocation{
		OrgID:        hit.Entry.OrgID,
		RepoID:       hit.Entry.RepoID,
		CacheKey:     hit.Entry.CacheKey,
		PlacementKey: hit.Entry.PlacementKey,
		WorkerNodeID: c.workerNodeID,
		SizeBytes:    sizeBytes,
	}
	if err := c.store.UpsertDependencyCacheLocation(ctx, location); err != nil {
		c.logger.Warn().Err(err).Str("cache_key", hit.Entry.CacheKey).Msg("failed to upsert dependency cache location")
	}
	if err := c.evictLocalLRU(ctx); err != nil {
		c.logger.Warn().Err(err).Msg("failed to evict dependency cache local LRU")
	}
}

func (c *SharedDependencyCache) blobKey(orgID, repoID uuid.UUID, cacheKey string) string {
	return fmt.Sprintf("%s/%s/%s/%s.tar.gz", c.prefix, orgID, repoID, cacheKey)
}

func (c *SharedDependencyCache) blobKeyForChecksum(orgID, repoID uuid.UUID, cacheKey, checksum string) string {
	if checksum == "" {
		return c.blobKey(orgID, repoID, cacheKey)
	}
	return fmt.Sprintf("%s/%s/%s/%s/%s.tar.gz", c.prefix, orgID, repoID, cacheKey, checksum)
}

func (c *SharedDependencyCache) localBlobPath(cacheKey string) string {
	prefix := cacheKey
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return filepath.Join(c.localDir, prefix, cacheKey+".tar.gz")
}

func (c *SharedDependencyCache) removeLocalBlob(ctx context.Context, cacheKey string) {
	if c.localDir == "" {
		return
	}
	path := c.localBlobPath(cacheKey)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		c.logger.Warn().Err(err).Str("path", path).Msg("failed to remove dependency cache local blob")
	}
	if err := os.Remove(path + ".sha256"); err != nil && !os.IsNotExist(err) {
		c.logger.Warn().Err(err).Str("path", path+".sha256").Msg("failed to remove dependency cache local checksum")
	}
	if c.workerNodeID != "" {
		if err := c.store.DeleteDependencyCacheLocationByWorkerCacheKey(ctx, c.workerNodeID, cacheKey); err != nil {
			c.logger.Warn().Err(err).Str("cache_key", cacheKey).Msg("failed to delete dependency cache local location")
		}
	}
}

func (c *SharedDependencyCache) evictLocalLRU(ctx context.Context) error {
	if c.localDir == "" || c.localMaxBytes <= 0 {
		return nil
	}
	entries, total, err := c.localBlobEntries()
	if err != nil {
		return err
	}
	if total <= c.localMaxBytes {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].modTime.Equal(entries[j].modTime) {
			return entries[i].path < entries[j].path
		}
		return entries[i].modTime.Before(entries[j].modTime)
	})
	for _, entry := range entries {
		if total <= c.localMaxBytes {
			break
		}
		if err := os.Remove(entry.path); err != nil && !os.IsNotExist(err) {
			c.logger.Warn().Err(err).Str("path", entry.path).Msg("failed to evict dependency cache local blob")
			continue
		}
		if err := os.Remove(entry.path + ".sha256"); err != nil && !os.IsNotExist(err) {
			c.logger.Warn().Err(err).Str("path", entry.path+".sha256").Msg("failed to evict dependency cache local checksum")
		}
		total -= entry.sizeBytes
		if c.workerNodeID != "" {
			if err := c.store.DeleteDependencyCacheLocationByWorkerCacheKey(ctx, c.workerNodeID, entry.cacheKey); err != nil {
				c.logger.Warn().Err(err).Str("cache_key", entry.cacheKey).Msg("failed to delete evicted dependency cache location")
			}
		}
	}
	return nil
}

type dependencyCacheLocalEntry struct {
	path      string
	cacheKey  string
	sizeBytes int64
	modTime   time.Time
}

func (c *SharedDependencyCache) localBlobEntries() ([]dependencyCacheLocalEntry, int64, error) {
	var entries []dependencyCacheLocalEntry
	var total int64
	err := filepath.WalkDir(c.localDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".tar.gz") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		cacheKey := strings.TrimSuffix(d.Name(), ".tar.gz")
		entries = append(entries, dependencyCacheLocalEntry{
			path:      path,
			cacheKey:  cacheKey,
			sizeBytes: info.Size(),
			modTime:   info.ModTime(),
		})
		total += info.Size()
		return nil
	})
	return entries, total, err
}

func buildDependencyCacheExtractCommand(workDir, tmpPath string, paths []string) string {
	allowed := make([]string, 0, len(paths))
	for _, p := range sortedNormalizedDependencyPaths(paths) {
		clean, err := cleanDependencyCacheRepoPath(p, true)
		if err != nil {
			continue
		}
		allowed = append(allowed, "^"+awkGlobRegex(clean)+"(/|$)")
	}
	allowedExpr := "$0 ~ /a^/"
	if len(allowed) > 0 {
		allowedExpr = "$0 ~ /(" + strings.Join(allowed, "|") + ")/"
	}
	awk := fmt.Sprintf(`BEGIN{bad=0} /^\// || /(^|\/)\.\.($|\/)/ {bad=1} !(%s) {bad=1} END{exit bad}`, allowedExpr)
	return fmt.Sprintf("cd %s && tar tzf %s | awk %s && tar xzf %s -C %s",
		shellQuote(workDir), shellQuote(tmpPath), shellQuote(awk), shellQuote(tmpPath), shellQuote(workDir))
}

func awkRegexEscape(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`.`, `\.`,
		`+`, `\+`,
		`?`, `\?`,
		`(`, `\(`,
		`)`, `\)`,
		`[`, `\[`,
		`]`, `\]`,
		`{`, `\{`,
		`}`, `\}`,
		`^`, `\^`,
		`$`, `\$`,
		`|`, `\|`,
		`/`, `\/`,
	)
	return replacer.Replace(value)
}

func awkGlobRegex(value string) string {
	escaped := awkRegexEscape(value)
	return strings.ReplaceAll(escaped, `*`, `[^/]*`)
}

func dependencyCacheShellPathArg(value string) string {
	if strings.Contains(value, "*") {
		return value
	}
	return shellQuote(value)
}
