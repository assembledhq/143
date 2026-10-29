package db

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func personalBaseUpdateFixture(t *testing.T) (*pgxpool.Pool, uuid.UUID, models.AutomationActionActor) {
	t.Helper()
	pool := testpostgres.NewPool(t, t.Context(), "../../migrations", 2)
	a := models.AutomationActionActor{OrgID: uuid.New(), RepositoryID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), RunID: uuid.New(), JobID: uuid.New(), AttemptToken: uuid.New()}
	automationID, userID, integrationID := uuid.New(), uuid.New(), uuid.New()
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO organizations(id,name) VALUES($1,'Self-update test')`, a.OrgID)
	batch.Queue(`INSERT INTO users(id,org_id,email,name) VALUES($1,$2,'owner@example.test','Test owner')`, userID, a.OrgID)
	batch.Queue(`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integrationID, a.OrgID)
	batch.Queue(`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id)
		VALUES($1,$2,$3,1,'test/self-update','https://example.test/repo.git',1)`, a.RepositoryID, a.OrgID, integrationID)
	batch.Queue(`INSERT INTO automations(id,org_id,repository_id,name,goal,identity_scope,created_by,base_branch)
		VALUES($1,$2,$3,'Stack tests','Prune redundant tests','personal',$4,'main')`, automationID, a.OrgID, a.RepositoryID, userID)
	batch.Queue(`INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,status,config_snapshot,capability_snapshot)
		VALUES($1,$2,$3,'Prune redundant tests','running','{"identity_scope":"personal","base_branch":"main"}',
		'[{"id":"automation_management","access_level":"write"}]')`, a.RunID, a.OrgID, automationID)
	require.NoError(t, pool.SendBatch(t.Context(), batch).Close(), "create Personal automation and frozen run")
	session := &models.Session{ID: a.SessionID, OrgID: a.OrgID, RepositoryID: &a.RepositoryID, Origin: models.SessionOriginAutomation,
		TriggeredByUserID: &userID, AutomationRunID: &a.RunID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning,
		AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, InteractionMode: models.SessionInteractionModeSingleRun,
		ValidationPolicy:   models.SessionValidationPolicyOnSessionEnd,
		CapabilitySnapshot: []models.AgentCapabilitySnapshotItem{{ID: models.AgentCapabilityAutomationManagement, AccessLevel: models.AgentCapabilityAccessWrite}}}
	require.NoError(t, NewSessionStore(pool).Create(t.Context(), session), "create durable automation session link")
	a.SessionID = session.ID
	batch = &pgx.Batch{}
	batch.Queue(`INSERT INTO session_threads(id,org_id,session_id,agent_type,label) VALUES($1,$2,$3,'codex','Main')`, a.ThreadID, a.OrgID, a.SessionID)
	batch.Queue(`INSERT INTO jobs(id,org_id,queue,job_type,status,lock_token,lease_expires_at,payload)
		VALUES($1,$2,'agent','run_agent','running',$3,now()+interval '1 hour',jsonb_build_object('session_id',$4::text,'thread_id',$5::text))`,
		a.JobID, a.OrgID, a.AttemptToken, a.SessionID, a.ThreadID)
	require.NoError(t, pool.SendBatch(t.Context(), batch).Close(), "create executing job and thread")
	return pool, automationID, a
}

func TestAutomationStore_PersonalBaseUpdatePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		change          string
		actor           func(models.AutomationActionActor) models.AutomationActionActor
		allowed         bool
		otherAutomation bool
	}{
		{name: "own running attempt", allowed: true},
		{name: "other Personal routine with same owner", otherAutomation: true},
		{name: "continuation", allowed: true, change: `UPDATE jobs SET job_type='continue_session' WHERE org_id=$1`},
		{name: "legacy job without thread", allowed: true, change: `UPDATE jobs SET payload=payload-'thread_id' WHERE org_id=$1`},
		{name: "other tenant", actor: func(a models.AutomationActionActor) models.AutomationActionActor { a.OrgID = uuid.New(); return a }},
		{name: "other repository", actor: func(a models.AutomationActionActor) models.AutomationActionActor {
			a.RepositoryID = uuid.New()
			return a
		}},
		{name: "other session", actor: func(a models.AutomationActionActor) models.AutomationActionActor { a.SessionID = uuid.New(); return a }},
		{name: "other thread", actor: func(a models.AutomationActionActor) models.AutomationActionActor { a.ThreadID = uuid.New(); return a }},
		{name: "other run", actor: func(a models.AutomationActionActor) models.AutomationActionActor { a.RunID = uuid.New(); return a }},
		{name: "other job", actor: func(a models.AutomationActionActor) models.AutomationActionActor { a.JobID = uuid.New(); return a }},
		{name: "stale attempt", actor: func(a models.AutomationActionActor) models.AutomationActionActor {
			a.AttemptToken = uuid.New()
			return a
		}},
		{name: "missing lease", actor: func(a models.AutomationActionActor) models.AutomationActionActor { a.AttemptToken = uuid.Nil; return a }},
		{name: "paused routine", change: `UPDATE automations SET enabled=false WHERE org_id=$1`},
		{name: "org routine", change: `UPDATE automations SET identity_scope='org' WHERE org_id=$1`},
		{name: "missing owner", change: `UPDATE automations SET created_by=NULL WHERE org_id=$1`},
		{name: "finished run", change: `UPDATE automation_runs SET status='completed' WHERE org_id=$1`},
		{name: "org run snapshot", change: `UPDATE automation_runs SET config_snapshot='{"identity_scope":"org"}' WHERE org_id=$1`},
		{name: "run missing capability", change: `UPDATE automation_runs SET capability_snapshot='[]' WHERE org_id=$1`},
		{name: "run read capability", change: `UPDATE automation_runs SET capability_snapshot='[{"id":"automation_management","access_level":"read"}]' WHERE org_id=$1`},
		{name: "session missing capability", change: `UPDATE session_execution_metadata SET capability_snapshot='[]' WHERE org_id=$1`},
		{name: "unlinked session", change: `DELETE FROM session_automation_links WHERE org_id=$1`},
		{name: "manual session", change: `UPDATE sessions SET origin='manual' WHERE org_id=$1`},
		{name: "wrong execution identity", change: `UPDATE sessions SET triggered_by_user_id=NULL WHERE org_id=$1`},
		{name: "deleted session", change: `UPDATE sessions SET deleted_at=now() WHERE org_id=$1`},
		{name: "inactive repository", change: `UPDATE repositories SET status='paused' WHERE org_id=$1`},
		{name: "expired lease", change: `UPDATE jobs SET lease_expires_at=now()-interval '1 second' WHERE org_id=$1`},
		{name: "finished job", change: `UPDATE jobs SET status='succeeded' WHERE org_id=$1`},
		{name: "other job type", change: `UPDATE jobs SET job_type='open_pr' WHERE org_id=$1`},
		{name: "wrong payload session", change: `UPDATE jobs SET payload=jsonb_set(payload,'{session_id}',to_jsonb(gen_random_uuid()::text)) WHERE org_id=$1`},
		{name: "wrong payload thread", change: `UPDATE jobs SET payload=jsonb_set(payload,'{thread_id}',to_jsonb(gen_random_uuid()::text)) WHERE org_id=$1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, id, actor := personalBaseUpdateFixture(t)
			orgID := actor.OrgID
			if tt.otherAutomation {
				otherID := uuid.New()
				_, err := pool.Exec(t.Context(), `INSERT INTO automations(id,org_id,repository_id,name,goal,identity_scope,created_by,base_branch)
					SELECT $3,org_id,repository_id,'Other routine',goal,identity_scope,created_by,base_branch FROM automations WHERE org_id=$1 AND id=$2`, orgID, id, otherID)
				require.NoError(t, err, "create another Personal routine owned by the same user")
				id = otherID
			}
			if tt.change != "" {
				_, err := pool.Exec(t.Context(), tt.change, orgID)
				require.NoError(t, err, "prepare denied authorization state")
			}
			if tt.actor != nil {
				actor = tt.actor(actor)
			}
			store := NewAutomationStore(pool)
			before, err := store.GetByID(t.Context(), orgID, id)
			require.NoError(t, err, "read original automation")
			old, updated, err := store.UpdatePersonalBaseBranch(t.Context(), orgID, id, actor, "stack/parent")
			if tt.allowed {
				require.NoError(t, err, "executing Personal attempt can advance its own base")
				require.Equal(t, before, old, "audit receives the exact previous configuration")
				before.BaseBranch = "stack/parent"
				before.UpdatedAt = updated.UpdatedAt
				require.Equal(t, before, updated, "only the base branch and update timestamp change")
			} else {
				require.ErrorIs(t, err, ErrAutomationSelfUpdateUnauthorized, "reject untrusted or expired authority")
			}
			saved, err := store.GetByID(t.Context(), orgID, id)
			require.NoError(t, err, "read durable result")
			require.Equal(t, before, saved, "denials do not write and successful updates preserve all other fields")
		})
	}
}
