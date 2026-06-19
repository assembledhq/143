package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type AutomationGoalImprovementStore struct {
	db DBTX
}

func NewAutomationGoalImprovementStore(db DBTX) *AutomationGoalImprovementStore {
	return &AutomationGoalImprovementStore{db: db}
}

const automationGoalImprovementColumns = `id, org_id, automation_id, repository_id,
	mode, status, input_name, input_goal, input_config, base_goal_hash, evidence_snapshot,
	proposed_goal, proposal, confidence, warnings, error_message, analysis_session_id,
	created_by, applied_by, applied_at, created_at, updated_at`

func scanAutomationGoalImprovement(row pgx.Row) (models.AutomationGoalImprovement, error) {
	var improvement models.AutomationGoalImprovement
	err := row.Scan(
		&improvement.ID, &improvement.OrgID, &improvement.AutomationID, &improvement.RepositoryID,
		&improvement.Mode, &improvement.Status, &improvement.InputName, &improvement.InputGoal,
		&improvement.InputConfig, &improvement.BaseGoalHash, &improvement.EvidenceSnapshot,
		&improvement.ProposedGoal, &improvement.Proposal, &improvement.Confidence,
		&improvement.Warnings, &improvement.ErrorMessage, &improvement.AnalysisSessionID,
		&improvement.CreatedBy, &improvement.AppliedBy, &improvement.AppliedAt,
		&improvement.CreatedAt, &improvement.UpdatedAt,
	)
	return improvement, err
}

func (s *AutomationGoalImprovementStore) Create(ctx context.Context, orgID uuid.UUID, improvement *models.AutomationGoalImprovement) error {
	inputConfig := improvement.InputConfig
	if len(inputConfig) == 0 {
		inputConfig = json.RawMessage(`{}`)
	}
	evidenceSnapshot := improvement.EvidenceSnapshot
	if len(evidenceSnapshot) == 0 {
		evidenceSnapshot = json.RawMessage(`{}`)
	}
	proposal := improvement.Proposal
	if len(proposal) == 0 {
		proposal = json.RawMessage(`{}`)
	}
	warnings := improvement.Warnings
	if len(warnings) == 0 {
		warnings = json.RawMessage(`[]`)
	}
	query := fmt.Sprintf(`INSERT INTO automation_goal_improvements (
			org_id, automation_id, repository_id, mode, status, input_name, input_goal,
			input_config, base_goal_hash, evidence_snapshot, proposed_goal, proposal,
			confidence, warnings, error_message, analysis_session_id, created_by
		) VALUES (
			@org_id, @automation_id, @repository_id, @mode, @status, @input_name, @input_goal,
			@input_config, @base_goal_hash, @evidence_snapshot, @proposed_goal, @proposal,
			@confidence, @warnings, @error_message, @analysis_session_id, @created_by
		) RETURNING %s`, automationGoalImprovementColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":              orgID,
		"automation_id":       improvement.AutomationID,
		"repository_id":       improvement.RepositoryID,
		"mode":                improvement.Mode,
		"status":              improvement.Status,
		"input_name":          improvement.InputName,
		"input_goal":          improvement.InputGoal,
		"input_config":        inputConfig,
		"base_goal_hash":      improvement.BaseGoalHash,
		"evidence_snapshot":   evidenceSnapshot,
		"proposed_goal":       improvement.ProposedGoal,
		"proposal":            proposal,
		"confidence":          improvement.Confidence,
		"warnings":            warnings,
		"error_message":       improvement.ErrorMessage,
		"analysis_session_id": improvement.AnalysisSessionID,
		"created_by":          improvement.CreatedBy,
	})
	created, err := scanAutomationGoalImprovement(row)
	if err != nil {
		return fmt.Errorf("create automation goal improvement: %w", err)
	}
	*improvement = created
	return nil
}

func (s *AutomationGoalImprovementStore) GetByID(ctx context.Context, orgID, improvementID uuid.UUID) (models.AutomationGoalImprovement, error) {
	query := fmt.Sprintf(`SELECT %s FROM automation_goal_improvements
		WHERE id = @id AND org_id = @org_id`, automationGoalImprovementColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{"id": improvementID, "org_id": orgID})
	return scanAutomationGoalImprovement(row)
}

func (s *AutomationGoalImprovementStore) GetByAutomation(ctx context.Context, orgID, automationID, improvementID uuid.UUID) (models.AutomationGoalImprovement, error) {
	query := fmt.Sprintf(`SELECT %s FROM automation_goal_improvements
		WHERE id = @id AND automation_id = @automation_id AND org_id = @org_id`, automationGoalImprovementColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":            improvementID,
		"automation_id": automationID,
		"org_id":        orgID,
	})
	return scanAutomationGoalImprovement(row)
}

func (s *AutomationGoalImprovementStore) GetByAnalysisSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.AutomationGoalImprovement, error) {
	query := fmt.Sprintf(`SELECT %s FROM automation_goal_improvements
		WHERE org_id = @org_id AND analysis_session_id = @analysis_session_id`, automationGoalImprovementColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID, "analysis_session_id": sessionID})
	return scanAutomationGoalImprovement(row)
}

func (s *AutomationGoalImprovementStore) ListByAutomation(ctx context.Context, orgID, automationID uuid.UUID, limit int) ([]models.AutomationGoalImprovement, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}
	query := fmt.Sprintf(`SELECT %s FROM automation_goal_improvements
		WHERE org_id = @org_id AND automation_id = @automation_id
		ORDER BY created_at DESC
		LIMIT @limit`, automationGoalImprovementColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":        orgID,
		"automation_id": automationID,
		"limit":         limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list automation goal improvements: %w", err)
	}
	defer rows.Close()
	improvements, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.AutomationGoalImprovement, error) {
		return scanAutomationGoalImprovement(row)
	})
	if err != nil {
		return nil, fmt.Errorf("scan automation goal improvements: %w", err)
	}
	return improvements, nil
}

func (s *AutomationGoalImprovementStore) HasRunningDeepByAutomation(ctx context.Context, orgID, automationID uuid.UUID) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM automation_goal_improvements
		WHERE org_id = @org_id
			AND automation_id = @automation_id
			AND mode = 'deep'
			AND status IN ('pending', 'running')
	)`, pgx.NamedArgs{"org_id": orgID, "automation_id": automationID}).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check running deep automation goal improvement: %w", err)
	}
	return exists, nil
}

func (s *AutomationGoalImprovementStore) MarkApplied(ctx context.Context, orgID, improvementID uuid.UUID, appliedBy *uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE automation_goal_improvements
		SET applied_by = @applied_by, applied_at = now(), updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": improvementID, "org_id": orgID, "applied_by": appliedBy})
	if err != nil {
		return fmt.Errorf("mark automation goal improvement applied: %w", err)
	}
	return nil
}

func (s *AutomationGoalImprovementStore) AttachAnalysisSession(ctx context.Context, orgID, improvementID, sessionID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE automation_goal_improvements
		SET analysis_session_id = @analysis_session_id, updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{
			"analysis_session_id": sessionID,
			"id":                  improvementID,
			"org_id":              orgID,
		})
	if err != nil {
		return fmt.Errorf("attach automation goal improvement analysis session: %w", err)
	}
	return nil
}

func (s *AutomationGoalImprovementStore) Complete(ctx context.Context, orgID, improvementID uuid.UUID, proposedGoal string, proposal json.RawMessage, confidence *string, warnings json.RawMessage) (models.AutomationGoalImprovement, error) {
	if len(proposal) == 0 {
		proposal = json.RawMessage(`{}`)
	}
	if len(warnings) == 0 {
		warnings = json.RawMessage(`[]`)
	}
	query := fmt.Sprintf(`UPDATE automation_goal_improvements
		SET status = @status, proposed_goal = @proposed_goal, proposal = @proposal,
			confidence = @confidence, warnings = @warnings, error_message = NULL, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status = 'running'
		RETURNING %s`, automationGoalImprovementColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"status":        models.AutomationGoalImprovementStatusCompleted,
		"proposed_goal": proposedGoal,
		"proposal":      proposal,
		"confidence":    confidence,
		"warnings":      warnings,
		"id":            improvementID,
		"org_id":        orgID,
	})
	improvement, err := scanAutomationGoalImprovement(row)
	if err != nil {
		return improvement, fmt.Errorf("complete automation goal improvement: %w", err)
	}
	return improvement, nil
}

func (s *AutomationGoalImprovementStore) Fail(ctx context.Context, orgID, improvementID uuid.UUID, errorMessage string) error {
	_, err := s.db.Exec(ctx, `UPDATE automation_goal_improvements
		SET status = @status, error_message = @error_message, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status IN ('pending', 'running')`,
		pgx.NamedArgs{
			"status":        models.AutomationGoalImprovementStatusFailed,
			"error_message": errorMessage,
			"id":            improvementID,
			"org_id":        orgID,
		})
	if err != nil {
		return fmt.Errorf("fail automation goal improvement: %w", err)
	}
	return nil
}

func (s *AutomationGoalImprovementStore) Cancel(ctx context.Context, orgID, improvementID uuid.UUID, errorMessage string) error {
	_, err := s.db.Exec(ctx, `UPDATE automation_goal_improvements
		SET status = @status, error_message = @error_message, updated_at = now()
		WHERE id = @id AND org_id = @org_id AND status IN ('pending', 'running')`,
		pgx.NamedArgs{
			"status":        models.AutomationGoalImprovementStatusCanceled,
			"error_message": errorMessage,
			"id":            improvementID,
			"org_id":        orgID,
		})
	if err != nil {
		return fmt.Errorf("cancel automation goal improvement: %w", err)
	}
	return nil
}

func (s *AutomationGoalImprovementStore) ExpireDrafts(ctx context.Context, orgID uuid.UUID, before time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE automation_goal_improvements
		SET status = @status, error_message = @error_message, updated_at = now()
		WHERE org_id = @org_id
			AND automation_id IS NULL
			AND status IN ('pending', 'running')
			AND created_at < @before`,
		pgx.NamedArgs{
			"status":        models.AutomationGoalImprovementStatusCanceled,
			"error_message": "draft proposal expired",
			"org_id":        orgID,
			"before":        before,
		})
	if err != nil {
		return fmt.Errorf("expire draft automation goal improvements: %w", err)
	}
	return nil
}

func (s *AutomationGoalImprovementStore) FailByAnalysisSession(ctx context.Context, orgID, sessionID uuid.UUID, errorMessage string) error {
	_, err := s.db.Exec(ctx, `UPDATE automation_goal_improvements
		SET status = @status, error_message = @error_message, updated_at = now()
		WHERE org_id = @org_id
			AND analysis_session_id = @analysis_session_id
			AND mode = 'deep'
			AND status IN ('pending', 'running')`,
		pgx.NamedArgs{
			"status":              models.AutomationGoalImprovementStatusFailed,
			"error_message":       errorMessage,
			"org_id":              orgID,
			"analysis_session_id": sessionID,
		})
	if err != nil {
		return fmt.Errorf("fail automation goal improvement by analysis session: %w", err)
	}
	return nil
}

func (s *AutomationGoalImprovementStore) CancelByAnalysisSession(ctx context.Context, orgID, sessionID uuid.UUID, errorMessage string) error {
	_, err := s.db.Exec(ctx, `UPDATE automation_goal_improvements
		SET status = @status, error_message = @error_message, updated_at = now()
		WHERE org_id = @org_id
			AND analysis_session_id = @analysis_session_id
			AND mode = 'deep'
			AND status IN ('pending', 'running')`,
		pgx.NamedArgs{
			"status":              models.AutomationGoalImprovementStatusCanceled,
			"error_message":       errorMessage,
			"org_id":              orgID,
			"analysis_session_id": sessionID,
		})
	if err != nil {
		return fmt.Errorf("cancel automation goal improvement by analysis session: %w", err)
	}
	return nil
}
