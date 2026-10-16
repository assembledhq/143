package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// UpdateResultWithAutomaticPublication commits an automation's completed result
// and its publication coordination retry together. Policy and readiness checks
// run in the durable job, so a transient failure cannot lose the handoff before
// a publication intent exists.
func (s *SessionStore) UpdateResultWithAutomaticPublication(ctx context.Context, orgID, runID uuid.UUID, status models.SessionStatus, result *models.SessionResult) error {
	if result == nil {
		result = &models.SessionResult{}
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	diffStats := computeDiffStatsForResult(result)
	session, err := s.updateResultRow(ctx, tx, orgID, runID, status, result, diffStats)
	if err != nil {
		return err
	}
	persistDiffSnapshot := shouldPersistDiffSnapshot(result)
	var updated workspaceRevisionUpdate
	if persistDiffSnapshot {
		updated, err = s.writeDiffSnapshot(ctx, tx, orgID, runID, 0, result, diffStats)
		if err != nil {
			return err
		}
	}
	if session.AutomationRunID != nil && session.Status == models.SessionStatusCompleted &&
		!session.IsInteractive() && session.Origin != models.SessionOriginAutomationGoalImprovement &&
		result.Diff != nil && strings.TrimSpace(*result.Diff) != "" {
		dedupeKey := "coordinate_automatic_publication:" + runID.String()
		_, err = enqueueOn(ctx, tx, orgID, EnqueueOpts{
			Queue: "default", JobType: "coordinate_automatic_publication", Priority: 5,
			DedupeKey: &dedupeKey, MaxAttempts: 5,
			Payload: map[string]string{"org_id": orgID.String(), "session_id": runID.String()},
		})
		if err != nil {
			return fmt.Errorf("queue automatic publication coordination: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.publishStatus(ctx, &session)
	if persistDiffSnapshot {
		s.publishWorkspaceGenerationChanged(ctx, orgID, runID, updated.Revision, updated.UpdatedAt, "diff_snapshot")
	}
	return nil
}
