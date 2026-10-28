package thread

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type sendPostgresLegacyLateEnqueueFailure struct {
	LegacySendPersistence
	t           *testing.T
	fixture     sendPostgresFixture
	cause       error
	provisional sendPostgresGraph
	calls       int
	afterInsert func(context.Context, pgx.Tx) error
}

func (p *sendPostgresLegacyLateEnqueueFailure) EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	jobID, err := p.LegacySendPersistence.EnqueueInTx(ctx, tx, orgID, opts)
	if err != nil {
		return jobID, err
	}
	p.calls++
	require.NotEqual(p.t, uuid.Nil, jobID, "the late fault must follow an actual newly inserted PostgreSQL job")
	p.provisional, err = readSendPostgresGraph(ctx, tx, p.fixture)
	if err != nil {
		return uuid.Nil, err
	}
	require.Equal(p.t, sendPostgresGraph{messages: 1, inbox: 1, continueJobs: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, p.provisional, "the supplied transaction must see the real job and complete provisional message, inbox, and claimed status graph before the injected fault")
	if p.afterInsert != nil {
		if err := p.afterInsert(ctx, tx); err != nil {
			return uuid.Nil, err
		}
	}
	return uuid.Nil, p.cause
}

func TestThreadSendLegacyAtomicLateEnqueueRollbackPostgres(t *testing.T) {
	t.Parallel()
	f := newSendPostgresFixture(t, false, 1)
	service, effects := f.service(t, true)
	baseline := sendPostgresReadGraph(t, f)
	cause := errors.New("synthetic late legacy enqueue atomicity failure")
	persistence := &sendPostgresLegacyLateEnqueueFailure{LegacySendPersistence: service.send.Legacy, t: t, fixture: f, cause: cause}
	service.send.Legacy = persistence

	result, err := service.SendMessage(context.Background(), f.input(0, "synthetic-late-legacy-enqueue-rollback"))
	require.ErrorIs(t, err, ErrEnqueueFailed, "a confirmed late enqueue rollback must retain the established failed-enqueue error")
	require.ErrorIs(t, err, cause, "a confirmed late enqueue rollback must preserve its exact callback cause")
	require.Nil(t, result, "a rolled-back legacy send must not return an accepted result")
	require.Equal(t, 1, persistence.calls, "an ordinary callback failure must not retry the send or insert another job")
	require.Equal(t, baseline, sendPostgresReadGraph(t, f), "a late enqueue failure must roll back the complete legacy send, including messages, inbox entries, and claimed statuses")
	require.Equal(t, []int{0, 0, 0, 0}, []int{effects.claims, effects.pending, effects.threadStatus, effects.parentStatus}, "a whole-command rollback must not publish provisional claims or compensating statuses")
	require.Empty(t, effects.jobs, "a rolled-back real job insertion must not wake any worker")
	require.Empty(t, effects.lifecycle, "a failed legacy send must not publish canonical lifecycle acceptance")
}

func TestThreadSendLegacyLateEnqueueRollsBackCommentsAndAnswersPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		answers bool
	}{
		{name: "resolved comment"},
		{name: "resolved comment and actual question and free-text answers", answers: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, false, 1)
			foreign := newSendPostgresComparisonScope(t, f, true)
			otherSession := newSendPostgresComparisonScope(t, f, false)
			for _, sentinel := range []sendPostgresFixture{foreign, otherSession} {
				seedSendPostgresComment(t, sentinel)
				seedSendPostgresAnswers(t, sentinel)
			}
			service, effects := f.service(t, true)
			input := f.input(0, "synthetic-whole-answer-rollback")
			comment := seedSendPostgresComment(t, f)
			input.ResolveReviewCommentIDs = []uuid.UUID{comment}
			var question models.SessionQuestion
			var request models.HumanInputRequest
			if tt.answers {
				question, request = seedSendPostgresAnswers(t, f)
				sendPostgresExec(t, f, `UPDATE sessions SET status='awaiting_input' WHERE org_id=$1 AND id=$2`, f.org, f.session)
				sendPostgresExec(t, f, `UPDATE session_threads SET status='awaiting_input' WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
				service.SetQuestionStore(db.NewSessionQuestionStore(f.pool))
				service.SetHumanInputRequestStore(db.NewSessionHumanInputRequestStore(f.pool))
			}
			baseline := sendPostgresReadSnapshot(t, f)
			foreignBaseline, otherBaseline := sendPostgresReadSnapshot(t, foreign), sendPostgresReadSnapshot(t, otherSession)
			cause := errors.New("synthetic late atomic comment and answer rollback")
			persistence := &sendPostgresLegacyLateEnqueueFailure{LegacySendPersistence: service.send.Legacy, t: t, fixture: f, cause: cause}
			persistence.afterInsert = func(ctx context.Context, tx pgx.Tx) error {
				var resolved bool
				if err := tx.QueryRow(ctx, `SELECT resolved FROM session_review_comments WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, comment).Scan(&resolved); err != nil {
					return err
				}
				require.True(t, resolved, "late failure must follow an actual provisional comment resolution")
				if tt.answers {
					answeredQuestion, err := db.NewSessionQuestionStore(tx).GetByID(ctx, f.org, question.ID)
					if err != nil {
						return err
					}
					answeredRequest, err := db.NewSessionHumanInputRequestStore(tx).GetByID(ctx, f.org, f.session, request.ID)
					if err != nil {
						return err
					}
					require.Equal(t, models.SessionQuestionStatusAnswered, answeredQuestion.Status, "the question must be really answered before the late fault")
					require.Equal(t, models.HumanInputRequestStatusAnswered, answeredRequest.Status, "the free-text request must be really answered before the late fault")
					require.Equal(t, &input.Message, answeredQuestion.AnswerText, "provisional question answer must retain the exact free text")
					require.Equal(t, &input.Message, answeredRequest.AnswerText, "provisional human-input answer must retain the exact free text")
					require.Equal(t, input.UserID, answeredQuestion.AnsweredBy, "provisional question answer must retain the exact actor")
					require.Equal(t, input.UserID, answeredRequest.AnsweredBy, "provisional free-text answer must retain the exact actor")
					require.NotNil(t, answeredQuestion.AnsweredAt, "question answer must have a real provisional answer timestamp")
					require.NotNil(t, answeredRequest.AnsweredAt, "free-text answer must have a real provisional answer timestamp")
				}
				return nil
			}
			service.send.Legacy = persistence
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, ErrEnqueueFailed, "late failure must preserve the actual enqueue error boundary")
			require.ErrorIs(t, err, cause, "late failure must preserve its exact cause after whole rollback")
			require.Nil(t, result, "rolled-back comment and answer work must not expose acceptance")
			require.Equal(t, 1, persistence.calls, "ordinary late callback failure cannot retry or compensate the send")
			require.JSONEq(t, string(baseline), string(sendPostgresReadSnapshot(t, f)), "whole rollback must restore every original row including resolved, answered_by, answered_at, claim and status fields")
			require.JSONEq(t, string(foreignBaseline), string(sendPostgresReadSnapshot(t, foreign)), "the same database's foreign tenant answer and comment sentinels must be untouched")
			require.JSONEq(t, string(otherBaseline), string(sendPostgresReadSnapshot(t, otherSession)), "the same tenant's other session answer and comment sentinels must be untouched")
			requireSendPostgresNoEffects(t, effects)
		})
	}
}

func TestThreadSendLegacyDedupeNilJobRemainsSuccessfulPostgres(t *testing.T) {
	t.Parallel()
	f := newSendPostgresFixture(t, false, 1)
	service, effects := f.service(t, true)
	key := db.ContinueSessionDedupeKey(f.lanes[0])
	var existing uuid.UUID
	require.NoError(t, db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		existing, err = db.NewJobStore(tx).EnqueueInTxWithOpts(ctx, tx, f.org, db.EnqueueOpts{Queue: "agent", JobType: "continue_session", Payload: map[string]string{"org_id": f.org.String(), "session_id": f.session.String(), "thread_id": f.lanes[0].String()}, Priority: 5, DedupeKey: &key})
		return err
	}), "seed the actual pending continuation with its exact legacy dedupe key")
	before := sendPostgresReadSnapshot(t, f)
	result, err := service.SendMessage(context.Background(), f.input(0, "synthetic-nil-job-legacy-dedupe"))
	require.NoError(t, err, "legacy (uuid.Nil,nil) dedupe result must retain successful message acceptance")
	require.NotNil(t, result.Message, "legacy dedupe success still commits its new message")
	require.Nil(t, result.Admission, "legacy job dedupe cannot fabricate canonical active receipt authority")
	require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, continueJobs: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, sendPostgresReadGraph(t, f), "dedupe success must commit one message and inbox while preserving the single original job")
	var beforeRows, afterRows map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(before, &beforeRows), "decode original exact job snapshot")
	require.NoError(t, json.Unmarshal(sendPostgresReadSnapshot(t, f), &afterRows), "decode accepted exact job snapshot")
	require.JSONEq(t, string(beforeRows["jobs"]), string(afterRows["jobs"]), "dedupe must retain the original exact job rather than mutate or replace it")
	require.NotEqual(t, uuid.Nil, existing, "seed must own an actual pending job")
	require.Equal(t, []uuid.UUID{uuid.Nil}, effects.jobs, "retain the established legacy notification call for the nil dedupe result; the concrete publisher decides its no-op handling")
}

func TestThreadSendLegacyWholeCommandFitsOneConnectionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		grouped  bool
		running  bool
		resume   bool
		capacity bool
	}{
		{name: "idle plain"},
		{name: "idle grouped", grouped: true},
		{name: "running plain", running: true},
		{name: "running grouped", grouped: true, running: true},
		{name: "resumed plain", resume: true},
		{name: "resumed grouped", resume: true, grouped: true},
		{name: "capacity wait plain", capacity: true},
		{name: "capacity wait grouped", capacity: true, grouped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lanes := 1
			if tt.capacity {
				lanes = 4
			}
			f := newSendPostgresFixtureWithMaxConns(t, false, lanes, 1)
			if tt.running {
				sendPostgresExec(t, f, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.org, f.session)
				sendPostgresExec(t, f, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
			}
			if tt.resume {
				sendPostgresExec(t, f, `UPDATE sessions SET status='completed' WHERE org_id=$1 AND id=$2`, f.org, f.session)
				sendPostgresExec(t, f, `UPDATE session_threads SET status='completed' WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
			}
			if tt.capacity {
				sendPostgresExec(t, f, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.org, f.session)
				sendPostgresExec(t, f, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND session_id=$2 AND id=ANY($3::uuid[])`, f.org, f.session, f.lanes[1:])
			}
			service, effects := f.service(t, tt.grouped)
			recorder := &sendPostgresRecorder{base: service.send.Transactions, fixture: f}
			service.send.Transactions = recorder
			input := f.input(0, "synthetic-one-connection-atomic-send")
			result, err := service.SendMessage(context.Background(), input)
			require.NoError(t, err, "all legacy decision reads and writes must use the supplied transaction without nested pool acquisition")
			require.NotNil(t, result.Message, "one-connection send must return its committed message")
			require.Equal(t, int32(1), f.pool.Config().MaxConns, "proof must retain an actual one-connection fixture")
			actual := make([]sendPostgresGraph, len(recorder.units))
			for i, unit := range recorder.units {
				actual[i] = unit.graph
			}
			want := sendPostgresExpectedUnits(false, tt.running)
			if tt.resume {
				want[0] = sendPostgresGraph{parentStatus: models.SessionStatusCompleted, threadStatus: models.ThreadStatusCompleted}
			}
			if tt.capacity {
				want = []sendPostgresGraph{{parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusIdle}, {messages: 1, inbox: 1, pending: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusIdle}}
				require.Empty(t, effects.jobs, "one-connection capacity waiting must not invent or notify a consumer job")
			}
			require.Equal(t, want, actual, "one connection must prove entry plus one entire idle, resumed, mid-turn or capacity command for grouped and plain wiring")
			require.Equal(t, want[1], sendPostgresReadGraph(t, f), "independent readback must equal the whole committed graph at the original one-connection bound")
			require.Nil(t, result.Admission, "legacy one-connection acceptance cannot fabricate an active receipt")
			baseline := sendPostgresReadSnapshot(t, f)
			beforeEffects := []int{len(effects.jobs), effects.claims, effects.pending}
			replay, err := service.SendMessage(context.Background(), input)
			require.NoError(t, err, "exact legacy replay must complete without nested acquisition on the single connection")
			require.Equal(t, result.Message, replay.Message, "legacy replay must return the original exact durable message on every branch")
			require.JSONEq(t, string(baseline), string(sendPostgresReadSnapshot(t, f)), "one-connection replay must not change any original row or create another job")
			require.Equal(t, beforeEffects, []int{len(effects.jobs), effects.claims, effects.pending}, "historical replay must not repeat job or stream notifications")
		})
	}
}
