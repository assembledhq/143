package agent

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
)

// LiveSandboxInventoryCounter obtains IDs and count from one daemon inventory.
type LiveSandboxInventoryCounter interface {
	LiveSandboxContainerIDs(context.Context) ([]string, error)
}

// MissingSandboxContainerInspector must confirm a definitive Docker NotFound.
// A stopped container, transport error, or generic unavailable result is not
// evidence of absence.
type MissingSandboxContainerInspector interface {
	ConfirmSandboxContainerMissing(context.Context, string) (bool, error)
}

type HostSandboxReferenceStore interface {
	ListHostSandboxReferences(context.Context, string) ([]models.SandboxContainerReference, error)
}

type MissingSandboxReferenceObserver interface {
	ObserveMissingSandboxReferences(context.Context, []string) ([]models.SandboxContainerReference, error)
}

type SandboxReferenceObserver struct {
	provider MissingSandboxContainerInspector
	store    HostSandboxReferenceStore
	hostID   string
}

func NewSandboxReferenceObserver(provider MissingSandboxContainerInspector, store HostSandboxReferenceStore, physicalHostID string) *SandboxReferenceObserver {
	return &SandboxReferenceObserver{provider: provider, store: store, hostID: physicalHostID}
}

// ObserveMissingSandboxReferences never mutates lifecycle state or destroys a
// container. Admission matches each observation against the current exact
// reference; a concurrent assignment or owner change invalidates old evidence.
func (o *SandboxReferenceObserver) ObserveMissingSandboxReferences(ctx context.Context, liveIDs []string) ([]models.SandboxContainerReference, error) {
	if o == nil || o.provider == nil || o.store == nil || o.hostID == "" {
		return nil, fmt.Errorf("sandbox presence observer is not configured")
	}
	live := make(map[string]struct{}, len(liveIDs))
	for _, id := range liveIDs {
		if !models.IsFullDockerContainerID(id) {
			return nil, fmt.Errorf("sandbox inventory contains an incomplete container ID")
		}
		live[id] = struct{}{}
	}
	references, err := o.store.ListHostSandboxReferences(ctx, o.hostID)
	if err != nil {
		return nil, err
	}
	missing := make([]models.SandboxContainerReference, 0)
	for _, reference := range references {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := reference.Validate(); err != nil {
			return nil, err
		}
		if _, present := live[reference.ContainerID]; present {
			continue
		}
		confirmed, err := o.provider.ConfirmSandboxContainerMissing(ctx, reference.ContainerID)
		if err != nil {
			return nil, fmt.Errorf("confirm sandbox physical presence: %w", err)
		}
		if confirmed {
			missing = append(missing, reference)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return missing, nil
}
