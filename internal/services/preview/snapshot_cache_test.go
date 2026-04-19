package preview

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestCappedCountingWriter_AllowsWritesUnderLimit verifies the streaming size
// guard is permissive while within budget.
func TestCappedCountingWriter_AllowsWritesUnderLimit(t *testing.T) {
	t.Parallel()

	w := &cappedCountingWriter{limit: 1024}
	n, err := w.Write(make([]byte, 512))
	require.NoError(t, err)
	require.Equal(t, 512, n)
	require.False(t, w.exceeded, "exceeded flag should stay false while under limit")
}

// TestCappedCountingWriter_FailsWhenLimitExceeded verifies the streaming size
// guard trips mid-stream rather than after buffering the whole payload.
func TestCappedCountingWriter_FailsWhenLimitExceeded(t *testing.T) {
	t.Parallel()

	w := &cappedCountingWriter{limit: 1024}
	_, err := w.Write(make([]byte, 600))
	require.NoError(t, err)
	require.False(t, w.exceeded)

	// This chunk tips the counter past the limit.
	_, err = w.Write(make([]byte, 600))
	require.Error(t, err, "Write crossing the limit must return an error to short-circuit the stream")
	require.True(t, w.exceeded)
	require.Contains(t, err.Error(), "exceeds max size")
}

// TestCappedCountingWriter_ZeroLimitDisablesCap verifies that a non-positive
// limit is treated as unlimited (for callers that want streaming size
// accounting without enforcement).
func TestCappedCountingWriter_ZeroLimitDisablesCap(t *testing.T) {
	t.Parallel()

	w := &cappedCountingWriter{limit: 0}
	_, err := w.Write(make([]byte, 10*1024*1024))
	require.NoError(t, err, "limit=0 should disable the cap so writes always succeed")
	require.False(t, w.exceeded)
}

// TestBlobPath_RejectsPathTraversal verifies that the blob-path helper
// rejects snapshot keys that could be used to escape the cache directory.
// Regression guard for arbitrary-file-write via a crafted snapshot key.
func TestBlobPath_RejectsPathTraversal(t *testing.T) {
	t.Parallel()

	sc := &SnapshotCache{cacheDir: "/var/cache/143-preview"}

	cases := []string{
		"../etc/passwd",
		"..",
		"a/b",
		"abc\\def",
	}
	for _, key := range cases {
		_, err := sc.blobPath(key)
		require.Errorf(t, err, "blobPath(%q) must reject unsafe keys", key)
		require.Contains(t, err.Error(), "path traversal")
	}
}

// TestBlobPath_ValidKey verifies the happy path: a hex-digest key produces a
// two-char-prefix sharded path under the cache directory.
func TestBlobPath_ValidKey(t *testing.T) {
	t.Parallel()

	sc := &SnapshotCache{cacheDir: "/var/cache/143-preview"}
	p, err := sc.blobPath("abcdef1234")
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/var/cache/143-preview", "ab", "abcdef1234.tar.gz"), p)
}

// TestAtomicWriteFile_ConcurrentWritesConverge verifies that atomicWriteFile
// is safe when multiple goroutines race to write the same final path — each
// goroutine uses its own os.CreateTemp staging file, so the final rename is
// last-writer-wins but never a partial file. Regression guard for the
// streaming snapshot create path where a second concurrent CreateSnapshot
// for the same key could race on the final rename.
func TestAtomicWriteFile_ConcurrentWritesConverge(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	final := filepath.Join(dir, "blob.sha256")

	const goroutines = 8
	payloads := make([][]byte, goroutines)
	for i := range payloads {
		payloads[i] = []byte(strings.Repeat(string(rune('a'+i)), 32))
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			if err := atomicWriteFile(final, payloads[idx], 0o640); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	// The final file must exist, have 0o640 perms, and be exactly 32 bytes
	// (one of the payloads) — never a truncated or concatenated blob.
	got, err := os.ReadFile(final)
	require.NoError(t, err)
	require.Len(t, got, 32, "atomicWriteFile must yield a complete payload, never a partial rename")

	// The content must match exactly one of the payloads (last-writer-wins).
	matched := false
	for _, p := range payloads {
		if string(got) == string(p) {
			matched = true
			break
		}
	}
	require.True(t, matched, "final file content must equal exactly one of the payloads")

	// No leftover temp files should remain in the cache dir (deferred cleanup
	// removes them on error; successful renames clear tmpPath beforehand).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.Falsef(t, strings.HasPrefix(e.Name(), ".snapshot-") && strings.HasSuffix(e.Name(), ".tmp"),
			"atomicWriteFile left a temp file behind: %s", e.Name())
	}
}

// TestComputeSnapshotKey_Deterministic verifies that the same inputs always
// produce the same key — critical for cache hits to actually hit.
func TestComputeSnapshotKey_Deterministic(t *testing.T) {
	t.Parallel()

	lock := []byte("package-lock.json contents")
	commit := "deadbeef"
	digest := "sha256:abc"

	k1 := ComputeSnapshotKey(lock, commit, digest)
	k2 := ComputeSnapshotKey(lock, commit, digest)
	require.Equal(t, k1, k2)
	require.Len(t, k1, 64, "snapshot key should be a sha256 hex digest")
}

// TestComputeSnapshotKey_ChangesOnAnyInput verifies that any single input
// changing produces a different key — prevents stale cache hits after a
// config or commit change.
func TestComputeSnapshotKey_ChangesOnAnyInput(t *testing.T) {
	t.Parallel()

	base := ComputeSnapshotKey([]byte("lock"), "commit-a", "digest-a")
	require.NotEqual(t, base, ComputeSnapshotKey([]byte("lock-changed"), "commit-a", "digest-a"))
	require.NotEqual(t, base, ComputeSnapshotKey([]byte("lock"), "commit-b", "digest-a"))
	require.NotEqual(t, base, ComputeSnapshotKey([]byte("lock"), "commit-a", "digest-b"))
}

// TestNewSnapshotCache_RequiresExecutor verifies that configuration errors are
// surfaced before the cache is constructed.
func TestNewSnapshotCache_RequiresExecutor(t *testing.T) {
	t.Parallel()

	_, err := NewSnapshotCache(SnapshotCacheConfig{
		CacheDir:     t.TempDir(),
		WorkerNodeID: "worker-1",
		Logger:       zerolog.Nop(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "executor must be non-nil")
}
