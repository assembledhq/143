package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
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
			name:               "reassigns when retry target moves to another worker",
			deadTargetNode:     "",
			reservationOwner:   "worker-a",
			claimingWorkerNode: "worker-b",
			expected:           true,
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

type startRunnerOrgStore struct {
	settings json.RawMessage
}

func (s startRunnerOrgStore) GetByID(_ context.Context, orgID uuid.UUID) (models.Organization, error) {
	return models.Organization{ID: orgID, Settings: s.settings}, nil
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

func TestStartRunnerApplyBranchPreviewSandboxNetworkUsesStaticEgress(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runner := &StartRunner{
		orgs: startRunnerOrgStore{settings: json.RawMessage(`{"sandbox_network":{"static_egress_enabled":true}}`)},
		staticEgress: agent.StaticEgressRuntimeConfig{
			Enabled:        true,
			Capable:        true,
			NetworkName:    "143-sandbox-static-egress",
			ResolvConfPath: "/etc/143/sandbox-static-egress-resolv.conf",
			PublicIP:       "203.0.113.10",
		},
	}
	cfg := agent.DefaultSandboxConfig()

	err := runner.applyBranchPreviewSandboxNetwork(context.Background(), orgID, &cfg)

	require.NoError(t, err, "static-egress-capable runners should accept branch previews for opted-in orgs")
	require.Equal(t, "143-sandbox-static-egress", cfg.NetworkName, "branch preview sandboxes should use the static egress bridge for opted-in orgs")
	require.Equal(t, "/etc/143/sandbox-static-egress-resolv.conf", cfg.ResolvConfPath, "branch preview sandboxes should use the static egress resolver")
	require.Equal(t, agent.SandboxEgressModeStatic, cfg.EgressMode, "branch preview metadata should record static egress mode")
}

func TestNewStartRunner_SnapshotCacheDoesNotUseTypedNilInterface(t *testing.T) {
	t.Parallel()

	withoutCache := NewStartRunner(StartRunnerConfig{Logger: zerolog.Nop()})
	require.True(t, withoutCache.snapshotCache == nil, "omitted snapshot cache should leave a nil interface")

	cache := &SnapshotCache{}
	manager := NewManager(ManagerConfig{SnapshotCache: cache, Logger: zerolog.Nop()})
	withManagerCache := NewStartRunner(StartRunnerConfig{Manager: manager, Logger: zerolog.Nop()})

	got, ok := withManagerCache.snapshotCache.(*SnapshotCache)
	require.True(t, ok, "runner should receive the manager snapshot cache when config cache is omitted")
	require.Same(t, cache, got, "runner should use the manager snapshot cache instead of a typed nil interface")
}

func TestPreviewCachePrewarmScopeKey_SessionAllowsDeferredConfigDigest(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	payload := PreviewCachePrewarmJobPayload{
		Source:            PreviewCachePrewarmSourceSession,
		SessionID:         sessionID,
		WorkspaceRevision: 7,
	}

	require.Equal(t, "preview_cache_prewarm:session:"+sessionID.String()+":7", PreviewCachePrewarmScopeKey(payload), "session prewarm scope should be computable before config digest is known")

	payload.ConfigDigest = "digest"
	require.Equal(t, "preview_cache_prewarm:session:"+sessionID.String()+":7:digest", PreviewCachePrewarmScopeKey(payload), "session prewarm scope should include digest when enqueue already knows it")
}

type prewarmLiveSandboxProvider struct {
	createdCfg agent.SandboxConfig
	created    *agent.Sandbox
	sourceID   string
	restored   bool
	destroyed  bool
}

func (p *prewarmLiveSandboxProvider) Name() string { return "fake" }
func (p *prewarmLiveSandboxProvider) Create(_ context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	p.createdCfg = cfg
	p.created = &agent.Sandbox{ID: "prewarm-sandbox", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir, SessionID: cfg.SessionID, OrgID: cfg.OrgID}
	return p.created, nil
}
func (p *prewarmLiveSandboxProvider) CloneRepo(context.Context, *agent.Sandbox, string, string, string) error {
	panic("not used")
}
func (p *prewarmLiveSandboxProvider) Exec(context.Context, *agent.Sandbox, string, io.Writer, io.Writer) (int, error) {
	panic("not used")
}
func (p *prewarmLiveSandboxProvider) ReadFile(context.Context, *agent.Sandbox, string) ([]byte, error) {
	panic("not used")
}
func (p *prewarmLiveSandboxProvider) WriteFile(context.Context, *agent.Sandbox, string, []byte) error {
	panic("not used")
}
func (p *prewarmLiveSandboxProvider) Destroy(_ context.Context, sb *agent.Sandbox) error {
	p.destroyed = sb != nil && sb.ID == "prewarm-sandbox"
	return nil
}
func (p *prewarmLiveSandboxProvider) IsAlive(_ context.Context, sb *agent.Sandbox) (bool, error) {
	p.sourceID = sb.ID
	return true, nil
}
func (p *prewarmLiveSandboxProvider) ConnectionInfo(context.Context, *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	panic("not used")
}
func (p *prewarmLiveSandboxProvider) Snapshot(_ context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
	p.sourceID = sb.ID
	return io.NopCloser(strings.NewReader("snapshot")), nil
}
func (p *prewarmLiveSandboxProvider) Restore(_ context.Context, sb *agent.Sandbox, reader io.Reader) error {
	p.restored = sb != nil && sb.ID == "prewarm-sandbox"
	_, err := io.ReadAll(reader)
	return err
}
func (p *prewarmLiveSandboxProvider) ExecStream(context.Context, *agent.Sandbox, string, func([]byte), io.Writer) (int, error) {
	panic("not used")
}

func TestStartRunnerPrepareLiveSessionPreviewCachePrewarmSandbox_ClonesLiveContainer(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	containerID := "live-container"
	workerNodeID := "worker-a"
	provider := &prewarmLiveSandboxProvider{}
	runner := &StartRunner{
		sandboxProvider: provider,
		nodeID:          workerNodeID,
		logger:          zerolog.Nop(),
	}

	sb, cleanup, ok, err := runner.prepareLiveSessionPreviewCachePrewarmSandbox(context.Background(), PreviewCachePrewarmJobPayload{
		OrgID:        orgID,
		RepositoryID: repoID,
		SessionID:    sessionID,
	}, &models.Session{
		ID:           sessionID,
		OrgID:        orgID,
		RepositoryID: &repoID,
		ContainerID:  &containerID,
		WorkerNodeID: &workerNodeID,
		SandboxState: models.SandboxStateRunning,
	})

	require.NoError(t, err, "live session prewarm helper should clone a live local container")
	require.True(t, ok, "live session prewarm helper should report that live clone was used")
	require.NotNil(t, sb, "live session prewarm helper should return an ephemeral sandbox")
	require.Equal(t, "preview_cache_prewarm", provider.createdCfg.Purpose, "prewarm clone should use the prewarm sandbox purpose")
	require.Equal(t, containerID, provider.sourceID, "prewarm clone should snapshot the live session container")
	require.True(t, provider.restored, "prewarm clone should restore live snapshot bytes into the ephemeral sandbox")
	require.NotNil(t, cleanup, "live session prewarm helper should return cleanup")
	cleanup()
	require.True(t, provider.destroyed, "cleanup should destroy the ephemeral prewarm sandbox")
}

type acquireSandboxProvider struct {
	aliveByID map[string]bool
	probedIDs []string
}

func (p *acquireSandboxProvider) Name() string { return "fake" }
func (p *acquireSandboxProvider) Create(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
	panic("not used")
}
func (p *acquireSandboxProvider) CloneRepo(context.Context, *agent.Sandbox, string, string, string) error {
	panic("not used")
}
func (p *acquireSandboxProvider) Exec(context.Context, *agent.Sandbox, string, io.Writer, io.Writer) (int, error) {
	panic("not used")
}
func (p *acquireSandboxProvider) ReadFile(context.Context, *agent.Sandbox, string) ([]byte, error) {
	panic("not used")
}
func (p *acquireSandboxProvider) WriteFile(context.Context, *agent.Sandbox, string, []byte) error {
	panic("not used")
}
func (p *acquireSandboxProvider) Destroy(context.Context, *agent.Sandbox) error {
	panic("not used")
}
func (p *acquireSandboxProvider) IsAlive(_ context.Context, sb *agent.Sandbox) (bool, error) {
	p.probedIDs = append(p.probedIDs, sb.ID)
	return p.aliveByID[sb.ID], nil
}
func (p *acquireSandboxProvider) ConnectionInfo(context.Context, *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	panic("not used")
}
func (p *acquireSandboxProvider) Snapshot(context.Context, *agent.Sandbox) (io.ReadCloser, error) {
	panic("not used")
}
func (p *acquireSandboxProvider) Restore(context.Context, *agent.Sandbox, io.Reader) error {
	panic("not used")
}
func (p *acquireSandboxProvider) ExecStream(context.Context, *agent.Sandbox, string, func([]byte), io.Writer) (int, error) {
	panic("not used")
}

func TestStartRunnerAcquireSandbox_ClearsStaleContainerIDBeforeHydrateRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	staleContainerID := "stale-container"
	snapshotKey := "snapshots/session.tar.zst"
	provider := &acquireSandboxProvider{aliveByID: map[string]bool{staleContainerID: false}}
	runner := &StartRunner{
		sessions:        db.NewSessionStore(mock),
		sandboxProvider: provider,
		snapshots:       acquireSandboxSnapshotStore{},
		logger:          zerolog.Nop(),
	}
	session := &models.Session{
		ID:           sessionID,
		OrgID:        orgID,
		ContainerID:  &staleContainerID,
		SandboxState: models.SandboxStateRunning,
		SnapshotKey:  &snapshotKey,
	}

	mock.ExpectQuery(`SELECT COALESCE\(container_id, ''\), COALESCE\(worker_node_id, ''\)\s+FROM sessions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"container_id", "worker_node_id"}).AddRow(staleContainerID, ""))
	mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL,\s+worker_node_id = NULL,\s+turn_holding_container = FALSE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	result := runner.acquireSandbox(context.Background(), orgID, session, nil)

	require.ErrorIs(t, result.Err, agent.ErrStaleSandboxIDCleared, "stale container cleanup should ask the caller to retry")
	require.Equal(t, "STALE_SANDBOX_CLEARED", result.ErrCode, "stale cleanup should return a stable preview error code")
	require.Nil(t, result.Sandbox, "stale cleanup should not hydrate in the same attempt")
	require.Equal(t, []string{staleContainerID, staleContainerID}, provider.probedIDs, "runner should probe the recorded container before clearing it")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestStartRunnerAcquireSandbox_DoesNotProbeContainerOwnedByDifferentWorker(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	containerID := "container-on-other-worker"
	workerNodeID := "worker-a"
	provider := &acquireSandboxProvider{aliveByID: map[string]bool{containerID: false}}
	runner := &StartRunner{
		sandboxProvider: provider,
		nodeID:          "worker-b",
		logger:          zerolog.Nop(),
	}
	session := &models.Session{
		ID:           sessionID,
		OrgID:        orgID,
		ContainerID:  &containerID,
		WorkerNodeID: &workerNodeID,
		SandboxState: models.SandboxStateRunning,
	}

	result := runner.acquireSandbox(context.Background(), orgID, session, nil)

	require.ErrorIs(t, result.Err, agent.ErrSandboxOnDifferentNode, "live containers owned by another worker should be retried on that worker")
	require.Equal(t, "SANDBOX_WRONG_NODE", result.ErrCode, "wrong-node live containers should use a stable preview error code")
	require.Empty(t, provider.probedIDs, "runner must not probe a container on the local Docker daemon when another worker owns it")
	require.Nil(t, result.Sandbox, "wrong-node acquisition should not return a sandbox")
}

func TestStartRunnerAcquireSandbox_WaitsForWorkerOwnershipBeforeProbe(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	containerID := "container-with-pending-owner"
	provider := &acquireSandboxProvider{aliveByID: map[string]bool{containerID: false}}
	runner := &StartRunner{
		sandboxProvider: provider,
		nodeID:          "worker-b",
		logger:          zerolog.Nop(),
	}
	session := &models.Session{
		ID:           sessionID,
		OrgID:        orgID,
		ContainerID:  &containerID,
		SandboxState: models.SandboxStateRunning,
	}

	result := runner.acquireSandbox(context.Background(), orgID, session, nil)

	require.ErrorIs(t, result.Err, ErrSandboxBusy, "live containers without worker ownership should be retried until ownership is visible")
	require.Equal(t, "SANDBOX_BUSY", result.ErrCode, "pending worker ownership should keep the sandbox-busy retry contract")
	require.Empty(t, provider.probedIDs, "runner must not probe a live container before worker ownership is known")
	require.Nil(t, result.Sandbox, "pending worker ownership should not return a sandbox")
}

type acquireSandboxSnapshotStore struct{}

func (acquireSandboxSnapshotStore) Save(context.Context, string, io.Reader) error {
	panic("not used")
}
func (acquireSandboxSnapshotStore) Load(context.Context, string, io.Writer) error {
	panic("not used")
}
func (acquireSandboxSnapshotStore) Delete(context.Context, string) error {
	panic("not used")
}

type fakePreviewStartupCache struct {
	findKey       string
	findRepoID    uuid.UUID
	findOrgID     uuid.UUID
	restoreCalled bool
	createKey     string
	createMeta    SnapshotMetadata
	hit           *CacheHit

	baseFindKey   string
	baseHit       *CacheHit
	partialCalled bool
	partialDiff   []byte
	partialErr    error
}

func (f *fakePreviewStartupCache) FindSnapshot(_ context.Context, orgID, repoID uuid.UUID, snapshotKey string) (*CacheHit, error) {
	f.findOrgID = orgID
	f.findRepoID = repoID
	f.findKey = snapshotKey
	return f.hit, nil
}

func (f *fakePreviewStartupCache) FindBaseSnapshot(_ context.Context, _, _ uuid.UUID, baseKey, _ string) (*CacheHit, error) {
	f.baseFindKey = baseKey
	return f.baseHit, nil
}

func (f *fakePreviewStartupCache) RestoreSnapshot(context.Context, *agent.Sandbox, *CacheHit) error {
	f.restoreCalled = true
	return nil
}

func (f *fakePreviewStartupCache) ApplyPartialInvalidation(_ context.Context, _ *agent.Sandbox, _ *CacheHit, gitDiff []byte) error {
	f.partialCalled = true
	f.partialDiff = append([]byte(nil), gitDiff...)
	return f.partialErr
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

	keys, err := runner.maybeRestoreBranchPreviewStartupCache(context.Background(), orgID, repoID, "abc1234", sb, cfg)
	require.NoError(t, err, "restoring an exact snapshot hit should not surface an error")

	expectedLockInput := []byte("package-lock.json\x00{\"lockfileVersion\":3}\x00")
	expectedKey := ComputeSnapshotKey(expectedLockInput, "abc1234", computeConfigDigest(cfg))
	expectedBaseKey := ComputeSnapshotBaseKey(expectedLockInput, computeConfigDigest(cfg))
	require.Equal(t, expectedKey, keys.SnapshotKey, "branch preview cache key should include committed lockfile contents and config digest")
	require.Equal(t, expectedBaseKey, keys.BaseKey, "base key should hash lockfiles and config digest without the commit")
	require.Equal(t, "abc1234", keys.CommitSHA, "cache keys should record the pinned commit")
	require.Equal(t, expectedKey, cache.findKey, "branch preview startup should look up the computed cache key")
	require.Equal(t, orgID, cache.findOrgID, "cache lookup should stay org-scoped")
	require.Equal(t, repoID, cache.findRepoID, "cache lookup should stay repo-scoped")
	require.True(t, cache.restoreCalled, "branch preview startup should restore a matching cached workspace before launching")
	require.Empty(t, cache.baseFindKey, "an exact hit should not consult base snapshots")
}

func TestStartRunnerMaybeRestoreBranchPreviewStartupCache_PartialInvalidationOnExactMiss(t *testing.T) {
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
	diff := []byte("diff --git a/main.go b/main.go\n")
	cache := &fakePreviewStartupCache{
		hit:     nil, // exact miss
		baseHit: &CacheHit{Entry: models.PreviewStartupCache{CommitSHA: "def5678"}},
	}
	runner := &StartRunner{
		sandboxProvider: diffingSandboxProvider{
			fakeStartRunnerSandboxProvider: fakeStartRunnerSandboxProvider{
				files: map[string][]byte{"/workspace/repo/package-lock.json": []byte(`{"lockfileVersion":3}`)},
			},
			diff: diff,
		},
		snapshotCache: cache,
		logger:        zerolog.Nop(),
	}

	keys, err := runner.maybeRestoreBranchPreviewStartupCache(context.Background(), orgID, repoID, "abc1234", sb, cfg)
	require.NoError(t, err, "partial invalidation success should not surface an error")

	require.Equal(t, keys.BaseKey, cache.baseFindKey, "an exact miss should look up base snapshots by base key")
	require.True(t, cache.partialCalled, "a base snapshot hit should restore and patch instead of launching cold")
	require.Equal(t, diff, cache.partialDiff, "the git diff from the base commit should be applied on top of the restored snapshot")
	require.False(t, cache.restoreCalled, "partial invalidation owns the restore; the exact-hit restore path should not run")
}

// diffingSandboxProvider serves a canned `git diff` for partial invalidation
// tests while inheriting lockfile reads from fakeStartRunnerSandboxProvider.
type diffingSandboxProvider struct {
	fakeStartRunnerSandboxProvider
	diff []byte
}

func (p diffingSandboxProvider) Exec(_ context.Context, _ *agent.Sandbox, cmd string, stdout, _ io.Writer) (int, error) {
	if !strings.Contains(cmd, "git diff --binary") {
		panic("unexpected exec: " + cmd)
	}
	_, _ = stdout.Write(p.diff)
	return 0, nil
}

func TestStartRunnerCreateBranchPreviewStartupCache_RecordsSuccessfulLaunch(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	cache := &fakePreviewStartupCache{}
	runner := &StartRunner{snapshotCache: cache, logger: zerolog.Nop()}
	sb := &agent.Sandbox{ID: "sandbox-1", WorkDir: "/workspace/repo"}

	keys := branchPreviewStartupCacheKeys{SnapshotKey: "cache-key", BaseKey: "base-key", CommitSHA: "abc1234"}
	runner.createBranchPreviewStartupCache(context.Background(), orgID, repoID, keys, sb, nil)

	require.Equal(t, "cache-key", cache.createKey, "successful branch preview launch should write the startup cache snapshot")
	require.Equal(t, SnapshotMetadata{OrgID: orgID, RepoID: repoID, BaseKey: "base-key", CommitSHA: "abc1234"}, cache.createMeta, "startup cache metadata should record org, repo, base key, and commit")
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

	keys, err := runner.maybeRestoreBranchPreviewStartupCache(context.Background(), orgID, repoID, "abc1234", sb, cfg)
	require.NoError(t, err, "skipping secret-file configs should not surface an error")
	runner.createBranchPreviewStartupCache(context.Background(), orgID, repoID, branchPreviewStartupCacheKeys{SnapshotKey: "cache-key"}, sb, cfg)

	require.Empty(t, keys.SnapshotKey, "branch preview startup cache should not restore snapshots for configs with generated secret files")
	require.Empty(t, cache.findKey, "secret-file configs should not query startup cache entries")
	require.Empty(t, cache.createKey, "secret-file configs should not write startup cache snapshots")
	require.False(t, cache.restoreCalled, "secret-file configs should not restore cached workspace files")
}
