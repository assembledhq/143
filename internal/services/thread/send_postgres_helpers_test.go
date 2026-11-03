package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// These helpers install only synthetic full-schema test data. The hand-built
// normalization/capability envelopes mirror the frozen G.1 fixture shapes; they
// are not an activation owner, provider conformance, or operational procedure.
type sendPostgresFixture struct {
	pool                          *pgxpool.Pool
	org, session, user, changeset uuid.UUID
	lanes                         []uuid.UUID
}

func newSendPostgresFixture(t *testing.T, active bool, laneCount int) sendPostgresFixture {
	t.Helper()
	return newSendPostgresFixtureWithMaxConns(t, active, laneCount, 2)
}

func newSendPostgresFixtureWithMaxConns(t *testing.T, active bool, laneCount int, maxConns int32) sendPostgresFixture {
	t.Helper()
	pool := testpostgres.NewPool(t, context.Background(), "../../../migrations", maxConns)
	f := sendPostgresFixture{pool: pool, org: uuid.New(), user: uuid.New()}
	err := db.RunLifecycleTransaction(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic service admission tenant')`, f.org); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO users(id,org_id,email,name,role) VALUES($1,$2,$3,'Synthetic service admission actor','member')`, f.user, f.org, f.user.String()+"@example.invalid"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organization_memberships(user_id,org_id,role) VALUES($1,$2,'member') ON CONFLICT(user_id,org_id) DO UPDATE SET role='member'`, f.user, f.org); err != nil {
			return err
		}
		session := models.Session{OrgID: f.org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
		if err := db.NewSessionStore(tx).CreateInTx(ctx, tx, &session); err != nil {
			return err
		}
		if session.PrimaryThreadID == nil {
			return errors.New("real session creation returned no primary lane")
		}
		f.session = session.ID
		f.lanes = []uuid.UUID{*session.PrimaryThreadID}
		for i := 1; i < laneCount; i++ {
			lane := models.SessionThread{OrgID: f.org, SessionID: f.session, AgentType: session.AgentType, Label: fmt.Sprintf("Synthetic service lane %d", i), Status: models.ThreadStatusIdle}
			if err := db.NewSessionThreadStore(tx).Create(ctx, &lane, models.MaxThreadsPerSession); err != nil {
				return err
			}
			f.lanes = append(f.lanes, lane.ID)
		}
		var err error
		f.changeset, err = db.NewSessionStore(tx).GetPrimaryChangesetID(ctx, f.org, f.session)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		var exclusive bool
		key := "session_lifecycle_cutover:" + f.org.String() + ":" + f.session.String()
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text,0))`, key).Scan(&exclusive); err != nil {
			return err
		}
		if !exclusive {
			return errors.New("synthetic fixture cutover interlock was unexpectedly busy")
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET admission_epoch=3,end_epoch=NULL,lifecycle_version=1 WHERE org_id=$1 AND id=$2`, f.org, f.session); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE session_threads SET admission_epoch=5,end_epoch=NULL,lifecycle_version=1 WHERE org_id=$1 AND session_id=$2 AND id=ANY($3::uuid[])`, f.org, f.session, f.lanes); err != nil {
			return err
		}
		normalization, err := json.Marshal(map[string]any{"schema_version": 1, "receipt_id": uuid.New(), "floor_receipt_id": uuid.New(), "cutoff_id": 1, "complete": true})
		if err != nil {
			return err
		}
		capability, err := json.Marshal(map[string]any{"schema_version": 1, "release_receipt_id": uuid.New(), "tuples": []map[string]any{{"provider": string(session.AgentType), "cli_build": "synthetic-cli-v1", "image_build": "synthetic-image-v1", "resume_identity": "native_conversation", "verification_protocol": "synthetic-verify-v1", "conformance_receipt_id": uuid.New()}}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO session_lifecycle_cutovers(org_id,session_id,version,state,legacy_input_cutoff_id,normalization,activated_at,minimum_writer_version,resumability_capability) VALUES($1,$2,3,'active',1,$3,$4,1,$5)`, f.org, f.session, normalization, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), capability)
		return err
	})
	require.NoError(t, err, "install the synthetic tenant, real session/lanes, and optional complete G.1 fixture in one bounded real transaction")
	return f
}

func (f sendPostgresFixture) input(lane int, key string) SendMessageInput {
	return SendMessageInput{OrgID: f.org, SessionID: f.session, ThreadID: f.lanes[lane], ChangesetID: &f.changeset, UserID: &f.user, ClientMessageID: key, Message: "Synthetic follow-up", PlanMode: true, MessageSource: models.SessionMessageSourceAgentTool,
		Images: []string{"synthetic-image"}, References: models.SessionInputReferences{{Kind: models.SessionInputReferenceKindFile, Path: "main.go", Display: "main.go"}}, Commands: models.SessionInputCommands{{Kind: "command", AgentType: models.DefaultDefaultAgentType, Name: "test", Token: "/test", Display: "/test", Source: models.SessionInputCommandSourceBuiltin}}, PostSuccessAction: "push_pr_changes", PostSuccessAuthorMode: "user"}
}

type sendPostgresEffects struct {
	SendEffects
	lifecycle                                   [][]db.LifecycleEffect
	jobs                                        []uuid.UUID
	claims, pending, threadStatus, parentStatus int
}

// Concrete publishers are deliberately configured without a Redis transport:
// this suite proves SQL/commit ownership and publisher invocation, not delivery
// to an external stream. Opaque publisher payloads have separate CPU coverage.

func (e *sendPostgresEffects) PublishLifecycle(ctx context.Context, org uuid.UUID, effects []db.LifecycleEffect) {
	e.lifecycle = append(e.lifecycle, append([]db.LifecycleEffect(nil), effects...))
	e.SendEffects.PublishLifecycle(ctx, org, effects)
}
func (e *sendPostgresEffects) NotifyJob(ctx context.Context, id uuid.UUID) {
	e.jobs = append(e.jobs, id)
	e.SendEffects.NotifyJob(ctx, id)
}
func (e *sendPostgresEffects) PublishThreadClaim(ctx context.Context, org uuid.UUID, effect db.ThreadClaimPersistenceEffects) {
	e.claims++
	e.SendEffects.PublishThreadClaim(ctx, org, effect)
}
func (e *sendPostgresEffects) PublishThreadPending(ctx context.Context, org uuid.UUID, effect db.ThreadPendingMessagePersistenceEffects) {
	e.pending++
	e.SendEffects.PublishThreadPending(ctx, org, effect)
}

// This wraps real production persistence and publishers only to count caller
// invocations. No authority, mode, SQL, transaction or guard is substituted.
func (f sendPostgresFixture) service(t *testing.T, grouped bool) (*Service, *sendPostgresEffects) {
	t.Helper()
	sessions, threads, jobs := db.NewSessionStore(f.pool), db.NewSessionThreadStore(f.pool), db.NewJobStore(f.pool)
	dependencies, err := NewSendDependencies(f.pool, sessions, threads, jobs)
	require.NoError(t, err, "construct the real pool-owned transaction runner and supplied-tx persistence")
	effects := &sendPostgresEffects{SendEffects: dependencies.Effects}
	dependencies.Effects = effects
	service, err := NewService(threads, sessions, db.NewSessionMessageStore(f.pool), db.NewSessionLogStore(f.pool), jobs, dependencies, zerolog.Nop())
	require.NoError(t, err, "construct actual production thread service with mandatory send plumbing")
	if grouped {
		service.SetThreadInboxStore(db.NewThreadInboxStore(f.pool), f.pool)
		service.SetReviewCommentResolver(f.pool, db.NewSessionReviewCommentStore(f.pool))
	} else {
		service.SetThreadInboxStore(db.NewThreadInboxStore(f.pool))
	}
	return service, effects
}

func sendPostgresExec(t *testing.T, f sendPostgresFixture, query string, args ...any) {
	t.Helper()
	require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, query, args...); return err }), "synthetic fixture mutation must use configured real transaction ownership")
}

type sendPostgresGraph struct {
	messages, inbox, dispatches, continueJobs, liveJobs, claims, pending int64
	parentTurn, threadTurn                                               int
	workspaceGeneration                                                  int64
	parentStatus                                                         models.SessionStatus
	threadStatus                                                         models.ThreadStatus
}

func readSendPostgresGraph(ctx context.Context, q db.DBTX, f sendPostgresFixture) (sendPostgresGraph, error) {
	var v sendPostgresGraph
	err := q.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='continue_session'),
 (SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='deliver_thread_inbox'),
 (SELECT count(*) FROM session_threads WHERE org_id=$1 AND session_id=$2 AND admission_claim_dispatch_id IS NOT NULL),
 (SELECT COALESCE(sum(pending_message_count),0) FROM session_threads WHERE org_id=$1 AND session_id=$2),
 s.current_turn,t.current_turn,s.workspace_generation,s.status,t.status
 FROM sessions s JOIN session_threads t ON t.org_id=s.org_id AND t.session_id=s.id
 WHERE s.org_id=$1 AND s.id=$2 AND t.id=$3`, f.org, f.session, f.lanes[0]).Scan(&v.messages, &v.inbox, &v.dispatches, &v.continueJobs, &v.liveJobs, &v.claims, &v.pending, &v.parentTurn, &v.threadTurn, &v.workspaceGeneration, &v.parentStatus, &v.threadStatus)
	return v, err
}
func sendPostgresReadGraph(t *testing.T, f sendPostgresFixture) sendPostgresGraph {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := readSendPostgresGraph(ctx, f.pool, f)
	require.NoError(t, err, "inspect only the isolated synthetic tenant's complete accepted graph and original counters")
	return v
}

// Keep every column, including status/claim tokens, answer identities and
// timestamps. Count-only equality could miss a compensating write, a replaced
// row, or a changed answer while leaving the same apparent graph cardinality.
func readSendPostgresSnapshot(ctx context.Context, q db.DBTX, f sendPostgresFixture) (json.RawMessage, error) {
	var snapshot json.RawMessage
	err := q.QueryRow(ctx, `SELECT jsonb_build_object(
 'session',(SELECT to_jsonb(s) FROM sessions s WHERE org_id=$1 AND id=$2),
 'threads',(SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY id),'[]'::jsonb) FROM session_threads t WHERE org_id=$1 AND session_id=$2),
 'messages',(SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY id),'[]'::jsonb) FROM session_messages m WHERE org_id=$1 AND session_id=$2),
 'inbox',(SELECT coalesce(jsonb_agg(to_jsonb(i) ORDER BY id),'[]'::jsonb) FROM thread_inbox_entries i WHERE org_id=$1 AND session_id=$2),
 'dispatches',(SELECT coalesce(jsonb_agg(to_jsonb(d) ORDER BY id),'[]'::jsonb) FROM session_input_dispatches d WHERE org_id=$1 AND session_id=$2),
 'comments',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY id),'[]'::jsonb) FROM session_review_comments c WHERE org_id=$1 AND session_id=$2),
 'questions',(SELECT coalesce(jsonb_agg(to_jsonb(q) ORDER BY id),'[]'::jsonb) FROM session_questions q WHERE org_id=$1 AND session_id=$2),
 'human_input',(SELECT coalesce(jsonb_agg(to_jsonb(h) ORDER BY id),'[]'::jsonb) FROM session_human_input_requests h WHERE org_id=$1 AND session_id=$2),
 'jobs',(SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY id),'[]'::jsonb) FROM jobs j WHERE org_id=$1 AND payload->>'session_id'=$2::text)
 )`, f.org, f.session).Scan(&snapshot)
	return snapshot, err
}

func sendPostgresReadSnapshot(t *testing.T, f sendPostgresFixture) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.LifecycleTransactionTimeout)
	defer cancel()
	snapshot, err := readSendPostgresSnapshot(ctx, f.pool, f)
	require.NoError(t, err, "read the exact tenant/session rows and every claim, projection, comment, answer and queued-job field")
	return snapshot
}

func requireSendPostgresNoEffects(t *testing.T, effects *sendPostgresEffects) {
	t.Helper()
	require.Equal(t, []int{0, 0, 0, 0, 0, 0}, []int{len(effects.lifecycle), len(effects.jobs), effects.claims, effects.pending, effects.threadStatus, effects.parentStatus}, "aborted or unknown legacy work must publish no lifecycle, job, claim, pending or status callback")
}

func seedSendPostgresAnswers(t *testing.T, f sendPostgresFixture) (models.SessionQuestion, models.HumanInputRequest) {
	t.Helper()
	question := models.SessionQuestion{OrgID: f.org, SessionID: f.session, QuestionText: "Synthetic exact clarifying question", Status: models.SessionQuestionStatusPending, Options: []string{}}
	request := models.HumanInputRequest{OrgID: f.org, SessionID: f.session, ThreadID: &f.lanes[0], AgentType: models.DefaultDefaultAgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Synthetic free text request", Body: "Synthetic answer context"}
	require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := db.NewSessionQuestionStore(tx).Create(ctx, &question); err != nil {
			return err
		}
		return db.NewSessionHumanInputRequestStore(tx).Create(ctx, &request)
	}), "seed a real pending clarifying question and a separate free-text request, not a tool approval")
	return question, request
}

type sendPostgresUnit struct {
	id    int64
	graph sendPostgresGraph
}
type sendPostgresTransactionsFunc func(context.Context, func(context.Context, pgx.Tx) error) error

func (f sendPostgresTransactionsFunc) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return f(ctx, work)
}

type sendPostgresRecorder struct {
	base       SendTransactions
	fixture    sendPostgresFixture
	units      []sendPostgresUnit
	afterEntry func() error
}

func (r *sendPostgresRecorder) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	var unit sendPostgresUnit
	err := r.base.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		unit = sendPostgresUnit{}
		if err := tx.QueryRow(ctx, `SELECT txid_current()`).Scan(&unit.id); err != nil {
			return err
		}
		if err := work(ctx, tx); err != nil {
			return err
		}
		var err error
		unit.graph, err = readSendPostgresGraph(ctx, tx, r.fixture)
		return err
	})
	if err != nil {
		return err
	}
	r.units = append(r.units, unit)
	if len(r.units) == 1 && r.afterEntry != nil {
		return r.afterEntry()
	}
	return nil
}

type sendPostgresLateFaultTx struct {
	pgx.Tx
	fixture     sendPostgresFixture
	provisional *sendPostgresGraph
	comment     *bool
	commentID   uuid.UUID
	cause       error
}

func (t sendPostgresLateFaultTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "UPDATE session_input_dispatches SET validated_payload=") {
		v, err := readSendPostgresGraph(ctx, t.Tx, t.fixture)
		if err != nil {
			return pgconn.CommandTag{}, err
		}
		*t.provisional = v
		if err := t.Tx.QueryRow(ctx, `SELECT resolved FROM session_review_comments WHERE org_id=$1 AND session_id=$2 AND id=$3`, t.fixture.org, t.fixture.session, t.commentID).Scan(t.comment); err != nil {
			return pgconn.CommandTag{}, err
		}
		return pgconn.CommandTag{}, t.cause
	}
	return t.Tx.Exec(ctx, query, args...)
}

func seedSendPostgresComment(t *testing.T, f sendPostgresFixture) uuid.UUID {
	t.Helper()
	comment := models.SessionReviewComment{OrgID: f.org, SessionID: f.session, UserID: f.user, FilePath: "main.go", LineNumber: 1, DiffSide: "new", Body: "Synthetic requested correction", PassNumber: 1}
	require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		return db.NewSessionReviewCommentStore(tx).Create(ctx, &comment)
	}), "create the actual review comment under the exact tenant and parent")
	return comment.ID
}

func sendPostgresCommentResolved(t *testing.T, f sendPostgresFixture, id uuid.UUID) bool {
	t.Helper()
	var resolved bool
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT resolved FROM session_review_comments WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, id).Scan(&resolved), "read only the exact tenant's synthetic comment disposition")
	return resolved
}

func seedSendPostgresCapabilityRequest(t *testing.T, f sendPostgresFixture, lane *uuid.UUID) uuid.UUID {
	t.Helper()
	request := models.HumanInputRequest{OrgID: f.org, SessionID: f.session, ThreadID: lane, AgentType: models.DefaultDefaultAgentType, Kind: models.HumanInputRequestKindToolApproval, Title: "Synthetic capability request", Body: "Synthetic access approval context", Choices: []models.HumanInputChoice{{ID: "approve", Label: "Approve"}, {ID: "deny", Label: "Deny"}}, ProviderPayload: json.RawMessage(`{"type":"agent_capability_request","capability_id":"session_history","access_level":"read"}`)}
	require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		return db.NewSessionHumanInputRequestStore(tx).Create(ctx, &request)
	}), "seed a real pending capability request without granting or answering it")
	return request.ID
}

func seedSendPostgresRepair(t *testing.T, f sendPostgresFixture, lane uuid.UUID, active bool) models.PullRequestRepairRun {
	t.Helper()
	head, base := "synthetic-current-head", "synthetic-base"
	pr := models.PullRequest{OrgID: f.org, SessionID: &f.session, GitHubPRNumber: 9, GitHubPRURL: "https://example.invalid/pr/9", GitHubRepo: "synthetic/admission", Title: "Synthetic admission repair", Status: models.PullRequestStatusOpen, ReviewStatus: models.PullRequestReviewStatusPending, HeadSHA: &head, BaseSHA: &base}
	run := models.PullRequestRepairRun{OrgID: f.org, SessionID: f.session, ThreadID: &lane, ActionType: models.PullRequestRepairActionTypeFixTests, HealthVersion: 1, HeadSHA: head, BaseSHA: base, Active: active}
	if !active {
		run.HeadSHA = "synthetic-released-old-head"
	}
	require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		store := db.NewPullRequestStore(tx)
		if err := store.Create(ctx, &pr); err != nil {
			return err
		}
		run.PullRequestID = pr.ID
		return store.CreateRepairRun(ctx, &run)
	}), "seed a real PR and repair reservation in one independently committed production store unit")
	return run
}

func requireSendPostgresClaimLink(t *testing.T, f sendPostgresFixture, admission db.Admission, job uuid.UUID, message *models.SessionMessage) {
	t.Helper()
	var dispatch, token, dispatchToken, claimJob uuid.UUID
	var messageID, sessionEpoch, laneEpoch int64
	var turn int
	var state, consumer string
	var dueCleared bool
	err := f.pool.QueryRow(context.Background(), `SELECT t.admission_claim_dispatch_id,t.admission_claim_token,d.job_id,d.message_id,d.session_epoch,d.thread_epoch,d.admission_claim_turn,d.admission_state,d.consumer_kind,d.admission_claim_token,d.next_attempt_at IS NULL
 FROM session_threads t JOIN session_input_dispatches d ON d.org_id=t.org_id AND d.session_id=t.session_id AND d.thread_id=t.id AND d.id=t.admission_claim_dispatch_id
 WHERE t.org_id=$1 AND t.session_id=$2 AND t.id=$3`, f.org, f.session, *message.ThreadID).Scan(&dispatch, &token, &claimJob, &messageID, &sessionEpoch, &laneEpoch, &turn, &state, &consumer, &dispatchToken, &dueCleared)
	require.NoError(t, err, "read the exact lane/dispatch/job/message association through the real committed composite links")
	require.Equal(t, []uuid.UUID{admission.DispatchID(), job}, []uuid.UUID{dispatch, claimJob}, "lane claim must name precisely the returned dispatch and newly committed job")
	require.NotEqual(t, uuid.Nil, token, "logical admission token must be nonzero")
	require.Equal(t, token, dispatchToken, "lane and dispatch must retain the same token, not merely the same message")
	require.Equal(t, []int64{message.ID, 3, 5, int64(message.TurnNumber)}, []int64{messageID, sessionEpoch, laneEpoch, int64(turn)}, "committed link must retain exact message, original epochs and admitted turn")
	require.Equal(t, []string{"bound", "job"}, []string{state, consumer}, "new job must own a bound dispatch")
	require.True(t, dueCleared, "bound input must not remain due as unbound waiting")
	authority, hasAuthority := admission.Authority()
	require.True(t, hasAuthority, "only a canonical new-job receipt may expose its shape-validated logical claim")
	require.NoError(t, authority.Validate(), "the returned immutable claim shape must match its durable binding without becoming a start permit")
}
