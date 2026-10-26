package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func sendPostgresExpectedUnits(active, grouped, running bool) []sendPostgresGraph {
	idle := sendPostgresGraph{parentStatus: models.SessionStatusIdle, threadStatus: models.ThreadStatusIdle}
	lane := idle
	lane.threadStatus = models.ThreadStatusRunning
	both := lane
	both.parentStatus = models.SessionStatusRunning
	message := both
	message.messages = 1
	inbox := message
	inbox.inbox = 1
	if active {
		accepted := inbox
		accepted.dispatches = 1
		if running {
			accepted.pending = 1
			return []sendPostgresGraph{both, accepted}
		}
		accepted.continueJobs, accepted.claims = 1, 1
		return []sendPostgresGraph{idle, accepted}
	}
	if running {
		pending := inbox
		pending.pending = 1
		queued := pending
		queued.liveJobs = 1
		if grouped {
			return []sendPostgresGraph{both, both, pending, queued}
		}
		return []sendPostgresGraph{both, both, message, inbox, pending, queued}
	}
	job := inbox
	job.continueJobs = 1
	if grouped {
		return []sendPostgresGraph{idle, lane, both, inbox, job}
	}
	return []sendPostgresGraph{idle, lane, both, message, inbox, job}
}

func TestThreadSendRealServiceDurableUnitsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                     string
		active, grouped, running bool
		wantTurn                 int
		wantDisposition          db.MessageAdmissionDisposition
	}{
		{name: "legacy idle separate message and inbox", wantTurn: 1},
		{name: "legacy idle grouped message and inbox", grouped: true, wantTurn: 1},
		{name: "legacy running separate message inbox pending notification", running: true, wantTurn: 2},
		{name: "legacy running grouped message inbox pending", grouped: true, running: true, wantTurn: 2},
		{name: "active idle one atomic accepted graph", active: true, wantTurn: 1, wantDisposition: db.MessageAdmissionNewJob},
		{name: "active grouped selector does not change atomic command", active: true, grouped: true, wantTurn: 1, wantDisposition: db.MessageAdmissionNewJob},
		{name: "active running accepts due queued input only", active: true, running: true, wantTurn: 2, wantDisposition: db.MessageAdmissionQueued},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, tt.active, 1)
			if tt.running {
				sendPostgresExec(t, f, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.org, f.session)
				sendPostgresExec(t, f, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.lanes[0])
			}
			service, effects := f.service(t, tt.grouped)
			recorder := &sendPostgresRecorder{base: service.send.Transactions, fixture: f}
			service.send.Transactions = recorder
			input := f.input(0, "synthetic-durable-send")
			var before time.Time
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&before), "bound legacy database-default schedule using server time before the command")
			result, err := service.SendMessage(context.Background(), input)
			require.NoError(t, err, "the actual service must compose real guards, persistence and transaction owner for the selected characterized path")
			require.NotNil(t, result.Message, "known acceptance must expose the committed message")
			require.Equal(t, tt.wantTurn, result.Message.TurnNumber, "message attribution must preserve idle current+1 versus same-lane current+2 semantics")
			require.Equal(t, "[PLAN_MODE]\n"+input.Message, result.Message.Content, "plan-mode prefix must survive real persistence")
			require.Equal(t, input.UserID, result.Message.UserID, "the exact authenticated actor must be retained")
			require.Equal(t, input.Images, result.Message.Attachments, "attachments cannot be dropped by transaction conversion")
			require.Equal(t, input.References, result.Message.References, "references must retain exact structured values")
			require.Equal(t, input.Commands, result.Message.Commands, "commands must retain exact typed structured values")
			require.Equal(t, input.MessageSource, result.Message.Source, "CreateWithSource attribution must survive the actual adapter")
			stored, err := db.NewSessionMessageStore(f.pool).GetByID(context.Background(), f.org, result.Message.ID)
			require.NoError(t, err, "read the exact committed message through its tenant-scoped production reader")
			require.Equal(t, *result.Message, stored, "the entire returned message must match durable storage")
			inbox, err := db.NewThreadInboxStore(f.pool).GetByClientMessageID(context.Background(), f.org, input.ThreadID, input.ClientMessageID)
			require.NoError(t, err, "read the exact durable client inbox association")
			require.Equal(t, &inbox, result.InboxEntry, "the entire returned inbox receipt must match durable storage")
			wantUnits := sendPostgresExpectedUnits(tt.active, tt.grouped, tt.running)
			actualUnits := make([]sendPostgresGraph, len(recorder.units))
			ids := make(map[int64]struct{}, len(recorder.units))
			for i, unit := range recorder.units {
				actualUnits[i] = unit.graph
				ids[unit.id] = struct{}{}
			}
			require.Equal(t, wantUnits, actualUnits, "real transaction snapshots must preserve every original v0 separate/grouped boundary or the v1 two-stage observation/admission boundary")
			require.Equal(t, len(wantUnits), len(ids), "each recorded unit must be a distinct real PostgreSQL transaction, never a savepoint or encompassing legacy owner")
			require.Equal(t, wantUnits[len(wantUnits)-1], sendPostgresReadGraph(t, f), "independent postcommit read must equal the complete final graph and unchanged execution counters")
			if tt.active {
				require.NotNil(t, result.Admission, "active acceptance must expose the sealed canonical receipt")
				require.Equal(t, tt.wantDisposition, result.Admission.Disposition(), "active running input must not pretend to have enqueued another job")
				require.Equal(t, 1, len(effects.lifecycle), "new active acceptance publishes one postcommit effect bundle")
				if tt.running {
					require.Empty(t, effects.jobs, "queued v1 input has no new consumer job to notify")
					var state, consumer string
					var due *time.Time
					require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT admission_state,consumer_kind,next_attempt_at FROM session_input_dispatches WHERE org_id=$1 AND id=$2`, f.org, result.Admission.DispatchID()).Scan(&state, &consumer, &due), "inspect exact queued dispatch discovery state")
					require.Equal(t, []string{"waiting", "none"}, []string{state, consumer}, "queued dispatch remains unbound and due")
					require.NotNil(t, due, "queued acceptance must remain discoverable independently of the handler")
					return
				}
			} else {
				require.Nil(t, result.Admission, "version zero must not fabricate a canonical v1 receipt")
				require.Empty(t, effects.lifecycle, "legacy sends retain their original opaque publishers rather than active effect bundles")
				wantClaims, wantPending := 1, 0
				if tt.running {
					wantClaims = 0
					if !tt.grouped {
						wantPending = 1
					}
				}
				require.Equal(t, []int{wantClaims, wantPending, 0, 0}, []int{effects.claims, effects.pending, effects.threadStatus, effects.parentStatus}, "claim snapshots, standalone pending reloads and eventless parent/grouped units must retain exact legacy publication behavior")
			}
			jobType, priority := "continue_session", 5
			if tt.running {
				jobType, priority = "deliver_thread_inbox", 7
			}
			var jobID uuid.UUID
			var queue, kind, status, key string
			var actualPriority, maxAttempts int
			var runAt time.Time
			var target *string
			var payload json.RawMessage
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id,queue,job_type,status,priority,max_attempts,run_at,target_node_id,payload,dedupe_key FROM jobs WHERE org_id=$1 AND job_type=$2`, f.org, jobType).Scan(&jobID, &queue, &kind, &status, &actualPriority, &maxAttempts, &runAt, &target, &payload, &key), "read only this tenant's single newly created job and concrete options")
			require.Equal(t, []string{"agent", jobType, "pending"}, []string{queue, kind, status}, "send must preserve queue, job kind and pending status")
			require.Equal(t, []int{priority, 3}, []int{actualPriority, maxAttempts}, "priority and retry budget must retain characterized defaults")
			require.Nil(t, target, "no physical provider or runtime owner is invented by this fixture")
			expectedPayload := map[string]any{"org_id": f.org.String(), "session_id": f.session.String(), "thread_id": input.ThreadID.String()}
			if !tt.running {
				var actual map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(payload, &actual), "decode the real immutable job envelope")
				var holder uuid.UUID
				require.NoError(t, json.Unmarshal(actual["changeset_lease_holder_id"], &holder), "retain the once-minted server holder")
				require.NotEqual(t, uuid.Nil, holder, "changeset continuation must have a nonzero server holder")
				expectedPayload["changeset_id"], expectedPayload["changeset_lease_holder_id"], expectedPayload["post_success_action"], expectedPayload["post_success_author_mode"] = f.changeset.String(), holder.String(), input.PostSuccessAction, input.PostSuccessAuthorMode
				if tt.active {
					expectedPayload["queued_message_id"] = fmt.Sprint(result.Message.ID)
					expectedPayload["lifecycle_binding_version"], expectedPayload["admission_session_epoch"], expectedPayload["admission_thread_epoch"], expectedPayload["input_dispatch_ids"], expectedPayload["lifecycle_dispatch_kind"] = 1, 3, 5, []uuid.UUID{result.Admission.DispatchID()}, "message"
					require.Equal(t, result.Admission.DispatchOptions(), db.MessageAdmissionDispatchOptions{RunAt: runAt.UTC(), MaxAttempts: maxAttempts}, "stored concrete options must equal the immutable accepted receipt independent of the database driver's scan location")
					retained, present := result.Admission.LeaseHolderID()
					require.True(t, present, "canonical receipt retains the server holder")
					require.Equal(t, retained, holder, "receipt and durable job must retain the same original holder")
					require.Equal(t, db.ContinueSessionDedupeKey(input.ThreadID), key, "active enqueue retains the exact concrete thread-keyed option while immutable dispatch binding prevents adopting another consumer")
					requireSendPostgresClaimLink(t, f, *result.Admission, jobID, result.Message)
				} else {
					require.False(t, runAt.Before(before), "legacy default schedule cannot precede command start")
					require.Equal(t, db.ContinueSessionDedupeKey(input.ThreadID), key, "legacy continuation dedupe remains thread keyed")
				}
			} else {
				require.Equal(t, "deliver_thread_inbox:"+input.ThreadID.String(), key, "live notification retains legacy thread inbox dedupe")
			}
			expectedJSON, err := json.Marshal(expectedPayload)
			require.NoError(t, err, "encode the independently expected complete job envelope")
			require.JSONEq(t, string(expectedJSON), string(payload), "real queue persistence must retain exactly the expected payload with no hidden consumer or authority fields")
			require.Equal(t, []uuid.UUID{jobID}, effects.jobs, "only the exact known-committed new job may be notified once")
		})
	}
}

func TestThreadSendThreeSiblingJobsAndFourthDuePostgres(t *testing.T) {
	t.Parallel()
	f := newSendPostgresFixture(t, true, 4)
	service, effects := f.service(t, true)
	// These fields are deliberately unavailable. They are not authority and the
	// test does not start a provider; B.2/E.1 still owns mandatory launch proof.
	sendPostgresExec(t, f, `UPDATE sessions SET sandbox_state='destroyed',snapshot_key=NULL,container_id=NULL,worker_node_id=NULL WHERE org_id=$1 AND id=$2`, f.org, f.session)
	var certified int64
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM session_input_workspace_compatibility WHERE org_id=$1 AND session_id=$2 AND state='certified'`, f.org, f.session).Scan(&certified), "inspect only the synthetic fixture's passive request compatibility rows")
	require.Equal(t, int64(0), certified, "fixture has no positive request certificate; this absence is not treated as admission authority")
	jobs := make([]uuid.UUID, 0, 3)
	for i := range f.lanes {
		result, err := service.SendMessage(context.Background(), f.input(i, fmt.Sprintf("synthetic-sibling-%d", i)))
		require.NoError(t, err, "pending sibling jobs and unprovable request compatibility cannot veto an independent ordinary lane")
		require.NotNil(t, result, "known successful ordinary admission must return its durable result")
		require.NotNil(t, result.Admission, "active sibling progress must expose its canonical receipt")
		want := db.MessageAdmissionNewJob
		if i == 3 {
			want = db.MessageAdmissionWaiting
		}
		require.Equal(t, want, result.Admission.Disposition(), "exactly three independent logical lane claims may bind and the fourth remains accepted waiting")
		job, hasJob := result.Admission.JobID()
		if i < 3 {
			require.True(t, hasJob, "each of the first three lanes must bind its own job")
			jobs = append(jobs, job)
			if i == 0 {
				// A request created only after lane zero's acceptance is an
				// explicit sibling request for every later target. Seeding it
				// before that first send would correctly require A3 instead.
				request := seedSendPostgresCapabilityRequest(t, f, &f.lanes[0])
				sendPostgresExec(t, f, `INSERT INTO session_input_workspace_compatibility(org_id,session_id,request_id,state) VALUES($1,$2,$3,'unprovable')`, f.org, f.session, request)
			}
		} else {
			require.False(t, hasJob, "capacity wait cannot borrow or invent a consumer")
			var state, consumer string
			var due *time.Time
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT admission_state,consumer_kind,next_attempt_at FROM session_input_dispatches WHERE org_id=$1 AND id=$2`, f.org, result.Admission.DispatchID()).Scan(&state, &consumer, &due), "read the fourth lane's independent waiting receipt")
			require.Equal(t, []string{"waiting", "none"}, []string{state, consumer}, "capacity acceptance must remain unbound")
			require.NotNil(t, due, "fourth-lane waiting must be independently due and discoverable")
		}
	}
	require.Equal(t, sendPostgresGraph{messages: 4, inbox: 4, dispatches: 4, continueJobs: 3, claims: 3, pending: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, sendPostgresReadGraph(t, f), "four accepted inputs must produce exactly three distinct jobs and lane claims, one durable wait, and no execution counter advancement")
	require.Equal(t, jobs, effects.jobs, "only the three independently committed jobs are notified")
	unique := map[uuid.UUID]struct{}{}
	for _, id := range jobs {
		unique[id] = struct{}{}
	}
	require.Equal(t, 3, len(unique), "the three logical jobs must have independent identities")
}

func TestThreadSendCompetingLegacyConsumersRemainDuePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		route string
	}{
		{name: "session-wide legacy consumer", route: "session"},
		{name: "same-lane legacy consumer", route: "lane"},
		{name: "unattributed retained executor consumer", route: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, true, 1)
			service, effects := f.service(t, true)
			payload := map[string]string{}
			if tt.route != "unknown" {
				payload["session_id"] = f.session.String()
			}
			if tt.route == "lane" {
				payload["thread_id"] = f.lanes[0].String()
			}
			var existing uuid.UUID
			require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				existing, err = db.NewJobStore(tx).EnqueueInTx(ctx, tx, f.org, "agent", "continue_session", payload, 5, nil)
				return err
			}), "seed one synthetic nonterminal legacy consumer using actual supplied-tx enqueue without notification")
			if tt.route == "unknown" {
				// An empty payload has no session route. Its retained executor is
				// the scoped evidence requiring reconciliation; unrelated jobs
				// must not become tenant-wide blockers merely by lacking a route.
				node := "synthetic-" + uuid.NewString()
				sendPostgresExec(t, f, `INSERT INTO nodes(id) VALUES($1)`, node)
				sendPostgresExec(t, f, `INSERT INTO session_executors(org_id,session_id,thread_id,job_id,job_type,host_node_id,owner_id,lock_token,status,lease_expires_at)
 VALUES($1,$2,$3,$4,'continue_session',$5,'synthetic-owner',gen_random_uuid(),'running',now()-interval '1 day')`, f.org, f.session, f.lanes[0], existing, node)
			}
			result, err := service.SendMessage(context.Background(), f.input(0, "synthetic-competing-consumer"))
			require.NoError(t, err, "canonical ordinary admission must accept blocked input durably without creating a second consumer")
			require.Equal(t, db.MessageAdmissionWaiting, result.Admission.Disposition(), "session-wide, same-lane and unattributed consumers cannot be mistaken for compatible sibling work")
			_, hasJob := result.Admission.JobID()
			require.False(t, hasJob, "waiting receipt must not adopt a foreign pre-existing job")
			require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, dispatches: 1, continueJobs: 1, pending: 1, parentStatus: models.SessionStatusIdle, threadStatus: models.ThreadStatusIdle}, sendPostgresReadGraph(t, f), "blocked input creates exactly one due dispatch and no lane claim or second job")
			var due *time.Time
			var consumer string
			var linked *uuid.UUID
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT next_attempt_at,consumer_kind,job_id FROM session_input_dispatches WHERE org_id=$1 AND id=$2`, f.org, result.Admission.DispatchID()).Scan(&due, &consumer, &linked), "read the exact independently discoverable wait")
			require.NotNil(t, due, "blocked acceptance must survive beyond handler lifetime with a due retry")
			require.Equal(t, "none", consumer, "waiting input owns no consumer")
			require.Nil(t, linked, "old consumer identity is not adopted into the new dispatch")
			var retained uuid.UUID
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id FROM jobs WHERE org_id=$1 AND id=$2 AND status='pending'`, f.org, existing).Scan(&retained), "the original consumer must remain untouched")
			require.Equal(t, existing, retained, "accepted waiting cannot replace the original consumer")
			require.Empty(t, effects.jobs, "waiting input must not issue a new-job notification")
		})
	}
}

func TestThreadSendPendingAnswerScopePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		requestScope   string
		targetAwaiting bool
		question       bool
		wantAccepted   bool
	}{
		{name: "aggregate parent awaiting alone does not invent answer authority", wantAccepted: true},
		{name: "other-lane capability request and unprovable certificate do not veto sibling", requestScope: "sibling", wantAccepted: true},
		{name: "target capability request requires exact A3 answer", requestScope: "target"},
		{name: "threadless capability request remains whole scope", requestScope: "threadless"},
		{name: "request lane belonging to another parent is not a compatible sibling", requestScope: "foreign-parent"},
		{name: "target lane awaiting remains strict despite sibling request", requestScope: "sibling", targetAwaiting: true},
		{name: "session-wide legacy question is not answered as latest", question: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, true, 2)
			sendPostgresExec(t, f, `UPDATE sessions SET status='awaiting_input' WHERE org_id=$1 AND id=$2`, f.org, f.session)
			sendPostgresExec(t, f, `UPDATE session_threads SET status='awaiting_input' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.lanes[1])
			if tt.targetAwaiting {
				sendPostgresExec(t, f, `UPDATE session_threads SET status='awaiting_input' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.lanes[0])
			}
			var requestID uuid.UUID
			var requestBefore models.HumanInputRequest
			if tt.requestScope != "" {
				var lane *uuid.UUID
				switch tt.requestScope {
				case "sibling":
					lane = &f.lanes[1]
				case "target":
					lane = &f.lanes[0]
				case "foreign-parent":
					other := models.Session{OrgID: f.org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
					require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error { return db.NewSessionStore(tx).CreateInTx(ctx, tx, &other) }), "seed a real same-tenant foreign parent so the contradictory request FK is valid")
					require.NotNil(t, other.PrimaryThreadID, "real foreign session creation must expose its distinct primary lane")
					lane = other.PrimaryThreadID
				}
				requestID = seedSendPostgresCapabilityRequest(t, f, lane)
				var err error
				requestBefore, err = db.NewSessionHumanInputRequestStore(f.pool).GetByID(context.Background(), f.org, f.session, requestID)
				require.NoError(t, err, "retain the complete unresolved capability request before ordinary admission")
				if tt.requestScope == "sibling" {
					sendPostgresExec(t, f, `INSERT INTO session_input_workspace_compatibility(org_id,session_id,request_id,state) VALUES($1,$2,$3,'unprovable')`, f.org, f.session, requestID)
				}
			}
			var question models.SessionQuestion
			if tt.question {
				question = models.SessionQuestion{OrgID: f.org, SessionID: f.session, QuestionText: "Synthetic exact question", Status: models.SessionQuestionStatusPending, Options: []string{}}
				require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
					return db.NewSessionQuestionStore(tx).Create(ctx, &question)
				}), "seed one real unresolved legacy question without calling an answer API")
			}
			before := sendPostgresReadGraph(t, f)
			service, effects := f.service(t, true)
			result, err := service.SendMessage(context.Background(), f.input(0, "synthetic-pending-answer-scope"))
			if tt.wantAccepted {
				require.NoError(t, err, "ordinary sibling progress cannot be blocked by aggregate parent status or another lane's capability certificate")
				require.Equal(t, db.MessageAdmissionNewJob, result.Admission.Disposition(), "idle independent target receives an ordinary job, not an answer or artificial wait")
				require.Nil(t, result.AnsweredQuestion, "ordinary active admission never calls AnswerLatestPendingBySession")
				require.Nil(t, result.AnsweredHumanInput, "ordinary active admission never grants or answers the sibling capability request")
				require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, dispatches: 1, continueJobs: 1, claims: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, sendPostgresReadGraph(t, f), "independent target creates exactly one ordinary graph without changing execution counters")
				require.Equal(t, []int{1, 1}, []int{len(effects.lifecycle), len(effects.jobs)}, "only the newly committed ordinary job may publish and notify")
			} else {
				require.ErrorIs(t, err, db.ErrMessageAdmissionAwaitingInput, "whole, target, ambiguous or exact awaiting input must remain unsupported by A1 ordinary admission")
				require.Nil(t, result, "A3-required input exposes no accepted ordinary receipt")
				require.Equal(t, before, sendPostgresReadGraph(t, f), "A3 refusal must leave no message, inbox, dispatch, job, lane claim or counter change")
				require.Empty(t, effects.lifecycle, "A3 refusal cannot publish committed admission effects")
				require.Empty(t, effects.jobs, "A3 refusal cannot notify a job")
			}
			if requestID != uuid.Nil {
				requestAfter, readErr := db.NewSessionHumanInputRequestStore(f.pool).GetByID(context.Background(), f.org, f.session, requestID)
				require.NoError(t, readErr, "reload the exact original request with its tenant and parent association")
				require.Equal(t, requestBefore, requestAfter, "ordinary success or refusal cannot alter any capability request attribution, payload, answer or status")
			}
			if tt.question {
				questionAfter, readErr := db.NewSessionQuestionStore(f.pool).GetByID(context.Background(), f.org, question.ID)
				require.NoError(t, readErr, "reload only the exact original tenant-scoped question")
				require.Equal(t, question, questionAfter, "ordinary admission cannot answer or mutate the latest legacy question")
			}
		})
	}
}

func TestThreadSendRepairReconciliationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		active   bool
		comments bool
		want     db.MessageAdmissionDisposition
		reason   string
	}{
		{name: "unresolved sibling repair accepts ordinary input due without scope proof", active: true, want: db.MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "unresolved repair and review comments require whole abort", active: true, comments: true},
		{name: "released old-head history alone is not a permanent owner", want: db.MessageAdmissionNewJob},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, true, 2)
			run := seedSendPostgresRepair(t, f, f.lanes[1], tt.active)
			service, effects := f.service(t, true)
			input := f.input(0, "synthetic-repair-reconciliation")
			var comment uuid.UUID
			if tt.comments {
				comment = seedSendPostgresComment(t, f)
				input.ResolveReviewCommentIDs = []uuid.UUID{comment}
			}
			before := sendPostgresReadGraph(t, f)
			result, err := service.SendMessage(context.Background(), input)
			if tt.comments {
				require.ErrorIs(t, err, ErrThreadNotIdle, "a review-comment command must preserve sent=false busy behavior rather than commit a waiting comment")
				require.ErrorIs(t, err, db.ErrMessageAdmissionBusy, "service must preserve the exact canonical busy cause for a non-new-job comment disposition")
				require.Nil(t, result, "busy comment command cannot expose partial acceptance")
				require.Equal(t, before, sendPostgresReadGraph(t, f), "whole rollback must retain no message, inbox, comment dispatch, job or claim")
				require.False(t, sendPostgresCommentResolved(t, f, comment), "unresolved repair cannot resolve the review comment without an admitted job")
				require.Empty(t, effects.lifecycle, "busy comment abort cannot publish effects")
				require.Empty(t, effects.jobs, "busy comment abort cannot notify a job")
			} else {
				require.NoError(t, err, "ordinary no-comment input must be accepted durably while unresolved repair awaits A5 scope reconciliation")
				require.Equal(t, tt.want, result.Admission.Disposition(), "active reservation and released history must have distinct finite dispositions")
				require.Equal(t, tt.reason, result.Admission.Reason(), "unresolved reservation exposes the explicit reconciliation reason instead of borrowing specialist authority")
				if tt.active {
					require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, dispatches: 1, pending: 1, parentStatus: models.SessionStatusIdle, threadStatus: models.ThreadStatusIdle}, sendPostgresReadGraph(t, f), "repair reconciliation creates durable due input but no job, lane claim, parent claim or execution counter change")
					var due *time.Time
					var consumer string
					var job *uuid.UUID
					var state string
					require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT next_attempt_at,consumer_kind,job_id,admission_state FROM session_input_dispatches WHERE org_id=$1 AND id=$2`, f.org, result.Admission.DispatchID()).Scan(&due, &consumer, &job, &state), "inspect the exact independently discoverable reconciliation dispatch")
					require.NotNil(t, due, "repair reconciliation must retain a finite due scan rather than silently drop input")
					require.Equal(t, []string{"none", "waiting"}, []string{consumer, state}, "unresolved reservation cannot be treated as an owned consumer")
					require.Nil(t, job, "waiting ordinary input must not adopt a specialist repair job")
					require.Empty(t, effects.jobs, "waiting cannot issue a speculative launch notification")
				} else {
					require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, dispatches: 1, continueJobs: 1, claims: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, sendPostgresReadGraph(t, f), "released history with no live consumer permits exactly one ordinary admission graph")
					require.Equal(t, 1, len(effects.jobs), "only the actual ordinary committed job may be notified")
				}
			}
			var retained struct {
				id, session, lane uuid.UUID
				active            bool
				head              string
			}
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id,session_id,thread_id,active,head_sha FROM pull_request_repair_runs WHERE org_id=$1 AND id=$2`, f.org, run.ID).Scan(&retained.id, &retained.session, &retained.lane, &retained.active, &retained.head), "reload the exact original repair reservation after ordinary handling")
			require.Equal(t, []uuid.UUID{run.ID, f.session, f.lanes[1]}, []uuid.UUID{retained.id, retained.session, retained.lane}, "ordinary admission cannot transfer reservation scope from the sibling")
			require.Equal(t, tt.active, retained.active, "ordinary admission cannot release or reactivate specialist ownership")
			require.Equal(t, run.HeadSHA, retained.head, "ordinary admission cannot rewrite retained historical head identity")
		})
	}
}

func TestThreadSendReplayOriginalReceiptAndConflictingPayloadPostgres(t *testing.T) {
	t.Parallel()
	f := newSendPostgresFixture(t, true, 1)
	service, effects := f.service(t, true)
	input := f.input(0, "synthetic-replay")
	accepted, err := service.SendMessage(context.Background(), input)
	require.NoError(t, err, "first actual service admission must commit before testing replay precedence")
	job, present := accepted.Admission.JobID()
	require.True(t, present, "idle admission must have an exact immutable job")
	sendPostgresExec(t, f, `UPDATE sessions SET admission_epoch=admission_epoch+1,end_epoch=admission_epoch+1,status='awaiting_input',automation_owner_generation_id=$3 WHERE org_id=$1 AND id=$2`, f.org, f.session, uuid.New())
	sendPostgresExec(t, f, `UPDATE session_threads SET admission_epoch=admission_epoch+1,end_epoch=admission_epoch+1,status='awaiting_input' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, input.ThreadID)
	sendPostgresExec(t, f, `UPDATE jobs SET run_at=run_at+interval '1 hour',target_node_id='synthetic-retarget' WHERE org_id=$1 AND id=$2`, f.org, job)
	before := sendPostgresReadGraph(t, f)
	replay, err := service.SendMessage(context.Background(), input)
	require.NoError(t, err, "accepted receipt must precede changed current epochs, ends, owners, status, schedule and target eligibility")
	require.Equal(t, db.MessageAdmissionReplayed, replay.Admission.Disposition(), "exact replay is not a second admission")
	require.Equal(t, accepted.Admission.Fence(), replay.Admission.Fence(), "replay must not recapture current epochs")
	require.Equal(t, accepted.Admission.DispatchOptions(), replay.Admission.DispatchOptions(), "mutable current backoff cannot replace original queue options")
	require.Equal(t, accepted.Admission.TargetNodeID(), replay.Admission.TargetNodeID(), "mutable current target cannot replace the retained original routing choice")
	originalHolder, _ := accepted.Admission.LeaseHolderID()
	replayedHolder, hasHolder := replay.Admission.LeaseHolderID()
	require.True(t, hasHolder, "original holder remains retained")
	require.Equal(t, originalHolder, replayedHolder, "new server preparation cannot replace the original holder")
	require.Equal(t, accepted.Message, replay.Message, "exact replay returns the same complete durable message")
	require.Nil(t, replay.ResolvedComments, "replay cannot authorize new audit work")
	require.Equal(t, before, sendPostgresReadGraph(t, f), "replay changes no accepted input job claim or counter")
	input.Message += " conflicting text"
	rejected, err := service.SendMessage(context.Background(), input)
	require.ErrorIs(t, err, db.ErrMessageAdmissionIdempotencyConflict, "same-key different semantic content must conflict before any new eligibility decision")
	require.Nil(t, rejected, "conflicting reuse cannot expose an accepted result")
	require.Equal(t, before, sendPostgresReadGraph(t, f), "conflicting payload must not mutate the original accepted graph")
	require.Equal(t, []int{1, 1}, []int{len(effects.lifecycle), len(effects.jobs)}, "receipt replay and conflict must never repeat original effect bundles or notifications")
}

func TestThreadSendWholeRollbackIncludesCommentsPostgres(t *testing.T) {
	t.Parallel()
	f := newSendPostgresFixture(t, true, 1)
	comment := seedSendPostgresComment(t, f)
	service, effects := f.service(t, true)
	input := f.input(0, "synthetic-late-failure")
	input.ResolveReviewCommentIDs = []uuid.UUID{comment}
	before := sendPostgresReadGraph(t, f)
	base := service.send.Transactions
	cause := errors.New("synthetic final-envelope persistence failure")
	var staged sendPostgresGraph
	var stagedComment bool
	service.send.Transactions = sendPostgresTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
		return base.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
			return work(ctx, sendPostgresLateFaultTx{Tx: tx, fixture: f, provisional: &staged, comment: &stagedComment, commentID: comment, cause: cause})
		})
	})
	result, err := service.SendMessage(context.Background(), input)
	require.ErrorIs(t, err, cause, "late failure must propagate only after actual whole rollback")
	require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "confirmed rollback of the exact callback error must not become ambiguous")
	require.Nil(t, result, "rolled-back admission exposes no message, receipt or comment result")
	require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, dispatches: 1, continueJobs: 1, claims: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, staged, "the injected failure must occur after every real provisional message/inbox/comment/dispatch/job/claim write")
	require.True(t, stagedComment, "the real comment must have been provisionally resolved before the late failure")
	require.Equal(t, before, sendPostgresReadGraph(t, f), "outer rollback must remove all provisional accepted-input rows and parent/lane projections")
	require.False(t, sendPostgresCommentResolved(t, f, comment), "comment resolution must roll back with its message and exact consumer")
	require.Empty(t, effects.lifecycle, "failed unit cannot publish effects")
	require.Empty(t, effects.jobs, "failed unit cannot notify a provisional job")
}

func TestThreadSendUnknownCommitExactResolutionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		commit bool
	}{
		{name: "successful commit with lost acknowledgement resolves positive exact receipt", commit: true},
		{name: "actual rollback with ambiguous acknowledgement and negative read stays unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, true, 1)
			service, effects := f.service(t, true)
			input := f.input(0, "synthetic-unknown")
			base := service.send.Transactions
			units := 0
			ack := errors.New("synthetic transaction acknowledgement loss")
			var originalIngress db.LifecycleIngress
			var staged sendPostgresGraph
			service.send.Transactions = sendPostgresTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				units++
				unit := units
				if unit == 1 {
					originalIngress = ctx.Value(sendIngressContextKey{}).(db.LifecycleIngress)
				}
				err := base.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
					if err := work(ctx, tx); err != nil {
						return err
					}
					if unit == 2 {
						var err error
						staged, err = readSendPostgresGraph(ctx, tx, f)
						if err != nil {
							return err
						}
						if !tt.commit {
							return ack
						}
					}
					return nil
				})
				if unit == 2 && tt.commit && err == nil {
					return ack
				}
				return err
			})
			result, err := service.SendMessage(context.Background(), input)
			acceptedGraph := sendPostgresGraph{messages: 1, inbox: 1, dispatches: 1, continueJobs: 1, claims: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}
			require.Equal(t, acceptedGraph, staged, "both acknowledgement cases must follow the complete real provisional admission, not a no-write stub")
			require.Equal(t, 3, units, "uncertainty permits exactly one nonmutating same-key observation, never a blind second mutation")
			if tt.commit {
				require.NoError(t, err, "only a positive exact durable receipt may resolve the simulated lost acknowledgement")
				require.Equal(t, db.MessageAdmissionReplayed, result.Admission.Disposition(), "confirmed exact receipt must return replay rather than duplicate work")
				require.Equal(t, originalIngress, result.Admission.Ingress(), "resolved receipt must retain original exact ingress")
				require.Equal(t, acceptedGraph, sendPostgresReadGraph(t, f), "positive resolution must leave exactly one message inbox dispatch consumer and lane claim")
			} else {
				var unknown *CommandOutcomeUnknownError
				require.ErrorAs(t, err, &unknown, "negative read cannot convert ambiguous prior completion into known abort")
				require.Equal(t, originalIngress, unknown.Ingress, "unknown preserves the exact original key/digest for caller reconciliation")
				require.Nil(t, result, "negative observation cannot expose successful acceptance")
				require.Equal(t, sendPostgresGraph{parentStatus: models.SessionStatusIdle, threadStatus: models.ThreadStatusIdle}, sendPostgresReadGraph(t, f), "the test's actual rollback must leave no accepted rows or compensation writes")
			}
			require.Empty(t, effects.lifecycle, "neither uncertain completion nor receipt observation may publish the original bundle")
			require.Empty(t, effects.jobs, "uncertainty resolution cannot notify an unconfirmed or duplicate job")
		})
	}
}

func TestThreadSendFrozenEpochBetweenObservationAndMutationPostgres(t *testing.T) {
	t.Parallel()
	f := newSendPostgresFixture(t, true, 1)
	service, effects := f.service(t, true)
	entered, released := make(chan struct{}), make(chan struct{})
	recorder := &sendPostgresRecorder{base: service.send.Transactions, fixture: f, afterEntry: func() error {
		close(entered)
		select {
		case <-released:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("synthetic epoch barrier was not released")
		}
	}}
	service.send.Transactions = recorder
	type outcome struct {
		result *SendMessageResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.SendMessage(context.Background(), f.input(0, "synthetic-stale-fence"))
		done <- outcome{result, err}
	}()
	select {
	case <-entered:
	case result := <-done:
		t.Fatalf("send ended before known entry observation: %v", result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out awaiting known completed entry observation")
	}
	sendPostgresExec(t, f, `UPDATE session_threads SET admission_epoch=admission_epoch+1 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.lanes[0])
	close(released)
	var result outcome
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out awaiting fixed-fence mutation result")
	}
	require.ErrorIs(t, result.err, db.ErrMessageAdmissionConflict, "mutating attempts must reject the originally captured epoch rather than adopt the intervening current one")
	require.Nil(t, result.result, "stale frozen command is not accepted work")
	require.Equal(t, sendPostgresGraph{parentStatus: models.SessionStatusIdle, threadStatus: models.ThreadStatusIdle}, sendPostgresReadGraph(t, f), "stale command must leave the accepted-input graph and execution counters unchanged")
	require.Empty(t, effects.lifecycle, "stale epoch cannot publish")
	require.Empty(t, effects.jobs, "stale epoch cannot notify a job")
}

func TestThreadSendRealInterlockAndParentContentionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		active, interlock bool
	}{
		{name: "legacy cutover interlock", interlock: true},
		{name: "active cutover interlock", active: true, interlock: true},
		{name: "legacy parent row"},
		{name: "active parent row", active: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, tt.active, 1)
			service, effects := f.service(t, true)
			before := sendPostgresReadGraph(t, f)
			// Baseline pool size two is intentional: one independent lock owner and
			// one real service connection suffice. No capacity/timeout relaxation.
			ctx, cancel := context.WithTimeout(context.Background(), db.LifecycleTransactionTimeout)
			defer cancel()
			owner, err := f.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			require.NoError(t, err, "hold an independent real transaction for the contention barrier")
			defer func() { _ = owner.Rollback(context.Background()) }()
			require.NoError(t, db.ConfigureLifecycleTransactionInTx(ctx, owner), "configure exact bounded barrier ownership")
			if tt.interlock {
				var locked bool
				require.NoError(t, owner.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text,0))`, "session_lifecycle_cutover:"+f.org.String()+":"+f.session.String()).Scan(&locked), "hold the exact real exclusive cutover interlock")
				require.True(t, locked, "fresh synthetic scope must have no other cutover holder")
			} else {
				var id uuid.UUID
				require.NoError(t, owner.QueryRow(ctx, `SELECT id FROM sessions WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE`, f.org, f.session).Scan(&id), "hold the exact parent row before service guard acquisition")
			}
			result, err := service.SendMessage(context.Background(), f.input(0, "synthetic-contention"))
			var exhausted *db.LifecycleContentionError
			require.ErrorAs(t, err, &exhausted, "actual cutover or row-lock contention must exhaust only confirmed-aborted attempts, never become status fallback or new acceptance")
			require.Equal(t, 3, exhausted.Attempts, "bounded runner keeps its production attempt policy")
			require.Nil(t, result, "contention must not expose a message receipt")
			require.Equal(t, before, sendPostgresReadGraph(t, f), "contention must preserve every accepted row and parent/lane counter")
			require.Empty(t, effects.lifecycle, "contended units publish no active effects")
			require.Empty(t, effects.jobs, "contended units notify no job")
			require.NoError(t, owner.Rollback(context.Background()), "explicit barrier owner releases its exact locks after the actual service result")
		})
	}
}
