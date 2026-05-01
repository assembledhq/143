package providers

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/preview"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

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

// TestEnsureInfraImages_PullsAllSupported verifies the worker-boot
// pre-pull pass invokes the registry once per supported template image,
// so the first preview start is a fast inspect rather than a multi-minute
// cold pull that would blow through the HTTP server's WriteTimeout.
func TestEnsureInfraImages_PullsAllSupported(t *testing.T) {
	t.Parallel()

	cli := &mockDockerPreviewClient{
		imagesPresent:      map[string]bool{},
		imagePullPopulates: true,
	}
	provider := NewDockerPreviewProvider(cli, &noopSandboxExecutor{}, zerolog.Nop())

	provider.EnsureInfraImages(context.Background())

	require.Equal(t, len(preview.AllInfraImages()), cli.imagePullCalls, "every supported infra image should be pre-pulled")

	// Second call is a no-op: every image is already present, so we hit
	// the fast inspect path.
	cli.imagePullCalls = 0
	provider.EnsureInfraImages(context.Background())
	require.Zero(t, cli.imagePullCalls, "second pass must not re-pull images that are already present")
}

// TestEnsureInfraImages_LogsAndContinuesOnFailure verifies that pre-pull
// is best-effort: a registry failure for one image does not propagate up
// or stop the others (and definitely doesn't block server boot). The
// lazy-pull path stays as a safety net for whatever didn't pre-pull.
func TestEnsureInfraImages_LogsAndContinuesOnFailure(t *testing.T) {
	t.Parallel()

	cli := &mockDockerPreviewClient{
		imagesPresent: map[string]bool{},
		imagePullErr:  errors.New("registry unreachable"),
	}
	provider := NewDockerPreviewProvider(cli, &noopSandboxExecutor{}, zerolog.Nop())

	// Should not panic, should not block, should not return.
	provider.EnsureInfraImages(context.Background())

	require.Equal(t, len(preview.AllInfraImages()), cli.imagePullCalls, "every image should still be attempted even when the first one fails")
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

func TestGenerateInfraCredential(t *testing.T) {
	t.Parallel()
	cred, err := generateInfraCredential("db")
	require.NoError(t, err)
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

	infraCreds := map[string]preview.InfraCredential{
		"db": {
			Host:     "preview-db-abc",
			Port:     5432,
			Username: "preview_db",
			Password: "secret",
			Database: "preview_db",
		},
	}

	envs := d.buildServiceEnvs(cfg, infraCreds, nil)

	// web should have NODE_ENV + DATABASE_URL
	require.Equal(t, "development", envs["web"]["NODE_ENV"])
	require.Equal(t, "postgres://preview_db:secret@preview-db-abc:5432/preview_db", envs["web"]["DATABASE_URL"])

	// worker should have WORKER_THREADS + DATABASE_URL
	require.Equal(t, "2", envs["worker"]["WORKER_THREADS"])
	require.Equal(t, "postgres://preview_db:secret@preview-db-abc:5432/preview_db", envs["worker"]["DATABASE_URL"])
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
