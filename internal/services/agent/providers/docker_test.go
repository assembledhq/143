package providers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/sandboxauth"
)

// mockDockerClient implements DockerClient for testing.
type mockDockerClient struct {
	pingFn                 func(ctx context.Context) (types.Ping, error)
	containerListFn        func(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	containerCreateFn      func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	containerStartFn       func(ctx context.Context, containerID string, options container.StartOptions) error
	containerStopFn        func(ctx context.Context, containerID string, options container.StopOptions) error
	containerRemoveFn      func(ctx context.Context, containerID string, options container.RemoveOptions) error
	containerInspectFn     func(ctx context.Context, containerID string) (container.InspectResponse, error)
	containerStatsFn       func(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error)
	containerExecCreateFn  func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error)
	containerExecAttachFn  func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	containerExecInspectFn func(ctx context.Context, execID string) (container.ExecInspect, error)
	networkInspectFn       func(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error)
	networkCreateFn        func(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error)
}

func (m *mockDockerClient) Ping(ctx context.Context) (types.Ping, error) {
	if m.pingFn != nil {
		return m.pingFn(ctx)
	}
	return types.Ping{}, nil
}

func (m *mockDockerClient) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	if m.containerListFn != nil {
		return m.containerListFn(ctx, options)
	}
	return nil, nil
}

func (m *mockDockerClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	if m.containerCreateFn != nil {
		return m.containerCreateFn(ctx, config, hostConfig, networkConfig, platform, containerName)
	}
	return container.CreateResponse{ID: "test-container-id"}, nil
}

func (m *mockDockerClient) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	if m.containerStartFn != nil {
		return m.containerStartFn(ctx, containerID, options)
	}
	return nil
}

func (m *mockDockerClient) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	if m.containerStopFn != nil {
		return m.containerStopFn(ctx, containerID, options)
	}
	return nil
}

func (m *mockDockerClient) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	if m.containerRemoveFn != nil {
		return m.containerRemoveFn(ctx, containerID, options)
	}
	return nil
}

func (m *mockDockerClient) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	if m.containerInspectFn != nil {
		return m.containerInspectFn(ctx, containerID)
	}
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "143-sandbox",
			},
		},
	}, nil
}

func (m *mockDockerClient) ContainerStats(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error) {
	if m.containerStatsFn != nil {
		return m.containerStatsFn(ctx, containerID, stream)
	}
	return container.StatsResponseReader{Body: io.NopCloser(bytes.NewReader([]byte("{}")))}, nil
}

func (m *mockDockerClient) ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
	if m.containerExecCreateFn != nil {
		return m.containerExecCreateFn(ctx, containerID, config)
	}
	return container.ExecCreateResponse{ID: "test-exec-id"}, nil
}

func (m *mockDockerClient) ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
	if m.containerExecAttachFn != nil {
		return m.containerExecAttachFn(ctx, execID, config)
	}
	return newMockHijackedResponse(""), nil
}

func (m *mockDockerClient) ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error) {
	if m.containerExecInspectFn != nil {
		return m.containerExecInspectFn(ctx, execID)
	}
	return container.ExecInspect{ExitCode: 0}, nil
}

func (m *mockDockerClient) NetworkInspect(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error) {
	if m.networkInspectFn != nil {
		return m.networkInspectFn(ctx, networkID, options)
	}
	// Default: pretend the network already exists so HealthCheck tests that
	// don't care about the network don't have to wire create up.
	return network.Inspect{Name: networkID}, nil
}

func (m *mockDockerClient) NetworkCreate(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error) {
	if m.networkCreateFn != nil {
		return m.networkCreateFn(ctx, name, options)
	}
	return network.CreateResponse{ID: "test-net-id"}, nil
}

// mockConn implements net.Conn for testing HijackedResponse.
type mockConn struct {
	net.Conn
	reader *bytes.Reader
	closed bool
}

func newMockConn(data string) *mockConn {
	return &mockConn{reader: bytes.NewReader([]byte(data))}
}

func (c *mockConn) Read(p []byte) (int, error)         { return c.reader.Read(p) }
func (c *mockConn) Write(p []byte) (int, error)        { return len(p), nil }
func (c *mockConn) Close() error                       { c.closed = true; return nil }
func (c *mockConn) LocalAddr() net.Addr                { return nil }
func (c *mockConn) RemoteAddr() net.Addr               { return nil }
func (c *mockConn) SetDeadline(t time.Time) error      { return nil }
func (c *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *mockConn) SetWriteDeadline(t time.Time) error { return nil }

// newMockHijackedResponse creates a HijackedResponse with mock data.
// The data should be in Docker's multiplexed stream format, or empty.
func newMockHijackedResponse(data string) types.HijackedResponse {
	conn := newMockConn(data)
	return types.HijackedResponse{
		Conn:   conn,
		Reader: bufio.NewReader(conn),
	}
}

// capturingConn wraps mockConn and records every Write so tests can assert
// on the bytes a handle sent to the agent's stdin (e.g. 0x1b for Escape).
type capturingConn struct {
	*mockConn
	mu      sync.Mutex
	written []byte
}

func newCapturingConn() *capturingConn {
	return &capturingConn{mockConn: newMockConn("")}
}

func (c *capturingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.written = append(c.written, p...)
	c.mu.Unlock()
	return len(p), nil
}

func (c *capturingConn) Captured() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, len(c.written))
	copy(out, c.written)
	return out
}

// CloseWrite is what the handle's CloseInput calls to signal EOF on stdin.
// Implementing it lets capturingConn satisfy the closeWriter interface in
// docker_handle.go without tearing down stdout.
func (c *capturingConn) CloseWrite() error { return nil }

// newCapturingHijackedResponse wires a capturingConn into a HijackedResponse
// so tests can both control stdout (none, here) and inspect stdin writes.
func newCapturingHijackedResponse() (*capturingConn, types.HijackedResponse) {
	conn := newCapturingConn()
	return conn, types.HijackedResponse{
		Conn:   conn,
		Reader: bufio.NewReader(conn),
	}
}

// dockerStreamFrame builds a single Docker multiplexed stream frame.
// stream is 1 (stdout) or 2 (stderr); the payload is wrapped with the
// 8-byte header stdcopy expects.
func dockerStreamFrame(stream byte, payload []byte) []byte {
	if len(payload) > 0xFFFFFFFF {
		panic("dockerStreamFrame: payload exceeds uint32 length")
	}
	frame := make([]byte, 8+len(payload))
	frame[0] = stream
	// bytes 1..3 are padding (zero)
	size := uint32(len(payload))
	frame[4] = byte(size >> 24)
	frame[5] = byte(size >> 16)
	frame[6] = byte(size >> 8)
	frame[7] = byte(size)
	copy(frame[8:], payload)
	return frame
}

// dockerMultiplexed concatenates one stdout frame and one stderr frame —
// the typical shape returned by `docker exec` once the tool has run.
func dockerMultiplexed(stdout, stderr []byte) string {
	out := dockerStreamFrame(1, stdout)
	if len(stderr) > 0 {
		out = append(out, dockerStreamFrame(2, stderr)...)
	}
	return string(out)
}

func newTestLogger() zerolog.Logger {
	return zerolog.Nop()
}

func TestDockerProvider_HealthCheck(t *testing.T) {
	t.Parallel()

	t.Run("runc pings but skips container test", func(t *testing.T) {
		t.Parallel()

		var pingCalled bool
		mock := &mockDockerClient{}
		mock.pingFn = func(ctx context.Context) (types.Ping, error) {
			pingCalled = true
			return types.Ping{}, nil
		}
		// ContainerCreate should not be called for runc runtime.
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			t.Fatal("ContainerCreate should not be called for runc runtime")
			return container.CreateResponse{}, nil
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runc"))

		err := p.HealthCheck(context.Background())
		require.NoError(t, err, "HealthCheck should return nil for runc runtime")
		require.True(t, pingCalled, "Ping should be called to verify Docker connectivity")
	})

	t.Run("ping failure returns error", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.pingFn = func(ctx context.Context) (types.Ping, error) {
			return types.Ping{}, fmt.Errorf("Cannot connect to the Docker daemon")
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runc"))

		err := p.HealthCheck(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "cannot connect to Docker daemon")
	})

	t.Run("success with non-runc runtime", func(t *testing.T) {
		t.Parallel()

		var createCalled, startCalled, removeCalled bool

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			createCalled = true
			require.Equal(t, "busybox:latest", config.Image, "health check should use busybox image")
			require.Equal(t, []string(config.Cmd), []string{"echo", "runtime-ok"}, "health check should run echo command")
			require.Equal(t, "runsc", hostConfig.Runtime, "health check should use configured runtime")
			require.NotNil(t, hostConfig.Resources.PidsLimit)
			require.Equal(t, int64(64), *hostConfig.Resources.PidsLimit)
			return container.CreateResponse{ID: "health-check-container"}, nil
		}
		mock.containerStartFn = func(ctx context.Context, containerID string, options container.StartOptions) error {
			startCalled = true
			require.Equal(t, "health-check-container", containerID)
			return nil
		}
		mock.containerInspectFn = func(ctx context.Context, containerID string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					State: &container.State{Running: false, ExitCode: 0},
					HostConfig: &container.HostConfig{
						NetworkMode: "143-sandbox",
					},
				},
			}, nil
		}
		mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
			removeCalled = true
			require.Equal(t, "health-check-container", containerID)
			require.True(t, options.Force, "cleanup should force-remove")
			return nil
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runsc"))

		err := p.HealthCheck(context.Background())
		require.NoError(t, err, "HealthCheck should succeed")
		require.True(t, createCalled, "ContainerCreate should have been called")
		require.True(t, startCalled, "ContainerStart should have been called")
		require.True(t, removeCalled, "ContainerRemove should have been called for cleanup")
	})

	t.Run("returns error when container create fails", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			return container.CreateResponse{}, fmt.Errorf("runtime not found")
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runsc"))

		err := p.HealthCheck(context.Background())
		require.Error(t, err, "HealthCheck should return an error")
		require.Contains(t, err.Error(), "health check")
		require.Contains(t, err.Error(), "failed to create test container")
		require.Contains(t, err.Error(), "runtime not found")
	})

	t.Run("returns error when container start fails and cleans up", func(t *testing.T) {
		t.Parallel()

		var removeCalled bool

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "health-fail"}, nil
		}
		mock.containerStartFn = func(ctx context.Context, containerID string, options container.StartOptions) error {
			return fmt.Errorf("OCI runtime create failed")
		}
		mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
			removeCalled = true
			require.Equal(t, "health-fail", containerID)
			return nil
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runsc"))

		err := p.HealthCheck(context.Background())
		require.Error(t, err, "HealthCheck should return an error")
		require.Contains(t, err.Error(), "health check")
		require.Contains(t, err.Error(), "failed to start test container")
		require.True(t, removeCalled, "ContainerRemove should be called for cleanup even on start failure")
	})

	t.Run("requires disk quota support when configured", func(t *testing.T) {
		t.Parallel()

		var capturedStorageOpt map[string]string

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedStorageOpt = hostConfig.StorageOpt
			return container.CreateResponse{ID: "quota-check"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runc"), WithRequireDiskQuota(true))

		err := p.HealthCheck(context.Background())
		require.NoError(t, err, "HealthCheck should pass when the quota probe container can be created")
		require.Equal(t, "1G", capturedStorageOpt["size"], "HealthCheck should create a tiny quota probe when disk quota enforcement is required")
	})

	t.Run("fails health check when required disk quota is unsupported", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			return container.CreateResponse{}, fmt.Errorf("storage-opt is supported only for overlay over xfs with pquota")
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runc"), WithRequireDiskQuota(true))

		err := p.HealthCheck(context.Background())
		require.ErrorIs(t, err, ErrDiskQuotaUnsupported, "HealthCheck should fail when required disk quota support is missing")
	})
}

func TestDockerProvider_ListManagedSandboxes(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	var capturedOptions container.ListOptions
	mock := &mockDockerClient{
		containerListFn: func(_ context.Context, options container.ListOptions) ([]container.Summary, error) {
			capturedOptions = options
			return []container.Summary{
				{
					ID:      "container-1",
					Created: createdAt.Add(-time.Hour).Unix(),
					Labels: map[string]string{
						SandboxLabelManaged:   "true",
						SandboxLabelType:      "sandbox",
						SandboxLabelSessionID: "session-1",
						SandboxLabelOrgID:     "org-1",
						SandboxLabelPurpose:   "agent_run",
						SandboxLabelCreatedAt: createdAt.Format(time.RFC3339Nano),
					},
				},
				{
					ID:      "container-2",
					Created: createdAt.Add(-2 * time.Hour).Unix(),
					Labels: map[string]string{
						sandboxLabelLegacySandbox:   "true",
						sandboxLabelLegacySessionID: "session-2",
						sandboxLabelLegacyOrgID:     "org-2",
						sandboxLabelLegacyPurpose:   "preview",
					},
				},
				{
					ID:      "container-3",
					Created: createdAt.Add(-3 * time.Hour).Unix(),
					Image:   "ghcr.io/assembledhq/143-sandbox:legacy",
					Labels:  map[string]string{},
					NetworkSettings: &container.NetworkSettingsSummary{
						Networks: map[string]*network.EndpointSettings{
							"143-sandbox": {},
						},
					},
				},
				{
					ID:      "container-4",
					Created: createdAt.Add(-4 * time.Hour).Unix(),
					Image:   "143-sandbox-dns:local",
					Labels:  map[string]string{},
					NetworkSettings: &container.NetworkSettingsSummary{
						Networks: map[string]*network.EndpointSettings{
							"143-sandbox": {},
						},
					},
				},
				{
					ID:      "container-5",
					Created: createdAt.Add(-5 * time.Hour).Unix(),
					Image:   "ghcr.io/assembledhq/143-sandbox:legacy",
					Labels:  map[string]string{},
					NetworkSettings: &container.NetworkSettingsSummary{
						Networks: map[string]*network.EndpointSettings{
							"other-network": {},
						},
					},
				},
			}, nil
		},
	}
	p := NewDockerProvider(mock, newTestLogger())

	containers, err := p.ListManagedSandboxes(context.Background())
	require.NoError(t, err, "ListManagedSandboxes should return Docker-managed sandbox containers")
	require.True(t, capturedOptions.All, "ListManagedSandboxes should include stopped containers")
	require.Empty(t, capturedOptions.Filters.Get("label"), "ListManagedSandboxes should filter in-process so both current and legacy label schemes are eligible")
	require.Equal(t, []agent.ManagedSandboxContainer{
		{
			ID:        "container-1",
			SessionID: "session-1",
			OrgID:     "org-1",
			Purpose:   "agent_run",
			CreatedAt: createdAt,
		},
		{
			ID:        "container-2",
			SessionID: "session-2",
			OrgID:     "org-2",
			Purpose:   "preview",
			CreatedAt: createdAt.Add(-2 * time.Hour),
		},
		{
			ID:        "container-3",
			SessionID: "",
			OrgID:     "",
			Purpose:   "",
			CreatedAt: createdAt.Add(-3 * time.Hour),
		},
	}, containers, "ListManagedSandboxes should map Docker summaries into GC metadata, include legacy image-based sandboxes only on the sandbox network, skip DNS sidecars, and fall back to Docker creation time")
}

func TestDockerProvider_EnsureNetwork(t *testing.T) {
	t.Parallel()

	t.Run("skips create when network already exists", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.networkInspectFn = func(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error) {
			require.Equal(t, "143-sandbox", networkID)
			return network.Inspect{Name: networkID}, nil
		}
		mock.networkCreateFn = func(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error) {
			t.Fatal("NetworkCreate should not be called when network already exists")
			return network.CreateResponse{}, nil
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runc"))

		err := p.HealthCheck(context.Background())
		require.NoError(t, err)
	})

	t.Run("creates missing network on health check", func(t *testing.T) {
		t.Parallel()

		var createCalled bool
		mock := &mockDockerClient{}
		mock.networkInspectFn = func(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{}, cerrdefs.ErrNotFound
		}
		mock.networkCreateFn = func(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error) {
			createCalled = true
			require.Equal(t, "143-sandbox", name)
			require.Equal(t, "bridge", options.Driver)
			require.Equal(t, "143", options.Labels["managed-by"])
			require.NotContains(t, options.Options, "com.docker.network.bridge.enable_icc",
				"sandbox network must leave bridge ICC at Docker's default so sandboxes can reach sandbox-dns")
			return network.CreateResponse{ID: "net-id"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runc"))

		err := p.HealthCheck(context.Background())
		require.NoError(t, err)
		require.True(t, createCalled, "NetworkCreate should be called when network is missing")
	})

	t.Run("treats conflict on create as success (concurrent workers)", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.networkInspectFn = func(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{}, cerrdefs.ErrNotFound
		}
		mock.networkCreateFn = func(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error) {
			return network.CreateResponse{}, cerrdefs.ErrConflict
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runc"))

		err := p.HealthCheck(context.Background())
		require.NoError(t, err, "conflict on create should be treated as success")
	})

	t.Run("propagates non-notfound inspect error", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.networkInspectFn = func(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{}, fmt.Errorf("docker daemon is melting")
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runc"))

		err := p.HealthCheck(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "inspect network")
		require.Contains(t, err.Error(), "melting")
	})

	t.Run("propagates create error", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.networkInspectFn = func(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error) {
			return network.Inspect{}, cerrdefs.ErrNotFound
		}
		mock.networkCreateFn = func(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error) {
			return network.CreateResponse{}, fmt.Errorf("no bridge for you")
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRuntime("runc"))

		err := p.HealthCheck(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "create network")
		require.Contains(t, err.Error(), "no bridge for you")
	})
}

func TestDockerProvider_Name(t *testing.T) {
	t.Parallel()

	cli := &mockDockerClient{}
	p := NewDockerProvider(cli, newTestLogger())
	require.Equal(t, "docker", p.Name(), "provider name should be 'docker'")
}

func TestDockerProvider_CountLiveSandboxes(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{}
	mock.containerListFn = func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
		require.Contains(t, options.Filters.Get("network"), "143-sandbox", "CountLiveSandboxes should scope counting to the local sandbox network")
		return []container.Summary{
			{
				ID:     "labeled-sandbox",
				Image:  "busybox:latest",
				Labels: map[string]string{"143.sandbox": "true"},
			},
			{
				ID:     "legacy-sandbox",
				Image:  "ghcr.io/assembledhq/143-sandbox:latest",
				Labels: map[string]string{},
			},
			{
				ID:     "dns-sidecar",
				Image:  "143-sandbox-dns:local",
				Labels: map[string]string{},
			},
			{
				ID:     "worker",
				Image:  "ghcr.io/assembledhq/143:latest",
				Labels: map[string]string{},
			},
		}, nil
	}
	p := NewDockerProvider(mock, newTestLogger())

	count, err := p.CountLiveSandboxes(context.Background())

	require.NoError(t, err, "CountLiveSandboxes should return the Docker count without error")
	require.Equal(t, 2, count, "CountLiveSandboxes should include labeled and legacy sandbox containers but skip sidecars")
}

func TestDockerProvider_CountLiveSandboxesListError(t *testing.T) {
	t.Parallel()

	listErr := errors.New("daemon unavailable")
	mock := &mockDockerClient{}
	mock.containerListFn = func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
		return nil, listErr
	}
	p := NewDockerProvider(mock, newTestLogger())

	count, err := p.CountLiveSandboxes(context.Background())

	require.ErrorIs(t, err, listErr, "CountLiveSandboxes should wrap Docker list failures")
	require.Equal(t, 0, count, "CountLiveSandboxes should not return a partial count on list failure")
}

func TestDockerProvider_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates container with valid config and security hardening", func(t *testing.T) {
		t.Parallel()

		var capturedConfig *container.Config
		var capturedHostConfig *container.HostConfig

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedConfig = config
			capturedHostConfig = hostConfig
			return container.CreateResponse{ID: "abc123"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		sb, err := p.Create(context.Background(), agent.DefaultSandboxConfig())
		require.NoError(t, err, "Create should not return an error")

		// Verify container config
		require.Equal(t, "sandbox", capturedConfig.User, "container should run as non-root user")
		require.Equal(t, "/home/sandbox", capturedConfig.WorkingDir, "container WorkingDir should be HomeDir (sandbox-owned, always present) so the OCI runtime doesn't auto-create cfg.WorkDir as root")

		// Verify security hardening
		require.Equal(t, "runsc", capturedHostConfig.Runtime, "container should use gVisor runtime")
		require.Equal(t, []string(capturedHostConfig.CapDrop), []string{"ALL"}, "container should drop all capabilities")
		require.Empty(t, capturedHostConfig.CapAdd, "container should not add back any caps — sudo is gone from the image and bootstrap runs unprivileged as the sandbox user")
		require.Empty(t, capturedHostConfig.SecurityOpt, "container should not set security options — no-new-privileges is moot without sudo, keep it unset for parity with the pre-sudo-removal behavior")
		require.False(t, capturedHostConfig.ReadonlyRootfs, "container should have writable rootfs for package installation")
		require.Contains(t, capturedHostConfig.Tmpfs, "/tmp", "container should have tmpfs at /tmp")
		require.Contains(t, capturedHostConfig.Tmpfs, "/var/tmp", "container should have exec-allowed tmpfs at /var/tmp for scratch binaries")
		require.NotContains(t, capturedHostConfig.Tmpfs, "/workspace", "workspace should not be a tmpfs (lives on writable rootfs)")
		require.Equal(t, "10G", capturedHostConfig.StorageOpt["size"], "container should have 10GB disk quota")

		// Verify tmpfs mount options
		require.Contains(t, capturedHostConfig.Tmpfs["/tmp"], "noexec", "/tmp tmpfs should be noexec")
		require.Contains(t, capturedHostConfig.Tmpfs["/var/tmp"], "exec", "/var/tmp tmpfs should allow exec so `go test` can run compiled binaries")
		require.Contains(t, capturedConfig.Env, "TMPDIR="+defaultScratchDir,
			"TMPDIR must be redirected off /tmp so tools writing executables to tempdir can exec them")
		require.Contains(t, capturedConfig.Env, "GOTMPDIR="+defaultScratchDir,
			"GOTMPDIR must be redirected off /tmp so `go test` can exec compiled test binaries")

		// Verify resource limits
		require.Equal(t, int64(2e9), capturedHostConfig.Resources.NanoCPUs, "container should have 2 CPU cores")
		require.Equal(t, int64(3072*1024*1024), capturedHostConfig.Resources.Memory, "container should have 3GB memory")
		require.NotNil(t, capturedHostConfig.Resources.PidsLimit, "container should have PID limit")
		require.Equal(t, int64(256), *capturedHostConfig.Resources.PidsLimit, "container should have PID limit of 256")

		require.Equal(t, []string{"1.1.1.1", "8.8.8.8"}, capturedHostConfig.DNS,
			"without resolv.conf bind mount, fall back to HostConfig.DNS (only effective on default bridge / runc)")
		require.Empty(t, capturedHostConfig.Mounts,
			"no bind mounts should be added when WithResolvConf is not set")

		// Verify sandbox result
		require.Equal(t, "abc123", sb.ID, "sandbox ID should match container ID")
		require.Equal(t, "docker", sb.Provider, "sandbox provider should be 'docker'")
		require.Equal(t, "/workspace", sb.WorkDir, "sandbox workdir should be '/workspace'")
		require.Equal(t, "runsc", sb.Metadata["runtime"], "sandbox metadata should include runtime")
	})

	t.Run("labels managed sandbox containers with tracing metadata", func(t *testing.T) {
		t.Parallel()

		var capturedLabels map[string]string

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedLabels = config.Labels
			return container.CreateResponse{ID: "labeled"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		cfg := agent.DefaultSandboxConfig()
		cfg.SessionID = "session-123"
		cfg.OrgID = "org-456"
		cfg.Purpose = "agent_run"
		_, err := p.Create(context.Background(), cfg)
		require.NoError(t, err, "Create should succeed for a labeled sandbox")

		require.Equal(t, "143", capturedLabels["managed-by"], "sandbox containers should retain the legacy managed-by label")
		require.Equal(t, "true", capturedLabels[sandboxLabelLegacySandbox], "sandbox containers should be labeled for live capacity checks")
		require.Equal(t, "session-123", capturedLabels[sandboxLabelLegacySessionID], "legacy sandbox labels should include the session id when present")
		require.Equal(t, "org-456", capturedLabels[sandboxLabelLegacyOrgID], "legacy sandbox labels should include the org id when present")
		require.Equal(t, "agent_run", capturedLabels[sandboxLabelLegacyPurpose], "legacy sandbox labels should include the sandbox purpose")
		require.Equal(t, "true", capturedLabels[SandboxLabelManaged], "sandbox containers should be labeled as 143-managed")
		require.Equal(t, "sandbox", capturedLabels[SandboxLabelType], "sandbox containers should carry a type label for host-local GC")
		require.Equal(t, "session-123", capturedLabels[SandboxLabelSessionID], "sandbox labels should include the session id when present")
		require.Equal(t, "org-456", capturedLabels[SandboxLabelOrgID], "sandbox labels should include the org id when present")
		require.Equal(t, "agent_run", capturedLabels[SandboxLabelPurpose], "sandbox labels should include the sandbox purpose")
		require.NotEmpty(t, capturedLabels[SandboxLabelCreatedAt], "sandbox labels should include an RFC3339 creation timestamp for GC age decisions")
	})

	t.Run("returns error when required StorageOpt unsupported", func(t *testing.T) {
		t.Parallel()

		callCount := 0

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			callCount++
			return container.CreateResponse{}, fmt.Errorf("--storage-opt is supported only for overlay over xfs with 'pquota' mount option")
		}
		p := NewDockerProvider(mock, newTestLogger(), WithRequireDiskQuota(true))

		_, err := p.Create(context.Background(), agent.DefaultSandboxConfig())
		require.ErrorIs(t, err, ErrDiskQuotaUnsupported, "Create should fail closed when disk quota is required but Docker rejects StorageOpt")
		require.Equal(t, 1, callCount, "Create should not retry without StorageOpt when disk quota is required")
	})

	t.Run("falls back when StorageOpt unsupported and quota is not required", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		var lastHostConfig container.HostConfig

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			callCount++
			lastHostConfig = *hostConfig
			if callCount == 1 {
				return container.CreateResponse{}, fmt.Errorf("--storage-opt is supported only for overlay over xfs with 'pquota' mount option")
			}
			return container.CreateResponse{ID: "fallback-ok"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		sb, err := p.Create(context.Background(), agent.DefaultSandboxConfig())
		require.NoError(t, err, "Create should succeed after fallback")
		require.Equal(t, "fallback-ok", sb.ID)
		require.Equal(t, 2, callCount, "should have retried without StorageOpt")
		require.Empty(t, lastHostConfig.StorageOpt, "retry should not include StorageOpt")
	})

	t.Run("skips StorageOpt when DiskLimitGB is zero", func(t *testing.T) {
		t.Parallel()

		var capturedHostConfig *container.HostConfig

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedHostConfig = hostConfig
			return container.CreateResponse{ID: "no-disk-limit"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		cfg := agent.DefaultSandboxConfig()
		cfg.DiskLimitGB = 0
		_, err := p.Create(context.Background(), cfg)
		require.NoError(t, err)
		require.Empty(t, capturedHostConfig.StorageOpt, "StorageOpt should not be set when DiskLimitGB is 0")
	})

	t.Run("injects env vars into container", func(t *testing.T) {
		t.Parallel()

		var capturedConfig *container.Config

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedConfig = config
			return container.CreateResponse{ID: "env-test"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		cfg := agent.DefaultSandboxConfig()
		cfg.Env = map[string]string{
			"ANTHROPIC_API_KEY": "sk-ant-test-key",
			"OPENAI_API_KEY":    "sk-test-openai-key",
		}
		sb, err := p.Create(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, "env-test", sb.ID)

		require.Contains(t, capturedConfig.Env, "ANTHROPIC_API_KEY=sk-ant-test-key")
		require.Contains(t, capturedConfig.Env, "OPENAI_API_KEY=sk-test-openai-key")
		require.Contains(t, capturedConfig.Env, "TMPDIR="+defaultScratchDir,
			"TMPDIR should be injected so tools dropping executables in tempdir can exec them")
		require.Contains(t, capturedConfig.Env, "GOTMPDIR="+defaultScratchDir,
			"GOTMPDIR should be injected so `go test` works despite /tmp being noexec")
	})

	t.Run("preserves caller-supplied GOTMPDIR override", func(t *testing.T) {
		t.Parallel()

		var capturedConfig *container.Config

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedConfig = config
			return container.CreateResponse{ID: "gotmpdir-override"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		cfg := agent.DefaultSandboxConfig()
		cfg.Env = map[string]string{"GOTMPDIR": "/workspace/.gotmp"}
		_, err := p.Create(context.Background(), cfg)
		require.NoError(t, err)
		require.Contains(t, capturedConfig.Env, "GOTMPDIR=/workspace/.gotmp")
		require.NotContains(t, capturedConfig.Env, "GOTMPDIR="+defaultScratchDir,
			"caller-supplied GOTMPDIR should not be overridden")
	})

	t.Run("preserves caller-supplied TMPDIR override", func(t *testing.T) {
		t.Parallel()

		var capturedConfig *container.Config

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedConfig = config
			return container.CreateResponse{ID: "tmpdir-override"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		cfg := agent.DefaultSandboxConfig()
		cfg.Env = map[string]string{"TMPDIR": "/workspace/.tmp"}
		_, err := p.Create(context.Background(), cfg)
		require.NoError(t, err)
		require.Contains(t, capturedConfig.Env, "TMPDIR=/workspace/.tmp")
		require.NotContains(t, capturedConfig.Env, "TMPDIR="+defaultScratchDir,
			"caller-supplied TMPDIR should not be overridden")
	})

	t.Run("handles nil env map gracefully", func(t *testing.T) {
		t.Parallel()

		var capturedConfig *container.Config

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedConfig = config
			return container.CreateResponse{ID: "no-env"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		cfg := agent.DefaultSandboxConfig()
		// cfg.Env is nil by default
		sb, err := p.Create(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, "no-env", sb.ID)
		require.ElementsMatch(t, []string{"TMPDIR=" + defaultScratchDir, "GOTMPDIR=" + defaultScratchDir}, capturedConfig.Env,
			"with nil cfg.Env, only the auto-injected TMPDIR/GOTMPDIR should be present")
	})

	t.Run("bind-mounts resolv.conf and clears DNS when WithResolvConf is set", func(t *testing.T) {
		t.Parallel()

		var capturedHostConfig *container.HostConfig

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedHostConfig = hostConfig
			return container.CreateResponse{ID: "resolv-mount"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger(), WithResolvConf("/etc/143/sandbox-resolv.conf"))

		_, err := p.Create(context.Background(), agent.DefaultSandboxConfig())
		require.NoError(t, err)

		require.Empty(t, capturedHostConfig.DNS,
			"HostConfig.DNS must be empty when bind-mounting resolv.conf — leaving it set would let Docker re-inject 127.0.0.11 as upstream-forwarder on user-defined networks")
		require.Len(t, capturedHostConfig.Mounts, 1, "exactly one bind mount expected")
		m := capturedHostConfig.Mounts[0]
		require.Equal(t, mount.TypeBind, m.Type)
		require.Equal(t, "/etc/143/sandbox-resolv.conf", m.Source)
		require.Equal(t, "/etc/resolv.conf", m.Target)
		require.True(t, m.ReadOnly, "resolv.conf mount must be read-only — sandboxes shouldn't mutate host DNS config")
	})

	t.Run("bind-mounts the auth socket directory when configured", func(t *testing.T) {
		t.Parallel()

		var capturedHostConfig *container.HostConfig

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedHostConfig = hostConfig
			return container.CreateResponse{ID: "auth-socket"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		cfg := agent.DefaultSandboxConfig()
		cfg.AuthSocketPath = "/var/run/143-auth/sessions/session-123/sock"

		_, err := p.Create(context.Background(), cfg)
		require.NoError(t, err, "Create should succeed when an auth socket path is configured")

		require.Len(t, capturedHostConfig.Mounts, 1, "only the auth socket directory mount is expected in the default config")
		m := capturedHostConfig.Mounts[0]
		require.Equal(t, mount.TypeBind, m.Type, "auth socket should be bind-mounted")
		require.Equal(t, filepath.Dir(cfg.AuthSocketPath), m.Source, "mount source should be the host directory containing the live socket")
		require.Equal(t, sandboxauth.SandboxSocketDir, m.Target, "mount target should be the fixed in-sandbox auth directory")
	})

	t.Run("returns error when container create fails", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			return container.CreateResponse{}, fmt.Errorf("image not found")
		}
		p := NewDockerProvider(mock, newTestLogger())

		_, err := p.Create(context.Background(), agent.DefaultSandboxConfig())
		require.Error(t, err, "Create should return an error")
		require.Contains(t, err.Error(), "create container", "error should contain expected message")
	})

	t.Run("cleans up on start failure", func(t *testing.T) {
		t.Parallel()

		var removedID string
		var removeForce bool

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "fail-start"}, nil
		}
		mock.containerStartFn = func(ctx context.Context, containerID string, options container.StartOptions) error {
			return fmt.Errorf("start failure")
		}
		mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
			removedID = containerID
			removeForce = options.Force
			return nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		_, err := p.Create(context.Background(), agent.DefaultSandboxConfig())
		require.Error(t, err, "Create should return an error")
		require.Contains(t, err.Error(), "start container", "error should contain expected message")
		require.Equal(t, "fail-start", removedID, "should remove the created container")
		require.True(t, removeForce, "should force-remove on start failure")
	})

	t.Run("copies tracing fields onto returned Sandbox", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		var createdCfg *container.Config
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			createdCfg = config
			return container.CreateResponse{ID: "traced"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		cfg := agent.DefaultSandboxConfig()
		cfg.SessionID = "sess-123"
		cfg.OrgID = "org-456"
		cfg.Purpose = "agent_run"

		sb, err := p.Create(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, "sess-123", sb.SessionID, "SessionID should be copied from config")
		require.Equal(t, "org-456", sb.OrgID, "OrgID should be copied from config")
		require.Equal(t, "agent_run", sb.Purpose, "Purpose should be copied from config")
		require.Equal(t, "true", createdCfg.Labels["143.sandbox"], "created containers should be labeled as 143 sandboxes for capacity and observability")
		require.Equal(t, "sess-123", createdCfg.Labels["143.session_id"], "created containers should carry session ID labels")
		require.Equal(t, "org-456", createdCfg.Labels["143.org_id"], "created containers should carry org ID labels")
		require.Equal(t, "agent_run", createdCfg.Labels["143.purpose"], "created containers should carry purpose labels")
	})

	t.Run("bootstrap runs as sandbox user with no sudo or chown", func(t *testing.T) {
		t.Parallel()

		// Bootstrap must NOT require root: sudo's setuid bit is stripped under
		// gVisor/nosuid, and Docker exec with User="root" + CapDrop=ALL has
		// been observed to fail `mkdir -p /home/sandbox` with Permission
		// denied on some runtimes. Anchoring the container's WorkingDir at
		// HomeDir (baked in by `useradd -m sandbox`, owned by sandbox:sandbox)
		// lets the sandbox user create the per-session workdir itself.
		var execCfgs []container.ExecOptions
		var createdCfg *container.Config

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			createdCfg = config
			return container.CreateResponse{ID: "bootstrap-sandbox"}, nil
		}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
			execCfgs = append(execCfgs, config)
			return container.ExecCreateResponse{ID: fmt.Sprintf("exec-%d", len(execCfgs))}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		_, err := p.Create(context.Background(), agent.DefaultSandboxConfig())
		require.NoError(t, err)

		require.Equal(t, "/home/sandbox", createdCfg.WorkingDir, "container WorkingDir must be HomeDir so the OCI runtime doesn't auto-create a root-owned cfg.WorkDir")

		require.Len(t, execCfgs, 1, "Create should issue exactly one bootstrap exec")
		boot := execCfgs[0]
		require.Empty(t, boot.User, "bootstrap exec must run as the container's default user (sandbox), not root")
		require.Len(t, boot.Cmd, 3, "bootstrap cmd should be wrapped by sh -c")
		require.NotContains(t, boot.Cmd[2], "sudo", "bootstrap shell cmd must not invoke sudo — the setuid bit is stripped under gVisor/no-new-privileges")
		require.NotContains(t, boot.Cmd[2], "chown", "bootstrap shell cmd must not invoke chown — sandbox already owns the created dir and chown under CapDrop=ALL fails without CAP_CHOWN")
		require.Contains(t, boot.Cmd[2], "mkdir -p", "bootstrap must still create the workdir idempotently")
	})

	t.Run("falls back to / when HomeDir is unset", func(t *testing.T) {
		t.Parallel()

		var createdCfg *container.Config
		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			createdCfg = config
			return container.CreateResponse{ID: "no-home"}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		cfg := agent.DefaultSandboxConfig()
		cfg.HomeDir = ""
		_, err := p.Create(context.Background(), cfg)
		require.NoError(t, err)

		require.Equal(t, "/", createdCfg.WorkingDir, "callers that omit HomeDir should anchor at / rather than an empty WorkingDir (which the OCI runtime would reject)")
	})

	t.Run("cleans up on bootstrap workdir failure", func(t *testing.T) {
		t.Parallel()

		var removedID string
		var removeForce bool

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "bootstrap-fail"}, nil
		}
		// ContainerStart succeeds (default). Bootstrap exec runs, but inspect
		// reports a non-zero exit code so the bootstrap branch in Create trips.
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 1}, nil
		}
		mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
			removedID = containerID
			removeForce = options.Force
			return nil
		}
		p := NewDockerProvider(mock, newTestLogger())

		_, err := p.Create(context.Background(), agent.DefaultSandboxConfig())
		require.Error(t, err, "Create should return an error when bootstrap fails")
		require.Contains(t, err.Error(), "bootstrap workdir", "error should identify bootstrap failure")
		require.Equal(t, "bootstrap-fail", removedID, "should force-remove the container after bootstrap failure")
		require.True(t, removeForce, "bootstrap failure cleanup should force-remove")
	})

	t.Run("configLogger tolerates partial tracing fields", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerCreateFn = func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			return container.CreateResponse{}, fmt.Errorf("boom")
		}
		p := NewDockerProvider(mock, newTestLogger())

		// Only OrgID set — SessionID/Purpose empty. The configLogger path must
		// not panic when some fields are empty; create-time failure still hits
		// the logger via the storage-opt fallback branch.
		cfg := agent.DefaultSandboxConfig()
		cfg.OrgID = "org-only"

		_, err := p.Create(context.Background(), cfg)
		require.Error(t, err)
	})
}

func TestDockerProvider_ScopedLogger(t *testing.T) {
	t.Parallel()

	// scopedLogger is exercised by every post-create lifecycle call (Destroy,
	// CloneRepo, Exec, Snapshot, Restore, ExecStream). Drive Destroy with a
	// fully-populated Sandbox to cover the three conditional Str() branches.
	t.Run("Destroy with populated tracing fields uses scoped logger", func(t *testing.T) {
		t.Parallel()

		var stopCalled, removeCalled bool
		mock := &mockDockerClient{}
		mock.containerStopFn = func(ctx context.Context, containerID string, options container.StopOptions) error {
			stopCalled = true
			return nil
		}
		mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
			removeCalled = true
			return nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{
			ID:        "scoped-container",
			Provider:  "docker",
			WorkDir:   "/workspace",
			SessionID: "sess-1",
			OrgID:     "org-1",
			Purpose:   "agent_run",
		}

		err := p.Destroy(context.Background(), sb)
		require.NoError(t, err)
		require.True(t, stopCalled)
		require.True(t, removeCalled)
	})

	t.Run("Destroy with empty tracing fields skips optional log keys", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		// Stop fails so the log.Warn() branch in Destroy is exercised even
		// when SessionID/OrgID/Purpose are empty — the scopedLogger should
		// still produce a valid zerolog.Logger.
		mock.containerStopFn = func(ctx context.Context, containerID string, options container.StopOptions) error {
			return fmt.Errorf("already stopped")
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "bare-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.Destroy(context.Background(), sb)
		require.NoError(t, err)
	})
}

func TestDockerProvider_Snapshot(t *testing.T) {
	t.Parallel()

	t.Run("tars workdir plus agent state dirs under HomeDir", func(t *testing.T) {
		t.Parallel()

		var gotCmd []string

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
			gotCmd = config.Cmd
			return container.ExecCreateResponse{ID: "snap-exec"}, nil
		}
		// Default attach returns empty data; stdcopy returns EOF and the pipe
		// closes cleanly without blocking the test.
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{
			ID:       "snap-container",
			Provider: "docker",
			WorkDir:  "/home/sandbox/backend",
			HomeDir:  "/home/sandbox",
		}

		rc, err := p.Snapshot(context.Background(), sb)
		require.NoError(t, err)
		require.NotNil(t, rc)
		_, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()

		require.Len(t, gotCmd, 3, "Snapshot exec should be sh -c <cmd>")
		tarCmd := gotCmd[2]
		require.Contains(t, tarCmd, "'home/sandbox/backend'", "tar should include the WorkDir relative path")
		require.Contains(t, tarCmd, "'home/sandbox/.claude'", "tar should include the .claude dir under HomeDir")
		require.Contains(t, tarCmd, "'home/sandbox/.codex'", "tar should include the .codex dir under HomeDir")
		require.Contains(t, tarCmd, "'home/sandbox/.gemini'", "tar should include the .gemini dir under HomeDir")
		require.NotContains(t, tarCmd, "2>/dev/null", "tar stderr must not be silenced — we capture it for diagnostics")
	})

	t.Run("returns error from Read when tar exits non-zero", func(t *testing.T) {
		t.Parallel()

		// Tar wrote one stdout chunk before failing, plus a stderr message.
		// The pipe should yield the stdout bytes but then surface the failure
		// to the caller — the "we silently uploaded a partial archive" bug.
		stdoutPayload := []byte("partial-archive-bytes")
		stderrPayload := []byte("tar: /workspace/missing: Cannot stat: No such file or directory\n")

		mock := &mockDockerClient{}
		mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
			return newMockHijackedResponse(dockerMultiplexed(stdoutPayload, stderrPayload)), nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{Running: false, ExitCode: 2}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "snap-container", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

		rc, err := p.Snapshot(context.Background(), sb)
		require.NoError(t, err, "Snapshot(start) should succeed; the failure surfaces during the read")
		defer rc.Close()

		var got bytes.Buffer
		_, err = io.Copy(&got, rc)
		require.Error(t, err, "draining the snapshot reader must surface tar's non-zero exit")
		require.Contains(t, err.Error(), "snapshot tar exited with code 2")
		require.Contains(t, err.Error(), "Cannot stat", "captured stderr should be in the error")
		require.Equal(t, stdoutPayload, got.Bytes(), "the bytes that did stream through should be readable; the error appears at EOF")
	})

	t.Run("returns clean EOF when tar exits zero", func(t *testing.T) {
		t.Parallel()

		archive := []byte("\x1f\x8b\x08mock-gzip-bytes")
		mock := &mockDockerClient{}
		mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
			return newMockHijackedResponse(dockerMultiplexed(archive, nil)), nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{Running: false, ExitCode: 0}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "snap-container", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

		rc, err := p.Snapshot(context.Background(), sb)
		require.NoError(t, err)
		defer rc.Close()

		got, err := io.ReadAll(rc)
		require.NoError(t, err, "a clean tar exit should not surface as an error")
		require.Equal(t, archive, got)
	})

	t.Run("surfaces ctx error when tar never exits", func(t *testing.T) {
		t.Parallel()

		// Inspect always reports Running=true so waitForExecExit spins until
		// ctx fires. Exercises the only way out when tar wedges inside the
		// container — without ctx-respecting wait the consumer would block
		// on the pipe forever.
		mock := &mockDockerClient{}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{Running: true}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "snap-container", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		rc, err := p.Snapshot(ctx, sb)
		require.NoError(t, err, "Snapshot start should succeed; the ctx error surfaces during the read")
		defer rc.Close()

		_, err = io.Copy(io.Discard, rc)
		require.Error(t, err)
		require.Contains(t, err.Error(), "wait for snapshot tar")
		require.Contains(t, err.Error(), context.DeadlineExceeded.Error())
	})
}

func TestDockerProvider_Restore(t *testing.T) {
	t.Parallel()

	t.Run("succeeds when tar exits zero", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{Running: false, ExitCode: 0}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "restore-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.Restore(context.Background(), sb, bytes.NewReader([]byte("archive-bytes")))
		require.NoError(t, err)
	})

	t.Run("error includes captured stderr when tar exits non-zero", func(t *testing.T) {
		t.Parallel()

		stderr := []byte("gzip: stdin: unexpected end of file\ntar: Child returned status 1\ntar: Error is not recoverable: exiting now\n")

		mock := &mockDockerClient{}
		mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
			// Only stderr; tar didn't emit anything to stdout before dying.
			return newMockHijackedResponse(dockerMultiplexed(nil, stderr)), nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{Running: false, ExitCode: 2}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "restore-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.Restore(context.Background(), sb, bytes.NewReader([]byte("garbage-bytes")))
		require.Error(t, err)
		require.Contains(t, err.Error(), "restore tar exited with code 2")
		require.Contains(t, err.Error(), "unexpected end of file", "tar's stderr should be in the error message — this is the diagnostic we lost before")
	})

	t.Run("returns ctx error when context is cancelled before exec exits", func(t *testing.T) {
		t.Parallel()

		// Inspect always reports Running=true so the wait loop spins until ctx
		// fires. Without ctx-respecting wait this would hang the test.
		mock := &mockDockerClient{}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{Running: true}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "restore-container", Provider: "docker", WorkDir: "/workspace"}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := p.Restore(ctx, sb, bytes.NewReader([]byte("archive")))
		require.Error(t, err)
		require.Contains(t, err.Error(), "inspect restore exec")
	})
}

func TestCappedBuffer(t *testing.T) {
	t.Parallel()

	t.Run("retains data under the cap and reports exact length", func(t *testing.T) {
		t.Parallel()
		b := newCappedBuffer(64)
		n, err := b.Write([]byte("hello"))
		require.NoError(t, err)
		require.Equal(t, 5, n)
		require.Equal(t, "hello", b.String())
	})

	t.Run("truncates writes that exceed the cap and marks dropped", func(t *testing.T) {
		t.Parallel()
		b := newCappedBuffer(8)
		// Single oversize write — only the first 8 bytes are kept.
		n, err := b.Write([]byte("0123456789"))
		require.NoError(t, err)
		require.Equal(t, 10, n, "Write should report success on the full payload so callers don't loop on a 'short write'")
		require.Equal(t, "01234567 [truncated]", b.String())
	})

	t.Run("drops further writes once full", func(t *testing.T) {
		t.Parallel()
		b := newCappedBuffer(4)
		_, _ = b.Write([]byte("ab"))
		_, _ = b.Write([]byte("cdef"))
		_, _ = b.Write([]byte("ghi"))
		require.Equal(t, "abcd [truncated]", b.String())
	})

	t.Run("zero-length write is a no-op and does not flip dropped", func(t *testing.T) {
		t.Parallel()
		b := newCappedBuffer(4)
		_, _ = b.Write(nil)
		require.Equal(t, "", b.String(), "no data written should not produce a [truncated] marker")
	})

	t.Run("non-positive limit panics", func(t *testing.T) {
		t.Parallel()
		// A zero/negative cap would let dropped flip to true with Len()==0,
		// silently swallowing the [truncated] marker downstream. We want a
		// loud panic at construction rather than a quiet diagnostic regression.
		require.PanicsWithValue(t,
			"newCappedBuffer: limit must be positive, got 0",
			func() { newCappedBuffer(0) })
		require.Panics(t, func() { newCappedBuffer(-1) })
	})
}

func TestSnapshotExecError(t *testing.T) {
	t.Parallel()

	t.Run("nil all → nil", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, snapshotExecError(nil, nil, container.ExecInspect{ExitCode: 0}, newCappedBuffer(64)))
	})

	t.Run("copy error wins over wait error and exit code", func(t *testing.T) {
		t.Parallel()
		err := snapshotExecError(io.ErrUnexpectedEOF, fmt.Errorf("wait failed"), container.ExecInspect{ExitCode: 2}, newCappedBuffer(64))
		require.Error(t, err)
		require.Contains(t, err.Error(), "read snapshot stream")
		require.Contains(t, err.Error(), io.ErrUnexpectedEOF.Error())
	})

	t.Run("wait error wins over exit code", func(t *testing.T) {
		t.Parallel()
		err := snapshotExecError(nil, fmt.Errorf("inspect: connection lost"), container.ExecInspect{ExitCode: 2}, newCappedBuffer(64))
		require.Error(t, err)
		require.Contains(t, err.Error(), "wait for snapshot tar")
		require.Contains(t, err.Error(), "connection lost")
	})

	t.Run("non-zero exit includes captured stderr suffix", func(t *testing.T) {
		t.Parallel()
		stderr := newCappedBuffer(128)
		_, _ = stderr.Write([]byte("tar: archive boom\n"))
		err := snapshotExecError(nil, nil, container.ExecInspect{ExitCode: 2}, stderr)
		require.Error(t, err)
		require.Contains(t, err.Error(), "snapshot tar exited with code 2")
		require.Contains(t, err.Error(), "tar: archive boom")
		require.NotContains(t, err.Error(), "\n", "trailing whitespace should be trimmed")
	})

	t.Run("non-zero exit with empty stderr omits the suffix", func(t *testing.T) {
		t.Parallel()
		err := snapshotExecError(nil, nil, container.ExecInspect{ExitCode: 1}, newCappedBuffer(64))
		require.Error(t, err)
		require.Equal(t, "snapshot tar exited with code 1", err.Error())
	})
}

func TestDockerProvider_Destroy(t *testing.T) {
	t.Parallel()

	t.Run("destroys container successfully", func(t *testing.T) {
		t.Parallel()

		var stopCalled, removeCalled bool

		mock := &mockDockerClient{}
		mock.containerStopFn = func(ctx context.Context, containerID string, options container.StopOptions) error {
			stopCalled = true
			return nil
		}
		mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
			removeCalled = true
			return nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.Destroy(context.Background(), sb)
		require.NoError(t, err, "Destroy should not return an error")
		require.True(t, stopCalled, "stop should have been called")
		require.True(t, removeCalled, "remove should have been called")
	})

	t.Run("idempotent when container already removed", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerStopFn = func(ctx context.Context, containerID string, options container.StopOptions) error {
			return fmt.Errorf("container not found: %w", cerrdefs.ErrNotFound)
		}
		mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
			return fmt.Errorf("container not found: %w", cerrdefs.ErrNotFound)
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.Destroy(context.Background(), sb)
		require.NoError(t, err, "Destroy should not return an error for already-removed container")
	})

	t.Run("returns error when remove fails with non-NotFound error", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
			return fmt.Errorf("permission denied")
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.Destroy(context.Background(), sb)
		require.Error(t, err, "Destroy should return an error")
		require.Contains(t, err.Error(), "remove container", "error should contain expected message")
	})
}

func TestDockerProvider_Exec(t *testing.T) {
	t.Parallel()

	t.Run("executes command with correct config and returns exit code", func(t *testing.T) {
		t.Parallel()

		var capturedCmd []string
		var capturedUser string
		var capturedAttachStdout, capturedAttachStderr bool

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
			capturedCmd = config.Cmd
			capturedUser = config.User
			capturedAttachStdout = config.AttachStdout
			capturedAttachStderr = config.AttachStderr
			return container.ExecCreateResponse{ID: "exec-1"}, nil
		}
		mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
			return newMockHijackedResponse(""), nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 0}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		var stdout, stderr bytes.Buffer
		code, err := p.Exec(context.Background(), sb, "echo hello", &stdout, &stderr)
		require.NoError(t, err, "Exec should not return an error")
		require.Equal(t, 0, code, "exit code should be 0")
		require.Equal(t, []string{"sh", "-c", "echo hello"}, capturedCmd, "command should be wrapped in sh -c")
		require.Empty(t, capturedUser, "Exec must leave User empty so the exec runs as the container's default (sandbox); bootstrap also runs as sandbox now")
		require.True(t, capturedAttachStdout, "should attach stdout")
		require.True(t, capturedAttachStderr, "should attach stderr")
	})

	t.Run("returns non-zero exit code", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 1}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		var stdout, stderr bytes.Buffer
		code, err := p.Exec(context.Background(), sb, "false", &stdout, &stderr)
		require.NoError(t, err, "Exec should not return an error for non-zero exit")
		require.Equal(t, 1, code, "exit code should be 1")
	})

	t.Run("returns error when exec create fails", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
			return container.ExecCreateResponse{}, fmt.Errorf("container not running")
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		var stdout, stderr bytes.Buffer
		_, err := p.Exec(context.Background(), sb, "echo hello", &stdout, &stderr)
		require.Error(t, err, "Exec should return an error")
		require.Contains(t, err.Error(), "create exec", "error should contain expected message")
	})

	t.Run("returns error when exec attach fails", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
			return types.HijackedResponse{}, fmt.Errorf("attach failed")
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		var stdout, stderr bytes.Buffer
		_, err := p.Exec(context.Background(), sb, "echo hello", &stdout, &stderr)
		require.Error(t, err, "Exec should return an error")
		require.Contains(t, err.Error(), "attach exec", "error should contain expected message")
	})
}

// TestDockerHandle_StartInteractiveCommand_NoTTY_WrapsWithSignalShim verifies
// that the provider wraps non-TTY commands with the internal pidfile shim so
// Interrupt(ctrl_c) has a tracked child PID to signal. The wrapping is
// provider-internal — adapters never see it.
func TestDockerHandle_StartInteractiveCommand_NoTTY_WrapsWithSignalShim(t *testing.T) {
	t.Parallel()

	var capturedCmd string
	mock := &mockDockerClient{}
	mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
		require.Equal(t, []string{"sh", "-c"}, config.Cmd[:2])
		capturedCmd = config.Cmd[2]
		require.False(t, config.Tty, "non-TTY spec should not allocate a TTY")
		return container.ExecCreateResponse{ID: "exec-1"}, nil
	}
	mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse(""), nil
	}
	mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "c1", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	handle, err := p.StartInteractiveCommand(context.Background(), sb, agent.InteractiveCommandSpec{
		Cmd:              "echo hi",
		CancellationSpec: agent.DefaultCancellationSpec,
	})
	require.NoError(t, err)
	require.NotNil(t, handle)
	require.Contains(t, capturedCmd, "echo hi", "user command should be embedded verbatim")
	require.Contains(t, capturedCmd, "& __143_pid=$!", "non-TTY ctrl_c handles wrap with the internal signal shim")
	require.Contains(t, capturedCmd, "/home/sandbox/.143-runtime-", "shim records PID under HomeDir with a per-handle suffix")
	require.Contains(t, capturedCmd, ".pid", "shim path ends in .pid")
	_ = handle.Close()
}

// TestDockerHandle_Interrupt_NoTTY_DeliversSIGINT verifies that Interrupt
// (ctrl_c) on a non-TTY handle exec-sends SIGINT to the pidfile-tracked child.
func TestDockerHandle_Interrupt_NoTTY_DeliversSIGINT(t *testing.T) {
	t.Parallel()

	var execCmds []string
	var mu sync.Mutex
	mock := &mockDockerClient{}
	mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
		mu.Lock()
		execCmds = append(execCmds, config.Cmd[2])
		mu.Unlock()
		return container.ExecCreateResponse{ID: "exec-1"}, nil
	}
	mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse(""), nil
	}
	mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "c1", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	handle, err := p.StartInteractiveCommand(context.Background(), sb, agent.InteractiveCommandSpec{
		Cmd:              "agent",
		CancellationSpec: agent.DefaultCancellationSpec,
	})
	require.NoError(t, err)
	defer handle.Close()

	require.NoError(t, handle.Interrupt(context.Background(), agent.DefaultCancellationSpec))

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(execCmds), 2, "Interrupt should run an additional exec to send SIGINT")
	last := execCmds[len(execCmds)-1]
	require.Contains(t, last, "kill -INT", "Interrupt(ctrl_c) on non-TTY handle should exec-send SIGINT")
	require.Contains(t, last, "/home/sandbox/.143-runtime-", "kill should target the per-handle pidfile written by the shim")
}

// TestDockerHandle_StartInteractiveCommand_TTY_AllocatesTTY ensures that
// adapters that declare RequiresTTY (e.g. Pi) get a real TTY and stdin path.
func TestDockerHandle_StartInteractiveCommand_TTY_AllocatesTTY(t *testing.T) {
	t.Parallel()

	var sawTTY bool
	var sawStdin bool
	mock := &mockDockerClient{}
	mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
		sawTTY = config.Tty
		sawStdin = config.AttachStdin
		return container.ExecCreateResponse{ID: "exec-tty"}, nil
	}
	mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse(""), nil
	}
	mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "c1", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	handle, err := p.StartInteractiveCommand(context.Background(), sb, agent.InteractiveCommandSpec{
		Cmd:              "pi",
		TTY:              true,
		OpenStdin:        true,
		CancellationSpec: agent.CancellationSpec{Method: agent.CancellationMethodEscape},
	})
	require.NoError(t, err)
	defer handle.Close()
	require.True(t, sawTTY, "TTY=true spec should allocate a real TTY")
	require.True(t, sawStdin, "OpenStdin=true spec should attach stdin")
}

// TestDockerHandle_Interrupt_TTY_Escape_WritesEscByteToStdin pins the wire
// behavior the Pi adapter relies on: when the spec asks for TTY+stdin and
// CancellationMethodEscape, Interrupt must write a single 0x1b byte to the
// hijacked stdin. Without this, Pi would never see the cancel signal because
// it only honors Esc under raw-mode TTY input.
func TestDockerHandle_Interrupt_TTY_Escape_WritesEscByteToStdin(t *testing.T) {
	t.Parallel()

	conn, hijacked := newCapturingHijackedResponse()
	mock := &mockDockerClient{}
	mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
		return container.ExecCreateResponse{ID: "exec-pi"}, nil
	}
	mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return hijacked, nil
	}
	mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "c1", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	handle, err := p.StartInteractiveCommand(context.Background(), sb, agent.InteractiveCommandSpec{
		Cmd:              "pi",
		TTY:              true,
		OpenStdin:        true,
		CancellationSpec: agent.CancellationSpec{Method: agent.CancellationMethodEscape},
	})
	require.NoError(t, err)
	defer handle.Close()

	require.NoError(t, handle.Interrupt(context.Background(), agent.CancellationSpec{Method: agent.CancellationMethodEscape}))

	require.Eventually(t, func() bool {
		return bytes.Contains(conn.Captured(), []byte{0x1b})
	}, 2*time.Second, 10*time.Millisecond, "Escape interrupt on a TTY+stdin handle must deliver 0x1b to stdin")
	require.Equal(t, []byte{0x1b}, conn.Captured(), "exactly one Esc byte should reach stdin per Interrupt call")
}

// TestDockerHandle_PerHandlePIDFile verifies that two handles in the same
// sandbox get distinct pidfile paths so a stale pidfile from a previous turn
// can never be mistaken for the current one.
func TestDockerHandle_PerHandlePIDFile(t *testing.T) {
	t.Parallel()

	// Only collect the shim commands (exec creations from StartInteractiveCommand).
	// Close() also schedules an `rm -f` exec for cleanup; that's not what we
	// want to compare here.
	var shimCmds []string
	var mu sync.Mutex
	mock := &mockDockerClient{}
	mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
		if strings.Contains(config.Cmd[2], "__143_pid=$!") {
			mu.Lock()
			shimCmds = append(shimCmds, config.Cmd[2])
			mu.Unlock()
		}
		return container.ExecCreateResponse{ID: "exec-x"}, nil
	}
	mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse(""), nil
	}
	mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "c1", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	for i := 0; i < 2; i++ {
		h, err := p.StartInteractiveCommand(context.Background(), sb, agent.InteractiveCommandSpec{
			Cmd:              "agent",
			CancellationSpec: agent.DefaultCancellationSpec,
		})
		require.NoError(t, err)
		_ = h.Close()
	}

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, shimCmds, 2, "each StartInteractiveCommand should produce one shim-wrapped exec")
	require.NotEqual(t, shimCmds[0], shimCmds[1],
		"each handle should get a unique pidfile suffix so multi-turn sessions never reuse a stale PID")
}

// TestDockerHandle_Wait_ReturnsInspectedExitCode covers the post-stream
// branch where Wait queries Docker for the exec exit code.
func TestDockerHandle_Wait_ReturnsInspectedExitCode(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{}
	mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
		return container.ExecCreateResponse{ID: "exec-1"}, nil
	}
	mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse(""), nil
	}
	mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 42}, nil
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "c1", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	h, err := p.StartInteractiveCommand(context.Background(), sb, agent.InteractiveCommandSpec{
		Cmd:              "agent",
		CancellationSpec: agent.DefaultCancellationSpec,
	})
	require.NoError(t, err)
	defer h.Close()

	exit, err := h.Wait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 42, exit, "Wait should return the exit code reported by ContainerExecInspect")

	// Cached on second call.
	exit2, err := h.Wait(context.Background())
	require.NoError(t, err)
	require.Equal(t, 42, exit2, "subsequent Wait calls should return the cached exit code")
}

// TestDockerHandle_Close_Idempotent ensures Close can be called repeatedly
// without panicking or double-closing the connection.
func TestDockerHandle_Close_Idempotent(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{}
	mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
		return container.ExecCreateResponse{ID: "exec-1"}, nil
	}
	mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse(""), nil
	}
	mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "c1", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	h, err := p.StartInteractiveCommand(context.Background(), sb, agent.InteractiveCommandSpec{
		Cmd:              "agent",
		CancellationSpec: agent.DefaultCancellationSpec,
	})
	require.NoError(t, err)
	require.NoError(t, h.Close())
	require.NoError(t, h.Close(), "Close must be idempotent — second call is a no-op")
	require.NoError(t, h.Close(), "third Close must still be a no-op")
}

// TestDockerHandle_WriteInput_ErrInputNotOpen ensures handles started without
// OpenStdin reject WriteInput cleanly.
func TestDockerHandle_WriteInput_ErrInputNotOpen(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{}
	mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
		return container.ExecCreateResponse{ID: "exec-1"}, nil
	}
	mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse(""), nil
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "c1", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	h, err := p.StartInteractiveCommand(context.Background(), sb, agent.InteractiveCommandSpec{
		Cmd:              "agent",
		CancellationSpec: agent.DefaultCancellationSpec,
	})
	require.NoError(t, err)
	defer h.Close()

	err = h.WriteInput(context.Background(), []byte{0x03})
	require.ErrorIs(t, err, agent.ErrInputNotOpen,
		"WriteInput on a handle without OpenStdin must return ErrInputNotOpen")
}

// TestDockerHandle_StartInteractiveCommand_AttachFailureSurfacesError
// verifies that a failing ContainerExecAttach is surfaced rather than
// silently leaving a half-built handle dangling.
func TestDockerHandle_StartInteractiveCommand_AttachFailureSurfacesError(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{}
	mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
		return container.ExecCreateResponse{ID: "exec-1"}, nil
	}
	mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return types.HijackedResponse{}, errors.New("attach refused: daemon unhealthy")
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "c1", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	h, err := p.StartInteractiveCommand(context.Background(), sb, agent.InteractiveCommandSpec{
		Cmd:              "agent",
		CancellationSpec: agent.DefaultCancellationSpec,
	})
	require.Error(t, err, "attach failure must be surfaced")
	require.Nil(t, h, "no handle should be returned on attach failure")
	require.Contains(t, err.Error(), "attach interactive exec",
		"attach error should be wrapped with attribution")
}

// TestDockerHandle_Kill_ShimSendsSIGKILL covers the Kill escalation path:
// after Interrupt's grace window, Kill must exec-send SIGKILL to the
// pidfile-tracked child rather than only closing the local connection.
func TestDockerHandle_Kill_ShimSendsSIGKILL(t *testing.T) {
	t.Parallel()

	var execCmds []string
	var mu sync.Mutex
	mock := &mockDockerClient{}
	mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
		mu.Lock()
		execCmds = append(execCmds, config.Cmd[2])
		mu.Unlock()
		return container.ExecCreateResponse{ID: "exec-1"}, nil
	}
	mock.containerExecAttachFn = func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse(""), nil
	}
	mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "c1", Provider: "docker", WorkDir: "/workspace", HomeDir: "/home/sandbox"}

	h, err := p.StartInteractiveCommand(context.Background(), sb, agent.InteractiveCommandSpec{
		Cmd:              "agent",
		CancellationSpec: agent.DefaultCancellationSpec,
	})
	require.NoError(t, err)

	require.NoError(t, h.Kill(context.Background()))

	mu.Lock()
	defer mu.Unlock()
	var sawSIGKILL bool
	for _, cmd := range execCmds {
		if strings.Contains(cmd, "kill -KILL") {
			sawSIGKILL = true
			break
		}
	}
	require.True(t, sawSIGKILL,
		"Kill on a shim handle must exec-send SIGKILL to the pidfile-tracked child, not just close the connection")
}

func TestDockerProvider_ConnectionInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupMock   func(m *mockDockerClient)
		expectErr   bool
		errContains string
		checkInfo   func(t *testing.T, info *agent.SandboxConnectionInfo)
	}{
		{
			name: "returns connection info",
			setupMock: func(m *mockDockerClient) {
				m.containerInspectFn = func(ctx context.Context, containerID string) (container.InspectResponse, error) {
					return container.InspectResponse{
						ContainerJSONBase: &container.ContainerJSONBase{
							HostConfig: &container.HostConfig{
								NetworkMode: "143-sandbox",
							},
						},
					}, nil
				}
			},
			checkInfo: func(t *testing.T, info *agent.SandboxConnectionInfo) {
				t.Helper()
				require.Equal(t, "docker", info.Provider, "provider should be 'docker'")
				require.Equal(t, "test-container", info.SandboxID, "sandbox ID should match")
				require.Equal(t, "docker://test-container", info.ConnectURL, "connect URL should be docker protocol")
			},
		},
		{
			name: "returns error when inspect fails",
			setupMock: func(m *mockDockerClient) {
				m.containerInspectFn = func(ctx context.Context, containerID string) (container.InspectResponse, error) {
					return container.InspectResponse{}, fmt.Errorf("container not found")
				}
			},
			expectErr:   true,
			errContains: "inspect container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockDockerClient{}
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			p := NewDockerProvider(mock, newTestLogger())
			sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

			info, err := p.ConnectionInfo(context.Background(), sb)
			if tt.expectErr {
				require.Error(t, err, "ConnectionInfo should return an error")
				require.Contains(t, err.Error(), tt.errContains, "error should contain expected message")
				return
			}
			require.NoError(t, err, "ConnectionInfo should not return an error")
			if tt.checkInfo != nil {
				tt.checkInfo(t, info)
			}
		})
	}
}

func TestDockerProvider_ReadFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		setupMock   func(m *mockDockerClient)
		expectErr   bool
		errContains string
	}{
		{
			name: "returns error when exec fails",
			path: "/workspace/test.txt",
			setupMock: func(m *mockDockerClient) {
				m.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
					return container.ExecCreateResponse{}, fmt.Errorf("container not running")
				}
			},
			expectErr:   true,
			errContains: "read file",
		},
		{
			name: "returns error on non-zero exit code",
			path: "/workspace/nonexistent.txt",
			setupMock: func(m *mockDockerClient) {
				m.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
					return container.ExecInspect{ExitCode: 1}, nil
				}
			},
			expectErr:   true,
			errContains: "cat exited with code 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockDockerClient{}
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			p := NewDockerProvider(mock, newTestLogger())
			sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

			_, err := p.ReadFile(context.Background(), sb, tt.path)
			if tt.expectErr {
				require.Error(t, err, "ReadFile should return an error")
				require.Contains(t, err.Error(), tt.errContains, "error should contain expected message")
				return
			}
			require.NoError(t, err, "ReadFile should not return an error")
		})
	}
}

func TestDockerProvider_WriteFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		data        []byte
		setupMock   func(m *mockDockerClient)
		expectErr   bool
		errContains string
	}{
		{
			name: "returns error when exec fails",
			path: "/workspace/test.txt",
			data: []byte("hello world"),
			setupMock: func(m *mockDockerClient) {
				m.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
					return container.ExecCreateResponse{}, fmt.Errorf("container not running")
				}
			},
			expectErr:   true,
			errContains: "write file",
		},
		{
			name: "returns error on non-zero exit code",
			path: "/workspace/readonly.txt",
			data: []byte("hello"),
			setupMock: func(m *mockDockerClient) {
				m.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
					return container.ExecInspect{ExitCode: 1}, nil
				}
			},
			expectErr:   true,
			errContains: "write file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockDockerClient{}
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}
			p := NewDockerProvider(mock, newTestLogger())
			sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

			err := p.WriteFile(context.Background(), sb, tt.path, tt.data)
			if tt.expectErr {
				require.Error(t, err, "WriteFile should return an error")
				require.Contains(t, err.Error(), tt.errContains, "error should contain expected message")
				return
			}
			require.NoError(t, err, "WriteFile should not return an error")
		})
	}
}

func TestDockerProvider_Options(t *testing.T) {
	t.Parallel()

	t.Run("WithRuntime sets custom runtime", func(t *testing.T) {
		t.Parallel()

		cli := &mockDockerClient{}
		p := NewDockerProvider(cli, newTestLogger(), WithRuntime("runc"))
		require.Equal(t, "runc", p.runtime, "runtime should be set to runc")
	})

	t.Run("WithNetwork sets custom network", func(t *testing.T) {
		t.Parallel()

		cli := &mockDockerClient{}
		p := NewDockerProvider(cli, newTestLogger(), WithNetwork("my-network"))
		require.Equal(t, "my-network", p.network, "network should be set to my-network")
	})
}

func TestDefaultSandboxConfig(t *testing.T) {
	t.Parallel()

	cfg := agent.DefaultSandboxConfig()
	require.Equal(t, "143-sandbox:latest", cfg.Image, "default image should be '143-sandbox:latest'")
	require.Equal(t, float64(2), cfg.CPULimit, "default CPU limit should be 2")
	require.Equal(t, 3072, cfg.MemoryLimitMB, "default memory limit should be 3072 MB")
	require.Equal(t, "/workspace", cfg.WorkDir, "default work dir should be '/workspace'")
	require.Equal(t, "restricted", cfg.NetworkPolicy, "default network policy should be 'restricted'")
	require.Equal(t, 10, cfg.DiskLimitGB, "default disk limit should be 10GB")
}

func TestDefaultSandboxConfigEnvOverride(t *testing.T) {
	t.Setenv("SANDBOX_IMAGE", "ghcr.io/assembledhq/143-sandbox:latest")
	cfg := agent.DefaultSandboxConfig()
	require.Equal(t, "ghcr.io/assembledhq/143-sandbox:latest", cfg.Image, "SANDBOX_IMAGE env should override default")
}

func TestShellEscape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no special characters",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "single quotes are escaped",
			input:    "it's a test",
			expected: "it'\\''s a test",
		},
		{
			name:     "multiple single quotes",
			input:    "it's a 'test'",
			expected: "it'\\''s a '\\''test'\\''",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, shellEscape(tt.input), "shell escape should handle special characters")
		})
	}
}

func TestDockerProvider_ShellInjection(t *testing.T) {
	t.Parallel()

	t.Run("CloneRepo escapes malicious branch name", func(t *testing.T) {
		t.Parallel()

		var capturedCmd []string

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
			// Capture only the first exec (the git clone). The second is a
			// post-clone `git remote set-url` that scrubs the token.
			if capturedCmd == nil {
				capturedCmd = config.Cmd
			}
			return container.ExecCreateResponse{ID: "exec-inject"}, nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 0}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.CloneRepo(context.Background(), sb, "https://github.com/org/repo.git", "main; echo pwned", "token")
		require.NoError(t, err)

		// The command passed to sh -c should have the branch single-quoted and escaped
		shellCmd := capturedCmd[2] // sh -c "<cmd>"
		require.Contains(t, shellCmd, "'main; echo pwned'", "branch with metacharacters should be single-quoted")
		require.NotContains(t, shellCmd, "--branch main;", "bare semicolon should not appear outside quotes")
	})

	t.Run("CloneRepo escapes branch name with single quotes", func(t *testing.T) {
		t.Parallel()

		var capturedCmd []string

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
			capturedCmd = config.Cmd
			return container.ExecCreateResponse{ID: "exec-inject"}, nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 0}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.CloneRepo(context.Background(), sb, "https://github.com/org/repo.git", "main'$(whoami)", "")
		require.NoError(t, err)

		shellCmd := capturedCmd[2]
		require.Contains(t, shellCmd, "'main'\\''$(whoami)'", "single quotes in branch should be escaped")
	})

	t.Run("ReadFile escapes malicious path", func(t *testing.T) {
		t.Parallel()

		var capturedCmd []string

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
			capturedCmd = config.Cmd
			return container.ExecCreateResponse{ID: "exec-inject"}, nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 0}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		_, _ = p.ReadFile(context.Background(), sb, "/workspace/foo; env")

		shellCmd := capturedCmd[2]
		require.Contains(t, shellCmd, "'/workspace/foo; env'", "path with metacharacters should be single-quoted")
		require.NotContains(t, shellCmd, "cat /workspace/foo;", "bare semicolon should not appear outside quotes")
	})

	t.Run("WriteFile escapes malicious path", func(t *testing.T) {
		t.Parallel()

		var capturedCmd []string

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
			capturedCmd = config.Cmd
			return container.ExecCreateResponse{ID: "exec-inject"}, nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 0}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		_ = p.WriteFile(context.Background(), sb, "/workspace/$(rm -rf /)", []byte("data"))

		shellCmd := capturedCmd[2]
		require.Contains(t, shellCmd, "'/workspace/$(rm -rf /)'", "path with command substitution should be single-quoted")
	})
}

func TestDockerProvider_CloneRepo(t *testing.T) {
	t.Parallel()

	t.Run("clones repository with auth token and scrubs token from .git/config", func(t *testing.T) {
		t.Parallel()

		var capturedCmds []string

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
			capturedCmds = append(capturedCmds, strings.Join(config.Cmd, " "))
			return container.ExecCreateResponse{ID: "exec-clone"}, nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 0}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.CloneRepo(context.Background(), sb, "https://github.com/org/repo.git", "main", "ghp_test123")
		require.NoError(t, err, "CloneRepo should not return an error")
		require.Len(t, capturedCmds, 2, "should run clone then remote set-url")

		cloneCmd := capturedCmds[0]
		require.Contains(t, cloneCmd, "git clone", "should run git clone command")
		require.Contains(t, cloneCmd, "--filter=blob:none", "should do partial clone so history is preserved")
		require.Contains(t, cloneCmd, "--branch 'main'", "should clone the specified branch")
		require.Contains(t, cloneCmd, "x-access-token:ghp_test123@", "should include auth token in URL")

		scrubCmd := capturedCmds[1]
		require.Contains(t, scrubCmd, "remote set-url origin", "should reset origin to scrub token")
		require.Contains(t, scrubCmd, "https://github.com/org/repo.git", "should reset to bare URL")
		require.NotContains(t, scrubCmd, "ghp_test123", "scrub command must not reintroduce the token")
	})

	t.Run("clones without token when empty", func(t *testing.T) {
		t.Parallel()

		var capturedCmd string

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error) {
			capturedCmd = strings.Join(config.Cmd, " ")
			return container.ExecCreateResponse{ID: "exec-clone"}, nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 0}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.CloneRepo(context.Background(), sb, "https://github.com/org/repo.git", "main", "")
		require.NoError(t, err, "CloneRepo should not return an error")
		require.NotContains(t, capturedCmd, "x-access-token", "should not include auth token when empty")
	})

	t.Run("returns error on clone failure", func(t *testing.T) {
		t.Parallel()

		mock := &mockDockerClient{}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 128}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.CloneRepo(context.Background(), sb, "https://github.com/org/repo.git", "main", "")
		require.Error(t, err, "CloneRepo should return an error")
		require.Contains(t, err.Error(), "git exited with code 128", "error should contain git exit code")
	})
}

func TestRedactToken(t *testing.T) {
	t.Parallel()

	require.Equal(t, "fatal: ***@github.com/org/repo.git not found",
		redactToken("fatal: ghp_secret@github.com/org/repo.git not found", "ghp_secret"))
	require.Equal(t, "fatal: repo not found",
		redactToken("fatal: repo not found", ""))
}

// blockingHijackedConn is a net.Conn whose Read blocks until Close is called,
// modeling a hijacked exec connection for a long-running process (e.g. a
// preview service like `npm run dev`) that never produces output and never
// EOFs on its own. Closing the connection unblocks the read with EOF — the
// same shape the docker daemon presents when the underlying exec is killed.
type blockingHijackedConn struct {
	pr        *io.PipeReader
	pw        *io.PipeWriter
	closeOnce sync.Once
}

func newBlockingHijackedConn() *blockingHijackedConn {
	pr, pw := io.Pipe()
	return &blockingHijackedConn{pr: pr, pw: pw}
}

func (c *blockingHijackedConn) Read(p []byte) (int, error)  { return c.pr.Read(p) }
func (c *blockingHijackedConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *blockingHijackedConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.pw.Close()
		_ = c.pr.Close()
	})
	return nil
}
func (c *blockingHijackedConn) LocalAddr() net.Addr                { return nil }
func (c *blockingHijackedConn) RemoteAddr() net.Addr               { return nil }
func (c *blockingHijackedConn) SetDeadline(t time.Time) error      { return nil }
func (c *blockingHijackedConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *blockingHijackedConn) SetWriteDeadline(t time.Time) error { return nil }

// TestDockerProvider_ExecStream_CancelUnblocksHijackedRead verifies that
// canceling the context unblocks ExecStream when StdCopy is reading from a
// hijacked connection that would otherwise never EOF.
//
// Regression: pre-fix, ExecStream blocked indefinitely on the hijacked read
// because stdcopy.StdCopy reads from a raw net.Conn that doesn't observe
// ctx cancellation. That deadlocked preview StopPreview's state.wg.Wait()
// and stranded sandboxes for ~15 minutes — until the cleanup worker's idle
// pass eventually destroyed the container, killing the exec process and
// closing the hijacked connection from the daemon side.
//
// The fix spawns a watcher goroutine inside ExecStream that closes the
// hijacked connection when ctx is canceled, unblocking StdCopy promptly.
func TestDockerProvider_ExecStream_CancelUnblocksHijackedRead(t *testing.T) {
	t.Parallel()

	conn := newBlockingHijackedConn()
	defer conn.Close()

	mock := &mockDockerClient{}
	mock.containerExecAttachFn = func(_ context.Context, _ string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
		return types.HijackedResponse{
			Conn:   conn,
			Reader: bufio.NewReader(conn),
		}, nil
	}
	// Have ExecInspect honor ctx so the post-StdCopy inspect surfaces the
	// cancellation if StdCopy returned a clean EOF (the io.Pipe shape).
	mock.containerExecInspectFn = func(ctx context.Context, _ string) (container.ExecInspect, error) {
		if err := ctx.Err(); err != nil {
			return container.ExecInspect{}, err
		}
		return container.ExecInspect{ExitCode: 0}, nil
	}

	p := NewDockerProvider(mock, newTestLogger())
	sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		exitCode int
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		ec, err := p.ExecStream(ctx, sb, "long-running-service", func([]byte) {}, io.Discard)
		resultCh <- result{ec, err}
	}()

	// Give the goroutine time to enter StdCopy on the hijacked read. If we
	// race ahead of it, canceling ctx before the watcher is registered would
	// still unblock the read on the next Close — but we want to exercise the
	// realistic ordering where StdCopy is already blocked.
	time.Sleep(50 * time.Millisecond)

	// Sanity: ExecStream should still be running. Pre-fix this would also
	// pass — the bug is in cancellation, not initial blocking.
	select {
	case r := <-resultCh:
		t.Fatalf("ExecStream returned before ctx was canceled: exitCode=%d err=%v", r.exitCode, r.err)
	default:
	}

	cancel()

	select {
	case r := <-resultCh:
		// Either StdCopy errored ("read exec output: ...") or ExecInspect
		// observed the canceled ctx ("inspect exec: context canceled"). Both
		// shapes are acceptable; what matters is that ExecStream returned.
		require.Error(t, r.err, "expected ExecStream to surface an error after ctx cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("ExecStream did not return within 2s after ctx cancel — the hijacked read was not unblocked")
	}
}
