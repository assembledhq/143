package preview

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/sandbox"
)

func TestClassifyLaunchFailure_InstallFailed(t *testing.T) {
	t.Parallel()

	failure := ClassifyLaunchFailure(fmt.Errorf("%w: npm ci exited with code 1", ErrInstallFailed))

	require.Equal(t, "PREVIEW_INSTALL_FAILED", failure.Code, "install failures should get a dedicated preview start error code")
	require.Contains(t, failure.Message, "preview.install", "install failure message should point users at the install config")
	require.Contains(t, failure.Message, "npm ci exited with code 1", "install failure message should include provider details")
}

func TestShouldReassignPreviewWorker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		deadTargetNode     string
		reservationOwner   string
		claimingWorkerNode string
		expected           bool
	}{
		{
			name:               "reassigns first fallback claim from dead target",
			deadTargetNode:     "worker-a",
			reservationOwner:   "worker-a",
			claimingWorkerNode: "worker-b",
			expected:           true,
		},
		{
			name:               "reassigns second fallback claim when prior claimant died before completion",
			deadTargetNode:     "worker-a",
			reservationOwner:   "worker-b",
			claimingWorkerNode: "worker-c",
			expected:           true,
		},
		{
			name:               "does not reassign when claiming worker already owns reservation",
			deadTargetNode:     "worker-a",
			reservationOwner:   "worker-b",
			claimingWorkerNode: "worker-b",
			expected:           false,
		},
		{
			name:               "does not reassign when claim is not dead-target fallback",
			deadTargetNode:     "",
			reservationOwner:   "worker-a",
			claimingWorkerNode: "worker-b",
			expected:           false,
		},
		{
			name:               "does not reassign without claiming worker identity",
			deadTargetNode:     "worker-a",
			reservationOwner:   "worker-a",
			claimingWorkerNode: "",
			expected:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := shouldReassignPreviewWorker(tt.deadTargetNode, tt.reservationOwner, tt.claimingWorkerNode)
			require.Equal(t, tt.expected, actual, "shouldReassignPreviewWorker should match the expected fallback ownership behavior")
		})
	}
}

type startRunnerFileReader struct {
	content string
	err     error
}

func (r startRunnerFileReader) ListDir(context.Context, string, string, string) ([]sandbox.FileEntry, error) {
	panic("not used")
}

func (r startRunnerFileReader) ReadFile(context.Context, string, string, string) (string, bool, error) {
	return r.content, false, r.err
}

func (r startRunnerFileReader) ReadFileContext(context.Context, string, string, string, int, int, int) (sandbox.FileContextResult, error) {
	panic("not used")
}

func TestStartRunnerReadWorkspacePreviewConfig_ParseError(t *testing.T) {
	t.Parallel()

	runner := &StartRunner{
		fileReader: startRunnerFileReader{content: `{"preview":{"version":{}}}`},
		logger:     zerolog.Nop(),
	}

	cfg, err := runner.readWorkspacePreviewConfig(
		context.Background(),
		&agent.Sandbox{ID: "container-1", WorkDir: "/home/sandbox/repo"},
		uuid.New(),
		"",
	)

	require.Error(t, err, "invalid committed preview config should surface instead of being treated as missing config")
	require.ErrorIs(t, err, ErrInvalidConfig, "invalid committed preview config should use the shared invalid-config sentinel")
	require.Contains(t, err.Error(), repoconfig.ConfigPath, "invalid config error should name the repo config path")
	require.Nil(t, cfg, "invalid committed preview config should not return a fallback config")
}

type fakePreviewStartupCache struct {
	findKey       string
	findRepoID    uuid.UUID
	findOrgID     uuid.UUID
	restoreCalled bool
	createKey     string
	createMeta    SnapshotMetadata
	hit           *CacheHit
}

func (f *fakePreviewStartupCache) FindSnapshot(_ context.Context, orgID, repoID uuid.UUID, snapshotKey string) (*CacheHit, error) {
	f.findOrgID = orgID
	f.findRepoID = repoID
	f.findKey = snapshotKey
	return f.hit, nil
}

func (f *fakePreviewStartupCache) RestoreSnapshot(context.Context, *agent.Sandbox, *CacheHit) error {
	f.restoreCalled = true
	return nil
}

func (f *fakePreviewStartupCache) CreateSnapshot(_ context.Context, _ *agent.Sandbox, snapshotKey string, metadata SnapshotMetadata) error {
	f.createKey = snapshotKey
	f.createMeta = metadata
	return nil
}

type fakeStartRunnerSandboxProvider struct {
	files map[string][]byte
}

func (f fakeStartRunnerSandboxProvider) Name() string { return "fake" }
func (f fakeStartRunnerSandboxProvider) Create(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
	panic("not used")
}
func (f fakeStartRunnerSandboxProvider) CloneRepo(context.Context, *agent.Sandbox, string, string, string) error {
	panic("not used")
}
func (f fakeStartRunnerSandboxProvider) Exec(context.Context, *agent.Sandbox, string, io.Writer, io.Writer) (int, error) {
	panic("not used")
}
func (f fakeStartRunnerSandboxProvider) ReadFile(_ context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
	if body, ok := f.files[path]; ok {
		return body, nil
	}
	return f.files[sb.WorkDir+"/"+path], nil
}
func (f fakeStartRunnerSandboxProvider) WriteFile(context.Context, *agent.Sandbox, string, []byte) error {
	panic("not used")
}
func (f fakeStartRunnerSandboxProvider) Destroy(context.Context, *agent.Sandbox) error {
	panic("not used")
}
func (f fakeStartRunnerSandboxProvider) IsAlive(context.Context, *agent.Sandbox) (bool, error) {
	panic("not used")
}
func (f fakeStartRunnerSandboxProvider) ConnectionInfo(context.Context, *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	panic("not used")
}
func (f fakeStartRunnerSandboxProvider) Snapshot(context.Context, *agent.Sandbox) (io.ReadCloser, error) {
	panic("not used")
}
func (f fakeStartRunnerSandboxProvider) Restore(context.Context, *agent.Sandbox, io.Reader) error {
	panic("not used")
}
func (f fakeStartRunnerSandboxProvider) ExecStream(context.Context, *agent.Sandbox, string, func([]byte), io.Writer) (int, error) {
	panic("not used")
}

func TestStartRunnerMaybeRestoreBranchPreviewStartupCache_RestoresMatchingSnapshot(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	cfg := &models.PreviewConfig{
		Version: "3",
		Name:    "web",
		Primary: "web",
		Install: &models.PreviewInstallConfig{
			Command:   []string{"npm", "ci"},
			Lockfiles: []string{"package-lock.json"},
		},
		Services: map[string]models.ServiceConfig{
			"web": {Command: []string{"npm", "run", "dev"}, Port: 3000},
		},
	}
	sb := &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}
	cache := &fakePreviewStartupCache{hit: &CacheHit{}}
	runner := &StartRunner{
		sandboxProvider: fakeStartRunnerSandboxProvider{
			files: map[string][]byte{"/workspace/repo/package-lock.json": []byte(`{"lockfileVersion":3}`)},
		},
		snapshotCache: cache,
		logger:        zerolog.Nop(),
	}

	key := runner.maybeRestoreBranchPreviewStartupCache(context.Background(), orgID, repoID, "abc1234", sb, cfg)

	expectedLockInput := []byte("package-lock.json\x00{\"lockfileVersion\":3}\x00")
	expectedKey := ComputeSnapshotKey(expectedLockInput, "abc1234", computeConfigDigest(cfg))
	require.Equal(t, expectedKey, key, "branch preview cache key should include committed lockfile contents and config digest")
	require.Equal(t, expectedKey, cache.findKey, "branch preview startup should look up the computed cache key")
	require.Equal(t, orgID, cache.findOrgID, "cache lookup should stay org-scoped")
	require.Equal(t, repoID, cache.findRepoID, "cache lookup should stay repo-scoped")
	require.True(t, cache.restoreCalled, "branch preview startup should restore a matching cached workspace before launching")
}

func TestStartRunnerCreateBranchPreviewStartupCache_RecordsSuccessfulLaunch(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	cache := &fakePreviewStartupCache{}
	runner := &StartRunner{snapshotCache: cache, logger: zerolog.Nop()}
	sb := &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}

	runner.createBranchPreviewStartupCache(context.Background(), orgID, repoID, "cache-key", sb, nil)

	require.Equal(t, "cache-key", cache.createKey, "successful branch preview launch should write the startup cache snapshot")
	require.Equal(t, SnapshotMetadata{OrgID: orgID, RepoID: repoID}, cache.createMeta, "startup cache metadata should preserve org and repo scope")
}

func TestStartRunnerBranchPreviewStartupCache_SkipsFileDeliveredSecrets(t *testing.T) {
	t.Parallel()

	// The current launch would overwrite restored secret files, but cache
	// creation happens after launch and would otherwise persist plaintext
	// generated secret files in worker-local cache blobs.
	orgID := uuid.New()
	repoID := uuid.New()
	cfg := &models.PreviewConfig{
		Version: "3",
		Name:    "web",
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {Command: []string{"npm", "run", "dev"}, Port: 3000},
		},
		Secrets: []models.PreviewSecretBundleRef{
			{Bundle: "app-secrets", Services: []string{"web"}, Files: []string{".env.local"}},
		},
	}
	cache := &fakePreviewStartupCache{hit: &CacheHit{}}
	runner := &StartRunner{
		sandboxProvider: fakeStartRunnerSandboxProvider{},
		snapshotCache:   cache,
		logger:          zerolog.Nop(),
	}
	sb := &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}

	key := runner.maybeRestoreBranchPreviewStartupCache(context.Background(), orgID, repoID, "abc1234", sb, cfg)
	runner.createBranchPreviewStartupCache(context.Background(), orgID, repoID, "cache-key", sb, cfg)

	require.Empty(t, key, "branch preview startup cache should not restore snapshots for configs with generated secret files")
	require.Empty(t, cache.findKey, "secret-file configs should not query startup cache entries")
	require.Empty(t, cache.createKey, "secret-file configs should not write startup cache snapshots")
	require.False(t, cache.restoreCalled, "secret-file configs should not restore cached workspace files")
}
