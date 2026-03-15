package memory

import (
	"context"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/services/agent"
)

// Ensure Adapter satisfies agent.MemoryService at compile time.
var _ agent.MemoryService = (*Adapter)(nil)

// Adapter wraps the memory Service to satisfy the agent.MemoryService interface,
// bridging the package boundary without introducing a circular import.
type Adapter struct {
	svc *Service
}

// NewAdapter creates a new memory adapter for the orchestrator.
func NewAdapter(svc *Service) *Adapter {
	return &Adapter{svc: svc}
}

// GetContextMemories implements agent.MemoryService.
func (a *Adapter) GetContextMemories(ctx context.Context, req agent.MemoryContextRequest) (*agent.MemoryContextResult, error) {
	result, err := a.svc.GetContextMemories(ctx, ContextRequest{
		OrgID:     req.OrgID,
		Repo:      req.Repo,
		FilePaths: req.FilePaths,
	})
	if err != nil {
		return nil, err
	}

	return &agent.MemoryContextResult{
		Formatted: result.Formatted,
		MemoryIDs: result.MemoryIDs,
	}, nil
}

// ReinforceMemories delegates to the underlying service.
func (a *Adapter) ReinforceMemories(ctx context.Context, orgID uuid.UUID, memoryIDs []uuid.UUID) error {
	return a.svc.ReinforceMemories(ctx, orgID, memoryIDs)
}
