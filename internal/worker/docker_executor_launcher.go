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
)

type dockerExecutorClient interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
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
}

func NewDockerExecutorLauncher(client *client.Client, cfg DockerExecutorLauncherConfig) *DockerExecutorLauncher {
	if len(cfg.Env) == 0 {
		cfg.Env = os.Environ()
	}
	return &DockerExecutorLauncher{client: client, cfg: cfg}
}

func (l *DockerExecutorLauncher) Launch(ctx context.Context, spec ExecutorLaunchSpec) error {
	if l == nil || l.client == nil {
		return fmt.Errorf("docker executor launcher is not configured")
	}
	image := l.cfg.Image
	if image == "" {
		image = spec.Image
	}
	if image == "" {
		return fmt.Errorf("session executor image is required")
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
		return fmt.Errorf("create session executor container: %w", err)
	}
	if err := l.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start session executor container: %w", err)
	}
	return nil
}

func (l *DockerExecutorLauncher) Cleanup(ctx context.Context, spec ExecutorLaunchSpec) error {
	if l == nil || l.client == nil {
		return fmt.Errorf("docker executor launcher is not configured")
	}
	name := "143-session-executor-" + spec.ExecutorID.String()
	err := l.client.ContainerRemove(ctx, name, container.RemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove session executor container: %w", err)
	}
	return nil
}
