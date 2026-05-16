package worker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestCreateAndAttachLinearAgentSessionUsesSingleTransaction(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	agentSessionRowID := uuid.New()
	issueID := uuid.New()
	now := time.Now().UTC()

	t.Run("commits session rows and bridge attach together", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "test should create pgx mock")
		defer mock.Close()

		sessionID := uuid.New()
		primaryThreadID := uuid.New()
		session := &models.Session{
			OrgID:          orgID,
			AgentType:      models.AgentTypeCodex,
			Status:         string(models.SessionStatusPending),
			PrimaryIssueID: &issueID,
		}

		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO sessions").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).
				AddRow(sessionID, now, now))
		mock.ExpectQuery("INSERT INTO session_threads").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(primaryThreadID))
		mock.ExpectExec("INSERT INTO session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectExec("UPDATE linear_agent_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectCommit()

		err = createAndAttachLinearAgentSession(context.Background(), &Stores{
			Sessions: db.NewSessionStore(mock),
		}, orgID, agentSessionRowID, session)
		require.NoError(t, err, "session create and bridge attach should commit together")
		require.Equal(t, sessionID, session.ID, "helper should preserve the generated session id for downstream tail steps")
		require.Equal(t, primaryThreadID, *session.PrimaryThreadID, "helper should preserve the generated primary thread id")
		require.NoError(t, mock.ExpectationsWereMet(), "all transactional expectations should be met")
	})

	t.Run("rolls back the session when bridge attach fails", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "test should create pgx mock")
		defer mock.Close()

		sessionID := uuid.New()
		session := &models.Session{
			OrgID:          orgID,
			AgentType:      models.AgentTypeCodex,
			Status:         string(models.SessionStatusPending),
			PrimaryIssueID: &issueID,
		}
		attachErr := errors.New("attach failed")

		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO sessions").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).
				AddRow(sessionID, now, now))
		mock.ExpectQuery("INSERT INTO session_threads").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
		mock.ExpectExec("INSERT INTO session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectExec("UPDATE linear_agent_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(attachErr)
		mock.ExpectRollback()

		err = createAndAttachLinearAgentSession(context.Background(), &Stores{
			Sessions: db.NewSessionStore(mock),
		}, orgID, agentSessionRowID, session)
		require.Error(t, err, "attach failure should abort the transaction so the session is not orphaned")
		require.Contains(t, err.Error(), "attach session", "error should identify the bridge attach step")
		require.NoError(t, mock.ExpectationsWereMet(), "failed attach should roll back the uncommitted session rows")
	})
}

func TestHandleLinearAgentCreatedReemitsBootstrapBeforeIssueFetch(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("handlers_linear_agent_created.go")
	require.NoError(t, err, "created handler source should be readable")

	body := string(src)
	emit := strings.Index(body, "emitLinearAgentBootstrap(")
	fetch := strings.Index(body, "client.FetchIssue(ctx, issueIdent)")
	require.NotEqual(t, -1, emit, "created handler should re-emit the dispatcher bootstrap activity")
	require.NotEqual(t, -1, fetch, "created handler should still fetch the live Linear issue")
	require.Less(t, emit, fetch, "worker bootstrap re-emit should happen before the potentially slower live issue fetch")
}
