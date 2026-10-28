package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type retainedRestartFixture struct {
	org         uuid.UUID
	session     models.Session
	changeset   uuid.UUID
	loop        *models.SessionReviewLoop
	publication models.SessionPublication
	stores      *Stores
	service     *reviewloopsvc.Service
	runtime     *retainedWakeRuntime
}

func newRetainedRestartFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) retainedRestartFixture {
	t.Helper()
	org, integration, repository := uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic retained restart')`, org)
	require.NoError(t, err, "seed an isolated synthetic tenant for actual review restart")
	sessions, threads, jobs := db.NewSessionStore(pool), db.NewSessionThreadStore(pool), db.NewJobStore(pool)
	session := models.Session{OrgID: org, AgentType: models.AgentTypeClaudeCode, Status: models.SessionStatusIdle,
		AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual,
		InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, sessions.Create(ctx, &session), "create genuine session, primary thread and changeset")
	_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key='synthetic/restart',sandbox_state='snapshotted' WHERE org_id=$1 AND id=$2`, org, session.ID)
	require.NoError(t, err, "provide the reusable snapshot for both ordinary review generations")
	session, err = sessions.GetByID(ctx, org, session.ID)
	require.NoError(t, err, "use the actual session revision in frozen review evidence")
	var changeset uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM session_changesets WHERE org_id=$1 AND session_id=$2 AND is_primary`, org, session.ID).Scan(&changeset), "select the real same-session primary review target")
	_, err = pool.Exec(ctx, `UPDATE session_changesets SET head_sha='synthetic-before-restart' WHERE org_id=$1 AND session_id=$2 AND id=$3`, org, session.ID, changeset)
	require.NoError(t, err, "seed synthetic review head without any provider calls")
	_, err = pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, org)
	require.NoError(t, err, "retain required owned integration references")
	_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,1,'synthetic/restart','https://example.invalid/restart.git',1)`, repository, org, integration)
	require.NoError(t, err, "retain required owned repository references using synthetic source values")
	payload, err := json.Marshal(openPRJobInput{OrgID: org.String(), SessionID: session.ID.String(), ChangesetID: changeset.String(), PublicationSource: string(models.SessionPublicationSourceUser)})
	require.NoError(t, err, "freeze a real original open-pr request for the restart wake")
	_, err = pool.Exec(ctx, `INSERT INTO session_publications(org_id,session_id,changeset_id,repository_id,source,base_branch,head_branch,state,review_gate_state,review_max_passes,request_payload)
 VALUES($1,$2,$3,$4,'user','main','synthetic/restart','review_pending','pending',2,$5::jsonb)`, org, session.ID, changeset, repository, payload)
	require.NoError(t, err, "create a real review-pending publication with the original request")
	_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
	require.NoError(t, err, "enable retained creation only in this disposable database")
	deps, err := threadsvc.NewSendDependencies(pool, sessions, threads, jobs)
	require.NoError(t, err, "construct the unchanged bounded thread transaction owner")
	threadService, err := threadsvc.NewService(threads, sessions, db.NewSessionMessageStore(pool), db.NewSessionLogStore(pool), jobs, deps, zerolog.Nop())
	require.NoError(t, err, "construct genuine exact-ID creation and actual stamped send")
	threadService.SetThreadInboxStore(db.NewThreadInboxStore(pool), pool)
	runtime := &retainedWakeRuntime{RuntimeAdapter: reviewloopsvc.RuntimeAdapter{Sessions: sessions, Threads: threadService}}
	loops := db.NewSessionReviewLoopStore(pool)
	service := reviewloopsvc.NewService(loops, runtime, reviewloopsvc.WithStartTransactions(deps.Transactions))
	head, revision := "synthetic-before-restart", session.WorkspaceRevision
	loop, err := service.Start(ctx, org, session.ID, reviewloopsvc.StartReviewLoopRequest{MaxPasses: 2, Source: models.ReviewLoopSourcePublication,
		ChangesetID: &changeset, WorkspaceRevision: &revision, DesiredHeadSHA: &head, ReviewRequired: true})
	require.NoError(t, err, "create the first retained generation through genuine T0/E1/T1 and actual send")
	accepted := models.ReviewLoopAdmissionAccepted
	require.Equal(t, &accepted, loop.AdmissionState, "stranded fixture must begin with positively accepted original startup")
	publications := db.NewSessionPublicationStore(pool)
	require.NoError(t, publications.LinkReviewLoop(ctx, org, session.ID, changeset, loop.ID, loop.MaxPasses, revision, head), "link the genuine accepted review to its exact publication")
	publication, err := publications.GetByChangeset(ctx, org, session.ID, changeset)
	require.NoError(t, err, "read current retained caller and loop identity before becoming stranded")
	stores := &Stores{Sessions: sessions, SessionThreads: threads, ReviewLoops: loops, SessionPublications: publications, SessionChangesets: db.NewSessionChangesetStore(pool), Jobs: jobs}
	return retainedRestartFixture{org: org, session: session, changeset: changeset, loop: loop, publication: publication, stores: stores, service: service, runtime: runtime}
}

// This represents the existing worker projection after startup work drained;
// it does not claim provider execution or a whole Worker.poll run.
func strandRetainedRestartFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f retainedRestartFixture) {
	t.Helper()
	_, err := pool.Exec(ctx, `UPDATE jobs SET status=$3,completed_at=statement_timestamp()
 WHERE org_id=$1 AND payload->>'thread_id'=$2 AND status IN ('pending','running')`, f.org, f.loop.ThreadID.String(), models.JobStatusSucceeded)
	require.NoError(t, err, "drain only the original review thread's actual queued work before stranded selection")
	_, err = pool.Exec(ctx, `UPDATE session_threads SET status='idle',last_activity_at=statement_timestamp()-interval '1 minute' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session.ID, f.loop.ThreadID)
	require.NoError(t, err, "make the original review thread genuinely idle beyond the unchanged inactivity threshold")
	_, err = pool.Exec(ctx, `UPDATE sessions SET status='idle' WHERE org_id=$1 AND id=$2`, f.org, f.session.ID)
	require.NoError(t, err, "retain an ordinary idle reusable session after the test's drained runtime projection")
}

func TestRetainedPublicationStrandedRestartPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
	f := newRetainedRestartFixture(t, ctx, pool)
	strandRetainedRestartFixture(t, ctx, pool, f)
	restarted, err := f.service.ReconcileStrandedPublicationLoops(ctx, f.org, time.Now().Add(-reviewLoopRecoveryInactiveFor), 1)
	require.NoError(t, err, "the actual stranded service must retire only the resolved old generation with its durable restart marker")
	require.Equal(t, 1, restarted, "the accepted inactive review must be genuinely eligible for exactly one restart")
	publication, err := f.stores.SessionPublications.GetByChangeset(ctx, f.org, f.session.ID, f.changeset)
	require.NoError(t, err, "read the actual reset caller rather than an invented detached snapshot")
	require.Equal(t, &f.loop.ID, publication.ReviewStartID, "restart must preserve resolved immutable history")
	require.Nil(t, publication.ReviewLoopID, "restart must clear the mutable old loop link")
	var oldEnvelope json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT envelope FROM review_loop_start_intents WHERE org_id=$1 AND id=$2`, f.org, f.loop.ID).Scan(&oldEnvelope), "capture the complete old frozen history before fresh work")
	branchCalls := 0
	pr := &stubPRService{createBranchFn: func(_ context.Context, run *models.Session, params ...ghservice.CreatePRParams) (*ghservice.CreateBranchResult, error) {
		branchCalls++
		require.Equal(t, f.session.ID, run.ID, "ordinary restart must checkpoint the same reusable session")
		require.Equal(t, &f.changeset, params[0].ChangesetID, "ordinary restart must use the same publication changeset")
		return &ghservice.CreateBranchResult{HeadSHA: "synthetic-after-restart"}, nil
	}}
	ready, err := ensurePublicationPrePRReview(ctx, f.stores, &Services{ReviewLoops: f.service, PR: pr}, zerolog.Nop(), f.session, &f.changeset, &publication)
	require.NoError(t, err, "ordinary worker path must start a fresh review instead of failing the reset publication from detached history")
	require.False(t, ready, "a newly running review cannot grant publication authority")
	require.Equal(t, 1, branchCalls, "the restarted generation must use the existing ordinary checkpoint path once")
	current, err := f.stores.SessionPublications.GetByChangeset(ctx, f.org, f.session.ID, f.changeset)
	require.NoError(t, err, "read the actual new caller generation")
	require.NotNil(t, current.ReviewStartID, "fresh T0 must install a new independently retained identity")
	require.NotEqual(t, f.loop.ID, *current.ReviewStartID, "resolved detached history must not force adoption of the failed old loop")
	require.Equal(t, current.ReviewStartID, current.ReviewLoopID, "fresh generation must bind its own exact loop, not old history")
	require.Equal(t, models.SessionPublicationStateReviewPending, current.State, "self-healing restart must remain live instead of becoming terminal_failed")
	require.Equal(t, models.SessionPublicationReviewGatePending, current.ReviewGateState, "fresh original must retain pending review gate")
	old, err := f.stores.ReviewLoops.GetLoopByID(ctx, f.org, f.loop.ID)
	require.NoError(t, err, "read preserved old lifecycle history")
	require.Equal(t, models.ReviewLoopStatusFailed, old.Status, "restart must not reopen the old retired loop")
	var afterEnvelope json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT envelope FROM review_loop_start_intents WHERE org_id=$1 AND id=$2`, f.org, f.loop.ID).Scan(&afterEnvelope), "read old immutable envelope after fresh generation")
	require.Equal(t, oldEnvelope, afterEnvelope, "fresh review must preserve every field of the old frozen history")
	var graph [4]int
	require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1),(SELECT count(*) FROM session_review_loops WHERE org_id=$1),(SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1),(SELECT count(*) FROM session_messages WHERE org_id=$1)`, f.org).Scan(&graph[0], &graph[1], &graph[2], &graph[3]), "read the complete two-generation graph")
	require.Equal(t, [4]int{2, 2, 2, 2}, graph, "ordinary restart creates one new generation and one new command while retaining the original graph")
	require.Equal(t, 2, f.runtime.sends, "each genuine generation sends its own command once; history is never resent")
}

func TestRetainedReconcileContinuesStrandedPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
	f := newRetainedRestartFixture(t, ctx, pool)
	strandRetainedRestartFixture(t, ctx, pool, f)
	bad := f.session
	bad.ID, bad.PrimaryThreadID = uuid.Nil, nil
	require.NoError(t, f.stores.Sessions.Create(ctx, &bad), "create an unrelated same-tenant session for ambiguous prepared recovery")
	_, err := pool.Exec(ctx, `UPDATE sessions SET snapshot_key='synthetic/bad-prepared',sandbox_state='snapshotted' WHERE org_id=$1 AND id=$2`, f.org, bad.ID)
	require.NoError(t, err, "make the unrelated preparation pass normal fresh-start preflight")
	f.runtime.crash, f.runtime.crashSession = true, bad.ID
	_, err = f.service.Start(ctx, f.org, bad.ID, reviewloopsvc.StartReviewLoopRequest{MaxPasses: 2})
	var pending *reviewloopsvc.StartPendingError
	require.ErrorAs(t, err, &pending, "untyped E1 storage uncertainty must retain the unrelated original prepared owner")
	_, err = pool.Exec(ctx, `UPDATE review_loop_start_intents SET due_at=statement_timestamp()-interval '1 hour' WHERE org_id=$1 AND session_id=$2 AND id=$3 AND state='prepared'`, f.org, bad.ID, pending.LoopID)
	require.NoError(t, err, "make the genuine ambiguous owner due before the repaired handler executes")
	payload, err := json.Marshal(map[string]any{"org_id": f.org.String(), "limit": 7})
	require.NoError(t, err, "encode the existing org-scoped reconciliation wake")
	err = newReconcileSessionReviewLoopHandler(&Services{ReviewLoops: f.service}, zerolog.Nop())(ctx, models.JobTypeReconcileSessionReviewLoop, payload)
	require.ErrorIs(t, err, reviewloopsvc.ErrReviewLoopStartPending, "the handler must preserve genuine retained uncertainty after independently eligible stranded work completes")
	old, err := f.stores.ReviewLoops.GetLoopByID(ctx, f.org, f.loop.ID)
	require.NoError(t, err, "read the unrelated eligible original after the same handler execution")
	require.Equal(t, models.ReviewLoopStatusFailed, old.Status, "one bad prepared owner must not starve the guarded retirement of an unrelated stranded publication")
	current, err := f.stores.SessionPublications.GetByChangeset(ctx, f.org, f.session.ID, f.changeset)
	require.NoError(t, err, "read the actual restarted publication after retained error")
	require.Nil(t, current.ReviewLoopID, "independent stranded reconciliation must clear the old loop link")
	require.Equal(t, &f.loop.ID, current.ReviewStartID, "stranded restart retains resolved history while its next ordinary generation is pending")
	require.Equal(t, models.SessionPublicationStateReviewPending, current.State, "unrelated publication remains eligible for ordinary restart")
	var badState string
	var badLoops, badMessages int
	require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, bad.ID, pending.LoopID).Scan(&badState), "read the exact unresolved ambiguous owner after joined error")
	require.Equal(t, "prepared", badState, "independent progress must not invent refusal or abandonment of uncertainty")
	require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_review_loops WHERE org_id=$1 AND session_id=$2),(SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2)`, f.org, bad.ID).Scan(&badLoops, &badMessages), "read all possible command effects for the bad prepared owner")
	require.Equal(t, [2]int{0, 0}, [2]int{badLoops, badMessages}, "the still-uncertain original must not expose a loop or send while unrelated recovery progresses")
	var requeues int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='open_pr' AND status='pending' AND payload->>'changeset_id'=$2`, f.org, f.changeset.String()).Scan(&requeues), "read the actual original publication wake created by stranded restart")
	require.Equal(t, 1, requeues, "unrelated eligible stranded publication must be durably re-enqueued despite the returned retained error")
	require.Equal(t, 1, f.runtime.sends, "the handler must not resend the old accepted command or send from ambiguous preparation")
}
