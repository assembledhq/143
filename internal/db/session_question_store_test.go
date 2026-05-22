package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var questionColumns = []string{
	"id", "session_id", "org_id", "question_text", "options", "context",
	"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
}

func newQuestionRow(id, sessionID, orgID uuid.UUID, now time.Time) []any {
	return []any{
		id, sessionID, orgID, "Should we proceed?", []string{"yes", "no"}, (*string)(nil),
		(*string)(nil), (*string)(nil), (*uuid.UUID)(nil), (*time.Time)(nil), "pending", now,
	}
}

func TestSessionQuestionStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionQuestionStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	q := &models.SessionQuestion{
		SessionID:    uuid.New(),
		OrgID:        uuid.New(),
		QuestionText: "Should we proceed?",
		Options:      []string{"yes", "no"},
		Status:       "pending",
	}

	mock.ExpectQuery("INSERT INTO session_questions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), q)
	require.NoError(t, err, "should create agent run question without error")
	require.Equal(t, generatedID, q.ID, "should set the generated ID on the question")
	require.Equal(t, now, q.CreatedAt, "should set the created_at timestamp on the question")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionQuestionStore_GetByID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionQuestionStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_questions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(questionColumns).
				AddRow(newQuestionRow(id, sessionID, orgID, now)...),
		)

	q, err := store.GetByID(context.Background(), orgID, id)
	require.NoError(t, err, "should retrieve question by ID without error")
	require.Equal(t, id, q.ID, "should return the correct question ID")
	require.Equal(t, sessionID, q.SessionID, "should return the correct agent run ID")
	require.Equal(t, orgID, q.OrgID, "should return the correct org ID")
	require.Equal(t, "Should we proceed?", q.QuestionText, "should return the correct question text")
	require.Equal(t, []string{"yes", "no"}, q.Options, "should return the correct options")
	require.Equal(t, models.SessionQuestionStatusPending, q.Status, "should return the correct status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionQuestionStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionQuestionStore(mock)

	mock.ExpectQuery("SELECT .+ FROM session_questions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(questionColumns))

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "should return an error when question is not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionQuestionStore_ListByRunID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionQuestionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_questions WHERE session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(questionColumns).
				AddRow(newQuestionRow(id1, sessionID, orgID, now)...).
				AddRow(newQuestionRow(id2, sessionID, orgID, now)...),
		)

	questions, err := store.ListByRunID(context.Background(), orgID, sessionID)
	require.NoError(t, err, "should list questions by run ID without error")
	require.Len(t, questions, 2, "should return both questions for the agent run")
	require.Equal(t, id1, questions[0].ID, "first question should have the correct ID")
	require.Equal(t, sessionID, questions[0].SessionID, "first question should have the correct agent run ID")
	require.Equal(t, orgID, questions[0].OrgID, "first question should have the correct org ID")
	require.Equal(t, "Should we proceed?", questions[0].QuestionText, "first question should have the correct text")
	require.Equal(t, []string{"yes", "no"}, questions[0].Options, "first question should have the correct options")
	require.Equal(t, models.SessionQuestionStatusPending, questions[0].Status, "first question should have the correct status")
	require.Equal(t, id2, questions[1].ID, "second question should have the correct ID")
	require.Equal(t, sessionID, questions[1].SessionID, "second question should have the correct agent run ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionQuestionStore_Answer_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionQuestionStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	userID := uuid.New()

	mock.ExpectExec("UPDATE session_questions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.Answer(context.Background(), orgID, id, "yes", userID)
	require.NoError(t, err, "should answer question without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionQuestionStore_AnswerLatestPendingBySession_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionQuestionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	questionID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	answer := "Option B"

	mock.ExpectQuery("UPDATE session_questions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(questionColumns).
				AddRow(
					questionID, sessionID, orgID, "Should we proceed?", []string{"yes", "no"}, (*string)(nil),
					(*string)(nil), &answer, &userID, &now, "answered", now,
				),
		)

	question, err := store.AnswerLatestPendingBySession(context.Background(), orgID, sessionID, answer, userID)
	require.NoError(t, err, "should answer latest pending question without error")
	require.Equal(t, questionID, question.ID, "should return the answered question ID")
	require.Equal(t, models.SessionQuestionStatusAnswered, question.Status, "should return the updated question status")
	require.Equal(t, answer, *question.AnswerText, "should persist the answer text")
	require.Equal(t, userID, *question.AnsweredBy, "should persist the answering user")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionQuestionStore_AnswerLatestPendingBySession_NoPendingQuestion(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionQuestionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("UPDATE session_questions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(questionColumns))

	_, err = store.AnswerLatestPendingBySession(context.Background(), orgID, sessionID, "Option B", userID)
	require.Error(t, err, "should return an error when no pending question exists for the session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionQuestionStore_AnswerLatestPendingBySession_QueryError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionQuestionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("UPDATE session_questions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db down"))

	_, err = store.AnswerLatestPendingBySession(context.Background(), orgID, sessionID, "Option B", userID)
	require.Error(t, err, "should return an error when the update query fails")
	require.Contains(t, err.Error(), "answer latest pending session question", "should wrap query failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

type errRowDB struct{}

func (errRowDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (errRowDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return errRows{}, nil }
func (errRowDB) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (errRowDB) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults  { return nil }

type errRows struct{}

func (errRows) Close()                                       {}
func (errRows) Err() error                                   { return nil }
func (errRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (errRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (errRows) Next() bool                                   { return true }
func (errRows) Scan(...any) error                            { return errors.New("scan failed") }
func (errRows) Values() ([]any, error)                       { return nil, nil }
func (errRows) RawValues() [][]byte                          { return nil }
func (errRows) Conn() *pgx.Conn                              { return nil }

func TestSessionQuestionStore_AnswerLatestPendingBySession_CollectError(t *testing.T) {
	t.Parallel()

	store := NewSessionQuestionStore(errRowDB{})

	_, err := store.AnswerLatestPendingBySession(context.Background(), uuid.New(), uuid.New(), "Option B", uuid.New())
	require.Error(t, err, "should return an error when collecting the answered row fails")
	require.Contains(t, err.Error(), "collect answered session question", "should wrap collect failures")
}
