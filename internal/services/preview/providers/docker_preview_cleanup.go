package providers

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/services/preview"
)

// PreviewInfrastructureManagedLabel identifies only resources created with
// the versioned ownership contract. Unlabelled historical resources are never
// adopted by reconciliation.
const PreviewInfrastructureManagedLabel = "com.143.preview.infrastructure"

const (
	infrastructureLabelVersion         = "v1"
	infrastructureOrgLabel             = "com.143.preview.org-id"
	infrastructurePreviewLabel         = "com.143.preview.preview-id"
	infrastructureSessionLabel         = "com.143.preview.session-id"
	infrastructureHandleLabel          = "com.143.preview.handle"
	infrastructureWorkerLabel          = "com.143.preview.worker-node-id"
	infrastructureNameLabel            = "com.143.preview.infra-name"
	infrastructureCreatedLabel         = "com.143.preview.created-at"
	infrastructureVolumeKindLabel      = "com.143.preview.volume-kind"
	infrastructureCleanupGrace         = 15 * time.Minute
	infrastructureCleanupInterval      = time.Minute
	infrastructureRemoveTimeout        = 15 * time.Second
	infrastructureCleanupBatchLimit    = 64
	infrastructureCleanupResourceLimit = 16
	infrastructureCleanupPhaseLimit    = infrastructureCleanupResourceLimit / 4
	infrastructureCleanupPhaseTimeout  = 10 * time.Second
)

// The production Docker SDK implements this extension. Keeping it separate
// permits lightweight lifecycle clients that do not support reconciliation.
type dockerInfrastructureCleanupClient interface {
	ContainerList(context.Context, container.ListOptions) ([]container.Summary, error)
	VolumeList(context.Context, volume.ListOptions) (volume.ListResponse, error)
	VolumeInspect(context.Context, string) (volume.Volume, error)
	VolumeRemove(context.Context, string, bool) error
}

// key, name, owner and rememberedAt are immutable after insertion and safe
// to read from a pointer snapshot. Operation state, including the volume
// set, is protected by mu; requested
// is atomic so metric snapshots never wait on Docker operations. Never wait
// for mu while holding cleanupMu or the serving-map lock.
type infrastructureCleanupRecord struct {
	mu               sync.Mutex
	rememberedAt     time.Time
	key, ref, name   string
	owner            preview.InfrastructureOwner
	uncertain        bool
	requested        atomic.Bool
	resolverRequired bool
	removed          bool
	volumesCaptured  bool
	volumes          map[string]struct{}
}

func WithPreviewWorkerNodeID(id string) DockerPreviewOption {
	return func(d *DockerPreviewProvider) { d.workerNodeID = id }
}

func WithInfrastructureCleanupResolver(resolver preview.InfrastructureCleanupResolver) DockerPreviewOption {
	return func(d *DockerPreviewProvider) { d.cleanupResolver = resolver }
}

func WithPreviewInfrastructureCleanupMetrics(m *metrics.PreviewInfrastructureCleanupMetrics) DockerPreviewOption {
	return func(d *DockerPreviewProvider) { d.cleanupMetrics = m }
}

func (d *DockerPreviewProvider) infrastructureOwner(handle string, opts preview.StartPreviewOptions) preview.InfrastructureOwner {
	return preview.InfrastructureOwner{OrgID: opts.OrgID, PreviewID: opts.PreviewID, SessionID: opts.SessionID, Handle: handle, WorkerNodeID: d.workerNodeID}
}

func validInfrastructureOwner(owner preview.InfrastructureOwner) bool {
	return owner.Valid()
}

func definitiveInfrastructureCreateRejection(err error) bool {
	// These daemon response classes reject creation before it can succeed.
	// Transport failures, cancellation, deadlines, unavailable, internal, and
	// unknown errors remain uncertain because their response may have been lost.
	return cerrdefs.IsInvalidArgument(err) || cerrdefs.IsPermissionDenied(err) ||
		cerrdefs.IsUnauthorized(err) || cerrdefs.IsNotFound(err) ||
		cerrdefs.IsConflict(err) || cerrdefs.IsAlreadyExists(err) ||
		cerrdefs.IsNotImplemented(err) || cerrdefs.IsFailedPrecondition(err)
}

func infrastructureLabels(owner preview.InfrastructureOwner, infraName string, created time.Time) map[string]string {
	return map[string]string{
		PreviewInfrastructureManagedLabel: infrastructureLabelVersion,
		infrastructureOrgLabel:            owner.OrgID.String(), infrastructurePreviewLabel: owner.PreviewID.String(),
		infrastructureSessionLabel: owner.SessionID.String(), infrastructureHandleLabel: owner.Handle,
		infrastructureWorkerLabel: owner.WorkerNodeID, infrastructureNameLabel: infraName,
		infrastructureCreatedLabel: created.UTC().Format(time.RFC3339Nano),
	}
}

func parseInfrastructureOwner(labels map[string]string) (preview.InfrastructureOwner, bool) {
	if labels[PreviewInfrastructureManagedLabel] != infrastructureLabelVersion || labels[infrastructureNameLabel] == "" {
		return preview.InfrastructureOwner{}, false
	}
	orgID, orgErr := uuid.Parse(labels[infrastructureOrgLabel])
	previewID, previewErr := uuid.Parse(labels[infrastructurePreviewLabel])
	sessionID, sessionErr := uuid.Parse(labels[infrastructureSessionLabel])
	owner := preview.InfrastructureOwner{OrgID: orgID, PreviewID: previewID, SessionID: sessionID, Handle: labels[infrastructureHandleLabel], WorkerNodeID: labels[infrastructureWorkerLabel]}
	return owner, orgErr == nil && previewErr == nil && sessionErr == nil && validInfrastructureOwner(owner)
}

func (d *DockerPreviewProvider) rememberInfrastructure(ref, name string, owner preview.InfrastructureOwner, uncertain bool) *infrastructureCleanupRecord {
	d.cleanupMu.Lock()
	defer d.cleanupMu.Unlock()
	if d.cleanupRecords == nil {
		d.cleanupRecords = make(map[string]*infrastructureCleanupRecord)
	}
	if _, exists := d.cleanupRecords[ref]; !exists {
		d.cleanupRecords[ref] = &infrastructureCleanupRecord{rememberedAt: time.Now(), key: ref, ref: ref, name: name, owner: owner, uncertain: uncertain, volumes: make(map[string]struct{})}
	}
	return d.cleanupRecords[ref]
}

// Gates fence registration and destructive cleanup for the same handle only.
// Reference counting releases idle entries instead of retaining every handle.
type infrastructureCleanupGate struct {
	token chan struct{}
	users int
}

func (d *DockerPreviewProvider) lockInfrastructureHandle(ctx context.Context, handle string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.cleanupMu.Lock()
	if d.cleanupHandleGates == nil {
		d.cleanupHandleGates = make(map[string]*infrastructureCleanupGate)
	}
	gate := d.cleanupHandleGates[handle]
	if gate == nil {
		gate = &infrastructureCleanupGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		d.cleanupHandleGates[handle] = gate
	}
	gate.users++
	d.cleanupMu.Unlock()
	releaseReference := func() {
		d.cleanupMu.Lock()
		gate.users--
		if gate.users == 0 {
			delete(d.cleanupHandleGates, handle)
		}
		d.cleanupMu.Unlock()
	}
	select {
	case <-ctx.Done():
		releaseReference()
		return nil, ctx.Err()
	case <-gate.token:
		release := func() { gate.token <- struct{}{}; releaseReference() }
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	}
}

func (d *DockerPreviewProvider) cleanupRecordSnapshot() []*infrastructureCleanupRecord {
	d.cleanupMu.Lock()
	defer d.cleanupMu.Unlock()
	records := make([]*infrastructureCleanupRecord, 0, len(d.cleanupRecords))
	for _, record := range d.cleanupRecords {
		records = append(records, record)
	}
	return records
}

func (d *DockerPreviewProvider) cleanupInfrastructureHandle(handle string) error {
	return d.cleanupInfrastructureHandleContext(context.Background(), handle)
}

func (d *DockerPreviewProvider) cleanupInfrastructureHandleContext(ctx context.Context, handle string) error {
	var records []*infrastructureCleanupRecord
	for _, record := range d.cleanupRecordSnapshot() {
		if record.owner.Handle == handle {
			// Publish all retry intent before a phase deadline can stop this pass.
			record.requested.Store(true)
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].key < records[j].key })
	return d.cleanupInfrastructureRecords(ctx, records, true)
}

func (d *DockerPreviewProvider) cleanupInfrastructureRecords(ctx context.Context, records []*infrastructureCleanupRecord, requestStop bool) error {
	var failures []error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		if err := d.cleanupInfrastructureRecord(ctx, record, requestStop, false); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (d *DockerPreviewProvider) cleanupInfrastructureRecord(ctx context.Context, record *infrastructureCleanupRecord, requestStop, discovered bool) error {
	releaseGate, err := d.lockInfrastructureHandle(ctx, record.owner.Handle)
	if err != nil {
		return err
	}
	defer releaseGate()
	record.mu.Lock()
	defer record.mu.Unlock()
	d.cleanupMu.Lock()
	current := d.cleanupRecords[record.key] == record
	d.cleanupMu.Unlock()
	if !current {
		return nil
	}
	if requestStop || discovered {
		record.requested.Store(true)
	}
	if discovered {
		record.resolverRequired = true
	}
	if !requestStop && record.resolverRequired && d.localInfrastructureActive(record.owner) {
		return nil
	}
	complete, err := d.removeInfrastructureRecord(ctx, record)
	if err != nil {
		d.logger.Warn().Err(err).Str("container_id", record.ref).Str("handle", record.owner.Handle).Msg("preview infrastructure cleanup will retry")
		return err
	}
	if complete {
		d.cleanupMu.Lock()
		if d.cleanupRecords[record.key] == record {
			delete(d.cleanupRecords, record.key)
		}
		d.cleanupMu.Unlock()
	}
	return nil
}

func (d *DockerPreviewProvider) removeInfrastructureRecord(ctx context.Context, record *infrastructureCleanupRecord) (bool, error) {
	// Capture actual volume IDs before Docker removes the container. Docker may
	// return success while logging a volume deletion failure internally.
	gc, supportsGC := d.client.(dockerInfrastructureCleanupClient)
	waitingForCreate := false
	if !record.removed {
		inspectCtx, inspectCancel := context.WithTimeout(ctx, infrastructureRemoveTimeout)
		inspected, inspectErr := d.client.ContainerInspect(inspectCtx, record.ref)
		inspectCancel()
		if record.uncertain {
			if cerrdefs.IsNotFound(inspectErr) {
				// A failed create response can precede daemon registration. Keep
				// exact ownership pending so a later matching container created
				// during this process is discovered without relying on old GC.
				if record.resolverRequired {
					// An exact ID already observed by GC cannot late-register
					// after disappearing. Finish its volume reconciliation.
					record.removed = true
					record.uncertain = false
				} else {
					waitingForCreate = true
				}
				// Volume allocation precedes daemon container registration. Keep
				// discovering exact-owned volumes while retaining the tombstone
				// for a possible late container response.
				record.volumesCaptured = false
			} else if inspectErr != nil {
				return false, fmt.Errorf("inspect uncertain container: %w", inspectErr)
			} else {
				var labels map[string]string
				if inspected.Config != nil {
					labels = inspected.Config.Labels
				}
				actual, valid := parseInfrastructureOwner(labels)
				if !valid || actual != record.owner || inspected.ContainerJSONBase == nil || strings.TrimPrefix(inspected.Name, "/") != record.name {
					// The name belongs to someone else. Never adopt it.
					return true, nil
				}
				record.ref = inspected.ID
				record.uncertain = false
			}
		}
		if inspectErr == nil && supportsGC {
			for _, point := range inspected.Mounts {
				if point.Type == mount.TypeVolume && point.Name != "" && anonymousMount(inspected, point.Destination) {
					record.volumes[point.Name] = struct{}{}
				}
			}
			record.volumesCaptured = true
		} else if cerrdefs.IsNotFound(inspectErr) && !waitingForCreate {
			record.removed = true
		}
		if !record.removed && !waitingForCreate {
			stopCtx, stopCancel := context.WithTimeout(ctx, infrastructureRemoveTimeout)
			timeout := 10
			if _, phaseBounded := ctx.Deadline(); phaseBounded {
				// Reconciliation reserves time for force removal and volume
				// verification even when a container ignores graceful shutdown.
				timeout = 1
			}
			if err := d.client.ContainerStop(stopCtx, record.ref, container.StopOptions{Timeout: &timeout}); err != nil && !cerrdefs.IsNotFound(err) {
				d.logger.Warn().Err(err).Str("container_id", record.ref).Msg("failed to gracefully stop preview infrastructure")
			}
			stopCancel()
			// Docker auto-removal cannot protect bind-path references. The
			// production SDK therefore removes the container first, then deletes
			// only captured, labelled anonymous volumes through the reference
			// guard below. Legacy clients retain their automatic removal path.
			removeVolumes := !supportsGC && inspectErr == nil
			if inspectErr != nil {
				d.logger.Warn().Err(inspectErr).Str("container_id", record.ref).Msg("preserving infrastructure volumes after failed mount inspection")
			}
			// Removal has its own budget, independent of the caller, background
			// uploads, and the graceful-stop timeout.
			removeCtx, removeCancel := context.WithTimeout(ctx, infrastructureRemoveTimeout)
			err := d.client.ContainerRemove(removeCtx, record.ref, container.RemoveOptions{Force: true, RemoveVolumes: removeVolumes})
			removeCancel()
			if err != nil && !cerrdefs.IsNotFound(err) {
				return false, fmt.Errorf("remove infrastructure container: %w", err)
			}
			record.removed = true
		}
	}
	if waitingForCreate && time.Since(record.rememberedAt) >= infrastructureCleanupGrace && supportsGC {
		inventoryCtx, inventoryCancel := context.WithTimeout(ctx, infrastructureRemoveTimeout)
		listed, err := gc.ContainerList(inventoryCtx, container.ListOptions{All: true, Filters: infrastructureOwnerFilter(record.owner)})
		inventoryCancel()
		if err != nil {
			return false, fmt.Errorf("verify expired uncertain container inventory: %w", err)
		}
		for _, candidate := range listed {
			owner, valid := parseInfrastructureOwner(candidate.Labels)
			if !valid || owner != record.owner {
				continue
			}
			for _, name := range candidate.Names {
				if strings.TrimPrefix(name, "/") == record.name {
					// Retry exact-name inspection next pass rather than adopting an
					// inventory row without its full current mount/ownership check.
					return false, nil
				}
			}
		}
		record.volumesCaptured = false
		waitingForCreate = false
	}
	if !supportsGC {
		return !waitingForCreate, nil
	}
	if !record.volumesCaptured && validInfrastructureOwner(record.owner) {
		ctx, cancel := context.WithTimeout(ctx, infrastructureRemoveTimeout)
		listed, err := gc.VolumeList(ctx, volume.ListOptions{Filters: infrastructureOwnerFilter(record.owner)})
		cancel()
		if err != nil {
			return false, fmt.Errorf("discover cleanup volumes: %w", err)
		}
		for _, v := range listed.Volumes {
			if v != nil {
				owner, valid := parseInfrastructureOwner(v.Labels)
				if valid && owner == record.owner && managedAnonymousVolume(*v) {
					record.volumes[v.Name] = struct{}{}
				}
			}
		}
		record.volumesCaptured = true
	}
	var failures []error
	for name := range record.volumes {
		ctx, cancel := context.WithTimeout(ctx, infrastructureRemoveTimeout)
		v, err := gc.VolumeInspect(ctx, name)
		if cerrdefs.IsNotFound(err) {
			delete(record.volumes, name)
			cancel()
			continue
		}
		if err == nil {
			owner, valid := parseInfrastructureOwner(v.Labels)
			if !valid || owner != record.owner || !managedAnonymousVolume(v) {
				delete(record.volumes, name)
				cancel()
				continue
			}
			err = d.removeUnreferencedVolume(ctx, gc, v)
			if err == nil {
				delete(record.volumes, name)
			}
		}
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("remove infrastructure volume %s: %w", name, err))
		}
	}
	return !waitingForCreate && len(failures) == 0, errors.Join(failures...)
}

func infrastructureOwnerFilter(owner preview.InfrastructureOwner) filters.Args {
	return filters.NewArgs(
		filters.Arg("label", PreviewInfrastructureManagedLabel+"="+infrastructureLabelVersion),
		filters.Arg("label", infrastructureOrgLabel+"="+owner.OrgID.String()),
		filters.Arg("label", infrastructurePreviewLabel+"="+owner.PreviewID.String()),
		filters.Arg("label", infrastructureSessionLabel+"="+owner.SessionID.String()),
		filters.Arg("label", infrastructureHandleLabel+"="+owner.Handle),
		filters.Arg("label", infrastructureWorkerLabel+"="+owner.WorkerNodeID),
	)
}

func anonymousMount(inspected container.InspectResponse, target string) bool {
	if inspected.ContainerJSONBase == nil || inspected.HostConfig == nil {
		return false
	}
	for _, m := range inspected.HostConfig.Mounts {
		if m.Type == mount.TypeVolume && m.Target == target {
			return m.Source == ""
		}
	}
	return false
}

func managedAnonymousVolume(v volume.Volume) bool {
	if v.Labels[infrastructureVolumeKindLabel] != "anonymous" || len(v.Name) != 64 {
		return false
	}
	for _, c := range v.Name {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// RunInfrastructureCleanup is explicitly started by the server and exits with
// its lifecycle context. Constructors never launch background goroutines.
func (d *DockerPreviewProvider) RunInfrastructureCleanup(ctx context.Context) {
	ticker := time.NewTicker(infrastructureCleanupInterval)
	defer ticker.Stop()
	for {
		err := d.ReconcileInfrastructure(ctx)
		if d.cleanupMetrics != nil {
			var pending int64
			for _, record := range d.cleanupRecordSnapshot() {
				if record.requested.Load() {
					pending++
				}
			}
			d.cleanupMetrics.Record(ctx, pending, err != nil)
		}
		if err != nil && ctx.Err() == nil {
			d.logger.Warn().Err(err).Msg("preview infrastructure reconciliation failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *DockerPreviewProvider) localInfrastructureActive(owner preview.InfrastructureOwner) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, exists := d.previews[owner.Handle]
	// Any local registration protects a handle, including unacknowledged
	// launches and an unlikely ownership collision.
	return exists
}

func (d *DockerPreviewProvider) oldInfrastructure(labels map[string]string, now time.Time) bool {
	created, err := time.Parse(time.RFC3339Nano, labels[infrastructureCreatedLabel])
	return err == nil && now.Sub(created) >= infrastructureCleanupGrace &&
		(created.Before(d.cleanupStartupCutoff) || labels[infrastructureWorkerLabel] == d.workerNodeID)
}

func (d *DockerPreviewProvider) cleanupPhaseTimeout() time.Duration {
	if d.testInfrastructureCleanupPhaseTimeout > 0 {
		return d.testInfrastructureCleanupPhaseTimeout
	}
	return infrastructureCleanupPhaseTimeout
}

// ReconcileInfrastructure retries local teardown and then resolves a bounded
// batch of aged, fully-owned resources, including late current-worker
// creates after uncertain records retire. Missing resolver entries and
// resolver errors always protect resources.
func (d *DockerPreviewProvider) ReconcileInfrastructure(ctx context.Context) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if !d.cleanupReconcileMu.TryLock() {
		// A pass already owns the cursor state; skip duplicate work without
		// making this caller wait beyond its lifecycle deadline.
		return ctx.Err()
	}
	defer d.cleanupReconcileMu.Unlock()
	defer func() {
		// Run the local handoff sweep after resource discovery so slow teardown
		// cannot monopolize every pass ahead of reclaimable orphan resources.
		if ctx.Err() == nil {
			localCtx, localCancel := context.WithTimeout(ctx, d.cleanupPhaseTimeout())
			resultErr = errors.Join(resultErr, d.reconcileLocalInfrastructureOwners(localCtx))
			localCancel()
		}
	}()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var pending, persistedPending []*infrastructureCleanupRecord
	requestedContainers := make(map[string]bool)
	requestedNames := make(map[string]bool)
	requestedVolumes := make(map[string]bool)
	busyOwners := make(map[preview.InfrastructureOwner]bool)
	for _, r := range d.cleanupRecordSnapshot() {
		if !r.mu.TryLock() {
			busyOwners[r.owner] = true
			continue
		}
		if !r.requested.Load() {
			r.mu.Unlock()
			continue
		}
		requestedContainers[r.key] = true
		requestedContainers[r.ref] = true
		if r.name != "" {
			requestedNames[r.name] = true
		}
		for name := range r.volumes {
			requestedVolumes[name] = true
		}
		if r.resolverRequired {
			persistedPending = append(persistedPending, r)
		} else {
			pending = append(pending, r)
		}
		r.mu.Unlock()
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].key < pending[j].key })
	sort.Slice(persistedPending, func(i, j int) bool { return persistedPending[i].key < persistedPending[j].key })
	pending = rotateCleanupCandidates(pending, &d.cleanupPendingCursor, infrastructureCleanupPhaseLimit)
	persistedPending = rotateCleanupCandidates(persistedPending, &d.cleanupPersistedCursor, infrastructureCleanupPhaseLimit)
	retryCtx, retryCancel := context.WithTimeout(ctx, d.cleanupPhaseTimeout())
	pendingErr := d.cleanupInfrastructureRecords(retryCtx, pending, false)
	retryCancel()
	gc, ok := d.client.(dockerInfrastructureCleanupClient)
	if !ok || d.cleanupResolver == nil || ctx.Err() != nil {
		return pendingErr
	}
	inventoryCtx, inventoryCancel := context.WithTimeout(ctx, d.cleanupPhaseTimeout())
	defer inventoryCancel()
	ownedFilter := filters.NewArgs(filters.Arg("label", PreviewInfrastructureManagedLabel+"="+infrastructureLabelVersion))
	containers, err := gc.ContainerList(inventoryCtx, container.ListOptions{All: true, Filters: ownedFilter})
	if err != nil {
		return errors.Join(pendingErr, err)
	}
	volumes, err := gc.VolumeList(inventoryCtx, volume.ListOptions{Filters: ownedFilter})
	if err != nil {
		return errors.Join(pendingErr, err)
	}
	owners := make(map[preview.InfrastructureOwner]struct{})
	for _, record := range persistedPending {
		if validInfrastructureOwner(record.owner) && !d.localInfrastructureActive(record.owner) {
			owners[record.owner] = struct{}{}
		}
	}
	now := time.Now()
	var containerCandidates []container.Summary
	for _, c := range containers {
		owner, valid := parseInfrastructureOwner(c.Labels)
		requested := requestedContainers[c.ID]
		for _, name := range c.Names {
			requested = requested || requestedNames[strings.TrimPrefix(name, "/")]
		}
		if valid && !busyOwners[owner] && !requested && d.oldInfrastructure(c.Labels, now) && !d.localInfrastructureActive(owner) {
			containerCandidates = append(containerCandidates, c)
		}
	}
	sort.Slice(containerCandidates, func(i, j int) bool { return containerCandidates[i].ID < containerCandidates[j].ID })
	containerCandidates = rotateCleanupCandidates(containerCandidates, &d.cleanupContainerCursor, infrastructureCleanupPhaseLimit)
	var volumeCandidates []*volume.Volume
	for _, v := range volumes.Volumes {
		if v == nil {
			continue
		}
		owner, valid := parseInfrastructureOwner(v.Labels)
		if valid && !busyOwners[owner] && !requestedVolumes[v.Name] && managedAnonymousVolume(*v) && d.oldInfrastructure(v.Labels, now) && !d.localInfrastructureActive(owner) {
			volumeCandidates = append(volumeCandidates, v)
		}
	}
	sort.Slice(volumeCandidates, func(i, j int) bool { return volumeCandidates[i].Name < volumeCandidates[j].Name })
	volumeCandidates = rotateCleanupCandidates(volumeCandidates, &d.cleanupVolumeCursor, infrastructureCleanupPhaseLimit)
	// Resolve only the rotating resource selections and persisted retries.
	// Their combined batch contains at most twelve owners per pass.
	for _, c := range containerCandidates {
		owner, _ := parseInfrastructureOwner(c.Labels)
		owners[owner] = struct{}{}
	}
	for _, v := range volumeCandidates {
		owner, _ := parseInfrastructureOwner(v.Labels)
		owners[owner] = struct{}{}
	}
	batch := make([]preview.InfrastructureOwner, 0, len(owners))
	for owner := range owners {
		batch = append(batch, owner)
	}
	sort.Slice(batch, func(i, j int) bool {
		return infrastructureOwnerSortKey(batch[i]) < infrastructureOwnerSortKey(batch[j])
	})
	if len(batch) == 0 {
		return pendingErr
	}
	eligible, err := d.cleanupResolver(inventoryCtx, batch)
	if err != nil {
		return errors.Join(pendingErr, fmt.Errorf("resolve infrastructure owners: %w", err))
	}
	inventoryCancel()
	var failures []error
	if pendingErr != nil {
		failures = append(failures, pendingErr)
	}
	persistedCtx, persistedCancel := context.WithTimeout(ctx, d.cleanupPhaseTimeout())
	for _, record := range persistedPending {
		if persistedCtx.Err() != nil {
			break
		}
		if !eligible[record.owner] {
			continue
		}
		if err := d.cleanupInfrastructureRecords(persistedCtx, []*infrastructureCleanupRecord{record}, false); err != nil {
			failures = append(failures, err)
		}
	}
	persistedCancel()
	containerCtx, containerCancel := context.WithTimeout(ctx, d.cleanupPhaseTimeout())
	for _, c := range containerCandidates {
		if containerCtx.Err() != nil {
			break
		}
		owner, _ := parseInfrastructureOwner(c.Labels)
		if !eligible[owner] {
			continue
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		record := d.rememberInfrastructure(c.ID, name, owner, true)
		if err := d.cleanupInfrastructureRecord(containerCtx, record, false, true); err != nil {
			failures = append(failures, err)
		}
		if record.mu.TryLock() {
			for name := range record.volumes {
				requestedVolumes[name] = true
			}
			record.mu.Unlock()
		} else {
			busyOwners[owner] = true
		}
	}
	containerCancel()
	volumeCtx, volumeCancel := context.WithTimeout(ctx, d.cleanupPhaseTimeout())
	for _, v := range volumeCandidates {
		if volumeCtx.Err() != nil {
			break
		}
		owner, _ := parseInfrastructureOwner(v.Labels)
		if !eligible[owner] || busyOwners[owner] || requestedVolumes[v.Name] {
			continue
		}
		if err := d.reconcileInfrastructureVolume(volumeCtx, gc, *v, owner); err != nil {
			failures = append(failures, fmt.Errorf("reconcile volume %s: %w", v.Name, err))
		}
	}
	volumeCancel()
	return errors.Join(failures...)
}

func (d *DockerPreviewProvider) reconcileInfrastructureVolume(ctx context.Context, gc dockerInfrastructureCleanupClient, v volume.Volume, owner preview.InfrastructureOwner) error {
	releaseGate, err := d.lockInfrastructureHandle(ctx, owner.Handle)
	if err != nil {
		return err
	}
	defer releaseGate()
	if d.localInfrastructureActive(owner) {
		return nil
	}
	return d.removeUnreferencedVolume(ctx, gc, v)
}

// Sorted round-robin selection gives every candidate a turn even when older
// referenced resources permanently consume their phase's count or time budget.
func rotateCleanupCandidates[T any](values []T, cursor *int, limit int) []T {
	if len(values) == 0 {
		return nil
	}
	count := min(len(values), limit)
	selected := make([]T, 0, count)
	start := *cursor % len(values)
	position := start
	for i := 0; i < count; i++ {
		selected = append(selected, values[position])
		position++
		if position == len(values) {
			position = 0
		}
	}
	// An early timeout may leave most selected rows unattempted. Advancing
	// one position ensures those rows eventually become first in a pass.
	*cursor = (start + 1) % len(values)
	return selected
}

func infrastructureOwnerSortKey(owner preview.InfrastructureOwner) string {
	return owner.OrgID.String() + owner.PreviewID.String() + owner.SessionID.String() + owner.Handle + owner.WorkerNodeID
}

// A ready local preview can lose its durable runtime ownership without an
// explicit HTTP stop (lease recovery, for example). Resolve finished launches
// after resource GC so retaining the serving map cannot hide that teardown.
// In-flight starts always remain protected until their handle is committed.
func (d *DockerPreviewProvider) reconcileLocalInfrastructureOwners(ctx context.Context) error {
	if d.cleanupResolver == nil || ctx.Err() != nil {
		return ctx.Err()
	}
	d.mu.RLock()
	batch := make([]preview.InfrastructureOwner, 0, infrastructureCleanupBatchLimit)
	for handle, state := range d.previews {
		owner := d.infrastructureOwner(handle, state.opts)
		if !state.starting && !state.stopRequested && state.acknowledged && validInfrastructureOwner(owner) {
			batch = append(batch, owner)
		}
	}
	d.mu.RUnlock()
	sort.Slice(batch, func(i, j int) bool {
		return infrastructureOwnerSortKey(batch[i]) < infrastructureOwnerSortKey(batch[j])
	})
	batch = rotateCleanupCandidates(batch, &d.cleanupLocalOwnerCursor, infrastructureCleanupBatchLimit)
	if len(batch) == 0 {
		return nil
	}
	eligible, err := d.cleanupResolver(ctx, batch)
	if err != nil {
		return fmt.Errorf("resolve local infrastructure ownership: %w", err)
	}
	var failures []error
	processed := 0
	for _, owner := range batch {
		if ctx.Err() != nil || processed >= infrastructureCleanupPhaseLimit {
			break
		}
		if !eligible[owner] {
			continue
		}
		d.mu.RLock()
		state, exists := d.previews[owner.Handle]
		ready := exists && !state.starting && !state.stopRequested && state.acknowledged && d.infrastructureOwner(owner.Handle, state.opts) == owner
		d.mu.RUnlock()
		if !ready {
			continue
		}
		processed++
		if err := d.stopPreviewForInfrastructureCleanup(ctx, owner.Handle); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (d *DockerPreviewProvider) removeUnreferencedVolume(ctx context.Context, gc dockerInfrastructureCleanupClient, v volume.Volume) error {
	current, err := gc.VolumeInspect(ctx, v.Name)
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	owner, valid := parseInfrastructureOwner(v.Labels)
	actual, currentValid := parseInfrastructureOwner(current.Labels)
	if !valid || !currentValid || owner != actual || !managedAnonymousVolume(current) {
		return fmt.Errorf("volume ownership changed")
	}
	v = current
	// Include stopped containers. Bind mounts may reference a volume's data
	// directory without Docker recording a volume attachment.
	containers, err := gc.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range containers {
		if err := volumeReferenceError(c, v.Name, v.Mountpoint); err != nil {
			return err
		}
	}
	// Non-force removal also closes the Docker volume-attachment race after
	// the reference scan. A conflict remains eligible for the next retry.
	if err := gc.VolumeRemove(ctx, v.Name, false); err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	_, err = gc.VolumeInspect(ctx, v.Name)
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("volume still exists after removal")
}

func volumeReferenceError(c container.Summary, name, mountpoint string) error {
	for _, m := range c.Mounts {
		if m.Type == mount.TypeVolume && m.Name == name {
			return fmt.Errorf("volume is referenced by container %s", c.ID)
		}
		if m.Type == mount.TypeBind && pathsOverlap(m.Source, mountpoint) {
			return fmt.Errorf("volume data is bind-mounted by container %s", c.ID)
		}
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = path.Clean(a), path.Clean(b)
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") || a == "/" || b == "/"
}
