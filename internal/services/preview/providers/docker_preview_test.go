package providers

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/preview"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_PREVIEW_PORT_LISTENER_HELPER") == "1" {
		runPreviewPortListenerHelper()
		return
	}
	os.Exit(m.Run())
}

type mockDockerPreviewClient struct {
	createHostConfig       *container.HostConfig
	createNetworkingConfig *network.NetworkingConfig
	inspectResp            container.InspectResponse

	// Image presence simulation:
	// - imagesPresent[ref] == true means ImageInspect returns success.
	// - imagePullCalls counts ImagePull invocations so tests can assert
	//   the lazy-pull path was (or was not) taken.
	// - imagePullErr, when set, makes ImagePull fail.
	// - imagePullPopulates, when true, flips imagesPresent[ref]=true
	//   after a successful pull so the post-pull verification inspect
	//   succeeds.
	// - imagePullBody, when set, replaces the default pull-stream payload
	//   so tests can inject errorDetail events.
	imagesPresent      map[string]bool
	imagePullCalls     int
	imagePullErr       error
	imagePullPopulates bool
	imagePullBody      string
	imagePullMu        sync.Mutex
}

func successfulPreviewDialer(_ context.Context, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func previewReachableClient() *mockDockerPreviewClient {
	return &mockDockerPreviewClient{inspectResp: container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"143-sandbox": {IPAddress: "172.30.0.2"},
			},
		},
	}}
}

func (m *mockDockerPreviewClient) ContainerCreate(_ context.Context, _ *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	m.createHostConfig = hostConfig
	m.createNetworkingConfig = networkingConfig
	return container.CreateResponse{ID: "infra-1"}, nil
}

func (m *mockDockerPreviewClient) ContainerStart(_ context.Context, _ string, _ container.StartOptions) error {
	return nil
}

func (m *mockDockerPreviewClient) ContainerStop(_ context.Context, _ string, _ container.StopOptions) error {
	return nil
}

func (m *mockDockerPreviewClient) ContainerRemove(_ context.Context, _ string, _ container.RemoveOptions) error {
	return nil
}

func (m *mockDockerPreviewClient) ContainerInspect(_ context.Context, _ string) (container.InspectResponse, error) {
	return m.inspectResp, nil
}

func (m *mockDockerPreviewClient) ContainerExecCreate(_ context.Context, _ string, _ container.ExecOptions) (container.ExecCreateResponse, error) {
	return container.ExecCreateResponse{}, nil
}

func (m *mockDockerPreviewClient) ContainerExecAttach(_ context.Context, _ string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
	return types.HijackedResponse{}, nil
}

func (m *mockDockerPreviewClient) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
	return container.ExecInspect{}, nil
}

func (m *mockDockerPreviewClient) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	m.imagePullMu.Lock()
	defer m.imagePullMu.Unlock()
	if m.imagesPresent[ref] {
		return image.InspectResponse{}, nil
	}
	return image.InspectResponse{}, cerrdefs.ErrNotFound
}

func (m *mockDockerPreviewClient) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	m.imagePullMu.Lock()
	m.imagePullCalls++
	if m.imagePullErr != nil {
		m.imagePullMu.Unlock()
		return nil, m.imagePullErr
	}
	if m.imagePullPopulates {
		if m.imagesPresent == nil {
			m.imagesPresent = map[string]bool{}
		}
		m.imagesPresent[ref] = true
	}
	body := m.imagePullBody
	m.imagePullMu.Unlock()
	if body == "" {
		body = `{"status":"Pulled"}`
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

// gatedPullClient wraps the mock so ImagePull blocks on a shared channel —
// the singleflight dedup test needs concurrent callers to all queue up on
// the same key before any of them complete.
type gatedPullClient struct {
	*mockDockerPreviewClient
	gate <-chan struct{}
}

func (g *gatedPullClient) ImagePull(ctx context.Context, ref string, opts image.PullOptions) (io.ReadCloser, error) {
	<-g.gate
	return g.mockDockerPreviewClient.ImagePull(ctx, ref, opts)
}

type noopSandboxExecutor struct{}

func (n *noopSandboxExecutor) ExecStream(_ context.Context, _ *agent.Sandbox, _ string, _ func(line []byte), _ io.Writer) (int, error) {
	return 0, nil
}

func (n *noopSandboxExecutor) Exec(_ context.Context, _ *agent.Sandbox, _ string, _, _ io.Writer) (int, error) {
	return 0, nil
}

func (n *noopSandboxExecutor) ReadFile(_ context.Context, _ *agent.Sandbox, _ string) ([]byte, error) {
	return nil, nil
}

func TestBuildInfraEnv_Postgres(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}
	cred := preview.InfraCredential{
		Username: "preview_db",
		Password: "secret123",
		Database: "preview_db",
	}
	env := d.buildInfraEnv("postgres-17", cred)
	require.Contains(t, env, "POSTGRES_USER=preview_db")
	require.Contains(t, env, "POSTGRES_PASSWORD=secret123")
	require.Contains(t, env, "POSTGRES_DB=preview_db")
}

func TestNewDockerPreviewProvider_DefaultNetworkMatchesCompose(t *testing.T) {
	t.Parallel()

	provider := NewDockerPreviewProvider(nil, nil, zerolog.Nop())

	require.Equal(t, "143-sandbox", provider.network, "default preview network should match the sandbox bridge name")
}

func TestNewDockerPreviewProvider_WithPreviewNetworkOverride(t *testing.T) {
	t.Parallel()

	provider := NewDockerPreviewProvider(nil, nil, zerolog.Nop(), WithPreviewNetwork("custom-preview-net"))

	require.Equal(t, "custom-preview-net", provider.network, "explicit preview network option should override the default")
}

func TestProvisionInfra_UsesSandboxNetwork(t *testing.T) {
	t.Parallel()

	cli := &mockDockerPreviewClient{
		imagesPresent: map[string]bool{"postgres:17": true},
		inspectResp: container.InspectResponse{
			ContainerJSONBase: &container.ContainerJSONBase{
				HostConfig: &container.HostConfig{
					NetworkMode: container.NetworkMode("143-sandbox-custom"),
				},
			},
			NetworkSettings: &container.NetworkSettings{
				Networks: map[string]*network.EndpointSettings{
					"143-sandbox-custom": {IPAddress: "172.18.0.5"},
				},
			},
		},
	}
	provider := NewDockerPreviewProvider(cli, &noopSandboxExecutor{}, zerolog.Nop())

	handle, err := provider.provisionInfra(
		context.Background(),
		&agent.Sandbox{ID: "sandbox-1"},
		"preview-handle",
		"db",
		models.InfrastructureConfig{Template: "postgres-17"},
		preview.InfraTemplate{Image: "postgres:17", DefaultMemMB: 128, DefaultCPU: 0.25, DefaultPort: 5432},
	)

	require.NoError(t, err, "provisionInfra should use the sandbox network when creating infra containers")
	require.NotNil(t, handle, "provisionInfra should return a handle")
	require.Equal(t, container.NetworkMode("143-sandbox-custom"), cli.createHostConfig.NetworkMode, "infra containers should join the sandbox container network")
	require.Contains(t, cli.createNetworkingConfig.EndpointsConfig, "143-sandbox-custom", "network aliases should be attached to the sandbox network")
	require.Zero(t, cli.imagePullCalls, "image is already present; ensureImage must not pull")
}

// TestProvisionInfra_PullsMissingImage verifies the lazy-pull behavior:
// when the requested template image isn't on the host, provisionInfra
// pulls it before creating the container, so workers don't need every
// supported postgres/redis/mysql image pre-pulled at provision time.
func TestProvisionInfra_PullsMissingImage(t *testing.T) {
	t.Parallel()

	cli := &mockDockerPreviewClient{
		// Image starts absent; the pull populates it so the post-pull
		// verification inspect succeeds.
		imagesPresent:      map[string]bool{},
		imagePullPopulates: true,
		inspectResp: container.InspectResponse{
			ContainerJSONBase: &container.ContainerJSONBase{
				HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("143-sandbox")},
			},
			NetworkSettings: &container.NetworkSettings{
				Networks: map[string]*network.EndpointSettings{"143-sandbox": {IPAddress: "172.18.0.5"}},
			},
		},
	}
	provider := NewDockerPreviewProvider(cli, &noopSandboxExecutor{}, zerolog.Nop())

	_, err := provider.provisionInfra(
		context.Background(),
		&agent.Sandbox{ID: "sandbox-1"},
		"preview-handle",
		"db",
		models.InfrastructureConfig{Template: "postgres-17"},
		preview.InfraTemplate{Image: "postgres:17-alpine", DefaultMemMB: 128, DefaultCPU: 0.25, DefaultPort: 5432},
	)

	require.NoError(t, err)
	require.Equal(t, 1, cli.imagePullCalls, "missing image should trigger exactly one pull")
}

// TestProvisionInfra_PullFailureSurfacesAsImageUnavailable verifies that
// when the registry pull fails, the caller receives ErrInfraImageUnavailable
// (the sentinel the HTTP handler maps to PREVIEW_INFRA_IMAGE_UNAVAILABLE).
// Bare network/registry errors must not bubble up unwrapped — that's what
// produced the opaque "failed to start preview" message before this fix.
func TestProvisionInfra_PullFailureSurfacesAsImageUnavailable(t *testing.T) {
	t.Parallel()

	cli := &mockDockerPreviewClient{
		imagesPresent: map[string]bool{},
		imagePullErr:  errors.New("registry unreachable"),
		// Provide a usable inspect response so resolveSandboxNetwork
		// succeeds; we want this test to fail in ensureImage, not earlier.
		inspectResp: container.InspectResponse{
			ContainerJSONBase: &container.ContainerJSONBase{
				HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("143-sandbox")},
			},
			NetworkSettings: &container.NetworkSettings{
				Networks: map[string]*network.EndpointSettings{"143-sandbox": {IPAddress: "172.18.0.5"}},
			},
		},
	}
	provider := NewDockerPreviewProvider(cli, &noopSandboxExecutor{}, zerolog.Nop())

	_, err := provider.provisionInfra(
		context.Background(),
		&agent.Sandbox{ID: "sandbox-1"},
		"preview-handle",
		"db",
		models.InfrastructureConfig{Template: "postgres-17"},
		preview.InfraTemplate{Image: "postgres:17-alpine", DefaultMemMB: 128, DefaultCPU: 0.25, DefaultPort: 5432},
	)

	require.Error(t, err)
	require.ErrorIs(t, err, preview.ErrInfraImageUnavailable, "pull failures must wrap ErrInfraImageUnavailable so the handler can classify them")
	require.Contains(t, err.Error(), "postgres:17-alpine", "error must name the image so the user can debug")
	require.Contains(t, err.Error(), "registry unreachable", "error must include the underlying cause")
}

// TestProvisionInfra_PullStreamErrorDetailSurfaces verifies that registry
// errors which Docker reports as JSON `errorDetail` events inside the pull
// stream — rather than as an ImagePull return value — are surfaced to the
// caller. Without parsing the stream the user sees the useless message
// "image not present after pull"; with parsing they see the actual cause
// (manifest unknown, unauthorized, rate limit, …).
func TestProvisionInfra_PullStreamErrorDetailSurfaces(t *testing.T) {
	t.Parallel()

	cli := &mockDockerPreviewClient{
		imagesPresent: map[string]bool{},
		// Simulate Docker's NDJSON pull stream emitting an error event.
		imagePullBody: `{"status":"Pulling from library/postgres"}` + "\n" +
			`{"errorDetail":{"message":"manifest for postgres:99-alpine not found: manifest unknown"},"error":"manifest unknown"}` + "\n",
		inspectResp: container.InspectResponse{
			ContainerJSONBase: &container.ContainerJSONBase{
				HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("143-sandbox")},
			},
			NetworkSettings: &container.NetworkSettings{
				Networks: map[string]*network.EndpointSettings{"143-sandbox": {IPAddress: "172.18.0.5"}},
			},
		},
	}
	provider := NewDockerPreviewProvider(cli, &noopSandboxExecutor{}, zerolog.Nop())

	_, err := provider.provisionInfra(
		context.Background(),
		&agent.Sandbox{ID: "sandbox-1"},
		"preview-handle",
		"db",
		models.InfrastructureConfig{Template: "postgres-17"},
		preview.InfraTemplate{Image: "postgres:99-alpine", DefaultMemMB: 128, DefaultCPU: 0.25, DefaultPort: 5432},
	)

	require.Error(t, err)
	require.ErrorIs(t, err, preview.ErrInfraImageUnavailable)
	require.Contains(t, err.Error(), "manifest for postgres:99-alpine not found", "errorDetail message must reach the user, not be swallowed by io.Discard")
}

// TestEnsureImage_SingleflightDedupes verifies that two concurrent
// ensureImage calls for the same ref share one underlying ImagePull call.
// Without dedup, every preview start that lands on a missing image would
// fan out a separate pull stream against the daemon — wasteful and prone
// to thundering-herd at boot.
func TestEnsureImage_SingleflightDedupes(t *testing.T) {
	t.Parallel()

	// pullGate blocks ImagePull until the test releases it, giving
	// concurrent callers time to all enter the singleflight before any
	// of them complete. Without this gate the first caller could finish
	// before the second arrives, defeating the dedup test.
	pullGate := make(chan struct{})
	cli := &gatedPullClient{
		mockDockerPreviewClient: &mockDockerPreviewClient{
			imagesPresent:      map[string]bool{},
			imagePullPopulates: true,
		},
		gate: pullGate,
	}
	provider := NewDockerPreviewProvider(cli, &noopSandboxExecutor{}, zerolog.Nop())

	const callers = 5
	errCh := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			errCh <- provider.ensureImage(context.Background(), "postgres:17-alpine")
		}()
	}
	// Release the pull and collect results.
	close(pullGate)
	for i := 0; i < callers; i++ {
		require.NoError(t, <-errCh)
	}
	require.Equal(t, 1, cli.imagePullCalls, "%d concurrent ensureImage callers should share a single underlying pull", callers)
}

func TestBuildInfraEnv_Redis(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}
	cred := preview.InfraCredential{Password: "redispass"}
	env := d.buildInfraEnv("redis-7", cred)
	require.Equal(t, []string{"REDIS_PASSWORD=redispass"}, env)
}

func TestBuildInfraEnv_MySQL(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}
	cred := preview.InfraCredential{
		Username: "preview_db",
		Password: "mysqlpass",
		Database: "preview_db",
	}
	env := d.buildInfraEnv("mysql-8", cred)
	require.Len(t, env, 4)
	require.Contains(t, env, "MYSQL_ROOT_PASSWORD=mysqlpass")
	require.Contains(t, env, "MYSQL_USER=preview_db")
	require.Contains(t, env, "MYSQL_PASSWORD=mysqlpass")
	require.Contains(t, env, "MYSQL_DATABASE=preview_db")
}

func TestBuildInfraEnv_Unknown(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}
	env := d.buildInfraEnv("unknown-1", preview.InfraCredential{})
	require.Nil(t, env)
}

func TestResolveCredentialTemplate(t *testing.T) {
	t.Parallel()
	cred := preview.InfraCredential{
		Host:     "preview-db-abc123",
		Port:     5432,
		Username: "preview_db",
		Password: "secret",
		Database: "preview_db",
	}
	result := resolveCredentialTemplate("postgres://{{username}}:{{password}}@{{host}}:{{port}}/{{database}}", cred)
	require.Equal(t, "postgres://preview_db:secret@preview-db-abc123:5432/preview_db", result)
}

// TestBuildInfraCredential_PopulatesAllFields locks in the invariant that the
// constructor populates every field consumed by resolveCredentialTemplate. A
// previous bug left Host/Port unset and produced a DSN with empty host/port,
// which the migrator silently dialed against [::1]:5432 instead of the real
// container.
func TestBuildInfraCredential_PopulatesAllFields(t *testing.T) {
	t.Parallel()
	cred, err := buildInfraCredential("db", "preview-db-abc123", 5432)
	require.NoError(t, err)
	require.Equal(t, "preview-db-abc123", cred.Host)
	require.Equal(t, 5432, cred.Port)
	require.Equal(t, "preview_db", cred.Username)
	require.Equal(t, "preview_db", cred.Database)
	require.Len(t, cred.Password, 32) // 16 bytes → 32 hex chars
}

func TestGenerateHandle(t *testing.T) {
	t.Parallel()
	h1, err := preview.RandomHex(16)
	require.NoError(t, err)
	h2, err := preview.RandomHex(16)
	require.NoError(t, err)
	require.Len(t, h1, 32)
	require.NotEqual(t, h1, h2)
}

func TestShellEscape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, want string
	}{
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"it's", "'it'\\''s'"},
		{"", "''"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, shellEscape(tt.input))
		})
	}
}

func TestBuildServiceEnvs(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}

	cfg := &models.PreviewConfig{
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {
				Port: 3000,
				Env:  map[string]string{"NODE_ENV": "development"},
			},
			"worker": {
				Port: 4000,
				Env:  map[string]string{"WORKER_THREADS": "2"},
			},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {
				Template:   "postgres-17",
				InjectEnv:  map[string]string{"DATABASE_URL": "postgres://{{username}}:{{password}}@{{host}}:{{port}}/{{database}}"},
				InjectInto: []string{"web", "worker"},
			},
		},
	}

	// Build the credential via the production constructor so this test
	// exercises the same wiring as provisionInfra. A prior version of this
	// test hand-rolled the InfraCredential literal with Host/Port set, which
	// hid a bug where the production path stored a credential with empty
	// Host/Port and produced an unconnectable DATABASE_URL.
	cred, err := buildInfraCredential("db", "preview-db-abc", 5432)
	require.NoError(t, err)
	infraCreds := map[string]preview.InfraCredential{"db": cred}
	expectedDSN := fmt.Sprintf("postgres://preview_db:%s@preview-db-abc:5432/preview_db", cred.Password)

	envs := d.buildServiceEnvs(cfg, infraCreds, nil)

	// web should have NODE_ENV + DATABASE_URL
	require.Equal(t, "development", envs["web"]["NODE_ENV"])
	require.Equal(t, expectedDSN, envs["web"]["DATABASE_URL"])

	// worker should have WORKER_THREADS + DATABASE_URL
	require.Equal(t, "2", envs["worker"]["WORKER_THREADS"])
	require.Equal(t, expectedDSN, envs["worker"]["DATABASE_URL"])
}

// TestBuildServiceEnvs_ExtraEnvOverrides verifies that platform-level extras
// (e.g. PREVIEW_ORIGIN) are applied to every service and win over both user
// env and infra-injected env.
func TestBuildServiceEnvs_ExtraEnvOverrides(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}

	cfg := &models.PreviewConfig{
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web":    {Port: 3000, Env: map[string]string{"PREVIEW_ORIGIN": "user-wins"}},
			"worker": {Port: 9000},
		},
		Infrastructure: map[string]models.InfrastructureConfig{},
	}

	envs := d.buildServiceEnvs(cfg, nil, map[string]string{"PREVIEW_ORIGIN": "http://abc.preview.localhost:9090"})

	require.Equal(t, "http://abc.preview.localhost:9090", envs["web"]["PREVIEW_ORIGIN"], "extraEnv must override user-declared value")
	require.Equal(t, "http://abc.preview.localhost:9090", envs["worker"]["PREVIEW_ORIGIN"], "extraEnv must reach services with no user env")
}

func TestBuildServiceEnvs_RuntimeSecretEnvScopedBeforePlatformEnv(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}

	cfg := &models.PreviewConfig{
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web":    {Port: 3000, Env: map[string]string{"DATABASE_URL": "repo", "PREVIEW_ORIGIN": "repo-origin"}},
			"worker": {Port: 9000},
		},
		Infrastructure: map[string]models.InfrastructureConfig{},
		RuntimeSecretEnv: map[string]map[string]string{
			"web": {"DATABASE_URL": "secret", "PREVIEW_ORIGIN": "secret-origin"},
		},
	}

	envs := d.buildServiceEnvs(cfg, nil, map[string]string{"PREVIEW_ORIGIN": "platform-origin"})

	require.Equal(t, "secret", envs["web"]["DATABASE_URL"], "runtime secret env should override repo env for scoped services")
	require.Equal(t, "platform-origin", envs["web"]["PREVIEW_ORIGIN"], "platform env should remain authoritative over secret env")
	require.Empty(t, envs["worker"]["DATABASE_URL"], "runtime secret env should not reach unscoped services")
}

func TestBuildServiceEnvs_NoInfra(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}

	cfg := &models.PreviewConfig{
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {
				Port: 3000,
				Env:  map[string]string{"PORT": "3000"},
			},
		},
		Infrastructure: map[string]models.InfrastructureConfig{},
	}

	envs := d.buildServiceEnvs(cfg, nil, nil)
	require.Equal(t, "3000", envs["web"]["PORT"])
	require.Len(t, envs["web"], 1)
}

func TestTcpPreviewStream_NilClose(t *testing.T) {
	t.Parallel()
	s := &tcpPreviewStream{Conn: nil}
	require.NoError(t, s.Close())
}

// hangingSandboxExecutor blocks Exec on a per-call channel so tests can prove
// the readiness loop bounds each docker-exec attempt with a timeout instead
// of letting a wedged daemon stall the whole probe.
type hangingSandboxExecutor struct {
	calls   chan struct{}
	release chan struct{}
}

func (h *hangingSandboxExecutor) ExecStream(_ context.Context, _ *agent.Sandbox, _ string, _ func(line []byte), _ io.Writer) (int, error) {
	return 0, nil
}

func (h *hangingSandboxExecutor) Exec(ctx context.Context, _ *agent.Sandbox, _ string, _, _ io.Writer) (int, error) {
	if h.calls != nil {
		select {
		case h.calls <- struct{}{}:
		default:
		}
	}
	if h.release != nil {
		select {
		case <-h.release:
		case <-ctx.Done():
			return 1, ctx.Err()
		}
	} else {
		<-ctx.Done()
		return 1, ctx.Err()
	}
	return 1, nil
}

func (h *hangingSandboxExecutor) ReadFile(_ context.Context, _ *agent.Sandbox, _ string) ([]byte, error) {
	return nil, nil
}

// recordingObserver captures Ready/Failed calls so tests can assert the
// provider notified the observer at the right transitions. Safe for
// concurrent use because progressive support and the startService goroutine
// can both fire it at the same time.
type recordingObserver struct {
	mu                 sync.Mutex
	readyCalls         []recordedReady
	failedCalls        []recordedFailed
	installFailedCalls []recordedInstallFailed
	outputCalls        []recordedOutput
	cacheRestores      []recordedCacheEvent
	cacheSaves         []recordedCacheEvent
	pmCacheRestores    []recordedCacheEvent
	pmCacheSaves       []recordedCacheEvent
	phaseStarts        []string
	phaseEnds          []string
}

type recordedReady struct {
	name string
	port int
	pid  int
}

type recordedFailed struct {
	name   string
	errMsg string
	tail   []string
}

type recordedInstallFailed struct {
	errMsg string
	tail   []string
}

type recordedOutput struct {
	name string
	line string
}

type recordedCacheEvent struct {
	status    string
	cacheKey  string
	sizeBytes int64
	err       error
}

func (r *recordingObserver) OnDependencyCacheRestore(status string, cacheKey string, sizeBytes int64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cacheRestores = append(r.cacheRestores, recordedCacheEvent{status: status, cacheKey: cacheKey, sizeBytes: sizeBytes, err: err})
}

func (r *recordingObserver) OnDependencyCacheSave(status string, cacheKey string, sizeBytes int64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cacheSaves = append(r.cacheSaves, recordedCacheEvent{status: status, cacheKey: cacheKey, sizeBytes: sizeBytes, err: err})
}

func (r *recordingObserver) OnPackageManagerCacheRestore(status string, cacheKey string, sizeBytes int64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pmCacheRestores = append(r.pmCacheRestores, recordedCacheEvent{status: status, cacheKey: cacheKey, sizeBytes: sizeBytes, err: err})
}

func (r *recordingObserver) OnPackageManagerCacheSave(status string, cacheKey string, sizeBytes int64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pmCacheSaves = append(r.pmCacheSaves, recordedCacheEvent{status: status, cacheKey: cacheKey, sizeBytes: sizeBytes, err: err})
}

func (r *recordingObserver) OnPhaseStart(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phaseStarts = append(r.phaseStarts, name)
}

func (r *recordingObserver) OnPhaseEnd(name string, _ error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phaseEnds = append(r.phaseEnds, name)
}

func (r *recordingObserver) OnServiceOutput(name, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outputCalls = append(r.outputCalls, recordedOutput{name: name, line: line})
}

func (r *recordingObserver) OnServiceReady(name string, port, pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readyCalls = append(r.readyCalls, recordedReady{name: name, port: port, pid: pid})
}

func (r *recordingObserver) OnServiceFailed(name, errMsg string, tail []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failedCalls = append(r.failedCalls, recordedFailed{
		name:   name,
		errMsg: errMsg,
		tail:   append([]string(nil), tail...),
	})
}

func (r *recordingObserver) OnInstallFailed(errMsg string, tail []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.installFailedCalls = append(r.installFailedCalls, recordedInstallFailed{
		errMsg: errMsg,
		tail:   append([]string(nil), tail...),
	})
}

func (r *recordingObserver) ready() []recordedReady {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedReady(nil), r.readyCalls...)
}

func (r *recordingObserver) failed() []recordedFailed {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedFailed(nil), r.failedCalls...)
}

func (r *recordingObserver) installFailed() []recordedInstallFailed {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedInstallFailed(nil), r.installFailedCalls...)
}

func (r *recordingObserver) output() []recordedOutput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedOutput(nil), r.outputCalls...)
}

func (r *recordingObserver) dependencyCacheRestores() []recordedCacheEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedCacheEvent(nil), r.cacheRestores...)
}

func (r *recordingObserver) packageManagerCacheRestores() []recordedCacheEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedCacheEvent(nil), r.pmCacheRestores...)
}

func (r *recordingObserver) packageManagerCacheSaves() []recordedCacheEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedCacheEvent(nil), r.pmCacheSaves...)
}

func (r *recordingObserver) phasesStarted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.phaseStarts...)
}

func (r *recordingObserver) phasesEnded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.phaseEnds...)
}

type fakeDependencyCache struct {
	mu           sync.Mutex
	findHit      *preview.DependencyCacheHit
	findErr      error
	restoreErr   error
	saveErr      error
	pmFindHit    *preview.DependencyCacheHit
	pmFindErr    error
	pmRestoreErr error
	pmSaveErr    error
	finds        int
	restores     int
	saves        int
	savePaths    []string
	pathFinds    map[models.PreviewCacheKind]int
	pathRestores map[models.PreviewCacheKind]int
	pathSaves    map[models.PreviewCacheKind]int
	restoreRoots []models.PreviewCacheRoot
	saveSpecs    []preview.PreviewPathCacheSaveSpec
}

func (f *fakeDependencyCache) Find(context.Context, uuid.UUID, uuid.UUID, string) (*preview.DependencyCacheHit, error) {
	return f.FindPathCache(context.Background(), uuid.Nil, uuid.Nil, models.PreviewCacheKindInstallArtifact, "")
}

func (f *fakeDependencyCache) Restore(context.Context, *agent.Sandbox, *preview.DependencyCacheHit) error {
	return f.RestorePathCache(context.Background(), nil, nil, models.PreviewCacheRootWorkDir)
}

func (f *fakeDependencyCache) Save(ctx context.Context, sb *agent.Sandbox, cacheKey string, paths []string, metadata preview.DependencyCacheMetadata) (preview.DependencyCacheSaveResult, error) {
	return f.SavePathCache(ctx, sb, preview.PreviewPathCacheSaveSpec{
		Kind:     models.PreviewCacheKindInstallArtifact,
		Root:     models.PreviewCacheRootWorkDir,
		CacheKey: cacheKey,
		Paths:    paths,
		Metadata: metadata,
	})
}

func (f *fakeDependencyCache) FindPathCache(_ context.Context, _ uuid.UUID, _ uuid.UUID, kind models.PreviewCacheKind, _ string) (*preview.DependencyCacheHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pathFinds == nil {
		f.pathFinds = map[models.PreviewCacheKind]int{}
	}
	f.pathFinds[kind]++
	switch kind {
	case models.PreviewCacheKindPackageManager:
		return f.pmFindHit, f.pmFindErr
	default:
		f.finds++
		return f.findHit, f.findErr
	}
}

func (f *fakeDependencyCache) RestorePathCache(_ context.Context, _ *agent.Sandbox, _ *preview.DependencyCacheHit, root models.PreviewCacheRoot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kind := models.PreviewCacheKindInstallArtifact
	if root == models.PreviewCacheRootHomeDir {
		kind = models.PreviewCacheKindPackageManager
	}
	if f.pathRestores == nil {
		f.pathRestores = map[models.PreviewCacheKind]int{}
	}
	f.pathRestores[kind]++
	f.restoreRoots = append(f.restoreRoots, root)
	switch kind {
	case models.PreviewCacheKindPackageManager:
		return f.pmRestoreErr
	default:
		f.restores++
		return f.restoreErr
	}
}

func (f *fakeDependencyCache) SavePathCache(_ context.Context, _ *agent.Sandbox, spec preview.PreviewPathCacheSaveSpec) (preview.DependencyCacheSaveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pathSaves == nil {
		f.pathSaves = map[models.PreviewCacheKind]int{}
	}
	f.pathSaves[spec.Kind]++
	f.saveSpecs = append(f.saveSpecs, spec)
	switch spec.Kind {
	case models.PreviewCacheKindPackageManager:
		return preview.DependencyCacheSaveResult{SizeBytes: 456}, f.pmSaveErr
	default:
		f.saves++
		f.savePaths = append([]string(nil), spec.Paths...)
		return preview.DependencyCacheSaveResult{SizeBytes: 123}, f.saveErr
	}
}

func (f *fakeDependencyCache) counts() (int, int, int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.finds, f.restores, f.saves, append([]string(nil), f.savePaths...)
}

func (f *fakeDependencyCache) pathCounts() (map[models.PreviewCacheKind]int, map[models.PreviewCacheKind]int, map[models.PreviewCacheKind]int, []models.PreviewCacheRoot, []preview.PreviewPathCacheSaveSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	finds := map[models.PreviewCacheKind]int{}
	for kind, count := range f.pathFinds {
		finds[kind] = count
	}
	restores := map[models.PreviewCacheKind]int{}
	for kind, count := range f.pathRestores {
		restores[kind] = count
	}
	saves := map[models.PreviewCacheKind]int{}
	for kind, count := range f.pathSaves {
		saves[kind] = count
	}
	return finds, restores, saves, append([]models.PreviewCacheRoot(nil), f.restoreRoots...), append([]preview.PreviewPathCacheSaveSpec(nil), f.saveSpecs...)
}

// fakeServiceExecutor lets a single test customize what each kind of exec
// call returns. ExecStream simulates the long-running service process
// (npm run dev / sh script.sh); Exec handles the readiness curl probes
// and the per-service `lsof` PID detection.
type fakeServiceExecutor struct {
	execStreamFn func(ctx context.Context, cmd string, onLine func([]byte)) (int, error)
	execFn       func(ctx context.Context, cmd string) (int, error)
	readFileFn   func(ctx context.Context, path string) ([]byte, error)
}

func (f *fakeServiceExecutor) ExecStream(ctx context.Context, _ *agent.Sandbox, cmd string, onLine func(line []byte), _ io.Writer) (int, error) {
	if f.execStreamFn != nil {
		return f.execStreamFn(ctx, cmd, onLine)
	}
	return 0, nil
}

func (f *fakeServiceExecutor) Exec(ctx context.Context, _ *agent.Sandbox, cmd string, _, _ io.Writer) (int, error) {
	if f.execFn != nil {
		return f.execFn(ctx, cmd)
	}
	return 0, nil
}

func (f *fakeServiceExecutor) ReadFile(ctx context.Context, _ *agent.Sandbox, path string) ([]byte, error) {
	if f.readFileFn != nil {
		return f.readFileFn(ctx, path)
	}
	return nil, nil
}

type recordingStopExecutor struct {
	mu        sync.Mutex
	execCalls []string
}

func (r *recordingStopExecutor) ExecStream(ctx context.Context, _ *agent.Sandbox, _ string, _ func(line []byte), _ io.Writer) (int, error) {
	<-ctx.Done()
	return -1, ctx.Err()
}

func (r *recordingStopExecutor) Exec(_ context.Context, _ *agent.Sandbox, cmd string, _, _ io.Writer) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execCalls = append(r.execCalls, cmd)
	return 0, nil
}

func (r *recordingStopExecutor) ReadFile(_ context.Context, _ *agent.Sandbox, _ string) ([]byte, error) {
	return nil, nil
}

func (r *recordingStopExecutor) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.execCalls...)
}

// TestNotifyService_NilSafe verifies the helper functions tolerate a nil
// observer — providers must work both when StartPreview is called from the
// manager (with an observer) and when it's called from a context that
// doesn't care about per-service updates.
func TestNotifyService_NilSafe(t *testing.T) {
	t.Parallel()
	require.NotPanics(t, func() { notifyServiceOutput(nil, "web", "booting") })
	require.NotPanics(t, func() { notifyServiceReady(nil, "web", 3000, 42) })
	require.NotPanics(t, func() { notifyServiceFailed(nil, "web", "boom", []string{"line"}) })
}

// TestNotifyService_InvokesObserver verifies the helpers forward arguments
// faithfully when the observer is non-nil.
func TestNotifyService_InvokesObserver(t *testing.T) {
	t.Parallel()
	obs := &recordingObserver{}
	notifyServiceOutput(obs, "web", "booting")
	notifyServiceReady(obs, "web", 3000, 42)
	notifyServiceFailed(obs, "server", "exited with code 126", []string{"hi", "bye"})

	output := obs.output()
	require.Len(t, output, 1)
	require.Equal(t, recordedOutput{name: "web", line: "booting"}, output[0])

	ready := obs.ready()
	require.Len(t, ready, 1)
	require.Equal(t, recordedReady{name: "web", port: 3000, pid: 42}, ready[0])

	failed := obs.failed()
	require.Len(t, failed, 1)
	require.Equal(t, "server", failed[0].name)
	require.Equal(t, "exited with code 126", failed[0].errMsg)
	require.Equal(t, []string{"hi", "bye"}, failed[0].tail)
}

func TestStartService_StreamsStartupOutputToObserver(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	exec := &fakeServiceExecutor{
		execFn: func(_ context.Context, _ string) (int, error) {
			return 1, nil
		},
		execStreamFn: func(ctx context.Context, _ string, onLine func([]byte)) (int, error) {
			onLine([]byte("running database migrations"))
			onLine([]byte("starting http server"))
			select {
			case <-release:
				return 0, nil
			case <-ctx.Done():
				return -1, ctx.Err()
			}
		},
	}
	d := NewDockerPreviewProvider(&mockDockerPreviewClient{}, exec, zerolog.Nop())
	obs := &recordingObserver{}

	state := &previewState{
		sandbox:  &agent.Sandbox{ID: "sb"},
		services: map[string]*serviceState{},
		cancelFn: func() {},
	}

	err := d.startService(
		context.Background(),
		state,
		"server",
		models.ServiceConfig{Command: []string{"go", "run", "."}, Port: 8080},
		nil,
		obs,
	)
	require.NoError(t, err, "startService should launch the service goroutine")

	require.Eventually(t, func() bool {
		return len(obs.output()) >= 2
	}, time.Second, 10*time.Millisecond, "observer should receive service output while the service is still starting")
	close(release)
	state.wg.Wait()

	require.Equal(t, []recordedOutput{
		{name: "server", line: "running database migrations"},
		{name: "server", line: "starting http server"},
	}, obs.output(), "observer should receive the exact startup output lines")
}

// TestStartService_FailureCapturesTailAndNotifies exercises the entire
// startService failure path: the goroutine streams output through onLine
// (which fills the ring buffer), then ExecStream returns a non-zero exit,
// and the deferred section copies the tail and fires the observer. This
// covers ~25 lines that previously had no test.
func TestStartService_FailureCapturesTailAndNotifies(t *testing.T) {
	t.Parallel()

	exec := &fakeServiceExecutor{
		execFn: func(_ context.Context, _ string) (int, error) {
			// lsof for PID detection: pretend nothing's listening.
			return 1, nil
		},
		execStreamFn: func(_ context.Context, _ string, onLine func([]byte)) (int, error) {
			onLine([]byte("starting up"))
			onLine([]byte("./bin/server: cannot execute"))
			return 126, nil
		},
	}
	d := NewDockerPreviewProvider(&mockDockerPreviewClient{}, exec, zerolog.Nop())
	obs := &recordingObserver{}

	state := &previewState{
		sandbox:  &agent.Sandbox{ID: "sb"},
		services: map[string]*serviceState{},
		cancelFn: func() {},
	}

	err := d.startService(
		context.Background(),
		state,
		"server",
		models.ServiceConfig{Command: []string{"sh", "preview-start.sh"}, Port: 8080},
		nil,
		obs,
	)
	require.NoError(t, err)
	state.wg.Wait()

	failed := obs.failed()
	require.Len(t, failed, 1, "observer should have been notified of the failure")
	require.Equal(t, "server", failed[0].name)
	require.Contains(t, failed[0].errMsg, "126")
	require.Equal(t, []string{"starting up", "./bin/server: cannot execute"}, failed[0].tail)
	require.Empty(t, obs.ready(), "should not see any Ready calls when the service exited non-zero")

	// Service status flipped to Failed in-memory so waitForReadiness can
	// fail-fast on it (covered by TestWaitForReadiness_FailsFastWhenServiceExited).
	d.mu.RLock()
	require.Equal(t, models.PreviewServiceStatusFailed, state.services["server"].status)
	d.mu.RUnlock()
}

// TestStartService_TailRingBufferBoundedAtServiceTailLines verifies that
// the per-service stdout/stderr ring buffer drops oldest lines after it
// fills, so a chatty service can't OOM the worker before it dies.
func TestStartService_TailRingBufferBoundedAtServiceTailLines(t *testing.T) {
	t.Parallel()

	totalLines := serviceTailLines + 50
	exec := &fakeServiceExecutor{
		execFn: func(_ context.Context, _ string) (int, error) { return 1, nil },
		execStreamFn: func(_ context.Context, _ string, onLine func([]byte)) (int, error) {
			for i := 0; i < totalLines; i++ {
				onLine([]byte(fmt.Sprintf("line-%d", i)))
			}
			return 1, nil
		},
	}
	d := NewDockerPreviewProvider(&mockDockerPreviewClient{}, exec, zerolog.Nop())
	obs := &recordingObserver{}
	state := &previewState{
		sandbox:  &agent.Sandbox{},
		services: map[string]*serviceState{},
		cancelFn: func() {},
	}

	err := d.startService(context.Background(), state, "web",
		models.ServiceConfig{Command: []string{"true"}, Port: 3000},
		nil, obs)
	require.NoError(t, err)
	state.wg.Wait()

	failed := obs.failed()
	require.Len(t, failed, 1)
	require.Len(t, failed[0].tail, serviceTailLines)
	// First retained line should be the (totalLines - serviceTailLines)th input.
	require.Equal(t, fmt.Sprintf("line-%d", totalLines-serviceTailLines), failed[0].tail[0])
	require.Equal(t, fmt.Sprintf("line-%d", totalLines-1), failed[0].tail[len(failed[0].tail)-1])
}

// TestFormatServiceExitError covers the user-visible error built from a
// service's non-zero exit. The bare exit number is opaque (especially the
// POSIX 127 "command not found"), so the formatted message has to:
//   - Decode 127 to a hint about $PATH / absolute paths in .143/config.json.
//   - Append the captured stdout/stderr tail so the user sees the real reason
//     (e.g. "/bin/sh: 1: npm: not found") in the API response, not just an
//     exit code.
func TestFormatServiceExitError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		exitCode int
		tail     []string
		want     []string // substrings we require to appear
		notWant  []string // substrings we require to be absent
	}{
		{
			name:     "code_127_includes_command_not_found_hint",
			exitCode: 127,
			tail:     []string{"/bin/sh: 1: npm: not found"},
			want:     []string{"127", "command not found", "$PATH", "npm: not found"},
		},
		{
			name:     "non_127_exit_omits_command_not_found_hint",
			exitCode: 1,
			tail:     []string{"crash boom"},
			want:     []string{"exited with code 1", "crash boom"},
			notWant:  []string{"command not found"},
		},
		{
			name:     "no_tail_keeps_message_terse",
			exitCode: 137,
			tail:     nil,
			want:     []string{"exited with code 137"},
			notWant:  []string{"last output"},
		},
		{
			name:     "tail_caps_to_last_three_lines",
			exitCode: 1,
			tail:     []string{"line-1", "line-2", "line-3", "line-4", "line-5"},
			want:     []string{"line-3", "line-4", "line-5"},
			notWant:  []string{"line-1", "line-2"},
		},
		{
			name:     "blank_tail_lines_filtered",
			exitCode: 1,
			tail:     []string{"", "  ", "real error"},
			want:     []string{"real error"},
			notWant:  []string{"last output:  "},
		},
		{
			// Trailing blanks must not degrade the surfacing error to a
			// bare "exited with code N" — services that pad readiness
			// output with blank-line separators (or end with a flushed
			// "\n\n") still need to expose their last real lines.
			name:     "trailing_blanks_look_back_for_real_content",
			exitCode: 1,
			tail:     []string{"line-A", "line-B", "real error", "", ""},
			want:     []string{"line-A", "line-B", "real error"},
			notWant:  []string{"last output: |"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatServiceExitError(tc.exitCode, tc.tail)
			for _, s := range tc.want {
				require.Contains(t, got, s, "want substring missing from %q", got)
			}
			for _, s := range tc.notWant {
				require.NotContains(t, got, s, "unwanted substring present in %q", got)
			}
		})
	}
}

// TestTruncatedTail_RuneCap guards the rune-based truncation: a single output
// line longer than maxRunes should be cut down with an ellipsis instead of
// flowing into the surfacing error message at full length, where it would
// crowd out the rest of the message in a constrained UI banner.
func TestTruncatedTail_RuneCap(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 500)
	got := truncatedTail([]string{long}, 3, 200)
	runes := []rune(got)
	require.LessOrEqual(t, len(runes), 201, "truncatedTail must respect maxRunes")
	require.Contains(t, got, "…", "truncated tail should end with an ellipsis")
}

// TestStartService_Code127IncludesCommandNotFoundHint is the integration-level
// regression guard: a service that exits 127 (the case that motivated this
// change) must surface a hint about $PATH and the captured stderr tail
// through the observer, so the user-visible launch error explains *why*
// instead of stopping at "exited with code 127".
func TestStartService_Code127IncludesCommandNotFoundHint(t *testing.T) {
	t.Parallel()

	exec := &fakeServiceExecutor{
		execFn: func(_ context.Context, _ string) (int, error) { return 1, nil },
		execStreamFn: func(_ context.Context, _ string, onLine func([]byte)) (int, error) {
			onLine([]byte("/bin/sh: 1: npm: not found"))
			return 127, nil
		},
	}
	d := NewDockerPreviewProvider(&mockDockerPreviewClient{}, exec, zerolog.Nop())
	obs := &recordingObserver{}
	state := &previewState{
		sandbox:  &agent.Sandbox{ID: "sb"},
		services: map[string]*serviceState{},
		cancelFn: func() {},
	}

	require.NoError(t, d.startService(
		context.Background(), state, "frontend",
		models.ServiceConfig{Command: []string{"npm", "run", "dev"}, Port: 3000},
		nil, obs,
	))
	state.wg.Wait()

	failed := obs.failed()
	require.Len(t, failed, 1)
	require.Contains(t, failed[0].errMsg, "127")
	require.Contains(t, failed[0].errMsg, "command not found")
	require.Contains(t, failed[0].errMsg, "npm: not found")
}

// TestWaitForReadiness_FailsFastWhenServiceStopped covers the second branch
// of checkExited: a service that exited cleanly (Stopped, not Failed) before
// becoming ready also short-circuits the loop.
func TestWaitForReadiness_FailsFastWhenServiceStopped(t *testing.T) {
	t.Parallel()
	d := NewDockerPreviewProvider(&mockDockerPreviewClient{}, &noopSandboxExecutor{}, zerolog.Nop())
	state := &previewState{
		sandbox:  &agent.Sandbox{},
		services: map[string]*serviceState{},
	}
	state.services["web"] = &serviceState{
		name:   "web",
		port:   3000,
		status: models.PreviewServiceStatusStopped,
	}
	err := d.waitForReadiness(context.Background(), state, "web", 3000, "/", 5*time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "stopped before becoming ready")
}

// TestStartPreview_ReadinessTimeoutSurfacesLiveTail verifies that a service
// that keeps running but never passes its readiness probe still exposes the
// current output tail. This is the dogfood-preview failure mode where the
// server may still be compiling or migrating when the readiness budget expires.
func TestStartPreview_ReadinessTimeoutSurfacesLiveTail(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, _ string, onLine func([]byte)) (int, error) {
			onLine([]byte("[143-preview] building binaries..."))
			onLine([]byte("go: downloading google.golang.org/genproto/googleapis/rpc"))
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, _ string) (int, error) {
			return 1, nil
		},
	}
	d := NewDockerPreviewProvider(&mockDockerPreviewClient{}, exec, zerolog.Nop())
	obs := &recordingObserver{}
	cfg := &models.PreviewConfig{
		Name:    "dogfood",
		Primary: "server",
		Services: map[string]models.ServiceConfig{
			"server": {
				Command: []string{"sh", ".143/preview-start.sh"},
				Port:    8080,
				Ready:   models.ReadinessProbe{HTTPPath: "/api/v1/health", TimeoutSeconds: 1},
			},
		},
	}

	_, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb"}, cfg, preview.StartPreviewOptions{}, obs)
	close(release)

	require.Error(t, err, "StartPreview should fail when readiness never passes")
	require.Contains(t, err.Error(), "readiness probe timed out", "error should explain readiness timeout")
	require.Contains(t, err.Error(), "[143-preview] building binaries...", "error should include live service output")
	require.Contains(t, err.Error(), "go: downloading", "error should include the most recent live service output")

	failed := obs.failed()
	require.Len(t, failed, 1, "observer should persist timeout diagnostics")
	require.Equal(t, "server", failed[0].name, "observer should identify the timed-out service")
	require.Contains(t, failed[0].errMsg, "readiness probe timed out", "observer error should explain readiness timeout")
	require.Equal(t, []string{
		"[143-preview] building binaries...",
		"go: downloading google.golang.org/genproto/googleapis/rpc",
	}, failed[0].tail, "observer should receive the live output tail")
}

// TestStartPreview_StandardMode_NotifiesObserverPerService runs StartPreview
// to completion in standard mode with two services, both of which become
// ready promptly, and asserts the observer received one OnServiceReady per
// service. This covers Phase 5 (startService) and the standard branch of
// Phase 6 (the readiness loop and notifyServiceReady call).
func TestStartPreview_StandardMode_NotifiesObserverPerService(t *testing.T) {
	t.Parallel()

	// Each service's exec stream blocks until the test releases it.
	release := make(chan struct{})
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, _ string, _ func([]byte)) (int, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			// curl readiness probes return 0 (ready); lsof PID detection
			// returns 1 (no listener). Distinguish by looking for "curl" in
			// the command.
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer))
	obs := &recordingObserver{}

	cfg := &models.PreviewConfig{
		Name:    "test-app",
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web":     {Command: []string{"npm", "run", "dev"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
			"support": {Command: []string{"node", "worker.js"}, Port: 9000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
	}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb"}, cfg, preview.StartPreviewOptions{}, obs)
	require.NoError(t, err)
	require.NotNil(t, handle)
	require.Equal(t, 3000, handle.PrimaryPort)
	require.False(t, handle.PartiallyReady)

	ready := obs.ready()
	require.Len(t, ready, 2)
	names := []string{ready[0].name, ready[1].name}
	require.ElementsMatch(t, []string{"web", "support"}, names)

	// Cleanup.
	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle))
}

func TestStartPreview_RunsPreviewInstallBeforeServices(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var mu sync.Mutex
	var streamCalls []string
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, onLine func([]byte)) (int, error) {
			mu.Lock()
			streamCalls = append(streamCalls, cmd)
			mu.Unlock()
			if strings.Contains(cmd, "'npm' 'ci'") {
				onLine([]byte("installed dependencies"))
				return 0, nil
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.HasPrefix(cmd, "test -e ") {
				return 0, nil
			}
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			switch {
			case path == "package-lock.json":
				return []byte(`{"lockfileVersion":3}`), nil
			case strings.Contains(path, ".143/cache/preview-install/"):
				return nil, os.ErrNotExist
			case path == "node_modules/.bin/next":
				return nil, os.ErrNotExist
			default:
				return nil, os.ErrNotExist
			}
		},
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer))
	cfg := previewInstallTestConfig()

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb"}, cfg, preview.StartPreviewOptions{}, &recordingObserver{})
	require.NoError(t, err, "StartPreview should succeed after preview.install completes")
	require.NotNil(t, handle, "StartPreview should return a handle")

	mu.Lock()
	calls := append([]string(nil), streamCalls...)
	mu.Unlock()
	require.Len(t, calls, 2, "preview.install and the service should each run once")
	require.Contains(t, calls[0], "'npm' 'ci'", "preview.install should run before any service command")
	require.Contains(t, calls[0], "rm -rf -- node_modules packages/*/node_modules", "preview.install should clean only declared clean paths")
	require.NotContains(t, calls[0], ".next", "preview.install cleanup should not remove undeclared paths")
	require.Contains(t, calls[1], "'npm' 'run' 'dev'", "service command should run after preview.install")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_WritesRuntimeSecretFilesAfterPreviewInstall(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var mu sync.Mutex
	var events []string
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, onLine func([]byte)) (int, error) {
			mu.Lock()
			switch {
			case strings.Contains(cmd, "'npm' 'ci'"):
				events = append(events, "install")
				mu.Unlock()
				onLine([]byte("installed dependencies"))
				return 0, nil
			case strings.Contains(cmd, "'npm' 'run' 'dev'"):
				events = append(events, "service")
				mu.Unlock()
				select {
				case <-release:
				case <-ctx.Done():
				}
				return 0, nil
			default:
				events = append(events, "stream")
				mu.Unlock()
				return 0, nil
			}
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			mu.Lock()
			if strings.Contains(cmd, "__143_SECRET_FILE__") {
				events = append(events, "secret_file")
			}
			mu.Unlock()
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 0, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			switch {
			case path == "package-lock.json":
				return []byte(`{"lockfileVersion":3}`), nil
			case strings.Contains(path, ".143/cache/preview-install/"):
				return nil, os.ErrNotExist
			case path == "node_modules/.bin/next":
				return nil, os.ErrNotExist
			default:
				return nil, os.ErrNotExist
			}
		},
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer))
	cfg := previewInstallTestConfig()
	cfg.RuntimeSecretFiles = []models.PreviewRuntimeSecretFile{{
		Path:    "development.conf.json",
		Mode:    "0600",
		Content: []byte(`{"database_url":"postgres://"}`),
	}}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb"}, cfg, preview.StartPreviewOptions{}, &recordingObserver{})
	require.NoError(t, err, "StartPreview should succeed with runtime secret files")
	require.NotNil(t, handle, "StartPreview should return a handle")

	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	require.Equal(t, []string{"install", "secret_file", "service"}, got, "runtime secret files should be written after install and before services")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_SkipsPreviewInstallWhenMarkerAndVerifyPathsExist(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var mu sync.Mutex
	var streamCalls []string
	var readPaths []string
	var execCalls []string
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
			mu.Lock()
			streamCalls = append(streamCalls, cmd)
			mu.Unlock()
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			mu.Lock()
			execCalls = append(execCalls, cmd)
			mu.Unlock()
			if strings.HasPrefix(cmd, "test -e ") {
				return 0, nil
			}
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			mu.Lock()
			readPaths = append(readPaths, path)
			mu.Unlock()
			switch {
			case path == "package-lock.json":
				return []byte(`{"lockfileVersion":3}`), nil
			case strings.Contains(path, ".143/cache/preview-install/"):
				return []byte("ok\n"), nil
			case path == "node_modules/.bin/next":
				return []byte("binary"), nil
			default:
				return nil, os.ErrNotExist
			}
		},
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer))
	cfg := previewInstallTestConfig()

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb"}, cfg, preview.StartPreviewOptions{}, &recordingObserver{})
	require.NoError(t, err, "StartPreview should succeed when preview.install cache is valid")
	require.NotNil(t, handle, "StartPreview should return a handle")

	mu.Lock()
	calls := append([]string(nil), streamCalls...)
	paths := append([]string(nil), readPaths...)
	execs := append([]string(nil), execCalls...)
	mu.Unlock()
	require.Len(t, calls, 1, "valid install marker and verify path should skip preview.install command")
	require.NotContains(t, calls[0], "'npm' 'ci'", "service command should not include skipped install command")
	require.Contains(t, calls[0], "'npm' 'run' 'dev'", "service command should still start")
	require.Contains(t, paths, "package-lock.json", "install cache key should read declared lockfile")
	require.Contains(t, strings.Join(execs, "\n"), "test -e 'node_modules/.bin/next'", "install skip should verify declared verify path")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_PreviewInstallFailureStopsBeforeServices(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var streamCalls []string
	exec := &fakeServiceExecutor{
		execStreamFn: func(_ context.Context, cmd string, onLine func([]byte)) (int, error) {
			mu.Lock()
			streamCalls = append(streamCalls, cmd)
			mu.Unlock()
			if strings.Contains(cmd, "'npm' 'ci'") {
				onLine([]byte("npm warn tar TAR_ENTRY_ERROR ENOENT"))
				onLine([]byte("npm error enoent Could not read package-lock.json"))
				return 1, nil
			}
			return 0, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			if path == "package-lock.json" {
				return []byte(`{"lockfileVersion":3}`), nil
			}
			return nil, os.ErrNotExist
		},
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer))
	obs := &recordingObserver{}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb"}, previewInstallTestConfig(), preview.StartPreviewOptions{}, obs)
	require.Nil(t, handle, "StartPreview should not return a handle when preview.install fails")
	require.Error(t, err, "StartPreview should fail when preview.install exits non-zero")
	require.ErrorIs(t, err, preview.ErrInstallFailed, "StartPreview should classify install failures with ErrInstallFailed")

	mu.Lock()
	calls := append([]string(nil), streamCalls...)
	mu.Unlock()
	require.Len(t, calls, 1, "service command should not run after preview.install failure")
	require.Contains(t, calls[0], "'npm' 'ci'", "the only stream command should be preview.install")
	failures := obs.installFailed()
	require.Len(t, failures, 1, "observer should receive the install failure")
	require.Contains(t, failures[0].errMsg, "exited with code 1", "install failure should include the exit code")
	require.Equal(t, []string{
		"npm warn tar TAR_ENTRY_ERROR ENOENT",
		"npm error enoent Could not read package-lock.json",
	}, failures[0].tail, "observer should receive the install output tail")
}

func TestStartPreview_DependencyCacheHitRestoresBeforeMarkerValidation(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var mu sync.Mutex
	var streamCalls []string
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
			mu.Lock()
			streamCalls = append(streamCalls, cmd)
			mu.Unlock()
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, _ string) (int, error) {
			return 0, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			switch path {
			case "package-lock.json":
				return []byte(`{"lockfileVersion":3}`), nil
			default:
				return []byte("ok"), nil
			}
		},
	}
	cache := &fakeDependencyCache{findHit: &preview.DependencyCacheHit{}}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer), WithDependencyCache(cache))
	obs := &recordingObserver{}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb", WorkDir: "/workspace/repo"}, previewInstallTestConfig(), preview.StartPreviewOptions{
		OrgID:        uuid.New(),
		RepositoryID: uuid.New(),
		SessionID:    uuid.New(),
	}, obs)
	require.NoError(t, err, "StartPreview should continue after dependency cache restore")
	require.NotNil(t, handle, "StartPreview should return a handle")

	finds, restores, _, _ := cache.counts()
	require.Equal(t, 1, finds, "dependency cache should be looked up once")
	require.Equal(t, 1, restores, "dependency cache hit should restore before marker validation")
	restoresObserved := obs.dependencyCacheRestores()
	require.Len(t, restoresObserved, 1, "observer should receive one restore event")
	require.Equal(t, "restored", restoresObserved[0].status, "observer should report a restored cache hit")

	mu.Lock()
	calls := append([]string(nil), streamCalls...)
	mu.Unlock()
	require.Len(t, calls, 1, "valid marker after restore should skip preview.install and only start service")
	require.NotContains(t, calls[0], "'npm' 'ci'", "preview.install should be skipped after restored cache satisfies marker validation")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_DependencyCacheRestoreSkippedWhenInstallMarkerMissing(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var mu sync.Mutex
	var streamCalls []string
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
			mu.Lock()
			streamCalls = append(streamCalls, cmd)
			mu.Unlock()
			if strings.Contains(cmd, "'npm' 'ci'") {
				return 0, nil
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			switch {
			case path == "package-lock.json":
				return []byte(`{"lockfileVersion":3}`), nil
			case strings.Contains(path, ".143/cache/preview-install/"):
				return nil, os.ErrNotExist
			default:
				return nil, os.ErrNotExist
			}
		},
	}
	cache := &fakeDependencyCache{findHit: &preview.DependencyCacheHit{Entry: models.PreviewDependencyCache{SizeBytes: 123}}}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer), WithDependencyCache(cache))
	obs := &recordingObserver{}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb", WorkDir: "/workspace/repo"}, previewInstallTestConfig(), preview.StartPreviewOptions{
		OrgID:        uuid.New(),
		RepositoryID: uuid.New(),
		SessionID:    uuid.New(),
	}, obs)
	require.NoError(t, err, "StartPreview should run install when the marker is absent")
	require.NotNil(t, handle, "StartPreview should return a handle")

	finds, restores, _, _ := cache.counts()
	require.Equal(t, 0, finds, "missing install marker should skip dependency cache lookup")
	require.Equal(t, 0, restores, "missing install marker should not restore dependency blobs that install will clean")
	restoresObserved := obs.dependencyCacheRestores()
	require.Len(t, restoresObserved, 1, "observer should receive one cache restore event")
	require.Equal(t, "skipped_marker_missing", restoresObserved[0].status, "observer should explain why restore was skipped")

	mu.Lock()
	calls := append([]string(nil), streamCalls...)
	mu.Unlock()
	require.Len(t, calls, 2, "missing marker should run install before starting service")
	require.Contains(t, calls[0], "'npm' 'ci'", "preview.install should run on first cold start")
	require.Contains(t, calls[1], "'npm' 'run' 'dev'", "service should start after install")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_PackageManagerCacheRestoresWhenInstallMarkerMissing(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var mu sync.Mutex
	var streamCalls []string
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
			mu.Lock()
			streamCalls = append(streamCalls, cmd)
			mu.Unlock()
			if strings.Contains(cmd, "'npm' 'ci'") {
				return 0, nil
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			switch {
			case path == "package-lock.json":
				return []byte(`{"lockfileVersion":3}`), nil
			case strings.Contains(path, ".143/cache/preview-install/"):
				return nil, os.ErrNotExist
			default:
				return nil, os.ErrNotExist
			}
		},
	}
	cache := &fakeDependencyCache{
		pmFindHit: &preview.DependencyCacheHit{Entry: models.PreviewDependencyCache{
			CacheKind: models.PreviewCacheKindPackageManager,
			SizeBytes: 99,
		}},
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer), WithDependencyCache(cache))
	obs := &recordingObserver{}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb", WorkDir: "/workspace/repo", HomeDir: "/home/codex"}, previewInstallTestConfig(), preview.StartPreviewOptions{
		OrgID:        uuid.New(),
		RepositoryID: uuid.New(),
		SessionID:    uuid.New(),
	}, obs)
	require.NoError(t, err, "StartPreview should continue after package-manager cache restore with a missing marker")
	require.NotNil(t, handle, "StartPreview should return a handle")

	finds, restores, _, roots, _ := cache.pathCounts()
	require.Equal(t, 1, finds[models.PreviewCacheKindPackageManager], "package-manager cache should be looked up even when marker is absent")
	require.Equal(t, 1, restores[models.PreviewCacheKindPackageManager], "package-manager cache should restore before install")
	require.Contains(t, roots, models.PreviewCacheRootHomeDir, "package-manager cache should restore relative to HomeDir")
	require.Equal(t, 0, finds[models.PreviewCacheKindInstallArtifact], "missing marker should still skip install artifact lookup")
	restoresObserved := obs.packageManagerCacheRestores()
	require.Len(t, restoresObserved, 1, "observer should receive one package-manager restore event")
	require.Equal(t, "restored", restoresObserved[0].status, "observer should report package-manager restored")

	mu.Lock()
	calls := append([]string(nil), streamCalls...)
	mu.Unlock()
	require.Len(t, calls, 2, "missing marker should run install before starting service")
	require.Contains(t, calls[0], "'npm' 'ci'", "preview.install should run after package-manager restore")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_PackageManagerCacheRestoreFailureContinuesCold(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var mu sync.Mutex
	var streamCalls []string
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
			mu.Lock()
			streamCalls = append(streamCalls, cmd)
			mu.Unlock()
			if strings.Contains(cmd, "'npm' 'ci'") {
				return 0, nil
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			if path == "package-lock.json" {
				return []byte(`{"lockfileVersion":3}`), nil
			}
			return nil, os.ErrNotExist
		},
	}
	cache := &fakeDependencyCache{
		pmFindHit: &preview.DependencyCacheHit{Entry: models.PreviewDependencyCache{
			CacheKind: models.PreviewCacheKindPackageManager,
			SizeBytes: 99,
		}},
		pmRestoreErr: fmt.Errorf("restore exploded"),
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer), WithDependencyCache(cache))
	obs := &recordingObserver{}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb", WorkDir: "/workspace/repo", HomeDir: "/home/codex"}, previewInstallTestConfig(), preview.StartPreviewOptions{
		OrgID:        uuid.New(),
		RepositoryID: uuid.New(),
		SessionID:    uuid.New(),
	}, obs)

	require.NoError(t, err, "StartPreview should continue cold after package-manager cache restore failure")
	require.NotNil(t, handle, "StartPreview should return a handle")
	mu.Lock()
	calls := strings.Join(streamCalls, "\n")
	mu.Unlock()
	require.Contains(t, calls, "'npm' 'ci'", "preview.install should run after package-manager cache restore failure")
	restoresObserved := obs.packageManagerCacheRestores()
	require.NotEmpty(t, restoresObserved, "observer should record package-manager restore status")
	require.Equal(t, "restore_failed", restoresObserved[len(restoresObserved)-1].status, "observer should report package-manager restore failure")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_PackageManagerCacheDisabledSkipsLookup(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
			if strings.Contains(cmd, "'npm' 'ci'") {
				return 0, nil
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			if path == "package-lock.json" {
				return []byte(`{"lockfileVersion":3}`), nil
			}
			return nil, os.ErrNotExist
		},
	}
	cache := &fakeDependencyCache{}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer), WithDependencyCache(cache), WithPackageManagerCacheEnabled(false))
	obs := &recordingObserver{}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb", WorkDir: "/workspace/repo", HomeDir: "/home/codex"}, previewInstallTestConfig(), preview.StartPreviewOptions{
		OrgID:        uuid.New(),
		RepositoryID: uuid.New(),
		SessionID:    uuid.New(),
	}, obs)

	require.NoError(t, err, "StartPreview should run with package-manager cache disabled")
	require.NotNil(t, handle, "StartPreview should return a handle")
	finds, _, _, _, _ := cache.pathCounts()
	require.Equal(t, 0, finds[models.PreviewCacheKindPackageManager], "disabled package-manager cache should not perform a lookup")
	restoresObserved := obs.packageManagerCacheRestores()
	require.NotEmpty(t, restoresObserved, "observer should record disabled package-manager status")
	require.Equal(t, "disabled", restoresObserved[0].status, "observer should report package-manager cache disabled")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_InstallPhaseTimingBreakdown(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
			if strings.Contains(cmd, "'npm' 'ci'") {
				return 0, nil
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			switch {
			case path == "package-lock.json":
				return []byte(`{"lockfileVersion":3}`), nil
			case strings.Contains(path, ".143/cache/preview-install/"):
				return nil, os.ErrNotExist
			default:
				return nil, os.ErrNotExist
			}
		},
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer))
	obs := &recordingObserver{}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb", WorkDir: "/workspace/repo"}, previewInstallTestConfig(), preview.StartPreviewOptions{
		OrgID: uuid.New(),
	}, obs)
	require.NoError(t, err, "StartPreview should succeed")
	require.NotNil(t, handle, "StartPreview should return a handle")

	started := obs.phasesStarted()
	ended := obs.phasesEnded()
	require.Subset(t, started, []string{"install_marker_check", "install_command"}, "install timing should break out marker check and command execution")
	require.Subset(t, ended, []string{"install_marker_check", "install_command"}, "install timing should close marker check and command execution phases")
	require.Contains(t, started, "install_build", "existing aggregate install phase should remain for compatibility")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_DependencyCacheRestoreFailureForcesInstallDespiteMarker(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var mu sync.Mutex
	var streamCalls []string
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
			mu.Lock()
			streamCalls = append(streamCalls, cmd)
			mu.Unlock()
			if strings.Contains(cmd, "'npm' 'ci'") {
				return 0, nil
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.HasPrefix(cmd, "test -e ") {
				return 0, nil
			}
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			switch {
			case path == "package-lock.json":
				return []byte(`{"lockfileVersion":3}`), nil
			case strings.Contains(path, ".143/cache/preview-install/"):
				return []byte("ok\n"), nil
			case path == "node_modules/.bin/next":
				return []byte("binary"), nil
			default:
				return nil, os.ErrNotExist
			}
		},
	}
	cache := &fakeDependencyCache{
		findHit:    &preview.DependencyCacheHit{Entry: models.PreviewDependencyCache{SizeBytes: 123}},
		restoreErr: fmt.Errorf("restore mutated dependency paths before failing"),
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer), WithDependencyCache(cache))
	obs := &recordingObserver{}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb", WorkDir: "/workspace/repo"}, previewInstallTestConfig(), preview.StartPreviewOptions{
		OrgID:        uuid.New(),
		RepositoryID: uuid.New(),
		SessionID:    uuid.New(),
	}, obs)
	require.NoError(t, err, "StartPreview should continue cold after dependency cache restore failure")
	require.NotNil(t, handle, "StartPreview should return a handle")

	mu.Lock()
	calls := append([]string(nil), streamCalls...)
	mu.Unlock()
	require.Len(t, calls, 2, "restore failure should force preview.install before starting service")
	require.Contains(t, calls[0], "'npm' 'ci'", "preview.install should run even when the marker appears valid after restore failure")
	require.Contains(t, calls[1], "'npm' 'run' 'dev'", "service should start after forced install succeeds")
	restoresObserved := obs.dependencyCacheRestores()
	require.Len(t, restoresObserved, 1, "observer should receive one restore event")
	require.Equal(t, "restore_failed", restoresObserved[0].status, "observer should report the restore failure")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_DependencyCacheMissRunsInstallAndSaves(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var mu sync.Mutex
	var streamCalls []string
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
			mu.Lock()
			streamCalls = append(streamCalls, cmd)
			mu.Unlock()
			if strings.Contains(cmd, "'npm' 'ci'") {
				return 0, nil
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, _ string) (int, error) {
			return 0, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			if path == "package-lock.json" {
				return []byte(`{"lockfileVersion":3}`), nil
			}
			return nil, os.ErrNotExist
		},
	}
	cache := &fakeDependencyCache{}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer), WithDependencyCache(cache))
	obs := &recordingObserver{}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb", WorkDir: "/workspace/repo"}, previewInstallTestConfig(), preview.StartPreviewOptions{
		OrgID:        uuid.New(),
		RepositoryID: uuid.New(),
		SessionID:    uuid.New(),
	}, obs)
	require.NoError(t, err, "StartPreview should run install after dependency cache miss")
	require.NotNil(t, handle, "StartPreview should return a handle")

	require.Eventually(t, func() bool {
		_, _, saves, paths := cache.counts()
		return saves == 1 && len(paths) > 0
	}, 2*time.Second, 10*time.Millisecond, "dependency cache should save asynchronously after install")
	_, restores, saves, paths := cache.counts()
	require.Equal(t, 0, restores, "dependency cache miss should not restore")
	require.Equal(t, 1, saves, "dependency cache miss should save after successful install")
	require.Contains(t, paths, "node_modules", "dependency cache save should include effective clean paths")

	mu.Lock()
	calls := strings.Join(streamCalls, "\n")
	mu.Unlock()
	require.Contains(t, calls, "'npm' 'ci'", "preview.install should run on dependency cache miss")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_PackageManagerAndDependencyCacheSavesAreIndependent(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
			if strings.Contains(cmd, "'npm' 'ci'") {
				return 0, nil
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
		readFileFn: func(_ context.Context, path string) ([]byte, error) {
			if path == "package-lock.json" {
				return []byte(`{"lockfileVersion":3}`), nil
			}
			return nil, os.ErrNotExist
		},
	}
	cache := &fakeDependencyCache{saveErr: fmt.Errorf("dependency artifact save failed")}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer), WithDependencyCache(cache))
	obs := &recordingObserver{}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb", WorkDir: "/workspace/repo", HomeDir: "/home/codex"}, previewInstallTestConfig(), preview.StartPreviewOptions{
		OrgID:        uuid.New(),
		RepositoryID: uuid.New(),
		SessionID:    uuid.New(),
	}, obs)
	require.NoError(t, err, "StartPreview should continue even when dependency cache save fails asynchronously")
	require.NotNil(t, handle, "StartPreview should return a handle")

	require.Eventually(t, func() bool {
		_, _, saves, _, specs := cache.pathCounts()
		return saves[models.PreviewCacheKindInstallArtifact] == 1 &&
			saves[models.PreviewCacheKindPackageManager] == 1 &&
			len(specs) == 2
	}, 2*time.Second, 10*time.Millisecond, "both cache kinds should attempt independent saves after install")

	_, _, saves, _, specs := cache.pathCounts()
	require.Equal(t, 1, saves[models.PreviewCacheKindInstallArtifact], "dependency artifact save should run once")
	require.Equal(t, 1, saves[models.PreviewCacheKindPackageManager], "package-manager cache save should run once")
	var packageManagerSpec *preview.PreviewPathCacheSaveSpec
	for i := range specs {
		if specs[i].Kind == models.PreviewCacheKindPackageManager {
			packageManagerSpec = &specs[i]
			break
		}
	}
	require.NotNil(t, packageManagerSpec, "package-manager save spec should be recorded")
	require.Equal(t, models.PreviewCacheRootHomeDir, packageManagerSpec.Root, "package-manager save should use the HomeDir root")
	require.Contains(t, packageManagerSpec.Paths, ".npm", "npm package-manager save should include the inferred npm cache path")
	pmSaves := obs.packageManagerCacheSaves()
	require.Len(t, pmSaves, 1, "observer should receive package-manager save result")
	require.Equal(t, "saved", pmSaves[0].status, "package-manager save should succeed independently from artifact save")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestBuildPreviewInstallCommand_RecreatesMarkerDirAfterCleanup(t *testing.T) {
	t.Parallel()

	cmd, err := buildPreviewInstallCommand(&models.PreviewInstallConfig{
		Command:    []string{"bash", ".143/preview-install.sh"},
		CleanPaths: []string{".143/cache", "node_modules"},
	}, ".143/cache/preview-install/cache-key.done")
	require.NoError(t, err, "buildPreviewInstallCommand should accept repo-local clean paths")

	cleanIndex := strings.Index(cmd, "rm -rf -- .143/cache node_modules")
	mkdirAfterCleanIndex := strings.LastIndex(cmd, "mkdir -p .143/cache/preview-install")
	markerIndex := strings.Index(cmd, "printf 'ok\\n' > ")
	require.NotEqual(t, -1, cleanIndex, "install command should clean declared paths")
	require.NotEqual(t, -1, mkdirAfterCleanIndex, "install command should create the marker directory")
	require.NotEqual(t, -1, markerIndex, "install command should write the success marker")
	require.Contains(t, cmd, ".143/cache/preview-install/cache-key.done", "install command should write the expected marker path")
	require.Greater(t, mkdirAfterCleanIndex, cleanIndex, "install command should recreate the marker directory after cleanup")
	require.Less(t, mkdirAfterCleanIndex, markerIndex, "install command should recreate the marker directory before writing the marker")
}

func previewInstallTestConfig() *models.PreviewConfig {
	return &models.PreviewConfig{
		Name:    "test-app",
		Primary: "web",
		Install: &models.PreviewInstallConfig{
			Command:        []string{"npm", "ci"},
			Lockfiles:      []string{"package-lock.json"},
			CleanPaths:     []string{"node_modules", "packages/*/node_modules"},
			VerifyPaths:    []string{"node_modules/.bin/next"},
			TimeoutSeconds: 420,
		},
		Services: map[string]models.ServiceConfig{
			"web": {Command: []string{"npm", "run", "dev"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
	}
}

// TestStartPreview_ProgressiveMode_NotifiesObserver covers Phase 6's
// progressive branch — primary becomes ready first (synchronously), support
// services come up in a background goroutine.
func TestStartPreview_ProgressiveMode_NotifiesObserver(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, _ string, _ func([]byte)) (int, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer))
	obs := &recordingObserver{}

	cfg := &models.PreviewConfig{
		Name:        "test-app",
		Primary:     "web",
		Progressive: true,
		Services: map[string]models.ServiceConfig{
			"web":     {Command: []string{"npm", "run", "dev"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
			"support": {Command: []string{"node", "worker.js"}, Port: 9000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
	}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb"}, cfg, preview.StartPreviewOptions{}, obs)
	require.NoError(t, err)
	require.True(t, handle.PartiallyReady)

	// Primary should be ready synchronously — assert without waiting.
	require.Eventually(t, func() bool {
		for _, r := range obs.ready() {
			if r.name == "web" {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "primary service should fire OnServiceReady before StartPreview returns")

	// Support service is polled in the background; allow time for it.
	require.Eventually(t, func() bool {
		for _, r := range obs.ready() {
			if r.name == "support" {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond, "support service should eventually fire OnServiceReady")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle))
}

// TestStartPreview_NilObserver_DoesNotPanic verifies that providers tolerate
// a nil observer — code paths in callers (e.g. tests, future internal
// callers) shouldn't be forced to construct a no-op observer just to call
// StartPreview.
func TestStartPreview_NilObserver_DoesNotPanic(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, _ string, _ func([]byte)) (int, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer))

	cfg := &models.PreviewConfig{
		Name:    "test-app",
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
	}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb"}, cfg, preview.StartPreviewOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 3000, handle.PrimaryPort)

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle))
}

func TestStartPreview_DoesNotReportInstallBuildPhaseWithoutInstall(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, _ string, _ func([]byte)) (int, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
	}
	d := NewDockerPreviewProvider(previewReachableClient(), exec, zerolog.Nop(), WithPreviewDialer(successfulPreviewDialer))
	obs := &recordingObserver{}

	cfg := &models.PreviewConfig{
		Name:    "test-app",
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
	}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb"}, cfg, preview.StartPreviewOptions{}, obs)
	require.NoError(t, err, "StartPreview should succeed without preview.install")
	require.NotContains(t, obs.phasesStarted(), "install_build", "install_build phase should only cover preview.install work")

	close(release)
	require.NoError(t, d.StopPreview(context.Background(), handle.Handle), "StopPreview should clean up the started preview")
}

func TestStartPreview_FailsWhenPrimaryPortNotExternallyReachable(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	exec := &fakeServiceExecutor{
		execStreamFn: func(ctx context.Context, _ string, _ func([]byte)) (int, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, nil
		},
		execFn: func(_ context.Context, cmd string) (int, error) {
			if strings.Contains(cmd, "curl") {
				return 0, nil
			}
			return 1, nil
		},
	}
	dialErr := errors.New("tcp timeout")
	d := NewDockerPreviewProvider(
		&mockDockerPreviewClient{inspectResp: container.InspectResponse{
			NetworkSettings: &container.NetworkSettings{
				Networks: map[string]*network.EndpointSettings{
					"143-sandbox": {IPAddress: "172.30.0.9"},
				},
			},
		}},
		exec,
		zerolog.Nop(),
		WithPreviewDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return nil, dialErr
		}),
	)
	obs := &recordingObserver{}

	cfg := &models.PreviewConfig{
		Name:    "test-app",
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {Command: []string{"npm", "start"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
		},
	}

	handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sb"}, cfg, preview.StartPreviewOptions{}, obs)
	close(release)

	require.Error(t, err, "StartPreview should fail when worker cannot dial the sandbox primary port")
	require.Nil(t, handle, "failed preview should not return a handle")
	require.ErrorIs(t, err, preview.ErrServiceNotReady, "external reachability failures should classify as service readiness failures")
	require.Contains(t, err.Error(), "external reachability check failed", "error should explain the worker-to-sandbox dial failure")
	require.Contains(t, err.Error(), "172.30.0.9:3000", "error should include the target address")

	failed := obs.failed()
	require.Len(t, failed, 1, "observer should receive the primary service reachability failure")
	require.Equal(t, "web", failed[0].name, "observer should identify the unreachable primary service")
	require.Contains(t, failed[0].errMsg, "external reachability check failed", "observer error should explain reachability failure")
}

func TestStopPreview_TerminatesServiceProcessesBeforeCleanup(t *testing.T) {
	t.Parallel()

	exec := &recordingStopExecutor{}
	d := NewDockerPreviewProvider(&mockDockerPreviewClient{}, exec, zerolog.Nop())
	handle := "preview-stop-test"
	cancelled := make(chan struct{})
	d.previews[handle] = &previewState{
		handle:   handle,
		sandbox:  &agent.Sandbox{ID: "sb"},
		infra:    map[string]*preview.InfraHandle{},
		services: map[string]*serviceState{},
		cancelFn: func() { close(cancelled) },
	}
	d.previews[handle].services["web"] = &serviceState{
		name:   "web",
		pid:    4242,
		port:   3000,
		status: models.PreviewServiceStatusReady,
	}
	d.previews[handle].services["worker"] = &serviceState{
		name:   "worker",
		port:   9000,
		status: models.PreviewServiceStatusReady,
	}

	err := d.StopPreview(context.Background(), handle)

	require.NoError(t, err, "StopPreview should complete service cleanup")
	select {
	case <-cancelled:
	default:
		require.Fail(t, "StopPreview should cancel service goroutines before cleanup")
	}
	calls := exec.calls()
	require.Len(t, calls, 2, "StopPreview should issue one termination command per service")
	require.Contains(t, strings.Join(calls, "\n"), "4242", "termination command should target the recorded service PID")
	require.Contains(t, strings.Join(calls, "\n"), ":3000", "termination command should target the ready service port")
	require.Contains(t, strings.Join(calls, "\n"), ":9000", "termination command should still use the port when PID detection has not populated")
}

func TestBuildTerminateServiceProcessCmd_IncludesProcFallbackForMissingLsof(t *testing.T) {
	t.Parallel()

	cmd := buildTerminateServiceProcessCmd(0, 8080)

	require.Contains(t, cmd, "/proc/net/tcp", "termination command should find listening sockets without lsof")
	require.Contains(t, cmd, "/proc/[0-9]*/fd/*", "termination command should map socket inodes back to owning processes")
}

func TestBuildTerminateServiceProcessCmd_KillsPortWithoutLsof(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("the /proc socket fallback is Linux-specific")
	}

	helper := exec.Command(os.Args[0], "-test.run=^$", "--")
	helper.Env = append(os.Environ(), "GO_WANT_PREVIEW_PORT_LISTENER_HELPER=1")
	stdout, err := helper.StdoutPipe()
	require.NoError(t, err, "helper stdout pipe should be created")
	helper.Stderr = os.Stderr
	require.NoError(t, helper.Start(), "helper listener should start")
	defer func() {
		if helper.Process != nil {
			_ = helper.Process.Kill()
		}
	}()

	scanner := bufio.NewScanner(stdout)
	require.True(t, scanner.Scan(), "helper should print its listening port")
	port, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	require.NoError(t, err, "helper should print a numeric port")

	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "lsof"), []byte("#!/bin/sh\nexit 0\n"), 0o755), "fake lsof should be installed")
	term := exec.Command("sh", "-c", buildTerminateServiceProcessCmd(0, port))
	term.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	output, err := term.CombinedOutput()
	require.NoError(t, err, "termination command should succeed without lsof output: %s", output)

	done := make(chan error, 1)
	go func() { done <- helper.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.Fail(t, "termination command should kill the listener discovered by port")
	}
}

func runPreviewPortListenerHelper() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println(ln.Addr().(*net.TCPAddr).Port)
	select {}
}

// TestWaitForReadiness_FailsFastWhenServiceExited verifies that the readiness
// loop bails immediately when the underlying service goroutine has set
// status=Failed, instead of polling for the entire (long) probe timeout.
// This was the worst part of the May 2026 16-minute preview-stuck incident:
// the user's `server` exited 126 in seconds but waitForReadiness kept curling
// a dead port for the full 4-minute budget.
func TestWaitForReadiness_FailsFastWhenServiceExited(t *testing.T) {
	t.Parallel()
	d := NewDockerPreviewProvider(&mockDockerPreviewClient{}, &noopSandboxExecutor{}, zerolog.Nop())
	state := &previewState{
		sandbox:  &agent.Sandbox{},
		services: map[string]*serviceState{},
	}
	state.services["server"] = &serviceState{
		name:   "server",
		port:   8080,
		status: models.PreviewServiceStatusFailed,
		err:    "exited with code 126",
	}
	// Use a generous timeout — the test asserts we return well before it.
	err := d.waitForReadiness(context.Background(), state, "server", 8080, "/health", 5*time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exited before becoming ready")
	require.Contains(t, err.Error(), "126")
}

// TestWaitForReadiness_BoundsExecPerAttempt verifies that each Exec call is
// wrapped in a per-attempt timeout, so a wedged docker daemon can't stretch
// the readiness budget. We give the loop a 2.5s overall timeout against an
// Exec that hangs forever; without per-attempt bounding the loop would never
// return because the deadline.C case can only fire between attempts.
func TestWaitForReadiness_BoundsExecPerAttempt(t *testing.T) {
	t.Parallel()
	hung := &hangingSandboxExecutor{calls: make(chan struct{}, 4)}
	d := NewDockerPreviewProvider(&mockDockerPreviewClient{}, hung, zerolog.Nop())
	state := &previewState{
		sandbox:  &agent.Sandbox{},
		services: map[string]*serviceState{},
	}
	state.services["web"] = &serviceState{
		name:   "web",
		port:   3000,
		status: models.PreviewServiceStatusStarting,
	}

	// Overall budget shorter than a single hung Exec would take if unbounded.
	// Per-attempt timeout (5s in production) is longer than this 2.5s overall
	// budget, so the test really exercises the deadline.C path between
	// attempts: the per-attempt timeout cancels the hung Exec, the loop
	// re-enters select, and deadline.C fires.
	start := time.Now()
	err := d.waitForReadiness(context.Background(), state, "web", 3000, "/", 2500*time.Millisecond)
	elapsed := time.Since(start)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timed out")
	// Generous upper bound so this isn't flaky on a loaded CI host but small
	// enough to catch the regression where the loop stalls on a hung Exec.
	require.Less(t, elapsed, 30*time.Second, "readiness loop didn't honor its timeout under a hung Exec")
}
