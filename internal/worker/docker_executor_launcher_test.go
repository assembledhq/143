package worker

import (
	"bytes"
	"context"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/google/uuid"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fakeDockerExecutorClient struct {
	createConfig     *container.Config
	createHostConfig *container.HostConfig
	createName       string
	inspectIDs       []string
	startID          string
	removeID         string
	removeOptions    container.RemoveOptions
}

func (c *fakeDockerExecutorClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	c.createConfig = config
	c.createHostConfig = hostConfig
	c.createName = containerName
	return container.CreateResponse{ID: "container-1"}, nil
}

func (c *fakeDockerExecutorClient) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	c.inspectIDs = append(c.inspectIDs, containerID)
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:   containerID,
			Name: "143-session-executor-test",
			State: &container.State{
				Status:    "running",
				Running:   true,
				Pid:       1234,
				StartedAt: "2026-06-26T04:13:46Z",
			},
		},
	}, nil
}

func (c *fakeDockerExecutorClient) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	c.startID = containerID
	return nil
}

func (c *fakeDockerExecutorClient) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	c.removeID = containerID
	c.removeOptions = options
	return nil
}

func TestDockerExecutorLauncher_LaunchCreatesLabeledExecutorContainer(t *testing.T) {
	t.Parallel()

	client := &fakeDockerExecutorClient{}
	var logs bytes.Buffer
	launcher := &DockerExecutorLauncher{
		client: client,
		cfg: DockerExecutorLauncherConfig{
			Image:       "ghcr.io/assembledhq/143-server:test",
			NetworkMode: "143_default",
			Binds:       []string{"/var/run/docker.sock:/var/run/docker.sock"},
			GroupAdd:    []string{"123"},
			Env:         []string{"DATABASE_URL=postgres://example"},
		},
		logger: zerolog.New(&logs),
	}
	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()

	result, err := launcher.Launch(context.Background(), ExecutorLaunchSpec{
		ExecutorID: executorID,
		OrgID:      orgID,
		SessionID:  sessionID,
		JobID:      jobID,
		JobType:    "run_agent",
		Image:      "ignored",
		BuildSHA:   "build-sha",
		NodeID:     "worker-a",
	})
	require.NoError(t, err, "Launch should create and start the executor container")
	require.Equal(t, "container-1", result.ContainerID, "Launch should return the Docker container id for durable recording")
	require.Equal(t, "143-session-executor-"+executorID.String(), client.createName, "Launch should use a stable executor container name")
	require.Equal(t, "ghcr.io/assembledhq/143-server:test", client.createConfig.Image, "Launch should use the configured executor image")
	require.Equal(t, []string{"/bin/session-executor", "--executor-id", executorID.String()}, []string(client.createConfig.Cmd), "Launch should run the session executor command")
	require.Equal(t, "session-executor", client.createConfig.Labels["com.143.role"], "Launch should label executor containers for prune and ops")
	require.Equal(t, orgID.String(), client.createConfig.Labels["com.143.org_id"], "Launch should label the org id")
	require.Equal(t, sessionID.String(), client.createConfig.Labels["com.143.session_id"], "Launch should label the session id")
	require.Equal(t, jobID.String(), client.createConfig.Labels["com.143.job_id"], "Launch should label the job id")
	require.Equal(t, container.NetworkMode("143_default"), client.createHostConfig.NetworkMode, "Launch should attach to the configured Docker network")
	require.Equal(t, []string{"/var/run/docker.sock:/var/run/docker.sock"}, client.createHostConfig.Binds, "Launch should mount required host resources")
	require.Equal(t, []string{"123"}, client.createHostConfig.GroupAdd, "Launch should add the configured supplemental groups")
	require.Equal(t, "container-1", client.startID, "Launch should start the created container")
	require.Contains(t, logs.String(), "created session executor Docker container", "Launch should log container creation")
	require.Contains(t, logs.String(), "started session executor Docker container", "Launch should log container start")
	require.Contains(t, logs.String(), "container-1", "Launch logs should include the Docker container id")
	require.Contains(t, logs.String(), "143-session-executor-"+executorID.String(), "Launch logs should include the stable container name")
	require.Contains(t, logs.String(), "inspected session executor Docker container", "Launch should log inspected container state")
	require.Contains(t, logs.String(), `"container_running":true`, "Launch inspect logs should include running state")
	require.Contains(t, logs.String(), `"container_oom_killed":false`, "Launch inspect logs should include OOM state")
}

func TestDockerExecutorLauncher_CleanupRemovesExecutorContainer(t *testing.T) {
	t.Parallel()

	client := &fakeDockerExecutorClient{}
	launcher := &DockerExecutorLauncher{client: client}
	executorID := uuid.New()

	err := launcher.Cleanup(context.Background(), ExecutorLaunchSpec{ExecutorID: executorID})

	require.NoError(t, err, "Cleanup should remove the executor container")
	require.Equal(t, "143-session-executor-"+executorID.String(), client.removeID, "Cleanup should remove by stable executor container name")
	require.True(t, client.removeOptions.Force, "Cleanup should force-remove a potentially running executor")
	require.True(t, client.removeOptions.RemoveVolumes, "Cleanup should remove anonymous executor volumes")
}
