package preview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
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

// An executor that cannot stream stdin must be rejected at construction. Caught
// at call time instead, it surfaces as a restore error, which the start path
// swallows into a cold launch — the cache would silently never hit.
func TestNewSnapshotCache_RequiresStdinCapableExecutor(t *testing.T) {
	t.Parallel()

	_, err := NewSnapshotCache(SnapshotCacheConfig{
		Executor:     execWithoutStdin{},
		CacheDir:     t.TempDir(),
		WorkerNodeID: "worker-1",
		Logger:       zerolog.Nop(),
	})
	require.Error(t, err, "an executor without ExecWithStdin should fail construction")
	require.Contains(t, err.Error(), "ExecWithStdin")
}

func TestSnapshotTarExitTolerable(t *testing.T) {
	t.Parallel()

	// 0 is success; 1 is "some files differ" — tar still produced a complete
	// archive, which on a live preview workspace is the common case and is worth
	// keeping. 2 and up are fatal.
	require.True(t, tarExitTolerable(0), "success must be tolerable")
	require.True(t, tarExitTolerable(1), "a live workspace must not cost us the snapshot")
	require.False(t, tarExitTolerable(2), "a fatal tar error must not be stored")
	require.False(t, tarExitTolerable(137), "an OOM kill must not be stored")
}

type streamingRestoreExecutor struct {
	writeFromReaderCalled bool
	writeFileCalled       bool
	execCmds              []string
	stdinCmds             []string
	// stdinWrites records each stdin stream in call order, so a test that drives
	// more than one (ApplyPartialInvalidation streams the blob, then the patch)
	// can assert on the specific one it cares about rather than on whichever
	// wrote last.
	stdinWrites [][]byte
	// stdinExit and stdinErr force the stdin exec to fail, exercising the
	// workspace-recovery path.
	stdinExit int
	stdinErr  error
	// tarCreateExit is the exit code returned for the create-side `tar -c`,
	// letting a test drive the exit-1-on-a-live-workspace case.
	tarCreateExit int
	// failRecovery makes the git-checkout recovery exec fail, so the
	// unrecoverable-workspace path can be reached.
	failRecovery bool
	// stdout, when set, is written to the Exec stdout writer for the create-side
	// tar, simulating the sandbox emitting the archive.
	stdout []byte
}

// lastStdinWrite returns the most recent stdin stream, or nil if there was none.
func (e *streamingRestoreExecutor) lastStdinWrite() []byte {
	if len(e.stdinWrites) == 0 {
		return nil
	}
	return e.stdinWrites[len(e.stdinWrites)-1]
}

func (e *streamingRestoreExecutor) Exec(_ context.Context, _ *agent.Sandbox, cmd string, stdout, _ io.Writer) (int, error) {
	e.execCmds = append(e.execCmds, cmd)
	if e.failRecovery && strings.Contains(cmd, "git checkout -f HEAD") {
		return 1, nil
	}
	if strings.HasPrefix(cmd, "tar -c ") {
		if len(e.stdout) > 0 {
			if _, err := stdout.Write(e.stdout); err != nil {
				return 2, err
			}
		}
		return e.tarCreateExit, nil
	}
	return 0, nil
}

func (e *streamingRestoreExecutor) ExecWithStdin(_ context.Context, _ *agent.Sandbox, cmd string, stdin io.Reader, _, _ io.Writer) (int, error) {
	e.stdinCmds = append(e.stdinCmds, cmd)
	body, err := io.ReadAll(stdin)
	if err != nil {
		return 2, err
	}
	e.stdinWrites = append(e.stdinWrites, append([]byte(nil), body...))
	if e.stdinErr != nil || e.stdinExit != 0 {
		return e.stdinExit, e.stdinErr
	}
	return 0, nil
}

func (e *streamingRestoreExecutor) ReadFile(context.Context, *agent.Sandbox, string) ([]byte, error) {
	return nil, nil
}

func (e *streamingRestoreExecutor) WriteFile(context.Context, *agent.Sandbox, string, []byte) error {
	e.writeFileCalled = true
	return nil
}

func (e *streamingRestoreExecutor) WriteFileFromReader(_ context.Context, _ *agent.Sandbox, _ string, reader io.Reader, _ int64) error {
	e.writeFromReaderCalled = true
	if _, err := io.ReadAll(reader); err != nil {
		return err
	}
	return nil
}

// hasCmdContaining reports whether any recorded command contains sub.
func hasCmdContaining(cmds []string, sub string) bool {
	for _, cmd := range cmds {
		if strings.Contains(cmd, sub) {
			return true
		}
	}
	return false
}

// execWithoutStdin satisfies SnapshotExecutor but deliberately omits
// ExecWithStdin, so it fails the sandboxStdinExecutor assertion.
type execWithoutStdin struct{}

func (execWithoutStdin) Exec(context.Context, *agent.Sandbox, string, io.Writer, io.Writer) (int, error) {
	return 0, nil
}
func (execWithoutStdin) ReadFile(context.Context, *agent.Sandbox, string) ([]byte, error) {
	return nil, nil
}
func (execWithoutStdin) WriteFile(context.Context, *agent.Sandbox, string, []byte) error { return nil }
func (execWithoutStdin) WriteFileFromReader(context.Context, *agent.Sandbox, string, io.Reader, int64) error {
	return nil
}

func TestSnapshotCache_RestoreSnapshotStreamsBlobToSandbox(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	cacheDir := t.TempDir()
	blobPath := filepath.Join(cacheDir, "snapshot.tar.gz")
	body := []byte("compressed snapshot body")
	require.NoError(t, os.WriteFile(blobPath, body, 0o600), "snapshot blob should be written")
	sum := sha256.Sum256(body)
	require.NoError(t, os.WriteFile(blobPath+".sha256", []byte(hex.EncodeToString(sum[:])), 0o600), "snapshot checksum should be written")

	orgID := uuid.New()
	entryID := uuid.New()
	executor := &streamingRestoreExecutor{}
	sc := &SnapshotCache{
		store:    db.NewPreviewStore(mock),
		executor: executor,
		logger:   zerolog.Nop(),
	}
	hit := &CacheHit{
		Entry: models.PreviewStartupCache{
			ID:          entryID,
			OrgID:       orgID,
			SnapshotKey: "snapshot-key",
			BlobPath:    blobPath,
			SizeBytes:   int64(len(body)),
		},
		BlobPath: blobPath,
	}

	mock.ExpectExec("UPDATE preview_startup_cache SET last_used_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = sc.RestoreSnapshot(context.Background(), &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}, hit)
	require.NoError(t, err, "RestoreSnapshot should restore a valid snapshot")
	require.Equal(t, body, executor.lastStdinWrite(), "RestoreSnapshot should stream the exact blob contents")
	require.True(t, hasCmdContaining(executor.stdinCmds, "tar xf - -C '/workspace/repo'"),
		"RestoreSnapshot should pipe the blob into a sandbox-side tar reading stdin, got %v", executor.stdinCmds)
	require.False(t, executor.writeFromReaderCalled,
		"RestoreSnapshot must not stage the blob on the sandbox filesystem — /tmp is a 256 MiB tmpfs")
	require.False(t, executor.writeFileCalled, "RestoreSnapshot should not materialize the blob through WriteFile")
	require.False(t, hasCmdContaining(executor.execCmds, "tar xf /tmp/snapshot.tar.zst"),
		"RestoreSnapshot should not extract from the pre-streaming staging path, got %v", executor.execCmds)
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// A streaming extract can fail after the workspace has already been wiped,
// where the old stage-then-extract form always failed before the wipe. The
// caller treats a restore error as "launch cold" and reuses the sandbox, so a
// failed extract must leave a workspace rebuilt from the clone rather than an
// empty directory.
func TestSnapshotCache_RestoreSnapshotRecoversWorkspaceOnExtractFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	cacheDir := t.TempDir()
	blobPath := filepath.Join(cacheDir, "snapshot.tar.gz")
	body := []byte("compressed snapshot body")
	require.NoError(t, os.WriteFile(blobPath, body, 0o600), "snapshot blob should be written")

	executor := &streamingRestoreExecutor{stdinExit: 2}
	sc := &SnapshotCache{
		store:    db.NewPreviewStore(mock),
		executor: executor,
		logger:   zerolog.Nop(),
	}
	hit := &CacheHit{
		Entry: models.PreviewStartupCache{
			ID:          uuid.New(),
			OrgID:       uuid.New(),
			SnapshotKey: "snapshot-key",
			BlobPath:    blobPath,
			SizeBytes:   int64(len(body)),
		},
		BlobPath: blobPath,
	}

	err = sc.RestoreSnapshot(context.Background(), &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}, hit)
	require.Error(t, err, "a fatal extract exit should fail the restore")
	require.Contains(t, err.Error(), "tar exited 2", "the error should carry tar's exit code")
	require.True(t, hasCmdContaining(executor.execCmds, "git checkout -f HEAD -- ."),
		"a failed extract must rebuild the wiped workspace from the clone, got %v", executor.execCmds)
	require.NotErrorIs(t, err, ErrPreviewWorkspaceUnrecoverable,
		"recovery succeeded, so the caller should still be free to launch cold")
}

// When recovery itself fails the tree matches neither the snapshot nor the
// checkout. That is the one case the caller must fail the start on rather than
// build whatever is left, so it has to be distinguishable.
func TestSnapshotCache_RestoreSnapshotFlagsUnrecoverableWorkspace(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	cacheDir := t.TempDir()
	blobPath := filepath.Join(cacheDir, "snapshot.tar.gz")
	body := []byte("compressed snapshot body")
	require.NoError(t, os.WriteFile(blobPath, body, 0o600), "snapshot blob should be written")

	executor := &streamingRestoreExecutor{stdinExit: 2, failRecovery: true}
	sc := &SnapshotCache{
		store:    db.NewPreviewStore(mock),
		executor: executor,
		logger:   zerolog.Nop(),
	}
	hit := &CacheHit{
		Entry: models.PreviewStartupCache{
			ID: uuid.New(), OrgID: uuid.New(), SnapshotKey: "snapshot-key",
			BlobPath: blobPath, SizeBytes: int64(len(body)),
		},
		BlobPath: blobPath,
	}

	err = sc.RestoreSnapshot(context.Background(), &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}, hit)
	require.Error(t, err, "a failed extract with failed recovery must error")
	require.ErrorIs(t, err, ErrPreviewWorkspaceUnrecoverable,
		"the caller needs to tell this apart from an ordinary cold-launch fallback")
}

// A restore that reports non-fatal warnings still produced a tree. Throwing it
// away costs a full cold launch, so keep it — a genuinely broken workspace
// surfaces as a build failure moments later.
func TestSnapshotCache_RestoreSnapshotToleratesTarWarnings(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	cacheDir := t.TempDir()
	blobPath := filepath.Join(cacheDir, "snapshot.tar.gz")
	body := []byte("compressed snapshot body")
	require.NoError(t, os.WriteFile(blobPath, body, 0o600), "snapshot blob should be written")

	executor := &streamingRestoreExecutor{stdinExit: 1}
	sc := &SnapshotCache{
		store:    db.NewPreviewStore(mock),
		executor: executor,
		logger:   zerolog.Nop(),
	}
	hit := &CacheHit{
		Entry: models.PreviewStartupCache{
			ID: uuid.New(), OrgID: uuid.New(), SnapshotKey: "snapshot-key",
			BlobPath: blobPath, SizeBytes: int64(len(body)),
		},
		BlobPath: blobPath,
	}
	mock.ExpectExec("UPDATE preview_startup_cache SET last_used_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = sc.RestoreSnapshot(context.Background(), &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}, hit)
	require.NoError(t, err, "tar exit 1 should not throw away a restored workspace")
	require.False(t, hasCmdContaining(executor.execCmds, "git checkout -f HEAD -- ."),
		"a tolerated warning must not trigger workspace recovery, got %v", executor.execCmds)
	require.NoError(t, mock.ExpectationsWereMet(), "the restore should have completed through the LRU touch")
}

// The create path must archive straight to the worker. Staging inside the
// sandbox capped snapshots at the 256 MiB /tmp tmpfs and charged the bytes to
// the container's memory cgroup.
// newCreateSnapshotFixture wires a SnapshotCache over a pgxmock pool with the
// upsert and post-create eviction queries stubbed, so create tests only have to
// vary the executor's behaviour. expectStore=false skips the query expectations
// for cases that fail before touching the database.
func newCreateSnapshotFixture(t *testing.T, executor SnapshotExecutor, archiveLen int, expectStore bool) (*SnapshotCache, SnapshotMetadata) {
	t.Helper()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	t.Cleanup(mock.Close)

	cacheDir := t.TempDir()
	sc := &SnapshotCache{
		store:         db.NewPreviewStore(mock),
		executor:      executor,
		cacheDir:      cacheDir,
		workerNodeID:  "worker-1",
		maxCacheBytes: DefaultMaxCacheBytes,
		logger:        zerolog.Nop(),
		// Shrink the floor so these cases run on a handful of bytes instead of
		// allocating and streaming a megabyte each. The real constant is
		// exercised by TestUndersizedSnapshotReason_AbsoluteFloor, which needs
		// no fixture at all.
		minSnapshotBytesOverride: 8,
	}
	metadata := SnapshotMetadata{OrgID: uuid.New(), RepoID: uuid.New(), BaseKey: "base", CommitSHA: "abc123"}
	if !expectStore {
		return sc, metadata
	}

	cacheCols := []string{
		"id", "org_id", "repo_id", "snapshot_key", "base_key", "commit_sha", "blob_path",
		"size_bytes", "worker_node_id", "last_used_at", "created_at",
	}
	// The size floor consults the last snapshot for this base key before storing.
	// Return a 1-byte baseline so the shrink rule is trivially satisfied and only
	// the (overridden) absolute floor is in play; the shrink rule has its own
	// test.
	//
	// WithArgs is required: pgxmock reads a missing WithArgs as "expects zero
	// arguments", so without it this expectation never matches, stays
	// unfulfilled, and the next query collides with it.
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(cacheCols).AddRow(
			uuid.New(), metadata.OrgID, metadata.RepoID, "older-key", metadata.BaseKey, "older-commit",
			filepath.Join(cacheDir, "older-blob"), int64(1), "worker-1", time.Now(), time.Now(),
		))
	mock.ExpectQuery("INSERT INTO preview_startup_cache").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(cacheCols).AddRow(
			uuid.New(), metadata.OrgID, metadata.RepoID, "snapshot-key", metadata.BaseKey, metadata.CommitSHA,
			filepath.Join(cacheDir, "blob"), int64(archiveLen), "worker-1", time.Now(), time.Now(),
		))
	mock.ExpectQuery("SELECT").WillReturnRows(pgxmock.NewRows(cacheCols))
	return sc, metadata
}

func TestSnapshotCache_CreateSnapshotStreamsArchiveToWorker(t *testing.T) {
	t.Parallel()

	archive := []byte("streamed archive body")
	executor := &streamingRestoreExecutor{stdout: archive}
	sc, metadata := newCreateSnapshotFixture(t, executor, len(archive), true)

	sizeBytes, err := sc.CreateSnapshot(context.Background(), &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}, "snapshot-key", metadata, nil)
	require.NoError(t, err, "CreateSnapshot should stream the archive to the worker")
	require.EqualValues(t, len(archive), sizeBytes, "the reported size should be what was stored")

	require.True(t, hasCmdContaining(executor.execCmds, "tar -c "),
		"CreateSnapshot should run a sandbox-side tar, got %v", executor.execCmds)
	require.True(t, hasCmdContaining(executor.execCmds, "-f - "),
		"CreateSnapshot must archive to stdout, not to a file inside the sandbox, got %v", executor.execCmds)
	require.True(t, hasCmdContaining(executor.execCmds, "-C '/workspace/repo'"),
		"CreateSnapshot should archive from the workspace root, got %v", executor.execCmds)
	require.True(t, hasCmdContaining(executor.execCmds, "--ignore-failed-read"),
		"a file vanishing mid-walk on a live workspace must not abort the archive, got %v", executor.execCmds)
	require.False(t, hasCmdContaining(executor.execCmds, "-f /tmp/snapshot.tar.zst"),
		"CreateSnapshot must not stage the archive in the sandbox tmpfs, got %v", executor.execCmds)
	require.False(t, hasCmdContaining(executor.execCmds, "cat "),
		"CreateSnapshot should no longer need a second exec to read a staged file, got %v", executor.execCmds)

	blobPath, err := sc.blobPath("snapshot-key")
	require.NoError(t, err, "blob path should resolve")
	written, err := os.ReadFile(blobPath) // #nosec G304 -- test-controlled temp dir
	require.NoError(t, err, "the streamed blob should land on the worker's disk")
	require.Equal(t, archive, written, "the worker blob should hold exactly what the sandbox tar emitted")
}

// Snapshots are taken moments after the preview reports ready, so services are
// actively writing into the tree tar is reading. tar exits 1 for that but still
// produces a complete archive — discarding it would mean the busiest workspaces,
// the ones most expensive to rebuild, are exactly the ones that never cache.
func TestSnapshotCache_CreateSnapshotKeepsArchiveWhenFilesChangedUnderTar(t *testing.T) {
	t.Parallel()

	archive := []byte("archive from a workspace that moved")
	executor := &streamingRestoreExecutor{stdout: archive, tarCreateExit: 1}
	sc, metadata := newCreateSnapshotFixture(t, executor, len(archive), true)

	_, err := sc.CreateSnapshot(context.Background(), &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}, "snapshot-key", metadata, nil)
	require.NoError(t, err, "tar exit 1 on a live workspace must still produce a usable snapshot")

	blobPath, err := sc.blobPath("snapshot-key")
	require.NoError(t, err, "blob path should resolve")
	written, err := os.ReadFile(blobPath) // #nosec G304 -- test-controlled temp dir
	require.NoError(t, err, "the archive should have been stored despite the warning")
	require.Equal(t, archive, written, "the stored blob should hold what tar emitted")
}

// Tolerance stops at exit 1. A fatal tar error means a broken or truncated
// stream, which must never be stored as a restorable snapshot.
func TestSnapshotCache_CreateSnapshotRejectsFatalTarExit(t *testing.T) {
	t.Parallel()

	executor := &streamingRestoreExecutor{stdout: []byte("truncated"), tarCreateExit: 2}
	sc, metadata := newCreateSnapshotFixture(t, executor, 0, false)

	_, err := sc.CreateSnapshot(context.Background(), &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}, "snapshot-key", metadata, nil)
	require.Error(t, err, "a fatal tar exit must fail the create")
	require.Contains(t, err.Error(), "tar exited 2")

	blobPath, err := sc.blobPath("snapshot-key")
	require.NoError(t, err, "blob path should resolve")
	_, statErr := os.Stat(blobPath)
	require.True(t, os.IsNotExist(statErr), "a fatally-failed archive must not be left on disk")
}

// The partial-invalidation patch must reach git over stdin. Writing it to
// /tmp put up to maxPartialInvalidationDiffBytes into a 256 MiB tmpfs, whose
// bytes come out of the sandbox's memory cgroup right before it builds.
func TestSnapshotCache_ApplyPartialInvalidationStreamsDiff(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	cacheDir := t.TempDir()
	blobPath := filepath.Join(cacheDir, "snapshot.tar.gz")
	body := []byte("compressed snapshot body")
	require.NoError(t, os.WriteFile(blobPath, body, 0o600), "snapshot blob should be written")

	executor := &streamingRestoreExecutor{}
	sc := &SnapshotCache{
		store:    db.NewPreviewStore(mock),
		executor: executor,
		logger:   zerolog.Nop(),
	}
	hit := &CacheHit{
		Entry: models.PreviewStartupCache{
			ID:          uuid.New(),
			OrgID:       uuid.New(),
			SnapshotKey: "snapshot-key",
			BlobPath:    blobPath,
			SizeBytes:   int64(len(body)),
		},
		BlobPath: blobPath,
	}
	mock.ExpectExec("UPDATE preview_startup_cache SET last_used_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	diff := []byte("diff --git a/x b/x\n@@ -1 +1 @@\n-a\n+b\n")
	err = sc.ApplyPartialInvalidation(context.Background(), &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}, hit, diff)
	require.NoError(t, err, "ApplyPartialInvalidation should apply a streamed diff")

	require.True(t, hasCmdContaining(executor.stdinCmds, "git apply --allow-empty"),
		"the patch should be piped into git apply over stdin, got %v", executor.stdinCmds)
	require.Equal(t, diff, executor.lastStdinWrite(), "git apply should receive the exact diff bytes, and it should be the last stream")
	require.False(t, executor.writeFileCalled,
		"ApplyPartialInvalidation must not stage the patch on the sandbox filesystem")
	require.False(t, hasCmdContaining(executor.execCmds, "/tmp/partial.diff"),
		"no command should reference the old /tmp patch path, got %v", executor.execCmds)
}

func TestSnapshotExtraExcludeFlags(t *testing.T) {
	t.Parallel()

	t.Run("empty input yields no flags", func(t *testing.T) {
		t.Parallel()
		require.Empty(t, snapshotExtraExcludeFlags(nil))
		require.Empty(t, snapshotExtraExcludeFlags([]string{}))
	})

	t.Run("emits bare and ./-rooted forms, quoted", func(t *testing.T) {
		t.Parallel()
		flags := snapshotExtraExcludeFlags([]string{"config/dev.json"})
		// Members are stored as "./config/dev.json"; both forms guarantee a match
		// regardless of how the tar implementation anchors patterns.
		require.Contains(t, flags, "'--exclude=config/dev.json'")
		require.Contains(t, flags, "'--exclude=./config/dev.json'")
		// Concatenated directly after the shared flags, so it must lead with a space.
		require.True(t, strings.HasPrefix(flags, " "), "extra flags should begin with a separating space")
	})

	t.Run("normalizes a ./-prefixed path so it is not double-rooted", func(t *testing.T) {
		t.Parallel()
		flags := snapshotExtraExcludeFlags([]string{"./.env.local"})
		require.Contains(t, flags, "'--exclude=.env.local'")
		require.Contains(t, flags, "'--exclude=./.env.local'")
		require.NotContains(t, flags, "././", "leading ./ must be trimmed before re-rooting")
	})
}

// Tolerating tar exit 1 made "tar complained" a routine success path, and tar
// emits a warning line per file that moved under it. The stderr we hold in
// worker memory and ship to the log pipeline has to stay bounded.
func TestBoundedBuffer_CapsRetainedOutput(t *testing.T) {
	t.Parallel()

	b := &boundedBuffer{limit: 16}
	n, err := b.Write([]byte("0123456789"))
	require.NoError(t, err, "a stderr writer must never error — that would kill the command")
	require.Equal(t, 10, n, "Write must report full consumption")

	n, err = b.Write([]byte("abcdefghijklmnop"))
	require.NoError(t, err)
	require.Equal(t, 16, n, "Write must report full consumption even past the limit")

	got := b.String()
	require.Contains(t, got, "0123456789abcdef", "the retained prefix should be kept verbatim")
	require.Contains(t, got, "10 more bytes suppressed", "the dropped count should be surfaced")
	require.Less(t, len(got), 100, "a runaway stderr must not reach the log intact")
}

func TestBoundedBuffer_UnderLimitIsVerbatim(t *testing.T) {
	t.Parallel()

	b := &boundedBuffer{limit: 1024}
	_, err := b.Write([]byte("tar: x: file changed as we read it"))
	require.NoError(t, err)
	require.Equal(t, "tar: x: file changed as we read it", b.String(),
		"output under the limit should carry no suppression note")
}

// A snapshot is restored by wiping the workspace and unpacking in its place, so
// storing a degenerate archive poisons every later launch that matches its key.
// The floor must reject it and leave the staged file behind on disk.
func TestSnapshotCache_CreateSnapshotRejectsUndersizedArchive(t *testing.T) {
	t.Parallel()

	archive := []byte("tiny") // under the fixture's floor
	executor := &streamingRestoreExecutor{stdout: archive}
	sc, metadata := newCreateSnapshotFixture(t, executor, len(archive), false)

	sizeBytes, err := sc.CreateSnapshot(context.Background(), &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}, "snapshot-key", metadata, nil)
	require.Error(t, err, "an archive under the floor must not be stored")
	require.Zero(t, sizeBytes, "nothing was stored, so the reported size must be zero")
	require.ErrorIs(t, err, ErrSnapshotTooSmall,
		"the caller needs to tell a rejected archive apart from a failed create")

	blobPath, err := sc.blobPath("snapshot-key")
	require.NoError(t, err, "blob path should resolve")
	_, statErr := os.Stat(blobPath)
	require.True(t, os.IsNotExist(statErr), "a rejected archive must not be left on disk")

	entries, err := os.ReadDir(sc.cacheDir)
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasSuffix(e.Name(), ".tmp"),
			"the staged temp file should have been cleaned up, found %s", e.Name())
	}
}

// The absolute floor cannot catch a snapshot that is large but has lost most of
// its content. Two snapshots sharing a base key describe the same dependency
// tree, so a fourfold collapse is damage, not drift.
func TestUndersizedSnapshotReason_ShrinkAgainstBaseKey(t *testing.T) {
	t.Parallel()

	orgID, repoID := uuid.New(), uuid.New()
	cacheCols := []string{
		"id", "org_id", "repo_id", "snapshot_key", "base_key", "commit_sha", "blob_path",
		"size_bytes", "worker_node_id", "last_used_at", "created_at",
	}
	priorSize := int64(100 << 20) // 100 MiB already stored for this base key

	cases := []struct {
		name         string
		sizeBytes    int64
		baselineAge  time.Duration
		wantReject   bool
		wantContains string
	}{
		{name: "same size", sizeBytes: priorSize, wantReject: false},
		{name: "modest shrink is normal drift", sizeBytes: priorSize / 2, wantReject: false},
		{name: "just above the threshold", sizeBytes: priorSize/4 + 1, wantReject: false},
		{name: "collapsed to a fraction", sizeBytes: priorSize / 10, wantReject: true, wantContains: "stored for base key"},
		// A rejected snapshot leaves the baseline in place, and restores keep it
		// off the LRU chopping block, so without an age bound one bad baseline
		// could reject its replacement forever.
		{name: "stale baseline stops blocking", sizeBytes: priorSize / 10,
			baselineAge: snapshotShrinkBaselineMaxAge + time.Hour, wantReject: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			createdAt := time.Now().Add(-tc.baselineAge)
			mock.ExpectQuery("SELECT").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(cacheCols).AddRow(
					uuid.New(), orgID, repoID, "older-key", "base", "older-commit",
					"/cache/older", priorSize, "worker-1", time.Now(), createdAt,
				))

			sc := &SnapshotCache{store: db.NewPreviewStore(mock), workerNodeID: "worker-1", logger: zerolog.Nop()}
			reason := sc.undersizedSnapshotReason(context.Background(), "snapshot-key",
				SnapshotMetadata{OrgID: orgID, RepoID: repoID, BaseKey: "base"}, tc.sizeBytes)

			if tc.wantReject {
				require.NotEmpty(t, reason, "a collapse against a fresh baseline should be rejected")
				require.Contains(t, reason, tc.wantContains)
			} else {
				require.Empty(t, reason, "this must still be cached")
			}
		})
	}
}

// The create tests shrink the floor so they can run on a few bytes. This one
// pins the real constant, which needs no fixture at all — just integers.
func TestUndersizedSnapshotReason_AbsoluteFloor(t *testing.T) {
	t.Parallel()

	sc := &SnapshotCache{logger: zerolog.Nop()} // no store: only the floor applies

	require.NotEmpty(t, sc.undersizedSnapshotReason(context.Background(), "snapshot-key", SnapshotMetadata{}, 0),
		"an empty archive must never be stored")
	require.NotEmpty(t, sc.undersizedSnapshotReason(context.Background(), "snapshot-key", SnapshotMetadata{}, minSnapshotBlobBytes-1),
		"one byte under the floor must be rejected")
	require.Empty(t, sc.undersizedSnapshotReason(context.Background(), "snapshot-key", SnapshotMetadata{}, minSnapshotBlobBytes),
		"exactly at the floor is acceptable")

	require.EqualValues(t, 1<<20, minSnapshotBlobBytes,
		"the floor is a deliberate 1 MiB: every snapshot describes a workspace with dependencies installed")
	require.EqualValues(t, minSnapshotBlobBytes, sc.minBlobBytes(),
		"an unset override must fall back to the real constant")
	require.EqualValues(t, 64, (&SnapshotCache{minSnapshotBytesOverride: 64}).minBlobBytes(),
		"a positive override replaces the floor so tests need no large fixtures")
}

// Without a baseline only the absolute floor applies — a first snapshot must
// never be blocked by the shrink rule, and a lookup failure must not either.
func TestUndersizedSnapshotReason_NoBaselineAllowsStore(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	// No expectation registered: the lookup errors, which must read as "no
	// baseline" rather than as a reason to discard a good archive.
	sc := &SnapshotCache{store: db.NewPreviewStore(mock), workerNodeID: "worker-1", logger: zerolog.Nop()}

	require.Empty(t, sc.undersizedSnapshotReason(context.Background(), "snapshot-key",
		SnapshotMetadata{OrgID: uuid.New(), RepoID: uuid.New(), BaseKey: "base"}, 50<<20),
		"a base-key lookup failure must not block caching")

	require.Empty(t, sc.undersizedSnapshotReason(context.Background(), "snapshot-key",
		SnapshotMetadata{OrgID: uuid.New(), RepoID: uuid.New()}, 50<<20),
		"no base key means no baseline to compare against")

	require.NotEmpty(t, sc.undersizedSnapshotReason(context.Background(), "snapshot-key",
		SnapshotMetadata{}, minSnapshotBlobBytes-1),
		"the absolute floor applies with or without a baseline")
}

// The shrink rule keeps a baseline that a rejected snapshot cannot replace, so
// without an escape hatch a workspace that legitimately got smaller — or one
// bad baseline — would reject every replacement for as long as the baseline
// stays fresh. Repetition distinguishes the two: damage does not reproduce at
// the same size, a genuine shrink does.
func TestUndersizedSnapshotReason_RepeatedShrinkIsAccepted(t *testing.T) {
	t.Parallel()

	orgID, repoID := uuid.New(), uuid.New()
	cacheCols := []string{
		"id", "org_id", "repo_id", "snapshot_key", "base_key", "commit_sha", "blob_path",
		"size_bytes", "worker_node_id", "last_used_at", "created_at",
	}
	priorSize := int64(100 << 20)
	shrunk := priorSize / 10

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	// One baseline row per lookup; the rule consults it on every attempt.
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("SELECT").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(cacheCols).AddRow(
				uuid.New(), orgID, repoID, "older-key", "base", "older-commit",
				"/cache/older", priorSize, "worker-1", time.Now(), time.Now(),
			))
	}

	sc := &SnapshotCache{store: db.NewPreviewStore(mock), workerNodeID: "worker-1", logger: zerolog.Nop()}
	metadata := SnapshotMetadata{OrgID: orgID, RepoID: repoID, BaseKey: "base"}

	require.NotEmpty(t, sc.undersizedSnapshotReason(context.Background(), "key-a", metadata, shrunk),
		"the first collapse should be treated as damage and rejected")
	require.Empty(t, sc.undersizedSnapshotReason(context.Background(), "key-a", metadata, shrunk),
		"a collapse that reproduces is the new truth and must be stored")

	// Accepting clears the count, so the rule is armed again rather than
	// permanently disabled for that key.
	require.NotEmpty(t, sc.undersizedSnapshotReason(context.Background(), "key-a", metadata, shrunk),
		"the strike count should reset after an acceptance")
}

// Strikes must be consecutive: a normal-sized snapshot in between means the
// earlier collapse was transient, so the next one starts from zero.
func TestUndersizedSnapshotReason_NormalSnapshotResetsStrikes(t *testing.T) {
	t.Parallel()

	orgID, repoID := uuid.New(), uuid.New()
	cacheCols := []string{
		"id", "org_id", "repo_id", "snapshot_key", "base_key", "commit_sha", "blob_path",
		"size_bytes", "worker_node_id", "last_used_at", "created_at",
	}
	priorSize := int64(100 << 20)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("SELECT").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(cacheCols).AddRow(
				uuid.New(), orgID, repoID, "older-key", "base", "older-commit",
				"/cache/older", priorSize, "worker-1", time.Now(), time.Now(),
			))
	}

	sc := &SnapshotCache{store: db.NewPreviewStore(mock), workerNodeID: "worker-1", logger: zerolog.Nop()}
	metadata := SnapshotMetadata{OrgID: orgID, RepoID: repoID, BaseKey: "base"}

	require.NotEmpty(t, sc.undersizedSnapshotReason(context.Background(), "key-b", metadata, priorSize/10),
		"first collapse rejected")
	require.Empty(t, sc.undersizedSnapshotReason(context.Background(), "key-b", metadata, priorSize),
		"a normal-sized snapshot must be stored and clear the strike")
	require.NotEmpty(t, sc.undersizedSnapshotReason(context.Background(), "key-b", metadata, priorSize/10),
		"a later collapse starts from zero strikes, not from the earlier one")
}

// The absolute floor is not a heuristic and gets no escape hatch: an archive
// that small cannot hold an installed dependency tree however often it repeats.
func TestUndersizedSnapshotReason_AbsoluteFloorHasNoEscapeHatch(t *testing.T) {
	t.Parallel()

	sc := &SnapshotCache{logger: zerolog.Nop()}
	for i := 0; i < snapshotShrinkRejectStrikes+2; i++ {
		require.NotEmptyf(t, sc.undersizedSnapshotReason(context.Background(), "key-c", SnapshotMetadata{}, 0),
			"attempt %d: an empty archive must never become acceptable", i+1)
	}
}

// The strike map must not grow without bound on a long-lived worker.
func TestNoteShrinkStrike_BoundsTrackedKeys(t *testing.T) {
	t.Parallel()

	sc := &SnapshotCache{logger: zerolog.Nop()}
	for i := 0; i < maxTrackedShrinkStrikes+50; i++ {
		sc.noteShrinkStrike(fmt.Sprintf("key-%d", i))
	}
	require.LessOrEqual(t, len(sc.shrinkStrikes), maxTrackedShrinkStrikes,
		"the strike map should reset rather than grow forever")

	// An empty key is not trackable and must not create an entry.
	before := len(sc.shrinkStrikes)
	require.Zero(t, sc.noteShrinkStrike(""), "an empty snapshot key has nothing to track")
	require.Equal(t, before, len(sc.shrinkStrikes), "an empty key must not add an entry")
}
