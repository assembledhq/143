package providers

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
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

type infrastructureCleanupRecord struct {
	key, ref, name   string
	owner            preview.InfrastructureOwner
	uncertain        bool
	requested        bool
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

func (d *DockerPreviewProvider) rememberInfrastructure(ref, name string, owner preview.InfrastructureOwner, uncertain bool) {
	d.cleanupMu.Lock()
	defer d.cleanupMu.Unlock()
	if d.cleanupRecords == nil {
		d.cleanupRecords = make(map[string]*infrastructureCleanupRecord)
	}
	if _, exists := d.cleanupRecords[ref]; !exists {
		d.cleanupRecords[ref] = &infrastructureCleanupRecord{key: ref, ref: ref, name: name, owner: owner, uncertain: uncertain, volumes: make(map[string]struct{})}
	}
}

func (d *DockerPreviewProvider) cleanupInfrastructureHandle(handle string) error {
	d.cleanupWorkMu.Lock()
	defer d.cleanupWorkMu.Unlock()
	d.cleanupMu.Lock()
	var records []*infrastructureCleanupRecord
	for _, record := range d.cleanupRecords {
		if record.owner.Handle == handle {
			record.requested = true
			records = append(records, record)
		}
	}
	d.cleanupMu.Unlock()
	return d.cleanupInfrastructureRecords(context.Background(), records)
}

func (d *DockerPreviewProvider) cleanupInfrastructureRecords(ctx context.Context, records []*infrastructureCleanupRecord) error {
	var failures []error
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		if err := d.removeInfrastructureRecord(ctx, record); err != nil {
			d.logger.Warn().Err(err).Str("container_id", record.ref).Str("handle", record.owner.Handle).Msg("preview infrastructure cleanup will retry")
			failures = append(failures, err)
			continue
		}
		d.cleanupMu.Lock()
		delete(d.cleanupRecords, record.key)
		d.cleanupMu.Unlock()
	}
	return errors.Join(failures...)
}

func (d *DockerPreviewProvider) removeInfrastructureRecord(ctx context.Context, record *infrastructureCleanupRecord) error {
	// Capture actual volume IDs before Docker removes the container. Docker may
	// return success while logging a volume deletion failure internally.
	gc, supportsGC := d.client.(dockerInfrastructureCleanupClient)
	var uncertainCreateErr error
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
					uncertainCreateErr = fmt.Errorf("uncertain infrastructure creation is not yet observable: %w", inspectErr)
				}
				// Volume allocation precedes daemon container registration. Keep
				// discovering exact-owned volumes while retaining the tombstone
				// for a possible late container response.
				record.volumesCaptured = false
			} else if inspectErr != nil {
				return fmt.Errorf("inspect uncertain container: %w", inspectErr)
			} else {
				var labels map[string]string
				if inspected.Config != nil {
					labels = inspected.Config.Labels
				}
				actual, valid := parseInfrastructureOwner(labels)
				if !valid || actual != record.owner || inspected.ContainerJSONBase == nil || strings.TrimPrefix(inspected.Name, "/") != record.name {
					// The name belongs to someone else. Never adopt it.
					return nil
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
		} else if cerrdefs.IsNotFound(inspectErr) && uncertainCreateErr == nil {
			record.removed = true
		}
		if !record.removed && uncertainCreateErr == nil {
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
				return fmt.Errorf("remove infrastructure container: %w", err)
			}
			record.removed = true
		}
	}
	if !supportsGC {
		return uncertainCreateErr
	}
	if !record.volumesCaptured && validInfrastructureOwner(record.owner) {
		ctx, cancel := context.WithTimeout(ctx, infrastructureRemoveTimeout)
		listed, err := gc.VolumeList(ctx, volume.ListOptions{Filters: filters.NewArgs(filters.Arg("label", PreviewInfrastructureManagedLabel+"="+infrastructureLabelVersion), filters.Arg("label", infrastructureHandleLabel+"="+record.owner.Handle))})
		cancel()
		if err != nil {
			return fmt.Errorf("discover cleanup volumes: %w", err)
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
	return errors.Join(append(failures, uncertainCreateErr)...)
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
			d.cleanupMu.Lock()
			var pending int64
			for _, record := range d.cleanupRecords {
				if record.requested {
					pending++
				}
			}
			d.cleanupMu.Unlock()
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
	state, exists := d.previews[owner.Handle]
	return exists && d.infrastructureOwner(owner.Handle, state.opts) == owner
}

func (d *DockerPreviewProvider) oldInfrastructure(labels map[string]string, now time.Time) bool {
	created, err := time.Parse(time.RFC3339Nano, labels[infrastructureCreatedLabel])
	return err == nil && created.Before(d.cleanupStartupCutoff) && now.Sub(created) >= infrastructureCleanupGrace
}

func (d *DockerPreviewProvider) cleanupPhaseTimeout() time.Duration {
	if d.testInfrastructureCleanupPhaseTimeout > 0 {
		return d.testInfrastructureCleanupPhaseTimeout
	}
	return infrastructureCleanupPhaseTimeout
}

// ReconcileInfrastructure retries local teardown and then resolves a bounded
// batch of fully-owned resources left by older process generations. Missing
// resolver entries and resolver errors always protect resources.
func (d *DockerPreviewProvider) ReconcileInfrastructure(ctx context.Context) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	d.cleanupReconcileMu.Lock()
	defer d.cleanupReconcileMu.Unlock()
	d.cleanupWorkMu.Lock()
	defer func() {
		d.cleanupWorkMu.Unlock()
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
	d.cleanupMu.Lock()
	var pending, persistedPending []*infrastructureCleanupRecord
	requestedContainers := make(map[string]bool)
	requestedNames := make(map[string]bool)
	requestedVolumes := make(map[string]bool)
	for _, r := range d.cleanupRecords {
		if !r.requested {
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
	}
	d.cleanupMu.Unlock()
	sort.Slice(pending, func(i, j int) bool { return pending[i].key < pending[j].key })
	sort.Slice(persistedPending, func(i, j int) bool { return persistedPending[i].key < persistedPending[j].key })
	pending = rotateCleanupCandidates(pending, &d.cleanupPendingCursor, infrastructureCleanupPhaseLimit)
	persistedPending = rotateCleanupCandidates(persistedPending, &d.cleanupPersistedCursor, infrastructureCleanupPhaseLimit)
	retryCtx, retryCancel := context.WithTimeout(ctx, d.cleanupPhaseTimeout())
	pendingErr := d.cleanupInfrastructureRecords(retryCtx, pending)
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
		if valid && !requested && d.oldInfrastructure(c.Labels, now) && !d.localInfrastructureActive(owner) {
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
		if valid && !requestedVolumes[v.Name] && managedAnonymousVolume(*v) && d.oldInfrastructure(v.Labels, now) && !d.localInfrastructureActive(owner) {
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
		d.mu.RLock()
		_, active := d.previews[record.owner.Handle]
		if active {
			d.mu.RUnlock()
			continue
		}
		if err := d.cleanupInfrastructureRecords(persistedCtx, []*infrastructureCleanupRecord{record}); err != nil {
			failures = append(failures, err)
		}
		d.mu.RUnlock()
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
		d.rememberInfrastructure(c.ID, name, owner, true)
		d.cleanupMu.Lock()
		record := d.cleanupRecords[c.ID]
		record.requested = true
		record.resolverRequired = true
		d.cleanupMu.Unlock()
		// Fence local registration through the final check and daemon teardown.
		d.mu.RLock()
		_, active := d.previews[owner.Handle]
		if active {
			d.mu.RUnlock()
			continue
		}
		if err := d.cleanupInfrastructureRecords(containerCtx, []*infrastructureCleanupRecord{record}); err != nil {
			failures = append(failures, err)
		}
		d.mu.RUnlock()
		for name := range record.volumes {
			requestedVolumes[name] = true
		}
	}
	containerCancel()
	volumeCtx, volumeCancel := context.WithTimeout(ctx, d.cleanupPhaseTimeout())
	for _, v := range volumeCandidates {
		if volumeCtx.Err() != nil {
			break
		}
		owner, _ := parseInfrastructureOwner(v.Labels)
		if !eligible[owner] || requestedVolumes[v.Name] {
			continue
		}
		d.mu.RLock()
		_, active := d.previews[owner.Handle]
		if active {
			d.mu.RUnlock()
			continue
		}
		if err := d.removeUnreferencedVolume(volumeCtx, gc, *v); err != nil {
			failures = append(failures, fmt.Errorf("reconcile volume %s: %w", v.Name, err))
		}
		d.mu.RUnlock()
	}
	volumeCancel()
	return errors.Join(failures...)
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
		if !state.starting && state.acknowledged && validInfrastructureOwner(owner) {
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
		ready := exists && !state.starting && state.acknowledged && d.infrastructureOwner(owner.Handle, state.opts) == owner
		d.mu.RUnlock()
		if !ready {
			continue
		}
		processed++
		if err := d.StopPreviewWithBackgroundWait(ctx, owner.Handle, preview.PreviewStopInteractiveWaitCap); err != nil {
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
