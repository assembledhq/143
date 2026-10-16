package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func seedAutomaticPublicationSession(t *testing.T, pool *pgxpool.Pool) models.Session {
	t.Helper()
	ctx := context.Background()
	orgID, automationID, runID := uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,$2)`, orgID, "publication-"+orgID.String())
	require.NoError(t, err, "create an isolated automatic publication tenant")
	_, err = pool.Exec(ctx, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic publication','Prepare a change')`, automationID, orgID)
	require.NoError(t, err, "create the synthetic automation")
	_, err = pool.Exec(ctx, `INSERT INTO automation_runs(id,automation_id,org_id,goal_snapshot,config_snapshot) VALUES($1,$2,$3,'Prepare a change','{"publish_policy":"pull_request"}')`, runID, automationID, orgID)
	require.NoError(t, err, "create the immutable run policy snapshot")
	session := models.Session{
		OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning,
		AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow,
		Origin: models.SessionOriginAutomation, InteractionMode: models.SessionInteractionModeSingleRun,
		ValidationPolicy: models.SessionValidationPolicyOnSessionEnd, AutomationRunID: &runID,
	}
	require.NoError(t, NewSessionStore(pool).Create(ctx, &session), "create the automation session and its scoped link")
	return session
}

func TestSessionAutomaticPublicationResultPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name     string
		change   func(*models.Session, *models.SessionResult)
		status   models.SessionStatus
		foreign  bool
		wantJobs int
	}{
		{name: "completion and retry admission commit together", status: models.SessionStatusCompleted, wantJobs: 1},
		{name: "blank diff has no automatic handoff", status: models.SessionStatusCompleted, change: func(_ *models.Session, result *models.SessionResult) { diff := "  "; result.Diff = &diff }},
		{name: "failed turn has no automatic handoff", status: models.SessionStatusFailed},
		{name: "interactive automation has no automatic handoff", status: models.SessionStatusCompleted, change: func(session *models.Session, _ *models.SessionResult) {
			session.InteractionMode = models.SessionInteractionModeInteractive
		}},
		{name: "goal improvement has no automatic handoff", status: models.SessionStatusCompleted, change: func(session *models.Session, _ *models.SessionResult) {
			session.Origin = models.SessionOriginAutomationGoalImprovement
		}},
		{name: "another tenant cannot complete or enqueue", status: models.SessionStatusCompleted, foreign: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			session := seedAutomaticPublicationSession(t, pool)
			diff, summary := "diff --git a/example.go b/example.go\n+changed\n", "Prepared synthetic change"
			result := &models.SessionResult{Diff: &diff, ResultSummary: &summary}
			if tt.change != nil {
				tt.change(&session, result)
				_, err := pool.Exec(ctx, `UPDATE sessions SET origin=$3,interaction_mode=$4 WHERE org_id=$1 AND id=$2`, session.OrgID, session.ID, session.Origin, session.InteractionMode)
				require.NoError(t, err, "persist the eligibility variant")
			}
			orgID := session.OrgID
			if tt.foreign {
				orgID = uuid.New()
			}
			store := NewSessionStore(pool)
			err := store.UpdateResultWithAutomaticPublication(ctx, orgID, session.ID, tt.status, result)
			if tt.foreign {
				require.Error(t, err, "cross-tenant result update must fail before enqueue")
			} else {
				require.NoError(t, err, "result and eligible coordination should commit atomically")
				require.NoError(t, store.UpdateResultWithAutomaticPublication(ctx, orgID, session.ID, tt.status, result), "replayed completion must reuse the active handoff")
			}
			var count int
			err = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='coordinate_automatic_publication'`, session.OrgID).Scan(&count)
			require.NoError(t, err, "inspect the scoped durable handoff count")
			require.Equal(t, tt.wantJobs, count, "repeated completion must enqueue at most one eligible coordination job")
			persisted, err := store.GetByID(ctx, session.OrgID, session.ID)
			require.NoError(t, err, "reload the authoritative result")
			if tt.foreign {
				require.Equal(t, models.SessionStatusRunning, persisted.Status, "another tenant must not change the original turn")
			} else {
				require.Equal(t, tt.status, persisted.Status, "the same commit must persist the terminal result")
				require.Equal(t, result.ResultSummary, persisted.ResultSummary, "the completion summary must be retained")
			}
			if tt.wantJobs != 0 {
				var payload json.RawMessage
				var queue, dedupeKey string
				var attempts int
				err = pool.QueryRow(ctx, `SELECT payload,queue,dedupe_key,max_attempts FROM jobs WHERE org_id=$1 AND job_type='coordinate_automatic_publication'`, session.OrgID).Scan(&payload, &queue, &dedupeKey, &attempts)
				require.NoError(t, err, "read durable publication coordination options")
				var got map[string]string
				require.NoError(t, json.Unmarshal(payload, &got), "decode durable tenant scope")
				require.Equal(t, map[string]string{"org_id": session.OrgID.String(), "session_id": session.ID.String()}, got, "retry must carry only server-owned tenant and session identity")
				require.Equal(t, "default", queue, "coordination must be independent of a sandbox worker")
				require.Equal(t, "coordinate_automatic_publication:"+session.ID.String(), dedupeKey, "coordination dedupe must be session scoped")
				require.Equal(t, 5, attempts, "external admission failures must have a bounded ordinary retry budget")
			}
		})
	}
}

func TestSessionAutomaticPublicationEnqueueFailureRollsBackResultPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	ctx := context.Background()
	session := seedAutomaticPublicationSession(t, pool)
	_, err := pool.Exec(ctx, `ALTER TABLE jobs ADD CONSTRAINT reject_automatic_coordination CHECK(job_type <> 'coordinate_automatic_publication')`)
	require.NoError(t, err, "inject a deterministic queue admission failure in an isolated database")
	diff, base := "diff --git a/example.go b/example.go\n+changed\n", "0123456789abcdef"
	collected := time.Now().UTC()
	err = NewSessionStore(pool).UpdateResultWithAutomaticPublication(ctx, session.OrgID, session.ID, models.SessionStatusCompleted, &models.SessionResult{Diff: &diff, DiffBaseCommitSHA: &base, DiffCollectedAt: &collected})
	require.ErrorContains(t, err, "queue automatic publication coordination", "queue failure must propagate to the completion caller")
	persisted, err := NewSessionStore(pool).GetByID(ctx, session.OrgID, session.ID)
	require.NoError(t, err, "reload the rolled-back session result")
	require.Equal(t, models.SessionStatusRunning, persisted.Status, "terminal completion must not commit without its retry job")
	require.Nil(t, persisted.Diff, "failed handoff admission must roll back the diff")
	require.Nil(t, persisted.LatestDiffSnapshotID, "failed handoff admission must roll back the diff snapshot pointer")
	var snapshots, jobs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM session_diff_snapshots WHERE org_id=$1 AND session_id=$2`, session.OrgID, session.ID).Scan(&snapshots), "count snapshots after rollback")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1`, session.OrgID).Scan(&jobs), "count handoff jobs after rollback")
	require.Equal(t, 0, snapshots, "the result transaction must not leak a snapshot")
	require.Equal(t, 0, jobs, "the failed handoff must not leave a partial job")
}
