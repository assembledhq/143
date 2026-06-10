package workspace

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type stubMentionReader struct {
	listings     map[string][]sandbox.FileEntry
	listDirCalls int
}

func (r *stubMentionReader) ListDir(ctx context.Context, dirPath string) ([]sandbox.FileEntry, error) {
	r.listDirCalls++
	if entries, ok := r.listings[dirPath]; ok {
		return entries, nil
	}
	return []sandbox.FileEntry{}, nil
}

func (r *stubMentionReader) ReadFile(ctx context.Context, filePath string) (string, bool, error) {
	panic("ReadFile should not be called by BuildMentionIndex")
}

func (r *stubMentionReader) ReadFileContext(ctx context.Context, filePath string, line, above, below int) (sandbox.FileContextResult, error) {
	panic("ReadFileContext should not be called by BuildMentionIndex")
}

func TestBuildMentionIndex(t *testing.T) {
	t.Parallel()

	reader := &stubMentionReader{
		listings: map[string][]sandbox.FileEntry{
			".": {
				{Type: "dir", Path: ".git"},
				{Type: "dir", Path: "docs"},
				{Type: "file", Path: "README.md"},
			},
			"docs": {
				{Type: "dir", Path: "docs/generated"},
				{Type: "file", Path: "docs/design.md"},
			},
			"docs/generated": {
				{Type: "file", Path: "docs/generated/output.json"},
			},
		},
	}

	index, err := BuildMentionIndex(context.Background(), reader)
	require.NoError(t, err, "BuildMentionIndex should succeed for a small recursive tree")
	require.Equal(t, []MentionIndexEntry{
		{Kind: string(models.SessionInputReferenceKindFile), Path: "README.md"},
		{Kind: string(models.SessionInputReferenceKindDirectory), Path: "docs"},
		{Kind: string(models.SessionInputReferenceKindFile), Path: "docs/design.md"},
		{Kind: string(models.SessionInputReferenceKindDirectory), Path: "docs/generated"},
		{Kind: string(models.SessionInputReferenceKindFile), Path: "docs/generated/output.json"},
	}, index.Entries, "BuildMentionIndex should include files and directories, recurse, and skip .git")
	require.False(t, index.Truncated, "BuildMentionIndex should not mark a small tree as truncated")
}

func TestBuildMentionIndex_SkipsHeavyGeneratedDirectories(t *testing.T) {
	t.Parallel()

	reader := &stubMentionReader{
		listings: map[string][]sandbox.FileEntry{
			".": {
				{Type: "dir", Path: "node_modules"},
				{Type: "dir", Path: ".next"},
				{Type: "dir", Path: "src"},
				{Type: "file", Path: "README.md"},
			},
			"node_modules": {
				{Type: "file", Path: "node_modules/pkg/index.js"},
			},
			".next": {
				{Type: "file", Path: ".next/cache/build.bin"},
			},
			"src": {
				{Type: "file", Path: "src/app.ts"},
			},
		},
	}

	index, err := BuildMentionIndex(context.Background(), reader)
	require.NoError(t, err, "BuildMentionIndex should succeed while skipping generated directories")
	require.Equal(t, []MentionIndexEntry{
		{Kind: string(models.SessionInputReferenceKindFile), Path: "README.md"},
		{Kind: string(models.SessionInputReferenceKindDirectory), Path: "src"},
		{Kind: string(models.SessionInputReferenceKindFile), Path: "src/app.ts"},
	}, index.Entries, "BuildMentionIndex should exclude dependency and build-cache directories from the mention index")
}

type recursiveMentionReader struct {
	stubMentionReader
	entries        []sandbox.FileEntry
	recursiveCalls int
	maxEntries     int
}

func (r *recursiveMentionReader) ListDirRecursive(_ context.Context, maxEntries int, _ []string) ([]sandbox.FileEntry, error) {
	r.recursiveCalls++
	r.maxEntries = maxEntries
	return r.entries, nil
}

func TestBuildMentionIndex_UsesRecursiveReaderFastPath(t *testing.T) {
	t.Parallel()

	reader := &recursiveMentionReader{
		entries: []sandbox.FileEntry{
			{Type: "dir", Path: "docs"},
			{Type: "file", Path: "docs/guide.md"},
			{Type: "dir", Path: "node_modules"},
			{Type: "file", Path: "node_modules/pkg/index.js"},
		},
	}

	index, err := BuildMentionIndex(context.Background(), reader)
	require.NoError(t, err, "BuildMentionIndex should succeed through the recursive reader fast path")
	require.Equal(t, 1, reader.recursiveCalls, "BuildMentionIndex should use one recursive list call when the reader supports it")
	require.Equal(t, defaultMentionIndexMaxPaths, reader.maxEntries, "BuildMentionIndex should pass its traversal cap into the recursive reader")
	require.Equal(t, 0, reader.listDirCalls, "BuildMentionIndex should avoid per-directory ListDir calls when the recursive fast path is available")
	require.Equal(t, []MentionIndexEntry{
		{Kind: string(models.SessionInputReferenceKindDirectory), Path: "docs"},
		{Kind: string(models.SessionInputReferenceKindFile), Path: "docs/guide.md"},
	}, index.Entries, "BuildMentionIndex should still filter ignored paths returned by the recursive reader")
}

func TestBuildMentionIndexWithConfig_PassesCustomCapToRecursiveReader(t *testing.T) {
	t.Parallel()

	reader := &recursiveMentionReader{
		entries: []sandbox.FileEntry{
			{Type: "file", Path: "a.go"},
			{Type: "file", Path: "b.go"},
			{Type: "file", Path: "c.go"},
		},
	}

	index, err := BuildMentionIndexWithConfig(context.Background(), reader, MentionIndexBuildConfig{MaxPaths: 2})
	require.NoError(t, err, "BuildMentionIndexWithConfig should succeed through the recursive reader fast path")
	require.Equal(t, 2, reader.maxEntries, "BuildMentionIndexWithConfig should pass the configured traversal cap into the recursive reader")
	require.True(t, index.Truncated, "BuildMentionIndexWithConfig should still mark results truncated when recursive output reaches the cap")
	require.Equal(t, []MentionIndexEntry{
		{Kind: string(models.SessionInputReferenceKindFile), Path: "a.go"},
		{Kind: string(models.SessionInputReferenceKindFile), Path: "b.go"},
	}, index.Entries, "BuildMentionIndexWithConfig should cap recursive reader results")
}

func TestSessionMentionIndexCacheKey_LiveWorkspaceUsesGenerationNotActivity(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	containerID := "ctr-live"
	early := time.Date(2026, 6, 9, 9, 0, 0, 0, time.UTC)
	later := early.Add(5 * time.Minute)

	first := &models.Session{
		ID:                  sessionID,
		OrgID:               orgID,
		ContainerID:         &containerID,
		CurrentTurn:         4,
		WorkspaceGeneration: 12,
		LastActivityAt:      early,
	}
	second := *first
	second.LastActivityAt = later

	require.Equal(t, SessionMentionIndexCacheKey(first), SessionMentionIndexCacheKey(&second), "live mention index keys should survive non-workspace session activity updates")

	second.WorkspaceGeneration = 13
	require.NotEqual(t, SessionMentionIndexCacheKey(first), SessionMentionIndexCacheKey(&second), "live mention index keys should change when the workspace generation changes")
}

func TestSessionMentionIndexStaleCacheKey_StableAcrossTurnsAndSources(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	containerID := "ctr-live"
	snapshotKey := "snapshots/o/s/workspace.tar.zst"

	live := &models.Session{ID: sessionID, OrgID: orgID, ContainerID: &containerID, CurrentTurn: 4, WorkspaceGeneration: 12}
	nextTurn := *live
	nextTurn.CurrentTurn = 5
	nextTurn.WorkspaceGeneration = 13
	snapshotted := &models.Session{ID: sessionID, OrgID: orgID, SnapshotKey: &snapshotKey}

	require.NotEqual(t, SessionMentionIndexCacheKey(live), SessionMentionIndexCacheKey(&nextTurn), "exact keys churn across turns by design")
	require.Equal(t, SessionMentionIndexStaleCacheKey(live), SessionMentionIndexStaleCacheKey(&nextTurn), "stale alias must survive turn and generation churn")
	require.Equal(t, SessionMentionIndexStaleCacheKey(live), SessionMentionIndexStaleCacheKey(snapshotted), "stale alias must be identical for live and snapshot workspace sources")
}

func TestMentionIndexCache_GetOrBuildStale_ServesStaleAliasAndRefreshes(t *testing.T) {
	t.Parallel()

	c := NewMentionIndexCache(MentionIndexCacheConfig{Logger: zerolog.Nop()})

	staleKey := "session-mention-index:v1:o:s:latest"
	exactKey := "session-mention-index:v1:o:s:live:ctr:turn:5:workspace:9"
	staleIndex := MentionIndex{Entries: []MentionIndexEntry{{Kind: "file", Path: "old/path.go"}}}
	freshIndex := MentionIndex{Entries: []MentionIndexEntry{{Kind: "file", Path: "new/path.go"}}}

	require.NoError(t, c.Warm(context.Background(), staleKey, staleIndex), "warming the stale alias should succeed")

	buildRelease := make(chan struct{})
	var buildCalls atomic.Int32
	build := func(ctx context.Context) (MentionIndex, error) {
		buildCalls.Add(1)
		<-buildRelease
		return freshIndex, nil
	}

	got, stale, err := c.GetOrBuildStale(context.Background(), exactKey, staleKey, build)
	require.NoError(t, err, "stale lookup should not error")
	require.True(t, stale, "an exact-key miss with a warm alias should be reported as stale")
	require.Equal(t, staleIndex, got, "the stale alias index should be returned immediately without waiting for the build")

	close(buildRelease)
	require.Eventually(t, func() bool {
		index, ok := c.getLocal(exactKey)
		return ok && len(index.Entries) == 1 && index.Entries[0].Path == "new/path.go"
	}, 5*time.Second, 10*time.Millisecond, "the background refresh should repopulate the exact key")
	require.Eventually(t, func() bool {
		index, ok := c.getLocal(staleKey)
		return ok && len(index.Entries) == 1 && index.Entries[0].Path == "new/path.go"
	}, 5*time.Second, 10*time.Millisecond, "the background refresh should also update the stale alias")
	require.Equal(t, int32(1), buildCalls.Load(), "the refresh should build exactly once")

	got, stale, err = c.GetOrBuildStale(context.Background(), exactKey, staleKey, func(ctx context.Context) (MentionIndex, error) {
		t.Error("builder should not run once the exact key is fresh")
		return MentionIndex{}, nil
	})
	require.NoError(t, err, "fresh lookup should not error")
	require.False(t, stale, "a fresh exact-key hit should not be reported as stale")
	require.Equal(t, freshIndex, got, "subsequent lookups should serve the refreshed index")
}

func TestMentionIndexCache_GetOrBuildStale_BuildsSynchronouslyWhenCold(t *testing.T) {
	t.Parallel()

	c := NewMentionIndexCache(MentionIndexCacheConfig{Logger: zerolog.Nop()})

	staleKey := "session-mention-index:v1:o:s2:latest"
	firstKey := "session-mention-index:v1:o:s2:live:ctr:turn:1:workspace:1"
	secondKey := "session-mention-index:v1:o:s2:live:ctr:turn:2:workspace:2"
	built := MentionIndex{Entries: []MentionIndexEntry{{Kind: "file", Path: "a.go"}}}

	got, stale, err := c.GetOrBuildStale(context.Background(), firstKey, staleKey, func(ctx context.Context) (MentionIndex, error) {
		return built, nil
	})
	require.NoError(t, err, "cold build should succeed")
	require.False(t, stale, "a synchronous cold build is not stale")
	require.Equal(t, built, got, "cold build should return the built index")

	// A new exact key (turn churn) must be served from the alias the cold
	// build populated, without blocking on the rebuild.
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	got, stale, err = c.GetOrBuildStale(context.Background(), secondKey, staleKey, func(ctx context.Context) (MentionIndex, error) {
		<-blocked
		return built, nil
	})
	require.NoError(t, err, "stale lookup after turn churn should not error")
	require.True(t, stale, "turn churn should be absorbed by the stale alias")
	require.Equal(t, built, got, "the alias should serve the previous turn's index")
}

func TestMentionIndexCache_GetOrBuild_UsesRedisAndLocalHotCache(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	metrics, err := cache.NewMetrics()
	require.NoError(t, err, "Redis metrics should initialize")
	redisClient := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), metrics)
	require.NotNil(t, redisClient, "Redis client should initialize against miniredis")
	t.Cleanup(func() {
		require.NoError(t, redisClient.Close(), "Redis client should close cleanly")
	})

	c := NewMentionIndexCache(MentionIndexCacheConfig{
		Redis:         redisClient,
		Logger:        zerolog.Nop(),
		RedisTTL:      24 * time.Hour,
		LocalMaxItems: 16,
	})

	key := "session-mention-index:v1:o:s:snapshot:test"
	expected := MentionIndex{
		Entries: []MentionIndexEntry{
			{Kind: string(models.SessionInputReferenceKindFile), Path: "docs/guide.md"},
			{Kind: string(models.SessionInputReferenceKindDirectory), Path: "internal/services"},
		},
	}

	buildCalls := 0
	first, err := c.GetOrBuild(context.Background(), key, func(ctx context.Context) (MentionIndex, error) {
		buildCalls++
		return expected, nil
	})
	require.NoError(t, err, "GetOrBuild should build the missing index")
	require.Equal(t, expected, first, "GetOrBuild should return the built index")
	require.Equal(t, 1, buildCalls, "GetOrBuild should invoke the builder on the first miss")

	second, err := c.GetOrBuild(context.Background(), key, func(ctx context.Context) (MentionIndex, error) {
		buildCalls++
		return MentionIndex{}, nil
	})
	require.NoError(t, err, "GetOrBuild should serve from cache on the second lookup")
	require.Equal(t, expected, second, "GetOrBuild should return the cached index")
	require.Equal(t, 1, buildCalls, "GetOrBuild should avoid rebuilding when the local hot cache is warm")
}
