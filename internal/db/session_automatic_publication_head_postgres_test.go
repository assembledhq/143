package db

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type automaticPublicationHeadFixture struct {
	session                 models.Session
	primaryID, repositoryID uuid.UUID
	result                  *models.SessionResult
	oldHead, capturedHead   string
}

func newAutomaticPublicationHeadFixture(t *testing.T, pool *pgxpool.Pool) automaticPublicationHeadFixture {
	t.Helper()
	ctx := context.Background()
	f := automaticPublicationHeadFixture{
		session: seedAutomaticPublicationSession(t, pool), repositoryID: uuid.New(),
		oldHead:      "1111111111111111111111111111111111111111",
		capturedHead: "2222222222222222222222222222222222222222",
	}
	integrationID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integrationID, f.session.OrgID)
	require.NoError(t, err, "create an isolated repository integration")
	_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id)
	 VALUES($1,$2,$3,$4,'example/publication','https://example.test/publication.git',1)`, f.repositoryID, f.session.OrgID, integrationID, int64(binary.BigEndian.Uint64(f.repositoryID[:8])>>1))
	require.NoError(t, err, "create the owned synthetic publication repository")
	_, err = pool.Exec(ctx, `UPDATE sessions SET repository_id=$3,working_branch='agent/synthetic-change',target_branch='main'
	 WHERE org_id=$1 AND id=$2`, f.session.OrgID, f.session.ID, f.repositoryID)
	require.NoError(t, err, "persist repository and working branch before final capture")
	err = pool.QueryRow(ctx, `UPDATE session_changesets SET status='external_update_detected',head_sha=$3,expected_remote_head_sha=$4
	 WHERE org_id=$1 AND session_id=$2 AND is_primary RETURNING id`, f.session.OrgID, f.session.ID, f.oldHead, f.capturedHead).Scan(&f.primaryID)
	require.NoError(t, err, "model imported remote head before the final local head capture")
	diff := "diff --git a/example.go b/example.go\n--- a/example.go\n+++ b/example.go\n@@ -1 +1 @@\n-before\n+after\n"
	base := "0000000000000000000000000000000000000000"
	collected := time.Now().UTC()
	f.result = &models.SessionResult{Diff: &diff, DiffBaseCommitSHA: &base, DiffHeadCommitSHA: &f.capturedHead, DiffCollectedAt: &collected}
	return f
}

type automaticPublicationHeadState struct {
	Status       models.ChangesetStatus
	Head, Remote *string
}

func readAutomaticPublicationHeadState(t *testing.T, pool *pgxpool.Pool, f automaticPublicationHeadFixture, id uuid.UUID) automaticPublicationHeadState {
	t.Helper()
	var result automaticPublicationHeadState
	err := pool.QueryRow(context.Background(), `SELECT status,head_sha,expected_remote_head_sha FROM session_changesets
	 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.session.OrgID, f.session.ID, id).Scan(&result.Status, &result.Head, &result.Remote)
	require.NoError(t, err, "read the exact scoped changeset status and remote-head lease")
	return result
}

func TestSessionAutomaticPublicationCapturedHeadPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name             string
		status           models.SessionStatus
		changesetStatus  models.ChangesetStatus
		policy           string
		omitHead         bool
		blankHead        bool
		omitSnapshot     bool
		oldSnapshot      bool
		emptyDiff        bool
		differentRemote  bool
		noRemote         bool
		interactive      bool
		goalImprovement  bool
		manualOrigin     bool
		noAutomation     bool
		target           bool
		child            bool
		existingPR       bool
		publicationState string
		want             models.ChangesetStatus
	}{
		{name: "final snapshot reconciles the imported primary head", want: models.ChangesetStatusPublishedBranch},
		{name: "historical default policy retains its automatic contract", policy: `{}`, want: models.ChangesetStatusPublishedBranch},
		{name: "remote mismatch remains blocked", differentRemote: true},
		{name: "unknown remote head remains blocked", noRemote: true},
		{name: "missing captured head cannot reconcile", omitHead: true},
		{name: "blank captured head cannot reconcile", blankHead: true},
		{name: "missing current snapshot cannot reconcile", omitSnapshot: true},
		{name: "a prior snapshot cannot substitute for current capture", omitSnapshot: true, oldSnapshot: true},
		{name: "empty diff cannot authorize publication", emptyDiff: true},
		{name: "failed completion cannot reconcile", status: models.SessionStatusFailed},
		{name: "interactive completion cannot reconcile", interactive: true},
		{name: "goal improvement cannot reconcile", goalImprovement: true},
		{name: "nonautomation origin cannot reconcile despite an automation link", manualOrigin: true},
		{name: "unlinked ordinary completion cannot reconcile", noAutomation: true},
		{name: "none policy cannot reconcile", policy: `{"publish_policy":"none"}`},
		{name: "invalid policy cannot reconcile", policy: `{"publish_policy":"unknown"}`},
		{name: "invalid snapshot shape cannot inherit defaults", policy: `42`},
		{name: "per-target snapshot cannot reconcile", policy: `{"publish_policy":"pull_request","session_continuity":"per_target"}`},
		{name: "per-target run cannot reconcile", target: true},
		{name: "child changeset remains blocked", child: true, want: models.ChangesetStatusPublishedBranch},
		{name: "existing primary PR retains its guard", existingPR: true},
		{name: "existing requested publication retains its guard", publicationState: "requested"},
		{name: "existing terminal publication retains its guard", publicationState: "terminal_failed"},
		{name: "merged changeset remains terminal", changesetStatus: models.ChangesetStatusMerged, want: models.ChangesetStatusMerged},
		{name: "abandoned changeset remains terminal", changesetStatus: models.ChangesetStatusAbandoned, want: models.ChangesetStatusAbandoned},
		{name: "PR state is preserved", changesetStatus: models.ChangesetStatusPROpen, want: models.ChangesetStatusPROpen},
		{name: "restack conflict is preserved", changesetStatus: models.ChangesetStatusRestackConflict, want: models.ChangesetStatusRestackConflict},
		{name: "planned state is preserved", changesetStatus: models.ChangesetStatusPlanned, want: models.ChangesetStatusPlanned},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newAutomaticPublicationHeadFixture(t, pool)
			if tt.oldSnapshot {
				require.NoError(t, NewSessionStore(pool).UpdateResult(ctx, f.session.OrgID, f.session.ID, models.SessionStatusRunning, f.result), "persist an earlier snapshot with the same head before the final result")
			}
			if tt.omitHead {
				f.result.DiffHeadCommitSHA = nil
			}
			if tt.blankHead {
				blank := "  "
				f.result.DiffHeadCommitSHA = &blank
			}
			if tt.omitSnapshot {
				f.result.DiffBaseCommitSHA = nil
			}
			if tt.emptyDiff {
				empty := ""
				f.result.Diff = &empty
			}
			if tt.interactive || tt.goalImprovement || tt.manualOrigin {
				mode, origin := models.SessionInteractionModeSingleRun, models.SessionOriginAutomation
				if tt.interactive {
					mode = models.SessionInteractionModeInteractive
				}
				if tt.goalImprovement {
					origin = models.SessionOriginAutomationGoalImprovement
				}
				if tt.manualOrigin {
					origin = models.SessionOriginManual
				}
				_, err := pool.Exec(ctx, `UPDATE sessions SET interaction_mode=$3,origin=$4 WHERE org_id=$1 AND id=$2`, f.session.OrgID, f.session.ID, mode, origin)
				require.NoError(t, err, "persist the completion authority variant")
			}
			if tt.noAutomation {
				_, err := pool.Exec(ctx, `DELETE FROM session_automation_links WHERE org_id=$1 AND session_id=$2`, f.session.OrgID, f.session.ID)
				require.NoError(t, err, "ordinary sessions have no automatic run authority")
			}
			if tt.policy != "" {
				_, err := pool.Exec(ctx, `UPDATE automation_runs SET config_snapshot=$3::jsonb WHERE org_id=$1 AND id=$2`, f.session.OrgID, *f.session.AutomationRunID, tt.policy)
				require.NoError(t, err, "persist the immutable policy variant")
			}
			if tt.target {
				var targetID uuid.UUID
				err := pool.QueryRow(ctx, `INSERT INTO automation_targets(org_id,automation_id,repository_id,target_kind,target_key)
				 SELECT org_id,automation_id,$3,'github_pull_request','1' FROM automation_runs WHERE org_id=$1 AND id=$2 RETURNING id`, f.session.OrgID, *f.session.AutomationRunID, f.repositoryID).Scan(&targetID)
				require.NoError(t, err, "create the run's owned continuity target")
				_, err = pool.Exec(ctx, `UPDATE automation_runs SET target_id=$3 WHERE org_id=$1 AND id=$2`, f.session.OrgID, *f.session.AutomationRunID, targetID)
				require.NoError(t, err, "retain target ownership independently of the captured snapshot")
			}
			if tt.existingPR {
				_, err := pool.Exec(ctx, `INSERT INTO pull_requests(org_id,session_id,changeset_id,github_pr_number,github_pr_url,github_repo,title,status,head_sha)
				 VALUES($1,$2,$3,42,'https://example.test/pull/42','example/publication','Synthetic existing PR','open',$4)`, f.session.OrgID, f.session.ID, f.primaryID, f.capturedHead)
				require.NoError(t, err, "retain preexisting primary publication ownership")
			}
			if tt.publicationState != "" {
				_, err := pool.Exec(ctx, `INSERT INTO session_publications(org_id,session_id,changeset_id,repository_id,state,source,base_branch,head_branch)
				 VALUES($1,$2,$3,$4,$5,'automation','main','agent/synthetic-change')`, f.session.OrgID, f.session.ID, f.primaryID, f.repositoryID, tt.publicationState)
				require.NoError(t, err, "retain the existing publication's state machine ownership")
			}
			before := models.ChangesetStatusExternalUpdateDetected
			if tt.changesetStatus != "" {
				before = tt.changesetStatus
			}
			remote := &f.capturedHead
			if tt.differentRemote {
				remoteHead := "3333333333333333333333333333333333333333"
				remote = &remoteHead
			}
			if tt.noRemote {
				remote = nil
			}
			_, err := pool.Exec(ctx, `UPDATE session_changesets SET status=$4,expected_remote_head_sha=$5 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.session.OrgID, f.session.ID, f.primaryID, before, remote)
			require.NoError(t, err, "persist the exact reconciliation precondition")
			var childID uuid.UUID
			if tt.child {
				err := pool.QueryRow(ctx, `INSERT INTO session_changesets(org_id,session_id,is_primary,order_index,title,status,target_branch,base_branch,head_sha,expected_remote_head_sha)
				 VALUES($1,$2,false,1,'Synthetic child','external_update_detected','main','main',$3,$3) RETURNING id`, f.session.OrgID, f.session.ID, f.capturedHead).Scan(&childID)
				require.NoError(t, err, "retain an equally matching child outside the primary completion scope")
			}
			status := tt.status
			if status == "" {
				status = models.SessionStatusCompleted
			}
			require.NoError(t, NewSessionStore(pool).UpdateResultWithAutomaticPublication(ctx, f.session.OrgID, f.session.ID, status, f.result), "final result and eligible imported-head reconciliation should commit together")
			wantStatus := tt.want
			if wantStatus == "" {
				wantStatus = models.ChangesetStatusExternalUpdateDetected
			}
			wantHead := &f.capturedHead
			if tt.omitHead || tt.blankHead {
				wantHead = &f.oldHead
			}
			require.Equal(t, automaticPublicationHeadState{Status: wantStatus, Head: wantHead, Remote: remote}, readAutomaticPublicationHeadState(t, pool, f, f.primaryID), "only a proven unpublished automatic primary may clear its external-update status; remote lease must remain exact")
			if tt.child {
				require.Equal(t, automaticPublicationHeadState{Status: models.ChangesetStatusExternalUpdateDetected, Head: &f.capturedHead, Remote: &f.capturedHead}, readAutomaticPublicationHeadState(t, pool, f, childID), "completion must not reconcile a child changeset")
			}
			if wantStatus == models.ChangesetStatusPublishedBranch {
				var snapshotHead string
				err := pool.QueryRow(ctx, `SELECT d.head_commit_sha FROM sessions s JOIN session_diff_snapshots d
				 ON d.org_id=s.org_id AND d.session_id=s.id AND d.id=s.latest_diff_snapshot_id WHERE s.org_id=$1 AND s.id=$2`, f.session.OrgID, f.session.ID).Scan(&snapshotHead)
				require.NoError(t, err, "status repair requires positive committed final snapshot evidence")
				require.Equal(t, f.capturedHead, snapshotHead, "the exact captured head must own the persisted final snapshot")
			}
		})
	}
}

func TestSessionAutomaticPublicationHeadRollbackAndReplayPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	ctx := context.Background()
	f := newAutomaticPublicationHeadFixture(t, pool)
	before := readAutomaticPublicationHeadState(t, pool, f, f.primaryID)
	_, err := pool.Exec(ctx, `ALTER TABLE jobs ADD CONSTRAINT reject_automatic_head_handoff CHECK(job_type <> 'coordinate_automatic_publication')`)
	require.NoError(t, err, "inject enqueue failure after final head reconciliation in the isolated database")
	store := NewSessionStore(pool)
	err = store.UpdateResultWithAutomaticPublication(ctx, f.session.OrgID, f.session.ID, models.SessionStatusCompleted, f.result)
	require.ErrorContains(t, err, "queue automatic publication coordination", "a failed handoff must roll back every part of the completion transaction")
	require.Equal(t, before, readAutomaticPublicationHeadState(t, pool, f, f.primaryID), "enqueue failure must restore external-update status and the preceding stored head without changing its remote lease")
	persisted, err := store.GetByID(ctx, f.session.OrgID, f.session.ID)
	require.NoError(t, err, "reload the rolled-back automation session")
	require.Equal(t, models.SessionStatusRunning, persisted.Status, "completion cannot commit ahead of the retry producer")
	require.Nil(t, persisted.LatestDiffSnapshotID, "failed admission must not retain the new snapshot pointer")
	var snapshots, jobs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM session_diff_snapshots WHERE org_id=$1 AND session_id=$2`, f.session.OrgID, f.session.ID).Scan(&snapshots), "count exact completion snapshots after rollback")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1`, f.session.OrgID).Scan(&jobs), "count tenant handoff jobs after rollback")
	require.Equal(t, 0, snapshots, "rollback must remove the final diff snapshot")
	require.Equal(t, 0, jobs, "rollback must leave no partial handoff job")
	_, err = pool.Exec(ctx, `ALTER TABLE jobs DROP CONSTRAINT reject_automatic_head_handoff`)
	require.NoError(t, err, "restore queue admission without changing the prior changeset proof")
	require.NoError(t, store.UpdateResultWithAutomaticPublication(ctx, f.session.OrgID, f.session.ID, models.SessionStatusCompleted, f.result), "replay after queue recovery should reconcile the exact final capture once")
	require.Equal(t, automaticPublicationHeadState{Status: models.ChangesetStatusPublishedBranch, Head: &f.capturedHead, Remote: &f.capturedHead}, readAutomaticPublicationHeadState(t, pool, f, f.primaryID), "successful replay must retain the original remote lease while resolving the captured head")
	var payload json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND job_type='coordinate_automatic_publication'`, f.session.OrgID).Scan(&payload), "successful replay must publish its durable handoff in the same commit")
	var actual map[string]string
	require.NoError(t, json.Unmarshal(payload, &actual), "decode the successful replay's tenant scope")
	require.Equal(t, map[string]string{"org_id": f.session.OrgID.String(), "session_id": f.session.ID.String()}, actual, "replay must coordinate only the same owned automation session")
}
