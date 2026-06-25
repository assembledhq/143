package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// localShellExecutor runs cache commands through the host's bash so a test can
// exercise the real chmod/rm/tar shell behavior (e.g. removing a read-only Go
// module cache) instead of a string-matching fake.
type localShellExecutor struct{}

func runLocalShell(c *exec.Cmd) (int, error) {
	err := c.Run()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}

func (localShellExecutor) Exec(ctx context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	c.Stdout, c.Stderr = stdout, stderr
	return runLocalShell(c)
}

func (localShellExecutor) ExecWithStdin(ctx context.Context, _ *agent.Sandbox, cmd string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	return runLocalShell(c)
}

func (localShellExecutor) ReadFile(_ context.Context, _ *agent.Sandbox, path string) ([]byte, error) {
	return os.ReadFile(path) // #nosec G304 -- test-only, path is under a temp dir.
}

func (localShellExecutor) WriteFile(_ context.Context, _ *agent.Sandbox, path string, data []byte) error {
	return os.WriteFile(path, data, 0o600) // #nosec G306 -- test-only.
}

func (localShellExecutor) WriteFileFromReader(_ context.Context, _ *agent.Sandbox, path string, r io.Reader, _ int64) error {
	f, err := os.Create(path) // #nosec G304 -- test-only, path is under a temp dir.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, r)
	return err
}

// TestRestorePathCache_RemovesReadOnlyGoModuleCache behaviorally validates the
// home Go-cache restore fix: against a real read-only Go module cache (files
// 0444 inside 0555 directories), the restore's `chmod -R u+rwX; rm -rf` cleanup
// actually removes the tree. A plain `chmod -R u+w` left directories
// non-traversable and the rm exited 1, which failed the home-rooted Go cache
// restore in production and forced a cold rebuild every launch.
func TestRestorePathCache_RemovesReadOnlyGoModuleCache(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required for this test")
	}
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar is required for this test")
	}

	homeDir := t.TempDir()
	// Make t.TempDir cleanup able to remove the read-only tree even if the
	// restore-under-test leaves it in place on failure.
	t.Cleanup(func() { _ = exec.Command("chmod", "-R", "u+rwX", homeDir).Run() })

	// Build a Go-style read-only module cache: a 0444 file inside 0555 dirs.
	modDir := filepath.Join(homeDir, "go", "pkg", "mod", "cache", "download", "example.com", "@v")
	require.NoError(t, os.MkdirAll(modDir, 0o755))
	staleFile := filepath.Join(modDir, "v1.0.0.info")
	require.NoError(t, os.WriteFile(staleFile, []byte("stale"), 0o644)) // #nosec G306 -- test fixture.
	require.NoError(t, os.Chmod(staleFile, 0o444))
	// Lock the directories read-only from the deepest up to go/pkg/mod.
	roDir := modDir
	modRoot := filepath.Join(homeDir, "go", "pkg", "mod")
	for {
		require.NoError(t, os.Chmod(roDir, 0o555))
		if roDir == modRoot {
			break
		}
		roDir = filepath.Dir(roDir)
	}

	blob := makeDependencyCacheTarGz(t, map[string]string{
		"go/pkg/mod/cache/download/example.com/@v/v2.0.0.info": "fresh",
	})
	blobStore := newMemorySnapshotStore()
	const blobKey = "deps/build/home-blob.tar.gz"
	require.NoError(t, blobStore.Save(context.Background(), blobKey, bytes.NewReader(blob)))

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	cache, err := NewDependencyCache(DependencyCacheConfig{
		Store:      db.NewPreviewStore(mock),
		Executor:   localShellExecutor{},
		BlobStore:  blobStore,
		Logger:     zerolog.Nop(),
		Prefix:     "deps",
		StagingDir: t.TempDir(),
	})
	require.NoError(t, err)

	meta, err := json.Marshal(DependencyCacheMetadata{EffectivePaths: []string{"go/pkg/mod"}})
	require.NoError(t, err)
	hit := &DependencyCacheHit{
		Entry: models.PreviewDependencyCache{
			CacheKind: models.PreviewCacheKindBuildArtifact,
			SizeBytes: int64(len(blob)),
			Metadata:  meta,
		},
		BlobKey: blobKey,
	}

	err = cache.RestorePathCache(context.Background(), &agent.Sandbox{HomeDir: homeDir}, hit, models.PreviewCacheRootHomeDir)
	require.NoError(t, err, "restore must remove a read-only Go module cache (chmod -R u+rwX, not u+w) before extracting")

	_, statErr := os.Stat(staleFile)
	require.True(t, os.IsNotExist(statErr), "the stale read-only module cache file should have been removed")
	require.FileExists(t, filepath.Join(homeDir, "go", "pkg", "mod", "cache", "download", "example.com", "@v", "v2.0.0.info"),
		"the restored blob content should be extracted into the home root")
}
