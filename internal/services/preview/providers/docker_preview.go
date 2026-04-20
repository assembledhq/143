// Package providers implements PreviewCapableProvider backends.
package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/preview"
)

// Compile-time check.
var _ preview.PreviewCapableProvider = (*DockerPreviewProvider)(nil)

// DockerPreviewClient defines the subset of the Docker API used for preview infrastructure.
type DockerPreviewClient interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
}

// SandboxExecutor defines the exec interface needed from the sandbox provider
// for running preview service processes inside the sandbox.
type SandboxExecutor interface {
	ExecStream(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error)
	Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error)
	ReadFile(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error)
}

// DockerPreviewProvider implements PreviewCapableProvider using Docker.
// It manages infrastructure sidecars as Docker containers and application
// services as processes inside the existing agent sandbox.
//
// SECURITY — network isolation:
// All preview infrastructure containers are attached to a single shared
// Docker bridge network (default "143-sandbox"). Bridge networks in Docker
// permit inter-container communication by default, which means a preview
// running AI-generated code can probe sibling previews or any other
// container on the same network. This is acceptable for the common case of
// a single-user, self-hosted deployment where all previews belong to the
// same trust domain. For multi-tenant deployments, operators MUST either:
//   - run a dedicated 143 server per tenant,
//   - supply a per-org network via WithPreviewNetwork, or
//   - (preferred) run Docker with per-preview networks via an out-of-band
//     orchestrator (e.g., Kubernetes NetworkPolicies).
//
// See docker-compose.yml for the bridge-level warning and docs/design/overall.md.
type DockerPreviewProvider struct {
	client   DockerPreviewClient
	executor SandboxExecutor
	network  string // Docker network for preview infrastructure
	logger   zerolog.Logger

	mu       sync.RWMutex
	previews map[string]*previewState // handle → state
}

// previewState tracks all running components of a preview.
type previewState struct {
	handle  string
	sandbox *agent.Sandbox
	config  *models.PreviewConfig

	// Infrastructure containers (keyed by infra_name).
	infra map[string]*preview.InfraHandle

	// Application service processes (keyed by service_name).
	services map[string]*serviceState

	// cancelFn stops all service goroutines.
	cancelFn context.CancelFunc

	// wg tracks background goroutines so StopPreview can wait for them.
	wg sync.WaitGroup

	primaryPort int
}

// serviceState tracks a running application service process.
type serviceState struct {
	name   string
	pid    int
	port   int
	status models.PreviewServiceStatus
	err    string
}

// DockerPreviewOption configures a DockerPreviewProvider.
type DockerPreviewOption func(*DockerPreviewProvider)

// WithPreviewNetwork sets the Docker network for preview infrastructure containers.
func WithPreviewNetwork(network string) DockerPreviewOption {
	return func(p *DockerPreviewProvider) {
		p.network = network
	}
}

// NewDockerPreviewProvider creates a new Docker-based preview provider.
func NewDockerPreviewProvider(
	client DockerPreviewClient,
	executor SandboxExecutor,
	logger zerolog.Logger,
	opts ...DockerPreviewOption,
) *DockerPreviewProvider {
	p := &DockerPreviewProvider{
		client:   client,
		executor: executor,
		network:  "143-sandbox",
		logger:   logger,
		previews: make(map[string]*previewState),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// =============================================================================
// StartPreview
// =============================================================================

func (d *DockerPreviewProvider) StartPreview(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, extraEnv map[string]string) (*preview.PreviewHandle, error) {
	handle, err := generateHandle()
	if err != nil {
		return nil, fmt.Errorf("generate preview handle: %w", err)
	}
	// Use context.Background() so service processes outlive the StartPreview call.
	// The cancelFn is stored in previewState and called by StopPreview.
	svcCtx, cancelFn := context.WithCancel(context.Background())

	state := &previewState{
		handle:   handle,
		sandbox:  sb,
		config:   cfg,
		infra:    make(map[string]*preview.InfraHandle),
		services: make(map[string]*serviceState),
		cancelFn: cancelFn,
	}

	d.mu.Lock()
	if _, exists := d.previews[handle]; exists {
		d.mu.Unlock()
		return nil, fmt.Errorf("preview handle %q already exists (duplicate handle collision)", handle)
	}
	d.previews[handle] = state
	d.mu.Unlock()

	// Phase 1: Provision infrastructure containers.
	infraCreds := make(map[string]preview.InfraCredential)
	for name, infraCfg := range cfg.Infrastructure {
		tmpl, ok := preview.LookupInfraTemplate(infraCfg.Template)
		if !ok {
			d.cleanupState(handle)
			return nil, fmt.Errorf("unknown infrastructure template %q", infraCfg.Template)
		}

		ih, err := d.provisionInfra(ctx, sb, handle, name, infraCfg, tmpl)
		if err != nil {
			d.cleanupState(handle)
			return nil, fmt.Errorf("provision infrastructure %q: %w", name, err)
		}
		state.infra[name] = ih
		infraCreds[name] = ih.Credential
	}

	// Phase 2: Wait for infrastructure health.
	for name, ih := range state.infra {
		infraCfg := cfg.Infrastructure[name]
		tmpl, _ := preview.LookupInfraTemplate(infraCfg.Template)
		if err := d.waitForInfraHealth(ctx, ih.ContainerID, tmpl); err != nil {
			d.cleanupState(handle)
			return nil, fmt.Errorf("infrastructure %q health check failed: %w", name, err)
		}
		d.logger.Info().Str("infra", name).Str("template", infraCfg.Template).Msg("infrastructure healthy")
	}

	// Phase 3: Run init scripts.
	for name, infraCfg := range cfg.Infrastructure {
		if infraCfg.InitScript == "" {
			continue
		}
		ih := state.infra[name]
		if err := d.runInitScript(ctx, sb, ih, infraCfg); err != nil {
			d.cleanupState(handle)
			return nil, fmt.Errorf("init script for %q failed: %w", name, err)
		}
		d.logger.Info().Str("infra", name).Str("script", infraCfg.InitScript).Msg("init script completed")
	}

	// Phase 4: Build service environment with injected credentials.
	svcEnvs := d.buildServiceEnvs(cfg, infraCreds, extraEnv)

	// Phase 5: Start application services in dependency order
	// (support services first, then primary).
	primaryPort := 0
	for name, svcCfg := range cfg.Services {
		if name == cfg.Primary {
			continue // primary starts last
		}
		if err := d.startService(svcCtx, state, name, svcCfg, svcEnvs[name]); err != nil {
			d.cleanupState(handle)
			return nil, fmt.Errorf("start service %q: %w", name, err)
		}
	}
	// Start primary service.
	if primaryCfg, ok := cfg.Services[cfg.Primary]; ok {
		primaryPort = primaryCfg.Port
		if err := d.startService(svcCtx, state, cfg.Primary, primaryCfg, svcEnvs[cfg.Primary]); err != nil {
			d.cleanupState(handle)
			return nil, fmt.Errorf("start primary service %q: %w", cfg.Primary, err)
		}
	}
	state.primaryPort = primaryPort

	// Phase 6: Wait for readiness probes.
	//
	// Progressive preview: when cfg.Progressive is true and this is a
	// multi-service config, report readiness as soon as the primary service
	// passes its probe. Support services continue starting in the background.
	// The caller receives a PartiallyReady flag on the handle so the manager
	// can set the correct status.
	partiallyReady := false
	if cfg.Progressive && len(cfg.Services) > 1 {
		// Wait for primary first.
		if primaryCfg, ok := cfg.Services[cfg.Primary]; ok {
			timeout := 90 * time.Second
			if primaryCfg.Ready.TimeoutSeconds > 0 {
				timeout = time.Duration(primaryCfg.Ready.TimeoutSeconds) * time.Second
			}
			if err := d.waitForReadiness(ctx, sb, primaryCfg.Port, primaryCfg.Ready.HTTPPath, timeout); err != nil {
				d.cleanupState(handle)
				return nil, fmt.Errorf("readiness probe for primary %q failed: %w", cfg.Primary, err)
			}
			d.mu.Lock()
			state.services[cfg.Primary].status = models.PreviewServiceStatusReady
			d.mu.Unlock()
			d.logger.Info().Str("service", cfg.Primary).Int("port", primaryCfg.Port).Msg("primary service ready (progressive)")
			partiallyReady = true
		}

		// Wait for support services in the background.
		state.wg.Add(1)
		go func() {
			defer state.wg.Done()
			for name, svcCfg := range cfg.Services {
				if name == cfg.Primary {
					continue
				}
				timeout := 90 * time.Second
				if svcCfg.Ready.TimeoutSeconds > 0 {
					timeout = time.Duration(svcCfg.Ready.TimeoutSeconds) * time.Second
				}
				bgCtx, cancel := context.WithTimeout(svcCtx, timeout)
				if err := d.waitForReadiness(bgCtx, sb, svcCfg.Port, svcCfg.Ready.HTTPPath, timeout); err != nil {
					d.logger.Warn().Err(err).Str("service", name).Msg("support service readiness failed (progressive)")
					d.mu.Lock()
					if ss, ok := state.services[name]; ok {
						ss.status = models.PreviewServiceStatusFailed
						ss.err = err.Error()
					}
					d.mu.Unlock()
				} else {
					d.mu.Lock()
					if ss, ok := state.services[name]; ok {
						ss.status = models.PreviewServiceStatusReady
					}
					d.mu.Unlock()
					d.logger.Info().Str("service", name).Int("port", svcCfg.Port).Msg("support service ready (progressive)")
				}
				cancel()
			}
		}()
	} else {
		// Standard: wait for all services before reporting ready.
		for name, svcCfg := range cfg.Services {
			timeout := 90 * time.Second
			if svcCfg.Ready.TimeoutSeconds > 0 {
				timeout = time.Duration(svcCfg.Ready.TimeoutSeconds) * time.Second
			}
			if err := d.waitForReadiness(ctx, sb, svcCfg.Port, svcCfg.Ready.HTTPPath, timeout); err != nil {
				d.cleanupState(handle)
				return nil, fmt.Errorf("readiness probe for %q failed: %w", name, err)
			}
			d.mu.Lock()
			state.services[name].status = models.PreviewServiceStatusReady
			d.mu.Unlock()
			d.logger.Info().Str("service", name).Int("port", svcCfg.Port).Msg("service ready")
		}
	}

	return &preview.PreviewHandle{
		Handle:           handle,
		PrimaryPort:      primaryPort,
		InfraCredentials: infraCreds,
		PartiallyReady:   partiallyReady,
	}, nil
}

// =============================================================================
// StopPreview
// =============================================================================

func (d *DockerPreviewProvider) StopPreview(ctx context.Context, handle string) error {
	d.mu.RLock()
	state, ok := d.previews[handle]
	d.mu.RUnlock()

	if !ok {
		return nil // already stopped — idempotent
	}

	// Cancel all service goroutines and wait for background work to finish.
	state.cancelFn()
	state.wg.Wait()

	// Tear down infrastructure containers.
	for name, ih := range state.infra {
		stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		stopTimeout := 10
		if err := d.client.ContainerStop(stopCtx, ih.ContainerID, container.StopOptions{Timeout: &stopTimeout}); err != nil {
			d.logger.Warn().Err(err).Str("infra", name).Msg("failed to stop infrastructure container")
		}
		if err := d.client.ContainerRemove(stopCtx, ih.ContainerID, container.RemoveOptions{Force: true}); err != nil {
			d.logger.Warn().Err(err).Str("infra", name).Msg("failed to remove infrastructure container")
		}
		cancel()
	}

	d.mu.Lock()
	delete(d.previews, handle)
	d.mu.Unlock()

	return nil
}

// =============================================================================
// DialPreview
// =============================================================================

func (d *DockerPreviewProvider) DialPreview(ctx context.Context, handle string) (preview.PreviewStream, error) {
	d.mu.RLock()
	state, ok := d.previews[handle]
	d.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("preview %q not found", handle)
	}

	// In Docker, the sandbox container is on the same network. We connect
	// to the sandbox's IP on the primary service port.
	sandboxIP, err := d.getSandboxIP(ctx, state.sandbox.ID)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox IP: %w", err)
	}

	addr := net.JoinHostPort(sandboxIP, fmt.Sprintf("%d", state.primaryPort))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial preview at %s: %w", addr, err)
	}

	return &tcpPreviewStream{Conn: conn}, nil
}

// =============================================================================
// PreviewStatus
// =============================================================================

func (d *DockerPreviewProvider) PreviewStatus(ctx context.Context, handle string) (*preview.PreviewStatusSnapshot, error) {
	d.mu.RLock()
	state, ok := d.previews[handle]
	if !ok {
		d.mu.RUnlock()
		return nil, fmt.Errorf("preview %q not found", handle)
	}

	snap := &preview.PreviewStatusSnapshot{}

	// Copy service state under lock to avoid races with background goroutines.
	for name, ss := range state.services {
		snap.Services = append(snap.Services, preview.ServiceSnapshot{
			Name:   name,
			Status: ss.status,
			PID:    ss.pid,
			Port:   ss.port,
			Error:  ss.err,
		})
	}

	// Collect infra snapshot data under lock, then release before Docker API calls.
	type infraInfo struct {
		name, template, containerID, host string
		port                              int
	}
	infraList := make([]infraInfo, 0, len(state.infra))
	for name, ih := range state.infra {
		infraList = append(infraList, infraInfo{
			name: name, template: ih.Template, containerID: ih.ContainerID,
			host: ih.Host, port: ih.Port,
		})
	}
	d.mu.RUnlock()

	// Check actual container health outside the lock.
	for _, info := range infraList {
		infraStatus := models.PreviewInfraStatusHealthy
		if info.containerID != "" {
			inspectResp, err := d.client.ContainerInspect(ctx, info.containerID)
			if err != nil {
				infraStatus = models.PreviewInfraStatusUnhealthy
			} else if !inspectResp.State.Running {
				infraStatus = models.PreviewInfraStatusFailed
			}
		}
		snap.Infrastructure = append(snap.Infrastructure, preview.InfraSnapshot{
			Name:        info.name,
			Template:    info.template,
			ContainerID: info.containerID,
			Status:      infraStatus,
			Host:        info.host,
			Port:        info.port,
		})
	}

	return snap, nil
}

// =============================================================================
// Infrastructure provisioning
// =============================================================================

func (d *DockerPreviewProvider) provisionInfra(
	ctx context.Context,
	sb *agent.Sandbox,
	previewHandle, infraName string,
	infraCfg models.InfrastructureConfig,
	tmpl preview.InfraTemplate,
) (*preview.InfraHandle, error) {
	networkName, err := d.resolveSandboxNetwork(ctx, sb.ID)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox network: %w", err)
	}

	cred, err := generateInfraCredential(infraName)
	if err != nil {
		return nil, fmt.Errorf("generate credential for %q: %w", infraName, err)
	}
	handlePrefix := previewHandle
	if len(handlePrefix) > 12 {
		handlePrefix = handlePrefix[:12]
	}
	containerName := fmt.Sprintf("preview-%s-%s", infraName, handlePrefix)

	env := d.buildInfraEnv(infraCfg.Template, cred)
	memLimit := int64(tmpl.DefaultMemMB) * 1024 * 1024
	cpuNanos := int64(tmpl.DefaultCPU * 1e9)

	resp, err := d.client.ContainerCreate(ctx,
		&container.Config{
			Image:    tmpl.Image,
			Env:      env,
			Hostname: containerName,
		},
		&container.HostConfig{
			Resources: container.Resources{
				Memory:   memLimit,
				NanoCPUs: cpuNanos,
			},
			NetworkMode: container.NetworkMode(networkName),
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {Aliases: []string{containerName}},
			},
		},
		nil,
		containerName,
	)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Cleanup the created container on start failure.
		cleanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if rmErr := d.client.ContainerRemove(cleanCtx, resp.ID, container.RemoveOptions{Force: true}); rmErr != nil {
			d.logger.Warn().Err(rmErr).Str("container_id", resp.ID).Msg("failed to remove container after start failure")
		}
		return nil, fmt.Errorf("start container: %w", err)
	}

	return &preview.InfraHandle{
		InfraName:   infraName,
		Template:    infraCfg.Template,
		ContainerID: resp.ID,
		Host:        containerName,
		Port:        tmpl.DefaultPort,
		Credential:  cred,
	}, nil
}

func (d *DockerPreviewProvider) buildInfraEnv(template string, cred preview.InfraCredential) []string {
	switch {
	case strings.HasPrefix(template, "postgres"):
		return []string{
			fmt.Sprintf("POSTGRES_USER=%s", cred.Username),
			fmt.Sprintf("POSTGRES_PASSWORD=%s", cred.Password),
			fmt.Sprintf("POSTGRES_DB=%s", cred.Database),
		}
	case strings.HasPrefix(template, "redis"):
		return []string{
			fmt.Sprintf("REDIS_PASSWORD=%s", cred.Password),
		}
	case strings.HasPrefix(template, "mysql"):
		return []string{
			fmt.Sprintf("MYSQL_ROOT_PASSWORD=%s", cred.Password),
			fmt.Sprintf("MYSQL_USER=%s", cred.Username),
			fmt.Sprintf("MYSQL_PASSWORD=%s", cred.Password),
			fmt.Sprintf("MYSQL_DATABASE=%s", cred.Database),
		}
	default:
		return nil
	}
}

func (d *DockerPreviewProvider) waitForInfraHealth(ctx context.Context, containerID string, tmpl preview.InfraTemplate) error {
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("health check timed out after 60 seconds")
		case <-tick.C:
			// Run the health check command inside the container.
			execResp, err := d.client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
				Cmd:          tmpl.HealthCmd,
				AttachStdout: true,
				AttachStderr: true,
			})
			if err != nil {
				continue // container might not be ready yet
			}

			attachResp, err := d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
			if err != nil {
				continue
			}
			// Drain buffered output before closing to prevent fd leaks.
			_, _ = io.Copy(io.Discard, attachResp.Reader)
			attachResp.Close()

			// Check exit code.
			inspectResp, err := d.client.ContainerExecInspect(ctx, execResp.ID)
			if err != nil {
				continue
			}
			if inspectResp.ExitCode == 0 {
				return nil
			}
		}
	}
}

func (d *DockerPreviewProvider) runInitScript(
	ctx context.Context,
	sb *agent.Sandbox,
	ih *preview.InfraHandle,
	infraCfg models.InfrastructureConfig,
) error {
	// Read the init script from the sandbox filesystem.
	scriptContent, err := d.executor.ReadFile(ctx, sb, infraCfg.InitScript)
	if err != nil {
		return fmt.Errorf("read init script %q: %w", infraCfg.InitScript, err)
	}

	// Pipe the script into the infrastructure container's client tool.
	var clientCmd []string
	switch {
	case strings.HasPrefix(infraCfg.Template, "postgres"):
		clientCmd = []string{"psql", "-U", ih.Credential.Username, "-d", ih.Credential.Database}
	case strings.HasPrefix(infraCfg.Template, "mysql"):
		clientCmd = []string{"sh", "-c", fmt.Sprintf("MYSQL_PWD=%s mysql -u %s %s", shellEscape(ih.Credential.Password), shellEscape(ih.Credential.Username), shellEscape(ih.Credential.Database))}
	default:
		return fmt.Errorf("init scripts not supported for template %q", infraCfg.Template)
	}

	execResp, err := d.client.ContainerExecCreate(ctx, ih.ContainerID, container.ExecOptions{
		Cmd:          clientCmd,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create for init script: %w", err)
	}

	attachResp, err := d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("exec attach for init script: %w", err)
	}
	defer attachResp.Close()

	// Send script content to stdin.
	if _, err := attachResp.Conn.Write(scriptContent); err != nil {
		return fmt.Errorf("write init script to stdin: %w", err)
	}
	if err := attachResp.CloseWrite(); err != nil {
		return fmt.Errorf("close stdin for init script: %w", err)
	}

	// Wait for execution and check exit code.
	inspectCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-inspectCtx.Done():
			return inspectCtx.Err()
		case <-tick.C:
			inspect, err := d.client.ContainerExecInspect(inspectCtx, execResp.ID)
			if err != nil {
				return fmt.Errorf("inspect init script exec: %w", err)
			}
			if !inspect.Running {
				if inspect.ExitCode != 0 {
					return fmt.Errorf("init script exited with code %d", inspect.ExitCode)
				}
				return nil
			}
		}
	}
}

// =============================================================================
// Service management
// =============================================================================

func (d *DockerPreviewProvider) startService(
	ctx context.Context,
	state *previewState,
	name string,
	svcCfg models.ServiceConfig,
	env map[string]string,
) error {
	// Build the command with environment variables and working directory.
	var cmdParts []string

	// Change to working directory if specified.
	if svcCfg.Cwd != "" {
		cmdParts = append(cmdParts, "cd", shellEscape(svcCfg.Cwd), "&&")
	}

	// Set environment variables in sorted order for deterministic commands.
	envKeys := make([]string, 0, len(env))
	for k := range env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		cmdParts = append(cmdParts, fmt.Sprintf("%s=%s", k, shellEscape(env[k])))
	}

	// Always inject HOST=0.0.0.0 for binding.
	cmdParts = append(cmdParts, "HOST=0.0.0.0")

	// Add the actual command (each element shell-escaped).
	escapedCmd := make([]string, len(svcCfg.Command))
	for i, c := range svcCfg.Command {
		escapedCmd[i] = shellEscape(c)
	}
	cmdParts = append(cmdParts, strings.Join(escapedCmd, " "))

	cmd := strings.Join(cmdParts, " ")

	ss := &serviceState{
		name:   name,
		port:   svcCfg.Port,
		status: models.PreviewServiceStatusStarting,
	}
	// Write to services map under the lock. Background goroutines also hold
	// the lock when updating ss fields, so all access is synchronized.
	d.mu.Lock()
	state.services[name] = ss
	d.mu.Unlock()

	// Start the process in the background with a hard timeout.
	state.wg.Add(1)
	go func() {
		defer state.wg.Done()
		// Apply a hard timeout to prevent runaway service processes.
		svcCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
		defer cancel()

		// Detect the service PID after a short delay to allow the process to start.
		go func() {
			time.Sleep(500 * time.Millisecond)
			pidCmd := fmt.Sprintf("lsof -ti :%d | head -1", svcCfg.Port)
			var pidOut bytes.Buffer
			if exitCode, err := d.executor.Exec(svcCtx, state.sandbox, pidCmd, &pidOut, io.Discard); err == nil && exitCode == 0 {
				if pid, err := strconv.Atoi(strings.TrimSpace(pidOut.String())); err == nil && pid > 0 {
					d.mu.Lock()
					ss.pid = pid
					d.mu.Unlock()
				}
			}
		}()

		exitCode, err := d.executor.ExecStream(svcCtx, state.sandbox, cmd, func(line []byte) {
			// Log output for diagnostics. In a full implementation this
			// would be streamed to the preview log store.
			d.logger.Debug().Str("service", name).Str("output", string(line)).Msg("service output")
		}, io.Discard)

		d.mu.Lock()
		defer d.mu.Unlock()
		if err != nil || exitCode != 0 {
			ss.status = models.PreviewServiceStatusFailed
			if err != nil {
				ss.err = err.Error()
			} else {
				ss.err = fmt.Sprintf("exited with code %d", exitCode)
			}
			d.logger.Error().Str("service", name).Int("exit_code", exitCode).Err(err).Msg("service exited")
		} else {
			ss.status = models.PreviewServiceStatusStopped
		}
	}()

	return nil
}

func (d *DockerPreviewProvider) waitForReadiness(ctx context.Context, sb *agent.Sandbox, port int, httpPath string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	// Use curl inside the sandbox to check readiness.
	// Shell-escape the path defensively even though ValidateConfig restricts characters.
	cmd := fmt.Sprintf("curl -sf -o /dev/null %s", shellEscape(fmt.Sprintf("http://localhost:%d%s", port, httpPath)))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("readiness probe timed out after %s", timeout)
		case <-tick.C:
			exitCode, _ := d.executor.Exec(ctx, sb, cmd, io.Discard, io.Discard)
			if exitCode == 0 {
				return nil
			}
		}
	}
}

// =============================================================================
// Network helpers
// =============================================================================

func (d *DockerPreviewProvider) getSandboxIP(ctx context.Context, containerID string) (string, error) {
	inspect, err := d.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	if inspect.NetworkSettings == nil {
		return "", fmt.Errorf("container %s has no network settings", containerID)
	}

	// Look for the IP on the preview network first, fall back to any network.
	if ep, ok := inspect.NetworkSettings.Networks[d.network]; ok && ep.IPAddress != "" {
		return ep.IPAddress, nil
	}
	for _, ep := range inspect.NetworkSettings.Networks {
		if ep.IPAddress != "" {
			return ep.IPAddress, nil
		}
	}

	return "", fmt.Errorf("no IP address found for container %s", containerID)
}

func (d *DockerPreviewProvider) resolveSandboxNetwork(ctx context.Context, containerID string) (string, error) {
	inspect, err := d.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}

	if inspect.ContainerJSONBase != nil && inspect.HostConfig != nil {
		if networkName := inspect.HostConfig.NetworkMode.NetworkName(); networkName != "" {
			return networkName, nil
		}
	}
	if inspect.NetworkSettings == nil {
		return "", fmt.Errorf("container %s has no network settings", containerID)
	}

	if _, ok := inspect.NetworkSettings.Networks[d.network]; ok {
		return d.network, nil
	}
	for networkName := range inspect.NetworkSettings.Networks {
		return networkName, nil
	}

	return "", fmt.Errorf("no network found for container %s", containerID)
}

// =============================================================================
// Credential helpers
// =============================================================================

func (d *DockerPreviewProvider) buildServiceEnvs(cfg *models.PreviewConfig, infraCreds map[string]preview.InfraCredential, extraEnv map[string]string) map[string]map[string]string {
	envs := make(map[string]map[string]string, len(cfg.Services))

	// Start with service-declared env vars.
	for name, svc := range cfg.Services {
		env := make(map[string]string, len(svc.Env))
		for k, v := range svc.Env {
			env[k] = v
		}
		envs[name] = env
	}

	// Inject infrastructure credentials into services per inject_into + inject_env.
	for infraName, infraCfg := range cfg.Infrastructure {
		cred, ok := infraCreds[infraName]
		if !ok {
			continue
		}
		for envKey, templateVal := range infraCfg.InjectEnv {
			resolved := resolveCredentialTemplate(templateVal, cred)
			for _, targetSvc := range infraCfg.InjectInto {
				if env, ok := envs[targetSvc]; ok {
					env[envKey] = resolved
				}
			}
		}
	}

	// Inject platform-level env vars (e.g. PREVIEW_ORIGIN) into every service,
	// overriding any user-declared value. This is applied last so it wins over
	// both service.env and infrastructure.inject_env.
	for svcName, env := range envs {
		for k, v := range extraEnv {
			env[k] = v
		}
		envs[svcName] = env
	}

	return envs
}

func resolveCredentialTemplate(template string, cred preview.InfraCredential) string {
	r := strings.NewReplacer(
		"{{host}}", cred.Host,
		"{{port}}", fmt.Sprintf("%d", cred.Port),
		"{{username}}", cred.Username,
		"{{password}}", cred.Password,
		"{{database}}", cred.Database,
	)
	return r.Replace(template)
}

func generateInfraCredential(infraName string) (preview.InfraCredential, error) {
	password, err := preview.RandomHex(16)
	if err != nil {
		return preview.InfraCredential{}, fmt.Errorf("generate infra credential password: %w", err)
	}
	return preview.InfraCredential{
		Username: fmt.Sprintf("preview_%s", infraName),
		Password: password,
		Database: "preview_db",
	}, nil
}

// =============================================================================
// Cleanup
// =============================================================================

func (d *DockerPreviewProvider) cleanupState(handle string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = d.StopPreview(ctx, handle)
}

// =============================================================================
// Helpers
// =============================================================================

func generateHandle() (string, error) {
	return preview.RandomHex(16)
}

func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// tcpPreviewStream wraps a net.Conn as a PreviewStream.
type tcpPreviewStream struct {
	net.Conn
}

// Close closes the underlying TCP connection. Safe to call multiple times.
func (s *tcpPreviewStream) Close() error {
	if s.Conn == nil {
		return nil
	}
	return s.Conn.Close()
}
