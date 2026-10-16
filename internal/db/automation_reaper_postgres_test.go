package db

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const legacyReaperAbandonedSummary = "run has no recoverable job or executor; marked failed by reaper"

type automationReaperFixture struct {
	pool         *pgxpool.Pool
	orgID, runID uuid.UUID
	completedAt  time.Time
}

func newAutomationReaperFixture(t *testing.T, pool *pgxpool.Pool) automationReaperFixture {
	t.Helper()
	f := automationReaperFixture{pool: pool, orgID: uuid.New(), runID: uuid.New(), completedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	automationID := uuid.New()
	f.exec(t, `INSERT INTO organizations(id,name) VALUES($1,'Reaper test')`, f.orgID)
	f.exec(t, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Test','Test the reaper')`, automationID, f.orgID)
	f.exec(t, `INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,status,triggered_at) VALUES($1,$2,$3,'Test','running',now()-interval '2 hours')`, f.runID, f.orgID, automationID)
	return f
}

func (f automationReaperFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err, "prepare automation recovery state")
}

func (f automationReaperFixture) session(t *testing.T, status string, diff, summary, failure *string) uuid.UUID {
	t.Helper()
	session := &models.Session{OrgID: f.orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatus(status), AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginAutomation, InteractionMode: models.SessionInteractionModeSingleRun, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd, AutomationRunID: &f.runID}
	require.NoError(t, NewSessionStore(f.pool).Create(context.Background(), session), "create a linked automation session")
	f.exec(t, `UPDATE sessions SET diff=$3,result_summary=$4,error=$5,completed_at=$6 WHERE org_id=$1 AND id=$2`, f.orgID, session.ID, diff, summary, failure, f.completedAt)
	return session.ID
}

func (f automationReaperFixture) job(t *testing.T, orgID uuid.UUID, jobType, status, payloadKey, payloadValue string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	payload, err := json.Marshal(map[string]string{payloadKey: payloadValue})
	require.NoError(t, err, "encode the linked job payload")
	f.exec(t, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,lease_expires_at) VALUES($1,$2,'agent',$3,$4,$5,now()-interval '1 hour')`, id, orgID, jobType, payload, status)
	return id
}

func TestAutomationRunReaperPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	text := func(s string) *string { return &s }
	type testCase struct {
		name            string
		setup           func(*testing.T, automationReaperFixture)
		wantStatus      string
		wantSummary     *string
		wantReaped      int64
		terminalSession bool
	}
	tests := []testCase{
		{name: "orphaned running run", wantStatus: "failed", wantSummary: text(legacyReaperAbandonedSummary), wantReaped: 1},
		{name: "orphaned pending run", setup: func(t *testing.T, f automationReaperFixture) {
			f.exec(t, `UPDATE automation_runs SET status='pending' WHERE org_id=$1 AND id=$2`, f.orgID, f.runID)
		}, wantStatus: "failed", wantSummary: text(legacyReaperAbandonedSummary), wantReaped: 1},
		{name: "fresh run", setup: func(t *testing.T, f automationReaperFixture) {
			f.exec(t, `UPDATE automation_runs SET triggered_at=now() WHERE org_id=$1 AND id=$2`, f.orgID, f.runID)
		}, wantStatus: "running"},
		{name: "per-target waiting run", setup: func(t *testing.T, f automationReaperFixture) {
			f.exec(t, `UPDATE automation_runs SET dispatch_state='waiting' WHERE org_id=$1 AND id=$2`, f.orgID, f.runID)
		}, wantStatus: "running"},
		{name: "per-target executing run", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "running", nil, nil, nil)
			f.exec(t, `UPDATE automation_runs SET dispatch_state='executing',session_id=$3,job_id=gen_random_uuid(),execution_started_at=now()-interval '2 hours' WHERE org_id=$1 AND id=$2`, f.orgID, f.runID, id)
		}, wantStatus: "running"},
		{name: "pending session without job is abandoned", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "pending", nil, nil, nil)
		}, wantStatus: "failed", wantSummary: text(legacyReaperAbandonedSummary), wantReaped: 1},
		{name: "creation enqueue gap retains dispatcher ownership", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "pending", nil, nil, nil)
			f.job(t, f.orgID, "automation_run", "running", "automation_run_id", f.runID.String())
		}, wantStatus: "running"},

		{name: "terminal run is immutable", setup: func(t *testing.T, f automationReaperFixture) {
			f.exec(t, `UPDATE automation_runs SET status='completed',result_summary='already settled',completed_at=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.runID, f.completedAt)
		}, wantStatus: "completed", wantSummary: text("already settled"), terminalSession: true},
		{name: "queued automation job", setup: func(t *testing.T, f automationReaperFixture) {
			f.job(t, f.orgID, "automation_run", "pending", "automation_run_id", f.runID.String())
		}, wantStatus: "running"},
		{name: "running automation job with expired lease", setup: func(t *testing.T, f automationReaperFixture) {
			f.job(t, f.orgID, "automation_run", "running", "automation_run_id", f.runID.String())
		}, wantStatus: "running"},
		{name: "queued session job", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "pending", nil, nil, nil)
			f.job(t, f.orgID, "run_agent", "pending", "session_id", id.String())
		}, wantStatus: "running"},
		{name: "running session job with expired lease", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "running", nil, nil, nil)
			f.job(t, f.orgID, "run_agent", "running", "session_id", id.String())
		}, wantStatus: "running"},
		{name: "queued continuation", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "idle", nil, nil, nil)
			f.job(t, f.orgID, "continue_session", "pending", "session_id", id.String())
		}, wantStatus: "running"},
		{name: "running continuation with expired lease", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "running", nil, nil, nil)
			f.job(t, f.orgID, "continue_session", "running", "session_id", id.String())
		}, wantStatus: "running"},
		{name: "terminal job does not protect orphan", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "running", nil, nil, nil)
			f.job(t, f.orgID, "run_agent", "failed", "session_id", id.String())
		}, wantStatus: "failed", wantSummary: text(legacyReaperAbandonedSummary), wantReaped: 1},
		{name: "malformed job ids do not abort recovery", setup: func(t *testing.T, f automationReaperFixture) {
			f.job(t, f.orgID, "automation_run", "pending", "automation_run_id", "malformed")
			f.job(t, f.orgID, "run_agent", "pending", "session_id", "malformed")
		}, wantStatus: "failed", wantSummary: text(legacyReaperAbandonedSummary), wantReaped: 1},
		{name: "completed session with diff", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "completed", text("diff --git a/file b/file"), text("Implemented the change."), nil)
		}, wantStatus: "completed", wantSummary: text("Implemented the change."), wantReaped: 1, terminalSession: true},
		{name: "completed session with whitespace diff", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "completed", text(" \n\t\r"), text("Nothing to change."), nil)
		}, wantStatus: "completed_noop", wantSummary: text("Nothing to change."), wantReaped: 1, terminalSession: true},
		{name: "completed session without diff", setup: func(t *testing.T, f automationReaperFixture) { f.session(t, "completed", nil, nil, nil) }, wantStatus: "completed_noop", wantSummary: text("Agent session completed."), wantReaped: 1, terminalSession: true},
		{name: "published session is completed", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "pr_created", nil, text("Created a pull request."), nil)
		}, wantStatus: "completed", wantSummary: text("Created a pull request."), wantReaped: 1, terminalSession: true},
		{name: "skipped session remains skipped", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "skipped", nil, nil, nil)
		}, wantStatus: "skipped", wantSummary: text("Agent session was skipped."), wantReaped: 1, terminalSession: true},
		{name: "cancelled session preserves outcome", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "cancelled", nil, nil, nil)
		}, wantStatus: "failed", wantSummary: text("Agent session was cancelled."), wantReaped: 1, terminalSession: true},
		{name: "latest orphan cannot inherit earlier success", setup: func(t *testing.T, f automationReaperFixture) {
			old := f.session(t, "completed", text("diff"), text("Old success."), nil)
			f.exec(t, `UPDATE session_automation_links SET created_at=now()-interval '1 hour' WHERE org_id=$1 AND session_id=$2`, f.orgID, old)
			f.session(t, "running", nil, nil, nil)
		}, wantStatus: "failed", wantSummary: text(legacyReaperAbandonedSummary), wantReaped: 1},
		{name: "latest terminal session wins", setup: func(t *testing.T, f automationReaperFixture) {
			old := f.session(t, "failed", nil, text("Old failure."), nil)
			f.exec(t, `UPDATE session_automation_links SET created_at=now()-interval '1 hour' WHERE org_id=$1 AND session_id=$2`, f.orgID, old)
			f.session(t, "completed", text("diff"), text("Retry succeeded."), nil)
		}, wantStatus: "completed", wantSummary: text("Retry succeeded."), wantReaped: 1, terminalSession: true},
		{name: "failed session explanation fallback", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "failed", nil, nil, nil)
			f.exec(t, `UPDATE sessions SET failure_explanation='The dependency server was unavailable.' WHERE org_id=$1 AND id=$2`, f.orgID, id)
		}, wantStatus: "failed", wantSummary: text("The dependency server was unavailable."), wantReaped: 1, terminalSession: true},

		{name: "success cannot inherit stale failure diagnostic", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "completed", text("diff"), nil, text("Old failure"))
		}, wantStatus: "completed", wantSummary: text("Agent session completed."), wantReaped: 1, terminalSession: true},
		{name: "running job without lease is still recoverable", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "running", nil, nil, nil)
			job := f.job(t, f.orgID, "run_agent", "running", "session_id", id.String())
			f.exec(t, `UPDATE jobs SET lease_expires_at=NULL WHERE org_id=$1 AND id=$2`, f.orgID, job)
		}, wantStatus: "running"},

		{name: "failed session diagnostic", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "failed", nil, text("Dependency installation failed."), text("exit 1"))
		}, wantStatus: "failed", wantSummary: text("Dependency installation failed."), wantReaped: 1, terminalSession: true},
		{name: "failed session error fallback", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "failed", nil, nil, text("The command exited with code 1."))
		}, wantStatus: "failed", wantSummary: text("The command exited with code 1."), wantReaped: 1, terminalSession: true},
		{name: "session needing guidance preserves diagnostic", setup: func(t *testing.T, f automationReaperFixture) {
			f.session(t, "needs_human_guidance", nil, text("A repository setting needs attention."), nil)
		}, wantStatus: "failed", wantSummary: text("A repository setting needs attention."), wantReaped: 1, terminalSession: true},
		{name: "any linked session work protects run", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "running", nil, nil, nil)
			f.job(t, f.orgID, "continue_session", "pending", "session_id", id.String())
			f.session(t, "failed", nil, text("Old attempt failed."), nil)
		}, wantStatus: "running"},
		{name: "other org automation job is ignored", setup: func(t *testing.T, f automationReaperFixture) {
			other := newAutomationReaperFixture(t, f.pool)
			f.job(t, other.orgID, "automation_run", "running", "automation_run_id", f.runID.String())
		}, wantStatus: "failed", wantSummary: text(legacyReaperAbandonedSummary), wantReaped: 1},
		{name: "other org session job is ignored", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "running", nil, nil, nil)
			other := newAutomationReaperFixture(t, f.pool)
			f.job(t, other.orgID, "run_agent", "running", "session_id", id.String())
		}, wantStatus: "failed", wantSummary: text(legacyReaperAbandonedSummary), wantReaped: 1},
		{name: "other org executor is ignored", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "running", nil, nil, nil)
			other := newAutomationReaperFixture(t, f.pool)
			job := f.job(t, other.orgID, "run_agent", "failed", "session_id", id.String())
			host := "test-" + uuid.NewString()
			f.exec(t, `INSERT INTO nodes(id) VALUES($1)`, host)
			f.exec(t, `INSERT INTO session_executors(org_id,session_id,job_id,job_type,host_node_id,owner_id,lock_token,status) VALUES($1,$2,$3,'run_agent',$4,'test',gen_random_uuid(),'running')`, other.orgID, id, job, host)
		}, wantStatus: "failed", wantSummary: text(legacyReaperAbandonedSummary), wantReaped: 1},

		{name: "other org session link is ignored", setup: func(t *testing.T, f automationReaperFixture) {
			other := newAutomationReaperFixture(t, f.pool)
			id := other.session(t, "completed", text("diff"), text("Other tenant result"), nil)
			f.exec(t, `UPDATE session_automation_links SET org_id=$1,automation_run_id=$2 WHERE session_id=$3`, f.orgID, f.runID, id)
		}, wantStatus: "failed", wantSummary: text(legacyReaperAbandonedSummary), wantReaped: 1},
	}
	for _, executorStatus := range []string{"starting", "running", "draining"} {
		tests = append(tests, testCase{name: "active " + executorStatus + " executor with expired lease", setup: func(t *testing.T, f automationReaperFixture) {
			id := f.session(t, "running", nil, nil, nil)
			job := f.job(t, f.orgID, "run_agent", "failed", "session_id", id.String())
			host := "test-" + uuid.NewString()
			f.exec(t, `INSERT INTO nodes(id) VALUES($1)`, host)
			f.exec(t, `INSERT INTO session_executors(org_id,session_id,job_id,job_type,host_node_id,owner_id,lock_token,status,lease_expires_at) VALUES($1,$2,$3,'run_agent',$4,'test',gen_random_uuid(),$5,now()-interval '1 hour')`, f.orgID, id, job, host, executorStatus)
		}, wantStatus: "running"})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAutomationReaperFixture(t, pool)
			if tt.setup != nil {
				tt.setup(t, f)
			}
			store := NewAutomationRunStore(pool)
			orgs, err := store.ListOrgsWithStuckRuns(context.Background(), time.Hour)
			require.NoError(t, err, "discover only recoverable automation tenants")
			require.Equal(t, tt.wantReaped > 0, slices.Contains(orgs, f.orgID), "discovery and mutation must use the same recovery eligibility")
			n, err := store.ReapStuckRuns(context.Background(), f.orgID, time.Hour)
			require.NoError(t, err, "recover the eligible legacy run")
			require.Equal(t, tt.wantReaped, n, "recovery must preserve every run with viable work")
			var actual struct {
				Status      string
				Summary     *string
				CompletedAt *time.Time
			}
			require.NoError(t, pool.QueryRow(context.Background(), `SELECT status,result_summary,completed_at FROM automation_runs WHERE org_id=$1 AND id=$2`, f.orgID, f.runID).Scan(&actual.Status, &actual.Summary, &actual.CompletedAt), "read complete recovery result")
			if actual.CompletedAt != nil {
				utc := actual.CompletedAt.UTC()
				actual.CompletedAt = &utc
			}
			var completedAt *time.Time
			if tt.terminalSession {
				completedAt = &f.completedAt
			} else if tt.wantReaped > 0 {
				completedAt = actual.CompletedAt
				require.NotNil(t, completedAt, "orphan failures must record completion")
			}
			expected := struct {
				Status      string
				Summary     *string
				CompletedAt *time.Time
			}{tt.wantStatus, tt.wantSummary, completedAt}
			require.Equal(t, expected, actual, "recovery must preserve the linked session outcome and completion metadata")
			again, err := store.ReapStuckRuns(context.Background(), f.orgID, time.Hour)
			require.NoError(t, err, "repeat recovery safely")
			require.Equal(t, int64(0), again, "recovery must be idempotent")
		})
	}
}

func TestAutomationRunReaperPostgresConcurrentWriter(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name, status, dispatch, summary string
		completed                       bool
	}{
		{name: "completion wins row lock", status: "completed", summary: "Completed concurrently.", completed: true},
		{name: "target dispatch wins row lock", status: "running", dispatch: "waiting", summary: "Dispatch changed."},
		{name: "newer metadata waits for another sweep", status: "running", summary: "Metadata changed."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAutomationReaperFixture(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			writer, err := pool.Begin(ctx)
			require.NoError(t, err, "begin concurrent run writer")
			defer func() { _ = writer.Rollback(context.Background()) }()
			var backendPID int
			require.NoError(t, writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID), "identify lock owner")
			var dispatch *string
			if tt.dispatch != "" {
				dispatch = &tt.dispatch
			}
			var completedAt *time.Time
			if tt.completed {
				completedAt = &f.completedAt
			}
			_, err = writer.Exec(ctx, `UPDATE automation_runs SET status=$3,dispatch_state=$4,result_summary=$5,completed_at=$6,updated_at=updated_at+interval '1 second' WHERE org_id=$1 AND id=$2`, f.orgID, f.runID, tt.status, dispatch, tt.summary, completedAt)
			require.NoError(t, err, "hold the run lock with a newer authoritative update")
			type result struct {
				n   int64
				err error
			}
			finished := make(chan result, 1)
			go func() {
				n, err := NewAutomationRunStore(pool).ReapStuckRuns(ctx, f.orgID, time.Hour)
				finished <- result{n, err}
			}()
			require.Eventually(t, func() bool {
				var blocked bool
				err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND $1=ANY(pg_blocking_pids(pid)))`, backendPID).Scan(&blocked)
				return err == nil && blocked
			}, 5*time.Second, 10*time.Millisecond, "reaper must observe the prior row and wait on the concurrent writer")
			require.NoError(t, writer.Commit(ctx), "publish the concurrent transition")
			got := <-finished
			require.NoError(t, got.err, "reaper must safely recheck the updated row")
			require.Equal(t, int64(0), got.n, "reaper must not overwrite the concurrent writer")
			var actual struct {
				Status            string
				Dispatch, Summary *string
				CompletedAt       *time.Time
			}
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,dispatch_state,result_summary,completed_at FROM automation_runs WHERE org_id=$1 AND id=$2`, f.orgID, f.runID).Scan(&actual.Status, &actual.Dispatch, &actual.Summary, &actual.CompletedAt), "read the result after the concurrent transition")
			if actual.CompletedAt != nil {
				utc := actual.CompletedAt.UTC()
				actual.CompletedAt = &utc
			}
			expected := struct {
				Status            string
				Dispatch, Summary *string
				CompletedAt       *time.Time
			}{tt.status, dispatch, &tt.summary, completedAt}
			require.Equal(t, expected, actual, "the concurrent transition must retain all of its metadata")
		})
	}
}

func TestAutomationRunReaperPostgresTenantScope(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	own, foreign := newAutomationReaperFixture(t, pool), newAutomationReaperFixture(t, pool)
	n, err := NewAutomationRunStore(pool).ReapStuckRuns(context.Background(), own.orgID, time.Hour)
	require.NoError(t, err, "recover one tenant's abandoned run")
	require.Equal(t, int64(1), n, "only one tenant's orphan may be settled")
	var actual string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT status FROM automation_runs WHERE org_id=$1 AND id=$2`, foreign.orgID, foreign.runID).Scan(&actual), "read the other tenant's run")
	require.Equal(t, "running", actual, "a tenant-scoped recovery must not mutate another tenant's orphan")
}
