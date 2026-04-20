package providers

import (
	"context"
	"io"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/preview"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type mockDockerPreviewClient struct {
	createHostConfig       *container.HostConfig
	createNetworkingConfig *network.NetworkingConfig
	inspectResp            container.InspectResponse
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

	client := &mockDockerPreviewClient{
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
	provider := NewDockerPreviewProvider(client, &noopSandboxExecutor{}, zerolog.Nop())

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
	require.Equal(t, container.NetworkMode("143-sandbox-custom"), client.createHostConfig.NetworkMode, "infra containers should join the sandbox container network")
	require.Contains(t, client.createNetworkingConfig.EndpointsConfig, "143-sandbox-custom", "network aliases should be attached to the sandbox network")
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
