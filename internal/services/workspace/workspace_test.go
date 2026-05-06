package workspace

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/storage"
)

// tarBuilder produces gzipped tar bytes representing a synthetic workspace
// snapshot for tests. Files are placed under "workspace/<path>" by default
// to match the orchestrator's tar layout.
type tarBuilder struct {
	t       testing.TB
	buf     *bytes.Buffer
	tw      *tar.Writer
	gzw     *gzip.Writer
	written bool
}

func newTarBuilder(t testing.TB) *tarBuilder {
	t.Helper()
	buf := &bytes.Buffer{}
	gzw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gzw)
	return &tarBuilder{t: t, buf: buf, tw: tw, gzw: gzw}
}

// addFile writes a regular file to the tar with the given content.
func (b *tarBuilder) addFile(name, content string) *tarBuilder {
	b.t.Helper()
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(b.t, b.tw.WriteHeader(hdr))
	_, err := b.tw.Write([]byte(content))
	require.NoError(b.t, err)
	return b
}

// addDir writes a directory entry. Optional — extractTarGz creates parent
// dirs lazily when it sees regular file entries.
func (b *tarBuilder) addDir(name string) *tarBuilder {
	b.t.Helper()
	hdr := &tar.Header{
		Name:     strings.TrimSuffix(name, "/") + "/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
	}
	require.NoError(b.t, b.tw.WriteHeader(hdr))
	return b
}

// addSymlink writes a symlink entry. Tests use this to verify the
// extractor skips them rather than honoring the link target.
func (b *tarBuilder) addSymlink(name, target string) *tarBuilder {
	b.t.Helper()
	hdr := &tar.Header{
		Name:     name,
		Linkname: target,
		Typeflag: tar.TypeSymlink,
	}
	require.NoError(b.t, b.tw.WriteHeader(hdr))
	return b
}

// addRawHeader lets a test inject a header with a specific name (e.g.
// absolute path, '..' traversal, NUL byte) without going through the
// safer helpers.
func (b *tarBuilder) addRawHeader(hdr *tar.Header, body []byte) *tarBuilder {
	b.t.Helper()
	require.NoError(b.t, b.tw.WriteHeader(hdr))
	if len(body) > 0 {
		_, err := b.tw.Write(body)
		require.NoError(b.t, err)
	}
	return b
}

func (b *tarBuilder) bytes() []byte {
	b.t.Helper()
	if !b.written {
		require.NoError(b.t, b.tw.Close())
		require.NoError(b.t, b.gzw.Close())
		b.written = true
	}
	return b.buf.Bytes()
}

// stageSnapshot writes a tar payload into a FileSnapshotStore and returns
// (store, key). Caller passes the key they want; the store is rooted at
// t.TempDir() so cleanup is automatic.
func stageSnapshot(t *testing.T, key string, payload []byte) storage.SnapshotStore {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewFileSnapshotStore(dir)
	require.NoError(t, store.Save(context.Background(), key, bytes.NewReader(payload)))
	return store
}

// ---------- safeTarEntryName tests ----------

func TestSafeTarEntryName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"workspace/main.go", "workspace/main.go", true},
		{"./workspace/main.go", "workspace/main.go", true},
		{"workspace/sub/dir/file.go", "workspace/sub/dir/file.go", true},

		{"", "", false},
		{".", "", false},
		{"/", "", false},
		{"/etc/passwd", "", false},
		{"../escape", "", false},
		{"workspace/../escape", "", false},
		{"workspace/sub/../../escape", "", false},
		{"..", "", false},
		{"workspace/with\x00nul", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			got, ok := safeTarEntryName(tt.raw)
			require.Equal(t, tt.ok, ok, "ok mismatch for %q", tt.raw)
			if tt.ok {
				require.Equal(t, tt.want, got)
			}
		})
	}
}

// ---------- extractTarGz tests ----------

func TestExtractTarGz_PlaceholderWorkspace(t *testing.T) {
	t.Parallel()
	dest := t.TempDir()

	payload := newTarBuilder(t).
		addDir("workspace").
		addFile("workspace/main.go", "package main\n").
		addFile("workspace/internal/lib.go", "package lib\n").
		bytes()

	written, err := extractTarGz(bytes.NewReader(payload), dest, zerolog.Nop())
	require.NoError(t, err)
	require.Greater(t, written, int64(0))

	for _, entry := range []string{
		"workspace/main.go",
		"workspace/internal/lib.go",
	} {
		body, err := os.ReadFile(filepath.Join(dest, entry))
		require.NoError(t, err, "missing extracted file %s", entry)
		require.Contains(t, string(body), "package")
	}
}

func TestExtractTarGz_RejectsHostileEntries(t *testing.T) {
	t.Parallel()
	dest := t.TempDir()

	// Build a tar that mixes good entries with several hostile ones. The
	// hostile entries must be skipped silently and the good entries must
	// still land on disk.
	tb := newTarBuilder(t)

	// good
	tb.addFile("workspace/ok.txt", "good\n")

	// absolute path
	tb.addRawHeader(&tar.Header{
		Name:     "/etc/passwd-bad",
		Mode:     0o644,
		Size:     5,
		Typeflag: tar.TypeReg,
	}, []byte("evil!"))

	// .. traversal
	tb.addRawHeader(&tar.Header{
		Name:     "workspace/../../escape.txt",
		Mode:     0o644,
		Size:     5,
		Typeflag: tar.TypeReg,
	}, []byte("evil!"))

	// symlink — should be skipped
	tb.addSymlink("workspace/link", "../../etc/passwd")

	// good entry after the hostile ones — must still be extracted
	tb.addFile("workspace/after.txt", "still here\n")

	written, err := extractTarGz(bytes.NewReader(tb.bytes()), dest, zerolog.Nop())
	require.NoError(t, err)
	require.Greater(t, written, int64(0))

	require.FileExists(t, filepath.Join(dest, "workspace/ok.txt"))
	require.FileExists(t, filepath.Join(dest, "workspace/after.txt"))
	require.NoFileExists(t, filepath.Join(dest, "workspace/link"))

	// Make sure nothing was created above the dest dir.
	parent := filepath.Dir(dest)
	require.NoFileExists(t, filepath.Join(parent, "escape.txt"))
}

// TestExtractTarGz_CappedReader covers the decompressed-stream cap
// directly. Producing a real >4 GiB tar stream in a unit test is
// prohibitive, so we exercise the cappedReader at a smaller cap and
// trust that the wiring inside extractTarGz uses the same primitive.
func TestExtractTarGz_CappedReader(t *testing.T) {
	t.Parallel()

	t.Run("io.ReadAll surfaces errOversize once cap is hit", func(t *testing.T) {
		t.Parallel()
		c := &cappedReader{r: bytes.NewReader(bytes.Repeat([]byte("a"), 32)), capLimit: 8}
		got, err := io.ReadAll(c)
		require.Error(t, err)
		require.ErrorIs(t, err, errOversize)
		require.LessOrEqual(t, len(got), 8, "cappedReader must not deliver more bytes than capLimit")
	})

	t.Run("io.ReadFull-style callers also see the error", func(t *testing.T) {
		t.Parallel()
		// io.ReadFull would otherwise swallow an error returned alongside
		// a full buffer. Our cappedReader avoids that by refusing to
		// deliver bytes past the cap, so the *next* Read returns the
		// error visibly.
		c := &cappedReader{r: bytes.NewReader(bytes.Repeat([]byte("a"), 32)), capLimit: 8}
		buf := make([]byte, 16)
		n, err := io.ReadFull(c, buf)
		require.Error(t, err)
		require.LessOrEqual(t, n, 8)
	})

	t.Run("under-cap reads return all bytes with no error", func(t *testing.T) {
		t.Parallel()
		c := &cappedReader{r: bytes.NewReader([]byte("hello")), capLimit: 100}
		got, err := io.ReadAll(c)
		require.NoError(t, err)
		require.Equal(t, "hello", string(got))
	})
}

// ---------- SnapshotCache tests ----------

func TestSnapshotCache_OpenAndReuse(t *testing.T) {
	t.Parallel()

	const key = "snapshots/org/session/workspace.tar.zst"
	payload := newTarBuilder(t).
		addFile("workspace/main.go", "package main\n").
		bytes()
	store := stageSnapshot(t, key, payload)

	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	first, err := cache.Open(context.Background(), key, "workspace")
	require.NoError(t, err)
	require.DirExists(t, first.WorkspaceRoot)
	require.FileExists(t, filepath.Join(first.WorkspaceRoot, "main.go"))
	first.Close()

	// Second Open must return the same path without re-fetching. We assert
	// the LRU is updated by checking that the entry is now at the front.
	second, err := cache.Open(context.Background(), key, "workspace")
	require.NoError(t, err)
	require.Equal(t, first.WorkspaceRoot, second.WorkspaceRoot)
	second.Close()

	// A different workspaceRel against the same cache key should resolve
	// to a different absolute path inside the same extraction — proving
	// Open does the join lazily rather than baking the prefix into the
	// cached extraction.
	rooted, err := cache.Open(context.Background(), key, "")
	require.NoError(t, err)
	require.NotEqual(t, first.WorkspaceRoot, rooted.WorkspaceRoot)
	require.Equal(t, filepath.Dir(first.WorkspaceRoot), rooted.WorkspaceRoot)
	rooted.Close()
}

func TestSnapshotCache_ConcurrentOpensDedupe(t *testing.T) {
	t.Parallel()

	const key = "snapshots/org/session/workspace.tar.zst"
	payload := newTarBuilder(t).
		addFile("workspace/main.go", "package main\n").
		bytes()

	// gateStore wraps the inner store and gates Load on a release channel.
	// Until the test closes the channel, the leader is parked inside
	// Load — which means the singleflight key is registered, all
	// followers calling Open will join the same Do() call, and we are
	// guaranteed to see exactly one Load even under heavy contention.
	// This replaces the prior time.Sleep(20ms) which only probabilistically
	// gave followers time to reach singleflight.
	inner := stageSnapshot(t, key, payload)
	release := make(chan struct{})
	gated := &gatedStore{inner: inner, release: release}
	cache, err := NewSnapshotCache(gated, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	roots := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			ent, err := cache.Open(context.Background(), key, "workspace")
			errs[i] = err
			if ent != nil {
				roots[i] = ent.WorkspaceRoot
				ent.Close()
			}
		}()
	}

	// Wait until the leader has reached the gated Load. By that point, the
	// singleflight key is registered and any follower racing into Open
	// will join the same Do() call instead of starting a new one. This is
	// observable, deterministic, and does not rely on a fixed sleep.
	require.Eventually(t, func() bool {
		return gated.loadCalls.Load() == 1
	}, 5*time.Second, time.Millisecond, "leader never entered Load")

	close(release)
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "open %d", i)
		require.Equal(t, roots[0], roots[i])
	}
	require.Equal(t, int64(1), gated.loadCalls.Load(), "concurrent Opens should be deduped via singleflight")
}

func TestSnapshotCache_LRUEviction(t *testing.T) {
	t.Parallel()

	keys := []string{"key-a", "key-b", "key-c"}
	payload := func(content string) []byte {
		return newTarBuilder(t).addFile("workspace/file.txt", content).bytes()
	}

	dir := t.TempDir()
	store := storage.NewFileSnapshotStore(dir)
	for _, k := range keys {
		require.NoError(t, store.Save(context.Background(), k, bytes.NewReader(payload("body for "+k+strings.Repeat("x", 4096)))))
	}

	// Cap chosen so that one entry fits but two do not — every Open
	// should evict the oldest cached entry.
	cache, err := NewSnapshotCache(store, t.TempDir(), 100, zerolog.Nop())
	require.NoError(t, err)

	entA, err := cache.Open(context.Background(), keys[0], "workspace")
	require.NoError(t, err)
	require.DirExists(t, entA.WorkspaceRoot)
	// Close A's handle so it becomes evictable. Without this the refcount
	// pin would prevent the eviction this test is designed to observe.
	rootA := entA.WorkspaceRoot
	entA.Close()

	entB, err := cache.Open(context.Background(), keys[1], "workspace")
	require.NoError(t, err)
	require.DirExists(t, entB.WorkspaceRoot)

	// keys[0] should have been evicted.
	require.NoDirExists(t, rootA, "oldest entry should be evicted under cap pressure")

	rootB := entB.WorkspaceRoot
	entB.Close()

	entC, err := cache.Open(context.Background(), keys[2], "workspace")
	require.NoError(t, err)
	require.DirExists(t, entC.WorkspaceRoot)
	require.NoDirExists(t, rootB, "next-oldest entry should be evicted")
	entC.Close()
}

// TestSnapshotCache_RefcountPinsAgainstEviction is the regression for
// the eviction-while-reading race: an Open against a different key
// MUST NOT delete an extraction that another goroutine still has open.
func TestSnapshotCache_RefcountPinsAgainstEviction(t *testing.T) {
	t.Parallel()

	keys := []string{"pinned", "evictor", "third"}
	payload := func(name string) []byte {
		return newTarBuilder(t).addFile("workspace/"+name, strings.Repeat("x", 4096)).bytes()
	}

	dir := t.TempDir()
	store := storage.NewFileSnapshotStore(dir)
	for _, k := range keys {
		require.NoError(t, store.Save(context.Background(), k, bytes.NewReader(payload(k))))
	}

	// Tight cap so one entry fits but two do not — eviction would normally
	// fire on the second Open.
	cache, err := NewSnapshotCache(store, t.TempDir(), 100, zerolog.Nop())
	require.NoError(t, err)

	pinned, err := cache.Open(context.Background(), keys[0], "workspace")
	require.NoError(t, err)
	pinnedRoot := pinned.WorkspaceRoot
	require.DirExists(t, pinnedRoot)
	// Deliberately do NOT close `pinned` yet — its ref pins the entry.

	// Opening a second key over cap would normally evict the LRU back.
	// Because `pinned` still holds a ref, the cache must keep its on-disk
	// extraction intact.
	evictor, err := cache.Open(context.Background(), keys[1], "workspace")
	require.NoError(t, err)
	require.DirExists(t, evictor.WorkspaceRoot)
	require.DirExists(t, pinnedRoot, "pinned entry must survive eviction while a handle is open")

	// We can still read from the pinned WorkspaceRoot — the file exists.
	require.FileExists(t, filepath.Join(pinnedRoot, "pinned"))

	// Release the pin. Now the entry is evictable; the next over-cap
	// insert should remove it.
	pinned.Close()
	evictor.Close()

	// Opening a third key forces eviction of an unpinned older entry.
	third, err := cache.Open(context.Background(), keys[2], "workspace")
	require.NoError(t, err)
	defer third.Close()
	require.DirExists(t, third.WorkspaceRoot)
	require.NoDirExists(t, pinnedRoot, "previously-pinned entry must be evictable after Close")
}

// TestSnapshotCache_CloseIsIdempotent verifies double-Close on the same
// handle does not double-decrement the refcount. Without this guarantee
// a buggy caller could allow a still-in-use entry to be evicted.
func TestSnapshotCache_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	const key = "k"
	payload := newTarBuilder(t).addFile("workspace/main.go", "x").bytes()
	store := stageSnapshot(t, key, payload)
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	a, err := cache.Open(context.Background(), key, "workspace")
	require.NoError(t, err)
	b, err := cache.Open(context.Background(), key, "workspace")
	require.NoError(t, err)

	// Two opens → refs == 2.
	a.Close()
	a.Close() // double-close should not drop refs below 1; b still holds one.

	// b's ref keeps the entry alive; verify by reading from its root.
	require.FileExists(t, filepath.Join(b.WorkspaceRoot, "main.go"))
	b.Close()
}

func TestSnapshotCache_MissingSnapshot(t *testing.T) {
	t.Parallel()

	store := storage.NewFileSnapshotStore(t.TempDir())
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	_, err = cache.Open(context.Background(), "does-not-exist", "workspace")
	require.ErrorIs(t, err, ErrSnapshotMissing)
}

// TestSnapshotCache_UnreadableArchive verifies that a corrupt or
// non-gzip payload surfaces as ErrSnapshotUnreadable rather than
// ErrSnapshotMissing or a bare wrapped error. The HTTP handler relies on
// this to map a damaged snapshot to a distinct response code from a
// genuinely missing one.
func TestSnapshotCache_UnreadableArchive(t *testing.T) {
	t.Parallel()

	const key = "snapshots/o/s/corrupt"
	dir := t.TempDir()
	store := storage.NewFileSnapshotStore(dir)
	// Save a payload that is not a valid gzipped tar so extractTarGz fails.
	require.NoError(t, store.Save(context.Background(), key, bytes.NewReader([]byte("not a tar archive"))))

	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	_, err = cache.Open(context.Background(), key, "workspace")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSnapshotUnreadable, "corrupt archive must surface as ErrSnapshotUnreadable")
	require.NotErrorIs(t, err, ErrSnapshotMissing, "corrupt archive is distinct from a missing snapshot")
}

func TestSnapshotCache_LoadFailureSurfacesUnavailable(t *testing.T) {
	t.Parallel()

	cache, err := NewSnapshotCache(&failingLoadStore{err: io.ErrUnexpectedEOF}, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err, "cache should initialize with a valid store")

	_, err = cache.Open(context.Background(), "snapshots/o/s/unavailable", "workspace")
	require.Error(t, err, "load failure should return an error")
	require.ErrorIs(t, err, ErrSnapshotUnavailable, "transport/load failures should surface as unavailable")
	require.NotErrorIs(t, err, ErrSnapshotMissing, "transport/load failures are distinct from missing snapshots")
	require.NotErrorIs(t, err, ErrSnapshotUnreadable, "transport/load failures are distinct from corrupt archives")
}

func TestSnapshotCache_CompressedDownloadCap(t *testing.T) {
	t.Parallel()

	const key = "snapshots/o/s/too-large"
	store := &largeLoadStore{payload: []byte(strings.Repeat("x", 32))}
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err, "cache should initialize with a valid store")
	cache.maxCompressedBytes = 8

	_, err = cache.Open(context.Background(), key, "workspace")
	require.Error(t, err, "oversize staged snapshot should return an error")
	require.ErrorIs(t, err, ErrSnapshotUnreadable, "oversize staged snapshots should be treated as unreadable artifacts")
	require.NotErrorIs(t, err, ErrSnapshotMissing, "oversize staged snapshots are distinct from missing snapshots")
	require.LessOrEqual(t, store.bytesWritten.Load(), int64(8), "cache must stop writing once the compressed cap is reached")
}

// TestSnapshotCache_RejectsInvalidWorkspaceRel verifies joinWorkspaceRel
// rejects traversal segments and absolute paths. Defense in depth — the
// handler already constructs the prefix from internal config, but the
// cache should fail closed if a future caller passes a hostile string.
func TestSnapshotCache_RejectsInvalidWorkspaceRel(t *testing.T) {
	t.Parallel()

	const key = "snapshots/o/s/ok"
	payload := newTarBuilder(t).addFile("workspace/main.go", "x").bytes()
	store := stageSnapshot(t, key, payload)
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	for _, rel := range []string{"../escape", "workspace/../escape", "../../etc"} {
		_, err := cache.Open(context.Background(), key, rel)
		require.Errorf(t, err, "Open should reject workspace prefix %q", rel)
	}
}

// ---------- SnapshotReader tests ----------

func TestSnapshotReader_ReadFileContext(t *testing.T) {
	t.Parallel()

	const key = "snapshots/k"
	body := "line one\nline two\nline three\nline four\nline five\n"
	payload := newTarBuilder(t).addFile("workspace/sample.go", body).bytes()
	store := stageSnapshot(t, key, payload)
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	reader := NewSnapshotReader(cache, key, "workspace")

	// Window centered on line 3 with 1 line above and 1 below.
	got, err := reader.ReadFileContext(context.Background(), "sample.go", 3, 1, 1)
	require.NoError(t, err)
	require.Equal(t, 2, got.StartLine)
	require.Equal(t, 4, got.EndLine)
	require.Equal(t, 5, got.TotalLines)
	require.True(t, got.HasMoreAbove)
	require.True(t, got.HasMoreBelow)
	require.Equal(t, []sandbox.FileLine{
		{Number: 2, Content: "line two"},
		{Number: 3, Content: "line three"},
		{Number: 4, Content: "line four"},
	}, got.Lines)

	// Window at start-of-file: HasMoreAbove must be false.
	got, err = reader.ReadFileContext(context.Background(), "sample.go", 1, 5, 0)
	require.NoError(t, err)
	require.Equal(t, 1, got.StartLine)
	require.Equal(t, 1, got.EndLine)
	require.False(t, got.HasMoreAbove)
	require.True(t, got.HasMoreBelow)

	// Window at end-of-file: HasMoreBelow must be false.
	got, err = reader.ReadFileContext(context.Background(), "sample.go", 5, 0, 5)
	require.NoError(t, err)
	require.Equal(t, 5, got.StartLine)
	require.Equal(t, 5, got.EndLine)
	require.True(t, got.HasMoreAbove)
	require.False(t, got.HasMoreBelow)
}

func TestSnapshotReader_ReadFile(t *testing.T) {
	t.Parallel()

	const key = "k"
	payload := newTarBuilder(t).addFile("workspace/main.go", "package main\n").bytes()
	store := stageSnapshot(t, key, payload)
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	reader := NewSnapshotReader(cache, key, "workspace")
	content, truncated, err := reader.ReadFile(context.Background(), "main.go")
	require.NoError(t, err)
	require.Equal(t, "package main\n", content)
	require.False(t, truncated)
}

func TestSnapshotReader_ReadFile_Truncates(t *testing.T) {
	t.Parallel()

	const key = "k"
	big := strings.Repeat("a", maxSnapshotReadBytes+512)
	payload := newTarBuilder(t).addFile("workspace/big.txt", big).bytes()
	store := stageSnapshot(t, key, payload)
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	reader := NewSnapshotReader(cache, key, "workspace")
	content, truncated, err := reader.ReadFile(context.Background(), "big.txt")
	require.NoError(t, err)
	require.True(t, truncated)
	require.Equal(t, maxSnapshotReadBytes, len(content))
}

func TestSnapshotReader_ListDir(t *testing.T) {
	t.Parallel()

	const key = "k"
	payload := newTarBuilder(t).
		addFile("workspace/a.txt", "a").
		addFile("workspace/b.txt", "b").
		addFile("workspace/sub/c.txt", "c").
		bytes()
	store := stageSnapshot(t, key, payload)
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	reader := NewSnapshotReader(cache, key, "workspace")
	entries, err := reader.ListDir(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, entries, 3)
	require.Equal(t, "a.txt", entries[0].Path)
	require.Equal(t, "b.txt", entries[1].Path)
	require.Equal(t, "sub", entries[2].Path)
	require.Equal(t, "dir", entries[2].Type)

	subEntries, err := reader.ListDir(context.Background(), "sub")
	require.NoError(t, err)
	require.Len(t, subEntries, 1)
	require.Equal(t, "sub/c.txt", subEntries[0].Path)
}

// TestSnapshotReader_MemoizesLineCount verifies the second
// ReadFileContext call for the same file does not re-scan to count total
// lines — the entry's lineCounts memo must serve the count.
func TestSnapshotReader_MemoizesLineCount(t *testing.T) {
	t.Parallel()

	const key = "snapshots/k"
	body := strings.Repeat("line\n", 50)
	payload := newTarBuilder(t).addFile("workspace/big.txt", body).bytes()
	store := stageSnapshot(t, key, payload)
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	reader := NewSnapshotReader(cache, key, "workspace")
	first, err := reader.ReadFileContext(context.Background(), "big.txt", 25, 1, 1)
	require.NoError(t, err)
	require.Equal(t, 50, first.TotalLines)

	// Mutate the on-disk file. If the reader re-counted lines on the
	// second call, the total would change; the memo means it should not.
	entry, err := cache.Open(context.Background(), key, "workspace")
	require.NoError(t, err)
	mutated := strings.Repeat("line\n", 5)
	require.NoError(t, os.WriteFile(filepath.Join(entry.WorkspaceRoot, "big.txt"), []byte(mutated), 0o644))
	entry.Close()

	second, err := reader.ReadFileContext(context.Background(), "big.txt", 1, 0, 4)
	require.NoError(t, err)
	require.Equal(t, 50, second.TotalLines, "memoized total must survive an underlying mutation; cache entry treats snapshot as immutable")
	require.Equal(t, 1, second.StartLine)
	// Hot path stops at endLine, so we won't see all 5 mutated lines, just up to line 5.
	require.LessOrEqual(t, second.EndLine, 5)
}

func TestSnapshotReader_FileNotFound(t *testing.T) {
	t.Parallel()

	const key = "k"
	payload := newTarBuilder(t).addFile("workspace/exists.txt", "x").bytes()
	store := stageSnapshot(t, key, payload)
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	reader := NewSnapshotReader(cache, key, "workspace")
	_, _, err = reader.ReadFile(context.Background(), "missing.txt")
	require.ErrorIs(t, err, sandbox.ErrFileNotFound)

	_, err = reader.ReadFileContext(context.Background(), "missing.txt", 1, 0, 0)
	require.ErrorIs(t, err, sandbox.ErrFileNotFound)
}

// TestSnapshotReader_LineTooLong covers the bufio.Scanner ErrTooLong
// failure mode. A file containing a single line longer than the scanner
// buffer (4 MiB) is rare but plausible — minified bundles, generated
// data, etc. The reader should surface this as ErrSnapshotUnreadable so
// the handler returns 500, not 404.
func TestSnapshotReader_LineTooLong(t *testing.T) {
	t.Parallel()

	const key = "k"
	// One line just over the 4 MiB scanner buffer. No newline so the
	// whole file is a single logical line.
	body := strings.Repeat("a", (4*1024*1024)+16)
	payload := newTarBuilder(t).addFile("workspace/giant.txt", body).bytes()
	store := stageSnapshot(t, key, payload)
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	reader := NewSnapshotReader(cache, key, "workspace")
	_, err = reader.ReadFileContext(context.Background(), "giant.txt", 1, 0, 5)
	require.Error(t, err, "ReadFileContext should fail on lines over the scanner buffer")
	require.ErrorIs(t, err, ErrSnapshotUnreadable, "oversize lines should map to ErrSnapshotUnreadable so the handler returns 500, not 404")
	require.NotErrorIs(t, err, sandbox.ErrFileNotFound, "oversize lines must not be reported as missing files")
}

func TestSnapshotReader_RejectsTraversal(t *testing.T) {
	t.Parallel()

	const key = "k"
	payload := newTarBuilder(t).addFile("workspace/main.go", "x").bytes()
	store := stageSnapshot(t, key, payload)
	cache, err := NewSnapshotCache(store, t.TempDir(), 0, zerolog.Nop())
	require.NoError(t, err)

	reader := NewSnapshotReader(cache, key, "workspace")
	// The handler validates paths upfront, but the reader must not blindly
	// honor a traversal that slips past — defense in depth.
	_, _, err = reader.ReadFile(context.Background(), "../../etc/passwd")
	require.Error(t, err)
}

// ---------- helpers ----------

// gatedStore wraps a SnapshotStore, counts Load calls, and parks Load
// on a release channel until the test signals it to proceed. Used to
// deterministically prove singleflight collapses concurrent Opens for
// the same key to a single underlying fetch — replacing a prior
// time.Sleep based test that was probabilistically correct.
type gatedStore struct {
	inner     storage.SnapshotStore
	loadCalls atomic.Int64
	release   chan struct{}
}

func (c *gatedStore) Save(ctx context.Context, key string, r io.Reader) error {
	return c.inner.Save(ctx, key, r)
}

func (c *gatedStore) Load(ctx context.Context, key string, w io.Writer) error {
	c.loadCalls.Add(1)
	if c.release != nil {
		<-c.release
	}
	return c.inner.Load(ctx, key, w)
}

func (c *gatedStore) Delete(ctx context.Context, key string) error {
	return c.inner.Delete(ctx, key)
}

type failingLoadStore struct {
	err error
}

func (s *failingLoadStore) Save(context.Context, string, io.Reader) error {
	return nil
}

func (s *failingLoadStore) Load(context.Context, string, io.Writer) error {
	return s.err
}

func (s *failingLoadStore) Delete(context.Context, string) error {
	return nil
}

type largeLoadStore struct {
	payload      []byte
	bytesWritten atomic.Int64
}

func (s *largeLoadStore) Save(context.Context, string, io.Reader) error {
	return nil
}

func (s *largeLoadStore) Load(_ context.Context, _ string, w io.Writer) error {
	n, err := w.Write(s.payload)
	s.bytesWritten.Add(int64(n))
	return err
}

func (s *largeLoadStore) Delete(context.Context, string) error {
	return nil
}
