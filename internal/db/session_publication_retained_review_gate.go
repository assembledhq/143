package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RetainedReviewGateScope is independently retained caller identity for a
// publication gate update, never a fresh-start or publication permit.
type RetainedReviewGateScope struct {
	SessionID, ChangesetID, StartID uuid.UUID               `json:"-"`
	Source                          models.ReviewLoopSource `json:"-"`
	AutomationRunID                 *uuid.UUID              `json:"-"`
}

// SetRetainedReviewGate changes only a pending publication owned by the same
// retained generation. A stale, bypassed, passed or terminal target is a finite
// no-op. It returns true for an applied outcome or the exact same current
// clean/blocked/failed outcome; an idempotent match preserves updated_at.
// Caller locking and the UPDATE CAS serialize any concurrent generation change.
func (s *SessionPublicationStore) SetRetainedReviewGate(ctx context.Context, orgID uuid.UUID, scope RetainedReviewGateScope, gate models.SessionPublicationReviewGateState) (bool, error) {
	return s.setRetainedReviewGate(ctx, orgID, scope, gate, models.SessionPublicationReviewGatePending, nil)
}

// InvalidateRetainedReviewEvidence preserves the existing passed-to-pending
// head-change transition only for that exact retained caller generation.
func (s *SessionPublicationStore) InvalidateRetainedReviewEvidence(ctx context.Context, orgID uuid.UUID, scope RetainedReviewGateScope, headSHA string) (bool, error) {
	if headSHA == "" {
		return false, errors.New("replacement review target head SHA is required")
	}
	return s.setRetainedReviewGate(ctx, orgID, scope, models.SessionPublicationReviewGatePending, models.SessionPublicationReviewGatePassed, &headSHA)
}

func (s *SessionPublicationStore) setRetainedReviewGate(ctx context.Context, orgID uuid.UUID, scope RetainedReviewGateScope, gate, expectedGate models.SessionPublicationReviewGateState, invalidateHead *string) (bool, error) {
	if ctx == nil || orgID == uuid.Nil || scope.SessionID == uuid.Nil || scope.ChangesetID == uuid.Nil || scope.StartID == uuid.Nil {
		return false, errors.New("retained review gate requires complete caller scope")
	}
	switch scope.Source {
	case models.ReviewLoopSourcePublication:
		if scope.AutomationRunID != nil {
			return false, errors.New("publication review gate cannot use an automation caller")
		}
	case models.ReviewLoopSourceAutomation:
		if scope.AutomationRunID == nil || *scope.AutomationRunID == uuid.Nil {
			return false, errors.New("automation review gate requires its original run")
		}
	default:
		return false, errors.New("retained review gate requires a publication or automation caller")
	}
	state := models.SessionPublicationStateReviewPending
	switch gate {
	case models.SessionPublicationReviewGatePending, models.SessionPublicationReviewGateNeedsHuman:
	case models.SessionPublicationReviewGatePassed:
		state = models.SessionPublicationStateReadyToPublish
	case models.SessionPublicationReviewGateFailed:
		state = models.SessionPublicationStateTerminalFailed
	default:
		return false, errors.New("invalid retained review gate outcome")
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return false, errors.New("retained review gate requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin retained review gate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current *uuid.UUID
	if scope.Source == models.ReviewLoopSourcePublication {
		err = tx.QueryRow(ctx, `SELECT review_start_id FROM session_publications
 WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3 FOR UPDATE`, orgID, scope.SessionID, scope.ChangesetID).Scan(&current)
	} else {
		// Generation changes require a row UPDATE lock, but unrelated parent
		// FK KEY SHARE reads must remain compatible with publication T1.
		err = tx.QueryRow(ctx, `SELECT review_start_id FROM automation_runs a WHERE a.org_id=$1 AND a.id=$2
 AND (a.session_id=$3 OR EXISTS(SELECT 1 FROM session_automation_links l
 WHERE l.org_id=a.org_id AND l.automation_run_id=a.id AND l.session_id=$3)) FOR NO KEY UPDATE OF a`, orgID, scope.AutomationRunID, scope.SessionID).Scan(&current)
	}
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock retained review gate caller: %w", err)
	}
	if current == nil || *current != scope.StartID {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE session_publications AS publication
 SET review_gate_state=@gate,state=@state,
 review_loop_id=CASE WHEN @invalidate THEN NULL ELSE publication.review_loop_id END,
 review_workspace_revision=CASE WHEN @invalidate THEN NULL ELSE publication.review_workspace_revision END,
 review_desired_head_sha=CASE WHEN @invalidate THEN NULL ELSE publication.review_desired_head_sha END,
 desired_head_sha=CASE WHEN @invalidate THEN @head_sha::text ELSE publication.desired_head_sha END,
 completed_at=CASE WHEN @state='terminal_failed' THEN COALESCE(publication.completed_at,now()) ELSE publication.completed_at END,
 updated_at=CASE WHEN publication.review_gate_state=@expected_gate THEN now() ELSE publication.updated_at END
 WHERE publication.org_id=@org_id AND publication.session_id=@session_id AND publication.changeset_id=@changeset_id
 AND ((publication.review_gate_state=@expected_gate
       AND publication.state IN ('requested','review_pending','ready_to_publish','retryable_failed'))
 OR (@expected_gate='pending' AND publication.review_gate_state=@gate
       AND ((@gate='passed' AND publication.state='ready_to_publish')
         OR (@gate='failed' AND publication.state='terminal_failed')
         OR (@gate='needs_human' AND publication.state='review_pending'))))
 AND (NOT @invalidate OR publication.review_max_passes IS NOT NULL)
 AND (publication.review_loop_id IS NULL OR publication.review_loop_id=@start_id)
 AND EXISTS(SELECT 1 FROM review_loop_start_intents retained
 WHERE retained.org_id=publication.org_id AND retained.session_id=publication.session_id AND retained.id=@start_id
 AND ((@source::text='publication' AND retained.caller_kind='publication' AND retained.publication_changeset_id=publication.changeset_id
       AND publication.review_start_id=@start_id)
 OR (@source::text='automation' AND retained.caller_kind='automation' AND retained.automation_run_id=@automation_run_id::uuid
       AND publication.review_start_id IS NULL AND EXISTS(SELECT 1 FROM automation_runs caller
       WHERE caller.org_id=publication.org_id AND caller.id=@automation_run_id::uuid AND caller.review_start_id=@start_id))))`, pgx.NamedArgs{
		"org_id": orgID, "session_id": scope.SessionID, "changeset_id": scope.ChangesetID, "start_id": scope.StartID,
		"source": scope.Source, "automation_run_id": scope.AutomationRunID, "gate": gate, "state": state,
		"expected_gate": expectedGate, "invalidate": invalidateHead != nil, "head_sha": invalidateHead,
	})
	if err != nil {
		return false, fmt.Errorf("set retained review gate: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit retained review gate: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
