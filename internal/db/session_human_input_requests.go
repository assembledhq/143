package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type HumanInputRequestFilters struct {
	Status   models.HumanInputRequestStatus
	ThreadID *uuid.UUID
}

type SessionHumanInputRequestStore struct {
	db DBTX
}

func NewSessionHumanInputRequestStore(db DBTX) *SessionHumanInputRequestStore {
	return &SessionHumanInputRequestStore{db: db}
}

const humanInputRequestSelectColumns = `
	id, org_id, session_id, thread_id, turn_number, agent_type,
	provider_request_id, request_kind, status, title, body, context,
	blocks_phase, choices, response_schema, provider_payload,
	answer_text, answer_payload, answered_by, answered_at, expires_at, created_at`

func (s *SessionHumanInputRequestStore) Create(ctx context.Context, req *models.HumanInputRequest) error {
	if req.Status == "" {
		req.Status = models.HumanInputRequestStatusPending
	}
	if req.Choices == nil {
		req.Choices = []models.HumanInputChoice{}
	}

	query := `
		INSERT INTO session_human_input_requests (
			org_id, session_id, thread_id, turn_number, agent_type,
			provider_request_id, request_kind, status, title, body, context,
			blocks_phase, choices, response_schema, provider_payload,
			answer_text, answer_payload
		)
		VALUES (
			@org_id, @session_id, @thread_id, @turn_number, @agent_type,
			@provider_request_id, @request_kind, @status, @title, @body, @context,
			@blocks_phase, @choices, @response_schema, @provider_payload,
			@answer_text, @answer_payload
		)
		RETURNING id, created_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":              req.OrgID,
		"session_id":          req.SessionID,
		"thread_id":           req.ThreadID,
		"turn_number":         req.TurnNumber,
		"agent_type":          req.AgentType,
		"provider_request_id": req.ProviderRequestID,
		"request_kind":        req.Kind,
		"status":              req.Status,
		"title":               req.Title,
		"body":                req.Body,
		"context":             req.Context,
		"blocks_phase":        req.BlocksPhase,
		"choices":             req.Choices,
		"response_schema":     nullRawMessage(req.ResponseSchema),
		"provider_payload":    nullRawMessage(req.ProviderPayload),
		"answer_text":         req.AnswerText,
		"answer_payload":      nullRawMessage(req.AnswerPayload),
	})
	if err := row.Scan(&req.ID, &req.CreatedAt); err != nil {
		return fmt.Errorf("create session human input request: %w", err)
	}
	return nil
}

func (s *SessionHumanInputRequestStore) GetByID(ctx context.Context, orgID, sessionID, id uuid.UUID) (models.HumanInputRequest, error) {
	query := `
		SELECT ` + humanInputRequestSelectColumns + `
		FROM session_human_input_requests
		WHERE org_id = @org_id AND session_id = @session_id AND id = @id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
		"id":         id,
	})
	if err != nil {
		return models.HumanInputRequest{}, fmt.Errorf("query session human input request: %w", err)
	}
	return collectHumanInputRequest(rows)
}

func (s *SessionHumanInputRequestStore) GetByIDForUpdate(ctx context.Context, orgID, sessionID, id uuid.UUID) (models.HumanInputRequest, error) {
	query := `
		SELECT ` + humanInputRequestSelectColumns + `
		FROM session_human_input_requests
		WHERE org_id = @org_id AND session_id = @session_id AND id = @id
		FOR UPDATE`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
		"id":         id,
	})
	if err != nil {
		return models.HumanInputRequest{}, fmt.Errorf("query session human input request for update: %w", err)
	}
	return collectHumanInputRequest(rows)
}

func (s *SessionHumanInputRequestStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID, filters HumanInputRequestFilters) ([]models.HumanInputRequest, error) {
	query := `
		SELECT ` + humanInputRequestSelectColumns + `
		FROM session_human_input_requests
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND (@status::text IS NULL OR status = @status)
		  AND (@thread_id::uuid IS NULL OR thread_id IS NOT DISTINCT FROM @thread_id)
		ORDER BY created_at ASC, id ASC`

	var status *models.HumanInputRequestStatus
	if filters.Status != "" {
		status = &filters.Status
	}
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
		"status":     status,
		"thread_id":  filters.ThreadID,
	})
	if err != nil {
		return nil, fmt.Errorf("query session human input requests: %w", err)
	}
	return collectHumanInputRequests(rows)
}

func (s *SessionHumanInputRequestStore) CountPendingBySession(ctx context.Context, orgID, sessionID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM session_human_input_requests
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND status = 'pending'`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"session_id": sessionID,
		},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending session human input requests: %w", err)
	}
	return count, nil
}

func (s *SessionHumanInputRequestStore) CountPendingByThread(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM session_human_input_requests
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND thread_id IS NOT DISTINCT FROM @thread_id
		  AND status = 'pending'`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"session_id": sessionID,
			"thread_id":  threadID,
		},
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending thread human input requests: %w", err)
	}
	return count, nil
}

func (s *SessionHumanInputRequestStore) AnswerPending(
	ctx context.Context,
	orgID, sessionID, id uuid.UUID,
	answerText *string,
	answerPayload json.RawMessage,
	answeredBy uuid.UUID,
) (models.HumanInputRequest, error) {
	query := `
		UPDATE session_human_input_requests
		SET answer_text = @answer_text,
		    answer_payload = @answer_payload,
		    answered_by = @answered_by,
		    answered_at = now(),
		    status = 'answered'
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND id = @id
		  AND status = 'pending'
		RETURNING ` + humanInputRequestSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":         orgID,
		"session_id":     sessionID,
		"id":             id,
		"answer_text":    answerText,
		"answer_payload": nullRawMessage(answerPayload),
		"answered_by":    answeredBy,
	})
	if err != nil {
		return models.HumanInputRequest{}, fmt.Errorf("answer session human input request: %w", err)
	}
	req, err := collectHumanInputRequest(rows)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.HumanInputRequest{}, pgx.ErrNoRows
		}
		return models.HumanInputRequest{}, fmt.Errorf("collect answered session human input request: %w", err)
	}
	return req, nil
}

func (s *SessionHumanInputRequestStore) CancelPending(
	ctx context.Context,
	orgID, sessionID, id uuid.UUID,
	answerText *string,
	answerPayload json.RawMessage,
	cancelledBy uuid.UUID,
) (models.HumanInputRequest, error) {
	query := `
		UPDATE session_human_input_requests
		SET answer_text = @answer_text,
		    answer_payload = @answer_payload,
		    answered_by = @answered_by,
		    answered_at = now(),
		    status = 'cancelled'
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND id = @id
		  AND status = 'pending'
		RETURNING ` + humanInputRequestSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":         orgID,
		"session_id":     sessionID,
		"id":             id,
		"answer_text":    answerText,
		"answer_payload": nullRawMessage(answerPayload),
		"answered_by":    cancelledBy,
	})
	if err != nil {
		return models.HumanInputRequest{}, fmt.Errorf("cancel session human input request: %w", err)
	}
	req, err := collectHumanInputRequest(rows)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.HumanInputRequest{}, pgx.ErrNoRows
		}
		return models.HumanInputRequest{}, fmt.Errorf("collect cancelled session human input request: %w", err)
	}
	return req, nil
}

func (s *SessionHumanInputRequestStore) AnswerLatestPendingFreeTextBySession(ctx context.Context, orgID, sessionID uuid.UUID, answerText string, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	payload, err := MarshalHumanInputAnswerPayload(models.HumanInputAnswerInput{AnswerText: &answerText})
	if err != nil {
		return models.HumanInputRequest{}, err
	}
	query := `
		UPDATE session_human_input_requests
		SET answer_text = @answer_text,
		    answer_payload = @answer_payload,
		    answered_by = @answered_by,
		    answered_at = now(),
		    status = 'answered'
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND thread_id IS NULL
		  AND status = 'pending'
		  AND id = (
			SELECT id
			FROM session_human_input_requests
			WHERE org_id = @org_id
			  AND session_id = @session_id
			  AND thread_id IS NULL
			  AND status = 'pending'
			  AND request_kind = 'free_text'
			ORDER BY created_at DESC
			LIMIT 1
		)
		RETURNING ` + humanInputRequestSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":         orgID,
		"session_id":     sessionID,
		"answer_text":    answerText,
		"answer_payload": payload,
		"answered_by":    answeredBy,
	})
	if err != nil {
		return models.HumanInputRequest{}, fmt.Errorf("answer latest pending free-text human input request: %w", err)
	}
	req, err := collectHumanInputRequest(rows)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.HumanInputRequest{}, pgx.ErrNoRows
		}
		return models.HumanInputRequest{}, fmt.Errorf("collect answered free-text human input request: %w", err)
	}
	return req, nil
}

func (s *SessionHumanInputRequestStore) AnswerLatestPendingFreeTextByThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID, answerText string, answeredBy uuid.UUID) (models.HumanInputRequest, error) {
	payload, err := MarshalHumanInputAnswerPayload(models.HumanInputAnswerInput{AnswerText: &answerText})
	if err != nil {
		return models.HumanInputRequest{}, err
	}
	query := `
		UPDATE session_human_input_requests
		SET answer_text = @answer_text,
		    answer_payload = @answer_payload,
		    answered_by = @answered_by,
		    answered_at = now(),
		    status = 'answered'
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND thread_id IS NOT DISTINCT FROM @thread_id
		  AND status = 'pending'
		  AND id = (
			SELECT id
			FROM session_human_input_requests
			WHERE org_id = @org_id
			  AND session_id = @session_id
			  AND thread_id IS NOT DISTINCT FROM @thread_id
			  AND status = 'pending'
			  AND request_kind = 'free_text'
			ORDER BY created_at DESC
			LIMIT 1
		)
		RETURNING ` + humanInputRequestSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":         orgID,
		"session_id":     sessionID,
		"thread_id":      threadID,
		"answer_text":    answerText,
		"answer_payload": payload,
		"answered_by":    answeredBy,
	})
	if err != nil {
		return models.HumanInputRequest{}, fmt.Errorf("answer latest pending thread free-text human input request: %w", err)
	}
	req, err := collectHumanInputRequest(rows)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.HumanInputRequest{}, pgx.ErrNoRows
		}
		return models.HumanInputRequest{}, fmt.Errorf("collect answered thread free-text human input request: %w", err)
	}
	return req, nil
}

func MarshalHumanInputAnswerPayload(input models.HumanInputAnswerInput) (json.RawMessage, error) {
	payload := map[string]any{}
	if input.AnswerText != nil {
		payload["answer_text"] = *input.AnswerText
	}
	if len(input.SelectedChoiceIDs) > 0 {
		payload["selected_choice_ids"] = input.SelectedChoiceIDs
	}
	if len(input.AnswerPayload) > 0 {
		var nested any
		if err := json.Unmarshal(input.AnswerPayload, &nested); err != nil {
			return nil, fmt.Errorf("unmarshal answer payload: %w", err)
		}
		payload["answer_payload"] = nested
	}
	if len(payload) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal answer payload: %w", err)
	}
	return b, nil
}

func collectHumanInputRequest(rows pgx.Rows) (models.HumanInputRequest, error) {
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return models.HumanInputRequest{}, err
		}
		return models.HumanInputRequest{}, pgx.ErrNoRows
	}
	req, err := scanHumanInputRequest(rows)
	if err != nil {
		return models.HumanInputRequest{}, err
	}
	if rows.Next() {
		return models.HumanInputRequest{}, fmt.Errorf("expected one human input request row")
	}
	if err := rows.Err(); err != nil {
		return models.HumanInputRequest{}, err
	}
	return req, nil
}

func collectHumanInputRequests(rows pgx.Rows) ([]models.HumanInputRequest, error) {
	defer rows.Close()
	var requests []models.HumanInputRequest
	for rows.Next() {
		req, err := scanHumanInputRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if requests == nil {
		return []models.HumanInputRequest{}, nil
	}
	return requests, nil
}

func scanHumanInputRequest(rows pgx.Rows) (models.HumanInputRequest, error) {
	var req models.HumanInputRequest
	var choicesJSON []byte
	if err := rows.Scan(
		&req.ID,
		&req.OrgID,
		&req.SessionID,
		&req.ThreadID,
		&req.TurnNumber,
		&req.AgentType,
		&req.ProviderRequestID,
		&req.Kind,
		&req.Status,
		&req.Title,
		&req.Body,
		&req.Context,
		&req.BlocksPhase,
		&choicesJSON,
		&req.ResponseSchema,
		&req.ProviderPayload,
		&req.AnswerText,
		&req.AnswerPayload,
		&req.AnsweredBy,
		&req.AnsweredAt,
		&req.ExpiresAt,
		&req.CreatedAt,
	); err != nil {
		return models.HumanInputRequest{}, err
	}
	if len(choicesJSON) > 0 {
		if err := json.Unmarshal(choicesJSON, &req.Choices); err != nil {
			return models.HumanInputRequest{}, fmt.Errorf("unmarshal human input choices: %w", err)
		}
	}
	if req.Choices == nil {
		req.Choices = []models.HumanInputChoice{}
	}
	return req, nil
}

func nullRawMessage(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
