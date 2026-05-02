package workspace

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/singleflight"

	"github.com/assembledhq/143/internal/services/storage"
)

// SnapshotCache is a host-local LRU disk cache of extracted session
// workspace snapshots. The first read for a given snapshot key downloads
// the tar from object storage and extracts it under the cache directory;
// subsequent reads serve straight off local disk. Concurrent reads of the
// same key are deduped via singleflight so we never download or extract
// the same snapshot twice in parallel.
//
// Concurrency contract:
//
//   - Open is safe to call from multiple goroutines.
//   - Each successful Open returns a *SnapshotEntry that holds a
//     reference on the underlying cache entry. The entry will not be
//     evicted while any reference is outstanding, so the caller may read
//     from WorkspaceRoot for as long as it likes. Callers MUST call
//     Close exactly once on every returned SnapshotEntry; failing to do
//     so leaks a refcount and pins the on-disk extraction past the cap.
//   - The cache treats a snapshot key as immutable for the process
//     lifetime. If a session re-snapshots under the same key while the
//     old entry is cached, the cache will keep serving the stale
//     extraction until eviction or restart. That is acceptable for
//     review-time use: diff and snapshot are written within the same
//     turn boundary, so drift is rare. Operators who need fresher reads
//     can shrink the cache cap or restart the process.
type SnapshotCache struct {
	store    storage.SnapshotStore
	rootDir  string
	maxBytes int64
	logger   zerolog.Logger

	mu         sync.Mutex
	entries    map[string]*list.Element // snapshotKey → *cacheEntry
	lru        *list.List               // front = most recently used
	totalBytes int64

	sf singleflight.Group

	// maxCompressedBytes bounds the raw object bytes staged before
	// extraction. The extractor has decompressed-size caps too, but this
	// prevents a large compressed object from filling the cache disk before
	// tar parsing starts.
	maxCompressedBytes int64

	// nowFn lets tests stub out time.Now without coupling to a clock
	// dependency. Production uses time.Now.
	nowFn func() time.Time
}

type cacheEntry struct {
	key        string
	extractDir string
	sizeBytes  int64
	loadedAt   time.Time

	// refs is the count of outstanding SnapshotEntry handles pointing at
	// this entry. Protected by SnapshotCache.mu. While refs > 0 the entry
	// is pinned and eviction will skip it; releasing the last ref makes
	// the entry eligible for LRU eviction on the next over-cap insert.
	refs int

	// lineCounts memoizes total-line counts per absolute host path within
	// this extracted snapshot. Populated lazily by ReadFileContext callers
	// so repeated reads of the same file skip the full-file scan. Bounded
	// implicitly by the number of files the reviewer touches before the
	// entry is evicted; in practice that is small relative to the rest of
	// the cache footprint.
	lineCounts sync.Map // map[string]int (absPath → line count)
}

// SnapshotEntry is a refcounted handle to an extracted snapshot returned
// by Open. The handle pins the underlying on-disk extraction so an
// eviction in another goroutine cannot delete the directory mid-read.
// Callers MUST call Close exactly once when finished.
type SnapshotEntry struct {
	// WorkspaceRoot is the absolute host path corresponding to the
	// workspaceRel passed to Open — i.e. the place the session's
	// workspace files actually live inside the extraction.
	WorkspaceRoot string

	entry  *cacheEntry
	cache  *SnapshotCache
	closed bool
	mu     sync.Mutex
}

// LineCount returns a memoized total-line count for absPath if present.
func (e *SnapshotEntry) LineCount(absPath string) (int, bool) {
	if e == nil || e.entry == nil {
		return 0, false
	}
	if v, ok := e.entry.lineCounts.Load(absPath); ok {
		return v.(int), true
	}
	return 0, false
}

// StoreLineCount memoizes a total-line count for absPath. Safe for
// concurrent use; subsequent LineCount lookups will return the stored value.
func (e *SnapshotEntry) StoreLineCount(absPath string, count int) {
	if e == nil || e.entry == nil {
		return
	}
	e.entry.lineCounts.Store(absPath, count)
}

// Close releases this handle's reference on the underlying cache entry.
// Subsequent calls are no-ops. After Close, WorkspaceRoot may be removed
// from disk at any time and callers must not read from it.
func (e *SnapshotEntry) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.closed || e.cache == nil {
		e.mu.Unlock()
		return
	}
	e.closed = true
	cache := e.cache
	entry := e.entry
	e.mu.Unlock()
	cache.releaseEntry(entry)
}

// NewSnapshotCache builds a SnapshotCache rooted at rootDir, with a soft
// cap of maxBytes total extracted bytes on disk. A maxBytes <= 0 disables
// the cap entirely (useful in tests). The caller owns the rootDir; the
// cache will create it if missing but does not attempt to clean stale
// directories from a previous process run — operators can wipe rootDir
// safely between deploys if disk pressure becomes an issue.
func NewSnapshotCache(store storage.SnapshotStore, rootDir string, maxBytes int64, logger zerolog.Logger) (*SnapshotCache, error) {
	if store == nil {
		return nil, errors.New("snapshot store is required")
	}
	if rootDir == "" {
		return nil, errors.New("snapshot cache root dir is required")
	}
	if err := os.MkdirAll(rootDir, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir snapshot cache root: %w", err)
	}
	return &SnapshotCache{
		store:              store,
		rootDir:            rootDir,
		maxBytes:           maxBytes,
		logger:             logger,
		entries:            make(map[string]*list.Element),
		lru:                list.New(),
		maxCompressedBytes: defaultMaxCompressedSnapshotBytes,
		nowFn:              time.Now,
	}, nil
}

// maxOpenRetries bounds the retry loop in Open. The retry path covers
// the rare case where a fresh entry is evicted between fetchAndExtract
// and the caller's acquire under heavy concurrent insert pressure
// across many keys; in practice a single retry suffices, so a low cap
// keeps us from spinning if something is structurally wrong.
const maxOpenRetries = 3

// defaultMaxCompressedSnapshotBytes caps the object bytes written to the
// staging file before extraction. Keep this below the decompressed stream cap
// so a single snapshot cannot consume unbounded cache disk even if the
// compressed artifact itself is pathological.
const defaultMaxCompressedSnapshotBytes int64 = 2 << 30 // 2 GiB

// Open returns a refcounted SnapshotEntry rooted at workspaceRel inside
// the extracted snapshot. workspaceRel is the in-tar path of the
// session's workspace directory — for sessions without an attached repo
// this is "workspace"; for sessions whose orchestrator placed the
// checkout under `<HomeDir>/<slug>` it is e.g. "home/sandbox/<slug>".
// Pass exactly the same path the producer fed to `tar -C / <path>`
// (with the leading slash stripped). An empty workspaceRel roots the
// entry at the extraction directory itself.
//
// The returned SnapshotEntry holds a reference on the underlying cache
// entry; callers MUST call Close on it when they are done reading.
// While at least one SnapshotEntry is open against an entry, the cache
// will not evict it — even if total bytes exceed the cap.
//
// On a cache miss this method downloads the snapshot tar from object
// storage and extracts it. The extraction is sandboxed: hostile tar
// entries are skipped and oversized files are rejected, so a single bad
// snapshot cannot poison the cache directory.
func (c *SnapshotCache) Open(ctx context.Context, key, workspaceRel string) (*SnapshotEntry, error) {
	if key == "" {
		return nil, errors.New("snapshot key is required")
	}

	// Retry loop covers the rare race where a freshly-inserted entry is
	// evicted between singleflight Do returning and the caller's acquire.
	// Under normal load this finishes on the first attempt.
	for attempt := 0; attempt < maxOpenRetries; attempt++ {
		// Phase 1: fast path — entry already cached.
		if entry, ok := c.acquireExisting(key); ok {
			return c.handleOpen(entry, workspaceRel)
		}

		// Phase 2: cache miss. Singleflight collapses concurrent Opens
		// for the same key to a single underlying fetch+extract.
		v, err, _ := c.sf.Do(key, func() (interface{}, error) {
			// Re-check under singleflight: a prior caller in the same
			// wave may have populated the cache between our first check
			// and our turn.
			if entry, ok := c.peekEntry(key); ok {
				return entry, nil
			}
			fresh, err := c.fetchAndExtract(ctx, key)
			if err != nil {
				return nil, err
			}
			c.insert(fresh)
			return fresh, nil
		})
		if err != nil {
			return nil, err
		}
		entry := v.(*cacheEntry)

		// Acquire a ref under the cache mutex. If the entry was evicted
		// since insert (possible only when a flurry of inserts for other
		// keys pushed it back), retry.
		if c.tryAcquireKnown(entry) {
			return c.handleOpen(entry, workspaceRel)
		}
	}
	return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("snapshot %s evicted before reader could acquire it", key))
}

// acquireExisting promotes a cached entry to the front of the LRU and
// increments its refcount under the mutex. ok=false if not cached.
func (c *SnapshotCache) acquireExisting(key string) (*cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(elem)
	entry := elem.Value.(*cacheEntry)
	entry.loadedAt = c.nowFn()
	entry.refs++
	return entry, true
}

// peekEntry checks for an entry without touching its refcount. Used
// inside the singleflight callback to short-circuit if a prior leader
// already inserted while we were waiting our turn — the eventual
// tryAcquireKnown step will increment refs under the mutex.
func (c *SnapshotCache) peekEntry(key string) (*cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return elem.Value.(*cacheEntry), true
}

// tryAcquireKnown bumps refs on a known entry pointer, but only if it's
// still the live entry under that key. Returns false when the entry was
// evicted; the caller (Open) retries from scratch.
func (c *SnapshotCache) tryAcquireKnown(want *cacheEntry) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[want.key]
	if !ok {
		return false
	}
	if elem.Value.(*cacheEntry) != want {
		return false
	}
	c.lru.MoveToFront(elem)
	want.loadedAt = c.nowFn()
	want.refs++
	return true
}

// insert adds a freshly extracted entry to the LRU with refs=0 and
// triggers eviction if the result is over cap. The entry is at the
// front of the LRU when this returns; the "never evict the front" rule
// keeps it alive until the caller's tryAcquireKnown bumps its refcount.
// On a duplicate-key race (which singleflight should prevent), the
// older entry wins and the dup directory is removed.
func (c *SnapshotCache) insert(entry *cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[entry.key]; ok {
		// Defense in depth — singleflight should have prevented this.
		if rmErr := os.RemoveAll(entry.extractDir); rmErr != nil {
			c.logger.Warn().Err(rmErr).Str("snapshot_key", entry.key).Msg("snapshot cache: failed to remove duplicate extract dir")
		}
		c.lru.MoveToFront(existing)
		return
	}
	elem := c.lru.PushFront(entry)
	c.entries[entry.key] = elem
	c.totalBytes += entry.sizeBytes
	c.evictLocked()
}

// releaseEntry decrements an entry's refcount and runs eviction if the
// release brought a previously-pinned entry within reach AND the cache
// is over cap. Idempotent against entries already evicted from the LRU.
func (c *SnapshotCache) releaseEntry(entry *cacheEntry) {
	if entry == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.refs > 0 {
		entry.refs--
	}
	if c.maxBytes > 0 && c.totalBytes > c.maxBytes {
		c.evictLocked()
	}
}

// evictLocked must be called with c.mu held. Walks the LRU back→front
// and evicts the first entry with refs == 0 until totalBytes is under
// cap. Front of the LRU (most recently used) is never evicted, even if
// alone over cap, so a freshly-inserted entry survives long enough for
// the caller to acquire its ref. In-use entries are skipped silently;
// total bytes may temporarily exceed the cap until those handles are
// closed.
func (c *SnapshotCache) evictLocked() {
	if c.maxBytes <= 0 {
		return
	}
	for c.totalBytes > c.maxBytes {
		var victim *list.Element
		for e := c.lru.Back(); e != nil; e = e.Prev() {
			if e == c.lru.Front() {
				// Don't evict the front — it's either the just-inserted
				// entry (still being acquired) or the last cache hit.
				break
			}
			ce := e.Value.(*cacheEntry)
			if ce.refs == 0 {
				victim = e
				break
			}
		}
		if victim == nil {
			// Either everything is pinned or only the front remains.
			// Bail out; we'll try again on the next release/insert.
			return
		}
		ce := victim.Value.(*cacheEntry)
		c.lru.Remove(victim)
		delete(c.entries, ce.key)
		c.totalBytes -= ce.sizeBytes
		if err := os.RemoveAll(ce.extractDir); err != nil {
			c.logger.Warn().
				Err(err).
				Str("snapshot_key", ce.key).
				Str("extract_dir", ce.extractDir).
				Msg("snapshot cache evict: failed to remove extract dir")
		}
	}
}

// handleOpen builds the SnapshotEntry handle returned by Open. On a
// resolve failure it releases the ref it just acquired so the caller
// doesn't leak a pin on a never-returned handle.
func (c *SnapshotCache) handleOpen(entry *cacheEntry, workspaceRel string) (*SnapshotEntry, error) {
	rooted, err := joinWorkspaceRel(entry.extractDir, workspaceRel)
	if err != nil {
		c.releaseEntry(entry)
		return nil, err
	}
	return &SnapshotEntry{WorkspaceRoot: rooted, entry: entry, cache: c}, nil
}

// fetchAndExtract downloads the snapshot tar from object storage, writes
// it to a per-key temp file, then extracts it into the final cache
// directory. The tar download is staged separately so a transport error
// midway through does not leave a half-extracted directory under the
// cache root.
func (c *SnapshotCache) fetchAndExtract(ctx context.Context, key string) (*cacheEntry, error) {
	startedAt := c.nowFn()
	hash := keyHash(key)
	stageDir := filepath.Join(c.rootDir, "_stage", hash)
	finalDir := filepath.Join(c.rootDir, hash[:2], hash[2:])

	if err := os.RemoveAll(stageDir); err != nil {
		return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("clear stage dir: %w", err))
	}
	if err := os.MkdirAll(stageDir, 0o750); err != nil {
		return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("mkdir stage dir: %w", err))
	}
	defer os.RemoveAll(stageDir)

	// The producer (DockerProvider.Snapshot) emits a gzipped tar today, but
	// the storage layer treats the bytes as opaque, and this staging file
	// is purely a transient buffer between download and extraction. Naming
	// it after a specific compression scheme would imply more guarantees
	// than we make. The path is a join of the operator-configured rootDir
	// and a SHA-256 of the snapshot key, so the filename component is
	// fixed-format and cannot escape rootDir.
	tarPath := filepath.Join(stageDir, "snapshot.tar")
	tarFile, err := os.OpenFile(tarPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- tarPath is rootDir + sha256(key); fixed filename, never user input
	if err != nil {
		return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("open stage tar: %w", err))
	}

	stageWriter := io.Writer(tarFile)
	if c.maxCompressedBytes > 0 {
		stageWriter = &cappedWriter{w: tarFile, capLimit: c.maxCompressedBytes}
	}
	if loadErr := c.store.Load(ctx, key, stageWriter); loadErr != nil {
		_ = tarFile.Close()
		if errors.Is(loadErr, storage.ErrSnapshotNotFound) {
			return nil, ErrSnapshotMissing
		}
		if errors.Is(loadErr, errOversize) {
			return nil, errors.Join(ErrSnapshotUnreadable, fmt.Errorf("stage snapshot %s: %w", key, loadErr))
		}
		return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("load snapshot %s: %w", key, loadErr))
	}
	if err := tarFile.Close(); err != nil {
		return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("close stage tar: %w", err))
	}

	tarBytes, err := fileSize(tarPath)
	if err != nil {
		return nil, errors.Join(ErrSnapshotUnavailable, err)
	}

	src, err := os.Open(tarPath) // #nosec G304 -- tarPath is the same staging file we just wrote above; bounded under rootDir
	if err != nil {
		return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("reopen stage tar: %w", err))
	}
	defer src.Close()

	extractStage := filepath.Join(stageDir, "extracted")
	if err := os.MkdirAll(extractStage, 0o750); err != nil {
		return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("mkdir extract stage: %w", err))
	}

	written, err := extractTarGz(src, extractStage, c.logger)
	if err != nil {
		// Tag the error with ErrSnapshotUnreadable so the handler can map
		// "snapshot is corrupt / fails safety checks" to a distinct HTTP
		// response from the existing "snapshot is missing" path. Without
		// this, an unreadable archive looks like a missing file to the
		// caller and the diff UI silently disables the wrong way.
		return nil, errors.Join(ErrSnapshotUnreadable, fmt.Errorf("extract snapshot %s: %w", key, err))
	}

	// Atomically place the extracted tree at its final path. Remove any
	// previous extraction first; on the success path it's empty, but a
	// crash during a previous fetch could have left a partial tree.
	if err := os.RemoveAll(finalDir); err != nil {
		return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("clear final dir: %w", err))
	}
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o750); err != nil {
		return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("mkdir final parent: %w", err))
	}
	if err := os.Rename(extractStage, finalDir); err != nil {
		return nil, errors.Join(ErrSnapshotUnavailable, fmt.Errorf("promote extract dir: %w", err))
	}

	loadDuration := c.nowFn().Sub(startedAt)
	c.logger.Info().
		Str("snapshot_key", key).
		Int64("tar_size_bytes", tarBytes).
		Int64("extracted_bytes", written).
		Dur("load_duration", loadDuration).
		Msg("snapshot cache miss: fetched and extracted")

	return &cacheEntry{
		key:        key,
		extractDir: finalDir,
		sizeBytes:  written,
		loadedAt:   c.nowFn(),
	}, nil
}

// keyHash returns a hex SHA-256 of the snapshot key. Used as the on-disk
// directory name so we can accept arbitrary key strings (including ones
// with slashes) without coupling our directory layout to the storage
// key format.
func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// fileSize returns the byte size of a regular file. Convenience wrapper
// around os.Stat for the cache fetch path.
func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return fi.Size(), nil
}

// joinWorkspaceRel produces the absolute host path of the workspace
// directory inside an extracted snapshot. workspaceRel is the in-tar
// relative path of the workspace ("workspace", "home/sandbox/<slug>",
// etc.). Returns an error for inputs that would resolve outside the
// extraction (absolute paths, traversal segments, NUL bytes) so a
// misconfigured caller cannot hand the snapshot reader a host path
// outside the cache directory.
func joinWorkspaceRel(extractDir, workspaceRel string) (string, error) {
	clean := strings.Trim(filepath.ToSlash(workspaceRel), "/")
	if clean == "" || clean == "." {
		return extractDir, nil
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid workspace prefix %q", workspaceRel)
		}
	}
	if strings.ContainsRune(clean, 0) {
		return "", fmt.Errorf("invalid workspace prefix %q", workspaceRel)
	}
	joined := filepath.Join(extractDir, filepath.FromSlash(clean))
	if joined != extractDir && !strings.HasPrefix(joined, extractDir+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid workspace prefix %q", workspaceRel)
	}
	return joined, nil
}
