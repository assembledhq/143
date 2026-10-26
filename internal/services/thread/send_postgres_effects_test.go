package thread

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
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
