// Package providers implements sandbox providers for running coding agents
// in isolated environments.
package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
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
	Ping(ctx context.Context) (types.Ping, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
	NetworkInspect(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error)
	NetworkCreate(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error)
}

// DockerProvider implements SandboxProvider using Docker containers
// with optional gVisor (runsc) runtime for enhanced isolation.
type DockerProvider struct {
	client     DockerClient
	runtime    string // "runsc" (gVisor) or "runc" (standard Docker)
	network    string // pre-created Docker network with egress restrictions
	resolvConf string // host path bind-mounted at /etc/resolv.conf in sandboxes
	logger     zerolog.Logger
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

// WithResolvConf sets a host path that will be bind-mounted read-only at
// /etc/resolv.conf inside every sandbox container. Required under runsc on
// user-defined networks: gVisor's netstack can't reach Docker's embedded DNS
// at 127.0.0.11, and HostConfig.DNS doesn't replace 127.0.0.11 in resolv.conf
// on user networks — it only changes the upstream the embedded resolver
// forwards to. Empty path disables the mount; the container falls back to
// whatever resolv.conf Docker injects.
func WithResolvConf(path string) DockerProviderOption {
	return func(p *DockerProvider) {
		p.resolvConf = path
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

// HealthCheck verifies Docker daemon connectivity and, for non-runc runtimes,
// that the configured runtime is functional by running a test container.
// It also ensures the sandbox egress network exists, creating it if missing.
func (d *DockerProvider) HealthCheck(ctx context.Context) error {
	// Always verify we can reach the Docker daemon.
	if _, err := d.client.Ping(ctx); err != nil {
		return fmt.Errorf("docker health check: cannot connect to Docker daemon: %w", err)
	}

	if err := d.ensureNetwork(ctx); err != nil {
		return fmt.Errorf("docker health check: %w", err)
	}

	if d.runtime == "runc" {
		d.logger.Info().Msg("docker health check passed (runc)")
		return nil
	}

	d.logger.Info().Str("runtime", d.runtime).Msg("running sandbox runtime health check")

	pidsLimit := int64(64)
	resp, err := d.client.ContainerCreate(ctx, &container.Config{
		Image: "busybox:latest",
		Cmd:   []string{"echo", "runtime-ok"},
	}, &container.HostConfig{
		Runtime: d.runtime,
		Resources: container.Resources{
			PidsLimit: &pidsLimit,
		},
	}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("runtime %s health check: failed to create test container: %w", d.runtime, err)
	}

	// Ensure cleanup using a background context so removal succeeds even if
	// the parent context has timed out.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.client.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true})
	}()

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("runtime %s health check: failed to start test container: %w", d.runtime, err)
	}

	// Wait for the container to finish by polling ContainerInspect.
	// The test command (echo) completes nearly instantly.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("runtime %s health check: timed out waiting for test container: %w", d.runtime, ctx.Err())
		case <-ticker.C:
			info, err := d.client.ContainerInspect(ctx, resp.ID)
			if err != nil {
				return fmt.Errorf("runtime %s health check: failed to inspect test container: %w", d.runtime, err)
			}
			if info.State == nil || info.State.Running {
				continue
			}
			if info.State.ExitCode != 0 {
				return fmt.Errorf("runtime %s health check: test container exited with code %d", d.runtime, info.State.ExitCode)
			}
			d.logger.Info().Str("runtime", d.runtime).Msg("sandbox runtime health check passed")
			return nil
		}
	}
}

// ensureNetwork verifies the configured sandbox network exists on the Docker
// host, creating a plain bridge network if it does not. This is idempotent:
// concurrent calls that race past the inspect will converge because the
// daemon rejects duplicate names with a conflict error, which we treat as
// success. Host-level egress rules (iptables DOCKER-USER chain) are applied
// out of band during host provisioning; this only guarantees attach works.
func (d *DockerProvider) ensureNetwork(ctx context.Context) error {
	if d.network == "" {
		return nil
	}
	if _, err := d.client.NetworkInspect(ctx, d.network, network.InspectOptions{}); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect network %q: %w", d.network, err)
	}

	d.logger.Info().Str("network", d.network).Msg("sandbox network missing; creating bridge network")
	_, err := d.client.NetworkCreate(ctx, d.network, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{"managed-by": "143"},
		// Disable inter-container chatter so one sandbox can't TCP-connect
		// to another on the same bridge. Existing networks created before
		// this was added are NOT updated — Docker rejects option changes on
		// existing networks, so the host provisioning path handles migration.
		Options: map[string]string{
			"com.docker.network.bridge.enable_icc": "false",
		},
	})
	if err != nil && !cerrdefs.IsConflict(err) && !cerrdefs.IsAlreadyExists(err) {
		return fmt.Errorf("create network %q: %w", d.network, err)
	}
	return nil
}

// Name returns the provider identifier.
func (d *DockerProvider) Name() string {
	return "docker"
}

// scopedLogger returns a logger scoped to a sandbox, including session_id,
// org_id, purpose, and container_id when available. This is the canonical way
// to emit provider log lines so every sandbox-lifecycle event is greppable by
// session in Grafana.
func (d *DockerProvider) scopedLogger(sb *agent.Sandbox) zerolog.Logger {
	lc := d.logger.With().Str("container_id", sb.ID)
	if sb.SessionID != "" {
		lc = lc.Str("session_id", sb.SessionID)
	}
	if sb.OrgID != "" {
		lc = lc.Str("org_id", sb.OrgID)
	}
	if sb.Purpose != "" {
		lc = lc.Str("purpose", sb.Purpose)
	}
	return lc.Logger()
}

// configLogger returns a logger scoped to a SandboxConfig, used during Create
// before a Sandbox exists. Carries session_id/org_id/purpose so create-time
// failures can still be traced back to the originating session.
func (d *DockerProvider) configLogger(cfg agent.SandboxConfig) zerolog.Logger {
	lc := d.logger.With()
	if cfg.SessionID != "" {
		lc = lc.Str("session_id", cfg.SessionID)
	}
	if cfg.OrgID != "" {
		lc = lc.Str("org_id", cfg.OrgID)
	}
	if cfg.Purpose != "" {
		lc = lc.Str("purpose", cfg.Purpose)
	}
	return lc.Logger()
}

// Create spins up a new Docker container with the given resource limits and
// security hardening (dropped capabilities, gVisor runtime, non-root user).
// The rootfs is writable so agents can install packages via sudo apt-get.
func (d *DockerProvider) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	log := d.configLogger(cfg)
	log.Info().
		Str("image", cfg.Image).
		Float64("cpu_limit", cfg.CPULimit).
		Int("memory_limit_mb", cfg.MemoryLimitMB).
		Int("disk_limit_gb", cfg.DiskLimitGB).
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
		CapAdd:         []string{"SETUID", "SETGID", "DAC_OVERRIDE"}, // minimum caps for sudo
		ReadonlyRootfs: false,
		Tmpfs: map[string]string{
			"/tmp": "rw,noexec,nosuid,size=1073741824",
		},
	}

	// On user-defined networks Docker injects 127.0.0.11 into /etc/resolv.conf,
	// which gVisor's netstack can't reach — so DNS in runsc sandboxes fails
	// regardless of HostConfig.DNS (that field only changes the upstream the
	// embedded resolver forwards to, not the resolver itself). Bind-mounting a
	// host-managed resolv.conf bypasses the embedded resolver entirely and
	// works for any runtime. The host path is provisioned out-of-band; see
	// deploy/scripts/provision.sh and the SANDBOX_RESOLV_CONF env var.
	if d.resolvConf != "" {
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   d.resolvConf,
			Target:   "/etc/resolv.conf",
			ReadOnly: true,
		})
	} else {
		hostCfg.DNS = []string{"1.1.1.1", "8.8.8.8"}
	}

	if cfg.DiskLimitGB > 0 {
		hostCfg.StorageOpt = map[string]string{
			"size": fmt.Sprintf("%dG", cfg.DiskLimitGB),
		}
	}

	networkCfg := &network.NetworkingConfig{}
	platform := &ocispec.Platform{}

	resp, err := d.client.ContainerCreate(ctx, containerCfg, hostCfg, networkCfg, platform, "")
	if err != nil {
		// StorageOpt requires overlay2 with XFS+pquota. Fall back gracefully
		// on hosts that don't support it (e.g. dev machines with ext4).
		if strings.Contains(err.Error(), "storage-opt") || strings.Contains(err.Error(), "pquota") {
			log.Warn().Err(err).Msg("storage quota not supported by Docker storage driver; creating container without disk limit")
			hostCfg.StorageOpt = nil
			resp, err = d.client.ContainerCreate(ctx, containerCfg, hostCfg, networkCfg, platform, "")
		}
		if err != nil {
			return nil, fmt.Errorf("create container: %w", err)
		}
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Best-effort cleanup on start failure
		removeErr := d.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		if removeErr != nil {
			log.Error().Err(removeErr).Str("container_id", resp.ID).Msg("failed to remove container after start failure")
		}
		return nil, fmt.Errorf("start container: %w", err)
	}

	log.Info().
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
		SessionID: cfg.SessionID,
		OrgID:     cfg.OrgID,
		Purpose:   cfg.Purpose,
	}, nil
}

// CloneRepo clones a repository into the sandbox's workspace using git.
func (d *DockerProvider) CloneRepo(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
	log := d.scopedLogger(sb)
	log.Info().
		Str("branch", branch).
		Msg("cloning repository into sandbox")

	// Construct authenticated URL
	authURL := repoURL
	if token != "" {
		authURL = strings.Replace(repoURL, "https://", fmt.Sprintf("https://x-access-token:%s@", token), 1)
	}

	cmd := fmt.Sprintf("git clone --depth 1 --branch '%s' '%s' '%s'",
		shellEscape(branch), shellEscape(authURL), shellEscape(sb.WorkDir))
	var stderr bytes.Buffer
	exitCode, err := d.Exec(ctx, sb, cmd, io.Discard, &stderr)
	if err != nil {
		return fmt.Errorf("exec git clone: %w", err)
	}
	if exitCode != 0 {
		msg := redactToken(strings.TrimSpace(stderr.String()), token)
		if msg == "" {
			return fmt.Errorf("git exited with code %d (no stderr)", exitCode)
		}
		return fmt.Errorf("git exited with code %d: %s", exitCode, msg)
	}

	// Strip the token out of .git/config. git clone embeds the whole
	// authenticated URL (including x-access-token:TOKEN@github.com) into
	// the origin remote, so `cat .git/config` from inside the sandbox
	// leaks the short-lived installation token. Resetting the remote to
	// the bare URL keeps push/pull working via GITHUB_TOKEN + credential
	// helpers without leaving the token at rest on disk.
	if token != "" {
		resetCmd := fmt.Sprintf("git -C '%s' remote set-url origin '%s'",
			shellEscape(sb.WorkDir), shellEscape(repoURL))
		var resetErr bytes.Buffer
		if code, err := d.Exec(ctx, sb, resetCmd, io.Discard, &resetErr); err != nil || code != 0 {
			return fmt.Errorf("scrub clone token from .git/config (exit %d): %w: %s", code, err, resetErr.String())
		}
	}

	log.Info().Msg("repository cloned successfully")

	return nil
}

// Exec runs a command inside the sandbox container and streams output to the
// provided writers. Returns the command's exit code.
func (d *DockerProvider) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	log := d.scopedLogger(sb)
	log.Debug().
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
	log := d.scopedLogger(sb)
	log.Info().Msg("destroying sandbox container")

	// Stop the container with a short timeout
	stopTimeout := 10 // seconds
	err := d.client.ContainerStop(ctx, sb.ID, container.StopOptions{Timeout: &stopTimeout})
	if err != nil && !cerrdefs.IsNotFound(err) {
		log.Warn().Err(err).Msg("failed to stop container, forcing removal")
	}

	// Remove the container
	err = d.client.ContainerRemove(ctx, sb.ID, container.RemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove container %s: %w", sb.ID, err)
	}

	log.Info().Msg("sandbox container destroyed")

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

// Snapshot tars the workspace and agent state directories from the container.
// The returned reader streams a compressed tar archive; the caller must close it.
func (d *DockerProvider) Snapshot(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
	d.logger.Info().
		Str("container_id", sb.ID).
		Msg("snapshotting sandbox")

	// Tar workspace + agent state dirs. --ignore-failed-read handles missing dirs gracefully.
	// Agent state dirs (e.g. .claude/, .codex/, .gemini/) live under WorkDir since
	// HOME is set to WorkDir in the sandbox config.
	workDirRel := strings.TrimPrefix(sb.WorkDir, "/")
	cmd := fmt.Sprintf(
		"tar czf - --ignore-failed-read -C / '%s' '%s/.claude' '%s/.codex' '%s/.gemini' 2>/dev/null",
		workDirRel, workDirRel, workDirRel, workDirRel,
	)

	execCfg := container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := d.client.ContainerExecCreate(ctx, sb.ID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("create snapshot exec: %w", err)
	}

	attachResp, err := d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("attach snapshot exec: %w", err)
	}

	// Docker multiplexes stdout/stderr. We pipe into a buffer via StdCopy
	// and return a reader over the stdout portion.
	pr, pw := io.Pipe()
	go func() {
		defer attachResp.Close()
		_, err := stdcopy.StdCopy(pw, io.Discard, attachResp.Reader)
		pw.CloseWithError(err)
	}()

	return pr, nil
}

// Restore extracts a snapshot tarball into the sandbox container.
func (d *DockerProvider) Restore(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
	d.logger.Info().
		Str("container_id", sb.ID).
		Msg("restoring snapshot into sandbox")

	execCfg := container.ExecOptions{
		Cmd:          []string{"tar", "xzf", "-", "-C", "/"},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := d.client.ContainerExecCreate(ctx, sb.ID, execCfg)
	if err != nil {
		return fmt.Errorf("create restore exec: %w", err)
	}

	attachResp, err := d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("attach restore exec: %w", err)
	}
	defer attachResp.Close()

	// Pipe the snapshot data into stdin.
	if _, err := io.Copy(attachResp.Conn, reader); err != nil {
		return fmt.Errorf("write snapshot to container: %w", err)
	}
	// Intentionally ignored: CloseWrite signals EOF to the exec process; any error here
	// does not affect the snapshot data already written.
	_ = attachResp.CloseWrite()

	// Drain stdout/stderr so the exec process can finish writing.
	// Without this, the process may block on a full output buffer.
	_, _ = stdcopy.StdCopy(io.Discard, io.Discard, attachResp.Reader)

	// Poll until the exec process finishes. ContainerExecInspect may return
	// Running=true if called immediately after CloseWrite.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			inspectResp, err := d.client.ContainerExecInspect(ctx, execResp.ID)
			if err != nil {
				return fmt.Errorf("inspect restore exec: %w", err)
			}
			if !inspectResp.Running {
				if inspectResp.ExitCode != 0 {
					return fmt.Errorf("restore tar exited with code %d", inspectResp.ExitCode)
				}
				return nil
			}
		}
	}
}

// ExecStream runs a command inside the sandbox and calls onLine for each
// newline-delimited line of stdout as it arrives. This enables real-time
// streaming of agent output to log channels.
func (d *DockerProvider) ExecStream(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
	d.logger.Debug().
		Str("container_id", sb.ID).
		Str("cmd", cmd).
		Msg("exec-stream command in sandbox")

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

	// Use StdCopy with a line-splitting writer for stdout so onLine is
	// called for each complete line as it arrives from Docker's stream.
	lineWriter := &lineSplitter{onLine: onLine}
	if _, err := stdcopy.StdCopy(lineWriter, stderr, attachResp.Reader); err != nil {
		return -1, fmt.Errorf("read exec output: %w", err)
	}
	lineWriter.flush()

	inspectResp, err := d.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return -1, fmt.Errorf("inspect exec: %w", err)
	}

	return inspectResp.ExitCode, nil
}

// lineSplitter is an io.Writer that buffers input and calls onLine for each
// complete newline-delimited line.
type lineSplitter struct {
	onLine func(line []byte)
	buf    bytes.Buffer
}

func (l *lineSplitter) Write(p []byte) (int, error) {
	n := len(p)
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadBytes('\n')
		if err != nil {
			// Incomplete line — put it back.
			l.buf.Write(line)
			break
		}
		// Trim the trailing newline before calling back.
		l.onLine(bytes.TrimRight(line, "\n"))
	}
	return n, nil
}

func (l *lineSplitter) flush() {
	if l.buf.Len() > 0 {
		l.onLine(l.buf.Bytes())
		l.buf.Reset()
	}
}

// shellEscape escapes single quotes in a string for safe use in shell commands.
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}

// redactToken removes an auth token from a string so it is safe to surface in
// error messages or logs.
func redactToken(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "***")
	}
	return s
}
