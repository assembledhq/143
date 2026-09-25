package db

import (
	"context"
	"encoding/json"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func actionPostgres(t *testing.T, continuous bool) (*pgxpool.Pool, models.AutomationActionActor) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for reservation and lease fencing proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect database")
	schema := "actions_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err, "create isolated schema")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, err, "remove isolated schema")
		require.NoError(t, admin.Close(ctx), "close admin")
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse connection")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `CREATE TABLE organizations(id uuid PRIMARY KEY);
 CREATE TABLE automations(id uuid PRIMARY KEY,org_id uuid,enabled boolean DEFAULT true,deleted_at timestamptz);
 CREATE TABLE repositories(id uuid PRIMARY KEY,org_id uuid,full_name text DEFAULT 'owner/repo',installation_id bigint DEFAULT 1,status text DEFAULT 'active',UNIQUE(id,org_id));
 CREATE TABLE sessions(id uuid PRIMARY KEY,org_id uuid,repository_id uuid,automation_owner_generation_id uuid,deleted_at timestamptz);
 CREATE TABLE session_threads(id uuid PRIMARY KEY,org_id uuid,session_id uuid);
 CREATE TABLE session_automation_links(session_id uuid PRIMARY KEY,org_id uuid,automation_run_id uuid);
 CREATE TABLE automation_targets(id uuid PRIMARY KEY,org_id uuid,automation_id uuid,repository_id uuid,target_key text DEFAULT '42',active_generation integer DEFAULT 1,target_kind text DEFAULT 'github_pull_request',lifecycle_state text DEFAULT 'open');
 CREATE TABLE automation_target_sessions(id uuid PRIMARY KEY,org_id uuid,target_id uuid,generation integer DEFAULT 1,session_id uuid,status text DEFAULT 'active');
 CREATE TABLE jobs(id uuid PRIMARY KEY,org_id uuid,job_type text DEFAULT 'run_agent',payload jsonb,status text DEFAULT 'running',lock_token uuid,lease_expires_at timestamptz DEFAULT now()+interval '1 hour');
 CREATE TABLE automation_runs(id uuid PRIMARY KEY,org_id uuid,automation_id uuid,status text DEFAULT 'running',config_snapshot jsonb DEFAULT '{}',target_id uuid,target_generation integer DEFAULT 1,session_id uuid,thread_id uuid,attempt_lock_token uuid,dispatch_state text DEFAULT 'executing',resolved_head_sha text,job_id uuid,capability_snapshot jsonb);
 CREATE TABLE agent_capability_policies(id uuid PRIMARY KEY,org_id uuid,automation_id uuid,active boolean DEFAULT true,policy_type text DEFAULT 'automation');
 CREATE TABLE agent_capability_policy_grants(id uuid PRIMARY KEY,org_id uuid,policy_id uuid,capability_id text DEFAULT 'automation_actions',enabled boolean DEFAULT true,access_level text DEFAULT 'write',config jsonb);`)
	require.NoError(t, err, "create authorization fixture tables")
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000292_automation_actions.up.sql"))
	require.NoError(t, err, "read actual migration")
	_, err = pool.Exec(ctx, string(migration))
	require.NoError(t, err, "apply actual action migration")
	a := models.AutomationActionActor{OrgID: uuid.New(), RepositoryID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), RunID: uuid.New(), AttemptToken: uuid.New(), JobID: uuid.New()}
	automation, target, generation, policy := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	// The configuration form includes empty Notion fields even when Notion is disabled.
	raw := json.RawMessage(`{"actions":["slack_notification","github_issue_comment"],"repository":"owner/repo","slack_channel_id":"C0123456789","notion_properties":{},"notion_data_source_id":""}`)
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO organizations VALUES($1)`, a.OrgID)
	batch.Queue(`INSERT INTO automations(id,org_id) VALUES($1,$2)`, automation, a.OrgID)
	batch.Queue(`INSERT INTO repositories(id,org_id) VALUES($1,$2)`, a.RepositoryID, a.OrgID)
	batch.Queue(`INSERT INTO sessions(id,org_id,repository_id,automation_owner_generation_id) VALUES($1,$2,$3,$4)`, a.SessionID, a.OrgID, a.RepositoryID, generation)
	batch.Queue(`INSERT INTO session_threads VALUES($1,$2,$3)`, a.ThreadID, a.OrgID, a.SessionID)
	batch.Queue(`INSERT INTO jobs(id,org_id,lock_token,payload) VALUES($1,$2,$3,jsonb_build_object('session_id',$4::text,'thread_id',$5::text))`, a.JobID, a.OrgID, a.AttemptToken, a.SessionID, a.ThreadID)
	batch.Queue(`INSERT INTO agent_capability_policies(id,org_id,automation_id) VALUES($1,$2,$3)`, policy, a.OrgID, automation)
	batch.Queue(`INSERT INTO agent_capability_policy_grants(id,org_id,policy_id,config) VALUES($1,$2,$3,$4)`, uuid.New(), a.OrgID, policy, raw)
	if continuous {
		batch.Queue(`INSERT INTO automation_targets(id,org_id,automation_id,repository_id) VALUES($1,$2,$3,$4)`, target, a.OrgID, automation, a.RepositoryID)
		batch.Queue(`INSERT INTO automation_target_sessions(id,org_id,target_id,session_id) VALUES($1,$2,$3,$4)`, generation, a.OrgID, target, a.SessionID)
		batch.Queue(`INSERT INTO automation_runs(id,org_id,automation_id,target_id,session_id,thread_id,attempt_lock_token,resolved_head_sha,job_id,capability_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,jsonb_build_array(jsonb_build_object('id','automation_actions','access_level','write','config',$10::jsonb)))`, a.RunID, a.OrgID, automation, target, a.SessionID, a.ThreadID, a.AttemptToken, strings.Repeat("a", 40), a.JobID, raw)
	} else {
		batch.Queue(`INSERT INTO automation_runs(id,org_id,automation_id,capability_snapshot) VALUES($1,$2,$3,jsonb_build_array(jsonb_build_object('id','automation_actions','access_level','write','config',$4::jsonb)))`, a.RunID, a.OrgID, automation, raw)
		batch.Queue(`INSERT INTO session_automation_links VALUES($1,$2,$3)`, a.SessionID, a.OrgID, a.RunID)
	}
	require.NoError(t, pool.SendBatch(ctx, batch).Close(), "seed authorized attempt")
	return pool, a
}
func reserveAction(t *testing.T, s *AutomationActionStore, a models.AutomationActionActor, step string, kind models.AutomationActionKind) models.AutomationAction {
	t.Helper()
	ctx := context.Background()
	scope, err := s.Resolve(ctx, a.OrgID, a)
	require.NoError(t, err, "resolve current attempt")
	r := models.AutomationActionRequest{AutomationActionKey: models.AutomationActionKey{OperationKey: "workflow", ActionKey: step}, Kind: kind, Text: "A notification"}
	if kind == models.AutomationActionComment && r.PRNumber == 0 {
		r.PRNumber = 42
		r.HeadSHA = strings.Repeat("a", 40)
	}
	payload, err := json.Marshal(struct {
		Request models.AutomationActionRequest `json:"request"`
	}{r})
	require.NoError(t, err, "encode frozen request")
	result, err := s.Reserve(ctx, a.OrgID, a, r, strings.Repeat("b", 64), scope.Config, payload)
	require.NoError(t, err, "reserve independent step")
	return result
}
func TestAutomationActionPostgresReservationAndFencing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		continuous bool
	}{{"scheduled or manual per-run", false}, {"continuous target", true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, a := actionPostgres(t, tt.continuous)
			s := NewAutomationActionStore(pool)
			ctx := context.Background()
			scope, err := s.Resolve(ctx, a.OrgID, a)
			require.NoError(t, err, "resolve fixture")
			action := reserveAction(t, s, a, "notify", models.AutomationActionSlack)
			const clients = 8
			results := make([]models.AutomationAction, clients)
			errs := make([]error, clients)
			var wg sync.WaitGroup
			var request struct {
				Request models.AutomationActionRequest `json:"request"`
			}
			require.NoError(t, json.Unmarshal(action.Payload, &request), "read request")
			for i := range clients {
				wg.Go(func() {
					results[i], errs[i] = s.Reserve(ctx, a.OrgID, a, request.Request, action.RequestDigest, scope.Config, action.Payload)
				})
			}
			wg.Wait()
			for i := range clients {
				require.NoError(t, errs[i], "identical reservations are idempotent")
				require.Equal(t, action, results[i], "all callers share one identity")
			}
			_, err = s.Reserve(ctx, a.OrgID, a, request.Request, strings.Repeat("c", 64), scope.Config, action.Payload)
			require.ErrorIs(t, err, ErrAutomationActionConflict, "changed content cannot overwrite a step")
			for i := range clients {
				wg.Go(func() { results[i], errs[i] = s.Claim(ctx, a.OrgID, a, action.ID) })
			}
			wg.Wait()
			successes := 0
			var claimed models.AutomationAction
			for i, err := range errs {
				if err == nil {
					successes++
					claimed = results[i]
				} else {
					require.ErrorIs(t, err, ErrAutomationActionBusy, "concurrent loser is fenced")
				}
			}
			require.Equal(t, 1, successes, "only one caller may send")
			require.WithinDuration(t, time.Now().Add(8*time.Second), *claimed.SendDeadlineAt, 2*time.Second, "deadline uses database time")
			require.ErrorIs(t, s.Finish(ctx, a.OrgID, claimed.ID, uuid.New(), models.AutomationActionSucceeded, "wrong", "", ""), ErrAutomationActionBusy, "wrong token cannot settle receipt")
			require.NoError(t, s.Finish(ctx, a.OrgID, claimed.ID, *claimed.SendToken, models.AutomationActionUnknown, "", "", "TIMEOUT"), "persist uncertainty")
			_, err = s.Claim(ctx, a.OrgID, a, claimed.ID)
			require.ErrorIs(t, err, ErrAutomationActionBusy, "unknown is never resent")
			other := reserveAction(t, s, a, "another-notification", models.AutomationActionSlack)
			_, err = s.Claim(ctx, a.OrgID, a, other.ID)
			require.NoError(t, err, "unrelated same-kind step remains callable")
			require.NoError(t, s.Finish(ctx, a.OrgID, claimed.ID, *claimed.SendToken, models.AutomationActionSucceeded, "receipt", "", ""), "late receipt can settle original send")
			_, err = s.Claim(ctx, a.OrgID, a, claimed.ID)
			require.ErrorIs(t, err, ErrAutomationActionBusy, "success is never resent")
		})
	}
}
func TestAutomationActionPostgresAuthorization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, sql  string
		mutate     func(*models.AutomationActionActor)
		targetOnly bool
	}{
		{name: "other org", mutate: func(a *models.AutomationActionActor) { a.OrgID = uuid.New() }},
		{name: "other repo", mutate: func(a *models.AutomationActionActor) { a.RepositoryID = uuid.New() }},
		{name: "other thread", mutate: func(a *models.AutomationActionActor) { a.ThreadID = uuid.New() }},
		{name: "other job", mutate: func(a *models.AutomationActionActor) { a.JobID = uuid.New() }},
		{name: "old attempt", mutate: func(a *models.AutomationActionActor) { a.AttemptToken = uuid.New() }},
		{name: "revoked grant", sql: `UPDATE agent_capability_policy_grants SET enabled=false`},
		{name: "disabled automation", sql: `UPDATE automations SET enabled=false`},
		{name: "inactive repository", sql: `UPDATE repositories SET status='inactive'`},
		{name: "deleted session", sql: `UPDATE sessions SET deleted_at=now()`},
		{name: "expired lease", sql: `UPDATE jobs SET lease_expires_at=now()-interval '1 second'`},
		{name: "completed job", sql: `UPDATE jobs SET status='completed'`},
		{name: "wrong job purpose", sql: `UPDATE jobs SET job_type='open_pr'`},
		{name: "wrong job session", sql: `UPDATE jobs SET payload='{}'`},
		{name: "changed config", sql: `UPDATE agent_capability_policy_grants SET config=config || '{"slack_channel_id":"C9999999999"}'::jsonb`},
		{name: "retired generation", sql: `UPDATE automation_target_sessions SET status='retired'`, targetOnly: true},
		{name: "other generation", sql: `UPDATE automation_targets SET active_generation=2`, targetOnly: true},
		{name: "closed target", sql: `UPDATE automation_targets SET lifecycle_state='closed'`, targetOnly: true},
	}
	for _, tt := range tests {
		for _, continuous := range []bool{false, true} {
			if tt.targetOnly && !continuous {
				continue
			}
			name := tt.name + "/per-run"
			if continuous {
				name = tt.name + "/continuous"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				pool, a := actionPostgres(t, continuous)
				if tt.mutate != nil {
					tt.mutate(&a)
				}
				if tt.sql != "" {
					_, err := pool.Exec(context.Background(), tt.sql)
					require.NoError(t, err, "modify isolated authority")
				}
				_, err := NewAutomationActionStore(pool).Resolve(context.Background(), a.OrgID, a)
				require.ErrorIs(t, err, ErrAutomationActionUnauthorized, "reject invalid authority in every mode")
			})
		}
	}
}
func TestAutomationActionPostgresAcrossRuns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                    string
		continuous, reconstruct bool
	}{{"later scheduled or manual run", false, true}, {"next continuous turn", true, false}, {"reconstructed continuous session", true, true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, a := actionPostgres(t, tt.continuous)
			s := NewAutomationActionStore(pool)
			ctx := context.Background()
			scope, err := s.Resolve(ctx, a.OrgID, a)
			require.NoError(t, err, "resolve first run")
			first := reserveAction(t, s, a, "done", models.AutomationActionSlack)
			pending := reserveAction(t, s, a, "pending", models.AutomationActionSlack)
			claimed, err := s.Claim(ctx, a.OrgID, a, first.ID)
			require.NoError(t, err, "claim first action")
			require.NoError(t, s.Finish(ctx, a.OrgID, first.ID, *claimed.SendToken, models.AutomationActionSucceeded, "receipt", "", ""), "persist receipt")
			next := a
			next.RunID, next.JobID, next.AttemptToken = uuid.New(), uuid.New(), uuid.New()
			generation := 1
			if tt.reconstruct {
				next.SessionID, next.ThreadID = uuid.New(), uuid.New()
				genID := uuid.New()
				_, err = pool.Exec(ctx, `INSERT INTO sessions(id,org_id,repository_id,automation_owner_generation_id) VALUES($1,$2,$3,$4)`, next.SessionID, next.OrgID, next.RepositoryID, genID)
				require.NoError(t, err, "create replacement session")
				_, err = pool.Exec(ctx, `INSERT INTO session_threads VALUES($1,$2,$3)`, next.ThreadID, next.OrgID, next.SessionID)
				require.NoError(t, err, "create replacement thread")
				if tt.continuous {
					generation = 2
					_, err = pool.Exec(ctx, `UPDATE automation_target_sessions SET status='retired';UPDATE automation_targets SET active_generation=2`)
					require.NoError(t, err, "retire old target generation")
					_, err = pool.Exec(ctx, `INSERT INTO automation_target_sessions(id,org_id,target_id,session_id,generation) VALUES($1,$2,$3,$4,2)`, genID, next.OrgID, scope.TargetID, next.SessionID)
					require.NoError(t, err, "register successor generation")
				}
			}
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='completed'; UPDATE automation_runs SET status='completed'`)
			require.NoError(t, err, "end previous attempt")
			_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,lock_token,payload) VALUES($1,$2,$3,jsonb_build_object('session_id',$4::text,'thread_id',$5::text))`, next.JobID, next.OrgID, next.AttemptToken, next.SessionID, next.ThreadID)
			require.NoError(t, err, "start successor attempt")
			_, err = pool.Exec(ctx, `INSERT INTO automation_runs(id,org_id,automation_id,target_id,target_generation,session_id,thread_id,attempt_lock_token,resolved_head_sha,job_id,capability_snapshot)
 SELECT $1,org_id,automation_id,target_id,$2,$3,$4,$5,resolved_head_sha,$6,capability_snapshot FROM automation_runs WHERE org_id=$7 AND id=$8`, next.RunID, generation, next.SessionID, next.ThreadID, next.AttemptToken, next.JobID, a.OrgID, a.RunID)
			require.NoError(t, err, "admit distinct run")
			if !tt.continuous {
				_, err = pool.Exec(ctx, `INSERT INTO session_automation_links VALUES($1,$2,$3)`, next.SessionID, next.OrgID, next.RunID)
				require.NoError(t, err, "bind per-run session")
			}
			_, err = s.Claim(ctx, a.OrgID, a, pending.ID)
			require.ErrorIs(t, err, ErrAutomationActionUnauthorized, "old job cannot send")
			same := reserveAction(t, s, next, "done", models.AutomationActionSlack)
			require.Equal(t, first.ID, same.ID, "later run reuses completed step identity")
			require.Equal(t, models.AutomationActionSucceeded, same.Status, "success survives reconstruction")
			resumed, err := s.Claim(ctx, next.OrgID, next, pending.ID)
			require.NoError(t, err, "later run resumes unfinished step")
			require.Equal(t, next.RunID, resumed.LastRunID, "receipt provenance records new run")
			historical, err := s.ResolveStatus(ctx, a.OrgID, a)
			require.NoError(t, err, "old signed membership can inspect history")
			require.Equal(t, scope.ScopeKey, historical.ScopeKey, "historical reads retain original namespace")
		})
	}
}
func TestAutomationActionPostgresExpiryAndOwnComment(t *testing.T) {
	t.Parallel()
	pool, a := actionPostgres(t, false)
	s := NewAutomationActionStore(pool)
	ctx := context.Background()
	scope, err := s.Resolve(ctx, a.OrgID, a)
	require.NoError(t, err, "resolve fixture")
	row := reserveAction(t, s, a, "comment", models.AutomationActionComment)
	claimed, err := s.Claim(ctx, a.OrgID, a, row.ID)
	require.NoError(t, err, "claim comment")
	body := "A notification\n\n<!-- 143-automation-action:" + row.ID.String() + " -->"
	found, err := s.ListAutomationActionCommentOwners(ctx, a.OrgID, a.RepositoryID, 42, 123, body)
	require.NoError(t, err, "lookup pre-receipt webhook")
	require.Equal(t, []uuid.UUID{scope.AutomationID}, found, "suppress exact own-app comment")
	for _, badOrg := range []uuid.UUID{uuid.New()} {
		found, err = s.ListAutomationActionCommentOwners(ctx, badOrg, a.RepositoryID, 42, 123, body)
		require.NoError(t, err, "query other tenant")
		require.Empty(t, found, "other tenant cannot match receipt")
	}
	found, err = s.ListAutomationActionCommentOwners(ctx, a.OrgID, a.RepositoryID, 42, 123, "copied "+body)
	require.NoError(t, err, "query copied marker")
	require.Empty(t, found, "marker alone cannot suppress comment")
	_, err = pool.Exec(ctx, `UPDATE automation_actions SET send_deadline_at=now()-interval '1 second' WHERE org_id=$1 AND id=$2`, a.OrgID, row.ID)
	require.NoError(t, err, "simulate crash deadline")
	require.NoError(t, s.Expire(ctx, a.OrgID, scope, "workflow"), "expire crashed send")
	_, err = s.Claim(ctx, a.OrgID, a, row.ID)
	require.ErrorIs(t, err, ErrAutomationActionBusy, "expired sends are uncertain")
	_, err = pool.Exec(ctx, `UPDATE agent_capability_policy_grants SET enabled=false`)
	require.NoError(t, err, "revoke current grant")
	require.NoError(t, s.Finish(ctx, a.OrgID, row.ID, *claimed.SendToken, models.AutomationActionSucceeded, "123", "", ""), "revocation must not discard late receipt")
	down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000292_automation_actions.down.sql"))
	require.NoError(t, err, "read rollback")
	_, err = pool.Exec(ctx, string(down))
	require.ErrorContains(t, err, "receipts exist", "rollback cannot erase duplicate protection")
}

func TestAutomationActionPostgresCompletedRunCannotSend(t *testing.T) {
	t.Parallel()
	tests := []struct{ status string }{{"completed"}, {"failed"}, {"skipped"}, {"pending"}}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			pool, actor := actionPostgres(t, false)
			store, ctx := NewAutomationActionStore(pool), context.Background()
			action := reserveAction(t, store, actor, "pending", models.AutomationActionSlack)
			_, err := pool.Exec(ctx, `UPDATE automation_runs SET status=$1`, tt.status)
			require.NoError(t, err, "end automation execution")
			_, err = pool.Exec(ctx, `UPDATE jobs SET job_type='continue_session'`)
			require.NoError(t, err, "simulate a fresh human continuation with a valid lease")
			_, err = store.Resolve(ctx, actor.OrgID, actor)
			require.ErrorIs(t, err, ErrAutomationActionUnauthorized, "human continuation cannot reuse an inactive automation grant")
			_, err = store.Claim(ctx, actor.OrgID, actor, action.ID)
			require.ErrorIs(t, err, ErrAutomationActionUnauthorized, "reserved work requires a currently executing automation run")
			_, err = store.ResolveStatus(ctx, actor.OrgID, actor)
			require.NoError(t, err, "historical receipts remain readable")
		})
	}
}

func TestAutomationActionPostgresConfigOrderAndOptionalHead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		bound bool
	}{{"independent notification", false}, {"commit-specific notification", true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, actor := actionPostgres(t, true)
			store, ctx := NewAutomationActionStore(pool), context.Background()
			scope, err := store.Resolve(ctx, actor.OrgID, actor)
			require.NoError(t, err, "resolve current target scope")
			request := models.AutomationActionRequest{AutomationActionKey: models.AutomationActionKey{OperationKey: "workflow", ActionKey: "notify"}, Kind: models.AutomationActionSlack, Text: "Report"}
			if tt.bound {
				request.PRNumber, request.HeadSHA = scope.PRNumber, scope.HeadSHA
			}
			payload, err := json.Marshal(struct {
				Request models.AutomationActionRequest `json:"request"`
			}{request})
			require.NoError(t, err, "encode frozen content")
			action, err := store.Reserve(ctx, actor.OrgID, actor, request, strings.Repeat("b", 64), scope.Config, payload)
			require.NoError(t, err, "reserve with optional precondition")
			_, err = pool.Exec(ctx, `UPDATE agent_capability_policy_grants SET config=jsonb_set(config,'{actions}','["github_issue_comment","slack_notification"]'::jsonb)`)
			require.NoError(t, err, "reorder actions without changing effective policy")
			_, err = store.Resolve(ctx, actor.OrgID, actor)
			require.NoError(t, err, "policy and snapshot compare action selections as sets")
			_, err = pool.Exec(ctx, `UPDATE automation_runs SET resolved_head_sha=$1`, strings.Repeat("c", 40))
			require.NoError(t, err, "simulate a new target head")
			claimed, err := store.Claim(ctx, actor.OrgID, actor, action.ID)
			if tt.bound {
				require.ErrorIs(t, err, ErrAutomationActionConflict, "database claim enforces explicit frozen preconditions")
			} else {
				require.NoError(t, err, "order-only edits and unrelated pushes preserve pending work")
				require.Equal(t, models.AutomationActionSending, claimed.Status, "unbound step can send")
			}
		})
	}
}

func TestAutomationActionPostgresTargetLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, lifecycle, event string
		allowed                bool
	}{
		{"open update", "open", "github.pull_request.updated", true},
		{"subscribed final merge turn", "merged", "github.pull_request.merged", true},
		{"old turn after merge", "merged", "github.pull_request.updated", false},
		{"missing event after merge", "merged", "", false},
		{"closed target", "closed", "github.pull_request.merged", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, actor := actionPostgres(t, true)
			ctx, store := context.Background(), NewAutomationActionStore(pool)
			_, err := pool.Exec(ctx, `UPDATE automation_targets SET lifecycle_state=$1`, tt.lifecycle)
			require.NoError(t, err, "set target lifecycle")
			_, err = pool.Exec(ctx, `UPDATE automation_runs SET config_snapshot=jsonb_build_object('github_event',$1::text)`, tt.event)
			require.NoError(t, err, "record the executing run's immutable trigger event")
			_, err = store.Resolve(ctx, actor.OrgID, actor)
			if !tt.allowed {
				require.ErrorIs(t, err, ErrAutomationActionUnauthorized, "only an admitted final merge turn may write after merge")
				return
			}
			require.NoError(t, err, "admitted turn may execute unbound provider work")
			action := reserveAction(t, store, actor, "notify", models.AutomationActionSlack)
			claimed, err := store.Claim(ctx, actor.OrgID, actor, action.ID)
			require.NoError(t, err, "reserve and claim retain the same lifecycle contract")
			require.Equal(t, models.AutomationActionSending, claimed.Status, "independent notification is callable")
		})
	}
}
