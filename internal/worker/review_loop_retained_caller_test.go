package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRetainedStartRefusalUsesFiniteCallerOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		automation bool
		bypass     bool
		failed     bool
	}{
		{name: "publication refusal becomes failed once"},
		{name: "already failed publication remains settled", failed: true},
		{name: "explicit bypass preserves not required", bypass: true},
		{name: "automation refusal uses existing failure outcome", automation: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			t.Cleanup(mock.Close)
			stores.SessionPublications = db.NewSessionPublicationStore(mock)
			org, session, changeset, startID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			run := models.Session{ID: session, OrgID: org}
			publication := &models.SessionPublication{OrgID: org, SessionID: session, ChangesetID: changeset, ReviewStartID: &startID, ReviewGateState: models.SessionPublicationReviewGatePending}
			if tt.bypass {
				publication.ReviewGateState = models.SessionPublicationReviewGateNotRequired
			}
			if tt.failed {
				publication.ReviewGateState = models.SessionPublicationReviewGateFailed
			}
			recovery := &retainedCallerReviewStub{err: reviewloopsvc.ErrReviewLoopStartRefused}
			if tt.automation {
				automationID, runID := uuid.New(), uuid.New()
				run.AutomationRunID = &runID
				stores.AutomationRuns = db.NewAutomationRunStore(mock)
				now := time.Now()
				for range 2 {
					mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id AND org_id = @org_id`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(automationRunRows(runID, automationID, org, now, models.AutomationTriggeredBySchedule, nil, nil, nil, nil, nil, []byte("{}"), "synthetic retained refusal", json.RawMessage(`{"pre_pr_review_loops":999}`), models.AutomationRunStatusCompleted, nil, nil, nil, now, now, &startID))
					rows := int64(1) // Exact same-current failed replay may repair its separate PR-state projection.
					expectRetainedReviewGateCAS(mock, org, session, changeset, startID, &runID, models.SessionPublicationReviewGateFailed, rows)
					if rows > 0 {
						mock.ExpectQuery("INSERT INTO session_publish_state").WithArgs(pgx.NamedArgs{
							"id": session, "org_id": org, "state": string(models.PRCreationStateFailed), "err": "Pre-PR review did not complete cleanly.",
						}).WillReturnRows(pgxmock.NewRows(workerSessionColumns))
					}
				}
			} else if !tt.bypass && !tt.failed {
				expectRetainedReviewGateCAS(mock, org, session, changeset, startID, nil, models.SessionPublicationReviewGateFailed, 1)
			}
			for range 2 {
				var ready bool
				var err error
				if tt.automation {
					ready, err = ensureAutomationPrePRReview(context.Background(), stores, &Services{ReviewLoops: recovery}, zerolog.Nop(), run, &changeset)
				} else {
					ready, err = ensurePublicationPrePRReview(context.Background(), stores, &Services{ReviewLoops: recovery}, zerolog.Nop(), run, &changeset, publication)
				}
				require.NoError(t, err, "known refusal must settle through the existing finite caller outcome, including repeated execution")
				require.Equal(t, tt.bypass, ready, "only an explicit existing bypass may permit publication")
			}
			if !tt.automation {
				expected := models.SessionPublicationReviewGateFailed
				if tt.bypass {
					expected = models.SessionPublicationReviewGateNotRequired
				}
				require.Equal(t, expected, publication.ReviewGateState, "refusal must preserve the established failed or explicitly bypassed outcome")
				require.Equal(t, &startID, publication.ReviewStartID, "settlement must retain the original refused identity")
			}
			require.Equal(t, [][3]uuid.UUID{{org, session, startID}, {org, session, startID}}, recovery.calls, "repeated caller execution recovers the same original")
			require.Empty(t, recovery.starts, "known refusal must never allocate a replacement start")
			require.NoError(t, mock.ExpectationsWereMet(), "refusal must not parse changed policy, push a branch, select another reviewer or load a fresh loop")
		})
	}
}

var errRetainedGateCommitACK = errors.New("synthetic retained gate commit acknowledgement unavailable")

type retainedGateACKPool struct {
	*pgxpool.Pool
	lost bool
}

func (p *retainedGateACKPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return retainedGateACKTx{Tx: tx, owner: p}, nil
}

type retainedGateACKTx struct {
	pgx.Tx
	owner *retainedGateACKPool
}

func (t retainedGateACKTx) Commit(ctx context.Context) error {
	if err := t.Tx.Commit(ctx); err != nil {
		return err
	}
	if !t.owner.lost {
		t.owner.lost = true
		return errRetainedGateCommitACK
	}
	return nil
}

// The retained start is a legal SQL-shape fixture and recovery supplies the
// terminal result. This exercises the actual worker mapping, gate transaction,
// suppressed real commit ACK and separate existing PR-state projection only.
func TestRetainedAutomationTerminalProjectionCommitACKPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status models.ReviewLoopStatus
		gate   models.SessionPublicationReviewGateState
	}{
		{name: "failed gate replay repairs missing PR projection", status: models.ReviewLoopStatusFailed, gate: models.SessionPublicationReviewGateFailed},
		{name: "needs human gate replay repairs missing PR projection", status: models.ReviewLoopStatusNeedsHumanDecision, gate: models.SessionPublicationReviewGateNeedsHuman},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
			org, automation, runID, startID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic terminal projection')`, org)
			require.NoError(t, err, "seed this isolated synthetic tenant")
			sessions := db.NewSessionStore(pool)
			session := models.Session{OrgID: org, AgentType: models.AgentTypeClaudeCode, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
			require.NoError(t, sessions.Create(ctx, &session), "use the genuine session and primary-thread writer")
			require.NotNil(t, session.PrimaryThreadID, "actual session must own its primary thread")
			_, err = pool.Exec(ctx, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic projection automation','Retain original review')`, automation, org)
			require.NoError(t, err, "seed exact owned automation")
			_, err = pool.Exec(ctx, `INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,session_id,triggered_by,config_snapshot) VALUES($1,$2,$3,'Synthetic original',$4,'manual','{"pre_pr_review_loops":999}')`, runID, org, automation, session.ID)
			require.NoError(t, err, "changed mutable policy cannot affect retained terminal recovery")
			integration, repository, changeset := uuid.New(), uuid.New(), uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, org)
			require.NoError(t, err, "seed synthetic owned integration for a legal publication")
			_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,1,'synthetic/projection','https://example.invalid/projection.git',1)`, repository, org, integration)
			require.NoError(t, err, "retain real scoped publication parent references")
			_, err = pool.Exec(ctx, `INSERT INTO session_changesets(id,org_id,session_id,order_index,title,target_branch,base_branch) VALUES($1,$2,$3,99,'Synthetic projection','main','main')`, changeset, org, session.ID)
			require.NoError(t, err, "retain exact changeset scope")
			_, err = pool.Exec(ctx, `INSERT INTO session_publications(org_id,session_id,changeset_id,repository_id,base_branch,head_branch,state,review_gate_state,review_max_passes) VALUES($1,$2,$3,$4,'main','synthetic/projection','review_pending','pending',2)`, org, session.ID, changeset, repository)
			require.NoError(t, err, "begin with an actual pending publication")
			_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
			require.NoError(t, err, "enable only this disposable retained fixture")
			input, err := json.Marshal(map[string]any{"version": 1, "purpose": "review-loop-initial-send", "org_id": org.String(), "session_id": session.ID.String(), "thread_id": session.PrimaryThreadID.String(), "client_message_id": "rl-startup:v1:" + startID.String()})
			require.NoError(t, err, "encode a bounded SQL-scope fixture without pretending to send it")
			transactions, err := db.NewThreadSendTransactions(pool)
			require.NoError(t, err, "prepare the genuine retained automation caller through the existing bounded owner")
			require.NoError(t, transactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
				_, err := db.PrepareReviewLoopStartInTx(ctx, org, tx, db.ReviewLoopStartPreparation{Scope: db.ReviewLoopStartScope{ID: startID, SessionID: session.ID, Source: models.ReviewLoopSourceAutomation, AutomationRunID: &runID}, PassID: uuid.New(), ThreadID: *session.PrimaryThreadID, Envelope: json.RawMessage(`{}`), Input: input, EnvelopeDigest: strings.Repeat("a", 64), StartupDigest: strings.Repeat("b", 64)})
				return err
			}), "commit real retained identity and its wake before testing only the terminal caller mapping")
			session.AutomationRunID = &runID
			fault := &retainedGateACKPool{Pool: pool}
			stores := &Stores{Sessions: sessions, AutomationRuns: db.NewAutomationRunStore(pool), SessionPublications: db.NewSessionPublicationStore(fault)}
			recovery := &retainedCallerReviewStub{loop: &models.SessionReviewLoop{ID: startID, OrgID: org, SessionID: session.ID, Source: models.ReviewLoopSourceAutomation, AutomationRunID: &runID, Status: tt.status}}
			var beforeProjection string
			require.NoError(t, pool.QueryRow(ctx, `SELECT COALESCE((SELECT pr_creation_state FROM session_publish_state WHERE org_id=$1 AND session_id=$2),'idle')`, org, session.ID).Scan(&beforeProjection), "capture the actual separate PR projection before the gate commit")
			ready, err := ensureAutomationPrePRReview(ctx, stores, &Services{ReviewLoops: recovery}, zerolog.Nop(), session, &changeset)
			require.ErrorIs(t, err, errRetainedGateCommitACK, "withhold only acknowledgement after the actual gate transaction commits")
			require.False(t, ready, "unknown commit acknowledgement grants no publication authority")
			require.True(t, fault.lost, "fault must follow a real successful Commit")
			var beforeReplay json.RawMessage
			var currentGate models.SessionPublicationReviewGateState
			var incompleteProjection string
			require.NoError(t, pool.QueryRow(ctx, `SELECT review_gate_state,to_jsonb(p) FROM session_publications p WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3`, org, session.ID, changeset).Scan(&currentGate, &beforeReplay), "read the authoritative committed gate before replay")
			require.Equal(t, tt.gate, currentGate, "the first terminal gate must actually commit despite its missing acknowledgement")
			require.NoError(t, pool.QueryRow(ctx, `SELECT COALESCE((SELECT pr_creation_state FROM session_publish_state WHERE org_id=$1 AND session_id=$2),'idle')`, org, session.ID).Scan(&incompleteProjection), "prove the separate projection has not yet been written")
			require.Equal(t, beforeProjection, incompleteProjection, "the lost-ACK boundary must occur before the existing PR-state projection")
			ready, err = ensureAutomationPrePRReview(ctx, stores, &Services{ReviewLoops: recovery}, zerolog.Nop(), session, &changeset)
			require.NoError(t, err, "same-current finite-outcome replay must repair the projection instead of stopping at a lost pending CAS")
			require.False(t, ready, "repaired blocked or failed projection never grants publication authority")
			var projection string
			require.NoError(t, pool.QueryRow(ctx, `SELECT pr_creation_state FROM session_publish_state WHERE org_id=$1 AND session_id=$2`, org, session.ID).Scan(&projection), "read the real existing PR-state writer result")
			require.Equal(t, string(models.PRCreationStateFailed), projection, "same-generation terminal replay must finish the established finite projection")
			var afterReplay json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(p) FROM session_publications p WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3`, org, session.ID, changeset).Scan(&afterReplay), "read every publication field after idempotent projection repair")
			require.Equal(t, beforeReplay, afterReplay, "projection repair must not change the committed gate, timestamps, evidence or private identity")
			require.Equal(t, [][3]uuid.UUID{{org, session.ID, startID}, {org, session.ID, startID}}, recovery.calls, "both executions must recover the same original caller identity")
			require.Empty(t, recovery.starts, "projection replay may not create another review generation")
		})
	}
}

type retainedCallerReviewStub struct {
	stubWorkerReviewLoops
	loop  *models.SessionReviewLoop
	err   error
	calls [][3]uuid.UUID
}

func (s *retainedCallerReviewStub) RecoverStart(_ context.Context, o, session, id uuid.UUID) (*models.SessionReviewLoop, error) {
	s.calls = append(s.calls, [3]uuid.UUID{o, session, id})
	return s.loop, s.err
}

func TestPublicationRetainedStartPrecedesFreshWork(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                             string
		pending, foreign, bypass, failed bool
	}{
		{name: "running original before empty policy"}, {name: "unknown creation", pending: true}, {name: "foreign original refused", foreign: true},
		{name: "cancelled original after bypass", bypass: true}, {name: "failed unlinked original", failed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.SessionPublications = db.NewSessionPublicationStore(mock)
			org, session, id, changeset := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			revision, head := int64(7), "synthetic frozen head"
			loop := &models.SessionReviewLoop{ID: id, OrgID: org, SessionID: session, Source: models.ReviewLoopSourcePublication, ChangesetID: &changeset, WorkspaceRevision: &revision, DesiredHeadSHA: &head, MaxPasses: 2, Status: models.ReviewLoopStatusRunning}
			recovery := &retainedCallerReviewStub{loop: loop}
			publication := &models.SessionPublication{OrgID: org, SessionID: session, ChangesetID: changeset, ReviewStartID: &id, ReviewGateState: models.SessionPublicationReviewGatePending}
			if tt.pending {
				recovery.loop = nil
				recovery.err = &reviewloopsvc.StartPendingError{LoopID: id}
			}
			if tt.foreign {
				loop.OrgID = uuid.New()
			}
			if tt.bypass {
				loop.Status = models.ReviewLoopStatusCancelled
				publication.ReviewGateState = models.SessionPublicationReviewGateNotRequired
			}
			if tt.failed {
				loop.Status = models.ReviewLoopStatusFailed
				expectRetainedReviewGateCAS(mock, org, session, changeset, id, nil, models.SessionPublicationReviewGateFailed, 1)
			}
			if !tt.pending && !tt.foreign && !tt.bypass && !tt.failed {
				mock.ExpectExec("UPDATE session_publications[\\s\\S]+review_start_id=@loop_id").WithArgs(anyArgs(7)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			ready, err := ensurePublicationPrePRReview(context.Background(), stores, &Services{ReviewLoops: recovery}, zerolog.Nop(), models.Session{ID: session, OrgID: org}, &changeset, publication)
			if tt.pending || tt.foreign {
				require.Error(t, err, "uncertain or foreign recovery may not fall through to a fresh start")
			} else {
				require.NoError(t, err, "the original caller must recover before any mutable configuration or provider work")
			}
			require.Equal(t, tt.bypass, ready, "only the explicit bypass preserves immediate publication")
			require.Equal(t, [][3]uuid.UUID{{org, session, id}}, recovery.calls, "recover exact independently retained correlation")
			require.Empty(t, recovery.starts, "recovery must never allocate a second start")
			require.NoError(t, mock.ExpectationsWereMet(), "no fresh policy, snapshot, reviewer selection, branch push or latest-loop read is permitted")
		})
	}
}

func TestAutomationRetainedStartPrecedesChangedPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pending bool
	}{{name: "running original ignores changed policy"}, {name: "uncertain original remains pending", pending: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.AutomationRuns = db.NewAutomationRunStore(mock)
			stores.ReviewLoops = db.NewSessionReviewLoopStore(mock)
			org, session, automation, runID, id := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Now()
			mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id AND org_id = @org_id`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(automationRunRows(runID, automation, org, now, models.AutomationTriggeredBySchedule, nil, nil, nil, nil, nil, []byte("{}"), "synthetic goal", json.RawMessage(`{"pre_pr_review_loops":999}`), models.AutomationRunStatusCompleted, nil, nil, nil, now, now, &id))
			recovery := &retainedCallerReviewStub{loop: &models.SessionReviewLoop{ID: id, OrgID: org, SessionID: session, Source: models.ReviewLoopSourceAutomation, AutomationRunID: &runID, Status: models.ReviewLoopStatusRunning}}
			if tt.pending {
				recovery.loop = nil
				recovery.err = &reviewloopsvc.StartPendingError{LoopID: id}
			}
			ready, err := ensureAutomationPrePRReview(context.Background(), stores, &Services{ReviewLoops: recovery}, zerolog.Nop(), models.Session{ID: session, OrgID: org, AutomationRunID: &runID}, nil)
			require.False(t, ready, "retained startup cannot release publication")
			if tt.pending {
				require.ErrorIs(t, err, reviewloopsvc.ErrReviewLoopStartPending, "uncertain creation remains explicitly pending")
			} else {
				var retry *RetryableError
				require.ErrorAs(t, err, &retry, "existing running original uses the existing terminal mapping without revalidating changed policy")
			}
			require.Equal(t, [][3]uuid.UUID{{org, session, id}}, recovery.calls, "automation must retain the original startup id")
			require.Empty(t, recovery.starts, "automation recovery must not start a replacement")
			require.NoError(t, mock.ExpectationsWereMet(), "retained recovery skips legacy latest-loop lookup and fresh preflights")
		})
	}
}

func expectRetainedReviewGateCAS(mock pgxmock.PgxPoolIface, org, session, changeset, startID uuid.UUID, automationRunID *uuid.UUID, gate models.SessionPublicationReviewGateState, rows int64) {
	mock.ExpectBegin()
	source := models.ReviewLoopSourcePublication
	if automationRunID == nil {
		mock.ExpectQuery("SELECT review_start_id FROM session_publications").WithArgs(org, session, changeset).WillReturnRows(pgxmock.NewRows([]string{"review_start_id"}).AddRow(&startID))
	} else {
		source = models.ReviewLoopSourceAutomation
		mock.ExpectQuery("SELECT review_start_id FROM automation_runs").WithArgs(org, automationRunID, session).WillReturnRows(pgxmock.NewRows([]string{"review_start_id"}).AddRow(&startID))
	}
	state := models.SessionPublicationStateReviewPending
	if gate == models.SessionPublicationReviewGateFailed {
		state = models.SessionPublicationStateTerminalFailed
	} else if gate == models.SessionPublicationReviewGatePassed {
		state = models.SessionPublicationStateReadyToPublish
	}
	mock.ExpectExec("UPDATE session_publications AS publication[\\s\\S]+publication.review_gate_state=@expected_gate[\\s\\S]+caller.review_start_id=@start_id").WithArgs(pgx.NamedArgs{
		"org_id": org, "session_id": session, "changeset_id": changeset, "start_id": startID,
		"source": source, "automation_run_id": automationRunID, "gate": gate, "state": state,
		"expected_gate": models.SessionPublicationReviewGatePending, "invalidate": false, "head_sha": (*string)(nil),
	}).WillReturnResult(pgxmock.NewResult("UPDATE", rows))
	mock.ExpectCommit()
}

func TestRetainedCallerGateCASMissGrantsNoAuthority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		automation, refused bool
		status              models.ReviewLoopStatus
		gate                models.SessionPublicationReviewGateState
	}{
		{name: "publication failed after bypass", status: models.ReviewLoopStatusFailed, gate: models.SessionPublicationReviewGateFailed},
		{name: "publication cancelled after bypass", status: models.ReviewLoopStatusCancelled, gate: models.SessionPublicationReviewGateNeedsHuman},
		{name: "publication needs human after replacement", status: models.ReviewLoopStatusNeedsHumanDecision, gate: models.SessionPublicationReviewGateNeedsHuman},
		{name: "publication refused after bypass", refused: true, gate: models.SessionPublicationReviewGateFailed},
		{name: "automation failed after bypass", automation: true, status: models.ReviewLoopStatusFailed, gate: models.SessionPublicationReviewGateFailed},
		{name: "automation cancelled after bypass", automation: true, status: models.ReviewLoopStatusCancelled, gate: models.SessionPublicationReviewGateFailed},
		{name: "automation needs human after replacement", automation: true, status: models.ReviewLoopStatusNeedsHumanDecision, gate: models.SessionPublicationReviewGateNeedsHuman},
		{name: "automation running after bypass", automation: true, status: models.ReviewLoopStatusRunning, gate: models.SessionPublicationReviewGatePending},
		{name: "automation clean after bypass", automation: true, status: models.ReviewLoopStatusClean, gate: models.SessionPublicationReviewGatePassed},
		{name: "automation refused after bypass", automation: true, refused: true, gate: models.SessionPublicationReviewGateFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			t.Cleanup(mock.Close)
			stores.SessionPublications = db.NewSessionPublicationStore(mock)
			org, session, changeset, startID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			run := models.Session{ID: session, OrgID: org}
			loop := &models.SessionReviewLoop{ID: startID, OrgID: org, SessionID: session, ChangesetID: &changeset, Source: models.ReviewLoopSourcePublication, Status: tt.status}
			publication := &models.SessionPublication{OrgID: org, SessionID: session, ChangesetID: changeset, ReviewStartID: &startID, ReviewGateState: models.SessionPublicationReviewGatePending}
			var runID *uuid.UUID
			if tt.automation {
				id, automation := uuid.New(), uuid.New()
				runID, run.AutomationRunID, loop.AutomationRunID = &id, &id, &id
				loop.Source = models.ReviewLoopSourceAutomation
				stores.AutomationRuns = db.NewAutomationRunStore(mock)
				now := time.Now()
				mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id AND org_id = @org_id`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(automationRunRows(id, automation, org, now, models.AutomationTriggeredBySchedule, nil, nil, nil, nil, nil, []byte("{}"), "synthetic stale caller", json.RawMessage(`{"pre_pr_review_loops":999}`), models.AutomationRunStatusCompleted, nil, nil, nil, now, now, &startID))
			}
			recovery := &retainedCallerReviewStub{loop: loop}
			if tt.refused {
				recovery.loop, recovery.err = nil, reviewloopsvc.ErrReviewLoopStartRefused
			}
			expectRetainedReviewGateCAS(mock, org, session, changeset, startID, runID, tt.gate, 0)
			var ready bool
			var err error
			if tt.automation {
				ready, err = ensureAutomationPrePRReview(context.Background(), stores, &Services{ReviewLoops: recovery}, zerolog.Nop(), run, &changeset)
			} else {
				ready, err = ensurePublicationPrePRReview(context.Background(), stores, &Services{ReviewLoops: recovery}, zerolog.Nop(), run, &changeset, publication)
			}
			require.NoError(t, err, "a bypassed or replaced target is a finite no-op after the retained gate CAS misses")
			require.False(t, ready, "a stale result cannot grant publication or fresh-start authority")
			require.Equal(t, models.SessionPublicationReviewGatePending, publication.ReviewGateState, "uncommitted local candidate must not claim that a failed CAS changed the current gate")
			require.Equal(t, &startID, publication.ReviewStartID, "no stale mapping may clear its retained correlation")
			require.Empty(t, recovery.starts, "a lost gate CAS cannot authorize a replacement start")
			require.NoError(t, mock.ExpectationsWereMet(), "a lost CAS must not update session PR state, inspect fresh policy, or invoke provider work")
		})
	}
}

func TestRetainedAutomationCleanReplayKeepsCurrentOutcome(t *testing.T) {
	t.Parallel()
	stores, mock := newTestStores(t)
	t.Cleanup(mock.Close)
	stores.AutomationRuns = db.NewAutomationRunStore(mock)
	stores.SessionPublications = db.NewSessionPublicationStore(mock)
	org, session, changeset, runID, automation, startID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	for range 2 {
		mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id AND org_id = @org_id`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(automationRunRows(runID, automation, org, now, models.AutomationTriggeredBySchedule, nil, nil, nil, nil, nil, []byte("{}"), "synthetic clean replay", json.RawMessage(`{"pre_pr_review_loops":999}`), models.AutomationRunStatusCompleted, nil, nil, nil, now, now, &startID))
		expectRetainedReviewGateCAS(mock, org, session, changeset, startID, &runID, models.SessionPublicationReviewGatePassed, 1)
	}
	recovery := &retainedCallerReviewStub{loop: &models.SessionReviewLoop{ID: startID, OrgID: org, SessionID: session, Source: models.ReviewLoopSourceAutomation, AutomationRunID: &runID, Status: models.ReviewLoopStatusClean}}
	for range 2 {
		ready, err := ensureAutomationPrePRReview(context.Background(), stores, &Services{ReviewLoops: recovery}, zerolog.Nop(), models.Session{ID: session, OrgID: org, AutomationRunID: &runID}, &changeset)
		require.NoError(t, err, "both first clean settlement and its exact same-generation replay must retain existing publication readiness")
		require.True(t, ready, "an atomic exact current clean-outcome match preserves ordinary retry progress")
	}
	require.Equal(t, [][3]uuid.UUID{{org, session, startID}, {org, session, startID}}, recovery.calls, "clean replay must keep the original independently retained identity")
	require.Empty(t, recovery.starts, "clean replay cannot allocate another review")
	require.NoError(t, mock.ExpectationsWereMet(), "clean replay must use the current-owner atomic match instead of fresh policy or legacy gate mutation")
}

func TestRetainedPublicationInvalidationCASMissStopsFreshWork(t *testing.T) {
	t.Parallel()
	stores, mock := newTestStores(t)
	t.Cleanup(mock.Close)
	stores.SessionPublications = db.NewSessionPublicationStore(mock)
	stores.SessionChangesets = db.NewSessionChangesetStore(mock)
	org, session, changesetID, startID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	oldRevision, oldHead, currentHead := int64(1), "synthetic-original-head", "synthetic-current-head"
	passes := 2
	publication := &models.SessionPublication{OrgID: org, SessionID: session, ChangesetID: changesetID, ReviewStartID: &startID, ReviewGateState: models.SessionPublicationReviewGatePassed, ReviewMaxPasses: &passes, ReviewWorkspaceRevision: &oldRevision, ReviewDesiredHeadSHA: &oldHead}
	recovery := &retainedCallerReviewStub{loop: &models.SessionReviewLoop{ID: startID, OrgID: org, SessionID: session, Source: models.ReviewLoopSourcePublication, ChangesetID: &changesetID, Status: models.ReviewLoopStatusClean}}
	changeset := models.SessionChangeset{ID: changesetID, OrgID: org, SessionID: session, HeadSHA: &currentHead}
	mock.ExpectQuery("SELECT .+ FROM session_changesets").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows(workerChangesetTestColumns).AddRow(workerChangesetTestRow(changeset)...))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT review_start_id FROM session_publications").WithArgs(org, session, changesetID).WillReturnRows(pgxmock.NewRows([]string{"review_start_id"}).AddRow(&startID))
	mock.ExpectExec("UPDATE session_publications AS publication[\\s\\S]+publication.review_gate_state=@expected_gate[\\s\\S]+publication.review_start_id=@start_id").WithArgs(pgx.NamedArgs{
		"org_id": org, "session_id": session, "changeset_id": changesetID, "start_id": startID,
		"source": models.ReviewLoopSourcePublication, "automation_run_id": (*uuid.UUID)(nil),
		"gate": models.SessionPublicationReviewGatePending, "state": models.SessionPublicationStateReviewPending,
		"expected_gate": models.SessionPublicationReviewGatePassed, "invalidate": true, "head_sha": &currentHead,
	}).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()
	ready, err := ensurePublicationPrePRReview(context.Background(), stores, &Services{ReviewLoops: recovery}, zerolog.Nop(), models.Session{ID: session, OrgID: org, WorkspaceRevision: 2}, &changesetID, publication)
	require.NoError(t, err, "replacement or bypass during retained clean-evidence invalidation is a finite no-op")
	require.False(t, ready, "lost retained invalidation cannot authorize publication or a fresh review")
	require.Equal(t, models.SessionPublicationReviewGatePassed, publication.ReviewGateState, "a missed CAS must not pretend the old local candidate changed the current gate")
	require.Equal(t, &startID, publication.ReviewStartID, "invalidation cannot replace the original correlation")
	require.Empty(t, recovery.starts, "a new caller generation must not be overwritten by a fresh Start from this stale invocation")
	require.NoError(t, mock.ExpectationsWereMet(), "retained invalidation must use the exact passed/current-generation CAS and stop before provider or fresh reviewer work")
}
