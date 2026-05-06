package linear

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// agentWriterFake implements the linear.Client interface for the writer
// tests. Reuses the existing fakeLinearClient pattern from
// handle_milestone_test.go but adds counters specific to the agent
// surface so we can assert "exactly one create call on happy path" and
// "no create call when dedupe short-circuits".
type agentWriterFake struct {
	*fakeLinearClient

	// agentMu guards the agent-specific counters below. Named to avoid
	// shadowing fakeLinearClient.mu (which the embedded type uses for
	// its own counters); a future test that locks the wrong mutex would
	// otherwise produce a confusing race.
	agentMu          sync.Mutex
	agentCreateCalls int
	agentCreateErr   error
	agentCreateRet   AgentActivityResult
	stateUpdateCalls int
	stateUpdateLast  AgentSessionUpdateInput
}

func newAgentWriterFake() *agentWriterFake {
	return &agentWriterFake{
		fakeLinearClient: newFakeLinearClient(),
		agentCreateRet:   AgentActivityResult{ActivityID: "lin_act_default"},
	}
}

func (f *agentWriterFake) AgentActivityCreate(_ context.Context, _ AgentActivityInput) (AgentActivityResult, error) {
	f.agentMu.Lock()
	defer f.agentMu.Unlock()
	f.agentCreateCalls++
	if f.agentCreateErr != nil {
		return AgentActivityResult{}, f.agentCreateErr
	}
	return f.agentCreateRet, nil
}

func (f *agentWriterFake) AgentSessionUpdate(_ context.Context, in AgentSessionUpdateInput) error {
	f.agentMu.Lock()
	defer f.agentMu.Unlock()
	f.stateUpdateCalls++
	f.stateUpdateLast = in
	return nil
}

// fakeAgentMetricsRecorder captures RecordActivityEmitted calls so tests
// can assert metrics were wired through correctly.
type fakeAgentMetricsRecorder struct {
	mu    sync.Mutex
	calls []fakeMetricsCall
}

type fakeMetricsCall struct {
	ActivityType string
	Skipped      bool
}

func (f *fakeAgentMetricsRecorder) RecordActivityEmitted(_ context.Context, t string, skipped bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeMetricsCall{ActivityType: t, Skipped: skipped})
}

type writerTestRig struct {
	mock    pgxmock.PgxPoolIface
	client  *agentWriterFake
	metrics *fakeAgentMetricsRecorder
	writer  *AgentActivityWriter
	orgID   uuid.UUID
	rowID   uuid.UUID
}

func newWriterTestRig(t *testing.T) *writerTestRig {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)

	client := newAgentWriterFake()
	metrics := &fakeAgentMetricsRecorder{}
	writer := NewAgentActivityWriter(client, db.NewLinearAgentActivityLogStore(mock), metrics, zerolog.Nop())
	return &writerTestRig{
		mock:    mock,
		client:  client,
		metrics: metrics,
		writer:  writer,
		orgID:   uuid.New(),
		rowID:   uuid.New(),
	}
}

func TestAgentActivityWriter_HappyPath(t *testing.T) {
	t.Parallel()
	rig := newWriterTestRig(t)

	// Reserve succeeds (Reserved=true).
	rig.mock.ExpectQuery("INSERT INTO linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "inserted"}).AddRow(uuid.New(), true))
	// Complete records the linear_activity_id.
	rig.mock.ExpectExec("UPDATE linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	rig.client.agentCreateRet = AgentActivityResult{ActivityID: "lin_act_42"}

	res, err := rig.writer.Emit(context.Background(), EmitInput{
		OrgID:             rig.orgID,
		AgentSessionRowID: rig.rowID,
		AgentSessionID:    "as_1",
		Activity: AgentMilestoneActivity{
			Type:    models.LinearAgentActivityResponse,
			Body:    "Opened PR #42.",
			IdemKey: "milestone:pr_opened",
		},
	})
	require.NoError(t, err)
	require.False(t, res.Skipped)
	require.Equal(t, "lin_act_42", res.LinearActivityID)
	require.Equal(t, 1, rig.client.agentCreateCalls,
		"happy path emits exactly once")
	require.NoError(t, rig.mock.ExpectationsWereMet())

	require.Len(t, rig.metrics.calls, 1)
	require.False(t, rig.metrics.calls[0].Skipped)
	require.Equal(t, "response", rig.metrics.calls[0].ActivityType)
}

func TestAgentActivityWriter_DuplicateShortCircuits(t *testing.T) {
	t.Parallel()
	rig := newWriterTestRig(t)

	// Reserve hits ON CONFLICT — Reserved=false.
	rig.mock.ExpectQuery("INSERT INTO linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "inserted"}).AddRow(uuid.New(), false))

	res, err := rig.writer.Emit(context.Background(), EmitInput{
		OrgID:             rig.orgID,
		AgentSessionRowID: rig.rowID,
		AgentSessionID:    "as_1",
		Activity: AgentMilestoneActivity{
			Type:    models.LinearAgentActivityResponse,
			Body:    "Opened PR #42.",
			IdemKey: "milestone:pr_opened",
		},
	})
	require.NoError(t, err)
	require.True(t, res.Skipped)
	require.Equal(t, 0, rig.client.agentCreateCalls,
		"duplicate idem_key must NOT emit to Linear — that's the whole point of the dedupe")
	require.NoError(t, rig.mock.ExpectationsWereMet())

	require.Len(t, rig.metrics.calls, 1)
	require.True(t, rig.metrics.calls[0].Skipped, "skipped emit must record skipped=true")
}

func TestAgentActivityWriter_LinearFailureKeepsReservation(t *testing.T) {
	t.Parallel()
	rig := newWriterTestRig(t)

	// Reserve succeeds.
	rig.mock.ExpectQuery("INSERT INTO linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "inserted"}).AddRow(uuid.New(), true))
	// No UPDATE expectation — Linear emit fails before Complete is called.

	rig.client.agentCreateErr = errors.New("linear is down")

	_, err := rig.writer.Emit(context.Background(), EmitInput{
		OrgID:             rig.orgID,
		AgentSessionRowID: rig.rowID,
		AgentSessionID:    "as_1",
		Activity: AgentMilestoneActivity{
			Type:    models.LinearAgentActivityResponse,
			Body:    "PR opened",
			IdemKey: "milestone:pr_opened",
		},
	})
	require.Error(t, err, "Linear failure surfaces to caller")
	require.NoError(t, rig.mock.ExpectationsWereMet(),
		"reservation row stays — replays will short-circuit on UNIQUE collision")
	// Failed emits do not record activity metrics — only the bounded
	// outcomes (sent / dedupe-skipped) hit the counter so the rate
	// dashboards stay readable.
	require.Empty(t, rig.metrics.calls,
		"failed emits do not record activity metrics; the failure is a system error, not a bounded outcome")
}

func TestAgentActivityWriter_PinSessionStatePropagates(t *testing.T) {
	t.Parallel()
	rig := newWriterTestRig(t)

	rig.mock.ExpectQuery("INSERT INTO linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "inserted"}).AddRow(uuid.New(), true))
	rig.mock.ExpectExec("UPDATE linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	_, err := rig.writer.Emit(context.Background(), EmitInput{
		OrgID:             rig.orgID,
		AgentSessionRowID: rig.rowID,
		AgentSessionID:    "as_1",
		Activity: AgentMilestoneActivity{
			Type:            models.LinearAgentActivityAction,
			Body:            "PR merged",
			Action:          "pr_merged",
			IdemKey:         "milestone:pr_merged",
			PinSessionState: "complete",
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, rig.client.stateUpdateCalls,
		"PinSessionState must trigger an AgentSessionUpdate so Linear's UI flips state immediately")
	require.Equal(t, "complete", rig.client.stateUpdateLast.State)
	require.NoError(t, rig.mock.ExpectationsWereMet())
}

func TestAgentActivityWriter_RejectsMissingFields(t *testing.T) {
	t.Parallel()
	rig := newWriterTestRig(t)

	_, err := rig.writer.Emit(context.Background(), EmitInput{
		OrgID:             rig.orgID,
		AgentSessionRowID: rig.rowID,
		// AgentSessionID intentionally empty
		Activity: AgentMilestoneActivity{
			Type:    models.LinearAgentActivityResponse,
			IdemKey: "x",
		},
	})
	require.Error(t, err, "missing AgentSessionID must surface as a clear error before any DB writes")
	require.Equal(t, 0, rig.client.agentCreateCalls)
}
