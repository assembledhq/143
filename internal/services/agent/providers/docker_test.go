package providers

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
)

// mockDockerClient implements DockerClient for testing.
type mockDockerClient struct {
	containerCreateFn      func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	containerStartFn       func(ctx context.Context, containerID string, options container.StartOptions) error
	containerStopFn        func(ctx context.Context, containerID string, options container.StopOptions) error
	containerRemoveFn      func(ctx context.Context, containerID string, options container.RemoveOptions) error
	containerInspectFn     func(ctx context.Context, containerID string) (types.ContainerJSON, error)
	containerExecCreateFn  func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error)
	containerExecAttachFn  func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	containerExecInspectFn func(ctx context.Context, execID string) (container.ExecInspect, error)
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

func (m *mockDockerClient) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	if m.containerInspectFn != nil {
		return m.containerInspectFn(ctx, containerID)
	}
	return types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "143-sandbox",
			},
		},
	}, nil
}

func (m *mockDockerClient) ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
	if m.containerExecCreateFn != nil {
		return m.containerExecCreateFn(ctx, containerID, config)
	}
	return types.IDResponse{ID: "test-exec-id"}, nil
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

func newTestLogger() zerolog.Logger {
	return zerolog.Nop()
}

func TestDockerProvider_Name(t *testing.T) {
	t.Parallel()

	cli := &mockDockerClient{}
	p := NewDockerProvider(cli, newTestLogger())
	require.Equal(t, "docker", p.Name(), "provider name should be 'docker'")
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
		require.Equal(t, "/workspace", capturedConfig.WorkingDir, "container should use /workspace as working dir")

		// Verify security hardening
		require.Equal(t, "runsc", capturedHostConfig.Runtime, "container should use gVisor runtime")
		require.Equal(t, []string(capturedHostConfig.CapDrop), []string{"ALL"}, "container should drop all capabilities")
		require.Equal(t, []string(capturedHostConfig.SecurityOpt), []string{"no-new-privileges:true"}, "container should set no-new-privileges")
		require.True(t, capturedHostConfig.ReadonlyRootfs, "container should have read-only rootfs")
		require.Contains(t, capturedHostConfig.Tmpfs, "/tmp", "container should have tmpfs at /tmp")

		// Verify resource limits
		require.Equal(t, int64(2e9), capturedHostConfig.Resources.NanoCPUs, "container should have 2 CPU cores")
		require.Equal(t, int64(4096*1024*1024), capturedHostConfig.Resources.Memory, "container should have 4GB memory")
		require.NotNil(t, capturedHostConfig.Resources.PidsLimit, "container should have PID limit")
		require.Equal(t, int64(256), *capturedHostConfig.Resources.PidsLimit, "container should have PID limit of 256")

		// Verify sandbox result
		require.Equal(t, "abc123", sb.ID, "sandbox ID should match container ID")
		require.Equal(t, "docker", sb.Provider, "sandbox provider should be 'docker'")
		require.Equal(t, "/workspace", sb.WorkDir, "sandbox workdir should be '/workspace'")
		require.Equal(t, "runsc", sb.Metadata["runtime"], "sandbox metadata should include runtime")
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
			return errdefs.NotFound(fmt.Errorf("container not found"))
		}
		mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
			return errdefs.NotFound(fmt.Errorf("container not found"))
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
		var capturedAttachStdout, capturedAttachStderr bool

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
			capturedCmd = config.Cmd
			capturedAttachStdout = config.AttachStdout
			capturedAttachStderr = config.AttachStderr
			return types.IDResponse{ID: "exec-1"}, nil
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
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
			return types.IDResponse{}, fmt.Errorf("container not running")
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
				m.containerInspectFn = func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
					return types.ContainerJSON{
						ContainerJSONBase: &types.ContainerJSONBase{
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
				m.containerInspectFn = func(ctx context.Context, containerID string) (types.ContainerJSON, error) {
					return types.ContainerJSON{}, fmt.Errorf("container not found")
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
				m.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
					return types.IDResponse{}, fmt.Errorf("container not running")
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
				m.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
					return types.IDResponse{}, fmt.Errorf("container not running")
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
	require.Equal(t, "143-agent:latest", cfg.Image, "default image should be '143-agent:latest'")
	require.Equal(t, float64(2), cfg.CPULimit, "default CPU limit should be 2")
	require.Equal(t, 4096, cfg.MemoryLimitMB, "default memory limit should be 4096 MB")
	require.Equal(t, "/workspace", cfg.WorkDir, "default work dir should be '/workspace'")
	require.Equal(t, "restricted", cfg.NetworkPolicy, "default network policy should be 'restricted'")
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

func TestDockerProvider_CloneRepo(t *testing.T) {
	t.Parallel()

	t.Run("clones repository with auth token", func(t *testing.T) {
		t.Parallel()

		var capturedCmd string

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
			capturedCmd = strings.Join(config.Cmd, " ")
			return types.IDResponse{ID: "exec-clone"}, nil
		}
		mock.containerExecInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
			return container.ExecInspect{ExitCode: 0}, nil
		}
		p := NewDockerProvider(mock, newTestLogger())
		sb := &agent.Sandbox{ID: "test-container", Provider: "docker", WorkDir: "/workspace"}

		err := p.CloneRepo(context.Background(), sb, "https://github.com/org/repo.git", "main", "ghp_test123")
		require.NoError(t, err, "CloneRepo should not return an error")
		require.Contains(t, capturedCmd, "git clone", "should run git clone command")
		require.Contains(t, capturedCmd, "--depth 1", "should do shallow clone")
		require.Contains(t, capturedCmd, "--branch main", "should clone the specified branch")
		require.Contains(t, capturedCmd, "x-access-token:ghp_test123@", "should include auth token in URL")
	})

	t.Run("clones without token when empty", func(t *testing.T) {
		t.Parallel()

		var capturedCmd string

		mock := &mockDockerClient{}
		mock.containerExecCreateFn = func(ctx context.Context, containerID string, config container.ExecOptions) (types.IDResponse, error) {
			capturedCmd = strings.Join(config.Cmd, " ")
			return types.IDResponse{ID: "exec-clone"}, nil
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
		require.Contains(t, err.Error(), "clone repo", "error should contain expected message")
	})
}
