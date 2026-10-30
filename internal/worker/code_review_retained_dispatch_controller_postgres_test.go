package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// These are real PostgreSQL controller/helper and thread-service verticals,
// not Worker.poll, provider execution, network loss, activation or I9 repair.
// ACK faults are injected only after an actual whole Run has settled. One
// observer permanently occupies one of exactly two pool slots; successful real
// SendMessage/Observe therefore also requires release of each controller unit.
type retainedControllerFixture struct {
	pool     *pgxpool.Pool
	f        strandedReviewFixture
	lane     *models.SessionThread
	result   models.CodeReviewAgentResult
	key      string
	command  threadsvc.CallerSendCommand
	wake     json.RawMessage
	base     threadsvc.SendTransactions
	deadline time.Time
}

func newRetainedControllerFixture(t *testing.T, pool *pgxpool.Pool) retainedControllerFixture {
	t.Helper()
	ctx := context.Background()
	f := seedStrandedReview(t, pool, "idle")
	terminalStore, err := db.NewCodeReviewStoreWithTerminalTransactions(pool, f.stores.ThreadSendDependencies.Transactions)
	require.NoError(t, err, "explicitly compose the unchanged canonical terminal metadata owner for real cancellation")
	f.stores.CodeReviews = terminalStore
	f.stores.ThreadInbox = db.NewThreadInboxStore(pool)
	f.job.TriggeringDisputeID = nil
	revision, err := json.Marshal(map[string]any{"repository_id": f.job.RepositoryID, "pull_request_id": f.job.PullRequestID, "head_sha": f.job.HeadSHA, "base_sha": "base", "pull_request_title": "Synthetic retained controller"})
	require.NoError(t, err, "retain realistic immutable code-review revision context, not a manual-origin ownership bypass")
	_, err = pool.Exec(ctx, `UPDATE sessions SET failure_explanation=NULL,revision_context=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID, revision)
	require.NoError(t, err, "fresh unowned code-review-origin parent must carry actual repository and revision data")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET phase='reviewing' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID)
	require.NoError(t, err, "the actual metadata owner must be eligible for reviewer dispatch")
	_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded',lock_token=NULL WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
	require.NoError(t, err, "retain nonempty original job history while allowing Prepare to insert its real original deduped wake")
	var enabled bool
	require.NoError(t, pool.QueryRow(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready RETURNING intents_enabled`).Scan(&enabled), "only the isolated full-schema test database is opted in after both real readiness gates")
	require.True(t, enabled, "a missing readiness row must not silently turn this into a legacy test")
	service, err := newWorkerThreadService(f.stores, zerolog.Nop())
	require.NoError(t, err, "construct the real worker thread service with its mandatory production dependencies")
	lane, err := service.CreateThread(ctx, threadsvc.CreateThreadInput{OrgID: f.job.OrgID, SessionID: f.job.SessionID, AgentType: "claude_code", Label: "Synthetic retained reviewer"})
	require.NoError(t, err, "create the real code-review-origin reviewer lane through unchanged ownership and capacity guards")
	resultID, key := uuid.New(), codeReviewReviewerKey(0, models.AgentTypeClaudeCode)
	structured, err := json.Marshal(codeReviewReviewerStructuredResult{ReviewerKey: key, ReviewerIndex: 0, ThreadID: lane.ID.String(), PromptRecordKey: "synthetic-retained-prompt", ReadOnly: true})
	require.NoError(t, err, "freeze the independently retained original reviewer identity and lane")
	input := threadsvc.SendMessageInput{OrgID: f.job.OrgID, SessionID: f.job.SessionID, ThreadID: lane.ID, Message: "Synthetic original reviewer command; do not re-render", MessageSource: models.SessionMessageSourceCodeReview, PlanMode: true, Images: []string{"synthetic-image"}, References: models.SessionInputReferences{{Kind: models.SessionInputReferenceKindFile, Path: "synthetic.go", Display: "original file"}}}
	continuation := "synthetic-retained-controller:" + resultID.String()
	input.ContinuationDedupeKeyOverride = &continuation
	command, err := threadsvc.NewCallerSendCommand(threadsvc.CallerSendCodeReviewReviewerDispatch, resultID, input)
	require.NoError(t, err, "freeze the full real codec command only after the original result and lane identities are fixed")
	original, err := json.Marshal(f.job)
	require.NoError(t, err, "encode original controller routing before adding opaque forward-compatible data")
	original = append(bytes.TrimSuffix(original, []byte("}")), []byte(`,"opaque_future":{"precise":9007199254740993,"max_int64":9223372036854775807,"original":"keep"}}`)...)
	wake, err := freezeCodeReviewReviewerDispatchWake(f.job, original)
	require.NoError(t, err, "the actual wake freezer must preserve original opaque payload rather than a current typed remarshal")
	f.job.originalPayload = bytes.Clone(original)
	canary := models.CodeReviewAgentResult{OrgID: f.job.OrgID, SessionID: f.job.SessionID, AgentProvider: "claude_code", Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted, RawOutput: new("Synthetic unrelated reviewer evidence"), StructuredResult: json.RawMessage(`{"reviewer_key":"synthetic-other-reviewer","read_only":true}`)}
	require.NoError(t, f.stores.CodeReviews.CreateAgentResult(ctx, &canary), "retain a nonempty same-session reviewer-key identity canary")
	return retainedControllerFixture{pool: pool, f: f, lane: lane, result: models.CodeReviewAgentResult{ID: resultID, OrgID: f.job.OrgID, SessionID: f.job.SessionID, AgentProvider: "claude_code", Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusQueued, StructuredResult: structured}, key: key, command: command, wake: wake, base: f.stores.ThreadSendDependencies.Transactions, deadline: time.Now().Add(time.Hour)}
}

func retainedControllerSnapshot(ctx context.Context, q terminalContentionRowReader, f retainedControllerFixture) (json.RawMessage, error) {
	graph, err := terminalContentionSnapshot(ctx, q, f.f)
	if err != nil {
		return nil, err
	}
	var all json.RawMessage
	err = q.QueryRow(ctx, `SELECT jsonb_build_object('graph',$2::jsonb,
 'repositories',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM repositories r WHERE org_id=$1),'[]'::jsonb),
 'pull_requests',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM pull_requests r WHERE org_id=$1),'[]'::jsonb),
 'automations',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM automations r WHERE org_id=$1),'[]'::jsonb),
 'targets',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM automation_targets r WHERE org_id=$1),'[]'::jsonb),
 'generations',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM automation_target_sessions r WHERE org_id=$1),'[]'::jsonb))`, f.f.job.OrgID, graph).Scan(&all)
	return all, err
}

func retainedControllerReadSnapshot(t *testing.T, q terminalContentionRowReader, f retainedControllerFixture) json.RawMessage {
	t.Helper()
	value, err := retainedControllerSnapshot(context.Background(), q, f)
	require.NoError(t, err, "independently inspect complete original rows, history, intent, wake, messages, inbox, stamps and ownership canaries")
	return value
}

func retainedControllerWithoutOriginalResult(t *testing.T, raw json.RawMessage, id uuid.UUID) map[string]json.RawMessage {
	t.Helper()
	var outer map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &outer), "decode complete synthetic graph without converting integers through float64")
	var graph map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(outer["graph"], &graph), "retain every exact original graph field outside the one permitted owner row")
	var results []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(graph["results"], &results), "retain all same-session reviewer identity canaries, not only row counts")
	kept := make([]map[string]json.RawMessage, 0, len(results))
	for _, row := range results {
		var found uuid.UUID
		require.NoError(t, json.Unmarshal(row["id"], &found), "read only the typed result identity while retaining every other raw field")
		if found != id {
			kept = append(kept, row)
		}
	}
	var err error
	graph["results"], err = json.Marshal(kept)
	require.NoError(t, err, "remove only the named original owner row from this permitted-delta comparison")
	outer["graph"], err = json.Marshal(graph)
	require.NoError(t, err, "preserve all non-target graph values for exact comparison")
	return outer
}

func retainedControllerAssertCanaries(t *testing.T, before, after json.RawMessage, f retainedControllerFixture, metadataChanged bool) {
	t.Helper()
	b, a := retainedControllerWithoutOriginalResult(t, before, f.result.ID), retainedControllerWithoutOriginalResult(t, after, f.result.ID)
	for _, key := range []string{"repositories", "pull_requests", "automations", "targets", "generations"} {
		require.Equal(t, b[key], a[key], "controller must preserve complete unrelated durable ownership and target rows: %s", key)
	}
	var bg, ag map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b["graph"], &bg), "read the complete baseline canary graph")
	require.NoError(t, json.Unmarshal(a["graph"], &ag), "read the complete settled canary graph")
	for _, key := range []string{"assessments", "schedule", "requests", "results", "findings", "prompts", "control"} {
		require.Equal(t, bg[key], ag[key], "controller receipt changes cannot mutate unrelated contribution, original outcome or scheduler evidence: %s", key)
	}
	if !metadataChanged {
		require.Equal(t, bg["metadata"], ag["metadata"], "admission settlement alone cannot change the complete review metadata outcome")
	}
}

type retainedControllerPhase struct {
	state                    string
	attempts, writer, stamps int
	attemptID                uuid.UUID
}

func readRetainedControllerPhase(ctx context.Context, q terminalContentionRowReader, f retainedControllerFixture) (retainedControllerPhase, error) {
	var v retainedControllerPhase
	err := q.QueryRow(ctx, `SELECT COALESCE((SELECT dispatch_state FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3),''),
 COALESCE((SELECT dispatch_attempt_seq FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3),0),
 COALESCE((SELECT dispatch_writer_seq FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3),0),
 (SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1 AND session_id=$2 AND owner_id=$3),
 COALESCE((SELECT dispatch_attempt_id FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3),'00000000-0000-0000-0000-000000000000'::uuid)`, f.f.job.OrgID, f.f.job.SessionID, f.result.ID).Scan(&v.state, &v.attempts, &v.writer, &v.stamps, &v.attemptID)
	return v, err
}

// This decorator never manufactures a commit/rollback, SQLSTATE, owner carrier,
// message or stamp. Its fault follows an actual complete callback; the real
// runner then commits, or rolls back the finite sentinel, before ACK injection.
type retainedControllerACKRun struct {
	base                       threadsvc.SendTransactions
	f                          retainedControllerFixture
	phase                      string
	rollback, unknown, faulted bool
	cause, ack, actual         error
	staged                     json.RawMessage
	provisional                retainedControllerPhase
	callbacks                  int
}

func (r *retainedControllerACKRun) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	matched := false
	r.actual = r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		matched = false
		before, err := readRetainedControllerPhase(unitCtx, tx, r.f)
		if err != nil {
			return err
		}
		if err := work(unitCtx, tx); err != nil {
			return err
		}
		after, err := readRetainedControllerPhase(unitCtx, tx, r.f)
		if err != nil {
			return err
		}
		switch r.phase {
		case "prepare":
			matched = before.state == "" && after.state == "pending"
		case "claim":
			matched = before.state == "pending" && after.state == "attempting"
		case "finalize":
			matched = before.state != "accepted" && after.state == "accepted"
		case "send":
			matched = before.stamps == 0 && after.stamps == 1
		}
		if !matched || r.faulted {
			matched = false
			return nil
		}
		r.callbacks++
		r.provisional = after
		r.staged, err = retainedControllerSnapshot(unitCtx, tx, r.f)
		if err != nil {
			return err
		}
		if r.rollback {
			return r.cause
		}
		return nil
	})
	if !matched {
		return r.actual
	}
	r.faulted = true
	if r.rollback {
		if r.actual != r.cause {
			return r.actual
		}
	} else if r.actual != nil {
		return r.actual
	}
	if r.unknown {
		return r.ack
	}
	return r.actual
}

type retainedControllerEffects struct {
	threadsvc.SendEffects
	jobs                       []uuid.UUID
	claims, pending, lifecycle int
}

func (e *retainedControllerEffects) PublishLifecycle(ctx context.Context, org uuid.UUID, effects []db.LifecycleEffect) {
	e.lifecycle++
	e.SendEffects.PublishLifecycle(ctx, org, effects)
}
func (e *retainedControllerEffects) PublishThreadClaim(ctx context.Context, org uuid.UUID, effect db.ThreadClaimPersistenceEffects) {
	e.claims++
	e.SendEffects.PublishThreadClaim(ctx, org, effect)
}
func (e *retainedControllerEffects) PublishThreadPending(ctx context.Context, org uuid.UUID, effect db.ThreadPendingMessagePersistenceEffects) {
	e.pending++
	e.SendEffects.PublishThreadPending(ctx, org, effect)
}
func (e *retainedControllerEffects) NotifyJob(ctx context.Context, job uuid.UUID) {
	e.jobs = append(e.jobs, job)
	e.SendEffects.NotifyJob(ctx, job)
}

func retainedControllerService(t *testing.T, f retainedControllerFixture, transactions threadsvc.SendTransactions) (*threadsvc.Service, *retainedControllerEffects) {
	t.Helper()
	dependencies := f.f.stores.ThreadSendDependencies
	dependencies.Transactions = transactions
	effects := &retainedControllerEffects{SendEffects: dependencies.Effects}
	dependencies.Effects = effects
	service, err := threadsvc.NewService(f.f.stores.SessionThreads, f.f.stores.Sessions, f.f.stores.SessionMessages, f.f.stores.SessionLogs, f.f.stores.Jobs, dependencies, zerolog.Nop())
	require.NoError(t, err, "decorate only real production send plumbing, not thread guards or persistence")
	service.SetThreadInboxStore(f.f.stores.ThreadInbox, f.pool)
	return service, effects
}

type retainedControllerSender struct {
	t             *testing.T
	f             retainedControllerFixture
	observer      *pgxpool.Conn
	actual        *threadsvc.Service
	calls         int
	claimAttempts []uuid.UUID
	last          *threadsvc.SendMessageResult
	lastErr       error
	unknown       bool
	before        func()
}

func (s *retainedControllerSender) SendMessage(ctx context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	s.calls++
	var state, key, digest string
	var attempt uuid.UUID
	var seq int
	require.NoError(s.t, s.observer.QueryRow(ctx, `SELECT dispatch_state,dispatch_key,dispatch_digest,dispatch_attempt_id,dispatch_attempt_seq FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3`, s.f.f.job.OrgID, s.f.f.job.SessionID, s.f.result.ID).Scan(&state, &key, &digest, &attempt, &seq), "an independent checkout must see the exact claim committed before the real sole SendMessage call")
	require.Equal(s.t, []string{"attempting", s.f.command.ClientMessageID(), s.f.command.Digest()}, []string{state, key, digest}, "only the committed original command claimant may reach the sender")
	require.NotEqual(s.t, uuid.Nil, attempt, "the known committed claim must carry a real new attempt identity")
	require.Equal(s.t, 1, seq, "the vertical must never consume another attempt before its original send")
	s.claimAttempts = append(s.claimAttempts, attempt)
	lock, err := s.observer.Begin(ctx)
	require.NoError(s.t, err, "inspect claim lock release using the already reserved independent connection")
	defer func() { _ = lock.Rollback(ctx) }()
	var id uuid.UUID
	require.NoError(s.t, lock.QueryRow(ctx, `SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND id=$2 FOR UPDATE NOWAIT`, s.f.f.job.OrgID, s.f.f.job.MetadataID).Scan(&id), "no metadata owner lock may cross the real thread-service call")
	require.NoError(s.t, lock.QueryRow(ctx, `SELECT id FROM code_review_agent_results WHERE org_id=$1 AND id=$2 FOR UPDATE NOWAIT`, s.f.f.job.OrgID, s.f.result.ID).Scan(&id), "the committed reviewer claim cannot retain its result lock across SendMessage")
	require.NoError(s.t, lock.Rollback(ctx), "release the independent lock probe before the real service acquires its unchanged parent/lane scope")
	want := s.f.command.SendInput()
	proof, err := s.f.command.Proof()
	require.NoError(s.t, err, "derive only the original immutable correlation transport")
	want.CallerCommand = proof
	require.Equal(s.t, want, input, "controller must restore every frozen original option and transport proof, not re-render or rotate key/lane")
	if s.before != nil {
		s.before()
	}
	s.last, s.lastErr = s.actual.SendMessage(ctx, input)
	if s.unknown && s.lastErr == nil {
		return nil, &threadsvc.CommandOutcomeUnknownError{ClientMessageID: input.ClientMessageID, DiagnosticID: uuid.New(), Cause: errors.New("Synthetic application ACK suppressed after actual send commit")}
	}
	return s.last, s.lastErr
}

func retainedControllerPrepare(t *testing.T, f retainedControllerFixture, runner threadsvc.SendTransactions) models.CodeReviewAgentResult {
	t.Helper()
	result, retained, err := prepareCodeReviewReviewerOwner(context.Background(), runner, f.f.job, f.key, &f.result, f.command, f.wake, true)
	require.NoError(t, err, "actual controller owner preparation must commit its result, complete intent and original wake together")
	require.True(t, retained, "the real enabled-control choice must install an S1 owner rather than silently choose legacy")
	require.Equal(t, f.result.ID, result.ID, "preparation must retain the independently preminted original result identity")
	return result
}

func retainedControllerAssertFrozen(t *testing.T, observer *pgxpool.Conn, f retainedControllerFixture) {
	t.Helper()
	var raw, wake json.RawMessage
	var digest string
	require.NoError(t, observer.QueryRow(context.Background(), `SELECT dispatch_input::text,dispatch_digest,dispatch_wake::text FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.f.job.OrgID, f.f.job.SessionID, f.result.ID).Scan(&raw, &digest, &wake), "read the actual immutable original input, digest and raw-controller wake after all owner transitions")
	command, err := threadsvc.DecodeCallerSendCommand(raw, digest, threadsvc.CallerSendOwnerScope{Purpose: threadsvc.CallerSendCodeReviewReviewerDispatch, OwnerID: f.result.ID, OrgID: f.f.job.OrgID, SessionID: f.f.job.SessionID, ThreadID: f.lane.ID, ClientMessageID: f.command.ClientMessageID()})
	require.NoError(t, err, "actual stored JSONB must decode under independent original scope and digest")
	expected, err := f.command.Encode()
	require.NoError(t, err, "retain the original canonical codec for exact comparison")
	actual, err := command.Encode()
	require.NoError(t, err, "canonicalize only the independently restored retained command")
	require.Equal(t, expected, actual, "all original effective semantic fields must survive controller state and contribution changes")
	var expectedWake string
	require.NoError(t, observer.QueryRow(context.Background(), `SELECT $1::jsonb::text`, string(f.wake)).Scan(&expectedWake), "derive the actual PostgreSQL JSONB presentation without float64 reconstruction")
	require.Equal(t, expectedWake, string(wake), "the full original wake including opaque maximal integers and unknown fields must be immutable")
	var payload, expectedPayload string
	var queue, kind, key string
	var priority, maxAttempts int
	require.NoError(t, observer.QueryRow(context.Background(), `SELECT queue,job_type,dedupe_key,priority,max_attempts,payload::text,($3::jsonb->'payload')::text FROM jobs WHERE org_id=$1 AND dedupe_key=$2 AND status='pending'`, f.f.job.OrgID, "code_review:"+f.f.job.OutputKey, string(f.wake)).Scan(&queue, &kind, &key, &priority, &maxAttempts, &payload, &expectedPayload), "the actual atomic Prepare must create the pending original controller wake beside the retained immutable intent")
	require.Equal(t, []string{"agent", "run_code_review", "code_review:" + f.f.job.OutputKey}, []string{queue, kind, key}, "the real wake must preserve only the approved original controller routing")
	require.Equal(t, []int{5, 8}, []int{priority, maxAttempts}, "the retained wake must preserve the unchanged original controller priority and attempt budget")
	require.Equal(t, expectedPayload, payload, "the actual pending job must preserve the complete original payload rather than a lossy typed remarshal")
}

func retainedControllerAssertAcceptedGraph(t *testing.T, observer *pgxpool.Conn, f retainedControllerFixture, message *models.SessionMessage) {
	t.Helper()
	require.NotNil(t, message, "real accepted graph must be backed by the actual service message, never a fabricated pointer")
	stored, err := db.NewSessionMessageStore(observer).GetByID(context.Background(), f.f.job.OrgID, message.ID)
	require.NoError(t, err, "independently read the whole actual original message after its real service commit")
	require.Equal(t, *message, stored, "the independent complete message must equal the actual service result")
	inbox, err := db.NewThreadInboxStore(observer).GetByClientMessageID(context.Background(), f.f.job.OrgID, f.lane.ID, f.command.ClientMessageID())
	require.NoError(t, err, "the original retained key must resolve to a real durable inbox row")
	require.Equal(t, []uuid.UUID{f.f.job.OrgID, f.f.job.SessionID, f.lane.ID}, []uuid.UUID{inbox.OrgID, inbox.SessionID, inbox.ThreadID}, "the actual inbox cannot borrow foreign tenant/session/lane identity")
	require.Equal(t, message.ID, inbox.MessageID, "the typed original inbox must name the exact actual message")
	var version int16
	var key, digest, purpose string
	var owner, lane, session uuid.UUID
	var messageID int64
	require.NoError(t, observer.QueryRow(context.Background(), `SELECT version,client_message_id,digest,purpose,owner_id,thread_id,session_id,message_id FROM caller_send_command_stamps WHERE org_id=$1 AND owner_id=$2`, f.f.job.OrgID, f.result.ID).Scan(&version, &key, &digest, &purpose, &owner, &lane, &session, &messageID), "observe the real same-send transaction stamp, not a synthetic acceptance record")
	require.Equal(t, int16(1), version, "actual stamp must retain the approved original codec version")
	require.Equal(t, []string{f.command.ClientMessageID(), f.command.Digest(), string(threadsvc.CallerSendCodeReviewReviewerDispatch)}, []string{key, digest, purpose}, "stamp must match the whole original owner/key/digest purpose tuple")
	require.Equal(t, []uuid.UUID{f.result.ID, f.lane.ID, f.f.job.SessionID}, []uuid.UUID{owner, lane, session}, "stamp identity must remain original and tenant-bound")
	require.Equal(t, message.ID, messageID, "the actual stamp may not point at an invented or sibling message")
	var counts [4]int
	require.NoError(t, observer.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2),(SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2),(SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1 AND session_id=$2),(SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='continue_session' AND payload->>'thread_id'=$3::uuid::text)`, f.f.job.OrgID, f.f.job.SessionID, f.lane.ID).Scan(&counts[0], &counts[1], &counts[2], &counts[3]), "count every real message/inbox/stamp/continuation tuple after full field-level correlation")
	require.Equal(t, [4]int{1, 1, 1, 1}, counts, "exactly one command may create one complete real legacy message/inbox/stamp/continuation graph")
}

func retainedControllerUnknownPreparation(t *testing.T, f, foreign retainedControllerFixture, observer *pgxpool.Conn, fault *retainedControllerACKRun, before, foreignBefore json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	service, effects := retainedControllerService(t, f, f.base)
	sender := &retainedControllerSender{t: t, f: f, observer: observer, actual: service}
	result, retained, prepErr := prepareCodeReviewReviewerOwner(ctx, fault, f.f.job, f.key, &f.result, f.command, f.wake, true)
	require.ErrorIs(t, prepErr, fault.ack, "unknown preparation acknowledgement must remain unknown after either real underlying settlement")
	require.Equal(t, models.CodeReviewAgentResult{}, result, "lost preparation acknowledgement cannot expose any provisional owner or successful result carrier")
	require.False(t, retained, "unknown preparation cannot expose a known committed S1 choice")
	require.True(t, fault.faulted, "application ACK suppression must follow the actual complete preparation phase, not an unrelated refusal")
	require.Equal(t, 1, fault.callbacks, "unknown preparation must not replay its result/intent/wake creation callback")
	require.Equal(t, retainedControllerPhase{state: "pending"}, fault.provisional, "the real provisional transaction must contain the original zero-attempt pending owner before ACK suppression")
	require.NotEmpty(t, fault.staged, "the decorator must observe the complete actual provisional preparation graph")
	require.NotEqual(t, before, fault.staged, "actual preparation must create its real result and wake before the real settlement is exercised")
	settled := retainedControllerReadSnapshot(t, observer, f)
	phase, err := readRetainedControllerPhase(ctx, observer, f)
	require.NoError(t, err, "independently read actual preparation settlement rather than infer it from the suppressed ACK")
	if fault.rollback {
		require.Same(t, fault.cause, fault.actual, "the unchanged real runner must confirm complete preparation rollback before its outward ACK is suppressed")
		require.Equal(t, retainedControllerPhase{}, phase, "real preparation rollback leaves no owner, attempt, writer sequence or acceptance stamp")
		require.Equal(t, before, settled, "actual preparation rollback must restore the entire original graph, including same-key canaries and job history")
	} else {
		require.NoError(t, fault.actual, "actual whole preparation commit must succeed before the application ACK is suppressed")
		require.Equal(t, retainedControllerPhase{state: "pending"}, phase, "committed lost-ACK preparation retains its actual original pending owner without authorizing send")
		require.Equal(t, fault.staged, settled, "independently committed preparation must equal the entire real provisional graph, not only row counts")
		retainedControllerAssertFrozen(t, observer, f)
		retainedControllerAssertCanaries(t, before, settled, f, false)
	}
	require.Equal(t, 0, sender.calls, "failed or unknown preparation acknowledgement must release zero real thread-service calls")
	require.Equal(t, [4]int{}, [4]int{len(effects.jobs), effects.claims, effects.pending, effects.lifecycle}, "no known send may escape preparation uncertainty as a job, claim, pending or lifecycle effect")
	var sends [4]int
	require.NoError(t, observer.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2),(SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2),(SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1 AND session_id=$2),(SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='continue_session' AND payload->>'thread_id'=$3::uuid::text)`, f.f.job.OrgID, f.f.job.SessionID, f.lane.ID).Scan(&sends[0], &sends[1], &sends[2], &sends[3]), "independently inspect every real send constituent before any known recovery claim")
	require.Equal(t, [4]int{}, sends, "neither real preparation outcome may create a message, inbox entry, stamp or continuation before recovery")
	require.Equal(t, foreignBefore, retainedControllerReadSnapshot(t, observer, foreign), "unknown preparation cannot mutate any foreign retained command, wake, history or business row")
	if fault.rollback {
		// Absence is not a receipt of a send abort. Here no claim or send was
		// ever released; this independent known preparation reuses the same
		// retained original candidate/command before its own positive claim.
		prepared := retainedControllerPrepare(t, f, f.base)
		require.Equal(t, f.result.ID, prepared.ID, "a separately acknowledged preparation must retain the original supplied candidate identity")
		require.Equal(t, f.result.OrgID, prepared.OrgID, "known preparation after actual rollback must keep the original tenant")
		require.Equal(t, f.result.SessionID, prepared.SessionID, "known preparation after actual rollback must keep the original review session")
	} else {
		var original models.CodeReviewAgentResult
		matches := 0
		for _, stored := range mustRetainedControllerResults(t, f) {
			if stored.ID == f.result.ID {
				original = stored
				matches++
			}
		}
		require.Equal(t, 1, matches, "the lost-ACK commit must leave exactly one independently readable original owner")
		// This replacement candidate is deliberately invalid and its command
		// is zero: exact metadata-locked identity adoption must occur before
		// validating, rendering, encoding or inserting a different candidate.
		changed := models.CodeReviewAgentResult{ID: uuid.New(), OrgID: uuid.New(), SessionID: uuid.New(), AgentProvider: "synthetic-invalid-recreated-provider", Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: json.RawMessage(`{`)}
		adopted, retained, adoptErr := prepareCodeReviewReviewerOwner(ctx, f.base, f.f.job, f.key, &changed, threadsvc.CallerSendCommand{}, nil, true)
		require.NoError(t, adoptErr, "a later actual metadata lock and exact-key reread must adopt the committed original before validating any changed candidate")
		require.True(t, retained, "the original committed S1 owner cannot become a legacy choice after lost preparation ACK")
		require.Equal(t, original, adopted, "identity recovery must preserve every original persisted result field, not only its key or ID")
		require.Equal(t, settled, retainedControllerReadSnapshot(t, observer, f), "exact-key adoption must preserve the entire committed graph without a new result, prompt, wake or owner mutation")
	}
	prepared := retainedControllerReadSnapshot(t, observer, f)
	retainedControllerAssertFrozen(t, observer, f)
	retainedControllerAssertCanaries(t, before, prepared, f, false)
	require.Equal(t, foreignBefore, retainedControllerReadSnapshot(t, observer, foreign), "independently known preparation or adoption must preserve the complete foreign graph")
	require.Equal(t, 0, sender.calls, "neither successful recovery preparation nor exact-key adoption is itself send authority")
	require.Equal(t, [4]int{}, [4]int{len(effects.jobs), effects.claims, effects.pending, effects.lifecycle}, "preparation/adoption must not release send effects before a known claim")
	require.NoError(t, dispatchRetainedCodeReviewReviewer(ctx, f.base, sender, f.f.job, f.result.ID, f.deadline), "only a later original pending read and known successful claim may authorize the first real send")
	require.Equal(t, 1, sender.calls, "recovery after either actual preparation outcome may release exactly one positively claimed first send")
	require.NoError(t, sender.lastErr, "the first authorized send must really commit through the unchanged service and router")
	accepted, err := readRetainedControllerPhase(ctx, observer, f)
	require.NoError(t, err, "read the real final owner independently after the first known-claim send")
	require.Equal(t, "accepted", accepted.state, "the independently known claim must settle the same original retained command")
	require.Equal(t, 1, accepted.attempts, "the uncertain preparation cannot consume or duplicate a dispatch attempt")
	require.Equal(t, 1, accepted.stamps, "the real first send must have exactly one original acceptance stamp")
	require.NotEqual(t, uuid.Nil, accepted.attemptID, "the first send requires a real independently committed claim identity")
	require.Equal(t, []uuid.UUID{accepted.attemptID}, sender.claimAttempts, "the independent sender probe must see only the exact known committed first claimant")
	retainedControllerAssertAcceptedGraph(t, observer, f, sender.last.Message)
	retainedControllerAssertFrozen(t, observer, f)
	after := retainedControllerReadSnapshot(t, observer, f)
	retainedControllerAssertCanaries(t, prepared, after, f, false)
	require.Equal(t, [4]int{1, 1, 0, 0}, [4]int{len(effects.jobs), effects.claims, effects.pending, effects.lifecycle}, "only the first actual committed send may release its existing job and claim effects")
	require.Equal(t, foreignBefore, retainedControllerReadSnapshot(t, observer, foreign), "first-send recovery cannot mutate any foreign original command or history")
	require.NoError(t, dispatchRetainedCodeReviewReviewer(ctx, f.base, sender, f.f.job, f.result.ID, f.deadline), "a later controller pass must adopt the settled original instead of replaying preparation or send")
	require.Equal(t, 1, sender.calls, "lost preparation acknowledgement must never lead to a second send after original acceptance")
	require.Equal(t, []uuid.UUID{accepted.attemptID}, sender.claimAttempts, "settled replay must not acquire or expose another committed send claimant")
	require.Equal(t, after, retainedControllerReadSnapshot(t, observer, f), "settled replay must retain every result, message, inbox, stamp, job and business field exactly")
	require.Equal(t, [4]int{1, 1, 0, 0}, [4]int{len(effects.jobs), effects.claims, effects.pending, effects.lifecycle}, "settled replay cannot republish any send effect")
	require.Equal(t, foreignBefore, retainedControllerReadSnapshot(t, observer, foreign), "settled replay cannot modify the complete independent foreign graph")
}

func TestRetainedReviewerControllerCommitAndACKPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, phase                                               string
		rollback, unknown, unknownSend, cancelBefore, cancelAfter bool
		owner                                                     string
	}{
		{name: "known claim commit before exactly one real send"},
		{name: "actual claim commit with lost application ACK sends zero", phase: "claim", unknown: true},
		{name: "actual claim rollback ACK sends zero until a new committed claim authorizes first send", phase: "claim", rollback: true, unknown: true},
		{name: "actual send commit with unknown result never resends", unknownSend: true},
		{name: "actual send rollback with unknown result never resends", phase: "send", rollback: true, unknown: true},
		{name: "late preparation error rolls back result intent and actual wake", phase: "prepare", rollback: true},
		{name: "actual preparation commit with lost application ACK adopts exact original before first send", phase: "prepare", unknown: true},
		{name: "actual preparation rollback with lost application ACK requires known preparation before first send", phase: "prepare", rollback: true, unknown: true},
		{name: "late finalization rollback cannot escape accepted owner", phase: "finalize", rollback: true, unknown: true},
		{name: "finalization commit ACK loss never releases another send", phase: "finalize", unknown: true},
		{name: "metadata cancellation commits before claim", cancelBefore: true},
		{name: "claim commits before metadata cancellation", cancelAfter: true},
		{name: "actual PR-owned session refuses unchanged admission", owner: "pr"},
		{name: "actual per-target-owned session refuses unchanged admission", owner: "target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "the controller vertical must not depend on hidden spare pool capacity")
			f, foreign := newRetainedControllerFixture(t, pool), newRetainedControllerFixture(t, pool)
			retainedControllerPrepare(t, foreign, foreign.base)
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the sole independent observer before any controller work")
			defer observer.Release()
			foreignBefore := retainedControllerReadSnapshot(t, observer, foreign)
			before := retainedControllerReadSnapshot(t, observer, f)
			fault := &retainedControllerACKRun{base: f.base, f: f, phase: tt.phase, rollback: tt.rollback, unknown: tt.unknown, cause: errors.New("Synthetic finite post-work rollback"), ack: errors.New("Synthetic unknown application dependency ACK")}
			ownerRunner := threadsvc.SendTransactions(f.base)
			if tt.phase != "" && tt.phase != "send" {
				ownerRunner = fault
			}
			if tt.phase == "prepare" {
				if tt.unknown {
					retainedControllerUnknownPreparation(t, f, foreign, observer, fault, before, foreignBefore)
					return
				}
				result, retained, prepErr := prepareCodeReviewReviewerOwner(ctx, ownerRunner, f.f.job, f.key, &f.result, f.command, f.wake, true)
				require.ErrorIs(t, prepErr, fault.cause, "late real preparation error must propagate after the existing runner rolls the whole unit back")
				require.Equal(t, models.CodeReviewAgentResult{}, result, "failed preparation cannot expose its provisional result identity or successful carrier")
				require.False(t, retained, "failed whole preparation cannot expose a committed S1 choice")
				require.Equal(t, 1, fault.callbacks, "fault must follow actual complete result/intent/wake work once, not a setup failure")
				require.Same(t, fault.cause, fault.actual, "the unchanged actual runner must confirm its whole rollback with the exact finite callback error")
				require.NotEqual(t, before, fault.staged, "the pre-rollback supplied transaction must genuinely contain the newly prepared graph")
				require.Equal(t, before, retainedControllerReadSnapshot(t, observer, f), "whole actual rollback must restore every original row and job-history field without leaked result, intent or wake")
				require.Equal(t, foreignBefore, retainedControllerReadSnapshot(t, observer, foreign), "failed Prepare may not change the nonempty foreign retained owner graph")
				return
			}
			retainedControllerPrepare(t, f, f.base)
			retainedControllerAssertFrozen(t, observer, f)
			prepared := retainedControllerReadSnapshot(t, observer, f)
			// Re-present a different freshly minted result candidate to the final
			// owner check: it must adopt the existing original, never install it.
			candidate := f.result
			candidate.ID = uuid.New()
			changedInput := f.command.SendInput()
			changedInput.ClientMessageID = ""
			changedInput.Message = "Synthetic newer render that must not replace the original"
			changedCommand, err := threadsvc.NewCallerSendCommand(threadsvc.CallerSendCodeReviewReviewerDispatch, candidate.ID, changedInput)
			require.NoError(t, err, "the losing new candidate must be internally valid so actual owner adoption, not input refusal, protects the original")
			adopted, retained, err := prepareCodeReviewReviewerOwner(ctx, f.base, f.f.job, f.key, &candidate, changedCommand, f.wake, true)
			require.NoError(t, err, "existing original reviewer-key ownership must be re-read after the actual metadata lock")
			require.True(t, retained, "existing intent-bearing owner cannot fall back to a fresh legacy choice")
			require.Equal(t, f.result.ID, adopted.ID, "the original result/key/lane must win over a current re-created candidate")
			require.Equal(t, prepared, retainedControllerReadSnapshot(t, observer, f), "owner adoption must not change any actual graph field or enqueue another wake")
			cancel := func() {
				_, err := f.f.stores.CodeReviews.CancelReview(ctx, f.f.job.OrgID, f.f.job.SessionID, "Synthetic exact controller cancellation")
				require.NoError(t, err, "real metadata cancellation must use its existing transaction owner and pending-only intent composition")
			}
			if tt.cancelBefore {
				cancel()
			}
			if tt.owner != "" {
				retainedControllerInstallOwner(t, f, tt.owner)
			}
			beforeDispatch := retainedControllerReadSnapshot(t, observer, f)
			sendRunner := threadsvc.SendTransactions(f.base)
			if tt.phase == "send" {
				sendRunner = fault
			}
			service, effects := retainedControllerService(t, f, sendRunner)
			sender := &retainedControllerSender{t: t, f: f, observer: observer, actual: service, unknown: tt.unknownSend}
			if tt.cancelAfter {
				sender.before = cancel
			}
			dispatchErr := dispatchRetainedCodeReviewReviewer(ctx, ownerRunner, sender, f.f.job, f.result.ID, f.deadline)
			if tt.phase == "claim" || tt.phase == "finalize" {
				require.Error(t, dispatchErr, "a failed or unconfirmed owner unit must not be reported as known settled")
			} else {
				require.NoError(t, dispatchErr, "the real controller must durably classify its exact send/cancellation/refusal without creating another command")
			}
			after := retainedControllerReadSnapshot(t, observer, f)
			phase, err := readRetainedControllerPhase(ctx, observer, f)
			require.NoError(t, err, "independently observe the real durable owner outcome")
			if tt.phase == "claim" || tt.cancelBefore {
				require.Equal(t, 0, sender.calls, "failed/unknown claim ACK or earlier metadata cancellation must release zero real SendMessage calls")
				require.Equal(t, 0, phase.stamps, "no-send branches may not mint a receipt-looking stamp")
				if tt.cancelBefore {
					require.Equal(t, "cancelled", phase.state, "committed metadata cancellation must settle only the original pending command")
				} else if tt.rollback {
					require.Equal(t, prepared, after, "real claim rollback must preserve the complete prepared graph and exact original pending state")
				} else {
					require.Equal(t, "attempting", phase.state, "actual committed lost-ACK claim must retain its original attempt rather than invent abort proof")
				}
			} else {
				require.Equal(t, 1, sender.calls, "one known committed original claim may make exactly one real thread-service call")
				switch {
				case tt.owner != "":
					require.Error(t, sender.lastErr, "actual durable ownership must be refused by the unchanged real thread service")
					require.Equal(t, "refused", phase.state, "positive ownership refusal is not an unknown or alternate send")
					require.Equal(t, 0, phase.stamps, "an ownership descriptor must never bypass PR/per-target admission guards")
				case tt.phase == "send":
					require.ErrorIs(t, sender.lastErr, threadsvc.ErrCommandOutcomeUnknown, "real send rollback with unconfirmed application ACK must retain unknown precedence")
					require.Equal(t, "unknown", phase.state, "absence after unknown send is never permission to return to pending")
					require.Equal(t, 0, phase.stamps, "actual send rollback must not leak its provisional stamp")
				case tt.unknownSend:
					require.NoError(t, sender.lastErr, "underlying actual send must really commit before the application ACK is suppressed")
					require.Equal(t, "unknown", phase.state, "committed unknown send must remain independently unresolved until due observation")
					retainedControllerAssertAcceptedGraph(t, observer, f, sender.last.Message)
				case tt.phase == "finalize" && tt.rollback:
					require.Equal(t, "attempting", phase.state, "actual finalization rollback must retain the exact original attempting tuple, not provisional acceptance")
					retainedControllerAssertAcceptedGraph(t, observer, f, sender.last.Message)
				default:
					require.Equal(t, "accepted", phase.state, "known send and committed settlement must produce real original receipt acceptance")
					retainedControllerAssertAcceptedGraph(t, observer, f, sender.last.Message)
				}
			}
			if tt.phase != "" {
				require.True(t, fault.faulted, "the finite fault must reach the intended actual SQL transition instead of an unrelated early refusal")
				require.Equal(t, 1, fault.callbacks, "fault injection must not replay a side-effecting unit")
				require.NotEmpty(t, fault.staged, "the complete provisional graph must be observed before fault settlement")
				if tt.rollback {
					require.Same(t, fault.cause, fault.actual, "real whole rollback must return the original finite callback sentinel, not an uncertain joined cleanup failure")
				} else {
					require.NoError(t, fault.actual, "actual whole commit must complete before the application ACK is suppressed")
				}
			}
			if tt.owner != "" || tt.phase == "claim" || tt.phase == "send" || tt.cancelBefore {
				require.Equal(t, retainedControllerWithoutOriginalResult(t, beforeDispatch, f.result.ID), retainedControllerWithoutOriginalResult(t, after, f.result.ID), "no-send or actual send rollback may change only the original intent; every same-key canary, session, lane, job, message, inbox, stamp and ownership row must remain exact")
			}
			if tt.phase == "finalize" && tt.rollback {
				require.Equal(t, retainedControllerWithoutOriginalResult(t, fault.staged, f.result.ID), retainedControllerWithoutOriginalResult(t, after, f.result.ID), "late whole finalization rollback must leak no metadata, history, business or extra send-graph mutation beyond the separately committed original send")
			}
			if tt.owner != "" || tt.phase == "claim" || tt.phase == "send" || tt.cancelBefore {
				require.Equal(t, [4]int{}, [4]int{len(effects.jobs), effects.claims, effects.pending, effects.lifecycle}, "no accepted real send means no escaped job/claim/pending/lifecycle effects")
			} else {
				require.Equal(t, [4]int{1, 1, 0, 0}, [4]int{len(effects.jobs), effects.claims, effects.pending, effects.lifecycle}, "the one known actual legacy send releases only its existing one job/claim effect, never an active receipt")
			}
			retainedControllerAssertFrozen(t, observer, f)
			retainedControllerAssertCanaries(t, beforeDispatch, after, f, tt.cancelAfter)
			require.Equal(t, foreignBefore, retainedControllerReadSnapshot(t, observer, foreign), "all actual foreign intent/wake/history/business rows must remain byte-exact")
			if tt.phase == "claim" && tt.rollback {
				// The failed invocation released no send and its whole durable
				// graph is still exactly pending, as asserted above. A later fresh
				// positively committed claim is new authority for the FIRST call,
				// never permission inherited from the unacknowledged provisional
				// claimant or from absence of a message/stamp.
				require.Equal(t, retainedControllerPhase{state: "pending"}, phase, "actual rollback must retain the complete original zero-attempt pending tuple before another claim is attempted")
				require.Equal(t, "attempting", fault.provisional.state, "the lost ACK must follow real provisional claim SQL, not refusal or a missing owner")
				require.Equal(t, 1, fault.provisional.attempts, "the rolled-back provisional claim must have installed exactly its own first attempt")
				require.NotEqual(t, uuid.Nil, fault.provisional.attemptID, "retain the actual rolled-back provisional attempt solely as a negative test canary")
				require.NoError(t, dispatchRetainedCodeReviewReviewer(ctx, f.base, sender, f.f.job, f.result.ID, f.deadline), "a separate exact pending reread and new known-committed claim may authorize the first real send")
				require.Equal(t, 1, sender.calls, "the second invocation may send once only after its own known successful claim commit")
				recoveredPhase, err := readRetainedControllerPhase(ctx, observer, f)
				require.NoError(t, err, "read the actual independently committed new claimant and final settlement")
				freshAttempt := recoveredPhase.attemptID
				require.NotEqual(t, uuid.Nil, freshAttempt, "the independently committed recovered owner must retain a real positive attempt identity")
				require.Equal(t, []uuid.UUID{freshAttempt}, sender.claimAttempts, "the independent sender probe must have observed exactly the one genuinely committed recovered claimant")
				require.NotEqual(t, fault.provisional.attemptID, freshAttempt, "new known-commit authority must use a fresh attempt, never replay the rolled-back provisional claimant")
				require.Equal(t, "accepted", recoveredPhase.state, "the first actual send must settle the original retained command, not allocate a replacement result")
				require.Equal(t, 1, recoveredPhase.attempts, "rolled-back provisional sequence must not be counted as a durable consumed attempt")
				require.Equal(t, freshAttempt, recoveredPhase.attemptID, "accepted owner must retain exactly the new positively acknowledged claim identity observed before SendMessage")
				retainedControllerAssertAcceptedGraph(t, observer, f, sender.last.Message)
				retainedControllerAssertFrozen(t, observer, f)
				recovered := retainedControllerReadSnapshot(t, observer, f)
				retainedControllerAssertCanaries(t, beforeDispatch, recovered, f, false)
				require.Equal(t, [4]int{1, 1, 0, 0}, [4]int{len(effects.jobs), effects.claims, effects.pending, effects.lifecycle}, "only the separately committed first real send may release its unchanged postcommit effects")
				require.NoError(t, dispatchRetainedCodeReviewReviewer(ctx, f.base, sender, f.f.job, f.result.ID, f.deadline), "a third pass must adopt the original accepted owner rather than send again")
				require.Equal(t, 1, sender.calls, "positive new claim after actual rollback authorizes send one, never send two")
				require.Equal(t, []uuid.UUID{freshAttempt}, sender.claimAttempts, "the accepted original command may never acquire or expose a second committed send claimant")
				require.Equal(t, recovered, retainedControllerReadSnapshot(t, observer, f), "third-pass adoption must preserve every complete result, message, inbox, stamp, job and original business field")
				require.Equal(t, foreignBefore, retainedControllerReadSnapshot(t, observer, foreign), "recovery from actual claim rollback cannot mutate any foreign original command or history")
			} else {
				require.NoError(t, dispatchRetainedCodeReviewReviewer(ctx, f.base, sender, f.f.job, f.result.ID, f.deadline), "a second controller pass must only reconcile the retained state, never replay an unknown or terminal command")
				next := retainedControllerReadSnapshot(t, observer, f)
				// If ordinary wall-clock time has made the row due, legal stale or
				// observation settlement may change only this original owner row.
				// No assumption that a loaded runner finishes within five seconds
				// is needed to prove the strict no-resend property.
				require.Equal(t, retainedControllerWithoutOriginalResult(t, after, f.result.ID), retainedControllerWithoutOriginalResult(t, next, f.result.ID), "another controller pass may only settle the original receipt tuple, never create a new message/job or mutate another business row")
				if phase.state == "accepted" || phase.state == "refused" || phase.state == "cancelled" {
					require.Equal(t, after, next, "settled historical owner must remain completely unchanged on replay")
				}
				expected := 1
				if tt.phase == "claim" || tt.cancelBefore {
					expected = 0
				}
				require.Equal(t, expected, sender.calls, "a lost claim/send/settlement ACK cannot release another original send")
			}
			if tt.cancelAfter {
				var status models.CodeReviewSessionStatus
				require.NoError(t, observer.QueryRow(ctx, `SELECT status FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, f.f.job.OrgID, f.f.job.MetadataID).Scan(&status), "read the independently cancelled metadata owner after late positive acceptance")
				require.Equal(t, models.CodeReviewSessionStatusCancelled, status, "claim-first acceptance may repair only its own receipt, never undo the later metadata cancellation")
			}
		})
	}
}

func retainedControllerInstallOwner(t *testing.T, f retainedControllerFixture, kind string) {
	t.Helper()
	ctx := context.Background()
	if kind == "pr" {
		_, err := f.pool.Exec(ctx, `UPDATE sessions SET code_review_owner_pr_id=$3 WHERE org_id=$1 AND id=$2`, f.f.job.OrgID, f.f.job.SessionID, f.f.job.PullRequestID)
		require.NoError(t, err, "install a real same-tenant PR ownership pointer as the admission negative control")
		return
	}
	automation, target, generation := uuid.New(), uuid.New(), uuid.New()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic retained target','Synthetic target-owned negative control')`, []any{automation, f.f.job.OrgID}},
		{`INSERT INTO automation_targets(id,org_id,automation_id,repository_id,target_kind,target_key) VALUES($1,$2,$3,$4,'github_pull_request','9')`, []any{target, f.f.job.OrgID, automation, f.f.job.RepositoryID}},
		{`INSERT INTO automation_target_sessions(id,org_id,target_id,generation,session_id) VALUES($1,$2,$3,1,$4)`, []any{generation, f.f.job.OrgID, target, f.f.job.SessionID}},
		{`UPDATE sessions SET automation_owner_generation_id=$3 WHERE org_id=$1 AND id=$2`, []any{f.f.job.OrgID, f.f.job.SessionID, generation}},
	} {
		_, err := f.pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "install a real durable target/generation graph, not a dangling fake ownership marker")
	}
}

// Historical rows below are legal fixture transitions under the real schema,
// not claimed execution of the production Claim helper. They make the approved
// due-time observer path deterministic without sleep, clock overrides or
// shortened production delays. The positive message/stamp is still created by
// the actual thread service, not fabricated in fixture SQL.
func TestRetainedReviewerControllerDueObservationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		stale bool
	}{
		{name: "due terminal timed-out owner repairs only original acceptance"},
		{name: "newer actual writer tuple defeats stale positive observer CAS", stale: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			f, foreign := newRetainedControllerFixture(t, pool), newRetainedControllerFixture(t, pool)
			retainedControllerPrepare(t, f, f.base)
			retainedControllerPrepare(t, foreign, foreign.base)
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "hold an independent observer so the actual observe/settle vertical has only one application checkout")
			defer observer.Release()
			foreignBefore := retainedControllerReadSnapshot(t, observer, foreign)
			attempt := uuid.New()
			require.NoError(t, f.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(unitCtx, `UPDATE code_review_agent_results SET dispatch_state='attempting',dispatch_attempt_id=$4,dispatch_attempt_seq=1,dispatch_attempted_at=statement_timestamp()-interval '1 minute',dispatch_due_at=statement_timestamp()-interval '1 second',dispatch_writer_seq=1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3 AND dispatch_state='pending'`, f.f.job.OrgID, f.f.job.SessionID, f.result.ID, attempt)
				return err
			}), "commit a legal historical attempting fixture through the real unchanged bounded owner")
			service, effects := retainedControllerService(t, f, f.base)
			input := f.command.SendInput()
			input.CallerCommand, err = f.command.Proof()
			require.NoError(t, err, "use only the original valid caller correlation proof")
			sent, err := service.SendMessage(ctx, input)
			require.NoError(t, err, "establish positive durable message/inbox/stamp evidence through the actual unchanged router and real admission")
			require.NotNil(t, sent, "the positive observer fixture must have actual thread-service acceptance")
			require.NotNil(t, sent.Message, "no fabricated message ID may stand in for the actual accepted command")
			require.NoError(t, f.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(unitCtx, `UPDATE code_review_agent_results SET dispatch_state='unknown',dispatch_due_at=statement_timestamp()-interval '1 second',dispatch_observe_deadline=dispatch_attempted_at+interval '15 minutes',dispatch_detail='unknown_ack',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3 AND dispatch_state='attempting' AND dispatch_attempt_id=$4`, f.f.job.OrgID, f.f.job.SessionID, f.result.ID, attempt)
				if err != nil {
					return err
				}
				closed, closeErr := db.CloseCodeReviewReviewerDispatchDeadlineInTx(unitCtx, f.f.job.OrgID, tx, codeReviewDispatchScope(f.f.job), f.result.ID, nil, f.result.StructuredResult, time.Now().Add(-time.Hour))
				if closeErr != nil {
					return closeErr
				}
				if !closed {
					return errors.New("Synthetic actual contribution deadline did not close")
				}
				_, err = db.CancelCodeReviewInTx(unitCtx, f.f.job.OrgID, tx, f.f.job.SessionID, "Synthetic later owner cancellation with unresolved admission")
				return err
			}), "close the actual contribution at a passed deadline and terminalize metadata without declaring its unknown send aborted")
			before := retainedControllerReadSnapshot(t, observer, f)
			var resultBefore models.CodeReviewAgentResult
			for _, result := range mustRetainedControllerResults(t, f) {
				if result.ID == f.result.ID {
					resultBefore = result
				}
			}
			require.Equal(t, models.CodeReviewAgentResultStatusTimedOut, resultBefore.Status, "positive late acceptance must begin after real aware contribution closure, not merely after a transport error")
			runner := threadsvc.SendTransactions(f.base)
			stale := &retainedControllerObserverRace{base: f.base, f: f}
			if tt.stale {
				runner = stale
			}
			// Calling the actual controller reconciler proves bounded retained-row
			// enumeration and terminal-owner observation, not only primitive CAS.
			f.f.stores.ThreadSendDependencies.Transactions = runner
			retained, err := reconcileRetainedCodeReviewDispatches(ctx, f.f.stores, zerolog.Nop(), f.f.job, codeReviewObserveRetainedOnly)
			require.NoError(t, err, "actual terminal controller reconciliation must observe retained original evidence without a send or provider dependency")
			require.True(t, retained, "the actual early selector must retain terminal original ownership without authorizing a new pending send")
			phase, err := readRetainedControllerPhase(ctx, observer, f)
			require.NoError(t, err, "read the independently committed observation settlement")
			if tt.stale {
				require.True(t, stale.changed, "the test must commit a real later writer after the sealed positive observation's read-only unit settles")
				require.Equal(t, "unknown", phase.state, "a stale positive candidate cannot overwrite the newer actual writer tuple")
				require.Equal(t, stale.after, retainedControllerReadSnapshot(t, observer, f), "zero-row stale settlement must leave the entire newer graph byte-exact")
			} else {
				require.Equal(t, "accepted", phase.state, "the actual due observer/controller must settle the exact original real stamped message")
				retainedControllerAssertAcceptedGraph(t, observer, f, sent.Message)
				require.NotEqual(t, before, retainedControllerReadSnapshot(t, observer, f), "the positive controller must really repair the intent rather than short-circuit on terminal metadata")
			}
			for _, result := range mustRetainedControllerResults(t, f) {
				if result.ID == f.result.ID {
					require.Equal(t, resultBefore, result, "late accepted linkage must preserve the complete closed timed-out contribution, not upgrade result/quorum/output")
				}
			}
			retainedControllerAssertFrozen(t, observer, f)
			require.Equal(t, foreignBefore, retainedControllerReadSnapshot(t, observer, foreign), "terminal observe-only recovery cannot mutate any foreign immutable command, result, wake or history")
			require.Equal(t, [4]int{1, 1, 0, 0}, [4]int{len(effects.jobs), effects.claims, effects.pending, effects.lifecycle}, "the seeded actual original send is the only effect; observation and separate CAS cannot republish or dispatch")
		})
	}
}

func mustRetainedControllerResults(t *testing.T, f retainedControllerFixture) []models.CodeReviewAgentResult {
	t.Helper()
	results, err := f.f.stores.CodeReviews.ListAgentResults(context.Background(), f.f.job.OrgID, f.f.job.SessionID)
	require.NoError(t, err, "independently read complete actual public contribution fields separately from private receipt state")
	return results
}

type retainedControllerObserverRace struct {
	base    threadsvc.SendTransactions
	f       retainedControllerFixture
	changed bool
	after   json.RawMessage
}

func (r *retainedControllerObserverRace) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	readonly := false
	err := r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		readonly = false
		if err := work(unitCtx, tx); err != nil {
			return err
		}
		var setting string
		if err := tx.QueryRow(unitCtx, `SHOW transaction_read_only`).Scan(&setting); err != nil {
			return err
		}
		readonly = setting == "on"
		return nil
	})
	if err != nil || !readonly || r.changed {
		return err
	}
	// The real read-only observation has committed and released its checkout.
	// Advance only the exact legal writer tuple in a new actual bounded unit.
	err = r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(unitCtx, `UPDATE code_review_agent_results SET dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3 AND dispatch_state='unknown'`, r.f.f.job.OrgID, r.f.f.job.SessionID, r.f.result.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("Synthetic concurrent current-owner writer did not match")
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.changed = true
	err = r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		var err error
		r.after, err = retainedControllerSnapshot(unitCtx, tx, r.f)
		return err
	})
	return err
}
