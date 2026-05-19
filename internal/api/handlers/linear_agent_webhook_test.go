package handlers

import (
	"context"
	"encoding/json"
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
	err   error
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
	if f.err != nil {
		return uuid.Nil, f.err
	}
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
		LinearAgentEventAppUserNotification, []byte(`{"type":"AppUserNotification","action":"created","payload":{}}`), nil)
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
	rowID := uuid.New()
	now := time.Now().UTC()
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
	// After the lookup miss the dispatcher persists a synthetic row via
	// UpsertOnCreated so downstream activity-log writes have a real
	// agent_session_row_id to bind to. The late-arriving `created`
	// re-upserts idempotently on the (org_id, linear_agent_session_id)
	// UNIQUE so this insert is safe.
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
			rowID, orgID, integrationID, "as_racing",
			"iss_1", "",
			"", "",
			nil, "pending", &now,
			now, now, true,
		))

	body := []byte(`{"type":"AgentSessionEvent","action":"prompted","payload":{"agentSession":{"id":"as_racing","issueId":"iss_1","commentId":"comment_1","issue":{"id":"iss_1","teamId":"team_1","projectId":"project_1"}},"agentActivity":{"body":"Please add regression coverage."}}}`)
	res := d.Dispatch(context.Background(), &models.Integration{ID: integrationID, OrgID: orgID}, LinearAgentEventAgentSession, body, nil)
	require.Equal(t, "agent_dispatched", res.Status, "prompted race should still enqueue a worker retry job after webhook ack")
	require.Len(t, jobs.calls, 1, "dispatcher should enqueue one prompted worker job")
	require.Equal(t, "linear_agent_event:"+orgID.String()+":as_racing:prompted:comment_1", jobs.calls[0].DedupeKey, "dedupe should preserve the prompted comment id and prefix the org id so two orgs sharing a Linear AgentSession ID can't collide")
	payload, ok := jobs.calls[0].Payload.(map[string]any)
	require.True(t, ok, "dispatcher should enqueue a map payload")
	require.Equal(t, "prompted", payload["action"], "worker payload should identify prompted action")
	require.Equal(t, "as_racing", payload["linear_agent_session_id"], "worker payload should carry the Linear AgentSession id for retry lookup")
	require.Equal(t, "Please add regression coverage.", payload["linear_prompt_body"], "worker payload should carry Linear's prompted agentActivity.body")
	require.Equal(t, rowID.String(), payload["agent_session_row_id"], "worker payload should carry the persisted synthetic row id rather than a zero uuid")
	require.NoError(t, mock.ExpectationsWereMet(), "dispatcher should look up, then upsert a synthetic row before enqueueing the retry job")
}

func TestLinearAgentDispatcher_CreatedAcceptsTopLevelAgentSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()
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
			rowID, orgID, integrationID, "as_top_level",
			"iss_1", "ACS-1",
			"app_user_1", "user_1",
			nil, "pending", &now,
			now, now, true,
		))

	body := []byte(`{"type":"AgentSessionEvent","action":"created","appUserId":"app_user_1","agentSession":{"id":"as_top_level","issueId":"iss_1","issue":{"id":"iss_1","identifier":"ACS-1","teamId":"team_1"},"creator":{"id":"user_1"}}}`)
	res := d.Dispatch(context.Background(), &models.Integration{ID: integrationID, OrgID: orgID}, LinearAgentEventAgentSession, body, nil)

	require.Equal(t, "agent_dispatched", res.Status, "dispatcher should accept Linear's top-level agentSession webhook shape")
	require.Len(t, jobs.calls, 1, "dispatcher should enqueue one created worker job")
	payload, ok := jobs.calls[0].Payload.(map[string]any)
	require.True(t, ok, "dispatcher should enqueue a map payload")
	require.Equal(t, "as_top_level", payload["linear_agent_session_id"], "worker payload should carry the top-level Linear AgentSession id")
	require.Equal(t, "created", payload["action"], "worker payload should identify created action")
	require.NoError(t, mock.ExpectationsWereMet(), "dispatcher should upsert the AgentSession row from the top-level payload")
}

func TestLinearAgentDispatcher_EnqueueFailureReturnsRetryableStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()
	jobs := &fakeJobs{err: errors.New("job store down")}
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
			rowID, orgID, integrationID, "as_enqueue_fail",
			"iss_1", "ACS-1",
			"", "",
			nil, "pending", &now,
			now, now, true,
		))

	body := []byte(`{"type":"AgentSessionEvent","action":"created","payload":{"agentSession":{"id":"as_enqueue_fail","issueId":"iss_1","issue":{"id":"iss_1","identifier":"ACS-1","teamId":"team_1"}}}}`)
	res := d.Dispatch(context.Background(), &models.Integration{ID: integrationID, OrgID: orgID}, LinearAgentEventAgentSession, body, nil)
	require.Equal(t, "enqueue_failed", res.Status, "enqueue failure must be visible to the webhook handler so the delivery is not marked processed")
	require.Error(t, res.Err, "enqueue failure should be returned in the dispatch result for logging/response handling")
	require.Len(t, jobs.calls, 1, "dispatcher should have attempted exactly one enqueue")
	require.NoError(t, mock.ExpectationsWereMet(), "dispatcher should upsert before attempting the enqueue")
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
	res := d.Dispatch(context.Background(), &models.Integration{ID: integrationID, OrgID: orgID}, LinearAgentEventAgentSession, body, nil)
	require.Equal(t, "agent_dispatched", res.Status, "a true per-team override should pass the coarse dispatcher gate")
	require.Len(t, jobs.calls, 1, "dispatcher should enqueue the created worker job so the worker can apply team-key gating")
	require.NoError(t, mock.ExpectationsWereMet(), "dispatcher should upsert the AgentSession row")
}

// TestLinearAgentDispatcher_SettingsLoaderErrorSurfacesAsRetryable pins the
// dispatcher behavior on a transient settings-loader failure: surface the
// error via DispatchResult.Err so the handler 500s and Linear redelivers,
// rather than silently returning "feature_off" and dropping the event.
func TestLinearAgentDispatcher_SettingsLoaderErrorSurfacesAsRetryable(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	jobs := &fakeJobs{}
	loaderErr := errors.New("connection refused")
	d := &LinearAgentDispatcher{
		logger:        zerolog.Nop(),
		agentSessions: db.NewLinearAgentSessionStore(mock),
		jobs:          jobs,
		settingsLoader: func(_ context.Context, _ uuid.UUID) (models.LinearAgentSettings, error) {
			return models.LinearAgentSettings{}, loaderErr
		},
		featureEnabled: true,
	}

	body := []byte(`{"type":"AgentSessionEvent","action":"created","payload":{"agentSession":{"id":"as_1","issueId":"iss_1","issue":{"id":"iss_1","identifier":"ACS-1","teamId":"team_1"}}}}`)
	res := d.Dispatch(context.Background(), &models.Integration{ID: integrationID, OrgID: orgID}, LinearAgentEventAgentSession, body, nil)

	require.Equal(t, "retryable_error", res.Status,
		"transient settings-loader failure must not be silently treated as feature_off")
	require.Error(t, res.Err,
		"settings-loader failure must propagate via DispatchResult.Err so the handler 500s and Linear redelivers")
	require.ErrorIs(t, res.Err, loaderErr,
		"the underlying loader error should be wrapped, not swallowed")
	require.Empty(t, jobs.calls,
		"no worker job should be enqueued when settings could not be resolved")
	require.NoError(t, mock.ExpectationsWereMet(),
		"dispatcher must short-circuit before touching the AgentSession row when settings cannot be loaded")
}

func TestLinearAgentDispatcher_AppUserMismatchUsesCredentialConfig(t *testing.T) {
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
		credentialLookup: &mockWebhookSecretLookup{
			cred: &models.DecryptedCredential{
				ID:       uuid.New(),
				OrgID:    orgID,
				Provider: models.ProviderLinear,
				Config:   models.LinearConfig{AppUserID: "app_user_expected"},
				Status:   "active",
			},
		},
		featureEnabled: true,
	}

	body := []byte(`{"type":"AgentSessionEvent","action":"created","appUserId":"app_user_other","payload":{"agentSession":{"id":"as_1","issueId":"iss_1","issue":{"id":"iss_1","identifier":"ACS-1","teamId":"team_1"}}}}`)
	res := d.Dispatch(context.Background(), &models.Integration{
		ID:     integrationID,
		OrgID:  orgID,
		Config: json.RawMessage(`{"workspace_id":"workspace_1"}`),
	}, LinearAgentEventAgentSession, body, nil)

	require.Equal(t, "ignored", res.Status, "dispatcher should ignore events addressed to a different stored Linear app user")
	require.Empty(t, jobs.calls, "dispatcher should not enqueue jobs for a mismatched Linear app user")
	require.NoError(t, mock.ExpectationsWereMet(), "app-user mismatch should short-circuit before touching agent session rows")
}

func TestLinearAgentDispatcher_UsesParsedEnvelopeWhenProvided(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	integrationID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()
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
			rowID, orgID, integrationID, "as_parsed",
			"iss_1", "ACS-1",
			"", "user_1",
			nil, "pending", &now,
			now, now, true,
		))

	var env linearAgentEventEnvelope
	env.Type = string(LinearAgentEventAgentSession)
	env.Action = string(linearAgentActionCreated)
	env.Payload.AgentSession.ID = "as_parsed"
	env.Payload.AgentSession.IssueID = "iss_1"
	env.Payload.AgentSession.Issue.ID = "iss_1"
	env.Payload.AgentSession.Issue.Identifier = "ACS-1"
	env.Payload.AgentSession.Creator.ID = "user_1"

	res := d.Dispatch(context.Background(), &models.Integration{ID: integrationID, OrgID: orgID}, LinearAgentEventAgentSession, []byte(`not json`), &env)
	require.Equal(t, "agent_dispatched", res.Status, "dispatcher should reuse the parsed envelope instead of reparsing the body")
	require.Len(t, jobs.calls, 1, "dispatcher should enqueue one worker job from the parsed envelope")
	payload, ok := jobs.calls[0].Payload.(map[string]any)
	require.True(t, ok, "dispatcher should enqueue a map payload")
	require.Equal(t, "as_parsed", payload["linear_agent_session_id"], "worker payload should come from the parsed envelope")
	require.NoError(t, mock.ExpectationsWereMet(), "dispatcher should upsert the AgentSession row using the parsed envelope")
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

func TestLinearAgentBootstrapWriterDiscardsReservationOnFailedEmit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()
	mock.ExpectQuery("INSERT INTO linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "inserted"}).AddRow(uuid.New(), true))
	mock.ExpectExec("DELETE FROM linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	writer := newLinearAgentBootstrapWriter(
		func(context.Context, uuid.UUID) (linear.Client, error) {
			return bootstrapWriterLinearClient{agentActivityErr: errors.New("linear timeout")}, nil
		},
		db.NewLinearAgentActivityLogStore(mock),
		zerolog.Nop(),
	)

	_, err = writer.Emit(context.Background(), linear.EmitInput{
		OrgID:             orgID,
		AgentSessionRowID: rowID,
		AgentSessionID:    "as_1",
		Activity:          linear.BootstrapActivity("ACS-1"),
	})
	require.Error(t, err, "bootstrap emit should still surface Linear failures to the dispatcher")
	require.NoError(t, mock.ExpectationsWereMet(), "failed bootstrap emits should discard the reserved idem slot so webhook retries can re-emit")
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

func TestSniffLinearEventEnvelope_NormalizesTopLevelAgentSession(t *testing.T) {
	t.Parallel()

	eventType, env := sniffLinearEventEnvelope([]byte(`{"type":"AgentSessionEvent","action":"created","agentSession":{"id":"as_top_level","issueId":"iss_1","issue":{"identifier":"ACS-1"}}}`))

	require.Equal(t, LinearAgentEventAgentSession, eventType, "sniffing should identify top-level AgentSessionEvent payloads")
	require.NotNil(t, env, "sniffing should return a parsed envelope")
	require.Equal(t, "as_top_level", env.Payload.AgentSession.ID, "sniffing should normalize top-level agentSession into payload.agentSession")
	require.Equal(t, "iss_1", env.Payload.AgentSession.IssueID, "sniffing should preserve the top-level issue id")
	require.Equal(t, "ACS-1", env.Payload.AgentSession.Issue.Identifier, "sniffing should preserve the issue identifier")
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

type bootstrapWriterLinearClient struct {
	agentActivityErr error
}

func (c bootstrapWriterLinearClient) FetchIssue(context.Context, string) (*linear.FetchedIssue, error) {
	return nil, errors.New("not used")
}

func (c bootstrapWriterLinearClient) ListTeamKeys(context.Context) ([]linear.TeamKeyInfo, error) {
	return nil, errors.New("not used")
}

func (c bootstrapWriterLinearClient) CreateOrUpdateAttachment(context.Context, linear.AttachmentWriteInput) (linear.AttachmentResult, error) {
	return linear.AttachmentResult{}, errors.New("not used")
}

func (c bootstrapWriterLinearClient) CreateComment(context.Context, string, string) (string, error) {
	return "", errors.New("not used")
}

func (c bootstrapWriterLinearClient) UpdateComment(context.Context, string, string) error {
	return errors.New("not used")
}

func (c bootstrapWriterLinearClient) FindRecentBotCommentByURL(context.Context, string, string) (string, error) {
	return "", errors.New("not used")
}

func (c bootstrapWriterLinearClient) WorkflowStateForType(context.Context, string, []string, string) (*linear.WorkflowState, error) {
	return nil, errors.New("not used")
}

func (c bootstrapWriterLinearClient) UpdateIssueState(context.Context, string, string) error {
	return errors.New("not used")
}

func (c bootstrapWriterLinearClient) IssueRecentHumanEdits(context.Context, string, time.Time) (bool, error) {
	return false, errors.New("not used")
}

func (c bootstrapWriterLinearClient) HasGitHubIntegrationAttachment(context.Context, string) (bool, error) {
	return false, errors.New("not used")
}

func (c bootstrapWriterLinearClient) AgentActivityCreate(context.Context, linear.AgentActivityInput) (linear.AgentActivityResult, error) {
	if c.agentActivityErr != nil {
		return linear.AgentActivityResult{}, c.agentActivityErr
	}
	return linear.AgentActivityResult{ActivityID: "act_1"}, nil
}

func (c bootstrapWriterLinearClient) AgentSessionUpdate(context.Context, linear.AgentSessionUpdateInput) error {
	return nil
}

func (c bootstrapWriterLinearClient) AgentSessionGet(context.Context, string) (*linear.FetchedAgentSession, error) {
	return nil, linear.ErrAgentSessionNotFound
}

func (c bootstrapWriterLinearClient) FetchComment(context.Context, string) (*linear.FetchedComment, error) {
	return nil, linear.ErrCommentNotFound
}
