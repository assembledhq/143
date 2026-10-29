package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrAutomationSelfUpdateUnauthorized = errors.New("automation self-update is not authorized")

func (s *AutomationStore) UpdatePersonalBaseBranch(ctx context.Context, orgID, automationID uuid.UUID, actor models.AutomationActionActor, baseBranch string) (models.Automation, models.Automation, error) {
	if orgID != actor.OrgID || orgID == uuid.Nil || automationID == uuid.Nil || actor.RepositoryID == uuid.Nil ||
		actor.SessionID == uuid.Nil || actor.ThreadID == uuid.Nil || actor.RunID == uuid.Nil || actor.JobID == uuid.Nil || actor.AttemptToken == uuid.Nil {
		return models.Automation{}, models.Automation{}, ErrAutomationSelfUpdateUnauthorized
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.Automation{}, models.Automation{}, fmt.Errorf("begin automation self-update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	before, err := s.LockByIDForUpdate(ctx, tx, orgID, automationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Automation{}, models.Automation{}, ErrAutomationSelfUpdateUnauthorized
	}
	if err != nil {
		return models.Automation{}, models.Automation{}, fmt.Errorf("lock automation self-update: %w", err)
	}
	after, err := scanAutomation(tx.QueryRow(ctx, `UPDATE automations a SET base_branch=$8, updated_at=now()
		WHERE a.org_id=$1 AND a.id=$2 AND a.repository_id=$3
		AND a.identity_scope='personal' AND a.enabled AND a.deleted_at IS NULL
		AND EXISTS (
			SELECT 1 FROM automation_runs r
			JOIN session_automation_links sal ON sal.org_id=r.org_id AND sal.automation_run_id=r.id
			JOIN sessions sess ON sess.org_id=sal.org_id AND sess.id=sal.session_id
			JOIN session_execution_metadata sem ON sem.org_id=sess.org_id AND sem.session_id=sess.id
			JOIN session_threads thread ON thread.org_id=sess.org_id AND thread.session_id=sess.id AND thread.id=$5
			JOIN repositories repo ON repo.org_id=sess.org_id AND repo.id=sess.repository_id
			JOIN jobs j ON j.org_id=sess.org_id AND j.id=$7
			WHERE r.org_id=$1 AND r.id=$4 AND r.automation_id=a.id AND r.target_id IS NULL AND r.status='running'
			AND r.config_snapshot->>'identity_scope'='personal'
			AND r.capability_snapshot @> '[{"id":"automation_management","access_level":"write"}]'::jsonb
			AND sess.id=$6 AND sess.repository_id=$3 AND sess.origin='automation' AND sess.deleted_at IS NULL
			AND sess.triggered_by_user_id=a.created_by AND repo.status='active'
			AND sem.capability_snapshot @> '[{"id":"automation_management","access_level":"write"}]'::jsonb
			AND j.status='running' AND j.lock_token=$9 AND j.lease_expires_at>clock_timestamp()
			AND j.job_type IN ('run_agent','continue_session') AND j.payload->>'session_id'=sess.id::text
			AND (COALESCE(j.payload->>'thread_id','')='' OR j.payload->>'thread_id'=thread.id::text)
		) RETURNING `+prefixColumns("a.", automationColumns), orgID, automationID, actor.RepositoryID, actor.RunID,
		actor.ThreadID, actor.SessionID, actor.JobID, baseBranch, actor.AttemptToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Automation{}, models.Automation{}, ErrAutomationSelfUpdateUnauthorized
	}
	if err != nil {
		return models.Automation{}, models.Automation{}, fmt.Errorf("update personal automation base branch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return models.Automation{}, models.Automation{}, fmt.Errorf("commit automation self-update: %w", err)
	}
	return before, after, nil
}
