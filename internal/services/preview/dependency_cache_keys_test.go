package preview

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type dependencyKeyExecutor struct {
	files map[string][]byte
}

func (e dependencyKeyExecutor) Exec(context.Context, *agent.Sandbox, string, io.Writer, io.Writer) (int, error) {
	return 0, nil
}

func (e dependencyKeyExecutor) ReadFile(_ context.Context, _ *agent.Sandbox, path string) ([]byte, error) {
	return append([]byte(nil), e.files[path]...), nil
}

func (e dependencyKeyExecutor) WriteFile(context.Context, *agent.Sandbox, string, []byte) error {
	return nil
}

func TestComputePreviewDependencyCacheKey(t *testing.T) {
	t.Parallel()

	exec := dependencyKeyExecutor{files: map[string][]byte{
		"package-lock.json": []byte(`{"lockfileVersion":3}`),
	}}
	sb := &agent.Sandbox{Provider: "docker", Metadata: map[string]string{"image": "143-sandbox@sha256:abc"}}
	install := &models.PreviewInstallConfig{
		Command:        []string{"npm", "ci"},
		Cwd:            ".",
		Lockfiles:      []string{"package-lock.json"},
		CleanPaths:     []string{"node_modules"},
		VerifyPaths:    []string{"node_modules/.bin/next"},
		TimeoutSeconds: 420,
	}
	base, lockfiles, err := ComputePreviewDependencyCacheKey(context.Background(), exec, sb, install, []string{"node_modules", ".next/cache"})
	require.NoError(t, err, "dependency cache key should compute")
	require.Equal(t, []PreviewInstallLockfileKey{{Path: "package-lock.json", SHA256: "2086223f909ecaf4b063cfc7e8187ab73e8e8a24ec7029d54e0403dee55a3265"}}, lockfiles, "dependency cache key should return lockfile hashes for metadata")

	reordered, _, err := ComputePreviewDependencyCacheKey(context.Background(), exec, sb, install, []string{".next/cache", "node_modules"})
	require.NoError(t, err, "dependency cache key should compute for reordered paths")
	require.Equal(t, base, reordered, "dependency cache key should be deterministic across effective path order")

	verifyChanged := *install
	verifyChanged.VerifyPaths = []string{"different"}
	verifyKey, _, err := ComputePreviewDependencyCacheKey(context.Background(), exec, sb, &verifyChanged, []string{"node_modules", ".next/cache"})
	require.NoError(t, err, "dependency cache key should compute when verify paths change")
	require.Equal(t, base, verifyKey, "verify paths should not affect dependency artifact identity")

	commandChanged := *install
	commandChanged.Command = []string{"npm", "install"}
	commandKey, _, err := ComputePreviewDependencyCacheKey(context.Background(), exec, sb, &commandChanged, []string{"node_modules", ".next/cache"})
	require.NoError(t, err, "dependency cache key should compute when command changes")
	require.NotEqual(t, base, commandKey, "install command should affect dependency artifact identity")

	imageChanged := &agent.Sandbox{Provider: "docker", Metadata: map[string]string{"image": "143-sandbox@sha256:def"}}
	imageKey, _, err := ComputePreviewDependencyCacheKey(context.Background(), exec, imageChanged, install, []string{"node_modules", ".next/cache"})
	require.NoError(t, err, "dependency cache key should compute when sandbox image changes")
	require.NotEqual(t, base, imageKey, "sandbox image should affect dependency artifact identity")
}

func TestComputePreviewPackageManagerCacheKey(t *testing.T) {
	t.Parallel()

	exec := dependencyKeyExecutor{files: map[string][]byte{
		"pnpm-lock.yaml": []byte("lockfileVersion: '9.0'\n"),
	}}
	sb := &agent.Sandbox{Provider: "docker", Metadata: map[string]string{"image": "143-sandbox@sha256:abc"}}
	install := &models.PreviewInstallConfig{
		Command:   []string{"pnpm", "install", "--frozen-lockfile"},
		Cwd:       "frontend",
		Lockfiles: []string{"pnpm-lock.yaml"},
	}

	base, lockfiles, err := ComputePreviewPackageManagerCacheKey(context.Background(), exec, sb, install, []string{".local/share/pnpm/store"}, []string{"pnpm"})
	require.NoError(t, err, "package-manager cache key should compute")
	require.Equal(t, []PreviewInstallLockfileKey{{Path: "pnpm-lock.yaml", SHA256: "f0bcde463fa201480015b9caa7db2017d3c1b6ca9c7e133df955038c54333d48"}}, lockfiles, "package-manager cache key should return lockfile hashes for metadata")

	reordered, _, err := ComputePreviewPackageManagerCacheKey(context.Background(), exec, sb, install, []string{".local/share/pnpm/store"}, []string{"pnpm"})
	require.NoError(t, err, "package-manager cache key should compute for same inputs")
	require.Equal(t, base, reordered, "package-manager cache key should be stable for same inputs")

	pathsChanged, _, err := ComputePreviewPackageManagerCacheKey(context.Background(), exec, sb, install, []string{".cache/pnpm", ".local/share/pnpm/store"}, []string{"pnpm"})
	require.NoError(t, err, "package-manager cache key should compute when paths change")
	require.NotEqual(t, base, pathsChanged, "home cache paths should affect package-manager cache identity")

	managersChanged, _, err := ComputePreviewPackageManagerCacheKey(context.Background(), exec, sb, install, []string{".local/share/pnpm/store"}, []string{"npm", "pnpm"})
	require.NoError(t, err, "package-manager cache key should compute when manager set changes")
	require.NotEqual(t, base, managersChanged, "package-manager set should affect package-manager cache identity")

	commandChanged := *install
	commandChanged.Command = []string{"pnpm", "install"}
	commandKey, _, err := ComputePreviewPackageManagerCacheKey(context.Background(), exec, sb, &commandChanged, []string{".local/share/pnpm/store"}, []string{"pnpm"})
	require.NoError(t, err, "package-manager cache key should compute when command changes")
	require.NotEqual(t, base, commandKey, "install command should affect package-manager cache identity")
}

func TestComputePreviewDependencyCachePlacementKey(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	install := &models.PreviewInstallConfig{
		Command:   []string{"npm", "ci"},
		Cwd:       "frontend",
		Lockfiles: []string{"frontend/package-lock.json"},
	}
	left, err := ComputePreviewDependencyCachePlacementKey(orgID, repoID, "web", "digest", install, []string{"frontend/node_modules", "frontend/.next/cache"})
	require.NoError(t, err, "placement key should compute")
	right, err := ComputePreviewDependencyCachePlacementKey(orgID, repoID, "web", "digest", install, []string{"frontend/.next/cache", "frontend/node_modules"})
	require.NoError(t, err, "placement key should compute with reordered paths")
	require.Equal(t, left, right, "placement key should not depend on effective path order")

	changedDigest, err := ComputePreviewDependencyCachePlacementKey(orgID, repoID, "web", "other-digest", install, []string{"frontend/node_modules", "frontend/.next/cache"})
	require.NoError(t, err, "placement key should compute when digest changes")
	require.NotEqual(t, left, changedDigest, "config digest should affect scheduler locality")
	require.False(t, bytes.Equal([]byte(left), []byte("")), "placement key should not be empty")
}

func TestComputePreviewDependencyCacheRepoPlacementKey(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	left, err := ComputePreviewDependencyCacheRepoPlacementKey(orgID, repoID)
	require.NoError(t, err, "repo placement key should compute without preview config")
	right, err := ComputePreviewDependencyCacheRepoPlacementKey(orgID, repoID)
	require.NoError(t, err, "repo placement key should be repeatable")
	require.Equal(t, left, right, "repo placement key should be stable for the same repo")

	otherRepo, err := ComputePreviewDependencyCacheRepoPlacementKey(orgID, uuid.New())
	require.NoError(t, err, "repo placement key should compute for another repo")
	require.NotEqual(t, left, otherRepo, "repo placement key should shard different repos independently")
}

func TestDependencyCachePathTargetsPreviewInstallMarkers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "exact marker dir", path: ".143/cache/preview-install", want: true},
		{name: "marker child", path: ".143/cache/preview-install/cache.done", want: true},
		{name: "marker parent", path: ".143/cache", want: true},
		{name: "marker child glob with install prefix", path: ".143/cache/preview-install*/*", want: true},
		{name: "marker child glob through wildcard segment", path: ".143/cache/*/*.done", want: true},
		{name: "neighboring install-like path", path: ".143/cache/preview-install-extra/*", want: false},
		{name: "ordinary dependency path", path: "node_modules", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, dependencyCachePathTargetsPreviewInstallMarkers(tt.path), "marker path detection should match expected safety classification")
		})
	}
}

func TestBuildDependencyCacheTarValidationCommand(t *testing.T) {
	t.Parallel()

	cmd := buildDependencyCacheExtractCommand("/workspace/repo", "/tmp/cache.tar.gz", []string{"node_modules", ".next/cache"})
	require.Contains(t, cmd, `^node_modules(/|$)`, "extract validation should allow node_modules entries")
	require.Contains(t, cmd, `^\.next\/cache(/|$)`, "extract validation should allow nested cache entries")
	require.Contains(t, cmd, "bad=1", "extract validation should reject entries outside effective cache paths")
	require.Contains(t, cmd, "tar xzf", "extract command should still extract after validation")
}
