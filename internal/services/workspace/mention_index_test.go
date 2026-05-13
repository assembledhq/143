package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type stubMentionReader struct {
	listings map[string][]sandbox.FileEntry
}

func (r *stubMentionReader) ListDir(ctx context.Context, dirPath string) ([]sandbox.FileEntry, error) {
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
