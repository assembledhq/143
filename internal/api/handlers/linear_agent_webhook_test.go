package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// fakeJobs records every Enqueue call so tests can pin the dedupe key,
// queue, and payload without exercising the real JobStore.
type fakeJobs struct {
	calls []fakeEnqueue
}

type fakeEnqueue struct {
	OrgID     uuid.UUID
	Queue     string
	JobType   string
	Payload   any
	Priority  int
	DedupeKey string
}

func (f *fakeJobs) Enqueue(_ context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	c := fakeEnqueue{OrgID: orgID, Queue: queue, JobType: jobType, Payload: payload, Priority: priority}
	if dedupeKey != nil {
		c.DedupeKey = *dedupeKey
	}
	f.calls = append(f.calls, c)
	return uuid.New(), nil
}

func TestLinearAgentDispatcher_FeatureOff(t *testing.T) {
	t.Parallel()
	jobs := &fakeJobs{}
	d := NewLinearAgentDispatcher(LinearAgentDispatcherConfig{
		Logger:         zerolog.Nop(),
		Jobs:           jobs,
		FeatureEnabled: false,
		// AgentSessions is non-nil so NewLinearAgentDispatcher returns
		// a constructed dispatcher; FeatureEnabled=false is what should
		// short-circuit Dispatch.
		AgentSessions: nil,
	})
	if d != nil {
		t.Fatalf("expected nil dispatcher when AgentSessions store is nil")
	}
}

func TestLinearAgentDispatcher_NonAgentEventIgnored(t *testing.T) {
	t.Parallel()
	// Build a dispatcher with the feature enabled but pass an event type
	// that isn't AgentSessionEvent. Should return ignored without
	// touching jobs or stores.
	jobs := &fakeJobs{}
	d := newDispatcherForTest(t, jobs, true /*featureEnabled*/)
	res := d.Dispatch(context.Background(), &models.Integration{ID: uuid.New(), OrgID: uuid.New()},
		LinearAgentEventAppUserNotification, []byte(`{"type":"AppUserNotification","action":"created","payload":{}}`))
	if res.Status != "ignored" {
		t.Fatalf("Status=%q want ignored", res.Status)
	}
	if len(jobs.calls) != 0 {
		t.Fatalf("expected no enqueue calls; got %d", len(jobs.calls))
	}
}

func TestLinearAgentDispatcher_PromptedWithoutCreatedEnqueuesRetryableJob(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	jobs := &fakeJobs{}
	enabled := true
	d := &LinearAgentDispatcher{
		logger:        zerolog.Nop(),
		agentSessions: db.NewLinearAgentSessionStore(mock),
		jobs:          jobs,
		settingsLoader: func(_ context.Context, _ uuid.UUID) (models.LinearAgentSettings, error) {
			return models.LinearAgentSettings{Enabled: &enabled}, nil
		},
		featureEnabled: true,
	}

	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(orgID, "as_racing").
		WillReturnError(pgx.ErrNoRows)

	body := []byte(`{"type":"AgentSessionEvent","action":"prompted","payload":{"agentSession":{"id":"as_racing","issueId":"iss_1","commentId":"comment_1","issue":{"id":"iss_1","teamId":"team_1","projectId":"project_1"}}}}`)
	res := d.Dispatch(context.Background(), &models.Integration{ID: integrationID, OrgID: orgID}, LinearAgentEventAgentSession, body)
	require.Equal(t, "agent_dispatched", res.Status, "prompted race should still enqueue a worker retry job after webhook ack")
	require.Len(t, jobs.calls, 1, "dispatcher should enqueue one prompted worker job")
	require.Equal(t, "linear_agent_event:as_racing:prompted:comment_1", jobs.calls[0].DedupeKey, "dedupe should preserve the prompted comment id")
	payload, ok := jobs.calls[0].Payload.(map[string]any)
	require.True(t, ok, "dispatcher should enqueue a map payload")
	require.Equal(t, "prompted", payload["action"], "worker payload should identify prompted action")
	require.Equal(t, "as_racing", payload["linear_agent_session_id"], "worker payload should carry the Linear AgentSession id for retry lookup")
	require.NoError(t, mock.ExpectationsWereMet(), "dispatcher should only attempt the row lookup before enqueueing the retry job")
}

func TestLinearAgentDispatcher_PerTeamEnableOverrideCanPassOrgDisabledGate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()
	jobs := &fakeJobs{}
	disabled := false
	teamEnabled := true
	d := &LinearAgentDispatcher{
		logger:        zerolog.Nop(),
		agentSessions: db.NewLinearAgentSessionStore(mock),
		jobs:          jobs,
		settingsLoader: func(_ context.Context, _ uuid.UUID) (models.LinearAgentSettings, error) {
			return models.LinearAgentSettings{
				Enabled:        &disabled,
				PerTeamEnabled: map[string]*bool{"ACS": &teamEnabled},
			}, nil
		},
		featureEnabled: true,
	}

	mock.ExpectQuery("INSERT INTO linear_agent_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at", "inserted",
		}).AddRow(
			rowID, orgID, integrationID, "as_1",
			"iss_1", "ACS-1",
			"", "",
			nil, "pending", &now,
			now, now, true,
		))

	body := []byte(`{"type":"AgentSessionEvent","action":"created","payload":{"agentSession":{"id":"as_1","issueId":"iss_1","issue":{"id":"iss_1","identifier":"ACS-1","teamId":"team_1"}}}}`)
	res := d.Dispatch(context.Background(), &models.Integration{ID: integrationID, OrgID: orgID}, LinearAgentEventAgentSession, body)
	require.Equal(t, "agent_dispatched", res.Status, "a true per-team override should pass the coarse dispatcher gate")
	require.Len(t, jobs.calls, 1, "dispatcher should enqueue the created worker job so the worker can apply team-key gating")
	require.NoError(t, mock.ExpectationsWereMet(), "dispatcher should upsert the AgentSession row")
}

func TestNewLinearAgentDispatcher_WiresBootstrapEmitterFromConfig(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	d := NewLinearAgentDispatcher(LinearAgentDispatcherConfig{
		Logger:         zerolog.Nop(),
		AgentSessions:  db.NewLinearAgentSessionStore(mock),
		Activities:     db.NewLinearAgentActivityLogStore(mock),
		Jobs:           &fakeJobs{},
		FeatureEnabled: true,
		ClientForOrg: func(_ context.Context, _ uuid.UUID) (linear.Client, error) {
			return nil, errors.New("not used by constructor")
		},
	})
	require.NotNil(t, d, "constructor should return a dispatcher when required stores are configured")
	require.NotNil(t, d.emitter, "constructor should wire the bootstrap emitter when activities and ClientForOrg are configured")
}

func TestSniffLinearEventType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want LinearAgentEventType
	}{
		{
			name: "agent session event",
			body: `{"type":"AgentSessionEvent","action":"created"}`,
			want: LinearAgentEventAgentSession,
		},
		{
			name: "app user notification",
			body: `{"type":"AppUserNotification","notification":{"type":"issueAssignedToYou"}}`,
			want: LinearAgentEventAppUserNotification,
		},
		{
			name: "ingestion-style payload",
			body: `{"type":"Issue","action":"create"}`,
			want: "",
		},
		{
			name: "junk",
			body: `not json`,
			want: "",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sniffLinearEventType([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// newDispatcherForTest constructs a dispatcher with non-nil feature-required
// fields so Dispatch flows past its construct-time short-circuits. The
// settings loader returns Enabled=true so the per-org gate doesn't block.
func newDispatcherForTest(t *testing.T, jobs linearAgentJobEnqueuer, featureEnabled bool) *LinearAgentDispatcher {
	t.Helper()
	enabled := true
	settings := func(_ context.Context, _ uuid.UUID) (models.LinearAgentSettings, error) {
		return models.LinearAgentSettings{Enabled: &enabled}, nil
	}
	clientFor := func(_ context.Context, _ uuid.UUID) (linear.Client, error) {
		return nil, nil
	}
	d := &LinearAgentDispatcher{
		logger:         zerolog.Nop(),
		jobs:           jobs,
		settingsLoader: settings,
		clientForOrg:   clientFor,
		featureEnabled: featureEnabled,
	}
	return d
}
