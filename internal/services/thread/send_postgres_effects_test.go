package thread

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type sendPostgresVisibleEffects struct {
	SendEffects
	t                        *testing.T
	fixture                  sendPostgresFixture
	observer                 *pgxpool.Conn
	expected                 sendPostgresGraph
	lifecycleCalls, jobCalls int
}

type legacySendPostgresVisibleEffects struct {
	SendEffects
	t        *testing.T
	fixture  sendPostgresFixture
	observer *pgxpool.Conn
	expected *json.RawMessage
	calls    []string
}

func (e *legacySendPostgresVisibleEffects) observe(ctx context.Context, name string) {
	e.t.Helper()
	require.NotEmpty(e.t, *e.expected, "postcommit publication must follow the complete successful command callback")
	actual, err := readSendPostgresSnapshot(ctx, e.observer, e.fixture)
	require.NoError(e.t, err, "an independent connection must read every durable row during each legacy publication callback")
	require.JSONEq(e.t, string(*e.expected), string(actual), "each legacy publisher must observe the entire committed graph, including job, claims, comments and answer fields")
	e.calls = append(e.calls, name)
}

func (e *legacySendPostgresVisibleEffects) PublishThreadClaim(ctx context.Context, org uuid.UUID, effect db.ThreadClaimPersistenceEffects) {
	e.observe(ctx, "claim")
	e.SendEffects.PublishThreadClaim(ctx, org, effect)
}
func (e *legacySendPostgresVisibleEffects) PublishThreadPending(ctx context.Context, org uuid.UUID, effect db.ThreadPendingMessagePersistenceEffects) {
	e.observe(ctx, "pending")
	e.SendEffects.PublishThreadPending(ctx, org, effect)
}
func (e *legacySendPostgresVisibleEffects) PublishLifecycle(ctx context.Context, org uuid.UUID, effects []db.LifecycleEffect) {
	e.observe(ctx, "lifecycle")
	e.SendEffects.PublishLifecycle(ctx, org, effects)
}
func (e *legacySendPostgresVisibleEffects) NotifyJob(ctx context.Context, id uuid.UUID) {
	e.observe(ctx, "job")
	var retained uuid.UUID
	require.NoError(e.t, e.observer.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND id=$2 AND payload->>'session_id'=$3`, e.fixture.org, id, e.fixture.session.String()).Scan(&retained), "independent observer must see the exact legacy job in its wake callback")
	require.Equal(e.t, id, retained, "legacy wake must refer to the actual durably committed job")
	e.SendEffects.NotifyJob(ctx, id)
}

type legacySendPostgresEnqueueProbe struct {
	LegacySendPersistence
	after func(context.Context, pgx.Tx) error
}

func (p legacySendPostgresEnqueueProbe) EnqueueInTx(ctx context.Context, tx pgx.Tx, org uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	id, err := p.LegacySendPersistence.EnqueueInTx(ctx, tx, org, opts)
	if err == nil && p.after != nil {
		err = p.after(ctx, tx)
	}
	return id, err
}

func TestThreadSendLegacyPostcommitEffectsObserveWholeGraphPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		grouped bool
		running bool
		answers bool
		calls   []string
	}{
		{name: "idle plain", calls: []string{"claim", "job"}},
		{name: "idle grouped with comments and both answers", grouped: true, answers: true, calls: []string{"claim", "job"}},
		{name: "running plain", running: true, calls: []string{"pending", "job"}},
		{name: "running grouped", grouped: true, running: true, calls: []string{"job"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, false, 1)
			service, _ := f.service(t, tt.grouped)
			input := f.input(0, "synthetic-legacy-independent-effects")
			if tt.running || tt.answers {
				status := models.SessionStatusRunning
				if tt.answers {
					status = models.SessionStatusAwaitingInput
					input.ResolveReviewCommentIDs = []uuid.UUID{seedSendPostgresComment(t, f)}
					seedSendPostgresAnswers(t, f)
					service.SetQuestionStore(db.NewSessionQuestionStore(f.pool))
					service.SetHumanInputRequestStore(db.NewSessionHumanInputRequestStore(f.pool))
				}
				sendPostgresExec(t, f, `UPDATE sessions SET status=$3 WHERE org_id=$1 AND id=$2`, f.org, f.session, status)
				sendPostgresExec(t, f, `UPDATE session_threads SET status=$3 WHERE org_id=$1 AND session_id=$2`, f.org, f.session, models.ThreadStatus(status))
			}
			baseline := sendPostgresReadSnapshot(t, f)
			ctx, cancel := context.WithTimeout(context.Background(), 3*db.LifecycleTransactionTimeout)
			defer cancel()
			observer, err := f.pool.Acquire(ctx)
			require.NoError(t, err, "reserve an independent observer within the unchanged two-connection fixture")
			defer observer.Release()
			var expected json.RawMessage
			calls := 0
			service.send.Legacy = legacySendPostgresEnqueueProbe{LegacySendPersistence: service.send.Legacy, after: func(ctx context.Context, tx pgx.Tx) error {
				calls++
				var err error
				expected, err = readSendPostgresSnapshot(ctx, tx, f)
				if err != nil {
					return err
				}
				independent, err := readSendPostgresSnapshot(ctx, observer, f)
				if err != nil {
					return err
				}
				require.JSONEq(t, string(baseline), string(independent), "even after actual job insertion the independent observer must see the entire original baseline until whole commit")
				cutover, err := observer.Begin(ctx)
				if err != nil {
					return err
				}
				defer func() { _ = cutover.Rollback(ctx) }()
				var exclusive bool
				if err := cutover.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text,0))`, "session_lifecycle_cutover:"+f.org.String()+":"+f.session.String()).Scan(&exclusive); err != nil {
					return err
				}
				require.False(t, exclusive, "the whole mutation's actual shared interlock must prevent an internal cutover after its message, answers and job writes")
				return cutover.Rollback(ctx)
			}}
			effects := &legacySendPostgresVisibleEffects{SendEffects: service.send.Effects, t: t, fixture: f, observer: observer, expected: &expected}
			service.send.Effects = effects
			result, err := service.SendMessage(ctx, input)
			require.NoError(t, err, "the actual legacy service must commit its whole graph before any publisher")
			require.NotNil(t, result.Message, "successful whole command must return its durable input")
			require.Equal(t, 1, calls, "the no-contention probe must follow one actual enqueue in one whole command")
			require.Equal(t, tt.calls, effects.calls, "each established legacy publisher must observe the whole graph once with no status compensation")
			require.Equal(t, int32(2), f.pool.Config().MaxConns, "visibility proof may not increase pool capacity")
		})
	}
}

type legacySendOwnerLossProbe struct {
	t         *testing.T
	fixture   sendPostgresFixture
	expected  *json.RawMessage
	committed *bool
	value     any
	returnErr error
	calls     int
}

type legacyOwnerLossContextKey struct{}

func (p *legacySendOwnerLossProbe) RecoverLostOwner(ctx context.Context, org, session uuid.UUID, lane *uuid.UUID) error {
	p.calls++
	require.True(p.t, *p.committed, "owner-loss recovery must start only after the whole command has actually committed")
	require.Equal(p.t, []uuid.UUID{p.fixture.org, p.fixture.session, p.fixture.lanes[0]}, []uuid.UUID{org, session, *lane}, "owner-loss recovery must retain the exact accepted scope")
	require.Equal(p.t, p.value, ctx.Value(legacyOwnerLossContextKey{}), "separate recovery context retains original context values")
	require.NoError(p.t, ctx.Err(), "cancelled original request must not cancel postcommit owner-loss recovery")
	deadline, present := ctx.Deadline()
	require.True(p.t, present, "owner-loss recovery must have its independent bounded deadline")
	require.Positive(p.t, time.Until(deadline), "independent recovery budget must still be live when recovery begins")
	require.LessOrEqual(p.t, time.Until(deadline), 5*time.Second, "owner-loss recovery must retain the existing five-second bound")
	actual, err := readSendPostgresSnapshot(ctx, p.fixture.pool, p.fixture)
	require.NoError(p.t, err, "owner-loss recovery must reacquire the one available connection after the command releases it")
	require.JSONEq(p.t, string(*p.expected), string(actual), "owner-loss recovery must observe the whole committed queued input and delivery job")
	return p.returnErr
}

func TestThreadSendLegacyOwnerLossRunsSeparatelyAfterWholeCommitPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "successful recovery"},
		{name: "best effort recovery failure does not undo acceptance", err: errors.New("synthetic owner-loss follow-up failure")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			sendPostgresExec(t, f, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.org, f.session)
			sendPostgresExec(t, f, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
			service, _ := f.service(t, true)
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), legacyOwnerLossContextKey{}, "synthetic-recovery-context"))
			defer cancel()
			runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f}
			var committed bool
			var expected json.RawMessage
			runner.afterKnown = func(unit int) error {
				if unit == 2 {
					committed, expected = true, runner.units[1].snapshot
					cancel()
				}
				return nil
			}
			service.send.Transactions = runner
			owner := &legacySendOwnerLossProbe{t: t, fixture: f, expected: &expected, committed: &committed, value: "synthetic-recovery-context", returnErr: tt.err}
			service.SetOwnerLossOrchestrator(owner)
			result, err := service.SendMessage(ctx, f.input(0, "synthetic-separate-owner-loss"))
			require.NoError(t, err, "postcommit owner-loss work must not turn a committed queued send into failure")
			require.NotNil(t, result.Message, "committed queued input remains accepted after original request cancellation")
			require.Equal(t, 1, owner.calls, "queued acceptance runs owner-loss follow-up once after whole commit")
			requireSendPostgresLegacyUnitGuards(t, runner.units)
			require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, liveJobs: 1, pending: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, sendPostgresReadGraph(t, f), "best-effort owner-loss recovery cannot undo the complete queued send")
		})
	}
}

func (e *sendPostgresVisibleEffects) observe(ctx context.Context) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
	defer cancel()
	actual, err := readSendPostgresGraph(ctx, e.observer, e.fixture)
	require.NoError(e.t, err, "an independent connection must read the durable graph inside each actual postcommit callback")
	require.Equal(e.t, e.expected, actual, "publishers must observe the complete committed acceptance rather than provisional transaction state")
}

func (e *sendPostgresVisibleEffects) PublishLifecycle(ctx context.Context, org uuid.UUID, effects []db.LifecycleEffect) {
	require.Equal(e.t, e.fixture.org, org, "postcommit notifications must retain the exact accepted tenant")
	e.observe(ctx)
	e.lifecycleCalls++
	e.SendEffects.PublishLifecycle(ctx, org, effects)
}

func (e *sendPostgresVisibleEffects) NotifyJob(ctx context.Context, id uuid.UUID) {
	e.observe(ctx)
	ctx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
	defer cancel()
	var linked bool
	err := e.observer.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM jobs j JOIN session_input_dispatches d ON d.org_id=j.org_id AND d.job_id=j.id
 JOIN session_threads t ON t.org_id=d.org_id AND t.session_id=d.session_id
 AND t.id=d.thread_id AND t.admission_claim_dispatch_id=d.id
 WHERE j.org_id=$1 AND j.id=$2 AND d.session_id=$3 AND t.id=$4)`, e.fixture.org, id, e.fixture.session, e.fixture.lanes[0]).Scan(&linked)
	require.NoError(e.t, err, "the independent observer must read the exact job dispatch and lane association during its wake callback")
	require.True(e.t, linked, "only a durably bound exact input job may be notified")
	e.jobCalls++
	e.SendEffects.NotifyJob(ctx, id)
}

func TestThreadSendPostcommitEffectsVisibleOnIndependentConnectionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		running bool
		jobs    int
	}{
		{name: "new bound input", jobs: 1},
		{name: "queued input has no speculative job wake", running: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, true, 1)
			if tt.running {
				sendPostgresExec(t, f, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.org, f.session)
				sendPostgresExec(t, f, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.lanes[0])
			}
			service, _ := f.service(t, true)
			// Reserve one of the unchanged two connections before the service
			// starts. It cannot be the mutation owner's connection, and each
			// callback SELECT obtains an independent committed snapshot.
			ctx, cancel := context.WithTimeout(context.Background(), 3*db.LifecycleTransactionTimeout)
			defer cancel()
			observer, err := f.pool.Acquire(ctx)
			require.NoError(t, err, "reserve the independent observer without increasing pool capacity")
			defer observer.Release()
			require.Equal(t, int32(2), f.pool.Config().MaxConns, "commit visibility proof must retain the two-connection baseline")
			expected := sendPostgresGraph{messages: 1, inbox: 1, dispatches: 1, continueJobs: 1, claims: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}
			if tt.running {
				expected.continueJobs, expected.claims, expected.pending = 0, 0, 1
			}
			effects := &sendPostgresVisibleEffects{SendEffects: service.send.Effects, t: t, fixture: f, observer: observer, expected: expected}
			service.send.Effects = effects
			input := f.input(0, "synthetic-independent-effect-visibility")
			result, err := service.SendMessage(ctx, input)
			require.NoError(t, err, "the actual service must publish only after its complete acceptance is visible independently")
			require.NotNil(t, result.Admission, "successful active send must return the same durable receipt observed by publishers")
			require.Equal(t, []int{1, tt.jobs}, []int{effects.lifecycleCalls, effects.jobCalls}, "new acceptance publishes once and wakes only its own committed job")
			replay, err := service.SendMessage(ctx, input)
			require.NoError(t, err, "exact same-key replay must remain readable while the independent observer is reserved")
			require.Equal(t, db.MessageAdmissionReplayed, replay.Admission.Disposition(), "the second request must be historical acceptance, not a new mutation")
			require.Equal(t, []int{1, tt.jobs}, []int{effects.lifecycleCalls, effects.jobCalls}, "replay must not repeat either postcommit publisher callback")
		})
	}
}
