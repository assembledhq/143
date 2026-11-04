package thread

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// The runtime revert retains the full schema, including unrelated package
// credentials. Exercise real legacy writes against those retained constraints.
func TestLegacySendRetainedSchemaPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		status        models.SessionStatus
		jobs, pending int
	}{
		{name: "idle", status: models.SessionStatusIdle, jobs: 1},
		{name: "resume", status: models.SessionStatusCompleted, jobs: 1},
		{name: "answer", status: models.SessionStatusAwaitingInput, jobs: 1},
		{name: "queue", status: models.SessionStatusRunning, jobs: 1, pending: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../../migrations", 2)
			org, user := uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic revert tenant')`, org)
			require.NoError(t, err, "seed isolated tenant")
			_, err = pool.Exec(ctx, `INSERT INTO users(id,org_id,email,name,role) VALUES($1,$2,$3,'Synthetic actor','member')`, user, org, user.String()+"@example.invalid")
			require.NoError(t, err, "seed isolated actor")
			sessions := db.NewSessionStore(pool)
			session := models.Session{OrgID: org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin ordinary legacy creation")
			defer func() { _ = tx.Rollback(ctx) }()
			require.NoError(t, sessions.CreateInTx(ctx, tx, &session), "create real legacy session on retained schema")
			require.NoError(t, tx.Commit(ctx), "commit session with primary lane")
			require.NotNil(t, session.PrimaryThreadID, "creation supplies a primary lane")
			changeset, err := sessions.GetPrimaryChangesetID(ctx, org, session.ID)
			require.NoError(t, err, "load real changeset")
			_, err = pool.Exec(ctx, `UPDATE sessions SET status=$3 WHERE org_id=$1 AND id=$2`, org, session.ID, tt.status)
			require.NoError(t, err, "set the legacy session state")
			_, err = pool.Exec(ctx, `UPDATE session_threads SET status=$3 WHERE org_id=$1 AND session_id=$2`, org, session.ID, tt.status)
			require.NoError(t, err, "set the legacy lane state")
			service := NewService(db.NewSessionThreadStore(pool), sessions, db.NewSessionMessageStore(pool), db.NewSessionLogStore(pool), db.NewJobStore(pool), zerolog.Nop())
			service.SetThreadInboxStore(db.NewThreadInboxStore(pool), pool)
			service.SetThreadRuntimeStore(db.NewThreadRuntimeStore(pool))
			if tt.status == models.SessionStatusAwaitingInput {
				question := models.SessionQuestion{OrgID: org, SessionID: session.ID, QuestionText: "Synthetic question", Status: models.SessionQuestionStatusPending, Options: []string{}}
				require.NoError(t, db.NewSessionQuestionStore(pool).Create(ctx, &question), "seed pending question")
				_, err = pool.Exec(ctx, `INSERT INTO session_human_input_requests(org_id,session_id,thread_id,agent_type,request_kind,title,body) VALUES($1,$2,$3,'codex','free_text','Synthetic question','Synthetic body')`, org, session.ID, *session.PrimaryThreadID)
				require.NoError(t, err, "seed pending free-text input")
				service.SetQuestionStore(db.NewSessionQuestionStore(pool))
				service.SetHumanInputRequestStore(db.NewSessionHumanInputRequestStore(pool))
			}
			result, err := service.SendMessage(ctx, SendMessageInput{OrgID: org, SessionID: session.ID, ThreadID: *session.PrimaryThreadID, ChangesetID: &changeset, UserID: &user, Message: "Synthetic answer", ClientMessageID: "synthetic-key"})
			require.NoError(t, err, "legacy send must work with retained lifecycle and package-credential schema")
			require.Equal(t, "Synthetic answer", result.Message.Content, "return the accepted message")
			if tt.status == models.SessionStatusAwaitingInput {
				require.NotNil(t, result.AnsweredQuestion, "question answer is usable")
				require.NotNil(t, result.AnsweredHumanInput, "free-text answer is usable")
			}
			var actual [4]int
			require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2),(SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2),(SELECT count(*) FROM jobs WHERE org_id=$1),(SELECT pending_message_count FROM session_threads WHERE org_id=$1 AND id=$3)`, org, session.ID, *session.PrimaryThreadID).Scan(&actual[0], &actual[1], &actual[2], &actual[3]), "read the durable send result")
			require.Equal(t, [4]int{1, 1, tt.jobs, tt.pending}, actual, "message, inbox, job and pending counter retain legacy behavior")
			var version int
			var dirty, enabled bool
			var cutovers, nonzero int
			require.NoError(t, pool.QueryRow(ctx, `SELECT version,dirty,(SELECT intents_enabled FROM caller_send_intent_control),(SELECT count(*) FROM session_lifecycle_cutovers WHERE org_id=$1),(SELECT count(*) FROM sessions WHERE org_id=$1 AND lifecycle_version<>0) FROM schema_migrations`, org).Scan(&version, &dirty, &enabled, &cutovers, &nonzero), "verify retained schema and disabled participation")
			require.Equal(t, 396, version, "the latest compatible migrations remain installed")
			require.False(t, dirty, "migration ledger stays clean")
			require.False(t, enabled, "legacy send cannot enable participation")
			require.Equal(t, [2]int{0, 0}, [2]int{cutovers, nonzero}, "legacy send cannot activate the lifecycle protocol")
		})
	}
}
