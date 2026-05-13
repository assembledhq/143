package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var humanInputRequestColumns = []string{
	"id", "org_id", "session_id", "thread_id", "turn_number", "agent_type",
	"provider_request_id", "request_kind", "status", "title", "body",
	"context", "blocks_phase", "choices", "response_schema", "provider_payload",
	"answer_text", "answer_payload", "answered_by", "answered_at", "expires_at", "created_at",
}

func newHumanInputRequestRow(id, orgID, sessionID uuid.UUID, now time.Time) []any {
	choices := []models.HumanInputChoice{{ID: "react", Label: "React"}}
	choiceJSON, _ := json.Marshal(choices)
	return []any{
		id, orgID, sessionID, (*uuid.UUID)(nil), 2, models.AgentTypeClaudeCode,
		humanInputStringPtr("toolu_123"), models.HumanInputRequestKindSingleChoice,
		models.HumanInputRequestStatusPending, "Framework", "Which framework?",
		(*string)(nil), (*string)(nil), choiceJSON, json.RawMessage(nil), json.RawMessage(`{"raw":true}`),
		(*string)(nil), json.RawMessage(nil), (*uuid.UUID)(nil), (*time.Time)(nil), (*time.Time)(nil), now,
	}
}

func TestSessionHumanInputRequestStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionHumanInputRequestStore(mock)
	now := time.Now()
	requestID := uuid.New()
	req := &models.HumanInputRequest{
		OrgID:             uuid.New(),
		SessionID:         uuid.New(),
		TurnNumber:        2,
		AgentType:         models.AgentTypeClaudeCode,
		ProviderRequestID: humanInputStringPtr("toolu_123"),
		Kind:              models.HumanInputRequestKindSingleChoice,
		Status:            models.HumanInputRequestStatusPending,
		Title:             "Framework",
		Body:              "Which framework?",
		Choices:           []models.HumanInputChoice{{ID: "react", Label: "React"}},
		ProviderPayload:   json.RawMessage(`{"raw":true}`),
	}

	mock.ExpectQuery("INSERT INTO session_human_input_requests").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(requestID, now))

	err = store.Create(context.Background(), req)
	require.NoError(t, err, "Create should insert the request")
	require.Equal(t, requestID, req.ID, "Create should hydrate the generated id")
	require.Equal(t, now, req.CreatedAt, "Create should hydrate created_at")
	require.NoError(t, mock.ExpectationsWereMet(), "all expectations should be met")
}

func TestSessionHumanInputRequestStore_ListBySession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionHumanInputRequestStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	requestID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_human_input_requests WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(humanInputRequestColumns).
				AddRow(newHumanInputRequestRow(requestID, orgID, sessionID, now)...),
		)

	requests, err := store.ListBySession(context.Background(), orgID, sessionID, HumanInputRequestFilters{
		Status: models.HumanInputRequestStatusPending,
	})
	require.NoError(t, err, "ListBySession should return requests")
	require.Equal(t, []models.HumanInputRequest{{
		ID:                requestID,
		OrgID:             orgID,
		SessionID:         sessionID,
		TurnNumber:        2,
		AgentType:         models.AgentTypeClaudeCode,
		ProviderRequestID: humanInputStringPtr("toolu_123"),
		Kind:              models.HumanInputRequestKindSingleChoice,
		Status:            models.HumanInputRequestStatusPending,
		Title:             "Framework",
		Body:              "Which framework?",
		Choices:           []models.HumanInputChoice{{ID: "react", Label: "React"}},
		ProviderPayload:   json.RawMessage(`{"raw":true}`),
		CreatedAt:         now,
	}}, requests, "ListBySession should scan the normalized request")
	require.NoError(t, mock.ExpectationsWereMet(), "all expectations should be met")
}

func TestSessionHumanInputRequestStore_AnswerPending(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionHumanInputRequestStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	requestID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	answer := "React"
	payload := json.RawMessage(`{"selected_choice_ids":["react"]}`)

	row := newHumanInputRequestRow(requestID, orgID, sessionID, now)
	row[8] = models.HumanInputRequestStatusAnswered
	row[16] = &answer
	row[17] = payload
	row[18] = &userID
	row[19] = &now

	mock.ExpectQuery("UPDATE session_human_input_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(humanInputRequestColumns).AddRow(row...))

	request, err := store.AnswerPending(context.Background(), orgID, sessionID, requestID, &answer, payload, userID)
	require.NoError(t, err, "AnswerPending should update a pending request")
	require.Equal(t, models.HumanInputRequestStatusAnswered, request.Status, "AnswerPending should return answered status")
	require.Equal(t, answer, *request.AnswerText, "AnswerPending should persist answer text")
	require.Equal(t, userID, *request.AnsweredBy, "AnswerPending should persist answering user")
	require.NoError(t, mock.ExpectationsWereMet(), "all expectations should be met")
}

func TestSessionHumanInputRequestStore_AnswerLatestPendingFreeTextBySessionRequiresSessionScope(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionHumanInputRequestStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	requestID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	answer := "Use React"
	payload := json.RawMessage(`{"answer_text":"Use React"}`)

	row := newHumanInputRequestRow(requestID, orgID, sessionID, now)
	row[3] = (*uuid.UUID)(nil)
	row[7] = models.HumanInputRequestKindFreeText
	row[8] = models.HumanInputRequestStatusAnswered
	row[16] = &answer
	row[17] = payload
	row[18] = &userID
	row[19] = &now

	mock.ExpectQuery(`UPDATE session_human_input_requests(?s).*thread_id IS NULL`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(humanInputRequestColumns).AddRow(row...))

	request, err := store.AnswerLatestPendingFreeTextBySession(context.Background(), orgID, sessionID, answer, userID)
	require.NoError(t, err, "AnswerLatestPendingFreeTextBySession should update a session-scoped request")
	require.Equal(t, requestID, request.ID, "AnswerLatestPendingFreeTextBySession should return the answered request")
	require.Nil(t, request.ThreadID, "AnswerLatestPendingFreeTextBySession should only answer session-scoped requests")
	require.Equal(t, models.HumanInputRequestStatusAnswered, request.Status, "AnswerLatestPendingFreeTextBySession should mark the request answered")
	require.NoError(t, mock.ExpectationsWereMet(), "all expectations should be met")
}

func TestSessionHumanInputRequestStore_AnswerLatestPendingFreeTextByThread(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionHumanInputRequestStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	requestID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	answer := "Use React"
	payload := json.RawMessage(`{"answer_text":"Use React"}`)

	row := newHumanInputRequestRow(requestID, orgID, sessionID, now)
	row[3] = &threadID
	row[7] = models.HumanInputRequestKindFreeText
	row[8] = models.HumanInputRequestStatusAnswered
	row[16] = &answer
	row[17] = payload
	row[18] = &userID
	row[19] = &now

	mock.ExpectQuery("UPDATE session_human_input_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(humanInputRequestColumns).AddRow(row...))

	request, err := store.AnswerLatestPendingFreeTextByThread(context.Background(), orgID, sessionID, threadID, answer, userID)
	require.NoError(t, err, "AnswerLatestPendingFreeTextByThread should update a pending thread-scoped request")
	require.Equal(t, requestID, request.ID, "AnswerLatestPendingFreeTextByThread should return the answered request")
	require.Equal(t, &threadID, request.ThreadID, "AnswerLatestPendingFreeTextByThread should preserve thread scope")
	require.Equal(t, models.HumanInputRequestStatusAnswered, request.Status, "AnswerLatestPendingFreeTextByThread should mark the request answered")
	require.Equal(t, answer, *request.AnswerText, "AnswerLatestPendingFreeTextByThread should persist answer text")
	require.NoError(t, mock.ExpectationsWereMet(), "all expectations should be met")
}

func TestSessionHumanInputRequestStore_CountPending(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		call      func(context.Context, *SessionHumanInputRequestStore, uuid.UUID, uuid.UUID, uuid.UUID) (int, error)
		setupMock func(pgxmock.PgxPoolIface)
		expected  int
	}{
		{
			name: "counts pending requests for session",
			call: func(ctx context.Context, store *SessionHumanInputRequestStore, orgID, sessionID, _ uuid.UUID) (int, error) {
				return store.CountPendingBySession(ctx, orgID, sessionID)
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT count\\(\\*\\).*session_human_input_requests").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
			},
			expected: 2,
		},
		{
			name: "counts pending requests for thread scope",
			call: func(ctx context.Context, store *SessionHumanInputRequestStore, orgID, sessionID, threadID uuid.UUID) (int, error) {
				return store.CountPendingByThread(ctx, orgID, sessionID, &threadID)
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT count\\(\\*\\).*session_human_input_requests").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
			},
			expected: 1,
		},
		{
			name: "counts pending requests for unscoped session prompt",
			call: func(ctx context.Context, store *SessionHumanInputRequestStore, orgID, sessionID, _ uuid.UUID) (int, error) {
				return store.CountPendingByThread(ctx, orgID, sessionID, nil)
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT count\\(\\*\\).*thread_id IS NOT DISTINCT FROM").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionHumanInputRequestStore(mock)
			tt.setupMock(mock)

			count, err := tt.call(context.Background(), store, uuid.New(), uuid.New(), uuid.New())
			require.NoError(t, err, "count method should not return an error")
			require.Equal(t, tt.expected, count, "count method should return the expected pending count")
			require.NoError(t, mock.ExpectationsWereMet(), "all expectations should be met")
		})
	}
}

func humanInputStringPtr(s string) *string {
	return &s
}
