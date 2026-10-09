package preview

import (
	"context"
	"fmt"
	"time"
)

// ResolveInfrastructureCleanup checks the durable ownership of every candidate.
// The provider additionally fences its own in-flight and retained handles.
func (m *Manager) ResolveInfrastructureCleanup(ctx context.Context, owners []InfrastructureOwner) (map[InfrastructureOwner]bool, error) {
	protected := make(map[InfrastructureOwner]bool, len(owners))
	valid := make([]InfrastructureOwner, 0, len(owners))
	for _, owner := range owners {
		protected[owner] = false
		if owner.Valid() {
			valid = append(valid, owner)
		}
	}
	if len(valid) == 0 {
		return protected, nil
	}
	if m == nil || m.store == nil || !m.store.Configured() || m.workerNodeID == "" {
		return protected, fmt.Errorf("preview infrastructure ownership resolver is not configured")
	}
	// Worker recovery uses the same 90-second heartbeat freshness window. Drain
	// intent and admission capability do not remove a worker's ownership.
	eligible, err := m.store.ResolvePreviewInfrastructureCleanup(ctx, valid, m.workerNodeID, time.Now().Add(-90*time.Second))
	if err != nil {
		return protected, err
	}
	for _, owner := range valid {
		protected[owner] = eligible[owner]
	}
	return protected, nil
}
