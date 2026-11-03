package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// PublishCommittedLifecycleThreadEffect reloads a visible lane for a best-effort
// postcommit notification. Its owner must call it only after known commit and
// discard effects after abort or uncertainty. It neither writes nor proves
// commit, admission, exactly-once delivery or permission to start a provider.
// The receiver must use a usable pool, not the settled transaction.
func (s *SessionThreadStore) PublishCommittedLifecycleThreadEffect(ctx context.Context, orgID uuid.UUID, effect LifecycleEffect) error {
	if s == nil || ctx == nil {
		return errors.New("lifecycle thread notification requires a store and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := effect.Validate(); err != nil {
		return fmt.Errorf("validate lifecycle thread notification: %w", err)
	}
	if orgID == uuid.Nil || effect.orgID != orgID {
		return errors.New("lifecycle thread notification belongs to another organization")
	}
	var eventType models.SessionStreamEventType
	switch effect.kind {
	case LifecycleEffectThreadChanged:
		eventType = models.SessionStreamEventThreadRuntimeUpdated
	case LifecycleEffectInboxChanged:
		eventType = models.SessionStreamEventThreadInboxQueued
	default:
		return errors.New("lifecycle thread publisher accepts only lane or inbox changes")
	}
	if s.streams == nil {
		return nil
	}
	thread, err := s.GetByID(ctx, orgID, effect.threadID)
	if err != nil {
		return fmt.Errorf("reload lifecycle notification lane: %w", err)
	}
	if thread.OrgID != orgID || thread.SessionID != effect.sessionID || thread.ID != effect.threadID {
		return errors.New("lifecycle notification lane no longer matches its exact scope")
	}
	var data any = models.NewThreadRuntimeEvent(thread)
	if effect.kind == LifecycleEffectInboxChanged {
		data = models.NewThreadInboxEvent(thread)
	}
	if err := s.streams.PublishEvent(ctx, models.SessionStreamEvent{
		Type: eventType, OrgID: orgID, SessionID: effect.sessionID, Data: data,
	}); err != nil {
		return fmt.Errorf("publish lifecycle thread notification: %w", err)
	}
	return nil
}
