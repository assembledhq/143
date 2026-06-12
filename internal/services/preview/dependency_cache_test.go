package preview

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/storage"
)

type dependencyCacheExec struct {
	payload        []byte
	execCalls      []string
	writeFiles     map[string][]byte
	readersWritten map[string][]byte
	mu             sync.Mutex
}

type evictingDependencyCacheExec struct {
	*dependencyCacheExec
	localPath string
}

func (e *evictingDependencyCacheExec) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	if strings.Contains(cmd, "rm -rf --") {
		if err := os.Remove(e.localPath); err != nil && !os.IsNotExist(err) {
			return -1, err
		}
	}
	return e.dependencyCacheExec.Exec(ctx, sb, cmd, stdout, stderr)
}

func (e *dependencyCacheExec) Exec(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	e.mu.Lock()
	e.execCalls = append(e.execCalls, cmd)
	e.mu.Unlock()
	switch {
	case strings.Contains(cmd, "printf '%s\\n'"):
		parts := strings.Split(cmd, "printf '%s\\n' ")
		for _, part := range parts[1:] {
			part = strings.TrimSpace(part)
			if !strings.HasPrefix(part, "'") {
				continue
			}
			rest := strings.TrimPrefix(part, "'")
			end := strings.Index(rest, "'")
			if end < 0 {
				continue
			}
			_, err := stdout.Write([]byte(rest[:end] + "\n"))
			if err != nil {
				return -1, err
			}
		}
		return 0, nil
	case strings.HasPrefix(cmd, "test -e "), strings.Contains(cmd, " && test -e "):
		return 0, nil
	case strings.Contains(cmd, "tar czf -"):
		_, err := stdout.Write(e.payload)
		return 0, err
	case strings.Contains(cmd, "tar tzf"):
		if stderr != nil {
			_, _ = stderr.Write([]byte("validated\n"))
		}
		return 0, nil
	case strings.Contains(cmd, "tar xzf"):
		return 0, nil
	case strings.Contains(cmd, "tar czf "):
		return 0, nil
	case strings.HasPrefix(cmd, "cat "):
		_, err := stdout.Write(e.payload)
		return 0, err
	case strings.Contains(cmd, "rm -rf --"):
		return 0, nil
	case strings.HasPrefix(cmd, "rm -f "):
		return 0, nil
	default:
		return 1, nil
	}
}

func (e *dependencyCacheExec) ReadFile(context.Context, *agent.Sandbox, string) ([]byte, error) {
	return nil, nil
}

func (e *dependencyCacheExec) WriteFileFromReader(_ context.Context, _ *agent.Sandbox, path string, reader io.Reader, _ int64) error {
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.readersWritten == nil {
		e.readersWritten = make(map[string][]byte)
	}
	e.readersWritten[path] = append([]byte(nil), body...)
	return nil
}

func (e *dependencyCacheExec) ExecWithStdin(_ context.Context, _ *agent.Sandbox, cmd string, stdin io.Reader, _, _ io.Writer) (int, error) {
	body, err := io.ReadAll(stdin)
	if err != nil {
		return -1, err
	}
	e.mu.Lock()
	e.execCalls = append(e.execCalls, cmd)
	if e.readersWritten == nil {
		e.readersWritten = make(map[string][]byte)
	}
	e.readersWritten["stdin:"+cmd] = append([]byte(nil), body...)
	e.mu.Unlock()
	return 0, nil
}

func (e *dependencyCacheExec) WriteFile(_ context.Context, _ *agent.Sandbox, path string, body []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.writeFiles == nil {
		e.writeFiles = make(map[string][]byte)
	}
	e.writeFiles[path] = append([]byte(nil), body...)
	return nil
}

func (e *dependencyCacheExec) calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.execCalls...)
}

func (e *dependencyCacheExec) writtenFilePaths() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	paths := make([]string, 0, len(e.writeFiles)+len(e.readersWritten))
	for path := range e.writeFiles {
		paths = append(paths, path)
	}
	for path := range e.readersWritten {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

type memorySnapshotStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

type dependencyCacheFailingLoadStore struct{}

func (s dependencyCacheFailingLoadStore) Save(context.Context, string, io.Reader) error {
	return nil
}

func (s dependencyCacheFailingLoadStore) Load(context.Context, string, io.Writer) error {
	return fmt.Errorf("load should be skipped by size preflight")
}

func (s dependencyCacheFailingLoadStore) Delete(context.Context, string) error {
	return nil
}

func makeDependencyCacheTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, body := range files {
		data := []byte(body)
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o600,
			Size: int64(len(data)),
		}), "test archive header should be written")
		_, err := tw.Write(data)
		require.NoError(t, err, "test archive body should be written")
	}
	require.NoError(t, tw.Close(), "test archive tar writer should close")
	require.NoError(t, gzw.Close(), "test archive gzip writer should close")
	return buf.Bytes()
}

func newMemorySnapshotStore() *memorySnapshotStore {
	return &memorySnapshotStore{blobs: make(map[string][]byte)}
}

func (s *memorySnapshotStore) Save(_ context.Context, key string, reader io.Reader) error {
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[key] = body
	return nil
}

func (s *memorySnapshotStore) Load(_ context.Context, key string, writer io.Writer) error {
	s.mu.Lock()
	body, ok := s.blobs[key]
	s.mu.Unlock()
	if !ok {
		return storage.ErrSnapshotNotFound
	}
	_, err := writer.Write(body)
	return err
}

func (s *memorySnapshotStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blobs, key)
	return nil
}

func TestSharedDependencyCache_SaveUploadsBlobChecksumAndReturnsSize(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	cacheKey := strings.Repeat("a", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{"node_modules/.bin/next": "next"})
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, cacheKey, "placement", "deps/"+cacheKey+".tar.gz", int64(len(payload)), []byte(`{}`), now, now))

	blobStore := newMemorySnapshotStore()
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(mock),
		Executor:  &dependencyCacheExec{payload: payload},
		BlobStore: blobStore,
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	result, err := cache.Save(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, cacheKey, []string{"node_modules"}, DependencyCacheMetadata{
		OrgID:          orgID,
		RepoID:         repoID,
		SessionID:      sessionID,
		PlacementKey:   "placement",
		InstallCommand: []string{"npm", "ci"},
	})
	require.NoError(t, err, "Save should upload dependency cache")
	require.Equal(t, int64(len(payload)), result.SizeBytes, "Save should report compressed archive size")
	sum := sha256.Sum256(payload)
	expectedChecksum := fmt.Sprintf("%x", sum[:])
	expectedBlobKey := "deps/" + orgID.String() + "/" + repoID.String() + "/install_artifact/" + cacheKey + "/" + expectedChecksum + ".tar.gz"
	require.Equal(t, payload, blobStore.blobs[expectedBlobKey], "Save should upload archive bytes to a checksum-addressed object key")
	require.NotEmpty(t, bytes.TrimSpace(blobStore.blobs[expectedBlobKey+".sha256"]), "Save should upload checksum sidecar next to the checksum-addressed blob")
	require.Empty(t, blobStore.blobs["deps/"+orgID.String()+"/"+repoID.String()+"/install_artifact/"+cacheKey+".tar.gz"], "Save should not overwrite a shared mutable blob key")
	require.Contains(t, strings.Join(cache.executor.(*dependencyCacheExec).calls(), "\n"), "tar czf -", "Save should stream tar output instead of creating a sandbox temp archive")
	require.NotContains(t, strings.Join(cache.executor.(*dependencyCacheExec).calls(), "\n"), "cat /tmp/preview-dependency-cache-", "Save should not read back a sandbox temp archive")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSharedDependencyCache_SaveBatchesEffectivePathExistenceProbe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	repoID := uuid.New()
	cacheKey := strings.Repeat("b", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{
		"node_modules/.bin/next": "next",
		".next/cache/app":        "cache",
		"dist/index.html":        "html",
	})
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, cacheKey, "placement", "deps/"+cacheKey+".tar.gz", int64(len(payload)), []byte(`{}`), now, now))

	exec := &dependencyCacheExec{payload: payload}
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(mock),
		Executor:  exec,
		BlobStore: newMemorySnapshotStore(),
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	_, err = cache.Save(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, cacheKey, []string{"node_modules", ".next/cache", "dist"}, DependencyCacheMetadata{
		OrgID:          orgID,
		RepoID:         repoID,
		PlacementKey:   "placement",
		InstallCommand: []string{"npm", "ci"},
	})
	require.NoError(t, err, "Save should upload dependency cache")

	var existenceProbeCount int
	for _, call := range exec.calls() {
		if strings.Contains(call, "test -e ") || strings.Contains(call, "find ") {
			existenceProbeCount++
		}
	}
	require.Equal(t, 1, existenceProbeCount, "Save should batch effective-path existence checks into one sandbox exec")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSharedDependencyCache_StageBlobUsesLocalCacheStagingDir(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	localDir := t.TempDir()
	orgID := uuid.New()
	repoID := uuid.New()
	cacheKey := strings.Repeat("a", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{"node_modules/.bin/next": "next"})
	blobKey := "deps/blob.tar.gz"
	blobStore := newMemorySnapshotStore()
	require.NoError(t, blobStore.Save(ctx, blobKey, bytes.NewReader(payload)), "test blob should be available in object storage")

	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(nil),
		Executor:  &dependencyCacheExec{},
		BlobStore: blobStore,
		Logger:    zerolog.Nop(),
		LocalDir:  localDir,
	})
	require.NoError(t, err, "dependency cache should initialize")

	blob, err := cache.stageBlob(ctx, &DependencyCacheHit{
		Entry: models.PreviewDependencyCache{
			ID:         uuid.New(),
			OrgID:      orgID,
			RepoID:     repoID,
			CacheKey:   cacheKey,
			BlobKey:    blobKey,
			SizeBytes:  int64(len(payload)),
			LastUsedAt: time.Now().UTC(),
		},
		BlobKey: blobKey,
	})
	require.NoError(t, err, "stageBlob should download the remote dependency cache blob")
	defer blob.cleanup()

	expectedRoot := filepath.Join(localDir, ".staging") + string(os.PathSeparator)
	require.True(t, strings.HasPrefix(blob.path, expectedRoot), "stageBlob should use the local dependency cache staging directory instead of the process temp dir")
}

func TestSharedDependencyCache_SaveRejectsPreviewInstallMarkerParentPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	repoID := uuid.New()
	cacheKey := strings.Repeat("a", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{".143/cache/some-build-cache": "cache"})
	now := time.Now().UTC()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()
	mock.ExpectQuery("INSERT INTO preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, cacheKey, "", "deps/blob.tar.gz", int64(len(payload)), []byte(`{}`), now, now))

	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(mock),
		Executor:  &dependencyCacheExec{payload: payload},
		BlobStore: newMemorySnapshotStore(),
		Logger:    zerolog.Nop(),
	})
	require.NoError(t, err, "dependency cache should initialize")

	_, err = cache.Save(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, cacheKey, []string{".143/cache"}, DependencyCacheMetadata{
		OrgID:  orgID,
		RepoID: repoID,
	})
	require.Error(t, err, "Save should reject cache paths that can include preview install markers")
	require.Contains(t, err.Error(), "preview install markers", "Save error should explain marker path protection")
}

func TestSharedDependencyCache_EvictLocalLRURemovesOldestBlobAndLocation(t *testing.T) {
	t.Parallel()

	localDir := t.TempDir()
	oldKey := strings.Repeat("a", 64)
	newKey := strings.Repeat("b", 64)
	oldPath := filepath.Join(localDir, oldKey[:2], oldKey+".tar.gz")
	newPath := filepath.Join(localDir, newKey[:2], newKey+".tar.gz")
	require.NoError(t, os.MkdirAll(filepath.Dir(oldPath), 0o750), "old blob dir should be created")
	require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0o750), "new blob dir should be created")
	require.NoError(t, os.WriteFile(oldPath, []byte("old"), 0o600), "old blob should be written")
	require.NoError(t, os.WriteFile(newPath, []byte("newer"), 0o600), "new blob should be written")
	require.NoError(t, os.Chtimes(oldPath, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)), "old blob mtime should be set")

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()
	mock.ExpectExec("DELETE FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg(), models.PreviewCacheKindInstallArtifact, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:         db.NewPreviewStore(mock),
		Executor:      &dependencyCacheExec{},
		BlobStore:     newMemorySnapshotStore(),
		Logger:        zerolog.Nop(),
		WorkerNodeID:  "worker-1",
		LocalDir:      localDir,
		LocalMaxBytes: int64(len("newer")),
	})
	require.NoError(t, err, "dependency cache should initialize")

	require.NoError(t, cache.evictLocalLRU(context.Background()), "local LRU eviction should complete")
	require.NoFileExists(t, oldPath, "local LRU should remove oldest blob first")
	require.FileExists(t, newPath, "local LRU should keep newest blob within budget")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSharedDependencyCache_RestoreDeletesMetadataWhenObjectMissing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	repoID := uuid.New()
	entryID := uuid.New()
	cacheKey := strings.Repeat("c", 64)
	metadata := DependencyCacheMetadata{
		EffectivePaths: []string{"node_modules"},
	}
	metadataJSON, err := json.Marshal(metadata)
	require.NoError(t, err, "dependency cache metadata should marshal")

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()
	mock.ExpectExec("DELETE FROM preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(mock),
		Executor:  &dependencyCacheExec{},
		BlobStore: newMemorySnapshotStore(),
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	err = cache.Restore(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, &DependencyCacheHit{
		Entry: models.PreviewDependencyCache{
			ID:         entryID,
			OrgID:      orgID,
			RepoID:     repoID,
			CacheKey:   cacheKey,
			BlobKey:    "deps/missing.tar.gz",
			Metadata:   metadataJSON,
			LastUsedAt: time.Now().UTC(),
		},
		BlobKey: "deps/missing.tar.gz",
	})
	require.Error(t, err, "Restore should return the missing object error")
	require.ErrorIs(t, err, storage.ErrSnapshotNotFound, "Restore should preserve the missing object sentinel")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSharedDependencyCache_RestoreDeletesMetadataOnChecksumMismatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	repoID := uuid.New()
	entryID := uuid.New()
	cacheKey := strings.Repeat("d", 64)
	metadata := DependencyCacheMetadata{
		EffectivePaths: []string{"node_modules"},
		ChecksumSHA256: strings.Repeat("0", 64),
	}
	metadataJSON, err := json.Marshal(metadata)
	require.NoError(t, err, "dependency cache metadata should marshal")
	blobStore := newMemorySnapshotStore()
	require.NoError(t, blobStore.Save(ctx, "deps/blob.tar.gz", strings.NewReader("not-the-recorded-checksum")), "test blob should save")

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()
	mock.ExpectExec("DELETE FROM preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(mock),
		Executor:  &dependencyCacheExec{},
		BlobStore: blobStore,
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	err = cache.Restore(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, &DependencyCacheHit{
		Entry: models.PreviewDependencyCache{
			ID:         entryID,
			OrgID:      orgID,
			RepoID:     repoID,
			CacheKey:   cacheKey,
			BlobKey:    "deps/blob.tar.gz",
			Metadata:   metadataJSON,
			LastUsedAt: time.Now().UTC(),
		},
		BlobKey: "deps/blob.tar.gz",
	})
	require.Error(t, err, "Restore should reject corrupted cache blobs")
	require.Contains(t, err.Error(), "checksum mismatch", "Restore should explain checksum failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSharedDependencyCache_RestoreRejectsPreviewInstallMarkerParentPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cacheKey := strings.Repeat("e", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{".143/cache/some-build-cache": "cache"})
	sum := sha256.Sum256(payload)
	metadataJSON, err := json.Marshal(DependencyCacheMetadata{
		EffectivePaths: []string{".143/cache"},
		ChecksumSHA256: fmt.Sprintf("%x", sum[:]),
	})
	require.NoError(t, err, "dependency cache metadata should marshal")
	blobStore := newMemorySnapshotStore()
	require.NoError(t, blobStore.Save(ctx, "deps/blob.tar.gz", bytes.NewReader(payload)), "test blob should save")

	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(nil),
		Executor:  &dependencyCacheExec{},
		BlobStore: blobStore,
		Logger:    zerolog.Nop(),
	})
	require.NoError(t, err, "dependency cache should initialize")

	err = cache.Restore(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, &DependencyCacheHit{
		Entry: models.PreviewDependencyCache{
			ID:         uuid.New(),
			OrgID:      uuid.New(),
			RepoID:     uuid.New(),
			CacheKey:   cacheKey,
			BlobKey:    "deps/blob.tar.gz",
			SizeBytes:  int64(len(payload)),
			Metadata:   metadataJSON,
			LastUsedAt: time.Now().UTC(),
		},
		BlobKey: "deps/blob.tar.gz",
	})
	require.Error(t, err, "Restore should reject cache metadata that can include preview install markers")
	require.Contains(t, err.Error(), "preview install markers", "Restore error should explain marker path protection")
}

func TestSharedDependencyCache_RestoreSkipsOversizedBlobBeforeSandboxMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cacheKey := strings.Repeat("f", 64)
	metadataJSON, err := json.Marshal(DependencyCacheMetadata{
		EffectivePaths: []string{"node_modules"},
	})
	require.NoError(t, err, "dependency cache metadata should marshal")

	exec := &dependencyCacheExec{}
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(nil),
		Executor:  exec,
		BlobStore: dependencyCacheFailingLoadStore{},
		Logger:    zerolog.Nop(),
	})
	require.NoError(t, err, "dependency cache should initialize")

	err = cache.Restore(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, &DependencyCacheHit{
		Entry: models.PreviewDependencyCache{
			ID:         uuid.New(),
			OrgID:      uuid.New(),
			RepoID:     uuid.New(),
			CacheKey:   cacheKey,
			BlobKey:    "deps/oversized.tar.gz",
			SizeBytes:  dependencyCacheMaxBlobBytes + 1,
			Metadata:   metadataJSON,
			LastUsedAt: time.Now().UTC(),
		},
		BlobKey: "deps/oversized.tar.gz",
	})
	require.Error(t, err, "Restore should skip blobs that exceed the restore size cap")
	require.Contains(t, err.Error(), "too large", "Restore should explain the size preflight failure")
	require.Empty(t, exec.calls(), "Restore should not mutate the sandbox after size preflight failure")
}

func TestSharedDependencyCache_RestoreStreamsExtractWithoutSandboxTempBlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	repoID := uuid.New()
	entryID := uuid.New()
	cacheKey := strings.Repeat("a", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{"node_modules/.bin/next": "next"})
	sum := sha256.Sum256(payload)
	metadataJSON, err := json.Marshal(DependencyCacheMetadata{
		EffectivePaths: []string{"node_modules"},
		ChecksumSHA256: fmt.Sprintf("%x", sum[:]),
	})
	require.NoError(t, err, "dependency cache metadata should marshal")
	blobStore := newMemorySnapshotStore()
	require.NoError(t, blobStore.Save(ctx, "deps/blob.tar.gz", bytes.NewReader(payload)), "test blob should save")

	exec := &dependencyCacheExec{}
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(nil),
		Executor:  exec,
		BlobStore: blobStore,
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	err = cache.Restore(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, &DependencyCacheHit{
		Entry: models.PreviewDependencyCache{
			ID:         entryID,
			OrgID:      orgID,
			RepoID:     repoID,
			CacheKey:   cacheKey,
			BlobKey:    "deps/blob.tar.gz",
			SizeBytes:  int64(len(payload)),
			Metadata:   metadataJSON,
			LastUsedAt: time.Now().UTC(),
		},
		BlobKey: "deps/blob.tar.gz",
	})
	require.NoError(t, err, "Restore should stream and extract a valid cache blob")
	calls := strings.Join(exec.calls(), "\n")
	require.Contains(t, calls, "tar xzf -", "Restore should extract the archive from stdin")
	require.NotContains(t, calls, "/tmp/preview-dependency-cache-", "Restore should not stage a compressed blob in sandbox /tmp")
	require.NotContains(t, strings.Join(exec.writtenFilePaths(), "\n"), "/tmp/preview-dependency-cache-", "Restore should not write the compressed blob as a sandbox file")
}

func TestSharedDependencyCache_RestorePathCacheHomeDirRejectsSensitiveArchiveEntry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cacheKey := strings.Repeat("c", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{".ssh/config": "secret"})
	sum := sha256.Sum256(payload)
	metadataJSON, err := json.Marshal(DependencyCacheMetadata{
		EffectivePaths: []string{".npm"},
		ChecksumSHA256: fmt.Sprintf("%x", sum[:]),
	})
	require.NoError(t, err, "dependency cache metadata should marshal")
	blobStore := newMemorySnapshotStore()
	require.NoError(t, blobStore.Save(ctx, "deps/home.tar.gz", bytes.NewReader(payload)), "test blob should save")

	exec := &dependencyCacheExec{}
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(nil),
		Executor:  exec,
		BlobStore: blobStore,
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	err = cache.RestorePathCache(ctx, &agent.Sandbox{WorkDir: "/workspace/repo", HomeDir: "/home/codex"}, &DependencyCacheHit{
		Entry: models.PreviewDependencyCache{
			ID:         uuid.New(),
			OrgID:      uuid.New(),
			RepoID:     uuid.New(),
			CacheKind:  models.PreviewCacheKindPackageManager,
			CacheKey:   cacheKey,
			BlobKey:    "deps/home.tar.gz",
			SizeBytes:  int64(len(payload)),
			Metadata:   metadataJSON,
			LastUsedAt: time.Now().UTC(),
		},
		BlobKey: "deps/home.tar.gz",
	}, models.PreviewCacheRootHomeDir)

	require.Error(t, err, "HomeDir cache restore should reject archive entries outside the configured paths")
	require.Contains(t, err.Error(), "validate archive", "HomeDir cache restore should fail during archive validation")
	require.Empty(t, exec.calls(), "HomeDir cache restore should not mutate the sandbox after archive validation fails")
}

func TestSharedDependencyCache_RestorePathCacheHomeDirExtractsUnderHomeDir(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cacheKey := strings.Repeat("d", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{".npm/_cacache/index": "pkg"})
	sum := sha256.Sum256(payload)
	metadataJSON, err := json.Marshal(DependencyCacheMetadata{
		EffectivePaths: []string{".npm"},
		ChecksumSHA256: fmt.Sprintf("%x", sum[:]),
	})
	require.NoError(t, err, "dependency cache metadata should marshal")
	blobStore := newMemorySnapshotStore()
	require.NoError(t, blobStore.Save(ctx, "deps/home-valid.tar.gz", bytes.NewReader(payload)), "test blob should save")

	exec := &dependencyCacheExec{}
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(nil),
		Executor:  exec,
		BlobStore: blobStore,
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	err = cache.RestorePathCache(ctx, &agent.Sandbox{WorkDir: "/workspace/repo", HomeDir: "/home/codex"}, &DependencyCacheHit{
		Entry: models.PreviewDependencyCache{
			ID:         uuid.New(),
			OrgID:      uuid.New(),
			RepoID:     uuid.New(),
			CacheKind:  models.PreviewCacheKindPackageManager,
			CacheKey:   cacheKey,
			BlobKey:    "deps/home-valid.tar.gz",
			SizeBytes:  int64(len(payload)),
			Metadata:   metadataJSON,
			LastUsedAt: time.Now().UTC(),
		},
		BlobKey: "deps/home-valid.tar.gz",
	}, models.PreviewCacheRootHomeDir)

	require.NoError(t, err, "HomeDir cache restore should extract valid package-manager cache archives")
	calls := strings.Join(exec.calls(), "\n")
	require.Contains(t, calls, "cd '/home/codex'", "HomeDir cache restore should clean paths under sandbox HomeDir")
	require.Contains(t, calls, "tar xzf - -C '/home/codex'", "HomeDir cache restore should extract under sandbox HomeDir")
}

func TestSharedDependencyCache_RestoreLocalBlobSurvivesConcurrentEviction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	localDir := t.TempDir()
	orgID := uuid.New()
	repoID := uuid.New()
	cacheKey := strings.Repeat("b", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{"node_modules/.bin/next": "next"})
	sum := sha256.Sum256(payload)
	metadataJSON, err := json.Marshal(DependencyCacheMetadata{
		EffectivePaths: []string{"node_modules"},
		ChecksumSHA256: fmt.Sprintf("%x", sum[:]),
	})
	require.NoError(t, err, "dependency cache metadata should marshal")
	localPath := filepath.Join(localDir, cacheKey[:2], cacheKey+".tar.gz")
	require.NoError(t, os.MkdirAll(filepath.Dir(localPath), 0o750), "local dependency cache dir should be created")
	require.NoError(t, os.WriteFile(localPath, payload, 0o600), "local dependency cache blob should be written")

	baseExec := &dependencyCacheExec{}
	exec := &evictingDependencyCacheExec{
		dependencyCacheExec: baseExec,
		localPath:           localPath,
	}
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(nil),
		Executor:  exec,
		BlobStore: newMemorySnapshotStore(),
		Logger:    zerolog.Nop(),
		LocalDir:  localDir,
	})
	require.NoError(t, err, "dependency cache should initialize")

	err = cache.Restore(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, &DependencyCacheHit{
		Entry: models.PreviewDependencyCache{
			ID:         uuid.New(),
			OrgID:      orgID,
			RepoID:     repoID,
			CacheKey:   cacheKey,
			BlobKey:    "deps/blob.tar.gz",
			SizeBytes:  int64(len(payload)),
			Metadata:   metadataJSON,
			LastUsedAt: time.Now().UTC(),
		},
		BlobKey: "deps/blob.tar.gz",
	})
	require.NoError(t, err, "Restore should stream the validated local blob even if local LRU removes its path before extraction")
	require.NoFileExists(t, localPath, "test should simulate local LRU eviction during restore cleanup")
	require.Contains(t, strings.Join(baseExec.calls(), "\n"), "tar xzf -", "Restore should still extract from a stable local blob reader")
}

func TestSharedDependencyCache_SavePathCacheExcludesSubtrees(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	repoID := uuid.New()
	cacheKey := strings.Repeat("b", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{"node_modules/.bin/next": "next"})
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	mock.ExpectQuery("SELECT (.+) FROM preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}))
	mock.ExpectQuery("INSERT INTO preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, cacheKey, "placement", "deps/"+cacheKey+".tar.gz", int64(len(payload)), []byte(`{}`), now, now))

	exec := &dependencyCacheExec{payload: payload}
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(mock),
		Executor:  exec,
		BlobStore: newMemorySnapshotStore(),
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	_, err = cache.SavePathCache(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, PreviewPathCacheSaveSpec{
		Kind:         models.PreviewCacheKindInstallArtifact,
		Root:         models.PreviewCacheRootWorkDir,
		CacheKey:     cacheKey,
		Paths:        []string{"node_modules"},
		ExcludePaths: []string{"node_modules/.cache/turbo", ".turbo/cache"},
		Metadata: DependencyCacheMetadata{
			OrgID:        orgID,
			RepoID:       repoID,
			PlacementKey: "placement",
		},
	})
	require.NoError(t, err, "SavePathCache should succeed with excludes")

	calls := strings.Join(exec.calls(), "\n")
	require.Contains(t, calls, "'--exclude=node_modules/.cache/turbo'", "archive command should exclude the build cache subtree")
	require.Contains(t, calls, "'--exclude=.turbo/cache'", "archive command should exclude every requested subtree")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSharedDependencyCache_SavePathCacheSeparatesExistenceProbes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	repoID := uuid.New()
	cacheKey := strings.Repeat("d", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{
		"node_modules/.bin/next": "next",
		".turbo/cache/hash":      "artifact",
	})
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	mock.ExpectQuery("SELECT (.+) FROM preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}))
	mock.ExpectQuery("INSERT INTO preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindBuildArtifact, cacheKey, "placement", "deps/"+cacheKey+".tar.gz", int64(len(payload)), []byte(`{}`), now, now))

	exec := &dependencyCacheExec{payload: payload}
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(mock),
		Executor:  exec,
		BlobStore: newMemorySnapshotStore(),
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	_, err = cache.SavePathCache(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, PreviewPathCacheSaveSpec{
		Kind:     models.PreviewCacheKindBuildArtifact,
		Root:     models.PreviewCacheRootWorkDir,
		CacheKey: cacheKey,
		Paths:    []string{"node_modules", ".turbo/cache"},
		Metadata: DependencyCacheMetadata{
			OrgID:        orgID,
			RepoID:       repoID,
			PlacementKey: "placement",
		},
	})
	require.NoError(t, err, "SavePathCache should succeed when more than one effective path is probed")

	calls := strings.Join(exec.calls(), "\n")
	require.NotContains(t, calls, "fi if ", "existence probes should be separated by shell statement delimiters")
	require.Contains(t, calls, "fi; if ", "existence probes should be joined as valid shell statements")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSharedDependencyCache_SavePathCacheSkipsUploadWhenChecksumUnchanged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	repoID := uuid.New()
	cacheKey := strings.Repeat("c", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{"node_modules/.cache/turbo/abc": "entry"})
	sum := sha256.Sum256(payload)
	checksum := fmt.Sprintf("%x", sum[:])

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()
	// No DB expectations: an unchanged archive must not touch the store.

	blobStore := newMemorySnapshotStore()
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(mock),
		Executor:  &dependencyCacheExec{payload: payload},
		BlobStore: blobStore,
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	result, err := cache.SavePathCache(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, PreviewPathCacheSaveSpec{
		Kind:           models.PreviewCacheKindBuildArtifact,
		Root:           models.PreviewCacheRootWorkDir,
		CacheKey:       cacheKey,
		Paths:          []string{"node_modules/.cache/turbo"},
		SkipIfChecksum: checksum,
		Metadata: DependencyCacheMetadata{
			OrgID:  orgID,
			RepoID: repoID,
		},
	})
	require.NoError(t, err, "SavePathCache should succeed when content is unchanged")
	require.True(t, result.Unchanged, "matching checksum should report the save as unchanged")
	require.Empty(t, blobStore.blobs, "unchanged content must not be re-uploaded")
	require.NoError(t, mock.ExpectationsWereMet(), "no database writes should happen for unchanged content")
}

func TestSharedDependencyCache_SavePathCacheDeletesSupersededBlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	repoID := uuid.New()
	cacheKey := strings.Repeat("d", 64)
	payload := makeDependencyCacheTarGz(t, map[string]string{"node_modules/.cache/turbo/abc": "entry"})
	now := time.Now().UTC()
	oldBlobKey := "deps/" + orgID.String() + "/" + repoID.String() + "/build_artifact/" + cacheKey + "/oldchecksum.tar.gz"

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	mock.ExpectQuery("SELECT (.+) FROM preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindBuildArtifact, cacheKey, "placement", oldBlobKey, int64(10), []byte(`{}`), now, now))
	mock.ExpectQuery("INSERT INTO preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindBuildArtifact, cacheKey, "placement", "deps/new.tar.gz", int64(len(payload)), []byte(`{}`), now, now))

	blobStore := newMemorySnapshotStore()
	require.NoError(t, blobStore.Save(ctx, oldBlobKey, bytes.NewReader([]byte("old"))), "old blob should exist before the save")
	require.NoError(t, blobStore.Save(ctx, oldBlobKey+".sha256", bytes.NewReader([]byte("oldchecksum"))), "old checksum sidecar should exist before the save")

	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:     db.NewPreviewStore(mock),
		Executor:  &dependencyCacheExec{payload: payload},
		BlobStore: blobStore,
		Logger:    zerolog.Nop(),
		Prefix:    "deps",
	})
	require.NoError(t, err, "dependency cache should initialize")

	_, err = cache.SavePathCache(ctx, &agent.Sandbox{WorkDir: "/workspace/repo"}, PreviewPathCacheSaveSpec{
		Kind:     models.PreviewCacheKindBuildArtifact,
		Root:     models.PreviewCacheRootWorkDir,
		CacheKey: cacheKey,
		Paths:    []string{"node_modules/.cache/turbo"},
		Metadata: DependencyCacheMetadata{
			OrgID:        orgID,
			RepoID:       repoID,
			PlacementKey: "placement",
		},
	})
	require.NoError(t, err, "SavePathCache should succeed")
	require.Empty(t, blobStore.blobs[oldBlobKey], "superseded latest-wins blob should be deleted after the upsert")
	require.Empty(t, blobStore.blobs[oldBlobKey+".sha256"], "superseded checksum sidecar should be deleted after the upsert")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
