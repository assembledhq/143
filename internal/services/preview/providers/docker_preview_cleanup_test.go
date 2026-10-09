package providers

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/preview"
)

type cleanupDockerClient struct {
	*mockDockerPreviewClient
	mu                           sync.Mutex
	containers                   map[string]container.InspectResponse
	volumes                      map[string]volume.Volume
	imageVolumes                 map[string]struct{}
	createErr, startErr, stopErr error
	inspectErr                   error
	containerListErr             error
	createUncertain              bool
	createForeign                bool
	createLate                   bool
	lateContainer                container.InspectResponse
	createEntered                chan struct{}
	createRelease                chan struct{}
	removeErrors                 []error
	volumeErrors                 []error
	removeOptions                []container.RemoveOptions
	removeContexts               []error
	removeBudgets                []time.Duration
	stopDeadline                 time.Time
	removeDeadline               time.Time
	volumeRemoveForces           []bool
	volumeRemoveLeaves           bool
	volumeInspectWaitName        string
	removeDeletesVolumes         bool
	removeEntered                chan struct{}
	removeRelease                chan struct{}
}

func newCleanupDockerClient() *cleanupDockerClient {
	return &cleanupDockerClient{mockDockerPreviewClient: &mockDockerPreviewClient{}, containers: make(map[string]container.InspectResponse), volumes: make(map[string]volume.Volume), imageVolumes: map[string]struct{}{"/image-defined/data": {}}, removeDeletesVolumes: true}
}

func (c *cleanupDockerClient) ImageInspect(context.Context, string, ...client.ImageInspectOption) (image.InspectResponse, error) {
	return image.InspectResponse{Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Volumes: c.imageVolumes}}}, nil
}

func (c *cleanupDockerClient) ContainerCreate(_ context.Context, config *container.Config, host *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
	if c.createEntered != nil {
		c.createEntered <- struct{}{}
		<-c.createRelease
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.createErr != nil && !c.createUncertain {
		return container.CreateResponse{}, c.createErr
	}
	if c.createForeign {
		config.Labels[infrastructureOrgLabel] = uuid.NewString()
	}
	inspected := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "infra-1", Name: "/" + name, HostConfig: host, State: &container.State{Running: true}}, Config: config}
	for i, m := range host.Mounts {
		if m.Type != mount.TypeVolume {
			continue
		}
		volumeName := m.Source
		if volumeName == "" {
			volumeName = strings.Repeat(string(rune('a'+i)), 64)
		}
		inspected.Mounts = append(inspected.Mounts, container.MountPoint{Type: mount.TypeVolume, Name: volumeName, Source: "/var/lib/docker/volumes/" + volumeName + "/_data", Destination: m.Target})
		labels := map[string]string{}
		if m.VolumeOptions != nil {
			labels = m.VolumeOptions.Labels
		}
		c.volumes[volumeName] = volume.Volume{Name: volumeName, Labels: labels, Mountpoint: "/var/lib/docker/volumes/" + volumeName + "/_data"}
	}
	if c.createLate {
		c.lateContainer = inspected
	} else {
		c.containers["infra-1"] = inspected
	}
	return container.CreateResponse{ID: "infra-1"}, c.createErr
}

type cleanupPhaseObserver struct {
	*recordingObserver
	onPhaseStart func(string)
}

func (o *cleanupPhaseObserver) OnPhaseStart(name string) {
	if o.onPhaseStart != nil {
		o.onPhaseStart(name)
	}
}

func TestInfrastructureUncertainCreateLaterAppears(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	cli.createErr = errors.New("create response disconnected")
	cli.createUncertain = true
	cli.createLate = true
	d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
	_, err := provisionCleanupTest(t, d, owner)
	require.ErrorIs(t, err, preview.ErrInfraStartFailed, "uncertain response should classify infrastructure start failure")
	require.NotEmpty(t, d.cleanupRecords, "first not-found inspection must retain uncertain creation ownership")
	require.Empty(t, cli.removeOptions, "uncertain missing container must not trigger blind removal")
	// Simulate the daemon allocating its mount after the first response loss.
	for _, point := range cli.lateContainer.Mounts {
		for _, configured := range cli.lateContainer.HostConfig.Mounts {
			if configured.Target == point.Destination {
				cli.volumes[point.Name] = volume.Volume{Name: point.Name, Labels: configured.VolumeOptions.Labels, Mountpoint: point.Source}
			}
		}
	}
	cli.containers[cli.lateContainer.ID] = cli.lateContainer
	require.NoError(t, d.ReconcileInfrastructure(context.Background()), "retry should adopt only the later exact-owned creation without restart")
	require.Empty(t, cli.containers, "later owned container should disappear")
	require.Empty(t, cli.volumes, "later owned anonymous volumes should disappear")
	require.Empty(t, d.cleanupRecords, "verified cleanup should release uncertain ownership")
}

func TestInfrastructureUncertainCreateCleansUnregisteredVolumes(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	cli.createErr = errors.New("create response disconnected")
	cli.createUncertain = true
	cli.createLate = true
	d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
	_, err := provisionCleanupTest(t, d, owner)
	require.ErrorIs(t, err, preview.ErrInfraStartFailed, "uncertain response should classify infrastructure failure")
	require.Empty(t, cli.containers, "daemon registration should not have happened")
	require.Empty(t, cli.volumes, "scoped cleanup must remove labelled allocation leftovers even if container never registered")
	require.NotEmpty(t, d.cleanupRecords, "cleanup must retain the exact uncertain container tombstone after removing volume leftovers")
	require.Equal(t, []bool{false}, cli.volumeRemoveForces, "unregistered volume cleanup must still use non-force reference-aware deletion")
	require.Error(t, d.ReconcileInfrastructure(context.Background()), "still-uncertain registration should remain pending")
	require.NotEmpty(t, d.cleanupRecords, "an absent container must not release future registration ownership")
}

func TestInfrastructureDefinitiveCreateRejectionsDoNotRetainOwnership(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "invalid parameters", err: cerrdefs.ErrInvalidArgument},
		{name: "forbidden", err: cerrdefs.ErrPermissionDenied},
		{name: "unauthorized", err: cerrdefs.ErrUnauthenticated},
		{name: "missing daemon image", err: cerrdefs.ErrNotFound},
		{name: "unsupported creation", err: cerrdefs.ErrNotImplemented},
		{name: "conflicting container", err: cerrdefs.ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner := cleanupTestOwner()
			cli := newCleanupDockerClient()
			cli.createErr = tt.err
			d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
			_, err := provisionCleanupTest(t, d, owner)
			require.ErrorIs(t, err, preview.ErrInfraStartFailed, "confirmed daemon rejection should still classify the infrastructure failure")
			require.Empty(t, d.cleanupRecords, "confirmed rejected creation must not leave permanent uncertain retry ownership")
			require.Empty(t, cli.removeOptions, "confirmed rejected creation must not trigger cleanup of another container")
		})
	}
}

func TestInfrastructureStopDuringCreationCleansLateResult(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	cli.createEntered = make(chan struct{}, 1)
	cli.createRelease = make(chan struct{})
	d := NewDockerPreviewProvider(cli, &noopSandboxExecutor{}, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
	cfg := &models.PreviewConfig{Infrastructure: map[string]models.InfrastructureConfig{"db": {Template: "postgres-17"}, "cache": {Template: "redis-7"}}, Services: map[string]models.ServiceConfig{}}
	type result struct {
		handle *preview.PreviewHandle
		err    error
	}
	done := make(chan result, 1)
	go func() {
		handle, err := d.StartPreview(context.Background(), &agent.Sandbox{ID: "sandbox"}, cfg, cleanupTestOptions(owner), nil)
		done <- result{handle: handle, err: err}
	}()
	select {
	case <-cli.createEntered:
	case <-time.After(time.Second):
		require.FailNow(t, "launch should reach gated container creation")
	}
	d.mu.RLock()
	handle := ""
	for candidate := range d.previews {
		handle = candidate
	}
	d.mu.RUnlock()
	require.NotEmpty(t, handle, "starting handle should be locally registered before creating infrastructure")
	require.NoError(t, d.StopPreview(context.Background(), handle), "stop should cancel an in-flight launch")
	close(cli.createRelease)
	select {
	case got := <-done:
		require.Nil(t, got.handle, "stopped launch must not return a serving handle")
		require.Error(t, got.err, "stopped launch must return cancellation failure")
	case <-time.After(time.Second):
		require.FailNow(t, "late create result should finish after stopped launch cleanup")
	}
	require.Empty(t, cli.containers, "late daemon create result must not escape cleanup")
	require.Empty(t, cli.volumes, "late daemon create result must not leak anonymous volumes")
	require.Empty(t, d.cleanupRecords, "successful late-create cleanup should release ownership")
	require.Empty(t, d.previews, "stopped launch must stay outside the serving map")
}

func TestInfrastructureAcknowledgementProtectsHandleHandoff(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	resolverCalls := 0
	resolver := func(_ context.Context, owners []preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
		resolverCalls++
		result := make(map[preview.InfrastructureOwner]bool)
		for _, candidate := range owners {
			result[candidate] = true
		}
		return result, nil
	}
	d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID), WithInfrastructureCleanupResolver(resolver))
	infra, err := provisionCleanupTest(t, d, owner)
	require.NoError(t, err, "infrastructure should provision")
	servingCleanupTest(d, owner, infra)
	d.previews[owner.Handle].acknowledged = false
	require.NoError(t, d.ReconcileInfrastructure(context.Background()), "unpersisted handle must stay protected even if observer demoted durable preview status")
	require.Zero(t, resolverCalls, "unacknowledged handle must not participate in local terminal-owner resolution")
	require.Empty(t, cli.removeOptions, "unacknowledged handle must not reach teardown")
	d.AcknowledgePreviewHandle(owner.Handle)
	d.AcknowledgePreviewHandle(owner.Handle)
	require.NoError(t, d.ReconcileInfrastructure(context.Background()), "persisted acknowledged terminal handle should become eligible")
	require.Empty(t, cli.volumes, "acknowledged terminal handle should remove owned infrastructure data")
	d.AcknowledgePreviewHandle(owner.Handle)
	require.Empty(t, d.previews, "late acknowledgement must never resurrect a removed handle")
}

func (c *cleanupDockerClient) ContainerStart(context.Context, string, container.StartOptions) error {
	return c.startErr
}
func (c *cleanupDockerClient) ContainerStop(ctx context.Context, _ string, _ container.StopOptions) error {
	c.mu.Lock()
	c.stopDeadline, _ = ctx.Deadline()
	c.mu.Unlock()
	return c.stopErr
}

func (c *cleanupDockerClient) ContainerRemove(ctx context.Context, id string, options container.RemoveOptions) error {
	if c.removeEntered != nil {
		c.removeEntered <- struct{}{}
		<-c.removeRelease
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeOptions = append(c.removeOptions, options)
	c.removeContexts = append(c.removeContexts, ctx.Err())
	deadline, _ := ctx.Deadline()
	c.removeDeadline = deadline
	c.removeBudgets = append(c.removeBudgets, time.Until(deadline))
	if len(c.removeErrors) > 0 {
		err := c.removeErrors[0]
		c.removeErrors = c.removeErrors[1:]
		if err != nil {
			return err
		}
	}
	inspected, exists := c.containers[id]
	if !exists {
		return cerrdefs.ErrNotFound
	}
	delete(c.containers, id)
	if c.removeDeletesVolumes && options.RemoveVolumes {
		for _, point := range inspected.Mounts {
			if anonymousMount(inspected, point.Destination) {
				delete(c.volumes, point.Name)
			}
		}
	}
	return nil
}

func (c *cleanupDockerClient) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id == "sandbox" {
		return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{HostConfig: &container.HostConfig{NetworkMode: "143-sandbox"}}, NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{"143-sandbox": {IPAddress: "172.20.0.2"}}}}, nil
	}
	if inspected, ok := c.containers[id]; ok {
		if c.inspectErr != nil {
			return container.InspectResponse{}, c.inspectErr
		}
		return inspected, nil
	}
	for _, inspected := range c.containers {
		if strings.TrimPrefix(inspected.Name, "/") == id {
			return inspected, nil
		}
	}
	return container.InspectResponse{}, cerrdefs.ErrNotFound
}

func labelsMatch(labels map[string]string, f filters.Args) bool {
	for _, value := range f.Get("label") {
		key, expected, _ := strings.Cut(value, "=")
		if labels[key] != expected {
			return false
		}
	}
	return true
}

func (c *cleanupDockerClient) ContainerList(_ context.Context, opts container.ListOptions) ([]container.Summary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.containerListErr != nil {
		return nil, c.containerListErr
	}
	var result []container.Summary
	for id, inspected := range c.containers {
		var labels map[string]string
		if inspected.Config != nil {
			labels = inspected.Config.Labels
		}
		if labelsMatch(labels, opts.Filters) {
			result = append(result, container.Summary{ID: id, Names: []string{inspected.Name}, Labels: labels, Mounts: inspected.Mounts})
		}
	}
	return result, nil
}

func (c *cleanupDockerClient) VolumeList(_ context.Context, opts volume.ListOptions) (volume.ListResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := volume.ListResponse{}
	for _, v := range c.volumes {
		if labelsMatch(v.Labels, opts.Filters) {
			result.Volumes = append(result.Volumes, &v)
		}
	}
	return result, nil
}

func (c *cleanupDockerClient) VolumeInspect(ctx context.Context, name string) (volume.Volume, error) {
	if name == c.volumeInspectWaitName {
		<-ctx.Done()
		return volume.Volume{}, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, exists := c.volumes[name]; exists {
		return v, nil
	}
	return volume.Volume{}, cerrdefs.ErrNotFound
}

func (c *cleanupDockerClient) VolumeRemove(_ context.Context, name string, force bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.volumeRemoveForces = append(c.volumeRemoveForces, force)
	if len(c.volumeErrors) > 0 {
		err := c.volumeErrors[0]
		c.volumeErrors = c.volumeErrors[1:]
		if err != nil {
			return err
		}
	}
	if !c.volumeRemoveLeaves {
		delete(c.volumes, name)
	}
	return nil
}

func (c *cleanupDockerClient) ContainerExecAttach(context.Context, string, container.ExecAttachOptions) (types.HijackedResponse, error) {
	local, remote := net.Pipe()
	if err := remote.Close(); err != nil {
		return types.HijackedResponse{}, err
	}
	return types.HijackedResponse{Conn: local, Reader: bufio.NewReader(strings.NewReader(""))}, nil
}

func cleanupTestOwner() preview.InfrastructureOwner {
	return preview.InfrastructureOwner{OrgID: uuid.New(), PreviewID: uuid.New(), SessionID: uuid.New(), Handle: strings.Repeat("1", 32), WorkerNodeID: "worker-generation"}
}
func cleanupTestOptions(owner preview.InfrastructureOwner) preview.StartPreviewOptions {
	return preview.StartPreviewOptions{OrgID: owner.OrgID, PreviewID: owner.PreviewID, SessionID: owner.SessionID}
}

func provisionCleanupTest(t *testing.T, d *DockerPreviewProvider, owner preview.InfrastructureOwner) (*preview.InfraHandle, error) {
	t.Helper()
	return d.provisionInfra(context.Background(), &agent.Sandbox{ID: "sandbox"}, owner.Handle, "db", models.InfrastructureConfig{Template: "postgres-17"}, preview.InfraTemplate{Image: "postgres:17", DefaultPort: 5432, DefaultMemMB: 128, DefaultCPU: .25}, cleanupTestOptions(owner))
}

func servingCleanupTest(d *DockerPreviewProvider, owner preview.InfrastructureOwner, infra *preview.InfraHandle) {
	d.previews[owner.Handle] = &previewState{handle: owner.Handle, acknowledged: true, opts: cleanupTestOptions(owner), infra: map[string]*preview.InfraHandle{"db": infra}, cancelFn: func() {}}
}

func TestInfrastructureStopRetriesAfterServingEviction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                          string
		containerErrors, volumeErrors []error
		deleteVolumes                 bool
		volumeRemoveLeaves            bool
	}{
		{name: "container conflict", containerErrors: []error{cerrdefs.ErrConflict}, deleteVolumes: true},
		{name: "daemon leaves volume", volumeErrors: []error{cerrdefs.ErrConflict}, deleteVolumes: false},
		{name: "volume removal acknowledgement leaves data", deleteVolumes: false, volumeRemoveLeaves: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner := cleanupTestOwner()
			cli := newCleanupDockerClient()
			cli.removeErrors = tt.containerErrors
			cli.volumeErrors = tt.volumeErrors
			cli.removeDeletesVolumes = tt.deleteVolumes
			cli.volumeRemoveLeaves = tt.volumeRemoveLeaves
			d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
			infra, err := provisionCleanupTest(t, d, owner)
			require.NoError(t, err, "infrastructure should provision")
			servingCleanupTest(d, owner, infra)
			require.Error(t, d.StopPreview(context.Background(), owner.Handle), "failed removal should surface cleanup error")
			require.Empty(t, d.previews, "preview should leave the serving map even if removal needs retry")
			require.NotEmpty(t, d.cleanupRecords, "cleanup ownership should survive serving eviction")
			cli.volumeRemoveLeaves = false
			require.NoError(t, d.ReconcileInfrastructure(context.Background()), "reconciliation should retry locally owned cleanup without resolver")
			require.Empty(t, d.cleanupRecords, "successful retry should release cleanup ownership")
			require.Empty(t, cli.volumes, "successful retry should remove actual anonymous volumes")
			require.Equal(t, container.RemoveOptions{Force: true, RemoveVolumes: false}, cli.removeOptions[0], "container removal should defer volume deletion to the reference guard")
			for _, force := range cli.volumeRemoveForces {
				require.False(t, force, "volume retry must preserve reference protection")
			}
		})
	}
}

func TestInfrastructureStopUsesFreshRemovalBudget(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	cli.stopErr = context.DeadlineExceeded
	d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
	infra, err := provisionCleanupTest(t, d, owner)
	require.NoError(t, err, "infrastructure should provision")
	servingCleanupTest(d, owner, infra)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, d.StopPreviewWithBackgroundWait(ctx, owner.Handle, time.Millisecond), "canceled caller must not prevent teardown")
	require.Equal(t, []error{nil}, cli.removeContexts, "removal should receive an uncanceled context")
	require.Greater(t, cli.removeBudgets[0], 14*time.Second, "graceful-stop failure must not consume removal budget")
	require.True(t, cli.removeDeadline.After(cli.stopDeadline), "removal must create a new deadline after graceful stop completes")
}

func TestInfrastructureStopPreservesExternalVolumeReferences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                  string
		reference                             mount.Type
		parentBind, failedInspect, failedList bool
	}{
		{name: "bind data directory", reference: mount.TypeBind},
		{name: "bind parent directory", reference: mount.TypeBind, parentBind: true},
		{name: "stopped volume attachment", reference: mount.TypeVolume},
		{name: "failed container inspection with bind reference", reference: mount.TypeBind, failedInspect: true},
		{name: "failed reference listing", failedList: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner := cleanupTestOwner()
			cli := newCleanupDockerClient()
			d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
			infra, err := provisionCleanupTest(t, d, owner)
			require.NoError(t, err, "infrastructure should provision")
			servingCleanupTest(d, owner, infra)
			volumeName := strings.Repeat("a", 64)
			v := cli.volumes[volumeName]
			if tt.reference != "" {
				source := v.Mountpoint
				if tt.parentBind {
					source = "/var/lib/docker/volumes"
				}
				cli.containers["external"] = container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "external", State: &container.State{}}, Mounts: []container.MountPoint{{Type: tt.reference, Name: volumeName, Source: source}}}
			}
			if tt.failedInspect {
				cli.inspectErr = errors.New("inspect unavailable")
			}
			if tt.failedList {
				cli.containerListErr = errors.New("reference listing unavailable")
			}
			require.Error(t, d.StopPreview(context.Background(), owner.Handle), "external usage or failed verification must retain volume cleanup for retry")
			require.Equal(t, []container.RemoveOptions{{Force: true, RemoveVolumes: false}}, cli.removeOptions, "container teardown must never bypass the explicit volume reference guard")
			_, exists := cli.containers[infra.ContainerID]
			require.False(t, exists, "exact owned container should still be removed")
			require.Equal(t, v, cli.volumes[volumeName], "shared data volume must survive container removal")
			require.NotEmpty(t, d.cleanupRecords, "shared-volume cleanup ownership must survive serving eviction")
			require.Empty(t, cli.volumeRemoveForces, "referenced or unverifiable volume must not reach deletion")
			delete(cli.containers, "external")
			cli.inspectErr = nil
			cli.containerListErr = nil
			require.NoError(t, d.ReconcileInfrastructure(context.Background()), "volume cleanup should retry after external reference disappears")
			require.Empty(t, cli.volumes, "unreferenced owned anonymous data should disappear on retry")
			require.Empty(t, d.cleanupRecords, "successful retry should release volume ownership")
			require.Equal(t, []bool{false}, cli.volumeRemoveForces, "shared-volume retry must use non-force deletion")
		})
	}
}

func TestInfrastructureConcurrentStopsShareRemoval(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	cli.removeEntered = make(chan struct{}, 20)
	cli.removeRelease = make(chan struct{})
	d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
	infra, err := provisionCleanupTest(t, d, owner)
	require.NoError(t, err, "infrastructure should provision")
	servingCleanupTest(d, owner, infra)
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- d.StopPreview(context.Background(), owner.Handle) }()
	}
	<-cli.removeEntered
	close(cli.removeRelease)
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err, "concurrent stops should complete idempotently")
	}
	require.Equal(t, []container.RemoveOptions{{Force: true, RemoveVolumes: false}}, cli.removeOptions, "concurrent stops should perform one container removal")
}

func TestRunInfrastructureCleanupRetriesAndStops(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	cli.removeErrors = []error{cerrdefs.ErrConflict}
	d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
	infra, err := provisionCleanupTest(t, d, owner)
	require.NoError(t, err, "infrastructure should provision")
	servingCleanupTest(d, owner, infra)
	require.Error(t, d.StopPreview(context.Background(), owner.Handle), "initial conflict should retain cleanup ownership")
	cli.removeEntered = make(chan struct{}, 1)
	cli.removeRelease = make(chan struct{})
	close(cli.removeRelease)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.RunInfrastructureCleanup(ctx); close(done) }()
	select {
	case <-cli.removeEntered:
	case <-time.After(time.Second):
		require.FailNow(t, "explicit cleanup loop should retry pending removal")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "cleanup loop should exit after server cancellation")
	}
	require.Empty(t, cli.volumes, "cleanup loop should finish pending anonymous-volume teardown")
	require.Empty(t, d.cleanupRecords, "successful background retry should release cleanup ownership")
}

func TestInfrastructureStartFailureAndUncertainCreateCleanup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		startErr, createErr error
		uncertain, foreign  bool
		expectRemoval       bool
	}{
		{name: "start failed", startErr: errors.New("start failed"), expectRemoval: true},
		{name: "create transport uncertainty", createErr: errors.New("connection closed"), uncertain: true, expectRemoval: true},
		{name: "name conflict", createErr: cerrdefs.ErrConflict},
		{name: "foreign matching name", createErr: errors.New("connection closed"), uncertain: true, foreign: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner := cleanupTestOwner()
			cli := newCleanupDockerClient()
			cli.startErr = tt.startErr
			cli.createErr = tt.createErr
			cli.createUncertain = tt.uncertain
			cli.createForeign = tt.foreign
			d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
			_, err := provisionCleanupTest(t, d, owner)
			require.ErrorIs(t, err, preview.ErrInfraStartFailed, "provisioning should classify infrastructure failure")
			if tt.expectRemoval {
				require.Equal(t, []container.RemoveOptions{{Force: true, RemoveVolumes: false}}, cli.removeOptions, "owned failed creation should clean container and anonymous volumes")
				require.Empty(t, cli.volumes, "owned failed creation should leave no anonymous volume")
			} else {
				require.Empty(t, cli.removeOptions, "foreign or conflict containers must never be removed")
			}
			require.Empty(t, d.cleanupRecords, "successful or rejected ownership cleanup must not leak retry records")
		})
	}
}

func TestInfrastructureMountsUseImageVolumePaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		template string
		paths    []string
	}{{"postgres-17", []string{"/var/lib/postgresql"}}, {"redis-7", []string{"/data"}}, {"mysql-8", []string{"/var/lib/mysql"}}}
	for _, tt := range tests {
		t.Run(tt.template, func(t *testing.T) {
			t.Parallel()
			owner := cleanupTestOwner()
			cli := newCleanupDockerClient()
			cli.imageVolumes = make(map[string]struct{})
			for _, target := range tt.paths {
				cli.imageVolumes[target] = struct{}{}
			}
			d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
			infra, err := d.provisionInfra(context.Background(), &agent.Sandbox{ID: "sandbox"}, owner.Handle, "db", models.InfrastructureConfig{Template: tt.template}, preview.InfraTemplate{Image: "test", DefaultPort: 5432}, cleanupTestOptions(owner))
			require.NoError(t, err, "catalog infrastructure should provision")
			inspected := cli.containers[infra.ContainerID]
			actual, valid := parseInfrastructureOwner(inspected.Config.Labels)
			require.True(t, valid, "container ownership labels must be complete")
			require.Equal(t, owner, actual, "container labels must preserve full tenant and generation identity")
			var targets []string
			for _, m := range inspected.HostConfig.Mounts {
				targets = append(targets, m.Target)
				require.Empty(t, m.Source, "infrastructure volumes must remain anonymous")
				actual, valid := parseInfrastructureOwner(m.VolumeOptions.Labels)
				require.True(t, valid, "volume ownership labels must be complete")
				require.Equal(t, owner, actual, "volume labels must preserve exact ownership")
				require.Equal(t, "anonymous", m.VolumeOptions.Labels[infrastructureVolumeKindLabel], "volume kind must identify platform anonymous resources")
			}
			require.Equal(t, tt.paths, targets, "mount paths must come from image metadata")
		})
	}
}

func TestReconcileInfrastructureProtectsResources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutation func(*DockerPreviewProvider, *cleanupDockerClient, preview.InfrastructureOwner)
		resolver func(preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error)
		removed  bool
	}{
		{name: "eligible orphan", removed: true},
		{name: "missing resolver result", resolver: func(preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
			return map[preview.InfrastructureOwner]bool{}, nil
		}},
		{name: "database owner active", resolver: func(o preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
			return map[preview.InfrastructureOwner]bool{o: false}, nil
		}},
		{name: "database unavailable", resolver: func(preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
			return nil, errors.New("database unavailable")
		}},
		{name: "local active", mutation: func(d *DockerPreviewProvider, _ *cleanupDockerClient, o preview.InfrastructureOwner) {
			servingCleanupTest(d, o, &preview.InfraHandle{})
			d.previews[o.Handle].starting = true
		}},
		{name: "new process resource", mutation: func(_ *DockerPreviewProvider, c *cleanupDockerClient, _ preview.InfrastructureOwner) {
			v := c.volumes[strings.Repeat("a", 64)]
			v.Labels[infrastructureCreatedLabel] = time.Now().Format(time.RFC3339Nano)
		}},
		{name: "creation grace", mutation: func(_ *DockerPreviewProvider, c *cleanupDockerClient, _ preview.InfrastructureOwner) {
			c.volumes[strings.Repeat("a", 64)].Labels[infrastructureCreatedLabel] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
		}},
		{name: "missing tenant", mutation: func(_ *DockerPreviewProvider, c *cleanupDockerClient, _ preview.InfrastructureOwner) {
			delete(c.volumes[strings.Repeat("a", 64)].Labels, infrastructureOrgLabel)
		}},
		{name: "named volume", mutation: func(_ *DockerPreviewProvider, c *cleanupDockerClient, _ preview.InfrastructureOwner) {
			v := c.volumes[strings.Repeat("a", 64)]
			delete(c.volumes, v.Name)
			v.Name = "customer-data"
			c.volumes[v.Name] = v
		}},
		{name: "legacy volume", mutation: func(_ *DockerPreviewProvider, c *cleanupDockerClient, _ preview.InfrastructureOwner) {
			delete(c.volumes[strings.Repeat("a", 64)].Labels, PreviewInfrastructureManagedLabel)
		}},
		{name: "stopped volume reference", mutation: func(_ *DockerPreviewProvider, c *cleanupDockerClient, _ preview.InfrastructureOwner) {
			c.containers["other"] = container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "other", State: &container.State{}}, Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: strings.Repeat("a", 64)}}}
		}},
		{name: "bind data reference", mutation: func(_ *DockerPreviewProvider, c *cleanupDockerClient, _ preview.InfrastructureOwner) {
			c.containers["other"] = container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "other"}, Mounts: []container.MountPoint{{Type: mount.TypeBind, Source: "/var/lib/docker/volumes/" + strings.Repeat("a", 64) + "/_data/subdir"}}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner := cleanupTestOwner()
			cli := newCleanupDockerClient()
			labels := infrastructureLabels(owner, "db", time.Now().Add(-time.Hour))
			labels[infrastructureVolumeKindLabel] = "anonymous"
			name := strings.Repeat("a", 64)
			cli.volumes[name] = volume.Volume{Name: name, Labels: labels, Mountpoint: "/var/lib/docker/volumes/" + name + "/_data"}
			resolver := func(_ context.Context, batch []preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
				require.Equal(t, []preview.InfrastructureOwner{owner}, batch, "resolver must receive exact complete owner")
				if tt.resolver != nil {
					return tt.resolver(owner)
				}
				return map[preview.InfrastructureOwner]bool{owner: true}, nil
			}
			d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID), WithInfrastructureCleanupResolver(resolver))
			if tt.mutation != nil {
				tt.mutation(d, cli, owner)
			}
			err := d.ReconcileInfrastructure(context.Background())
			if tt.removed {
				require.NoError(t, err, "eligible unreferenced orphan should reconcile")
				require.Empty(t, cli.volumes, "eligible owned anonymous volume should disappear")
				require.Equal(t, []bool{false}, cli.volumeRemoveForces, "GC must use non-force removal")
			} else {
				require.NotEmpty(t, cli.volumes, "protected volume must survive reconciliation")
				require.Empty(t, cli.volumeRemoveForces, "protected resource must never reach volume removal")
			}
		})
	}
}

func TestReconcileInfrastructureRetriesRecheckPersistedOwner(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	cli.removeErrors = []error{cerrdefs.ErrConflict}
	eligible := true
	resolver := func(_ context.Context, owners []preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
		result := make(map[preview.InfrastructureOwner]bool, len(owners))
		for _, candidate := range owners {
			result[candidate] = eligible
		}
		return result, nil
	}
	d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID), WithInfrastructureCleanupResolver(resolver))
	infra, err := provisionCleanupTest(t, d, owner)
	require.NoError(t, err, "owned infrastructure should provision")
	old := time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
	cli.containers[infra.ContainerID].Config.Labels[infrastructureCreatedLabel] = old
	for _, v := range cli.volumes {
		v.Labels[infrastructureCreatedLabel] = old
	}
	require.Error(t, d.ReconcileInfrastructure(context.Background()), "GC conflict should remain pending")
	eligible = false
	require.NoError(t, d.ReconcileInfrastructure(context.Background()), "new durable owner should protect previously pending GC")
	require.Equal(t, []container.RemoveOptions{{Force: true, RemoveVolumes: false}}, cli.removeOptions, "retry must not bypass updated persisted ownership")
	require.NotEmpty(t, cli.containers, "protected pending infrastructure should survive")
	eligible = true
	require.NoError(t, d.ReconcileInfrastructure(context.Background()), "eligible pending GC should finish")
	require.Empty(t, cli.containers, "eligible GC should remove infrastructure")
	require.Empty(t, cli.volumes, "eligible GC should remove anonymous volumes")
	require.Empty(t, d.cleanupRecords, "successful GC should release its retry ledger")
}

func TestReconcileInfrastructureBlockedRetriesDoNotStarveDiscovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		persisted bool
		slow      bool
	}{{name: "local requested retries"}, {name: "persisted requested retries", persisted: true}, {name: "slow local retry", slow: true}, {name: "slow persisted retry", persisted: true, slow: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cli := newCleanupDockerClient()
			resolver := func(_ context.Context, owners []preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
				result := make(map[preview.InfrastructureOwner]bool, len(owners))
				for _, owner := range owners {
					result[owner] = true
				}
				return result, nil
			}
			d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID("worker-generation"), WithInfrastructureCleanupResolver(resolver))
			if tt.slow {
				d.testInfrastructureCleanupPhaseTimeout = 50 * time.Millisecond
				cli.volumeInspectWaitName = fmt.Sprintf("%064x", 1)
			}
			var references []container.MountPoint
			for i := range 16 {
				owner := cleanupTestOwner()
				owner.Handle = fmt.Sprintf("pending-%02d", i)
				name := fmt.Sprintf("%064x", i+1)
				labels := infrastructureLabels(owner, "db", time.Now().Add(-time.Hour))
				labels[infrastructureVolumeKindLabel] = "anonymous"
				mountpoint := "/var/lib/docker/volumes/" + name + "/_data"
				cli.volumes[name] = volume.Volume{Name: name, Labels: labels, Mountpoint: mountpoint}
				key := fmt.Sprintf("pending-container-%02d", i)
				d.cleanupRecords[key] = &infrastructureCleanupRecord{key: key, ref: key, owner: owner, requested: true, resolverRequired: tt.persisted, removed: true, volumesCaptured: true, volumes: map[string]struct{}{name: {}}}
				references = append(references, container.MountPoint{Type: mount.TypeBind, Source: mountpoint})
			}
			cli.containers["external-bindings"] = container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "external-bindings"}, Mounts: references}
			orphanOwner := cleanupTestOwner()
			orphanName := fmt.Sprintf("%064x", 1000)
			labels := infrastructureLabels(orphanOwner, "db", time.Now().Add(-time.Hour))
			labels[infrastructureVolumeKindLabel] = "anonymous"
			cli.volumes[orphanName] = volume.Volume{Name: orphanName, Labels: labels, Mountpoint: "/var/lib/docker/volumes/" + orphanName + "/_data"}
			require.Error(t, d.ReconcileInfrastructure(context.Background()), "blocked requested records should remain pending while discovery makes progress")
			_, exists := cli.volumes[orphanName]
			require.False(t, exists, "reclaimable discovered orphan must be removed despite sixteen blocked retry records")
			require.Equal(t, 16, len(d.cleanupRecords), "all externally referenced retry records should remain owned")
			require.Equal(t, []bool{false}, cli.volumeRemoveForces, "only unreferenced orphan should reach non-force deletion")
		})
	}
}

func TestReconcileInfrastructureRotatesBlockedDiscoveredVolumes(t *testing.T) {
	t.Parallel()
	cli := newCleanupDockerClient()
	resolver := func(_ context.Context, owners []preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
		result := make(map[preview.InfrastructureOwner]bool, len(owners))
		for _, owner := range owners {
			result[owner] = true
		}
		return result, nil
	}
	d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID("worker-generation"), WithInfrastructureCleanupResolver(resolver))
	var references []container.MountPoint
	for i := range 17 {
		owner := cleanupTestOwner()
		name := fmt.Sprintf("%064x", i+1)
		labels := infrastructureLabels(owner, "db", time.Now().Add(-time.Hour))
		labels[infrastructureVolumeKindLabel] = "anonymous"
		mountpoint := "/var/lib/docker/volumes/" + name + "/_data"
		cli.volumes[name] = volume.Volume{Name: name, Labels: labels, Mountpoint: mountpoint}
		if i < 16 {
			references = append(references, container.MountPoint{Type: mount.TypeBind, Source: mountpoint})
		}
	}
	cli.containers["external-bindings"] = container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "external-bindings"}, Mounts: references}
	orphanName := fmt.Sprintf("%064x", 17)
	for range 17 {
		if _, exists := cli.volumes[orphanName]; !exists {
			break
		}
		err := d.ReconcileInfrastructure(context.Background())
		require.Error(t, err, "referenced discovered resources should remain protected during rotation")
	}
	_, exists := cli.volumes[orphanName]
	require.False(t, exists, "rotating discovery must eventually reach the unreferenced seventeenth volume")
	require.Equal(t, 16, len(cli.volumes), "all sixteen referenced volumes must survive rotating discovery")
	require.Equal(t, []bool{false}, cli.volumeRemoveForces, "only reclaimable orphan should reach non-force deletion")
}

func TestReconcileInfrastructurePendingSidecarDoesNotHideSibling(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	resolver := func(_ context.Context, owners []preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
		result := make(map[preview.InfrastructureOwner]bool, len(owners))
		for _, candidate := range owners {
			result[candidate] = true
		}
		return result, nil
	}
	d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID), WithInfrastructureCleanupResolver(resolver))
	created := time.Now().Add(-time.Hour)
	pgName := fmt.Sprintf("%064x", 1)
	pgLabels := infrastructureLabels(owner, "db", created)
	pgLabels[infrastructureVolumeKindLabel] = "anonymous"
	pg := volume.Volume{Name: pgName, Labels: pgLabels, Mountpoint: "/var/lib/docker/volumes/" + pgName + "/_data"}
	cli.volumes[pgName] = pg
	d.cleanupRecords["pg-pending"] = &infrastructureCleanupRecord{key: "pg-pending", ref: "pg-pending", owner: owner, requested: true, resolverRequired: true, removed: true, volumesCaptured: true, volumes: map[string]struct{}{pgName: {}}}
	cli.containers["external-binding"] = container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "external-binding"}, Mounts: []container.MountPoint{{Type: mount.TypeBind, Source: pg.Mountpoint}}}
	redisName := fmt.Sprintf("%064x", 2)
	redisLabels := infrastructureLabels(owner, "cache", created)
	volumeLabels := infrastructureLabels(owner, "cache", created)
	volumeLabels[infrastructureVolumeKindLabel] = "anonymous"
	redisMountpoint := "/var/lib/docker/volumes/" + redisName + "/_data"
	cli.volumes[redisName] = volume.Volume{Name: redisName, Labels: volumeLabels, Mountpoint: redisMountpoint}
	cli.containers["redis-orphan"] = container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "redis-orphan", Name: "/preview-cache-test", HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Target: "/data"}}}}, Config: &container.Config{Labels: redisLabels}, Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: redisName, Source: redisMountpoint, Destination: "/data"}}}
	require.Error(t, d.ReconcileInfrastructure(context.Background()), "shared PostgreSQL data should remain pending while sibling cleanup progresses")
	_, exists := cli.containers["redis-orphan"]
	require.False(t, exists, "pending PostgreSQL sidecar must not hide orphan Redis container sharing the same owner")
	_, exists = cli.volumes[redisName]
	require.False(t, exists, "pending PostgreSQL sidecar must not hide orphan Redis data")
	require.Equal(t, pg, cli.volumes[pgName], "externally referenced PostgreSQL data must remain intact")
	require.Equal(t, 1, len(d.cleanupRecords), "only referenced PostgreSQL teardown should remain pending")
}

func TestReconcileInfrastructureObservedContainerDisappearanceCompletes(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	resolver := func(_ context.Context, owners []preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
		result := make(map[preview.InfrastructureOwner]bool, len(owners))
		for _, candidate := range owners {
			result[candidate] = true
		}
		return result, nil
	}
	d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID), WithInfrastructureCleanupResolver(resolver))
	name := fmt.Sprintf("%064x", 1)
	labels := infrastructureLabels(owner, "db", time.Now().Add(-time.Hour))
	labels[infrastructureVolumeKindLabel] = "anonymous"
	cli.volumes[name] = volume.Volume{Name: name, Labels: labels, Mountpoint: "/var/lib/docker/volumes/" + name + "/_data"}
	d.cleanupRecords["observed-container"] = &infrastructureCleanupRecord{key: "observed-container", ref: "observed-container", name: "preview-db-test", owner: owner, uncertain: true, requested: true, resolverRequired: true, volumes: make(map[string]struct{})}
	require.NoError(t, d.ReconcileInfrastructure(context.Background()), "missing previously observed container must complete its owned volume cleanup")
	require.Empty(t, cli.volumes, "previously observed missing container should leave no anonymous volume")
	require.Empty(t, d.cleanupRecords, "confirmed missing historical container must not become a permanent late-create tombstone")
}

func TestReconcileLocalInfrastructureOwners(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                        string
		starting, eligible, missing bool
		resolverErr                 error
		removed                     bool
	}{
		{name: "terminal local runtime", eligible: true, removed: true},
		{name: "in-flight launch", starting: true, eligible: true},
		{name: "active durable runtime"},
		{name: "missing persisted result", missing: true},
		{name: "persisted query failed", resolverErr: errors.New("database unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner := cleanupTestOwner()
			cli := newCleanupDockerClient()
			resolver := func(_ context.Context, owners []preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
				if tt.resolverErr != nil {
					return nil, tt.resolverErr
				}
				if tt.missing {
					return map[preview.InfrastructureOwner]bool{}, nil
				}
				result := make(map[preview.InfrastructureOwner]bool)
				for _, o := range owners {
					result[o] = tt.eligible
				}
				return result, nil
			}
			d := NewDockerPreviewProvider(cli, nil, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID), WithInfrastructureCleanupResolver(resolver))
			infra, err := provisionCleanupTest(t, d, owner)
			require.NoError(t, err, "local infrastructure should provision")
			servingCleanupTest(d, owner, infra)
			d.previews[owner.Handle].starting = tt.starting
			err = d.ReconcileInfrastructure(context.Background())
			if tt.resolverErr != nil {
				require.Error(t, err, "ownership query failure should be observable")
			}
			if tt.removed {
				require.Empty(t, d.previews, "durably terminal finished handle should leave serving map")
				require.Empty(t, cli.containers, "durably terminal finished handle should remove infrastructure")
				require.Empty(t, cli.volumes, "durably terminal finished handle should remove volumes even if created this process")
			} else {
				require.NotEmpty(t, d.previews, "protected local handle must retain serving state")
				require.Empty(t, cli.removeOptions, "protected local handle must not reach container removal")
			}
		})
	}
}

func TestInfrastructureLaterStartFailuresCleanVolumes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		phase    string
		expected error
	}{
		{name: "infrastructure health", phase: "health", expected: preview.ErrInfraUnhealthy},
		{name: "init script", phase: "init", expected: preview.ErrInitScriptFailed},
		{name: "install command", phase: "install", expected: preview.ErrInstallFailed},
		{name: "service readiness", phase: "readiness", expected: preview.ErrServiceNotReady},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner := cleanupTestOwner()
			cli := newCleanupDockerClient()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executor := &fakeServiceExecutor{readFileFn: func(context.Context, string) ([]byte, error) { return nil, errors.New("missing init script") }, execFn: func(_ context.Context, cmd string) (int, error) {
				if tt.phase == "readiness" && (strings.Contains(cmd, "curl") || strings.Contains(cmd, "lsof")) {
					return 1, nil
				}
				return 0, nil
			}, execStreamFn: func(ctx context.Context, cmd string, _ func([]byte)) (int, error) {
				if strings.Contains(cmd, "'npm' 'ci'") {
					return 1, nil
				}
				<-ctx.Done()
				return -1, ctx.Err()
			}}
			d := NewDockerPreviewProvider(cli, executor, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID))
			cfg := &models.PreviewConfig{Infrastructure: map[string]models.InfrastructureConfig{"db": {Template: "postgres-17"}}, Services: map[string]models.ServiceConfig{}}
			if tt.phase == "init" {
				cfg.Infrastructure["db"] = models.InfrastructureConfig{Template: "postgres-17", InitScript: "init.sql"}
			}
			if tt.phase == "install" {
				cfg.Install = &models.PreviewInstallConfig{Command: []string{"npm", "ci"}}
			}
			if tt.phase == "readiness" {
				cfg.Primary = "web"
				cfg.Services["web"] = models.ServiceConfig{Command: []string{"npm", "run", "dev"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/", TimeoutSeconds: 1}}
			}
			observer := &cleanupPhaseObserver{recordingObserver: &recordingObserver{}, onPhaseStart: func(phase string) {
				if tt.phase == "health" && phase == "infrastructure_health" {
					cancel()
				}
			}}
			handle, err := d.StartPreview(ctx, &agent.Sandbox{ID: "sandbox"}, cfg, cleanupTestOptions(owner), observer)
			require.Nil(t, handle, "failed preview start must not return a handle")
			require.ErrorIs(t, err, tt.expected, "start failure should retain its phase classification")
			require.Empty(t, d.previews, "later-phase failure should evict serving state")
			require.Empty(t, cli.containers, "later-phase failure should tear down created infrastructure")
			require.Empty(t, cli.volumes, "later-phase failure should tear down anonymous volumes")
			require.Equal(t, []container.RemoveOptions{{Force: true, RemoveVolumes: false}}, cli.removeOptions, "all later start failures should remove anonymous volumes")
		})
	}
}

func TestInfrastructureSoftRestartPreservesContainersAndVolumes(t *testing.T) {
	t.Parallel()
	owner := cleanupTestOwner()
	cli := newCleanupDockerClient()
	executor := &fakeServiceExecutor{execFn: func(context.Context, string) (int, error) { return 0, nil }, execStreamFn: func(ctx context.Context, _ string, _ func([]byte)) (int, error) { <-ctx.Done(); return -1, ctx.Err() }}
	d := NewDockerPreviewProvider(cli, executor, zerolog.Nop(), WithPreviewWorkerNodeID(owner.WorkerNodeID), WithPreviewDialer(successfulPreviewDialer))
	infra, err := provisionCleanupTest(t, d, owner)
	require.NoError(t, err, "infrastructure should provision")
	servingCleanupTest(d, owner, infra)
	state := d.previews[owner.Handle]
	state.sandbox = &agent.Sandbox{ID: "sandbox"}
	state.config = &models.PreviewConfig{Primary: "web", Services: map[string]models.ServiceConfig{"web": {Command: []string{"npm", "run", "dev"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/", TimeoutSeconds: 5}}}, Infrastructure: map[string]models.InfrastructureConfig{"db": {Template: "postgres-17"}}}
	state.services = map[string]*serviceState{}
	_, err = d.SoftRestartPreview(context.Background(), owner.Handle, nil)
	require.NoError(t, err, "soft restart should restart application service")
	require.Empty(t, cli.removeOptions, "soft restart must preserve infrastructure containers")
	require.NotEmpty(t, cli.volumes, "soft restart must preserve anonymous infrastructure data")
	require.NoError(t, d.StopPreview(context.Background(), owner.Handle), "final stop should clean up soft-restarted preview")
}
