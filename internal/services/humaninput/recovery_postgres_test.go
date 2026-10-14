package humaninput

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newHumanInputRecoveryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL for human input recovery tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect to disposable human input database server")
	t.Cleanup(func() { require.NoError(t, admin.Close(ctx), "close disposable database administration connection") })
	name := "human_input_recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	require.NoError(t, err, "create isolated database for real recovery transactions")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		require.NoError(t, err, "drop owned disposable human input database")
	})
	parsed, err := url.Parse(raw)
	require.NoError(t, err, "parse disposable database URL")
	parsed.Path = "/" + name
	source, err := filepath.Abs("../../../migrations")
	require.NoError(t, err, "resolve complete migration chain")
	migrations, err := migrate.New("file://"+source, parsed.String())
	require.NoError(t, err, "initialize recovery database migrations")
	require.NoError(t, migrations.Up(), "recovery transactions should exercise the complete current schema")
	sourceErr, databaseErr := migrations.Close()
	require.NoError(t, sourceErr, "close migration source")
	require.NoError(t, databaseErr, "close migration database connection")
	pool, err := pgxpool.New(ctx, parsed.String())
	require.NoError(t, err, "open isolated human input connection pool")
	t.Cleanup(pool.Close)
	return pool
}

func TestHumanInputRecoveryAtomicityPostgres(t *testing.T) {
	t.Parallel()
	pool := newHumanInputRecoveryPool(t)
	tests := []struct {
		name   string
		cancel bool
		reject bool
	}{
		{name: "answer atomically resumes thread"},
		{name: "cancel atomically resumes thread with denial", cancel: true},
		{name: "failed answer enqueue rolls back all prior writes", reject: true},
		{name: "failed cancellation enqueue rolls back all prior writes", cancel: true, reject: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, userID := uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name,settings) VALUES($1,$2,'{}')`, orgID, "recovery-"+orgID.String())
			require.NoError(t, err, "seed a tenant isolated from other parallel cases")
			_, err = pool.Exec(ctx, `INSERT INTO users(id,org_id,email,name,role) VALUES($1,$2,$3,'Recovery user','member')`, userID, orgID, userID.String()+"@example.test")
			require.NoError(t, err, "seed the authenticated answering user")
			sessions, requests, questions := db.NewSessionStore(pool), db.NewSessionHumanInputRequestStore(pool), db.NewSessionQuestionStore(pool)
			messages, threads, jobs := db.NewSessionMessageStore(pool), db.NewSessionThreadStore(pool), db.NewJobStore(pool)
			session := &models.Session{OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
			require.NoError(t, sessions.Create(ctx, session), "create the session and its durable primary thread")
			require.NotNil(t, session.PrimaryThreadID, "recovery fixture should have a primary thread")
			_, err = pool.Exec(ctx, `UPDATE sessions SET status='awaiting_input',sandbox_state='running',snapshot_key='durable-checkpoint',current_turn=2 WHERE org_id=$1 AND id=$2`, orgID, session.ID)
			require.NoError(t, err, "persist a ready human input checkpoint")
			_, err = pool.Exec(ctx, `UPDATE session_threads SET status='awaiting_input',current_turn=7 WHERE org_id=$1 AND id=$2`, orgID, *session.PrimaryThreadID)
			require.NoError(t, err, "persist a thread-scoped checkpoint on a different turn")
			request := &models.HumanInputRequest{OrgID: orgID, SessionID: session.ID, ThreadID: session.PrimaryThreadID, TurnNumber: 7, AgentType: session.AgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Approach", Body: "Which approach?"}
			require.NoError(t, requests.Create(ctx, request), "persist the pending provider input")
			question := &models.SessionQuestion{OrgID: orgID, SessionID: session.ID, QuestionText: request.Body, Options: []string{}, Status: models.SessionQuestionStatusPending}
			require.NoError(t, questions.Create(ctx, question), "persist the matching compatibility question")
			svc := NewDBService(sessions, requests, questions, messages, threads, jobs)
			before, err := svc.List(ctx, orgID, session.ID, RequestFilters{ThreadID: session.PrimaryThreadID})
			require.NoError(t, err, "service should list the owned pending thread input")
			require.Equal(t, []models.HumanInputRequest{*request}, before, "list should preserve the complete pending request")
			foreign := uuid.New()
			_, err = svc.List(ctx, foreign, session.ID, RequestFilters{})
			require.ErrorIs(t, err, ErrNotFound, "a known session id must not expose requests to another tenant")
			_, err = svc.Cancel(ctx, CancelInput{OrgID: foreign, SessionID: session.ID, RequestID: request.ID, UserID: userID})
			require.ErrorIs(t, err, ErrNotFound, "foreign tenant cancellation must stop before modifying the request")
			if tt.reject {
				// The trigger lives only in this owned disposable database and only
				// rejects this case's tenant. Other parallel cases remain independent.
				function := pgx.Identifier{"reject_resume_" + strings.ReplaceAll(orgID.String(), "-", "")}.Sanitize()
				_, err = pool.Exec(ctx, `CREATE FUNCTION `+function+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected resume enqueue failure'; END $$; CREATE TRIGGER `+function+` BEFORE INSERT ON jobs FOR EACH ROW WHEN (NEW.org_id = '`+orgID.String()+`'::uuid) EXECUTE FUNCTION `+function+`()`)
				require.NoError(t, err, "install a tenant-specific queue failure in the disposable database")
			}
			answer := "Use transactions."
			var result Result
			if tt.cancel {
				result, err = svc.Cancel(ctx, CancelInput{OrgID: orgID, SessionID: session.ID, RequestID: request.ID, UserID: userID})
			} else {
				result, err = svc.Answer(ctx, AnswerInput{OrgID: orgID, SessionID: session.ID, RequestID: request.ID, UserID: userID, Answer: models.HumanInputAnswerInput{AnswerText: &answer}})
			}
			if tt.reject {
				require.ErrorContains(t, err, "injected resume enqueue failure", "queue failure must propagate out of the real transaction")
				require.Equal(t, Result{}, result, "rolled-back recovery must not expose a success result")
			} else {
				require.NoError(t, err, "successful recovery should commit all state and the resume job")
			}
			after, err := svc.List(ctx, orgID, session.ID, RequestFilters{})
			require.NoError(t, err, "inspect the durable request after recovery")
			savedSession, err := sessions.GetByID(ctx, orgID, session.ID)
			require.NoError(t, err, "inspect durable session status")
			savedThread, err := threads.GetByID(ctx, orgID, *session.PrimaryThreadID)
			require.NoError(t, err, "inspect durable thread status")
			savedQuestion, err := questions.GetByID(ctx, orgID, question.ID)
			require.NoError(t, err, "inspect the compatibility question after recovery")
			savedMessages, err := messages.ListBySession(ctx, orgID, session.ID)
			require.NoError(t, err, "inspect the durable resume message")
			var jobCount int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='continue_session'`, orgID).Scan(&jobCount), "inspect tenant-scoped resume job count")
			if tt.reject {
				require.Equal(t, before, after, "enqueue failure must roll back the exact answer or cancellation")
				require.Equal(t, models.SessionStatusAwaitingInput, savedSession.Status, "enqueue failure must roll back the session claim")
				require.Equal(t, models.ThreadStatusAwaitingInput, savedThread.Status, "enqueue failure must roll back the thread claim")
				require.Equal(t, *question, savedQuestion, "enqueue failure must roll back the legacy question change")
				require.Equal(t, []models.SessionMessage{}, savedMessages, "enqueue failure must roll back the resume message")
				require.Equal(t, 0, jobCount, "enqueue failure must leave no resume job")
				return
			}
			expectedRequest := *request
			expectedRequest.Status = models.HumanInputRequestStatusAnswered
			expectedRequest.AnswerText, expectedRequest.AnswerPayload, expectedRequest.AnsweredBy = &answer, json.RawMessage(`{"answer_text": "Use transactions."}`), &userID
			if tt.cancel {
				answer = "Cancelled human input request."
				expectedRequest.Status = models.HumanInputRequestStatusCancelled
				expectedRequest.AnswerPayload = json.RawMessage(`{"decision": "deny", "cancelled": true}`)
			}
			expectedRequest.AnsweredAt = result.Request.AnsweredAt
			require.NotNil(t, expectedRequest.AnsweredAt, "completed input should persist an answer timestamp")
			require.Equal(t, []models.HumanInputRequest{expectedRequest}, after, "committed request should preserve the exact answer or structured cancellation")
			require.Equal(t, models.SessionStatusRunning, savedSession.Status, "committed resume should claim the session")
			require.Equal(t, models.ThreadStatusRunning, savedThread.Status, "committed resume should claim the originating thread")
			expectedQuestion := *question
			if tt.cancel {
				expectedQuestion.Status = models.SessionQuestionStatusSkipped
			} else {
				expectedQuestion.Status = models.SessionQuestionStatusAnswered
				expectedQuestion.AnswerText, expectedQuestion.AnsweredBy, expectedQuestion.AnsweredAt = &answer, &userID, savedQuestion.AnsweredAt
				require.NotNil(t, savedQuestion.AnsweredAt, "legacy question answer should be timestamped in the same transaction")
			}
			require.Equal(t, expectedQuestion, savedQuestion, "committed recovery should close exactly the matching compatibility question")
			require.NotNil(t, result.Message, "committed recovery should return its persisted message")
			expectedMessage := models.SessionMessage{ID: result.Message.ID, SessionID: session.ID, OrgID: orgID, ThreadID: session.PrimaryThreadID, UserID: &userID, TurnNumber: 8, Role: models.MessageRoleUser, Content: answer, CreatedAt: result.Message.CreatedAt, References: models.SessionInputReferences{}, Commands: models.SessionInputCommands{}}
			require.Equal(t, []models.SessionMessage{expectedMessage}, savedMessages, "committed message should use the thread's next turn and user attribution")
			require.Equal(t, 1, jobCount, "one completed answer should enqueue exactly one resume")
			var payload json.RawMessage
			var dedupe string
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload,dedupe_key FROM jobs WHERE org_id=$1 AND id=$2`, orgID, result.JobID).Scan(&payload, &dedupe), "resume job should be durable under the answering tenant")
			expectedPayload := map[string]string{"session_id": session.ID.String(), "org_id": orgID.String(), "thread_id": session.PrimaryThreadID.String(), "human_input_request_id": request.ID.String(), "human_input_request_status": "ready"}
			encoded, err := json.Marshal(expectedPayload)
			require.NoError(t, err, "encode the expected provider resume payload")
			require.JSONEq(t, string(encoded), string(payload), "resume job should preserve exact provider, tenant and thread identities")
			require.Equal(t, db.ContinueSessionDedupeKey(*session.PrimaryThreadID), dedupe, "resume dedupe should be thread scoped")
			_, err = svc.Answer(ctx, AnswerInput{OrgID: orgID, SessionID: session.ID, RequestID: request.ID, UserID: userID, Answer: models.HumanInputAnswerInput{AnswerText: &answer}})
			require.ErrorIs(t, err, ErrNotPending, "repeated delivery must not answer a completed request again")
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='continue_session'`, orgID).Scan(&jobCount), "inspect job count after duplicate delivery")
			require.Equal(t, 1, jobCount, "duplicate delivery must not enqueue additional work")
		})
	}
}
