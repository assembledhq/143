// Package providers implements PreviewCapableProvider backends.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"golang.org/x/sync/singleflight"

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
	// ImageInspect / ImagePull are used to lazy-pull preview infrastructure
	// images on first use, so worker hosts don't need to pre-pull every
	// supported template (postgres:17-alpine, redis:7-alpine, mysql:8, …).
	ImageInspect(ctx context.Context, ref string, opts ...client.ImageInspectOption) (image.InspectResponse, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
}

// imagePullTimeout bounds how long a single infrastructure image pull can
// take before we give up. 5 minutes is generous for postgres-alpine over a
// reasonable connection but stops a wedged registry from blocking the
// preview start indefinitely.
const imagePullTimeout = 5 * time.Minute

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
	dialer   previewDialer

	mu       sync.RWMutex
	previews map[string]*previewState // handle → state

	// imagePulls deduplicates concurrent pulls of the same image ref. Two
	// preview starts hitting an absent image at the same moment share a
	// single pull instead of fanning out N redundant streams.
	imagePulls singleflight.Group
}

type previewDialer func(ctx context.Context, addr string) (net.Conn, error)

func defaultPreviewDialer(ctx context.Context, addr string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", addr)
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

	// outputTail holds the last serviceTailLines stdout/stderr lines captured
	// from the service process. It is appended to from the ExecStream onLine
	// callback under d.mu and is surfaced to the observer when the service
	// fails so the user can see why it exited.
	outputTail []string
}

// serviceTailLines is the size of the per-service stdout/stderr ring buffer
// that the provider keeps so it can replay the tail to the observer when a
// service exits non-zero.
const serviceTailLines = 200

// serviceExitTailLines is the number of trailing non-blank stdout/stderr
// lines folded into the user-visible exit error from formatServiceExitError.
// Three is enough to capture a typical shell error like "/bin/sh: 1: npm:
// not found" plus one or two contextual lines, while staying short enough
// not to crowd the rest of the launch error in a UI banner.
const serviceExitTailLines = 3

// serviceExitTailRunes caps the rune length of the joined tail rendered
// into a service-exit error so a runaway log line (a stack trace, a
// minified JSON dump) cannot inflate the API response. Sized to leave
// room for the wrapping "preview service did not pass its readiness probe…"
// chrome that classifyLaunchError prepends.
const serviceExitTailRunes = 200

const previewDialTimeout = 5 * time.Second

// DockerPreviewOption configures a DockerPreviewProvider.
type DockerPreviewOption func(*DockerPreviewProvider)

// WithPreviewNetwork sets the Docker network for preview infrastructure containers.
func WithPreviewNetwork(network string) DockerPreviewOption {
	return func(p *DockerPreviewProvider) {
		p.network = network
	}
}

// WithPreviewDialer overrides worker-to-sandbox TCP dialing. It is intended
// for tests; production uses the default net.Dialer path.
func WithPreviewDialer(dialer previewDialer) DockerPreviewOption {
	return func(p *DockerPreviewProvider) {
		if dialer != nil {
			p.dialer = dialer
		}
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
		dialer:   defaultPreviewDialer,
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

func (d *DockerPreviewProvider) StartPreview(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, extraEnv map[string]string, observer preview.ServiceObserver) (*preview.PreviewHandle, error) {
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

	infraCreds := make(map[string]preview.InfraCredential)
	var svcEnvs map[string]map[string]string
	if err := func() (phaseErr error) {
		notifyPhaseStart(observer, "install_build")
		defer func() { notifyPhaseEnd(observer, "install_build", phaseErr) }()

		// Phase 1: Provision infrastructure containers.
		for name, infraCfg := range cfg.Infrastructure {
			tmpl, ok := preview.LookupInfraTemplate(infraCfg.Template)
			if !ok {
				phaseErr = fmt.Errorf("unknown infrastructure template %q", infraCfg.Template)
				return phaseErr
			}

			ih, err := d.provisionInfra(ctx, sb, handle, name, infraCfg, tmpl)
			if err != nil {
				phaseErr = fmt.Errorf("provision infrastructure %q: %w", name, err)
				return phaseErr
			}
			d.mu.Lock()
			state.infra[name] = ih
			d.mu.Unlock()
			infraCreds[name] = ih.Credential
		}

		// Phase 2: Wait for infrastructure health.
		for name, ih := range state.infra {
			infraCfg := cfg.Infrastructure[name]
			tmpl, _ := preview.LookupInfraTemplate(infraCfg.Template)
			if err := d.waitForInfraHealth(ctx, ih.ContainerID, tmpl); err != nil {
				phaseErr = fmt.Errorf("%w: infrastructure %q (%s): %v", preview.ErrInfraUnhealthy, name, infraCfg.Template, err)
				return phaseErr
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
				phaseErr = fmt.Errorf("%w: infrastructure %q script %q: %v", preview.ErrInitScriptFailed, name, infraCfg.InitScript, err)
				return phaseErr
			}
			d.logger.Info().Str("infra", name).Str("script", infraCfg.InitScript).Msg("init script completed")
		}

		// Phase 4: Run the platform-managed install phase before services start.
		if err := d.runPreviewInstall(ctx, state, cfg.Install, observer); err != nil {
			phaseErr = fmt.Errorf("%w: %v", preview.ErrInstallFailed, err)
			return phaseErr
		}

		// Phase 5: Build service environment with injected credentials.
		svcEnvs = d.buildServiceEnvs(cfg, infraCreds, extraEnv)
		return nil
	}(); err != nil {
		d.cleanupState(handle)
		return nil, err
	}

	// Phase 6: Start application services in dependency order
	// (support services first, then primary).
	primaryPort := 0
	if err := func() (phaseErr error) {
		notifyPhaseStart(observer, "start_services")
		defer func() { notifyPhaseEnd(observer, "start_services", phaseErr) }()
		for name, svcCfg := range cfg.Services {
			if name == cfg.Primary {
				continue // primary starts last
			}
			if err := d.startService(svcCtx, state, name, svcCfg, svcEnvs[name], observer); err != nil {
				phaseErr = fmt.Errorf("start service %q: %w", name, err)
				return phaseErr
			}
		}
		// Start primary service.
		if primaryCfg, ok := cfg.Services[cfg.Primary]; ok {
			primaryPort = primaryCfg.Port
			if err := d.startService(svcCtx, state, cfg.Primary, primaryCfg, svcEnvs[cfg.Primary], observer); err != nil {
				phaseErr = fmt.Errorf("start primary service %q: %w", cfg.Primary, err)
				return phaseErr
			}
		}
		return nil
	}(); err != nil {
		d.cleanupState(handle)
		return nil, err
	}
	state.primaryPort = primaryPort

	// Phase 7: Wait for readiness probes.
	//
	// Progressive preview: when cfg.Progressive is true and this is a
	// multi-service config, report readiness as soon as the primary service
	// passes its probe. Support services continue starting in the background.
	// The caller receives a PartiallyReady flag on the handle so the manager
	// can set the correct status.
	partiallyReady := false
	if err := func() (phaseErr error) {
		notifyPhaseStart(observer, "readiness")
		defer func() { notifyPhaseEnd(observer, "readiness", phaseErr) }()

		if cfg.Progressive && len(cfg.Services) > 1 {
			// Wait for primary first.
			if primaryCfg, ok := cfg.Services[cfg.Primary]; ok {
				timeout := 90 * time.Second
				if primaryCfg.Ready.TimeoutSeconds > 0 {
					timeout = time.Duration(primaryCfg.Ready.TimeoutSeconds) * time.Second
				}
				if err := d.waitForReadiness(ctx, state, cfg.Primary, primaryCfg.Port, primaryCfg.Ready.HTTPPath, timeout); err != nil {
					errMsg, tail := d.recordServiceReadinessFailure(state, cfg.Primary, err)
					notifyServiceFailed(observer, cfg.Primary, errMsg, tail)
					phaseErr = fmt.Errorf("%w: primary service %q (port %d): %s", preview.ErrServiceNotReady, cfg.Primary, primaryCfg.Port, errMsg)
					return phaseErr
				}
				if err := d.verifyPrimaryReachable(ctx, state, cfg.Primary, primaryCfg.Port); err != nil {
					errMsg, tail := d.recordServiceReadinessFailure(state, cfg.Primary, err)
					notifyServiceFailed(observer, cfg.Primary, errMsg, tail)
					phaseErr = fmt.Errorf("%w: primary service %q (port %d): %s", preview.ErrServiceNotReady, cfg.Primary, primaryCfg.Port, errMsg)
					return phaseErr
				}
				d.mu.Lock()
				state.services[cfg.Primary].status = models.PreviewServiceStatusReady
				pid := state.services[cfg.Primary].pid
				d.mu.Unlock()
				d.logger.Info().Str("service", cfg.Primary).Int("port", primaryCfg.Port).Msg("primary service ready (progressive)")
				notifyServiceReady(observer, cfg.Primary, primaryCfg.Port, pid)
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
					if err := d.waitForReadiness(bgCtx, state, name, svcCfg.Port, svcCfg.Ready.HTTPPath, timeout); err != nil {
						errMsg, tail := d.recordServiceReadinessFailure(state, name, err)
						d.logger.Warn().Err(err).Str("service", name).Strs("output_tail", tail).Msg("support service readiness failed (progressive)")
						notifyServiceFailed(observer, name, errMsg, tail)
					} else {
						d.mu.Lock()
						var pid int
						if ss, ok := state.services[name]; ok {
							ss.status = models.PreviewServiceStatusReady
							pid = ss.pid
						}
						d.mu.Unlock()
						d.logger.Info().Str("service", name).Int("port", svcCfg.Port).Msg("support service ready (progressive)")
						notifyServiceReady(observer, name, svcCfg.Port, pid)
					}
					cancel()
				}
			}()
			return nil
		}

		// Standard: wait for all services before reporting ready.
		for name, svcCfg := range cfg.Services {
			timeout := 90 * time.Second
			if svcCfg.Ready.TimeoutSeconds > 0 {
				timeout = time.Duration(svcCfg.Ready.TimeoutSeconds) * time.Second
			}
			if err := d.waitForReadiness(ctx, state, name, svcCfg.Port, svcCfg.Ready.HTTPPath, timeout); err != nil {
				errMsg, tail := d.recordServiceReadinessFailure(state, name, err)
				notifyServiceFailed(observer, name, errMsg, tail)
				phaseErr = fmt.Errorf("%w: service %q (port %d): %s", preview.ErrServiceNotReady, name, svcCfg.Port, errMsg)
				return phaseErr
			}
			if name == cfg.Primary {
				if err := d.verifyPrimaryReachable(ctx, state, name, svcCfg.Port); err != nil {
					errMsg, tail := d.recordServiceReadinessFailure(state, name, err)
					notifyServiceFailed(observer, name, errMsg, tail)
					phaseErr = fmt.Errorf("%w: service %q (port %d): %s", preview.ErrServiceNotReady, name, svcCfg.Port, errMsg)
					return phaseErr
				}
			}
			d.mu.Lock()
			state.services[name].status = models.PreviewServiceStatusReady
			pid := state.services[name].pid
			d.mu.Unlock()
			d.logger.Info().Str("service", name).Int("port", svcCfg.Port).Msg("service ready")
			notifyServiceReady(observer, name, svcCfg.Port, pid)
		}
		return nil
	}(); err != nil {
		d.cleanupState(handle)
		return nil, err
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
	d.terminateServiceProcesses(state)
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

const serviceTerminateTimeout = 5 * time.Second

type serviceProcessTarget struct {
	name string
	pid  int
	port int
}

func (d *DockerPreviewProvider) terminateServiceProcesses(state *previewState) {
	if d.executor == nil || state.sandbox == nil {
		return
	}

	d.mu.RLock()
	targets := make([]serviceProcessTarget, 0, len(state.services))
	for name, ss := range state.services {
		targets = append(targets, serviceProcessTarget{
			name: name,
			pid:  ss.pid,
			port: ss.port,
		})
	}
	d.mu.RUnlock()
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].name < targets[j].name
	})

	for _, target := range targets {
		if target.pid <= 0 && target.port <= 0 {
			continue
		}

		termCtx, cancel := context.WithTimeout(context.Background(), serviceTerminateTimeout)
		var stderr bytes.Buffer
		exitCode, err := d.executor.Exec(termCtx, state.sandbox, buildTerminateServiceProcessCmd(target.pid, target.port), io.Discard, &stderr)
		cancel()
		if err != nil {
			d.logger.Warn().Err(err).Str("service", target.name).Msg("failed to terminate preview service process")
			continue
		}
		if exitCode != 0 {
			d.logger.Warn().
				Str("service", target.name).
				Int("exit_code", exitCode).
				Str("stderr", strings.TrimSpace(stderr.String())).
				Msg("preview service process termination command exited non-zero")
		}
	}
}

func buildTerminateServiceProcessCmd(pid, port int) string {
	var explicitPID string
	if pid > 0 {
		explicitPID = fmt.Sprintf("printf '%%s\\n' %d", pid)
	} else {
		explicitPID = ":"
	}

	var portPIDs string
	if port > 0 {
		portPIDs = fmt.Sprintf("if command -v lsof >/dev/null 2>&1; then lsof -ti :%d 2>/dev/null || true; fi", port)
	} else {
		portPIDs = ":"
	}

	collectPIDs := fmt.Sprintf("{ %s; %s; } | awk 'NF && !seen[$1]++'", explicitPID, portPIDs)
	return fmt.Sprintf(`pids="$(%s)"; if [ -n "$pids" ]; then kill $pids 2>/dev/null || true; sleep 1; kill -9 $pids 2>/dev/null || true; fi`, collectPIDs)
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
	conn, err := d.dialPreviewAddr(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("dial preview at %s: %w", addr, err)
	}

	return &tcpPreviewStream{Conn: conn}, nil
}

func (d *DockerPreviewProvider) verifyPrimaryReachable(ctx context.Context, state *previewState, name string, port int) error {
	sandboxIP, err := d.getSandboxIP(ctx, state.sandbox.ID)
	if err != nil {
		return fmt.Errorf("external reachability check failed for service %q: resolve sandbox IP: %w", name, err)
	}

	addr := net.JoinHostPort(sandboxIP, fmt.Sprintf("%d", port))
	conn, err := d.dialPreviewAddr(ctx, addr)
	if err != nil {
		return fmt.Errorf("external reachability check failed for service %q at %s: %w", name, addr, err)
	}
	if err := conn.Close(); err != nil {
		d.logger.Debug().Err(err).Str("service", name).Str("addr", addr).Msg("external preview reachability probe close failed")
	}
	return nil
}

func (d *DockerPreviewProvider) dialPreviewAddr(ctx context.Context, addr string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, previewDialTimeout)
	defer cancel()
	return d.dialer(dialCtx, addr)
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
			host: ih.Credential.Host, port: ih.Credential.Port,
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

	handlePrefix := previewHandle
	if len(handlePrefix) > 12 {
		handlePrefix = handlePrefix[:12]
	}
	containerName := fmt.Sprintf("preview-%s-%s", infraName, handlePrefix)
	cred, err := buildInfraCredential(infraName, containerName, tmpl.DefaultPort)
	if err != nil {
		return nil, fmt.Errorf("generate credential for %q: %w", infraName, err)
	}

	// Ensure the image is on the host before asking Docker to create the
	// container. Worker hosts only pre-pull the 143-server / 143-sandbox /
	// headless-shell images; infrastructure templates (postgres-N,
	// redis-N, mysql-N) are pulled lazily on first use so adding a new
	// template doesn't require re-provisioning workers.
	if err := d.ensureImage(ctx, tmpl.Image); err != nil {
		return nil, err
	}

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
		return nil, fmt.Errorf("%w: create container for image %q: %v", preview.ErrInfraStartFailed, tmpl.Image, err)
	}

	if err := d.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Cleanup the created container on start failure.
		cleanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if rmErr := d.client.ContainerRemove(cleanCtx, resp.ID, container.RemoveOptions{Force: true}); rmErr != nil {
			d.logger.Warn().Err(rmErr).Str("container_id", resp.ID).Msg("failed to remove container after start failure")
		}
		return nil, fmt.Errorf("%w: start container: %v", preview.ErrInfraStartFailed, err)
	}

	return &preview.InfraHandle{
		InfraName:   infraName,
		Template:    infraCfg.Template,
		ContainerID: resp.ID,
		Credential:  cred,
	}, nil
}

// ensureImage makes sure the named image is present on the local Docker
// host, pulling it from the registry on demand if it isn't. Concurrent
// callers for the same image ref share a single pull (singleflight) so two
// preview starts hitting a missing image don't fan out two redundant
// streams.
//
// Failures are wrapped with preview.ErrInfraImageUnavailable so the HTTP
// handler can map them to a specific error code that names the image, rather
// than burying the cause inside a generic "failed to start preview".
func (d *DockerPreviewProvider) ensureImage(ctx context.Context, ref string) error {
	_, err, _ := d.imagePulls.Do(ref, func() (any, error) {
		return nil, d.pullImage(ctx, ref)
	})
	return err
}

func (d *DockerPreviewProvider) pullImage(ctx context.Context, ref string) error {
	if _, err := d.client.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%w: inspect %q: %v", preview.ErrInfraImageUnavailable, ref, err)
	}

	d.logger.Info().Str("image", ref).Msg("preview infra image missing on host; pulling from registry")
	start := time.Now()

	pullCtx, cancel := context.WithTimeout(ctx, imagePullTimeout)
	defer cancel()

	rc, err := d.client.ImagePull(pullCtx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("%w: pull %q: %v", preview.ErrInfraImageUnavailable, ref, err)
	}
	defer rc.Close()

	// Docker streams pull progress as one JSON object per line. Errors
	// surface as `{"errorDetail": {...}, "error": "..."}` events that the
	// daemon does NOT report via the ImagePull return value — if we just
	// drain to /dev/null we lose the actual reason (auth failure, manifest
	// unknown, rate limit) and only see "image not present after pull",
	// which is useless for debugging.
	if pullErr := scanPullStreamForError(rc); pullErr != nil {
		return fmt.Errorf("%w: pull %q: %v", preview.ErrInfraImageUnavailable, ref, pullErr)
	}

	// Confirm the image actually landed. Catches the rare case where the
	// stream finished cleanly but no errorDetail was emitted yet the image
	// isn't present (e.g., daemon-side race during prune).
	if _, err := d.client.ImageInspect(pullCtx, ref); err != nil {
		return fmt.Errorf("%w: image %q not present after pull: %v", preview.ErrInfraImageUnavailable, ref, err)
	}

	d.logger.Info().Str("image", ref).Dur("elapsed", time.Since(start)).Msg("preview infra image pulled successfully")
	return nil
}

// scanPullStreamForError reads Docker's pull-progress NDJSON stream, drains
// it (the pull only completes once the daemon-side reader is done), and
// returns the first errorDetail it sees (or nil for a clean pull). The
// stream format is documented at
// https://docs.docker.com/reference/api/engine/version/v1.45/#tag/Image/operation/ImageCreate
// — each line is a JSON object with optional `status`, `progress`,
// `errorDetail`, or `error` fields.
func scanPullStreamForError(r io.Reader) error {
	type pullEvent struct {
		Error       string `json:"error"`
		ErrorDetail struct {
			Message string `json:"message"`
		} `json:"errorDetail"`
	}
	var firstErr error
	scanner := bufio.NewScanner(r)
	// Pull progress events are short, but a Pulling-from line on a big
	// image can stretch; bump the buffer ceiling well above bufio's
	// default 64KiB so we never trip on it.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev pullEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// Malformed line; ignore and keep draining so the daemon
			// finishes writing.
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
// Preview install management
// =============================================================================

const previewInstallRuntimeVersion = "docker-preview-install-v1"

type previewInstallCacheKey struct {
	RuntimeVersion  string                      `json:"runtime_version"`
	SandboxProvider string                      `json:"sandbox_provider,omitempty"`
	SandboxImage    string                      `json:"sandbox_image,omitempty"`
	Command         []string                    `json:"command"`
	Cwd             string                      `json:"cwd"`
	Lockfiles       []previewInstallLockfileKey `json:"lockfiles"`
	CleanPaths      []string                    `json:"clean_paths"`
	VerifyPaths     []string                    `json:"verify_paths"`
	TimeoutSeconds  int                         `json:"timeout_seconds"`
}

type previewInstallLockfileKey struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func (d *DockerPreviewProvider) runPreviewInstall(ctx context.Context, state *previewState, install *models.PreviewInstallConfig, observer preview.ServiceObserver) error {
	if install == nil {
		return nil
	}
	timeout := time.Duration(install.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(preview.DefaultInstallTimeoutSeconds) * time.Second
	}

	cacheKey, err := d.computePreviewInstallCacheKey(ctx, state.sandbox, install)
	if err != nil {
		notifyInstallFailed(observer, err.Error(), nil)
		return err
	}
	markerPath := fmt.Sprintf(".143/cache/preview-install/%s.done", cacheKey)
	if d.previewInstallCacheValid(ctx, state.sandbox, markerPath, install.VerifyPaths) {
		d.logger.Info().Str("marker", markerPath).Msg("preview install cache hit")
		return nil
	}

	cmd, err := buildPreviewInstallCommand(install, markerPath)
	if err != nil {
		notifyInstallFailed(observer, err.Error(), nil)
		return err
	}
	outputTail := make([]string, 0, serviceTailLines)
	appendTail := func(line []byte) {
		text := string(line)
		if len(outputTail) >= serviceTailLines {
			outputTail = outputTail[1:]
		}
		outputTail = append(outputTail, text)
		d.logger.Debug().Str("output", text).Msg("preview install output")
	}
	installCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stderrSplitter := &previewLineSplitter{onLine: appendTail}
	exitCode, err := d.executor.ExecStream(installCtx, state.sandbox, cmd, appendTail, stderrSplitter)
	stderrSplitter.flush()
	if err != nil || exitCode != 0 {
		errMsg := formatPreviewInstallError(exitCode, err, timeout, installCtx.Err())
		notifyInstallFailed(observer, errMsg, outputTail)
		return fmt.Errorf("%s", errMsg)
	}
	d.logger.Info().Str("marker", markerPath).Msg("preview install completed")
	return nil
}

func (d *DockerPreviewProvider) computePreviewInstallCacheKey(ctx context.Context, sb *agent.Sandbox, install *models.PreviewInstallConfig) (string, error) {
	lockfiles := make([]previewInstallLockfileKey, 0, len(install.Lockfiles))
	for _, lockfile := range install.Lockfiles {
		cleanPath, err := cleanPreviewInstallRepoPath(lockfile, false)
		if err != nil {
			return "", fmt.Errorf("preview.install.lockfiles path %q: %w", lockfile, err)
		}
		body, err := d.executor.ReadFile(ctx, sb, cleanPath)
		if err != nil {
			return "", fmt.Errorf("read preview.install lockfile %q: %w", cleanPath, err)
		}
		sum := sha256.Sum256(body)
		lockfiles = append(lockfiles, previewInstallLockfileKey{Path: cleanPath, SHA256: fmt.Sprintf("%x", sum[:])})
	}
	sort.Slice(lockfiles, func(i, j int) bool { return lockfiles[i].Path < lockfiles[j].Path })

	cwd := install.Cwd
	if cwd == "" {
		cwd = "."
	}
	if _, err := cleanPreviewInstallRepoPath(cwd, false); err != nil {
		return "", fmt.Errorf("preview.install.cwd %q: %w", cwd, err)
	}
	key := previewInstallCacheKey{
		RuntimeVersion: previewInstallRuntimeVersion,
		Command:        append([]string(nil), install.Command...),
		Cwd:            cwd,
		Lockfiles:      lockfiles,
		CleanPaths:     sortedStringCopy(install.CleanPaths),
		VerifyPaths:    sortedStringCopy(install.VerifyPaths),
		TimeoutSeconds: install.TimeoutSeconds,
	}
	if sb != nil {
		key.SandboxProvider = sb.Provider
		if sb.Metadata != nil {
			key.SandboxImage = sb.Metadata["image"]
		}
	}
	payload, err := json.Marshal(key)
	if err != nil {
		return "", fmt.Errorf("marshal preview install cache key: %w", err)
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum[:]), nil
}

func (d *DockerPreviewProvider) previewInstallCacheValid(ctx context.Context, sb *agent.Sandbox, markerPath string, verifyPaths []string) bool {
	if _, err := d.executor.ReadFile(ctx, sb, markerPath); err != nil {
		return false
	}
	for _, verifyPath := range verifyPaths {
		cleanPath, err := cleanPreviewInstallRepoPath(verifyPath, false)
		if err != nil {
			return false
		}
		if !d.previewInstallPathExists(ctx, sb, cleanPath) {
			return false
		}
	}
	return true
}

func (d *DockerPreviewProvider) previewInstallPathExists(ctx context.Context, sb *agent.Sandbox, repoPath string) bool {
	exitCode, err := d.executor.Exec(ctx, sb, "test -e "+shellEscape(repoPath), io.Discard, io.Discard)
	return err == nil && exitCode == 0
}

func buildPreviewInstallCommand(install *models.PreviewInstallConfig, markerPath string) (string, error) {
	var parts []string
	parts = append(parts, "mkdir -p .143/cache/preview-install")
	if len(install.CleanPaths) > 0 {
		cleanArgs := make([]string, 0, len(install.CleanPaths))
		for _, cleanPath := range install.CleanPaths {
			arg, err := cleanPreviewInstallRepoPath(cleanPath, true)
			if err != nil {
				return "", fmt.Errorf("preview.install.clean_paths path %q: %w", cleanPath, err)
			}
			cleanArgs = append(cleanArgs, arg)
		}
		parts = append(parts, "rm -rf -- "+strings.Join(cleanArgs, " "))
	}

	escapedCmd := make([]string, 0, len(install.Command))
	for _, arg := range install.Command {
		escapedCmd = append(escapedCmd, shellEscape(arg))
	}
	installCmd := strings.Join(escapedCmd, " ")
	cwd := install.Cwd
	if cwd == "" {
		cwd = "."
	}
	if cwd != "." {
		cleanCwd, err := cleanPreviewInstallRepoPath(cwd, false)
		if err != nil {
			return "", fmt.Errorf("preview.install.cwd %q: %w", cwd, err)
		}
		installCmd = fmt.Sprintf("(cd %s && %s)", shellEscape(cleanCwd), installCmd)
	}
	parts = append(parts, installCmd)
	parts = append(parts, fmt.Sprintf("printf 'ok\\n' > %s", shellEscape(markerPath)))
	return strings.Join(parts, " && "), nil
}

func cleanPreviewInstallRepoPath(raw string, allowGlob bool) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	if strings.ContainsAny(raw, " \t\r\n;&|`$(){}[]<>!?\\\"'") {
		return "", fmt.Errorf("unsupported shell metacharacter")
	}
	if !allowGlob && strings.Contains(raw, "*") {
		return "", fmt.Errorf("glob paths are not allowed here")
	}
	clean := path.Clean(raw)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes the repo root")
	}
	if allowGlob {
		for _, part := range strings.Split(clean, "/") {
			if part == ".." {
				return "", fmt.Errorf("path escapes the repo root")
			}
		}
	}
	if allowGlob && clean == "." {
		return "", fmt.Errorf("path is too broad to clean")
	}
	return clean, nil
}

func sortedStringCopy(values []string) []string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	return copied
}

func formatPreviewInstallError(exitCode int, err error, timeout time.Duration, ctxErr error) string {
	if ctxErr != nil {
		return fmt.Sprintf("timed out after %s", timeout)
	}
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("exited with code %d", exitCode)
}

func notifyInstallFailed(observer preview.ServiceObserver, errMsg string, tail []string) {
	if observer == nil {
		return
	}
	observer.OnInstallFailed(errMsg, tail)
}

// =============================================================================
// Service management
// =============================================================================

// notifyServiceReady invokes observer.OnServiceReady when observer is non-nil.
// Centralised so callers can stay nil-safe without scattering checks.
func notifyServiceReady(observer preview.ServiceObserver, name string, port, pid int) {
	if observer == nil {
		return
	}
	observer.OnServiceReady(name, port, pid)
}

// notifyServiceFailed invokes observer.OnServiceFailed when observer is non-nil.
func notifyServiceFailed(observer preview.ServiceObserver, name, errMsg string, tail []string) {
	if observer == nil {
		return
	}
	observer.OnServiceFailed(name, errMsg, tail)
}

type phaseObserver interface {
	OnPhaseStart(name string)
	OnPhaseEnd(name string, err error)
}

func notifyPhaseStart(observer preview.ServiceObserver, name string) {
	if observer == nil {
		return
	}
	if phaseObserver, ok := observer.(phaseObserver); ok {
		phaseObserver.OnPhaseStart(name)
	}
}

func notifyPhaseEnd(observer preview.ServiceObserver, name string, err error) {
	if observer == nil {
		return
	}
	if phaseObserver, ok := observer.(phaseObserver); ok {
		phaseObserver.OnPhaseEnd(name, err)
	}
}

// recordServiceReadinessFailure snapshots the live service output before
// cleanup cancels the process. A timeout usually means the process is still
// running, so this is the only point where we can preserve boot progress such
// as "building binaries", "running migrations", or dependency downloads.
func (d *DockerPreviewProvider) recordServiceReadinessFailure(state *previewState, name string, err error) (string, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if ss, ok := state.services[name]; ok {
		ss.status = models.PreviewServiceStatusFailed
		ss.err = formatServiceReadinessError(err, ss.outputTail)
		return ss.err, append([]string(nil), ss.outputTail...)
	}
	return err.Error(), nil
}

func formatServiceReadinessError(err error, outputTail []string) string {
	base := err.Error()
	tail := truncatedTail(outputTail, serviceExitTailLines, serviceExitTailRunes)
	if tail == "" {
		return base
	}
	return base + "; last output: " + tail
}

// formatServiceExitError builds a human-readable exit message from a service's
// exit code plus the last few lines of its stdout/stderr. Bare "exited with
// code N" — especially the POSIX "command not found" code 127 — leaves the
// user staring at a number without any of the context the shell already
// printed to stderr (e.g. "/bin/sh: 1: npm: not found"). Surfacing the tail
// here lets that output reach the launch error returned to the API, not just
// the preview_logs row.
func formatServiceExitError(exitCode int, outputTail []string) string {
	hint := ""
	if exitCode == 127 {
		hint = " (command not found — check that the executable exists on the sandbox's $PATH or use an absolute path in .143/config.json)"
	}
	base := fmt.Sprintf("exited with code %d%s", exitCode, hint)
	tail := truncatedTail(outputTail, serviceExitTailLines, serviceExitTailRunes)
	if tail == "" {
		return base
	}
	return base + "; last output: " + tail
}

// truncatedTail returns up to the last maxLines non-blank lines of
// outputTail joined into a single string, capped at maxRunes runes with an
// ellipsis so a runaway log line can't blow up the surfacing error message.
// Returns "" when outputTail has no usable content.
//
// Walks from the newest line back so trailing blank lines (a service that
// ends with a flush of "\n\n" or pads its readiness logs with separators)
// don't degrade the message into a useless tail. We still bound the scan at
// the full ring buffer length, so an O(serviceTailLines) walk is the
// worst case.
func truncatedTail(outputTail []string, maxLines, maxRunes int) string {
	if len(outputTail) == 0 || maxLines <= 0 {
		return ""
	}
	parts := make([]string, 0, maxLines)
	for i := len(outputTail) - 1; i >= 0 && len(parts) < maxLines; i-- {
		trimmed := strings.TrimSpace(strings.TrimRight(outputTail[i], "\r\n"))
		if trimmed == "" {
			continue
		}
		parts = append(parts, trimmed)
	}
	if len(parts) == 0 {
		return ""
	}
	// parts is newest-first from the reverse walk; flip to chronological so
	// the joined message reads in the order the service actually printed.
	for lo, hi := 0, len(parts)-1; lo < hi; lo, hi = lo+1, hi-1 {
		parts[lo], parts[hi] = parts[hi], parts[lo]
	}
	joined := strings.Join(parts, " | ")
	if maxRunes > 0 {
		runes := []rune(joined)
		if len(runes) > maxRunes {
			joined = string(runes[:maxRunes]) + "…"
		}
	}
	return joined
}

func (d *DockerPreviewProvider) startService(
	ctx context.Context,
	state *previewState,
	name string,
	svcCfg models.ServiceConfig,
	env map[string]string,
	observer preview.ServiceObserver,
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

		// appendTail records one line into the per-service ring buffer so we
		// can replay it to the observer when the service fails. The closure
		// is shared by stdout and stderr so a process that only logs errors
		// to stderr (every Go binary using log.Print, every `go build` error)
		// still surfaces a useful tail instead of an empty buffer.
		appendTail := func(line string) {
			d.mu.Lock()
			if len(ss.outputTail) >= serviceTailLines {
				ss.outputTail = ss.outputTail[1:]
			}
			ss.outputTail = append(ss.outputTail, line)
			d.mu.Unlock()
			d.logger.Debug().Str("service", name).Str("output", line).Msg("service output")
		}
		stderrSplitter := &previewLineSplitter{onLine: func(line []byte) {
			appendTail(string(line))
		}}
		exitCode, err := d.executor.ExecStream(svcCtx, state.sandbox, cmd, func(line []byte) {
			// Copy the bytes because ExecStream reuses the slice across calls.
			appendTail(string(line))
		}, stderrSplitter)
		stderrSplitter.flush()

		d.mu.Lock()
		var tail []string
		// If svcCtx was canceled, the service was torn down intentionally
		// (StopPreview, or readiness-failure cleanup tearing down siblings).
		// ExecStream's hijacked-connection watcher closes the read mid-stream
		// in that case and returns a "read exec output" error, but that's the
		// expected shape of an intentional stop — not a real service failure.
		// Without this check, a healthy service torn down during cleanup would
		// be reported to observers as Failed instead of Stopped.
		stoppedByCancel := svcCtx.Err() != nil && err != nil
		failed := !stoppedByCancel && (err != nil || exitCode != 0)
		if failed {
			ss.status = models.PreviewServiceStatusFailed
			if err != nil {
				ss.err = err.Error()
			} else {
				ss.err = formatServiceExitError(exitCode, ss.outputTail)
			}
			tail = append([]string(nil), ss.outputTail...)
		} else {
			ss.status = models.PreviewServiceStatusStopped
		}
		errMsg := ss.err
		d.mu.Unlock()

		if failed {
			// Surface the tail at error level so it shows up in worker logs
			// for ops debugging — the ExecStream callback only logs each line
			// at debug, which is filtered out in production.
			evt := d.logger.Error().Str("service", name).Int("exit_code", exitCode).Err(err)
			if len(tail) > 0 {
				evt = evt.Strs("output_tail", tail)
			}
			evt.Msg("service exited")
			notifyServiceFailed(observer, name, errMsg, tail)
		}
	}()

	return nil
}

// readinessProbeAttemptTimeout caps how long a single curl-via-docker-exec
// call inside the sandbox is allowed to take. Without this, a wedged docker
// daemon can stretch the overall readiness budget far past its declared
// timeout — the for-select can only react to deadline.C *between* probe
// attempts, so a slow exec stalls the timer too.
const readinessProbeAttemptTimeout = 5 * time.Second

func (d *DockerPreviewProvider) waitForReadiness(ctx context.Context, state *previewState, name string, port int, httpPath string, timeout time.Duration) error {
	overallCtx, cancelOverall := context.WithTimeout(ctx, timeout)
	defer cancelOverall()
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	// Use curl inside the sandbox to check readiness.
	// Shell-escape the path defensively even though ValidateConfig restricts characters.
	cmd := fmt.Sprintf("curl -sf -o /dev/null %s", shellEscape(fmt.Sprintf("http://localhost:%d%s", port, httpPath)))

	// Snapshot of the service status read each tick. If the goroutine
	// running the service in startService has already set Failed/Stopped
	// (i.e. the process exited before becoming ready), there is no point
	// continuing to poll — bail immediately with the captured error.
	checkExited := func() error {
		d.mu.RLock()
		defer d.mu.RUnlock()
		ss, ok := state.services[name]
		if !ok {
			return nil
		}
		switch ss.status {
		case models.PreviewServiceStatusFailed:
			if ss.err != "" {
				return fmt.Errorf("service %q exited before becoming ready: %s", name, ss.err)
			}
			return fmt.Errorf("service %q exited before becoming ready", name)
		case models.PreviewServiceStatusStopped:
			return fmt.Errorf("service %q stopped before becoming ready", name)
		}
		return nil
	}

	// timeoutErr maps overallCtx.Err() back to a caller-friendly error,
	// distinguishing a parent-ctx cancellation from our own deadline.
	timeoutErr := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("readiness probe timed out after %s", timeout)
	}

	for {
		// Check the wall-clock deadline first, before re-entering select. A
		// time.NewTimer + select on deadline.C used to handle this, but when
		// a hung Exec returned at the same instant as a buffered tick.C and
		// a buffered deadline.C, Go's select picked pseudo-randomly between
		// the two and a string of unlucky picks could stretch the loop far
		// past `timeout`. Wall-clock check makes the deadline deterministic.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return timeoutErr()
		}
		if err := checkExited(); err != nil {
			return err
		}
		// Check overall deadline before re-entering select: if a hung Exec
		// just returned because per-attempt timeout cancelled it, both
		// tick.C and overallCtx.Done() may be ready and select would pick
		// uniformly at random. We want the deadline to win deterministically.
		if overallCtx.Err() != nil {
			return timeoutErr()
		}
		// Cap the per-attempt timeout at the remaining budget so a wedged
		// docker daemon cannot stretch the loop beyond `timeout` by even one
		// attempt's worth of time.
		attemptTimeout := readinessProbeAttemptTimeout
		if remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		select {
		case <-overallCtx.Done():
			return timeoutErr()
		case <-tick.C:
			execCtx, cancel := context.WithTimeout(overallCtx, attemptTimeout)
			exitCode, _ := d.executor.Exec(execCtx, state.sandbox, cmd, io.Discard, io.Discard)
			cancel()
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
	// overriding any user-declared value. Applied last so it wins over both
	// service.env and infrastructure.inject_env. Mutation in place is fine —
	// env is the same map reference stored in envs[svcName].
	for _, env := range envs {
		for k, v := range extraEnv {
			env[k] = v
		}
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

// buildInfraCredential constructs a fully-populated InfraCredential. All five
// fields (Host, Port, Username, Password, Database) must be set on the
// returned value — resolveCredentialTemplate substitutes every one of them
// into the inject_env DSN and a zero Host/Port silently produces a broken URL
// that the consuming service tries to dial against localhost.
func buildInfraCredential(infraName, host string, port int) (preview.InfraCredential, error) {
	password, err := preview.RandomHex(16)
	if err != nil {
		return preview.InfraCredential{}, fmt.Errorf("generate infra credential password: %w", err)
	}
	return preview.InfraCredential{
		Host:     host,
		Port:     port,
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

// previewLineSplitter is an io.Writer that calls onLine for each newline-
// delimited line written to it. Used as the stderr sink for ExecStream so
// stderr is fed into the same per-service ring buffer as stdout. The trailing
// flush() drains a partial final line that wasn't terminated by '\n' (a
// common shape for `go build` errors and panic stacks that exit without a
// newline) so it isn't silently lost.
type previewLineSplitter struct {
	onLine func(line []byte)
	buf    bytes.Buffer
}

func (l *previewLineSplitter) Write(p []byte) (int, error) {
	n := len(p)
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadBytes('\n')
		if err != nil {
			l.buf.Write(line)
			break
		}
		l.onLine(bytes.TrimRight(line, "\n"))
	}
	return n, nil
}

func (l *previewLineSplitter) flush() {
	if l.buf.Len() > 0 {
		l.onLine(l.buf.Bytes())
		l.buf.Reset()
	}
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
