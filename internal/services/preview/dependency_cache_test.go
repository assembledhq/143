package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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
	payload    []byte
	execCalls  []string
	writeFiles map[string][]byte
	mu         sync.Mutex
}

func (e *dependencyCacheExec) Exec(_ context.Context, _ *agent.Sandbox, cmd string, stdout, _ io.Writer) (int, error) {
	e.mu.Lock()
	e.execCalls = append(e.execCalls, cmd)
	e.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, "test -e "):
		return 0, nil
	case strings.Contains(cmd, "tar czf "):
		return 0, nil
	case strings.HasPrefix(cmd, "cat "):
		_, err := stdout.Write(e.payload)
		return 0, err
	case strings.HasPrefix(cmd, "rm -f "):
		return 0, nil
	default:
		return 1, nil
	}
}

func (e *dependencyCacheExec) ReadFile(context.Context, *agent.Sandbox, string) ([]byte, error) {
	return nil, nil
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

type memorySnapshotStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
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
	payload := []byte("compressed dependency archive")
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO preview_dependency_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_key", "placement_key", "blob_key", "size_bytes", "metadata", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, cacheKey, "placement", "deps/"+cacheKey+".tar.gz", int64(len(payload)), []byte(`{}`), now, now))

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
	require.Equal(t, payload, blobStore.blobs["deps/"+orgID.String()+"/"+repoID.String()+"/"+cacheKey+".tar.gz"], "Save should upload archive bytes")
	require.NotEmpty(t, bytes.TrimSpace(blobStore.blobs["deps/"+orgID.String()+"/"+repoID.String()+"/"+cacheKey+".tar.gz.sha256"]), "Save should upload checksum sidecar")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
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
