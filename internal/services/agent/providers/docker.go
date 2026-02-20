// Package providers implements sandbox providers for running coding agents
// in isolated environments.
package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/services/agent"
)

// Compile-time check that DockerProvider implements agent.SandboxProvider.
var _ agent.SandboxProvider = (*DockerProvider)(nil)

// DockerClient defines the subset of the Docker API used by DockerProvider.
type DockerClient interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
}

// DockerProvider implements SandboxProvider using Docker containers
// with optional gVisor (runsc) runtime for enhanced isolation.
type DockerProvider struct {
	client  DockerClient
	runtime string // "runsc" (gVisor) or "runc" (standard Docker)
	network string // pre-created Docker network with egress restrictions
	logger  zerolog.Logger
}

// DockerProviderOption configures a DockerProvider.
type DockerProviderOption func(*DockerProvider)

// WithRuntime sets the OCI runtime (e.g., "runsc" for gVisor, "runc" for standard).
func WithRuntime(runtime string) DockerProviderOption {
	return func(p *DockerProvider) {
		p.runtime = runtime
	}
}

// WithNetwork sets the Docker network for sandbox containers.
func WithNetwork(network string) DockerProviderOption {
	return func(p *DockerProvider) {
		p.network = network
	}
}

// NewDockerProvider creates a new DockerProvider with the given Docker client.
func NewDockerProvider(cli DockerClient, logger zerolog.Logger, opts ...DockerProviderOption) *DockerProvider {
	p := &DockerProvider{
		client:  cli,
		runtime: "runsc",
		network: "143-sandbox",
		logger:  logger,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider identifier.
func (d *DockerProvider) Name() string {
	return "docker"
}

// Create spins up a new Docker container with the given resource limits and
// security hardening (dropped capabilities, read-only rootfs, non-root user).
func (d *DockerProvider) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	d.logger.Info().
		Str("image", cfg.Image).
		Float64("cpu_limit", cfg.CPULimit).
		Int("memory_limit_mb", cfg.MemoryLimitMB).
		Str("runtime", d.runtime).
		Msg("creating sandbox container")

	pidsLimit := int64(256)

	// Convert env map to Docker's KEY=VALUE slice format.
	var envSlice []string
	for k, v := range cfg.Env {
		envSlice = append(envSlice, k+"="+v)
	}

	containerCfg := &container.Config{
		Image:      cfg.Image,
		WorkingDir: cfg.WorkDir,
		User:       "sandbox",
		Tty:        false,
		Env:        envSlice,
		// Keep container running with a long sleep so we can exec into it
		Cmd: []string{"sleep", "infinity"},
	}

	hostCfg := &container.HostConfig{
		Runtime: d.runtime,
		Resources: container.Resources{
			NanoCPUs:  int64(cfg.CPULimit * 1e9),
			Memory:    int64(cfg.MemoryLimitMB) * 1024 * 1024,
			PidsLimit: &pidsLimit,
		},
		NetworkMode:    container.NetworkMode(d.network),
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges:true"},
		ReadonlyRootfs: true,
		Tmpfs: map[string]string{
			"/tmp": "rw,noexec,nosuid,size=1073741824",
		},
	}

	networkCfg := &network.NetworkingConfig{}
	platform := &ocispec.Platform{}

	resp, err := d.client.ContainerCreate(ctx, containerCfg, hostCfg, networkCfg, platform, "")
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Best-effort cleanup on start failure
		removeErr := d.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		if removeErr != nil {
			d.logger.Error().Err(removeErr).Str("container_id", resp.ID).Msg("failed to remove container after start failure")
		}
		return nil, fmt.Errorf("start container: %w", err)
	}

	d.logger.Info().
		Str("container_id", resp.ID).
		Msg("sandbox container started")

	return &agent.Sandbox{
		ID:       resp.ID,
		Provider: "docker",
		WorkDir:  cfg.WorkDir,
		Metadata: map[string]string{
			"runtime": d.runtime,
			"network": d.network,
		},
	}, nil
}

// CloneRepo clones a repository into the sandbox's workspace using git.
func (d *DockerProvider) CloneRepo(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
	d.logger.Info().
		Str("container_id", sb.ID).
		Str("branch", branch).
		Msg("cloning repository into sandbox")

	// Construct authenticated URL
	authURL := repoURL
	if token != "" {
		authURL = strings.Replace(repoURL, "https://", fmt.Sprintf("https://x-access-token:%s@", token), 1)
	}

	cmd := fmt.Sprintf("git clone --depth 1 --branch '%s' '%s' '%s'",
		shellEscape(branch), shellEscape(authURL), shellEscape(sb.WorkDir))
	exitCode, err := d.Exec(ctx, sb, cmd, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("clone repo: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("clone repo: git exited with code %d", exitCode)
	}

	d.logger.Info().
		Str("container_id", sb.ID).
		Msg("repository cloned successfully")

	return nil
}

// Exec runs a command inside the sandbox container and streams output to the
// provided writers. Returns the command's exit code.
func (d *DockerProvider) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	d.logger.Debug().
		Str("container_id", sb.ID).
		Str("cmd", cmd).
		Msg("executing command in sandbox")

	execCfg := container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   sb.WorkDir,
	}

	execResp, err := d.client.ContainerExecCreate(ctx, sb.ID, execCfg)
	if err != nil {
		return -1, fmt.Errorf("create exec: %w", err)
	}

	attachResp, err := d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return -1, fmt.Errorf("attach exec: %w", err)
	}
	defer attachResp.Close()

	// Docker multiplexes stdout and stderr into a single stream
	if _, err := stdcopy.StdCopy(stdout, stderr, attachResp.Reader); err != nil {
		return -1, fmt.Errorf("read exec output: %w", err)
	}

	inspectResp, err := d.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return -1, fmt.Errorf("inspect exec: %w", err)
	}

	return inspectResp.ExitCode, nil
}

// ReadFile reads a file from the sandbox filesystem by exec-ing cat.
func (d *DockerProvider) ReadFile(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode, err := d.Exec(ctx, sb, fmt.Sprintf("cat '%s'", shellEscape(path)), &stdout, &stderr)
	if err != nil {
		return nil, fmt.Errorf("read file %s: %w", path, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("read file %s: cat exited with code %d: %s", path, exitCode, stderr.String())
	}

	return stdout.Bytes(), nil
}

// WriteFile writes data to a file inside the sandbox.
func (d *DockerProvider) WriteFile(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
	var stderr bytes.Buffer

	cmd := fmt.Sprintf("printf '%%s' '%s' > '%s'", shellEscape(string(data)), shellEscape(path))
	exitCode, err := d.Exec(ctx, sb, cmd, io.Discard, &stderr)
	if err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("write file %s: exited with code %d: %s", path, exitCode, stderr.String())
	}

	return nil
}

// Destroy stops and removes the sandbox container. Safe to call multiple times.
func (d *DockerProvider) Destroy(ctx context.Context, sb *agent.Sandbox) error {
	d.logger.Info().
		Str("container_id", sb.ID).
		Msg("destroying sandbox container")

	// Stop the container with a short timeout
	stopTimeout := 10 // seconds
	err := d.client.ContainerStop(ctx, sb.ID, container.StopOptions{Timeout: &stopTimeout})
	if err != nil && !cerrdefs.IsNotFound(err) {
		d.logger.Warn().Err(err).Str("container_id", sb.ID).Msg("failed to stop container, forcing removal")
	}

	// Remove the container
	err = d.client.ContainerRemove(ctx, sb.ID, container.RemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove container %s: %w", sb.ID, err)
	}

	d.logger.Info().
		Str("container_id", sb.ID).
		Msg("sandbox container destroyed")

	return nil
}

// ConnectionInfo returns Docker-specific connection details for local resume.
func (d *DockerProvider) ConnectionInfo(ctx context.Context, sb *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	inspect, err := d.client.ContainerInspect(ctx, sb.ID)
	if err != nil {
		return nil, fmt.Errorf("inspect container %s: %w", sb.ID, err)
	}

	return &agent.SandboxConnectionInfo{
		Provider:   "docker",
		SandboxID:  sb.ID,
		ConnectURL: fmt.Sprintf("docker://%s", sb.ID),
		Environment: map[string]string{
			"DOCKER_HOST": inspect.HostConfig.NetworkMode.NetworkName(),
		},
	}, nil
}

// shellEscape escapes single quotes in a string for safe use in shell commands.
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
