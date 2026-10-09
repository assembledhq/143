//go:build docker_integration

package providers

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/preview"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// These tests intentionally fail when Docker is unavailable. Selecting the
// docker_integration tag means the runner must provide a real Docker daemon.
func TestDockerIntegrationPreviewPostgresAnonymousVolumeLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		canceledStop bool
	}{
		{name: "normal stop"},
		{name: "canceled caller still removes infrastructure", canceledStop: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newDockerCleanupIntegrationFixture(t)
			sandboxID := fixture.createContainer(t, "sandbox", &container.Config{
				Image: fixture.template.Image, Entrypoint: []string{"sleep"}, Cmd: []string{"300"},
			}, nil)
			fixture.registerPreview(sandboxID)
			infra, err := fixture.provider.provisionInfra(fixture.ctx, &agent.Sandbox{ID: sandboxID}, fixture.owner.Handle, "postgres", models.InfrastructureConfig{Template: "postgres-17"}, fixture.template, fixture.opts)
			require.NoError(t, err, "PostgreSQL infrastructure should start with labelled anonymous image volumes")
			fixture.cleanupContainer(t, infra.ContainerID)
			fixture.provider.previews[fixture.owner.Handle].infra["postgres"] = infra
			fixture.requirePostgresQuery(t, infra)

			inspected, err := fixture.client.ContainerInspect(fixture.ctx, infra.ContainerID)
			require.NoError(t, err, "running PostgreSQL infrastructure should be inspectable")
			actualOwner, valid := parseInfrastructureOwner(inspected.Config.Labels)
			require.True(t, valid, "container ownership labels should contain the complete versioned identity")
			require.Equal(t, fixture.owner, actualOwner, "container labels should retain the full test owner identity")
			imageConfig, err := fixture.client.ImageInspect(fixture.ctx, fixture.template.Image)
			require.NoError(t, err, "PostgreSQL image should declare its data volume")
			require.Equal(t, map[string]struct{}{"/var/lib/postgresql/data": {}}, imageConfig.Config.Volumes, "PostgreSQL 17 should initialize the declared data directory")
			actualTargets := make(map[string]struct{})
			var volumeNames []string
			for _, point := range inspected.Mounts {
				require.Equal(t, mount.TypeVolume, point.Type, "each infrastructure image data directory should use a Docker volume")
				require.True(t, anonymousMount(inspected, point.Destination), "an explicit image mount should retain an empty source and anonymous removal semantics")
				actualTargets[point.Destination] = struct{}{}
				volumeNames = append(volumeNames, point.Name)
				fixture.cleanupVolume(t, point.Name)
				v, err := fixture.client.VolumeInspect(fixture.ctx, point.Name)
				require.NoError(t, err, "the anonymous infrastructure data volume should exist while PostgreSQL is running")
				volumeOwner, valid := parseInfrastructureOwner(v.Labels)
				require.True(t, valid, "anonymous volumes should carry recoverable owner labels")
				require.Equal(t, fixture.owner, volumeOwner, "anonymous volume labels should retain the same complete owner as the container")
				require.True(t, managedAnonymousVolume(v), "the actual Docker-created anonymous volume should qualify for scoped reconciliation")
			}
			require.Equal(t, imageConfig.Config.Volumes, actualTargets, "all and only image-declared data directories should receive labelled anonymous volumes")

			stopCtx, cancel := context.WithCancel(fixture.ctx)
			if tt.canceledStop {
				cancel()
			}
			defer cancel()
			require.NoError(t, fixture.provider.StopPreview(stopCtx, fixture.owner.Handle), "stop should remove infrastructure with a fresh cleanup budget even if the caller is canceled")
			_, err = fixture.client.ContainerInspect(fixture.ctx, infra.ContainerID)
			require.True(t, cerrdefs.IsNotFound(err), "the stopped PostgreSQL container should no longer exist")
			for _, name := range volumeNames {
				_, err := fixture.client.VolumeInspect(fixture.ctx, name)
				require.True(t, cerrdefs.IsNotFound(err), "stop should remove each real PostgreSQL anonymous data volume")
			}
		})
	}
}

func TestDockerIntegrationPreviewInfrastructureReconcileAfterContainerRemoval(t *testing.T) {
	t.Parallel()
	fixture := newDockerCleanupIntegrationFixture(t)
	labels := infrastructureLabels(fixture.owner, "postgres", time.Now().Add(-infrastructureCleanupGrace-time.Minute))
	volumeLabels := infrastructureLabels(fixture.owner, "postgres", time.Now().Add(-infrastructureCleanupGrace-time.Minute))
	volumeLabels[infrastructureVolumeKindLabel] = "anonymous"
	id := fixture.createContainer(t, "orphan", &container.Config{
		Image: fixture.template.Image, Entrypoint: []string{"sleep"}, Cmd: []string{"300"}, Labels: labels,
	}, []mount.Mount{{Type: mount.TypeVolume, Target: "/var/lib/postgresql/data", VolumeOptions: &mount.VolumeOptions{Labels: volumeLabels}}})
	inspected, err := fixture.client.ContainerInspect(fixture.ctx, id)
	require.NoError(t, err, "the scoped orphan fixture should have an actual anonymous volume")
	mountTypes := make(map[string]mount.Type)
	volumeName := ""
	for _, point := range inspected.Mounts {
		mountTypes[point.Destination] = point.Type
		if point.Destination == "/var/lib/postgresql/data" {
			volumeName = point.Name
		}
	}
	require.Equal(t, map[string]mount.Type{"/var/lib/postgresql/data": mount.TypeVolume}, mountTypes, "the PostgreSQL fixture should own exactly its image-declared data mount")
	fixture.cleanupVolume(t, volumeName)
	require.NoError(t, fixture.client.ContainerRemove(fixture.ctx, id, container.RemoveOptions{Force: true}), "removing only the exact fixture container should reproduce an orphan volume without host pruning")
	_, err = fixture.client.VolumeInspect(fixture.ctx, volumeName)
	require.NoError(t, err, "container removal without RemoveVolumes should leave the anonymous data volume behind")
	unowned, err := fixture.client.VolumeCreate(fixture.ctx, volume.CreateOptions{Labels: map[string]string{"com.143.test.preview-cleanup": fixture.owner.Handle}})
	require.NoError(t, err, "an unrelated anonymous volume should exist for the cleanup scope check")
	fixture.cleanupVolume(t, unowned.Name)

	// A newly constructed provider has no in-memory container or retry record.
	// Its resolver authorizes only this test's full ownership tuple.
	restarted := fixture.newProvider()
	require.NoError(t, restarted.ReconcileInfrastructure(fixture.ctx), "a fresh provider should reconcile old labelled volumes after their container disappears")
	_, err = fixture.client.VolumeInspect(fixture.ctx, volumeName)
	require.True(t, cerrdefs.IsNotFound(err), "reconciliation should remove the precisely owned orphan anonymous volume")
	remaining, err := fixture.client.VolumeInspect(fixture.ctx, unowned.Name)
	require.NoError(t, err, "reconciliation should retain anonymous volumes outside the versioned ownership contract")
	require.Equal(t, integrationVolumeIdentity(unowned), integrationVolumeIdentity(remaining), "reconciliation should preserve the unrelated anonymous volume's identity and ownership")
}

func TestDockerIntegrationPreviewInfrastructurePreservesNamedVolume(t *testing.T) {
	t.Parallel()
	fixture := newDockerCleanupIntegrationFixture(t)
	labels := infrastructureLabels(fixture.owner, "postgres", time.Now().Add(-infrastructureCleanupGrace-time.Minute))
	volumeLabels := infrastructureLabels(fixture.owner, "postgres", time.Now().Add(-infrastructureCleanupGrace-time.Minute))
	volumeLabels[infrastructureVolumeKindLabel] = "named"
	v, err := fixture.client.VolumeCreate(fixture.ctx, volume.CreateOptions{Name: "143-preview-cleanup-named-" + uuid.NewString(), Labels: volumeLabels})
	require.NoError(t, err, "a separately named data volume should be created for the preservation check")
	fixture.cleanupVolume(t, v.Name)
	id := fixture.createContainer(t, "named", &container.Config{
		Image: fixture.template.Image, Entrypoint: []string{"sleep"}, Cmd: []string{"300"}, Labels: labels,
	}, []mount.Mount{{Type: mount.TypeVolume, Source: v.Name, Target: "/var/lib/postgresql/data"}})
	fixture.registerPreview(id)
	fixture.provider.rememberInfrastructure(id, "143-preview-cleanup-test-named-"+fixture.owner.Handle, fixture.owner, false)
	require.NoError(t, fixture.provider.StopPreview(fixture.ctx, fixture.owner.Handle), "scoped infrastructure stop should remove a container with an externally named mount")
	remaining, err := fixture.client.VolumeInspect(fixture.ctx, v.Name)
	require.NoError(t, err, "RemoveVolumes should preserve an explicitly named data volume")
	require.Equal(t, integrationVolumeIdentity(v), integrationVolumeIdentity(remaining), "stop should preserve the separately named data volume's identity and ownership")
	require.NoError(t, fixture.newProvider().ReconcileInfrastructure(fixture.ctx), "reconciliation should leave named volumes outside its anonymous-volume contract")
	remaining, err = fixture.client.VolumeInspect(fixture.ctx, v.Name)
	require.NoError(t, err, "an old labelled named volume should survive infrastructure reconciliation")
	require.Equal(t, integrationVolumeIdentity(v), integrationVolumeIdentity(remaining), "reconciliation should preserve the separately named data volume's identity and ownership")
}

func TestDockerIntegrationPreviewInfrastructurePreservesBindReferencedVolume(t *testing.T) {
	t.Parallel()
	fixture := newDockerCleanupIntegrationFixture(t)
	sandboxID := fixture.createContainer(t, "bind-sandbox", &container.Config{
		Image: fixture.template.Image, Entrypoint: []string{"sleep"}, Cmd: []string{"300"},
	}, nil)
	fixture.registerPreview(sandboxID)
	infra, err := fixture.provider.provisionInfra(fixture.ctx, &agent.Sandbox{ID: sandboxID}, fixture.owner.Handle, "postgres", models.InfrastructureConfig{Template: "postgres-17"}, fixture.template, fixture.opts)
	require.NoError(t, err, "the bind-reference fixture should start real PostgreSQL infrastructure")
	fixture.cleanupContainer(t, infra.ContainerID)
	fixture.provider.previews[fixture.owner.Handle].infra["postgres"] = infra
	fixture.requirePostgresQuery(t, infra)
	inspected, err := fixture.client.ContainerInspect(fixture.ctx, infra.ContainerID)
	require.NoError(t, err, "the owned infrastructure volume should be discoverable before the bind mount")
	mountTypes := make(map[string]mount.Type)
	volumeName := ""
	for _, point := range inspected.Mounts {
		mountTypes[point.Destination] = point.Type
		if point.Destination == "/var/lib/postgresql/data" {
			volumeName = point.Name
		}
	}
	require.Equal(t, map[string]mount.Type{"/var/lib/postgresql/data": mount.TypeVolume}, mountTypes, "the bind-reference fixture should expose exactly its PostgreSQL anonymous data mount")
	v, err := fixture.client.VolumeInspect(fixture.ctx, volumeName)
	require.NoError(t, err, "the owned anonymous volume should expose its real Docker data directory")
	fixture.cleanupVolume(t, v.Name)
	sentinel := "owned-preview-" + fixture.owner.Handle
	sentinelPath := "/var/lib/postgresql/data/143-cleanup-test-sentinel"
	fixture.requireContainerOutput(t, infra.ContainerID, []string{"sh", "-c", `printf '%s' "$1" > "$2"`, "write-sentinel", sentinel, sentinelPath}, "")
	binderID := fixture.createContainer(t, "bind-reader", &container.Config{
		Image: fixture.template.Image, Entrypoint: []string{"sleep"}, Cmd: []string{"300"},
	}, []mount.Mount{{Type: mount.TypeBind, Source: v.Mountpoint, Target: "/shared-data", ReadOnly: true}})
	readSentinel := []string{"cat", "/shared-data/143-cleanup-test-sentinel"}
	fixture.requireContainerOutput(t, binderID, readSentinel, sentinel)

	err = fixture.provider.StopPreview(fixture.ctx, fixture.owner.Handle)
	require.Error(t, err, "stop should retain cleanup retry ownership while another container bind-mounts the anonymous data directory")
	_, err = fixture.client.ContainerInspect(fixture.ctx, infra.ContainerID)
	require.True(t, cerrdefs.IsNotFound(err), "stop should remove the owned PostgreSQL container while preserving its referenced data volume")
	remaining, err := fixture.client.VolumeInspect(fixture.ctx, v.Name)
	require.NoError(t, err, "stop must preserve the volume while another container bind-mounts its data directory")
	require.Equal(t, integrationVolumeIdentity(v), integrationVolumeIdentity(remaining), "stop should preserve the shared anonymous volume's identity and ownership")
	fixture.requireContainerOutput(t, binderID, readSentinel, sentinel)

	require.NoError(t, fixture.client.ContainerRemove(fixture.ctx, binderID, container.RemoveOptions{Force: true, RemoveVolumes: true}), "the test should remove only its own bind reader before retrying cleanup")
	require.NoError(t, fixture.provider.ReconcileInfrastructure(fixture.ctx), "pending cleanup should finish after the exact bind reader is removed")
	_, err = fixture.client.VolumeInspect(fixture.ctx, v.Name)
	require.True(t, cerrdefs.IsNotFound(err), "cleanup retry should remove the formerly bind-referenced owned anonymous volume")
}

type dockerIntegrationVolumeIdentity struct {
	Name       string
	Mountpoint string
	Labels     map[string]string
}

func integrationVolumeIdentity(v volume.Volume) dockerIntegrationVolumeIdentity {
	return dockerIntegrationVolumeIdentity{Name: v.Name, Mountpoint: v.Mountpoint, Labels: v.Labels}
}

type dockerCleanupIntegrationFixture struct {
	ctx        context.Context
	client     *client.Client
	provider   *DockerPreviewProvider
	owner      preview.InfrastructureOwner
	opts       preview.StartPreviewOptions
	template   preview.InfraTemplate
	network    string
	containers map[string]struct{}
	volumes    map[string]struct{}
}

func newDockerCleanupIntegrationFixture(t *testing.T) *dockerCleanupIntegrationFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err, "Docker integration tests should initialize the real SDK client")
	t.Cleanup(func() { require.NoError(t, docker.Close(), "the Docker integration client should close cleanly") })
	_, err = docker.Ping(ctx)
	require.NoError(t, err, "the selected docker_integration tests require an available Docker daemon")
	fixture := &dockerCleanupIntegrationFixture{
		ctx: ctx, client: docker,
		owner:      preview.InfrastructureOwner{OrgID: uuid.New(), PreviewID: uuid.New(), SessionID: uuid.New(), Handle: uuid.NewString(), WorkerNodeID: "143-preview-cleanup-test-" + uuid.NewString()},
		network:    "143-preview-cleanup-test-" + uuid.NewString(),
		containers: make(map[string]struct{}), volumes: make(map[string]struct{}),
	}
	fixture.opts = preview.StartPreviewOptions{OrgID: fixture.owner.OrgID, PreviewID: fixture.owner.PreviewID, SessionID: fixture.owner.SessionID}
	var found bool
	fixture.template, found = preview.LookupInfraTemplate("postgres-17")
	require.True(t, found, "the PostgreSQL 17 infrastructure template should be available")
	fixture.provider = fixture.newProvider()
	require.NoError(t, fixture.provider.ensureImage(ctx, fixture.template.Image), "the provider should lazy-pull the real PostgreSQL image when needed")
	created, err := docker.NetworkCreate(ctx, fixture.network, network.CreateOptions{Labels: map[string]string{"com.143.test.preview-cleanup": fixture.owner.Handle}})
	require.NoError(t, err, "each Docker integration fixture should create a uniquely owned network")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		err := docker.NetworkRemove(cleanupCtx, created.ID)
		if !cerrdefs.IsNotFound(err) {
			require.NoError(t, err, "the fixture should remove only its own test network")
		}
	})
	t.Cleanup(func() {
		// Remove test containers before volumes, even when an assertion fails
		// while PostgreSQL is running. Each operation targets a captured ID.
		for id := range fixture.containers {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := docker.ContainerRemove(cleanupCtx, id, container.RemoveOptions{Force: true, RemoveVolumes: true})
			cleanupCancel()
			if err != nil && !cerrdefs.IsNotFound(err) {
				t.Errorf("cleanup should remove only the exact test container %s and its anonymous volumes: %v", id, err)
			}
		}
		for name := range fixture.volumes {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := docker.VolumeRemove(cleanupCtx, name, false)
			cleanupCancel()
			if err != nil && !cerrdefs.IsNotFound(err) {
				t.Errorf("cleanup should remove only the exact unreferenced test volume %s without force: %v", name, err)
			}
		}
	})
	return fixture
}

func (f *dockerCleanupIntegrationFixture) newProvider() *DockerPreviewProvider {
	return NewDockerPreviewProvider(f.client, nil, zerolog.New(io.Discard), WithPreviewNetwork(f.network), WithPreviewWorkerNodeID(f.owner.WorkerNodeID), WithInfrastructureCleanupResolver(func(_ context.Context, owners []preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
		eligible := make(map[preview.InfrastructureOwner]bool, len(owners))
		for _, owner := range owners {
			eligible[owner] = owner == f.owner
		}
		return eligible, nil
	}))
}

func (f *dockerCleanupIntegrationFixture) registerPreview(sandboxID string) {
	f.provider.previews[f.owner.Handle] = &previewState{handle: f.owner.Handle, sandbox: &agent.Sandbox{ID: sandboxID}, opts: f.opts, infra: make(map[string]*preview.InfraHandle), cancelFn: func() {}}
}

func (f *dockerCleanupIntegrationFixture) createContainer(t *testing.T, suffix string, config *container.Config, mounts []mount.Mount) string {
	t.Helper()
	created, err := f.client.ContainerCreate(f.ctx, config, &container.HostConfig{NetworkMode: container.NetworkMode(f.network), Mounts: mounts}, &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{f.network: {}}}, nil, "143-preview-cleanup-test-"+suffix+"-"+f.owner.Handle)
	require.NoError(t, err, "the fixture should create only a uniquely named container on its own network")
	f.cleanupContainer(t, created.ID)
	require.NoError(t, f.client.ContainerStart(f.ctx, created.ID, container.StartOptions{}), "the fixture container should start on its own network")
	return created.ID
}

func (f *dockerCleanupIntegrationFixture) cleanupContainer(t *testing.T, id string) {
	t.Helper()
	f.containers[id] = struct{}{}
}

func (f *dockerCleanupIntegrationFixture) cleanupVolume(t *testing.T, name string) {
	t.Helper()
	f.volumes[name] = struct{}{}
}

func (f *dockerCleanupIntegrationFixture) requirePostgresQuery(t *testing.T, infra *preview.InfraHandle) {
	t.Helper()
	// The image temporarily starts a socket-only server during initdb. TCP
	// readiness waits for the final server and avoids querying that transient
	// server before it has created the requested database.
	health := f.template
	health.HealthCmd = []string{"pg_isready", "--host", "127.0.0.1", "--username", infra.Credential.Username, "--dbname", infra.Credential.Database}
	require.NoError(t, f.provider.waitForInfraHealth(f.ctx, infra.ContainerID, health), "PostgreSQL should initialize its anonymous data directory and serve the final database")
	f.requireContainerOutput(t, infra.ContainerID, []string{"psql", "--username", infra.Credential.Username, "--dbname", infra.Credential.Database, "--tuples-only", "--no-align", "--command", "SELECT current_database(), current_user;"}, infra.Credential.Database+"|"+infra.Credential.Username+"\n")
}

func (f *dockerCleanupIntegrationFixture) requireContainerOutput(t *testing.T, id string, command []string, expected string) {
	t.Helper()
	created, err := f.client.ContainerExecCreate(f.ctx, id, container.ExecOptions{Cmd: command, AttachStdout: true, AttachStderr: true})
	require.NoError(t, err, "the exact test container should accept the verification command")
	attached, err := f.client.ContainerExecAttach(f.ctx, created.ID, container.ExecAttachOptions{})
	require.NoError(t, err, "the test container verification output should attach successfully")
	defer attached.Close()
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, attached.Reader)
	require.NoError(t, err, "the test container verification output should finish cleanly")
	result, err := f.client.ContainerExecInspect(f.ctx, created.ID)
	require.NoError(t, err, "the test container verification completion should be inspectable")
	require.Equal(t, 0, result.ExitCode, "the test container verification command should succeed: %s", stderr.String())
	require.Equal(t, expected, stdout.String(), "the test container verification command should produce the exact expected data")
}
