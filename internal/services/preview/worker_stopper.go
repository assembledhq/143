package preview

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/db"
)

// WorkerStopper routes preview stop operations to the worker that owns them.
type WorkerStopper struct {
	store       *db.PreviewStore
	selector    *WorkerSelector
	client      *WorkerPreviewClient
	localNodeID string
	local       *Manager
}

// NewWorkerStopper creates a preview stopper that follows worker ownership.
func NewWorkerStopper(store *db.PreviewStore, selector *WorkerSelector, client *WorkerPreviewClient, localNodeID string, local *Manager) *WorkerStopper {
	return &WorkerStopper{
		store:       store,
		selector:    selector,
		client:      client,
		localNodeID: localNodeID,
		local:       local,
	}
}

func (s *WorkerStopper) stopOnWorker(ctx context.Context, orgID uuid.UUID, previewID uuid.UUID, workerNodeID string) error {
	worker, err := s.selector.ResolveNode(ctx, workerNodeID)
	if err != nil {
		return fmt.Errorf("resolve preview worker: %w", err)
	}
	if worker.ID == s.localNodeID && s.local != nil {
		return s.local.StopPreview(ctx, orgID, previewID)
	}
	return s.client.StopPreview(ctx, worker, orgID, previewID)
}

// StopPreview stops a preview by ID, routing to the owning worker when needed.
func (s *WorkerStopper) StopPreview(ctx context.Context, orgID, previewID uuid.UUID) error {
	instance, err := s.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return fmt.Errorf("get preview instance: %w", err)
	}
	return s.stopOnWorker(ctx, orgID, previewID, instance.WorkerNodeID)
}

// StopActivePreviewForSession stops the active preview for a session, if one exists.
func (s *WorkerStopper) StopActivePreviewForSession(ctx context.Context, orgID, sessionID uuid.UUID) (bool, error) {
	instance, err := s.store.GetActivePreviewForSession(ctx, orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lookup active preview for session: %w", err)
	}
	if err := s.stopOnWorker(ctx, orgID, instance.ID, instance.WorkerNodeID); err != nil {
		return false, err
	}
	return true, nil
}
