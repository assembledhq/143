package worker

import (
	"context"
	"fmt"
	"os"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
)

type dockerExecutorClient interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
}

type DockerExecutorLauncherConfig struct {
	Image       string
	NetworkMode string
	Binds       []string
	GroupAdd    []string
	Env         []string
	StopTimeout time.Duration
}

type DockerExecutorLauncher struct {
	client dockerExecutorClient
	cfg    DockerExecutorLauncherConfig
	logger zerolog.Logger
}

func NewDockerExecutorLauncher(client *client.Client, cfg DockerExecutorLauncherConfig) *DockerExecutorLauncher {
	if len(cfg.Env) == 0 {
		cfg.Env = os.Environ()
	}
	return &DockerExecutorLauncher{client: client, cfg: cfg}
}

func (l *DockerExecutorLauncher) Launch(ctx context.Context, spec ExecutorLaunchSpec) (ExecutorLaunchResult, error) {
	if l == nil || l.client == nil {
		return ExecutorLaunchResult{}, fmt.Errorf("docker executor launcher is not configured")
	}
	image := l.cfg.Image
	if image == "" {
		image = spec.Image
	}
	if image == "" {
		return ExecutorLaunchResult{}, fmt.Errorf("session executor image is required")
	}

	labels := map[string]string{
		"com.143.role":         "session-executor",
		"com.143.org_id":       spec.OrgID.String(),
		"com.143.session_id":   spec.SessionID.String(),
		"com.143.job_id":       spec.JobID.String(),
		"com.143.executor_id":  spec.ExecutorID.String(),
		"com.143.job_type":     spec.JobType,
		"com.143.host_node_id": spec.NodeID,
		"com.143.build_sha":    spec.BuildSHA,
	}
	if spec.ThreadID != nil {
		labels["com.143.thread_id"] = spec.ThreadID.String()
	}

	name := "143-session-executor-" + spec.ExecutorID.String()
	logger := l.loggerWithSpec(spec).With().
		Str("container_name", name).
		Str("image", image).
		Str("network_mode", l.cfg.NetworkMode).
		Logger()
	hostConfig := &container.HostConfig{
		Binds:    l.cfg.Binds,
		GroupAdd: l.cfg.GroupAdd,
	}
	if l.cfg.NetworkMode != "" {
		hostConfig.NetworkMode = container.NetworkMode(l.cfg.NetworkMode)
	}
	var stopTimeoutSeconds *int
	if l.cfg.StopTimeout > 0 {
		seconds := int(l.cfg.StopTimeout.Seconds())
		stopTimeoutSeconds = &seconds
	}
	resp, err := l.client.ContainerCreate(ctx, &container.Config{
		Image:       image,
		Cmd:         []string{"/bin/session-executor", "--executor-id", spec.ExecutorID.String()},
		Env:         append(append([]string{}, l.cfg.Env...), "SESSION_EXECUTOR_ID="+spec.ExecutorID.String()),
		Labels:      labels,
		StopTimeout: stopTimeoutSeconds,
	}, hostConfig, nil, nil, name)
	if err != nil {
		logger.Error().Err(err).Msg("failed to create session executor Docker container")
		return ExecutorLaunchResult{}, fmt.Errorf("create session executor container: %w", err)
	}
	logger.Info().Str("container_id", resp.ID).Msg("created session executor Docker container")
	if err := l.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		l.logContainerInspect(ctx, logger, resp.ID, "start_error")
		logger.Error().Err(err).Str("container_id", resp.ID).Msg("failed to start session executor Docker container")
		return ExecutorLaunchResult{ContainerID: resp.ID}, fmt.Errorf("start session executor container: %w", err)
	}
	logger.Info().Str("container_id", resp.ID).Msg("started session executor Docker container")
	l.logContainerInspect(ctx, logger, resp.ID, "post_start")
	return ExecutorLaunchResult{ContainerID: resp.ID}, nil
}

func (l *DockerExecutorLauncher) Cleanup(ctx context.Context, spec ExecutorLaunchSpec) error {
	if l == nil || l.client == nil {
		return fmt.Errorf("docker executor launcher is not configured")
	}
	name := "143-session-executor-" + spec.ExecutorID.String()
	logger := l.loggerWithSpec(spec).With().Str("container_name", name).Logger()
	l.logContainerInspect(ctx, logger, name, "pre_cleanup")
	logger.Info().Msg("removing session executor Docker container")
	err := l.client.ContainerRemove(ctx, name, container.RemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		logger.Warn().Err(err).Msg("failed to remove session executor Docker container")
		return fmt.Errorf("remove session executor container: %w", err)
	}
	if cerrdefs.IsNotFound(err) {
		logger.Info().Msg("session executor Docker container already removed")
	} else {
		logger.Info().Msg("removed session executor Docker container")
	}
	return nil
}

func (l *DockerExecutorLauncher) logContainerInspect(ctx context.Context, logger zerolog.Logger, containerID, phase string) {
	if l == nil || l.client == nil || containerID == "" {
		return
	}
	inspect, err := l.client.ContainerInspect(ctx, containerID)
	if err != nil {
		logger.Warn().Err(err).Str("container_id", containerID).Str("inspect_phase", phase).Msg("failed to inspect session executor Docker container")
		return
	}
	event := logger.Info().
		Str("container_id", inspect.ID).
		Str("container_name", inspect.Name).
		Str("inspect_phase", phase).
		Int("restart_count", inspect.RestartCount)
	if inspect.State != nil {
		event.
			Str("container_status", string(inspect.State.Status)).
			Bool("container_running", inspect.State.Running).
			Bool("container_restarting", inspect.State.Restarting).
			Bool("container_paused", inspect.State.Paused).
			Bool("container_dead", inspect.State.Dead).
			Bool("container_oom_killed", inspect.State.OOMKilled).
			Int("container_pid", inspect.State.Pid).
			Int("container_exit_code", inspect.State.ExitCode).
			Str("container_error", inspect.State.Error).
			Str("container_started_at", inspect.State.StartedAt).
			Str("container_finished_at", inspect.State.FinishedAt)
	}
	event.Msg("inspected session executor Docker container")
}

func (l *DockerExecutorLauncher) SetLogger(logger zerolog.Logger) {
	if l != nil {
		l.logger = logger
	}
}

func (l *DockerExecutorLauncher) loggerWithSpec(spec ExecutorLaunchSpec) zerolog.Logger {
	logger := zerolog.Nop()
	if l != nil && l.logger.GetLevel() != zerolog.Disabled {
		logger = l.logger
	}
	return logger.With().
		Str("org_id", spec.OrgID.String()).
		Str("session_id", spec.SessionID.String()).
		Str("job_id", spec.JobID.String()).
		Str("job_type", spec.JobType).
		Str("executor_id", spec.ExecutorID.String()).
		Str("host_node_id", spec.NodeID).
		Str("build_sha", spec.BuildSHA).
		Logger()
}
