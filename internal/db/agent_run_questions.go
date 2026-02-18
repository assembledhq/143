package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type AgentRunQuestionStore struct {
	db DBTX
}

func NewAgentRunQuestionStore(db DBTX) *AgentRunQuestionStore {
	return &AgentRunQuestionStore{db: db}
}

func (s *AgentRunQuestionStore) Create(ctx context.Context, q *models.AgentRunQuestion) error {
	query := `
		INSERT INTO agent_run_questions (agent_run_id, org_id, question_text, options, context, blocks_phase, status)
		VALUES (@agent_run_id, @org_id, @question_text, @options, @context, @blocks_phase, @status)
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"agent_run_id":  q.AgentRunID,
		"org_id":        q.OrgID,
		"question_text": q.QuestionText,
		"options":       q.Options,
		"context":       q.Context,
		"blocks_phase":  q.BlocksPhase,
		"status":        q.Status,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&q.ID, &q.CreatedAt)
}

func (s *AgentRunQuestionStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.AgentRunQuestion, error) {
	query := `
		SELECT id, agent_run_id, org_id, question_text, options, context,
		       blocks_phase, answer_text, answered_by, answered_at, status, created_at
		FROM agent_run_questions
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err != nil {
		return models.AgentRunQuestion{}, fmt.Errorf("query agent run question: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.AgentRunQuestion])
}

func (s *AgentRunQuestionStore) ListByRunID(ctx context.Context, orgID, agentRunID uuid.UUID) ([]models.AgentRunQuestion, error) {
	query := `
		SELECT id, agent_run_id, org_id, question_text, options, context,
		       blocks_phase, answer_text, answered_by, answered_at, status, created_at
		FROM agent_run_questions
		WHERE agent_run_id = @agent_run_id AND org_id = @org_id
		ORDER BY created_at ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"agent_run_id": agentRunID,
		"org_id":       orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query agent run questions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.AgentRunQuestion])
}

func (s *AgentRunQuestionStore) Answer(ctx context.Context, orgID, id uuid.UUID, answerText string, answeredBy uuid.UUID) error {
	query := `
		UPDATE agent_run_questions
		SET answer_text = @answer_text, answered_by = @answered_by, answered_at = now(), status = 'answered'
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":          id,
		"org_id":      orgID,
		"answer_text": answerText,
		"answered_by": answeredBy,
	})
	return err
}
