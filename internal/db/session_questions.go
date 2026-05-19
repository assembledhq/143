package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionQuestionStore struct {
	db DBTX
}

func NewSessionQuestionStore(db DBTX) *SessionQuestionStore {
	return &SessionQuestionStore{db: db}
}

func (s *SessionQuestionStore) Create(ctx context.Context, q *models.SessionQuestion) error {
	query := `
		INSERT INTO session_questions (session_id, org_id, question_text, options, context, blocks_phase, status)
		VALUES (@session_id, @org_id, @question_text, @options, @context, @blocks_phase, @status)
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"session_id":    q.SessionID,
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

func (s *SessionQuestionStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.SessionQuestion, error) {
	query := `
		SELECT id, session_id, org_id, question_text, options, context,
		       blocks_phase, answer_text, answered_by, answered_at, status, created_at
		FROM session_questions
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err != nil {
		return models.SessionQuestion{}, fmt.Errorf("query session question: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionQuestion])
}

func (s *SessionQuestionStore) ListByRunID(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionQuestion, error) {
	query := `
		SELECT id, session_id, org_id, question_text, options, context,
		       blocks_phase, answer_text, answered_by, answered_at, status, created_at
		FROM session_questions
		WHERE session_id = @session_id AND org_id = @org_id
		ORDER BY created_at ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id": sessionID,
		"org_id":     orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query session questions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionQuestion])
}

func (s *SessionQuestionStore) Answer(ctx context.Context, orgID, id uuid.UUID, answerText string, answeredBy uuid.UUID) error {
	query := `
		UPDATE session_questions
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

func (s *SessionQuestionStore) AnswerLatestPendingBySession(ctx context.Context, orgID, sessionID uuid.UUID, answerText string, answeredBy uuid.UUID) (models.SessionQuestion, error) {
	query := `
		UPDATE session_questions
		SET answer_text = @answer_text, answered_by = @answered_by, answered_at = now(), status = 'answered'
		WHERE id = (
			SELECT id
			FROM session_questions
			WHERE session_id = @session_id AND org_id = @org_id AND status = 'pending'
			ORDER BY created_at DESC
			LIMIT 1
		)
		RETURNING id, session_id, org_id, question_text, options, context,
		          blocks_phase, answer_text, answered_by, answered_at, status, created_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id":  sessionID,
		"org_id":      orgID,
		"answer_text": answerText,
		"answered_by": answeredBy,
	})
	if err != nil {
		return models.SessionQuestion{}, fmt.Errorf("answer latest pending session question: %w", err)
	}
	question, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionQuestion])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.SessionQuestion{}, pgx.ErrNoRows
		}
		return models.SessionQuestion{}, fmt.Errorf("collect answered session question: %w", err)
	}
	return question, nil
}

func (s *SessionQuestionStore) AnswerLatestPendingBySessionAndQuestion(ctx context.Context, orgID, sessionID uuid.UUID, questionText, answerText string, answeredBy uuid.UUID) (models.SessionQuestion, error) {
	query := `
		UPDATE session_questions
		SET answer_text = @answer_text, answered_by = @answered_by, answered_at = now(), status = 'answered'
		WHERE id = (
			SELECT id
			FROM session_questions
			WHERE session_id = @session_id
			  AND org_id = @org_id
			  AND status = 'pending'
			  AND question_text = @question_text
			ORDER BY created_at DESC
			LIMIT 1
		)
		RETURNING id, session_id, org_id, question_text, options, context,
		          blocks_phase, answer_text, answered_by, answered_at, status, created_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id":    sessionID,
		"org_id":        orgID,
		"question_text": questionText,
		"answer_text":   answerText,
		"answered_by":   answeredBy,
	})
	if err != nil {
		return models.SessionQuestion{}, fmt.Errorf("answer matching pending session question: %w", err)
	}
	question, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionQuestion])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.SessionQuestion{}, pgx.ErrNoRows
		}
		return models.SessionQuestion{}, fmt.Errorf("collect answered matching session question: %w", err)
	}
	return question, nil
}

func (s *SessionQuestionStore) SkipLatestPendingBySessionAndQuestion(ctx context.Context, orgID, sessionID uuid.UUID, questionText string) error {
	query := `
		UPDATE session_questions
		SET status = 'skipped'
		WHERE id = (
			SELECT id
			FROM session_questions
			WHERE session_id = @session_id
			  AND org_id = @org_id
			  AND status = 'pending'
			  AND question_text = @question_text
			ORDER BY created_at DESC
			LIMIT 1
		)`

	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"session_id":    sessionID,
		"org_id":        orgID,
		"question_text": questionText,
	})
	if err != nil {
		return fmt.Errorf("skip matching pending session question: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
