package preview

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type PreviewRuntimeRevisionStamper interface {
	StampPreviewRuntimeRevision(ctx context.Context, orgID, previewID uuid.UUID, source models.PreviewRuntimeRevisionSource) error
}

type DBPreviewRuntimeRevisionStamper struct {
	PreviewStore *db.PreviewStore
	SessionStore *db.SessionStore
}

func (s DBPreviewRuntimeRevisionStamper) StampPreviewRuntimeRevision(ctx context.Context, orgID, previewID uuid.UUID, source models.PreviewRuntimeRevisionSource) error {
	if s.PreviewStore == nil {
		return fmt.Errorf("preview store is not configured")
	}
	if s.SessionStore == nil {
		return fmt.Errorf("session store is not configured")
	}
	instance, err := s.PreviewStore.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return fmt.Errorf("get preview instance: %w", err)
	}
	if instance.SessionID == uuid.Nil {
		return nil
	}
	session, err := s.SessionStore.GetByID(ctx, orgID, instance.SessionID)
	if err != nil {
		return fmt.Errorf("get preview session: %w", err)
	}
	return s.PreviewStore.UpdatePreviewRuntimeWorkspaceRevision(ctx, orgID, previewID, session.WorkspaceRevision, session.WorkspaceRevisionUpdatedAt, source)
}
