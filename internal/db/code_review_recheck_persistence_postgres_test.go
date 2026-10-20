package db

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestSessionTurnAndRecheckPersistenceFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newSessionPersistencePostgresPool(t)
	installRecheckPersistenceLateFailure(t, pool)
	for _, tt := range []struct {
		variant string
		commit  bool
	}{
		{variant: "head only", commit: true}, {variant: "head only"},
		{variant: "empty diff", commit: true}, {variant: "empty diff"},
	} {
		name := "standalone/" + tt.variant + "/rollback"
		if tt.commit {
			name = "standalone/" + tt.variant + "/commit"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fixture := seedSessionPersistencePostgres(t, ctx, pool)
			if tt.variant == "head only" {
				fixture.result.Diff, fixture.result.DiffBaseCommitSHA = nil, nil
			} else {
				fixture.result.Diff = stringPtr("")
			}
			before := sessionPersistencePostgresRows(t, ctx, pool, fixture)
			configuration := pool.Config()
			configuration.MaxConns, configuration.MinConns = 1, 0
			callerPool, err := pgxpool.NewWithConfig(ctx, configuration)
			require.True(t, err == nil, "open only this generated fixture's one-connection pool without publishing its DSN")
			t.Cleanup(callerPool.Close)
			store, redis := sessionPersistenceStoreWithStreams(t, callerPool)
			owner, err := callerPool.Begin(ctx)
			require.NoError(t, err, "the caller alone borrows the pool's only connection and begins its transaction")
			defer func() { _ = owner.Rollback(context.Background()) }()
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			var beforeID, afterID int64
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&beforeID), "record the caller's original PostgreSQL transaction")
			effects, err := store.UpdateTurnCompleteInTx(ctx, fixture.orgID, tx, fixture.sessionID, 2, fixture.result, "synthetic-agent", "synthetic-checkpoint")
			require.NoError(t, err, "the standalone real turn must persist both head-only and legitimate empty-diff results without another connection or savepoint")
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&afterID), "the same caller transaction must remain open after persistence")
			require.Equal(t, beforeID, afterID, "standalone persistence must not replace or settle the caller transaction")
			require.Empty(t, tx.settlements, "standalone persistence must not Begin, Commit or Rollback even transitively")
			require.Empty(t, redis.Keys(), "enabled publishers must remain silent before outer settlement")
			require.Equal(t, before, sessionPersistencePostgresRows(t, ctx, pool, fixture), "independent readers must see every original row until the owner commits")
			staged := sessionPersistencePostgresRows(t, ctx, owner, fixture)
			stagedSession, err := NewSessionStore(owner).GetByID(ctx, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "inspect the real staged session projection")
			requireSessionPersistencePostgresValues(t, ctx, owner, fixture, stagedSession, "turn")
			if !tt.commit {
				require.NoError(t, owner.Rollback(ctx), "the owner may reject the whole otherwise valid standalone persistence unit")
				require.Equal(t, before, sessionPersistencePostgresRows(t, ctx, pool, fixture), "outer rollback must remove the complete session, head, snapshot and publish-state mutations")
				require.Empty(t, redis.Keys(), "rolled-back effect data must not publish itself")
				return
			}
			require.NoError(t, owner.Commit(ctx), "only the outer owner commits the complete standalone unit")
			require.Equal(t, staged, sessionPersistencePostgresRows(t, ctx, pool, fixture), "commit must expose exactly the staged rows without an independent write")
			require.Empty(t, redis.Keys(), "confirmed commit alone must not publish captured effects")
			store.PublishCommittedSessionPersistenceEffects(ctx, fixture.orgID, effects)
			requireSessionPersistenceWorkspaceEvent(t, redis, fixture, tt.variant == "empty diff")
		})
	}
	for _, tt := range []struct{ variant, outcome string }{
		{variant: "head only", outcome: "commit"}, {variant: "empty diff", outcome: "commit"},
		{variant: "head only", outcome: "late receipt rejection"}, {variant: "empty diff", outcome: "late receipt rejection"},
		{variant: "head only", outcome: "nonempty diff rejection"}, {variant: "head only", outcome: "stale lease"},
	} {
		t.Run("recheck/"+tt.variant+"/"+tt.outcome, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			completion := seedRecheckPersistenceCompletion(t, ctx, pool, tt.variant)
			before, err := recheckPersistenceRows(ctx, pool, completion)
			require.NoError(t, err, "capture every original full-schema row in this exact completion graph")
			if tt.outcome == "late receipt rejection" {
				_, err := pool.Exec(ctx, "INSERT INTO synthetic_recheck_persistence_rejections(assessment_id,snapshot_count) VALUES($1,$2)", completion.AssessmentID, map[bool]int{false: 0, true: 1}[tt.variant == "empty diff"])
				require.NoError(t, err, "enable only this assessment's fixture-owned late receipt rejection")
			}
			configuration := pool.Config()
			configuration.MaxConns, configuration.MinConns = 1, 0
			completionPool, err := pgxpool.NewWithConfig(ctx, configuration)
			require.True(t, err == nil, "open the generated completion fixture's one-connection pool without exposing its DSN")
			t.Cleanup(completionPool.Close)
			starter := &recheckPersistenceStarter{Pool: completionPool}
			store := NewCodeReviewRecheckStore(starter)
			var staged json.RawMessage
			var captureErr error
			capture := func(ctx context.Context, tx pgx.Tx) {
				staged, captureErr = recheckPersistenceRows(ctx, tx, completion)
			}
			if tt.outcome == "late receipt rejection" {
				starter.beforeRollback = capture
			} else if tt.outcome == "commit" {
				starter.beforeCommit = capture
			}
			requested := completion
			if tt.outcome == "nonempty diff rejection" {
				requested.Result = &models.SessionResult{Diff: stringPtr("synthetic forbidden workspace change")}
			} else if tt.outcome == "stale lease" {
				requested.LockToken = uuid.New()
			}
			messageID, completionErr := store.Complete(ctx, requested)
			after, err := recheckPersistenceRows(ctx, pool, completion)
			require.NoError(t, err, "reload the complete graph after the actual Complete method settles its transaction")
			if tt.outcome == "nonempty diff rejection" {
				require.ErrorContains(t, completionErr, "changed workspace", "explicit persistence must not weaken the existing read-only review contract")
				require.Zero(t, starter.begins, "a changed worktree must be rejected before opening a transaction")
				require.Zero(t, messageID, "read-only rejection must not return an accepted assistant receipt")
				require.Equal(t, before, after, "read-only rejection must preserve every original row")
				return
			}
			require.Equal(t, 1, starter.begins, "the actual Complete method must own exactly one outer transaction")
			require.Zero(t, starter.tx.nestedBegins, "head and empty-diff persistence must not open even a savepoint in the actual Complete path")
			require.Equal(t, 1, starter.tx.rollbackCalls, "Complete must retain its one deferred outer rollback attempt")
			if tt.outcome != "commit" {
				require.ErrorIs(t, completionErr, ErrCodeReviewRecheckFence, "late receipt or job-lease rejection must preserve the existing fence error")
				require.Zero(t, messageID, "rejected completion must not return an accepted assistant message")
				require.Zero(t, starter.tx.commits, "rejected completion must not try to commit")
				require.Equal(t, 1, starter.tx.rollbacks, "the actual completion owner must successfully abort its entire rejected unit")
				require.Equal(t, before, after, "assistant, phase, root/thread result, head, snapshot, usage and receipt mutations must roll back atomically")
				if tt.outcome == "late receipt rejection" {
					require.NoError(t, captureErr, "application-level late rejection must leave its transaction usable until outer rollback")
					require.NotEqual(t, before, staged, "the late rejection proof must observe real earlier mutations rather than an early no-op fence")
					requireRecheckPersistenceState(t, staged, before, completion, false, tt.variant == "empty diff")
				}
				return
			}
			require.NoError(t, completionErr, "the actual recheck Complete path must accept legitimate head-only and empty-diff/base results")
			require.Positive(t, messageID, "successful completion must return the exact durable assistant identifier")
			require.Equal(t, 1, starter.tx.commits, "only Complete's outer owner may commit this graph")
			require.Zero(t, starter.tx.rollbacks, "the deferred rollback after commit must remain a harmless closed-transaction no-op")
			require.NoError(t, captureErr, "capture every staged row immediately before the real outer commit")
			require.Equal(t, staged, after, "the outer commit must make exactly the observed staged graph durable")
			requireRecheckPersistenceState(t, after, before, completion, true, tt.variant == "empty diff")
			var graph struct {
				Receipt models.CodeReviewRecheckDispatch `json:"receipt"`
			}
			require.NoError(t, json.Unmarshal(after, &graph), "decode the exact committed receipt")
			require.Equal(t, &messageID, graph.Receipt.ResultMessageID, "the returned message must be the exact assistant bound by the durable receipt")
		})
	}
}

type recheckPersistenceStarter struct {
	*pgxpool.Pool
	begins                       int
	tx                           *recheckPersistenceTx
	beforeCommit, beforeRollback func(context.Context, pgx.Tx)
}

func (s *recheckPersistenceStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	s.begins++
	owner, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s.tx = &recheckPersistenceTx{Tx: owner, beforeCommit: s.beforeCommit, beforeRollback: s.beforeRollback}
	return s.tx, nil
}

type recheckPersistenceTx struct {
	pgx.Tx
	nestedBegins, commits, rollbackCalls, rollbacks int
	beforeCommit, beforeRollback                    func(context.Context, pgx.Tx)
}

func (tx *recheckPersistenceTx) Begin(context.Context) (pgx.Tx, error) {
	tx.nestedBegins++
	return nil, errors.New("nested completion transaction is forbidden")
}

func (tx *recheckPersistenceTx) Commit(ctx context.Context) error {
	tx.commits++
	if tx.beforeCommit != nil {
		tx.beforeCommit(ctx, tx.Tx)
	}
	return tx.Tx.Commit(ctx)
}

func (tx *recheckPersistenceTx) Rollback(ctx context.Context) error {
	tx.rollbackCalls++
	if tx.beforeRollback != nil {
		tx.beforeRollback(ctx, tx.Tx)
	}
	err := tx.Tx.Rollback(ctx)
	if err == nil {
		tx.rollbacks++
	}
	return err
}

// This graph is seeded with exact full-schema relationships to exercise actual
// Complete. It does not establish Dispatch, policy admission or activation proof.
func seedRecheckPersistenceCompletion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, variant string) models.CodeReviewRecheckTurnCompletion {
	t.Helper()
	org, integration, repository, pr, policy, metadata, baseline, assessment, job, token := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO organizations(id,name) VALUES($1,'Synthetic recheck persistence')`, []any{org}},
		{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, org}},
		{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0)&9223372036854775807),'test/recheck-persistence','https://example.invalid/repo.git',1)`, []any{repository, org, integration}},
		{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,9,'https://example.invalid/pr/9','test/recheck-persistence','Synthetic review','head','base')`, []any{pr, org}},
		{`INSERT INTO code_review_policies(id,org_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,1,'approve_acceptable','{}','{}','{}','','','{}')`, []any{policy, org}},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed only synthetic full-schema repository, PR and policy relationships")
	}
	session := &models.Session{OrgID: org, RepositoryID: &repository, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning, Origin: models.SessionOriginCodeReview, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(pool).Create(ctx, session), "create the genuine read-only primary thread and primary changeset before installing review ownership")
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key) VALUES($1,$2,$3,$4,$5,$6,'base','head','slash_command','completed',$7)`, []any{metadata, org, session.ID, repository, pr, policy, "synthetic-metadata:" + assessment.String()}},
		{`INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,status,publication_key,completed_at,result_origin,coverage_complete,decision,acceptable,structured_outcome,rendered_body,publication_state) VALUES($1,$2,$3,'test/recheck-persistence',$4,$5,$6,$7,1,'base','main','head',1,'code','contract','intent','visual','request','gate','synthetic-baseline',jsonb_build_object('contract',jsonb_build_object('policy_id',$7::uuid::text,'policy_version',1,'policy_digest','synthetic-policy')),'full','initial_full','completed',$8,now(),'executed',true,'approved',true,'{}','Synthetic baseline','not_required')`, []any{baseline, org, repository, pr, metadata, session.ID, policy, "synthetic-baseline:" + baseline.String()}},
		{`INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,status,publication_key,source_assessment_id) SELECT $1,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,2,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,'updated-visual',request_digest,gate_digest,'synthetic-recheck',input_manifest,'evidence_only','visual_changed','running',$2,id FROM code_review_revision_assessments WHERE org_id=$3 AND id=$4`, []any{assessment, "synthetic-recheck:" + assessment.String(), org, baseline}},
		{`UPDATE sessions SET code_review_owner_pr_id=$3,token_usage='{"total_cost_usd":4}',diff='synthetic previous diff',diff_history='[]' WHERE org_id=$1 AND id=$2`, []any{org, session.ID, pr}},
		{`UPDATE session_threads SET status='running',current_turn=0 WHERE org_id=$1 AND id=$2`, []any{org, *session.PrimaryThreadID}},
		{`INSERT INTO jobs(id,org_id,queue,job_type,status,lock_token,payload) VALUES($1,$2,'agent','continue_session','running',$3,jsonb_build_object('session_id',$4::uuid::text,'thread_id',$5::uuid::text,'recheck_assessment_id',$6::uuid::text))`, []any{job, org, token, session.ID, *session.PrimaryThreadID, assessment}},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed exact full-schema review provenance, running job lease and owned conversation")
	}
	thread := *session.PrimaryThreadID
	user := &models.SessionMessage{OrgID: org, SessionID: session.ID, ThreadID: &thread, TurnNumber: 1, Role: models.MessageRoleUser, Content: "Synthetic evidence recheck", Source: models.SessionMessageSourceCodeReviewRecheck}
	require.NoError(t, NewSessionMessageStore(pool).CreateWithSource(ctx, user), "persist the exact source-tagged user message bound to the running recheck receipt")
	_, err := pool.Exec(ctx, `INSERT INTO code_review_recheck_dispatches(assessment_id,org_id,repository_id,pull_request_id,session_id,thread_id,expected_turn,payload_digest,message_id,job_id,status,attempt_lock_token,attempt_usage) VALUES($1,$2,$3,$4,$5,$6,1,'synthetic-payload',$7,$8,'running',$9,'{"synthetic-attempt":{"input_tokens":11}}')`, assessment, org, repository, pr, session.ID, thread, user.ID, job, token)
	require.NoError(t, err, "bind one real running receipt to its assessment, user message and exact job lease")
	phase, err := NewSessionActivityPhaseStore(pool).StartPhase(ctx, org, session.ID, thread, 1, nil, nil, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err, "start the actual transcript activity phase that completion must atomically close")
	result := &models.SessionResult{ResultSummary: stringPtr("Synthetic recheck result"), TokenUsage: json.RawMessage(`{"input_tokens":9}`), DiffHeadCommitSHA: stringPtr("synthetic-new-head")}
	if variant == "empty diff" {
		result.Diff, result.DiffBaseCommitSHA = stringPtr(""), stringPtr("synthetic-base")
	}
	return models.CodeReviewRecheckTurnCompletion{OrgID: org, AssessmentID: assessment, SessionID: session.ID, ThreadID: thread, JobID: job, LockToken: token, ExpectedTurn: 1, SessionTurn: 1, ActivityPhaseID: &phase.ID, Summary: "Synthetic assistant response", Result: result, ProviderSessionID: "synthetic-thread-provider", ParentAgentSessionID: "synthetic-parent-provider", SnapshotKey: "synthetic-checkpoint", NativeContext: true, TokenUsage: json.RawMessage(`{"input_tokens":7}`)}
}

func recheckPersistenceRows(ctx context.Context, database DBTX, c models.CodeReviewRecheckTurnCompletion) (json.RawMessage, error) {
	var graph json.RawMessage
	err := database.QueryRow(ctx, `SELECT jsonb_build_object(
		'session',(SELECT to_jsonb(s) FROM sessions s WHERE org_id=$1 AND id=$2),
		'thread',(SELECT to_jsonb(t) FROM session_threads t WHERE org_id=$1 AND id=$3),
		'messages',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id,created_at) FROM session_messages m WHERE org_id=$1 AND session_id=$2),
		'phase',(SELECT to_jsonb(p) FROM session_activity_phases p WHERE org_id=$1 AND id=$4),
		'publish',(SELECT jsonb_agg(to_jsonb(p) ORDER BY session_id) FROM session_publish_state p WHERE org_id=$1 AND session_id=$2),
		'changesets',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM session_changesets c WHERE org_id=$1 AND session_id=$2),
		'snapshots',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM session_diff_snapshots d WHERE org_id=$1 AND session_id=$2),
		'receipt',(SELECT to_jsonb(d) FROM code_review_recheck_dispatches d WHERE org_id=$1 AND assessment_id=$5),
		'assessment',(SELECT to_jsonb(a) FROM code_review_revision_assessments a WHERE org_id=$1 AND id=$5),
		'job',(SELECT to_jsonb(j) FROM jobs j WHERE org_id=$1 AND id=$6))`, c.OrgID, c.SessionID, c.ThreadID, c.ActivityPhaseID, c.AssessmentID, c.JobID).Scan(&graph)
	return graph, err
}

func requireRecheckPersistenceState(t *testing.T, raw, baseline json.RawMessage, c models.CodeReviewRecheckTurnCompletion, committed, snapshot bool) {
	t.Helper()
	var graph struct {
		Session    models.Session                   `json:"session"`
		Thread     models.SessionThread             `json:"thread"`
		Messages   []models.SessionMessage          `json:"messages"`
		Phase      models.SessionActivityPhase      `json:"phase"`
		Changesets []models.SessionChangeset        `json:"changesets"`
		Snapshots  []models.SessionDiffSnapshot     `json:"snapshots"`
		Receipt    models.CodeReviewRecheckDispatch `json:"receipt"`
		Assessment json.RawMessage                  `json:"assessment"`
		Job        json.RawMessage                  `json:"job"`
	}
	require.NoError(t, json.Unmarshal(raw, &graph), "decode the full graph captured on the actual completion transaction")
	var original struct {
		Messages   []models.SessionMessage `json:"messages"`
		Assessment json.RawMessage         `json:"assessment"`
		Job        json.RawMessage         `json:"job"`
	}
	require.NoError(t, json.Unmarshal(baseline, &original), "decode only baseline-owned rows for exact preservation checks")
	require.Equal(t, models.SessionStatusIdle, graph.Session.Status, "the real root result must return the parent to idle")
	require.Equal(t, 1, graph.Session.CurrentTurn, "the real root turn must advance exactly once")
	require.Equal(t, models.ThreadStatusIdle, graph.Thread.Status, "the exact recheck thread must become idle in the same transaction")
	require.Equal(t, 1, graph.Thread.CurrentTurn, "the exact thread turn must advance exactly once")
	require.JSONEq(t, `{"total_cost_usd":4}`, string(graph.Session.TokenUsage), "recheck completion must preserve the historical full review's root usage")
	require.Equal(t, &c.ParentAgentSessionID, graph.Session.AgentSessionID, "the parent must retain its own provider context, not the thread's")
	require.Equal(t, &c.ProviderSessionID, graph.Thread.AgentSessionID, "the thread must retain its exact recheck provider context")
	require.Equal(t, &c.SnapshotKey, graph.Session.SnapshotKey, "the checkpoint key must persist in the same unit as the receipt")
	require.Equal(t, c.Result.ResultSummary, graph.Session.ResultSummary, "the root result summary must not be dropped")
	require.Equal(t, c.Result.ResultSummary, graph.Thread.ResultSummary, "the thread result summary must not be dropped")
	require.Equal(t, original.Assessment, graph.Assessment, "completion persistence must not claim the assessment's separate decision/settlement authority")
	require.Equal(t, original.Job, graph.Job, "completion must preserve the worker's existing job lease and settlement responsibility")
	expectedMessages := append([]models.SessionMessage{}, original.Messages...)
	require.Len(t, graph.Messages, len(expectedMessages)+1, "exactly one new assistant message must precede receipt settlement")
	assistant := graph.Messages[len(graph.Messages)-1]
	expectedMessages = append(expectedMessages, models.SessionMessage{ID: assistant.ID, OrgID: c.OrgID, SessionID: c.SessionID, ThreadID: &c.ThreadID, TurnNumber: 1, Role: models.MessageRoleAssistant, Content: c.Summary, References: models.SessionInputReferences{}, Commands: models.SessionInputCommands{}, TokenUsage: assistant.TokenUsage, CreatedAt: assistant.CreatedAt, ActivityPhaseID: c.ActivityPhaseID})
	require.Equal(t, expectedMessages, graph.Messages, "the entire original user message and every new assistant field must remain exact")
	require.JSONEq(t, string(c.TokenUsage), string(assistant.TokenUsage), "assistant usage must come from the exact recheck invocation, not root or result usage")
	require.Equal(t, models.ActivityPhaseStatusCompleted, graph.Phase.Status, "the assistant's exact phase must complete atomically with its result")
	require.Equal(t, models.ActivityPhaseBoundaryFinalResponse, graph.Phase.BoundaryReason, "the terminal boundary must remain the final response")
	require.Equal(t, &assistant.CreatedAt, graph.Phase.CompletedAt, "phase completion must use the persisted assistant's exact timestamp")
	var heads []string
	for _, changeset := range graph.Changesets {
		if changeset.IsPrimary {
			require.NotNil(t, changeset.HeadSHA, "the primary changeset must retain the newly supplied head")
			heads = append(heads, *changeset.HeadSHA)
		}
	}
	require.Equal(t, []string{*c.Result.DiffHeadCommitSHA}, heads, "head-only and empty-diff results must retain exactly one updated primary head")
	if snapshot {
		require.Len(t, graph.Snapshots, 1, "a legitimate empty diff plus base must not be discarded as though no snapshot existed")
		require.Equal(t, "", graph.Snapshots[0].Diff, "the authoritative empty diff must persist exactly")
		require.Equal(t, *c.Result.DiffBaseCommitSHA, graph.Snapshots[0].BaseCommitSHA, "the empty snapshot must retain its explicit base")
		require.Equal(t, c.Result.DiffHeadCommitSHA, graph.Snapshots[0].HeadCommitSHA, "the empty snapshot must retain its explicit head")
		require.Equal(t, 1, graph.Snapshots[0].TurnNumber, "the empty snapshot must retain the parent turn's exact attribution")
	} else {
		require.Empty(t, graph.Snapshots, "a head-only result must update the head without fabricating a diff snapshot")
		require.Equal(t, stringPtr("synthetic previous diff"), graph.Session.Diff, "a head-only result must preserve the previous authoritative diff")
	}
	require.JSONEq(t, `{"synthetic-attempt":{"input_tokens":11}}`, string(graph.Receipt.AttemptUsage), "completion must preserve already-recorded per-launch usage")
	if committed {
		require.Equal(t, models.CodeReviewRecheckDispatchCompleted, graph.Receipt.Status, "the outer commit must make the exact receipt completed")
		require.Equal(t, &assistant.ID, graph.Receipt.ResultMessageID, "the receipt must bind the exact new assistant message")
		require.Equal(t, &c.ProviderSessionID, graph.Receipt.ProviderSessionID, "the receipt must preserve exact thread provider provenance")
		require.Equal(t, &c.SnapshotKey, graph.Receipt.SnapshotKey, "the receipt must bind its exact checkpoint key")
		require.Equal(t, &c.NativeContext, graph.Receipt.NativeContext, "the receipt must preserve native-context provenance")
	} else {
		require.Equal(t, models.CodeReviewRecheckDispatchRunning, graph.Receipt.Status, "fixture-only suppression must leave the old receipt running until outer rollback")
		require.Nil(t, graph.Receipt.ResultMessageID, "a suppressed final receipt must not gain the new assistant identifier")
	}
}

func installRecheckPersistenceLateFailure(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `CREATE TABLE synthetic_recheck_persistence_rejections(assessment_id uuid PRIMARY KEY,snapshot_count integer NOT NULL);
CREATE FUNCTION synthetic_recheck_persistence_reject() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected_snapshots integer;
BEGIN
 SELECT snapshot_count INTO expected_snapshots FROM synthetic_recheck_persistence_rejections WHERE assessment_id=NEW.assessment_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM sessions WHERE org_id=NEW.org_id AND id=NEW.session_id AND status='idle' AND current_turn=1 AND token_usage='{"total_cost_usd":4}'::jsonb)
 OR NOT EXISTS(SELECT 1 FROM session_threads WHERE org_id=NEW.org_id AND id=NEW.thread_id AND status='idle' AND current_turn=NEW.expected_turn)
 OR NOT EXISTS(SELECT 1 FROM session_messages m JOIN session_activity_phases p ON p.org_id=m.org_id AND p.id=m.activity_phase_id WHERE m.org_id=NEW.org_id AND m.id=NEW.result_message_id AND m.session_id=NEW.session_id AND m.thread_id=NEW.thread_id AND m.role='assistant' AND p.status='completed' AND p.boundary_reason='final_response' AND p.completed_at=m.created_at)
 OR NOT EXISTS(SELECT 1 FROM session_changesets WHERE org_id=NEW.org_id AND session_id=NEW.session_id AND is_primary AND head_sha='synthetic-new-head')
 OR (SELECT count(*) FROM session_diff_snapshots WHERE org_id=NEW.org_id AND session_id=NEW.session_id)<>expected_snapshots
 THEN RAISE EXCEPTION 'synthetic late receipt probe did not observe every earlier persistence effect'; END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER synthetic_recheck_persistence_reject BEFORE UPDATE OF status ON code_review_recheck_dispatches FOR EACH ROW WHEN (NEW.status='completed') EXECUTE FUNCTION synthetic_recheck_persistence_reject();`)
	require.NoError(t, err, "install only a generated-database, assessment-scoped late failure probe with nonvacuous earlier-write assertions")
}

func TestRecheckCompleteExplicitPersistenceWiring(t *testing.T) {
	t.Parallel()
	source, err := parser.ParseFile(token.NewFileSet(), "code_review_recheck_dispatch.go", nil, 0)
	require.NoError(t, err, "parse the real completion boundary, not an alternate test implementation")
	var persistence, forbidden []string
	for _, declaration := range source.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "Complete" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			name := selector.Sel.Name
			if name == "UpdateTurnCompleteInTx" {
				persistence = append(persistence, name)
			}
			if name == "UpdateTurnComplete" || name == "WithTx" || name == "SetStreams" || strings.HasPrefix(name, "Publish") {
				forbidden = append(forbidden, name)
			}
			return true
		})
	}
	require.Equal(t, []string{"UpdateTurnCompleteInTx", "UpdateTurnCompleteInTx"}, persistence, "the real completion boundary must compose both session and thread through their explicit transaction seams")
	require.Empty(t, forbidden, "temporary nil-publisher stores must not gain pool wrappers, implicit publication, reload events or new publisher wiring")
}
