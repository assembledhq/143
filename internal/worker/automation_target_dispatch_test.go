package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	automationservice "github.com/assembledhq/143/internal/services/automations"
)

type fakeAutomationTargetDispatcher struct {
	applies bool
	outcome automationservice.DispatchOutcome
	err     error
	inputs  []automationservice.DispatchInput
}

func (f *fakeAutomationTargetDispatcher) Applies(in automationservice.DispatchInput) bool {
	return f.applies
}

func (f *fakeAutomationTargetDispatcher) Dispatch(_ context.Context, in automationservice.DispatchInput) (automationservice.DispatchOutcome, error) {
	f.inputs = append(f.inputs, in)
	return f.outcome, f.err
}

func TestAutomationRunHandler_PerTargetDispatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		outcome        automationservice.DispatchOutcome
		err            error
		wantRetry      bool
		wantRetryAfter time.Duration
		wantMaxWait    time.Duration
		wantErr        bool
	}{
		{name: "reserved runs finish the job", outcome: automationservice.DispatchOutcome{Kind: automationservice.DispatchReserved, JobID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), ContinuationMode: models.AutomationRunContinuationFresh}},
		{name: "waiting runs keep the job polling without spending attempts", outcome: automationservice.DispatchOutcome{Kind: automationservice.DispatchWaiting, Note: "target busy"}, wantRetry: true, wantRetryAfter: automationWaitingPollInterval, wantMaxWait: automationWaitingPollWindow},
		{name: "terminalized runs finish the job", outcome: automationservice.DispatchOutcome{Kind: automationservice.DispatchTerminalized, OutcomeReason: models.AutomationRunOutcomePRClosed}},
		{name: "undecidable runs retry with the dispatcher's backoff and bound", outcome: automationservice.DispatchOutcome{Kind: automationservice.DispatchRetry, RetryAfter: 15 * time.Second, MaxWait: 3 * time.Minute, Note: "snapshot upload in flight"}, wantRetry: true, wantRetryAfter: 15 * time.Second, wantMaxWait: 3 * time.Minute},
		{name: "dispatch errors surface for the job's retry", err: errors.New("boom"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.Automations = db.NewAutomationStore(mock)
			stores.AutomationRuns = db.NewAutomationRunStore(mock)

			orgID := uuid.New()
			automationID := uuid.New()
			runID := uuid.New()
			repoID := uuid.New()
			targetID := uuid.New()
			now := time.Now()
			payload, err := json.Marshal(map[string]string{
				"org_id": orgID.String(), "automation_id": automationID.String(), "automation_run_id": runID.String(),
			})
			require.NoError(t, err, "marshal payload should succeed")

			runValues := []any{
				runID, automationID, orgID, now, models.AutomationTriggeredByGitHub,
				nil, nil, nil, nil, nil, []byte("{}"), "goal", []byte(`{"github":{"pull_request_number":42,"head_sha":"abc"}}`),
				models.AutomationRunStatusPending, nil, nil, nil, now, now,
				&targetID,
			}
			mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(automationRunRows(runValues...))
			mock.ExpectQuery(`SELECT .+ FROM automations WHERE id = @id`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(automationRows(
					automationID, orgID, &repoID, "front-end review", "review", nil,
					models.AutomationIconTypeEmoji, "⚙️",
					nil, nil, nil, "sequential", 1, "main", models.AutomationIdentityScopeOrg, models.AutomationPublishPolicyNone, 0,
					models.AutomationScheduleNone, nil, nil, nil, nil, "UTC",
					[]string{"github.pull_request.updated"}, []byte("{}"),
					nil, nil, true, nil, nil, nil,
					50, []byte("{}"), now, now, nil,
					models.AutomationSessionContinuityPerTarget,
				))

			dispatcher := &fakeAutomationTargetDispatcher{applies: true, outcome: tt.outcome, err: tt.err}
			handler := newAutomationRunHandler(stores, &Services{AutomationTargets: dispatcher}, zerolog.Nop())
			err = handler(context.Background(), models.JobTypeAutomationRun, payload)

			switch {
			case tt.wantErr:
				require.Error(t, err, "dispatcher errors leave the run pending for retry")
			case tt.wantRetry:
				var retryable *RetryableError
				require.ErrorAs(t, err, &retryable, "undecided runs retry")
				require.NotNil(t, retryable.RetryAfter, "retry carries a delay")
				require.Equal(t, tt.wantRetryAfter, *retryable.RetryAfter, "retry uses the expected delay")
				require.False(t, retryable.ConsumeAttempt, "polling and deferrals never spend the attempt budget")
				require.NotNil(t, retryable.MaxRetryDuration, "retry is bounded")
				require.Equal(t, tt.wantMaxWait, *retryable.MaxRetryDuration, "retry bound matches the wait window")
			default:
				require.NoError(t, err, "decided runs finish the job")
			}
			require.Len(t, dispatcher.inputs, 1, "the ownership transaction runs once")
			in := dispatcher.inputs[0]
			require.Equal(t, runID, in.Run.ID, "dispatch receives the run")
			require.Equal(t, automationID, in.Automation.ID, "dispatch receives the automation")
			require.NotNil(t, in.SessionTemplate, "dispatch receives the fresh-session template")
			require.Equal(t, &runID, in.SessionTemplate.AutomationRunID, "template links the run")
			require.Equal(t, &repoID, in.SessionTemplate.RepositoryID, "template carries the repository")
			require.False(t, in.KillSwitch, "kill switch is off by default")
			require.NoError(t, mock.ExpectationsWereMet(), "the per-run claim and session insert must not run")
		})
	}
}

func TestAutomationContinuityDisabled(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "", want: false},
		{value: "0", want: false},
		{value: "1", want: true},
		{value: "true", want: true},
		{value: "TRUE", want: true},
		{value: "yes", want: false},
	}
	for _, tt := range tests {
		t.Run("value="+tt.value, func(t *testing.T) {
			t.Setenv(automationContinuityKillSwitchEnv, tt.value)
			require.Equal(t, tt.want, automationContinuityDisabled(), "kill switch accepts 1 and true only")
		})
	}
}

// TestAutomationTurnJobPayloadContract pins the producer-to-consumer field
// contract: the dispatcher's payload must decode into the worker's
// continue_session input with every per-target field intact.
func TestAutomationTurnJobPayloadContract(t *testing.T) {
	t.Parallel()
	payload := automationservice.AutomationTurnJobPayload{
		SessionID: uuid.NewString(), OrgID: uuid.NewString(), ThreadID: uuid.NewString(),
		AutomationRunID: uuid.NewString(), TargetGeneration: 3, ContinuationMode: "continued",
		HeadSHA: "abc", PullRequestNumber: 42, StructuredPrompt: "review",
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err, "payload marshals")
	var input continueSessionJobInput
	require.NoError(t, json.Unmarshal(raw, &input), "the worker decodes the dispatcher's payload")
	require.Equal(t, payload.SessionID, input.SessionID, "session id round-trips")
	require.Equal(t, payload.OrgID, input.OrgID, "org id round-trips")
	require.Equal(t, payload.ThreadID, input.ThreadID, "thread id round-trips")
	require.Equal(t, payload.AutomationRunID, input.AutomationRunID, "run id round-trips")
	require.Equal(t, payload.TargetGeneration, input.TargetGeneration, "generation round-trips as a number")
	require.Equal(t, payload.ContinuationMode, input.ContinuationMode, "continuation mode round-trips")
	require.Equal(t, payload.HeadSHA, input.HeadSHA, "head round-trips")
	require.Equal(t, payload.PullRequestNumber, input.PullRequestNumber, "pull request number round-trips as a number")
	require.Equal(t, payload.StructuredPrompt, input.StructuredPrompt, "prompt round-trips")
}
