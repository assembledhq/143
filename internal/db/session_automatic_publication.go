package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
		if persistDiffSnapshot && result.DiffHeadCommitSHA != nil && strings.TrimSpace(*result.DiffHeadCommitSHA) != "" {
			if err := reconcileAutomaticPublicationHead(ctx, tx, orgID, runID, *session.AutomationRunID, *result.DiffHeadCommitSHA); err != nil {
				return err
			}
		}
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

// A completed automation may capture the same head that its agent already
// pushed and imported. That final persisted snapshot resolves the primary
// changeset's external-update warning without changing any remote-head lease.
// Older automation sessions retain issue-trigger provenance; the scoped run
// link and frozen policy still establish their automatic publication authority.
// Other publication owners and changeset states retain their existing guards.
func reconcileAutomaticPublicationHead(ctx context.Context, tx DBTX, orgID, sessionID, automationRunID uuid.UUID, head string) error {
	_, err := tx.Exec(ctx, `UPDATE session_changesets sc
	 SET status = 'published_branch'
	 FROM sessions sess, automation_runs ar, session_diff_snapshots snapshot
	 WHERE sc.org_id = @org_id AND sc.session_id = @session_id AND sc.is_primary
	  AND sc.status = 'external_update_detected'
	  AND sc.head_sha = @head AND sc.expected_remote_head_sha = @head
	  AND sess.org_id = @org_id AND sess.id = @session_id
	  AND sess.status = 'completed' AND sess.interaction_mode <> 'interactive'
	  AND sess.origin IN ('automation', 'issue_trigger')
	  AND snapshot.org_id = @org_id AND snapshot.session_id = @session_id
	  AND snapshot.id = sess.latest_diff_snapshot_id AND snapshot.head_commit_sha = @head
	  AND NULLIF(btrim(snapshot.diff), '') IS NOT NULL
	  AND ar.org_id = @org_id AND ar.id = @automation_run_id AND ar.target_id IS NULL
	  AND (ar.config_snapshot IS NULL OR jsonb_typeof(ar.config_snapshot) IN ('object', 'null'))
	  AND COALESCE(NULLIF(ar.config_snapshot->>'publish_policy', ''), 'pull_request') = 'pull_request'
	  AND COALESCE(NULLIF(ar.config_snapshot->>'session_continuity', ''), 'per_run') = 'per_run'
	  AND EXISTS (SELECT 1 FROM session_automation_links link
	   WHERE link.org_id = @org_id AND link.session_id = @session_id AND link.automation_run_id = ar.id)
	  AND NOT EXISTS (SELECT 1 FROM pull_requests pr
	   WHERE pr.org_id = @org_id AND pr.session_id = @session_id
	    AND (pr.changeset_id = sc.id OR pr.changeset_id IS NULL))
	  AND NOT EXISTS (SELECT 1 FROM session_publications publication
	   WHERE publication.org_id = @org_id AND publication.session_id = @session_id AND publication.changeset_id = sc.id)`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "automation_run_id": automationRunID, "head": head})
	if err != nil {
		return fmt.Errorf("reconcile captured automatic publication head: %w", err)
	}
	return nil
}
