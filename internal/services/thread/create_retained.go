package thread

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type retainedThreadStore interface {
	GetByIDForCreation(context.Context, uuid.UUID, uuid.UUID) (models.SessionThread, error)
	CreateWithID(context.Context, uuid.UUID, *models.SessionThread, int) error
}

// Adoption precedes mutable session/model/capacity preflights. It returns the
// original row, including later runtime progress, and supplies no send authority.
func (s *Service) adoptRetainedThread(ctx context.Context, input CreateThreadInput) (*models.SessionThread, bool, error) {
	if input.OrgID == uuid.Nil || input.SessionID == uuid.Nil || input.AgentType == "" {
		return nil, false, db.ErrThreadCreationIdentityConflict
	}
	store, ok := s.threadStore.(retainedThreadStore)
	if !ok {
		return nil, false, fmt.Errorf("retained thread creation capability unavailable")
	}
	existing, err := store.GetByIDForCreation(ctx, input.OrgID, input.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var model *string
	if input.Model != "" {
		model = &input.Model
	}
	if !db.SameThreadCreationIdentity(existing, models.SessionThread{ID: input.ID, OrgID: input.OrgID,
		SessionID: input.SessionID, AgentType: models.AgentType(input.AgentType), ModelOverride: model, Label: input.Label}) {
		return nil, false, db.ErrThreadCreationIdentityConflict
	}
	return &existing, true, nil
}

// PreflightCreateThread checks ordinary creation eligibility without creating a
// row or reserving capacity. E1 repeats these checks and its insert enforces the
// same visible-thread bound, so a race cannot turn this read into authority.
func (s *Service) PreflightCreateThread(ctx context.Context, input CreateThreadInput) error {
	if input.ID != uuid.Nil {
		if _, found, err := s.adoptRetainedThread(ctx, input); err != nil || found {
			return err
		}
	}
	if _, err := s.prepareThreadCreation(ctx, input); err != nil {
		return err
	}
	threads, err := s.threadStore.ListBySession(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return err
	}
	visible := 0
	for _, row := range threads {
		if row.ArchivedAt == nil {
			visible++
		}
	}
	if visible >= models.MaxThreadsPerSession {
		return db.ErrThreadLimitReached
	}
	return nil
}
