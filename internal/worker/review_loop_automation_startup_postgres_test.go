package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Only the send boundary is intercepted. Preflight, exact-ID thread creation,
// retained preparation, loop creation, claim and settlement use real stores.
type automationStartupRuntime struct {
	reviewloopsvc.RuntimeAdapter
	t       *testing.T
	pool    *pgxpool.Pool
	sendErr error
	calls   int
}

func (r *automationStartupRuntime) SendMessage(_ context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	r.calls++
	require.NotNil(r.t, input.CallerCommand, "the real startup claim must supply its retained command proof")
	require.Zero(r.t, r.pool.Stat().AcquiredConns(), "the send boundary must begin after the owned claim transaction releases its checkout")
	return nil, r.sendErr
}

type automationStartupFixture struct {
	session   models.Session
	changeset uuid.UUID
	runID     uuid.UUID
	jobID     uuid.UUID
	stores    *Stores
	service   *reviewloopsvc.Service
	runtime   *automationStartupRuntime
}

func newAutomationStartupFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sendErr error) automationStartupFixture {
	t.Helper()
	org, automation, runID := uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic automation startup')`, org)
	require.NoError(t, err, "create an isolated synthetic automation tenant")
	sessions, threads, jobs := db.NewSessionStore(pool), db.NewSessionThreadStore(pool), db.NewJobStore(pool)
	session := models.Session{OrgID: org, AgentType: models.AgentTypeClaudeCode, Status: models.SessionStatusIdle,
		AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginAutomation,
		InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, sessions.Create(ctx, &session), "create the actual reusable legacy automation session and primary thread")
	_, err = pool.Exec(ctx, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic startup automation','Review before publication')`, automation, org)
	require.NoError(t, err, "retain the real owned automation parent")
	_, err = pool.Exec(ctx, `INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,session_id,triggered_by,config_snapshot,status,completed_at)
 VALUES($1,$2,$3,'Synthetic startup review',$4,'manual','{"pre_pr_review_loops":2}','completed',statement_timestamp())`, runID, org, automation, session.ID)
	require.NoError(t, err, "create the original completed automation run with fresh two-pass review policy")
	_, err = pool.Exec(ctx, `INSERT INTO session_automation_links(org_id,session_id,automation_run_id) VALUES($1,$2,$3)`, org, session.ID, runID)
	require.NoError(t, err, "persist the real session automation link used by the session model projection")
	_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key='synthetic/automation-startup',sandbox_state='snapshotted'
 WHERE org_id=$1 AND id=$2`, org, session.ID)
	require.NoError(t, err, "provide a reusable snapshot without changing the persisted automation relation")
	session, err = sessions.GetByID(ctx, org, session.ID)
	require.NoError(t, err, "use the real session's persisted automation caller identity")
	require.Equal(t, &runID, session.AutomationRunID, "the worker and retained caller must refer to the same original automation run")
	var changeset uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM session_changesets WHERE org_id=$1 AND session_id=$2 AND is_primary`, org, session.ID).Scan(&changeset), "use the genuine primary changeset created with the session")
	integration, repository := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, org)
	require.NoError(t, err, "retain the actual publication integration foreign key")
	_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id)
 VALUES($1,$2,$3,1,'synthetic/automation-startup','https://example.invalid/automation-startup.git',1)`, repository, org, integration)
	require.NoError(t, err, "retain the scoped repository foreign key using synthetic source values")
	payload, err := json.Marshal(openPRJobInput{OrgID: org.String(), SessionID: session.ID.String(), ChangesetID: changeset.String(),
		PublicationSource: string(models.SessionPublicationSourceAutomation), PublicationQueue: string(models.SessionPublicationJobQueueDefault)})
	require.NoError(t, err, "encode the original automation publication request for an actual resume enqueue")
	_, err = pool.Exec(ctx, `INSERT INTO session_publications(org_id,session_id,changeset_id,repository_id,source,base_branch,head_branch,
 state,review_gate_state,review_max_passes,request_payload,job_queue)
 VALUES($1,$2,$3,$4,'automation','main','synthetic/automation-startup','review_pending','pending',2,$5::jsonb,'default')`, org, session.ID, changeset, repository, payload)
	require.NoError(t, err, "park the actual automation publication before its first review generation")
	jobID, token := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,dedupe_key,attempts,lock_token,locked_at,lease_expires_at)
 VALUES($1,$2,'default','open_pr',$3::jsonb,$6,$4,1,$5,statement_timestamp(),statement_timestamp()+interval '1 minute')`, jobID, org, payload, db.OpenPRDedupeKey(changeset), token, models.JobStatusRunning)
	require.NoError(t, err, "retain the current running open-pr dedupe owner during synchronous startup settlement")
	_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
	require.NoError(t, err, "enable retained creation only in this disposable database")
	deps, err := threadsvc.NewSendDependencies(pool, sessions, threads, jobs)
	require.NoError(t, err, "construct the production transaction owner with its unchanged budgets")
	threadService, err := threadsvc.NewService(threads, sessions, db.NewSessionMessageStore(pool), db.NewSessionLogStore(pool), jobs, deps, zerolog.Nop())
	require.NoError(t, err, "construct the real thread preflight and exact-ID creation service")
	threadService.SetThreadInboxStore(db.NewThreadInboxStore(pool), pool)
	runtime := &automationStartupRuntime{RuntimeAdapter: reviewloopsvc.RuntimeAdapter{Sessions: sessions, Threads: threadService}, t: t, pool: pool, sendErr: sendErr}
	loops := db.NewSessionReviewLoopStore(pool)
	service := reviewloopsvc.NewService(loops, runtime, reviewloopsvc.WithStartTransactions(deps.Transactions))
	stores := &Stores{Sessions: sessions, SessionThreads: threads, Jobs: jobs, AutomationRuns: db.NewAutomationRunStore(pool),
		ReviewLoops: loops, SessionPublications: db.NewSessionPublicationStore(pool), SessionChangesets: db.NewSessionChangesetStore(pool)}
	require.NoError(t, updateChangesetPRCreationState(ctx, stores, org, session.ID, &changeset, models.PRCreationStatePushing, ""), "begin with real active primary and session PR creation projections")
	return automationStartupFixture{session: session, changeset: changeset, runID: runID, jobID: jobID, stores: stores, service: service, runtime: runtime}
}

type automationStartupReceipt struct {
	State       string
	AttemptID   uuid.UUID
	AttemptSeq  int
	WriterSeq   int
	Due         bool
	Completed   bool
	Resolution  *string
	Detail      *string
	MessageID   *int64
	PassMessage *int64
}

func TestAutomationRetainedStartupOutcomePostgres(t *testing.T) {
	t.Parallel()
	refusalResolution, refusalDetail, unknownDetail := "send_refusal", "stamped_active_unsupported", "unknown_ack"
	tests := []struct {
		name        string
		sendErr     error
		status      models.ReviewLoopStatus
		admission   models.ReviewLoopAdmissionState
		gate        models.SessionPublicationReviewGateState
		publication models.SessionPublicationState
		prState     models.PRCreationState
		retry       bool
		receipt     automationStartupReceipt
	}{
		{name: "synchronous refusal fails the current automation publication", sendErr: threadsvc.ErrCallerCommandRequiresLegacy,
			status: models.ReviewLoopStatusFailed, admission: models.ReviewLoopAdmissionRefused,
			gate: models.SessionPublicationReviewGateFailed, publication: models.SessionPublicationStateTerminalFailed, prState: models.PRCreationStateFailed,
			receipt: automationStartupReceipt{State: "refused", AttemptSeq: 1, WriterSeq: 3, Completed: true, Resolution: &refusalResolution, Detail: &refusalDetail}},
		{name: "unknown startup keeps an explicit retryable automation wait", sendErr: threadsvc.ErrCommandOutcomeUnknown,
			status: models.ReviewLoopStatusRunning, admission: models.ReviewLoopAdmissionUnknown,
			gate: models.SessionPublicationReviewGatePending, publication: models.SessionPublicationStateReviewPending, prState: models.PRCreationStatePushing, retry: true,
			receipt: automationStartupReceipt{State: "unknown", AttemptSeq: 1, WriterSeq: 2, Due: true, Detail: &unknownDetail}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
			f := newAutomationStartupFixture(t, ctx, pool, tt.sendErr)
			ready, err := ensureAutomationPrePRReview(ctx, f.stores, &Services{ReviewLoops: f.service}, zerolog.Nop(), f.session, &f.changeset)
			require.False(t, ready, "neither refused nor unknown startup may grant publication authority")
			if tt.retry {
				var retry *RetryableError
				require.ErrorAs(t, err, &retry, "a newly created retained running loop must keep the current open-pr job retryable")
				expectedDelay := prePRReviewRetryDelay
				require.Equal(t, &expectedDelay, retry.RetryAfter, "retained startup must preserve the existing bounded review wait")
			} else {
				require.NoError(t, err, "a known synchronous refusal must map to the finite automation failure in this same call")
			}
			caller, err := f.stores.AutomationRuns.GetByRunID(ctx, f.session.OrgID, f.runID)
			require.NoError(t, err, "read the real retained automation caller after fresh startup")
			require.NotNil(t, caller.ReviewStartID, "fresh automation startup must install its original retained correlation")
			loop, err := f.stores.ReviewLoops.GetLoopByID(ctx, f.session.OrgID, *caller.ReviewStartID)
			require.NoError(t, err, "read the exact loop created by actual T0, E1 and T1")
			require.Equal(t, tt.status, loop.Status, "the current loop must reflect the synchronously settled startup outcome")
			require.Equal(t, &tt.admission, loop.AdmissionState, "the worker must consume the actual retained startup projection")
			require.Equal(t, models.ReviewLoopSourceAutomation, loop.Source, "the first review must belong to the automation caller")
			require.Equal(t, &f.runID, loop.AutomationRunID, "the loop and retained caller must keep the exact original automation identity")
			require.Equal(t, 2, loop.MaxPasses, "fresh automation startup must freeze its actual policy once")
			var receipt automationStartupReceipt
			require.NoError(t, pool.QueryRow(ctx, `SELECT l.startup_send_state,l.startup_send_attempt_id,l.startup_send_attempt_seq,l.startup_send_writer_seq,
 l.startup_send_due_at IS NOT NULL,l.completed_at IS NOT NULL,l.startup_send_resolution,l.startup_send_detail,l.startup_send_message_id,p.review_message_id
 FROM session_review_loops l JOIN session_review_loop_passes p ON p.org_id=l.org_id AND p.session_id=l.session_id AND p.loop_id=l.id AND p.id=l.startup_send_pass_id
 WHERE l.org_id=$1 AND l.session_id=$2 AND l.id=$3`, f.session.OrgID, f.session.ID, loop.ID).Scan(&receipt.State, &receipt.AttemptID, &receipt.AttemptSeq,
				&receipt.WriterSeq, &receipt.Due, &receipt.Completed, &receipt.Resolution, &receipt.Detail, &receipt.MessageID, &receipt.PassMessage), "read the real claim and atomic startup settlement receipt")
			require.NotEqual(t, uuid.Nil, receipt.AttemptID, "the injected send outcome must follow a real committed startup claim")
			expectedReceipt := tt.receipt
			expectedReceipt.AttemptID = receipt.AttemptID
			require.Equal(t, expectedReceipt, receipt, "known refusal must atomically retire the loop while unknown startup remains unresolved without a message pointer")
			publication, err := f.stores.SessionPublications.GetByChangeset(ctx, f.session.OrgID, f.session.ID, f.changeset)
			require.NoError(t, err, "read the actual publication after the same fresh-start worker call")
			require.Equal(t, tt.gate, publication.ReviewGateState, "the fresh-start result must settle the caller gate immediately even while resume enqueue collides")
			require.Equal(t, tt.publication, publication.State, "a refused automation startup must finish the publication instead of leaving it pending forever")
			require.Nil(t, publication.ReviewStartID, "automation retained ownership belongs to its original run rather than a publication generation")
			require.Nil(t, publication.ReviewLoopID, "automation outcome mapping must not invent a publication loop link")
			var primaryPR, sessionPR models.PRCreationState
			require.NoError(t, pool.QueryRow(ctx, `SELECT c.pr_creation_state,p.pr_creation_state FROM session_changesets c
 JOIN session_publish_state p ON p.org_id=c.org_id AND p.session_id=c.session_id
 WHERE c.org_id=$1 AND c.session_id=$2 AND c.id=$3`, f.session.OrgID, f.session.ID, f.changeset).Scan(&primaryPR, &sessionPR), "read both actual PR creation projections")
			require.Equal(t, [2]models.PRCreationState{tt.prState, tt.prState}, [2]models.PRCreationState{primaryPR, sessionPR}, "known refusal must mark both primary and session PR creation failed in the same caller execution")
			var openJobs []uuid.UUID
			rows, err := pool.Query(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND queue='default' AND job_type='open_pr' AND dedupe_key=$2 ORDER BY id`, f.session.OrgID, db.OpenPRDedupeKey(f.changeset))
			require.NoError(t, err, "inspect the actual open-pr dedupe collision without assuming a later wake exists")
			for rows.Next() {
				var id uuid.UUID
				require.NoError(t, rows.Scan(&id), "scan every open-pr dedupe owner")
				openJobs = append(openJobs, id)
			}
			require.NoError(t, rows.Err(), "complete the exact open-pr collision read")
			rows.Close()
			require.Equal(t, []uuid.UUID{f.jobID}, openJobs, "terminal resume enqueue must collide with the still-running original open-pr job, leaving no rescue job")
			var currentJobStatus models.JobStatus
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, f.session.OrgID, f.jobID).Scan(&currentJobStatus), "read the original open-pr owner after synchronous settlement")
			require.Equal(t, models.JobStatusRunning, currentJobStatus, "the fixture must keep the current job running throughout the real resume enqueue")
			var frozenBefore, frozenAfter json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(s) FROM review_loop_start_intents s WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.session.OrgID, f.session.ID, loop.ID).Scan(&frozenBefore), "capture all immutable retained start fields before original-caller replay")
			ready, err = ensureAutomationPrePRReview(ctx, f.stores, &Services{ReviewLoops: f.service}, zerolog.Nop(), f.session, &f.changeset)
			require.False(t, ready, "same-original replay must not gain publication authority")
			if tt.retry {
				var retry *RetryableError
				require.ErrorAs(t, err, &retry, "same-original unresolved recovery must preserve the retryable automation wait")
			} else {
				require.NoError(t, err, "same-original refused recovery must preserve the finite failure")
			}
			callerAfter, err := f.stores.AutomationRuns.GetByRunID(ctx, f.session.OrgID, f.runID)
			require.NoError(t, err, "read the retained automation caller after idempotent recovery")
			require.Equal(t, caller.ReviewStartID, callerAfter.ReviewStartID, "replay must recover the exact original generation")
			publicationAfter, err := f.stores.SessionPublications.GetByChangeset(ctx, f.session.OrgID, f.session.ID, f.changeset)
			require.NoError(t, err, "read the current publication gate after original-caller replay")
			require.Equal(t, tt.gate, publicationAfter.ReviewGateState, "replay must preserve the original finite failure or unresolved gate")
			require.Equal(t, tt.publication, publicationAfter.State, "replay must preserve the same publication outcome without granting readiness")
			require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(s) FROM review_loop_start_intents s WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.session.OrgID, f.session.ID, loop.ID).Scan(&frozenAfter), "read every original retained start field after replay")
			require.Equal(t, frozenBefore, frozenAfter, "same-original automation replay must preserve the complete frozen start")
			var graph [7]int
			require.NoError(t, pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM session_review_loops WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM session_threads WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2)`, f.session.OrgID, f.session.ID).
				Scan(&graph[0], &graph[1], &graph[2], &graph[3], &graph[4], &graph[5], &graph[6]), "read the complete retained review graph and every possible command effect after replay")
			require.Equal(t, [7]int{1, 1, 1, 2, 0, 0, 0}, graph, "automation replay must retain one original loop, pass and review thread without a message, stamp, inbox entry or replacement generation")
			require.Equal(t, 1, f.runtime.calls, "a refused or unknown original startup must never send again during caller replay")
		})
	}
}
