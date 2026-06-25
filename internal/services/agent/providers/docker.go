// Package providers implements sandbox providers for running coding agents
// in isolated environments.
package providers

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/sandboxauth"
)

// Compile-time check that DockerProvider implements agent.SandboxProvider.
var _ agent.SandboxProvider = (*DockerProvider)(nil)
var _ agent.InteractiveSandboxProvider = (*DockerProvider)(nil)
var _ agent.SandboxGCProvider = (*DockerProvider)(nil)

// defaultScratchDir is the exec-allowed scratch dir injected as $TMPDIR (and
// $GOTMPDIR) for sandbox containers. /tmp is mounted noexec for defense in
// depth, which breaks any tool that compiles a binary into its tempdir and
// execs it (most famously `go test` / `go run`, but the same pattern shows up
// in pytest-xdist, native-extension installers, etc.). /var/tmp is mounted as
// a separate writable+exec tmpfs so well-behaved tooling has a place to work
// without weakening the /tmp hardening.
const defaultScratchDir = "/var/tmp"

const (
	defaultHealthCheckImage     = "busybox:1.36.1"
	healthCheckImagePullTimeout = 2 * time.Minute

	SandboxLabelManaged   = "com.assembledhq.143.managed"
	SandboxLabelType      = "com.assembledhq.143.type"
	SandboxLabelSessionID = "com.assembledhq.143.session_id"
	SandboxLabelOrgID     = "com.assembledhq.143.org_id"
	SandboxLabelPurpose   = "com.assembledhq.143.purpose"
	SandboxLabelCreatedAt = "com.assembledhq.143.created_at"

	sandboxLabelLegacySandbox   = "143.sandbox"
	sandboxLabelLegacySessionID = "143.session_id"
	sandboxLabelLegacyOrgID     = "143.org_id"
	sandboxLabelLegacyPurpose   = "143.purpose"
)

func cloneEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

func envSliceFromMap(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := env[k]
		out = append(out, k+"="+v)
	}
	return out
}

// ErrDiskQuotaUnsupported is returned when Docker rejects the StorageOpt
// quota needed to make SANDBOX_DISK_LIMIT_GB a real host-level limit.
var ErrDiskQuotaUnsupported = errors.New("docker disk quota unsupported")

// DockerClient defines the subset of the Docker API used by DockerProvider.
type DockerClient interface {
	Ping(ctx context.Context) (types.Ping, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerStats(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error)
	ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
	ImageInspect(ctx context.Context, ref string, opts ...client.ImageInspectOption) (image.InspectResponse, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
	NetworkInspect(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error)
	NetworkCreate(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error)
}

// DockerProvider implements SandboxProvider using Docker containers
// with optional gVisor (runsc) runtime for enhanced isolation.
type DockerProvider struct {
	client                 DockerClient
	runtime                string // "runsc" (gVisor) or "runc" (standard Docker)
	network                string // pre-created Docker network with egress restrictions
	resolvConf             string // host path bind-mounted at /etc/resolv.conf in sandboxes
	healthImage            string // small image used to verify the configured runtime can start containers
	requireDiskQuota       bool
	authSocketPreflightDir string
	logger                 zerolog.Logger
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

// WithHealthCheckImage sets the small image used for startup Docker probes.
// Empty values keep the default pinned BusyBox tag.
func WithHealthCheckImage(ref string) DockerProviderOption {
	return func(p *DockerProvider) {
		if strings.TrimSpace(ref) != "" {
			p.healthImage = ref
		}
	}
}

// WithRequireDiskQuota makes Docker StorageOpt quota support mandatory when
// SandboxConfig.DiskLimitGB is non-zero.
func WithRequireDiskQuota(required bool) DockerProviderOption {
	return func(p *DockerProvider) {
		p.requireDiskQuota = required
	}
}

// WithAuthSocketPreflightDir enables a startup-only proof that a sandbox can
// connect to a bind-mounted per-session credential socket. Empty disables it.
func WithAuthSocketPreflightDir(dir string) DockerProviderOption {
	return func(p *DockerProvider) {
		p.authSocketPreflightDir = dir
	}
}

// NewDockerProvider creates a new DockerProvider with the given Docker client.
func NewDockerProvider(cli DockerClient, logger zerolog.Logger, opts ...DockerProviderOption) *DockerProvider {
	p := &DockerProvider{
		client:      cli,
		runtime:     "runsc",
		network:     "143-sandbox",
		healthImage: defaultHealthCheckImage,
		logger:      logger,
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

	if d.runtime != "runc" || d.requireDiskQuota {
		if err := d.ensureHealthCheckImage(ctx); err != nil {
			return fmt.Errorf("docker health check: %w", err)
		}
	}

	if d.requireDiskQuota {
		if err := d.checkDiskQuotaSupport(ctx); err != nil {
			return fmt.Errorf("docker health check: %w", err)
		}
	}

	if d.runtime == "runc" {
		d.logger.Info().Msg("docker health check passed (runc)")
		return nil
	}

	d.logger.Info().Str("runtime", d.runtime).Msg("running sandbox runtime health check")

	pidsLimit := int64(64)
	resp, err := d.client.ContainerCreate(ctx, &container.Config{
		Image: d.healthImage,
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
			if d.authSocketPreflightDir != "" {
				if err := d.checkAuthSocketMount(ctx); err != nil {
					return err
				}
			}
			return nil
		}
	}
}

func (d *DockerProvider) checkAuthSocketMount(ctx context.Context) error {
	sessionDir, err := os.MkdirTemp(d.authSocketPreflightDir, "preflight-*")
	if err != nil {
		return fmt.Errorf("sandbox auth socket health check: create preflight dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(sessionDir); err != nil {
			d.logger.Warn().Err(err).Str("dir", sessionDir).Msg("sandbox auth socket health check: failed to remove preflight dir")
		}
	}()

	sockPath := filepath.Join(sessionDir, sandboxauth.SocketFileName)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("sandbox auth socket health check: listen on %s: %w", sockPath, err)
	}
	defer ln.Close()
	if err := os.Chmod(sockPath, 0o600); err != nil {
		return fmt.Errorf("sandbox auth socket health check: chmod %s: %w", sockPath, err)
	}

	serveErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(sandboxauth.CallTimeout))
		var req sandboxauth.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			serveErr <- fmt.Errorf("decode request: %w", err)
			return
		}
		if req.Op != sandboxauth.OpGet {
			serveErr <- fmt.Errorf("unexpected op %q", req.Op)
			return
		}
		if err := json.NewEncoder(conn).Encode(&sandboxauth.Response{
			Token:    "preflight-token",
			Username: sandboxauth.DefaultUsername,
			Identity: sandboxauth.IdentityApp,
		}); err != nil {
			serveErr <- fmt.Errorf("encode response: %w", err)
			return
		}
		serveErr <- nil
	}()

	cfg := agent.DefaultSandboxConfig()
	pidsLimit := int64(64)
	resp, err := d.client.ContainerCreate(ctx, &container.Config{
		Image: cfg.Image,
		User:  "sandbox",
		Env: []string{
			sandboxauth.SocketEnvVar + "=" + sandboxauth.SandboxSocketPath,
		},
		Cmd: []string{"sh", "-c", "143-tools auth-token --action=api >/dev/null"},
	}, &container.HostConfig{
		Runtime: d.runtime,
		Resources: container.Resources{
			PidsLimit: &pidsLimit,
		},
		Mounts: []mount.Mount{{
			Type:   mount.TypeBind,
			Source: sessionDir,
			Target: sandboxauth.SandboxSocketDir,
		}},
	}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("sandbox auth socket health check: create test container: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.client.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true})
	}()

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("sandbox auth socket health check: start test container: %w", err)
	}
	if err := d.waitForOneShotContainer(ctx, resp.ID, "sandbox auth socket health check"); err != nil {
		return err
	}
	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("sandbox auth socket health check: host socket exchange: %w", err)
		}
	case <-ctx.Done():
		return fmt.Errorf("sandbox auth socket health check: wait for host socket exchange: %w", ctx.Err())
	}
	d.logger.Info().Str("runtime", d.runtime).Msg("sandbox auth socket health check passed")
	return nil
}

func (d *DockerProvider) waitForOneShotContainer(ctx context.Context, containerID, label string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: timed out waiting for test container: %w", label, ctx.Err())
		case <-ticker.C:
			info, err := d.client.ContainerInspect(ctx, containerID)
			if err != nil {
				return fmt.Errorf("%s: inspect test container: %w", label, err)
			}
			if info.State == nil || info.State.Running {
				continue
			}
			if info.State.ExitCode != 0 {
				return fmt.Errorf("%s: test container exited with code %d", label, info.State.ExitCode)
			}
			return nil
		}
	}
}

func (d *DockerProvider) ensureHealthCheckImage(ctx context.Context) error {
	if _, err := d.client.ImageInspect(ctx, d.healthImage); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect health check image %q: %w", d.healthImage, err)
	}

	d.logger.Info().Str("image", d.healthImage).Msg("health check image missing on host; pulling from registry")
	start := time.Now()

	pullCtx, cancel := context.WithTimeout(ctx, healthCheckImagePullTimeout)
	defer cancel()

	rc, err := d.client.ImagePull(pullCtx, d.healthImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull health check image %q: %w", d.healthImage, err)
	}
	defer rc.Close()

	if pullErr := scanPullStreamForError(rc); pullErr != nil {
		return fmt.Errorf("pull health check image %q: %w", d.healthImage, pullErr)
	}

	if _, err := d.client.ImageInspect(pullCtx, d.healthImage); err != nil {
		return fmt.Errorf("health check image %q not present after pull: %w", d.healthImage, err)
	}

	d.logger.Info().Str("image", d.healthImage).Dur("elapsed", time.Since(start)).Msg("health check image pulled successfully")
	return nil
}

func scanPullStreamForError(r io.Reader) error {
	type pullEvent struct {
		Error       string `json:"error"`
		ErrorDetail struct {
			Message string `json:"message"`
		} `json:"errorDetail"`
	}
	var firstErr error
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev pullEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if firstErr == nil {
			switch {
			case ev.ErrorDetail.Message != "":
				firstErr = fmt.Errorf("%s", ev.ErrorDetail.Message)
			case ev.Error != "":
				firstErr = fmt.Errorf("%s", ev.Error)
			}
		}
	}
	if err := scanner.Err(); err != nil && firstErr == nil {
		return fmt.Errorf("read pull stream: %w", err)
	}
	return firstErr
}

func (d *DockerProvider) checkDiskQuotaSupport(ctx context.Context) error {
	resp, err := d.client.ContainerCreate(ctx, &container.Config{
		Image: d.healthImage,
		Cmd:   []string{"true"},
		Labels: map[string]string{
			SandboxLabelManaged: "true",
			SandboxLabelType:    "quota-probe",
		},
	}, &container.HostConfig{
		Runtime: d.runtime,
		StorageOpt: map[string]string{
			"size": "1G",
		},
	}, nil, nil, "")
	if err != nil {
		if isDiskQuotaUnsupported(err) {
			return fmt.Errorf("%w: %v", ErrDiskQuotaUnsupported, err)
		}
		return fmt.Errorf("quota probe create: %w", err)
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.client.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("quota probe cleanup: %w", err)
	}
	d.logger.Info().Msg("docker disk quota health check passed")
	return nil
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
// The rootfs is writable so the orchestrator can seed per-session state via
// unprivileged docker exec as the sandbox user (see bootstrap below).
// Runtime apt-get is not supported — every dependency is baked into the
// sandbox image — because sudo's setuid bit is stripped under gVisor /
// nosuid mounts and the provider runs with CapDrop=ALL.
func (d *DockerProvider) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	networkName := cfg.NetworkName
	if networkName == "" {
		networkName = d.network
	}
	resolvConf := cfg.ResolvConfPath
	if resolvConf == "" {
		resolvConf = d.resolvConf
	}
	egressMode := cfg.EgressMode
	if egressMode == "" {
		egressMode = agent.SandboxEgressModeDirect
	}
	log := d.configLogger(cfg)
	log.Info().
		Str("image", cfg.Image).
		Float64("cpu_limit", cfg.CPULimit).
		Int("memory_limit_mb", cfg.MemoryLimitMB).
		Int("disk_limit_gb", cfg.DiskLimitGB).
		Str("runtime", d.runtime).
		Str("network", networkName).
		Str("egress_mode", egressMode).
		Msg("creating sandbox container")

	pidsLimit := int64(256)

	// Convert env map to Docker's KEY=VALUE slice format.
	var envSlice []string
	for k, v := range cfg.Env {
		envSlice = append(envSlice, k+"="+v)
	}
	// Point TMPDIR at the exec-allowed scratch tmpfs (see defaultScratchDir)
	// so well-behaved tools don't trip over the noexec /tmp. GOTMPDIR is kept
	// as belt-and-suspenders in case a caller overrides TMPDIR.
	if _, ok := cfg.Env["TMPDIR"]; !ok {
		envSlice = append(envSlice, "TMPDIR="+defaultScratchDir)
	}
	if _, ok := cfg.Env["GOTMPDIR"]; !ok {
		envSlice = append(envSlice, "GOTMPDIR="+defaultScratchDir)
	}

	// Anchor the container's WorkingDir at HomeDir (baked into the image by
	// `useradd -m sandbox`, owned by sandbox:sandbox) rather than cfg.WorkDir.
	// If WorkingDir doesn't exist, the OCI runtime auto-creates it as root —
	// which then makes the dir unwritable by the sandbox user without a
	// privileged chown. Pointing at an always-present, sandbox-owned dir
	// sidesteps that entirely so bootstrap can run as the sandbox user.
	bootstrapCwd := cfg.HomeDir
	if bootstrapCwd == "" {
		// Fallback for unusual callers that don't set HomeDir. Bootstrap can
		// still succeed as long as cfg.WorkDir is writable by sandbox.
		bootstrapCwd = "/"
	}
	containerCfg := &container.Config{
		Image:      cfg.Image,
		WorkingDir: bootstrapCwd,
		User:       "sandbox",
		Tty:        false,
		Env:        envSlice,
		Labels:     sandboxContainerLabels(cfg, time.Now()),
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
		NetworkMode: container.NetworkMode(networkName),
		CapDrop:     []string{"ALL"},
		// No CapAdd: sudo was removed from the sandbox image (setuid bits
		// are stripped under gVisor / nosuid), and bootstrap provisioning
		// runs as the sandbox user under its own home directory, which
		// needs no container capabilities. Keeping the cap set empty
		// shrinks the attack surface for any agent code that escapes into
		// the sandbox.
		ReadonlyRootfs: false,
		Tmpfs: map[string]string{
			// /tmp is hardened (noexec,nosuid) so exploits can't drop a binary
			// and run it. /var/tmp is the exec-allowed scratch dir for tools
			// that need to compile and exec in their tempdir; TMPDIR points
			// here by default (see defaultScratchDir).
			//
			// Sizes count against the container's memory cgroup as soon as
			// processes touch them, so they directly subtract from
			// SANDBOX_MEMORY_LIMIT_MB. Keeping them small reserves more of
			// that budget for the agent's actual heap. /var/tmp is the
			// larger of the two because TMPDIR points there and most
			// compile/test tooling churns scratch files there.
			"/tmp":     "rw,noexec,nosuid,size=268435456", // 256 MiB
			"/var/tmp": "rw,exec,nosuid,size=536870912",   // 512 MiB
		},
	}

	// On user-defined networks Docker injects 127.0.0.11 into /etc/resolv.conf,
	// which gVisor's netstack can't reach — so DNS in runsc sandboxes fails
	// regardless of HostConfig.DNS (that field only changes the upstream the
	// embedded resolver forwards to, not the resolver itself). Bind-mounting a
	// host-managed resolv.conf bypasses the embedded resolver entirely and
	// works for any runtime. The host path is provisioned out-of-band; see
	// deploy/scripts/provision.sh and the SANDBOX_RESOLV_CONF env var.
	if resolvConf != "" {
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   resolvConf,
			Target:   "/etc/resolv.conf",
			ReadOnly: true,
		})
	} else {
		hostCfg.DNS = []string{"1.1.1.1", "8.8.8.8"}
	}

	// Bind-mount the per-session GitHub credential socket *directory*.
	//
	// We mount the directory containing the socket — not the socket file
	// itself — so the in-container path keeps resolving to the live socket
	// even if the host process recreates it (close+reopen cycle on a turn
	// boundary, or orchestrator restart while a preview keeps the
	// container alive). Linux file bind-mounts pin the source inode at
	// mount time and would orphan on recreate; directory bind-mounts
	// resolve filenames at lookup time and survive recreate cleanly.
	//
	// Cross-tenant isolation: each container's source dir is unique to its
	// session, so even though the host listener could in principle answer
	// any session's request, the only socket reachable from inside this
	// container is its own.
	if cfg.AuthSocketPath != "" {
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: filepath.Dir(cfg.AuthSocketPath),
			Target: sandboxauth.SandboxSocketDir,
		})
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
		if isDiskQuotaUnsupported(err) {
			if d.requireDiskQuota {
				return nil, fmt.Errorf("create container with %dGB disk quota: %w: %v", cfg.DiskLimitGB, ErrDiskQuotaUnsupported, err)
			}
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

	sb := &agent.Sandbox{
		ID:       resp.ID,
		Provider: "docker",
		WorkDir:  cfg.WorkDir,
		HomeDir:  cfg.HomeDir,
		Env:      cloneEnv(cfg.Env),
		Metadata: map[string]string{
			"runtime":                       d.runtime,
			"network":                       networkName,
			agent.SandboxMetadataEgressMode: egressMode,
		},
		SessionID: cfg.SessionID,
		OrgID:     cfg.OrgID,
		Purpose:   cfg.Purpose,
	}

	// Bootstrap WorkDir as the sandbox user. cfg.WorkDir is always either the
	// image's baked-in /workspace (a no-op for mkdir -p) or a HomeDir subdir
	// like /home/sandbox/<repo>; in the latter case /home/sandbox already
	// exists and is sandbox-owned (from `useradd -m`), so sandbox can create
	// the per-session subdir without any privileged exec. The previous
	// root-exec bootstrap chown'd the dir to sandbox, which requires
	// CAP_CHOWN — and that cap is stripped by CapDrop=ALL under gVisor and
	// some Docker runtimes. Owning the dir from creation sidesteps the cap
	// requirement entirely.
	bootstrapSB := &agent.Sandbox{ID: resp.ID, Provider: "docker", WorkDir: "/"}
	bootstrapCmd := fmt.Sprintf("mkdir -p '%s'", shellEscape(cfg.WorkDir))
	var bootErr bytes.Buffer
	if code, err := d.Exec(ctx, bootstrapSB, bootstrapCmd, io.Discard, &bootErr); err != nil || code != 0 {
		removeErr := d.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		if removeErr != nil {
			log.Error().Err(removeErr).Str("container_id", resp.ID).Msg("failed to remove container after bootstrap failure")
		}
		// Split the error construction so %w only wraps a non-nil error.
		// When the exec itself succeeds but the command returns a non-zero
		// exit code, err is nil and we surface only the exit code + stderr.
		if err != nil {
			return nil, fmt.Errorf("bootstrap workdir (exit %d): %w: %s", code, err, bootErr.String())
		}
		return nil, fmt.Errorf("bootstrap workdir (exit %d): %s", code, bootErr.String())
	}

	return sb, nil
}

func sandboxContainerLabels(cfg agent.SandboxConfig, createdAt time.Time) map[string]string {
	labels := map[string]string{
		"managed-by":              "143",
		sandboxLabelLegacySandbox: "true",
		SandboxLabelManaged:       "true",
		SandboxLabelType:          "sandbox",
		SandboxLabelCreatedAt:     createdAt.UTC().Format(time.RFC3339Nano),
	}
	if cfg.SessionID != "" {
		labels[sandboxLabelLegacySessionID] = cfg.SessionID
		labels[SandboxLabelSessionID] = cfg.SessionID
	}
	if cfg.OrgID != "" {
		labels[sandboxLabelLegacyOrgID] = cfg.OrgID
		labels[SandboxLabelOrgID] = cfg.OrgID
	}
	if cfg.Purpose != "" {
		labels[sandboxLabelLegacyPurpose] = cfg.Purpose
		labels[SandboxLabelPurpose] = cfg.Purpose
	}
	return labels
}

// CountLiveSandboxes counts running local sandbox containers across all
// sandbox bridges. Labels are preferred for newly-created containers; image-name
// matching keeps existing unlabeled sandboxes visible on the default bridge.
func (d *DockerProvider) CountLiveSandboxes(ctx context.Context) (int, error) {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("list live sandbox containers: %w", err)
	}
	count := 0
	for _, summary := range containers {
		if isLiveSandboxContainer(summary, d.network) {
			count++
		}
	}
	return count, nil
}

func isLiveSandboxContainer(summary container.Summary, sandboxNetwork string) bool {
	if summary.Labels != nil && (summary.Labels[sandboxLabelLegacySandbox] == "true" || isManagedSandboxLabels(summary.Labels)) {
		return true
	}
	return isLegacySandboxImage(summary) && isContainerAttachedToNetwork(summary, sandboxNetwork)
}

func isLegacySandboxImage(summary container.Summary) bool {
	image := strings.ToLower(summary.Image)
	return strings.Contains(image, "143-sandbox") && !strings.Contains(image, "143-sandbox-dns")
}

func isDiskQuotaUnsupported(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "storage-opt") ||
		strings.Contains(msg, "pquota") ||
		strings.Contains(msg, "project quota") ||
		strings.Contains(msg, "overlay over xfs")
}

// ListManagedSandboxes returns every Docker container created by this provider
// for sandbox execution, including stopped containers. It keys off sandbox
// labels rather than image names so tag churn does not affect cleanup safety.
func (d *DockerProvider) ListManagedSandboxes(ctx context.Context) ([]agent.ManagedSandboxContainer, error) {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{
		All: true,
	})
	if err != nil {
		return nil, fmt.Errorf("list managed sandbox containers: %w", err)
	}

	out := make([]agent.ManagedSandboxContainer, 0, len(containers))
	for _, c := range containers {
		if !isManagedSandboxSummary(c, d.network) {
			continue
		}
		createdAt := time.Unix(c.Created, 0).UTC()
		if raw := c.Labels[SandboxLabelCreatedAt]; raw != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
				createdAt = parsed.UTC()
			}
		}
		out = append(out, agent.ManagedSandboxContainer{
			ID:        c.ID,
			SessionID: firstLabelValue(c.Labels, SandboxLabelSessionID, sandboxLabelLegacySessionID),
			OrgID:     firstLabelValue(c.Labels, SandboxLabelOrgID, sandboxLabelLegacyOrgID),
			Purpose:   firstLabelValue(c.Labels, SandboxLabelPurpose, sandboxLabelLegacyPurpose),
			CreatedAt: createdAt,
		})
	}
	return out, nil
}

func isManagedSandboxSummary(summary container.Summary, sandboxNetwork string) bool {
	return summary.Labels != nil && (summary.Labels[sandboxLabelLegacySandbox] == "true" || isManagedSandboxLabels(summary.Labels)) ||
		(isLegacySandboxImage(summary) && isContainerAttachedToNetwork(summary, sandboxNetwork))
}

func isManagedSandboxLabels(labels map[string]string) bool {
	return labels[SandboxLabelManaged] == "true" && labels[SandboxLabelType] == "sandbox"
}

func isContainerAttachedToNetwork(summary container.Summary, networkName string) bool {
	if networkName == "" || summary.NetworkSettings == nil {
		return false
	}
	_, ok := summary.NetworkSettings.Networks[networkName]
	return ok
}

func firstLabelValue(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := labels[key]; value != "" {
			return value
		}
	}
	return ""
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

	// Partial clone (--filter=blob:none) keeps the full commit graph so agents
	// can use `git log`, `git blame`, `git show <sha>` to understand history,
	// while deferring blob downloads to first access. Setup cost is similar
	// to --depth 1 but unlocks history-based reasoning.
	cmd := fmt.Sprintf("git clone --filter=blob:none --branch '%s' '%s' '%s'",
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
// provided writers. Returns the command's exit code. Leaves ExecOptions.User
// empty so the exec runs as the container's default user (sandbox).
func (d *DockerProvider) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	log := d.scopedLogger(sb)
	log.Debug().Str("cmd", redactSandboxCommandForLog(cmd)).Msg("executing command in sandbox")

	execCfg := container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   sb.WorkDir,
		Env:          envSliceFromMap(sb.Env),
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

// ExecWithStdin runs a command inside the sandbox while streaming stdin into
// the exec session. It is used for large payloads where staging a sandbox temp
// file would create avoidable disk pressure.
func (d *DockerProvider) ExecWithStdin(ctx context.Context, sb *agent.Sandbox, cmd string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	log := d.scopedLogger(sb)
	log.Debug().Str("cmd", redactSandboxCommandForLog(cmd)).Msg("executing stdin command in sandbox")

	execCfg := container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   sb.WorkDir,
		Env:          envSliceFromMap(sb.Env),
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

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			attachResp.Close()
		case <-done:
		}
	}()

	writeErrCh := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(attachResp.Conn, stdin)
		closeErr := attachResp.CloseWrite()
		writeErrCh <- errors.Join(copyErr, closeErr)
	}()

	if _, err := stdcopy.StdCopy(stdout, stderr, attachResp.Reader); err != nil {
		attachResp.Close()
		<-writeErrCh
		return -1, fmt.Errorf("read exec output: %w", err)
	}
	if err := <-writeErrCh; err != nil {
		return -1, fmt.Errorf("write exec stdin: %w", err)
	}

	inspectResp, err := d.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return -1, fmt.Errorf("inspect exec: %w", err)
	}

	return inspectResp.ExitCode, nil
}

// cliTokenPattern matches the distinctive 143-credential prefixes (user CLI
// tokens "143u_", org join tokens "143j_") wherever they appear in logged
// command lines. The prefixes exist partly so leaked tokens are
// machine-findable — the log layer must therefore strip them before
// shipping.
var cliTokenPattern = regexp.MustCompile(`143[uj]_[A-Za-z0-9_-]{8,}`)

func redactSandboxCommandForLog(cmd string) string {
	if strings.Contains(cmd, "__143_SECRET_FILE__") {
		return "[redacted preview secret file write]"
	}
	return cliTokenPattern.ReplaceAllString(cmd, "143?_***")
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
func (d *DockerProvider) WriteFile(ctx context.Context, sb *agent.Sandbox, filePath string, data []byte) error {
	return d.WriteFileFromReader(ctx, sb, filePath, bytes.NewReader(data), int64(len(data)))
}

// WriteFileFromReader writes a file into the sandbox without requiring callers
// to materialize the entire payload as a byte slice.
func (d *DockerProvider) WriteFileFromReader(ctx context.Context, sb *agent.Sandbox, filePath string, reader io.Reader, sizeBytes int64) error {
	cleanPath := filepath.ToSlash(filepath.Clean(filePath))
	if cleanPath == "." {
		return fmt.Errorf("write file %s: invalid path", filePath)
	}
	if sizeBytes < 0 {
		return fmt.Errorf("write file %s: invalid size", filePath)
	}
	relPath := strings.TrimPrefix(cleanPath, "/")
	if relPath == "" {
		return fmt.Errorf("write file %s: invalid path", filePath)
	}
	for _, part := range strings.Split(relPath, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("write file %s: invalid path", filePath)
		}
	}
	extractDir, archiveName := writeFileTarTarget(sb, cleanPath, relPath)

	execCfg := container.ExecOptions{
		Cmd:          []string{"tar", "xf", "-", "-C", extractDir},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}
	execResp, err := d.client.ContainerExecCreate(ctx, sb.ID, execCfg)
	if err != nil {
		return fmt.Errorf("write file %s: %w", filePath, err)
	}
	attachResp, err := d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("write file %s: attach: %w", filePath, err)
	}
	defer attachResp.Close()

	pr, pw := io.Pipe()
	writeErrCh := make(chan error, 1)
	go func() {
		tw := tar.NewWriter(pw)
		var err error
		if err = writeTarDirs(tw, path.Dir(archiveName)); err == nil {
			err = tw.WriteHeader(&tar.Header{Name: archiveName, Mode: 0o600, Size: sizeBytes})
		}
		if err == nil {
			_, err = io.Copy(tw, reader)
		}
		if closeErr := tw.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = pw.CloseWithError(err)
			writeErrCh <- err
			return
		}
		writeErrCh <- pw.Close()
	}()
	if _, err := io.Copy(attachResp.Conn, pr); err != nil {
		// Close the read end so the writer goroutine unblocks and exits.
		_ = pr.CloseWithError(err)
		<-writeErrCh // wait for goroutine to finish before returning
		return fmt.Errorf("write file %s: stream tar: %w", filePath, err)
	}
	if err := <-writeErrCh; err != nil {
		return fmt.Errorf("write file %s: build tar stream: %w", filePath, err)
	}
	_ = attachResp.CloseWrite()
	stderrBuf := newCappedBuffer(tarStderrCap)
	_, _ = stdcopy.StdCopy(io.Discard, stderrBuf, attachResp.Reader)
	inspect, err := waitForExecExit(ctx, d.client, execResp.ID)
	if err != nil {
		return fmt.Errorf("write file %s: inspect tar: %w", filePath, err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("write file %s: tar exited with code %d%s", filePath, inspect.ExitCode, formatStderrSuffix(stderrBuf))
	}
	return nil
}

func writeFileTarTarget(sb *agent.Sandbox, cleanPath, fallbackRelPath string) (string, string) {
	if !strings.HasPrefix(cleanPath, "/") || sb == nil {
		return "/", fallbackRelPath
	}
	bestRoot := ""
	for _, root := range []string{sb.WorkDir, sb.HomeDir} {
		root = cleanContainerRoot(root)
		if root == "" || root == "/" {
			continue
		}
		if strings.HasPrefix(cleanPath, root+"/") && len(root) > len(bestRoot) {
			bestRoot = root
		}
	}
	if bestRoot == "" {
		if strings.HasPrefix(cleanPath, "/") {
			return path.Dir(cleanPath), path.Base(cleanPath)
		}
		return "/", fallbackRelPath
	}
	return bestRoot, strings.TrimPrefix(cleanPath, bestRoot+"/")
}

func cleanContainerRoot(root string) string {
	root = filepath.ToSlash(filepath.Clean(strings.TrimSpace(root)))
	if !strings.HasPrefix(root, "/") {
		return ""
	}
	for _, part := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return ""
		}
	}
	return root
}

func writeTarDirs(tw *tar.Writer, dir string) error {
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}
	var current string
	for _, part := range strings.Split(dir, "/") {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current = current + "/" + part
		}
		if err := tw.WriteHeader(&tar.Header{Name: current + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			return err
		}
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

// IsAlive reports whether the container still exists on the daemon. A
// definitive "not found" from Docker maps to (false, nil) so callers can
// distinguish a gone container from a transient lookup failure. Running
// state is not checked — the goal is only to catch zombie rows pointing at
// a container that's been removed out-of-band.
func (d *DockerProvider) IsAlive(ctx context.Context, sb *agent.Sandbox) (bool, error) {
	_, err := d.client.ContainerInspect(ctx, sb.ID)
	if err == nil {
		return true, nil
	}
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("inspect container %s: %w", sb.ID, err)
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

// tarStderrCap bounds how much stderr we keep from snapshot/restore tar
// processes for diagnostic messages. Tar's error output is short by design;
// 4 KiB leaves room for several wrapped messages without unbounded memory
// growth on a tar that goes haywire (e.g. runaway warnings on a huge tree).
const tarStderrCap = 4 * 1024

// execPollInterval is how often we poll Docker to discover that an exec
// process has finished. 50 ms keeps responsiveness without burning CPU on
// idle sessions; matches the cadence Restore used historically.
const execPollInterval = 50 * time.Millisecond

// restoreDrainGrace bounds how long Restore will wait to drain tar's stderr
// after a failed stdin write, so a half-open docker socket can't hang the
// restore. tar's stderr is short and already buffered, so this is ample.
const restoreDrainGrace = 5 * time.Second

// snapshotMagicLen is how many leading bytes we peek to identify a snapshot
// archive's compression. 4 covers the longest magic we check (zstd).
const snapshotMagicLen = 4

// tarStdinDecompressFlag returns the explicit tar decompression flag for an
// archive identified by its leading magic bytes, or "" when the bytes match no
// known compression (uncompressed, or too few bytes to tell). GNU tar requires
// this flag when reading an archive from a pipe; from a seekable file it
// auto-detects and the flag is unnecessary.
func tarStdinDecompressFlag(magic []byte) string {
	switch {
	case len(magic) >= 4 && magic[0] == 0x28 && magic[1] == 0xB5 && magic[2] == 0x2F && magic[3] == 0xFD:
		return "--zstd" // zstd magic: 28 B5 2F FD
	case len(magic) >= 2 && magic[0] == 0x1F && magic[1] == 0x8B:
		return "-z" // gzip magic: 1F 8B
	default:
		return ""
	}
}

// Snapshot tars the workspace and agent state directories from the container.
// The returned reader streams a compressed tar archive; the caller must close it.
//
// Tar's exit status is checked after the stream completes — a non-zero exit
// surfaces as an error from Read or Close on the returned ReadCloser. This
// matters: an earlier version silently uploaded partial archives whenever tar
// failed (e.g. when the gVisor runtime crashed mid-turn), which left sessions
// permanently un-restorable. We now refuse to claim success on a broken tar.
func (d *DockerProvider) Snapshot(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
	d.logger.Info().
		Str("container_id", sb.ID).
		Msg("snapshotting sandbox")

	// Tar workspace + agent state. --ignore-failed-read handles missing paths gracefully.
	// Agent state dirs (.claude/, .codex/, .gemini/) and Claude Code's top-level
	// .claude.json live under HomeDir, not WorkDir; HOME is set to the sandbox
	// user's home so CLI configs resolve there.
	//
	// Stderr is intentionally NOT redirected to /dev/null inside the shell so
	// diagnostic messages from a failing tar reach our caller via the docker
	// multiplex stream. --ignore-failed-read keeps benign warnings silent.
	// Compression and the regenerable-cache exclude list are shared with the
	// preview snapshot path via the agent package so the two stay consistent;
	// excludeGit=false because a session checkpoint must retain .git for resume.
	workDirRel := strings.TrimPrefix(sb.WorkDir, "/")
	homeDirRel := strings.TrimPrefix(sb.HomeDir, "/")
	cmd := fmt.Sprintf(
		"tar -c %s -f - --ignore-failed-read %s -C / '%s' '%s/.claude' '%s/.claude.json' '%s/.codex' '%s/.gemini'",
		agent.SnapshotTarCompressFlag, agent.SnapshotTarExcludeFlags(false),
		workDirRel, homeDirRel, homeDirRel, homeDirRel, homeDirRel,
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

	// Docker multiplexes stdout/stderr over a single connection. StdCopy
	// demuxes: tar's stdout (the archive) flows through the pipe to the
	// caller; stderr is captured into a bounded buffer so a non-zero exit
	// surfaces an actionable error rather than a bare "code 2".
	pr, pw := io.Pipe()
	go func() {
		defer attachResp.Close()

		stderrBuf := newCappedBuffer(tarStderrCap)
		_, copyErr := stdcopy.StdCopy(pw, stderrBuf, attachResp.Reader)

		// StdCopy returning means the connection drained, not that tar
		// exited. Without this wait we'd accept a half-written gzip stream
		// as a successful snapshot the moment the connection closed.
		inspect, waitErr := waitForExecExit(ctx, d.client, execResp.ID)

		pw.CloseWithError(snapshotExecError(copyErr, waitErr, inspect, stderrBuf))
	}()

	return pr, nil
}

// Restore extracts a snapshot tarball into the sandbox container.
//
// Tar's stderr is captured (bounded) and included in the error returned
// when tar exits non-zero. Without that, callers see only "exited with
// code N" and have to guess whether the archive is corrupt, the path is
// missing, or the container ran out of disk.
func (d *DockerProvider) Restore(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
	d.logger.Info().
		Str("container_id", sb.ID).
		Msg("restoring snapshot into sandbox")

	// GNU tar will NOT auto-detect compression on a streamed stdin — it errors
	// with "Archive is compressed. Use -z/--zstd option" because it can't seek
	// back over a pipe. (Auto-detection only works on a seekable -f FILE, which
	// is why the preview snapshot path, which extracts from a temp file, can use
	// a bare `tar xf`.) So we sniff the archive's magic bytes here and pass the
	// matching decompression flag explicitly. Snapshots may be zstd (current) or
	// gzip (legacy, or the fallback when the sandbox image lacks zstd), so both
	// must be handled. See agent.SnapshotTarCompressFlag.
	bufReader := bufio.NewReader(reader)
	magic, _ := bufReader.Peek(snapshotMagicLen) // best-effort; a short/empty stream falls through to plain tar and fails loudly in the exec
	tarCmd := []string{"tar"}
	if flag := tarStdinDecompressFlag(magic); flag != "" {
		tarCmd = append(tarCmd, flag)
	}
	tarCmd = append(tarCmd, "-x", "-f", "-", "-C", "/")

	execCfg := container.ExecOptions{
		Cmd:          tarCmd,
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

	// Pipe the snapshot data into stdin. A write failure here ("broken pipe",
	// "connection reset") most often means the tar process inside the container
	// already exited — it ran out of disk, hit a decompress error, or the daemon
	// killed the exec — and that close is what broke our write. We deliberately
	// do NOT return immediately: tar's stderr and exit code carry the actionable
	// cause, and returning the bare "broken pipe" (as we used to) buried it under
	// a generic socket error. Capture the write error and fall through to drain
	// stderr + inspect so we can surface the real reason when there is one.
	_, writeErr := io.Copy(attachResp.Conn, bufReader)

	// Intentionally ignored: CloseWrite signals EOF to the exec process; any error here
	// does not affect the snapshot data already written.
	_ = attachResp.CloseWrite()

	// If the write broke, the read half of the hijacked connection may be
	// half-open (the daemon went away), and StdCopy below is not ctx-aware — a
	// dead-but-not-erroring socket could hang Restore indefinitely. Bound the
	// drain with a read deadline so we can't hang; tar's stderr is tiny and
	// already buffered, so this still captures the real diagnostic. The healthy
	// path is untouched (tar closes stdout when extraction completes).
	if writeErr != nil && attachResp.Conn != nil {
		_ = attachResp.Conn.SetReadDeadline(time.Now().Add(restoreDrainGrace))
	}

	// Drain stdout/stderr so the exec process can finish writing. Stderr is
	// captured in a bounded buffer so a non-zero exit surfaces tar's actual
	// complaint instead of just an exit code.
	stderrBuf := newCappedBuffer(tarStderrCap)
	_, _ = stdcopy.StdCopy(io.Discard, stderrBuf, attachResp.Reader)

	inspect, inspectErr := waitForExecExit(ctx, d.client, execResp.ID)

	switch {
	case inspectErr == nil && inspect.ExitCode != 0:
		// tar reported a definite failure. This is the most actionable error
		// even when the write also broke — tar closing stdin is what broke it.
		return fmt.Errorf("restore tar exited with code %d%s", inspect.ExitCode, formatStderrSuffix(stderrBuf))
	case writeErr != nil:
		// The write broke and we couldn't pin it on a non-zero tar exit (the
		// daemon connection itself failed, or inspect failed too). Surface the
		// write error plus whatever tar managed to emit before dying.
		return fmt.Errorf("write snapshot to container: %w%s", writeErr, formatStderrSuffix(stderrBuf))
	case inspectErr != nil:
		return fmt.Errorf("inspect restore exec: %w", inspectErr)
	}
	return nil
}

// waitForExecExit polls until the given exec finishes and returns the
// inspect snapshot. Polls at execPollInterval and respects ctx cancellation;
// the only error returns are ctx.Err() and inspect transport failures.
func waitForExecExit(ctx context.Context, client DockerClient, execID string) (container.ExecInspect, error) {
	ticker := time.NewTicker(execPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return container.ExecInspect{}, ctx.Err()
		case <-ticker.C:
			inspect, err := client.ContainerExecInspect(ctx, execID)
			if err != nil {
				return container.ExecInspect{}, err
			}
			if !inspect.Running {
				return inspect, nil
			}
		}
	}
}

// snapshotExecError rolls up the three failure modes of a snapshot tar — an
// in-flight read error, a Docker inspect failure waiting for the exec to
// finish, or a non-zero tar exit — into the single error that gets handed
// to the caller via PipeWriter.CloseWithError. nil means clean EOF.
func snapshotExecError(copyErr, waitErr error, inspect container.ExecInspect, stderr *cappedBuffer) error {
	switch {
	case copyErr != nil:
		return fmt.Errorf("read snapshot stream: %w", copyErr)
	case waitErr != nil:
		return fmt.Errorf("wait for snapshot tar: %w", waitErr)
	case inspect.ExitCode != 0:
		return fmt.Errorf("snapshot tar exited with code %d%s", inspect.ExitCode, formatStderrSuffix(stderr))
	default:
		return nil
	}
}

// formatStderrSuffix renders captured stderr into an error suffix like
// `: tar: /workspace: Cannot stat: ...`, or "" if stderr was empty.
// Whitespace (often a trailing newline) is trimmed for readability.
func formatStderrSuffix(stderr *cappedBuffer) string {
	if stderr == nil || stderr.Len() == 0 {
		return ""
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		return ""
	}
	return ": " + msg
}

// cappedBuffer is a write-only sink that retains at most limit bytes and
// silently drops the rest, while still reporting a "successful" Write.
// Used for capturing diagnostic stderr without unbounded memory exposure
// to a misbehaving process.
//
// Invariant: limit > 0 (enforced by newCappedBuffer). That means dropped
// only ever flips to true after at least one byte has been buffered, so
// `Len() == 0` reliably implies "no data captured" — callers can use Len
// alone to gate empty-message handling without also having to consult
// dropped.
type cappedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	if limit <= 0 {
		// A zero/negative cap would let dropped flip to true with Len()==0,
		// silently swallowing the [truncated] marker. Programmer error;
		// loud panic beats a quiet diagnostic regression.
		panic(fmt.Sprintf("newCappedBuffer: limit must be positive, got %d", limit))
	}
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buf.Len()
	switch {
	case remaining <= 0 && n > 0:
		b.dropped = true
	case n <= remaining:
		b.buf.Write(p)
	default:
		b.buf.Write(p[:remaining])
		b.dropped = true
	}
	return n, nil
}

func (b *cappedBuffer) Len() int { return b.buf.Len() }

func (b *cappedBuffer) String() string {
	if b.dropped {
		return b.buf.String() + " [truncated]"
	}
	return b.buf.String()
}

// ExecStream runs a command inside the sandbox and calls onLine for each
// newline-delimited line of stdout as it arrives. This enables real-time
// streaming of agent output to log channels.
func (d *DockerProvider) ExecStream(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
	d.logger.Debug().
		Str("container_id", sb.ID).
		Str("cmd", cmd).
		Msg("exec-stream command in sandbox")

	return d.ExecStreamWithOptions(ctx, sb, agent.ExecStreamOptions{
		Cmd:        []string{"sh", "-c", cmd},
		WorkingDir: sb.WorkDir,
	}, onLine, stderr)
}

// ExecStreamWithOptions runs a structured command inside the sandbox and calls
// onLine for each newline-delimited line of stdout as it arrives.
func (d *DockerProvider) ExecStreamWithOptions(ctx context.Context, sb *agent.Sandbox, opts agent.ExecStreamOptions, onLine func(line []byte), stderr io.Writer) (int, error) {
	if len(opts.Cmd) == 0 {
		return -1, fmt.Errorf("exec-stream command is required")
	}
	workingDir := opts.WorkingDir
	if workingDir == "" {
		workingDir = sb.WorkDir
	}
	envKeys := make([]string, 0, len(opts.Env))
	for k := range opts.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	d.logger.Debug().
		Str("container_id", sb.ID).
		Str("cmd", strings.Join(opts.Cmd, " ")).
		Strs("env_keys", envKeys).
		Str("working_dir", workingDir).
		Msg("exec-stream command in sandbox")

	execCfg := container.ExecOptions{
		Cmd:          append([]string(nil), opts.Cmd...),
		Env:          envSliceFromMap(opts.Env),
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   workingDir,
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

	// StdCopy reads from a hijacked TCP connection that does NOT observe
	// ctx cancellation — once the connection is upgraded, the docker client
	// detaches it from the http transport's normal cancellation. Without
	// this watcher a long-running command (a preview service like
	// `npm run dev`, an interactive shell, anything that does not exit on
	// its own) blocks here forever after the caller cancels, deadlocking
	// preview StopPreview at state.wg.Wait() and stranding the sandbox.
	// Closing the hijacked connection from under StdCopy unblocks it with
	// a read error; the caller can disambiguate via ctx.Err().
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			// Close is idempotent with the deferred Close above.
			attachResp.Close()
		case <-done:
		}
	}()

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
// error messages or logs. CLI/join-token shapes are stripped by pattern as
// well, since those can appear without the caller knowing the exact value.
func redactToken(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "***")
	}
	return cliTokenPattern.ReplaceAllString(s, "143?_***")
}
