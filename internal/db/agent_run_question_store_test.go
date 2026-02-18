package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var questionColumns = []string{
	"id", "agent_run_id", "org_id", "question_text", "options", "context",
	"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
}

func newQuestionRow(id, agentRunID, orgID uuid.UUID, now time.Time) []any {
	return []any{
		id, agentRunID, orgID, "Should we proceed?", []string{"yes", "no"}, (*string)(nil),
		(*string)(nil), (*string)(nil), (*uuid.UUID)(nil), (*time.Time)(nil), "pending", now,
	}
}

func TestAgentRunQuestionStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunQuestionStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	q := &models.AgentRunQuestion{
		AgentRunID:   uuid.New(),
		OrgID:        uuid.New(),
		QuestionText: "Should we proceed?",
		Options:      []string{"yes", "no"},
		Status:       "pending",
	}

	mock.ExpectQuery("INSERT INTO agent_run_questions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), q)
	require.NoError(t, err)
	assert.Equal(t, generatedID, q.ID)
	assert.Equal(t, now, q.CreatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunQuestionStore_GetByID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunQuestionStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	agentRunID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM agent_run_questions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(questionColumns).
				AddRow(newQuestionRow(id, agentRunID, orgID, now)...),
		)

	q, err := store.GetByID(context.Background(), orgID, id)
	require.NoError(t, err)
	assert.Equal(t, id, q.ID)
	assert.Equal(t, "Should we proceed?", q.QuestionText)
	assert.Equal(t, "pending", q.Status)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunQuestionStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunQuestionStore(mock)

	mock.ExpectQuery("SELECT .+ FROM agent_run_questions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(questionColumns))

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunQuestionStore_ListByRunID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunQuestionStore(mock)
	orgID := uuid.New()
	agentRunID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM agent_run_questions WHERE agent_run_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(questionColumns).
				AddRow(newQuestionRow(id1, agentRunID, orgID, now)...).
				AddRow(newQuestionRow(id2, agentRunID, orgID, now)...),
		)

	questions, err := store.ListByRunID(context.Background(), orgID, agentRunID)
	require.NoError(t, err)
	assert.Len(t, questions, 2)
	assert.Equal(t, id1, questions[0].ID)
	assert.Equal(t, id2, questions[1].ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunQuestionStore_Answer_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunQuestionStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	userID := uuid.New()

	mock.ExpectExec("UPDATE agent_run_questions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.Answer(context.Background(), orgID, id, "yes", userID)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
