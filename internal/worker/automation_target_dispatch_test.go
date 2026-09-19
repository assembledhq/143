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
		name      string
		outcome   automationservice.DispatchOutcome
		err       error
		wantRetry bool
		wantErr   bool
	}{
		{name: "reserved runs finish the job", outcome: automationservice.DispatchOutcome{Kind: automationservice.DispatchReserved, JobID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), ContinuationMode: models.AutomationRunContinuationFresh}},
		{name: "waiting runs finish the job", outcome: automationservice.DispatchOutcome{Kind: automationservice.DispatchWaiting, Note: "target busy"}},
		{name: "terminalized runs finish the job", outcome: automationservice.DispatchOutcome{Kind: automationservice.DispatchTerminalized, OutcomeReason: models.AutomationRunOutcomePRClosed}},
		{name: "undecidable runs retry with the dispatcher's backoff", outcome: automationservice.DispatchOutcome{Kind: automationservice.DispatchRetry, RetryAfter: 15 * time.Second, Note: "snapshot upload in flight"}, wantRetry: true},
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
				require.ErrorAs(t, err, &retryable, "undecidable runs retry")
				require.NotNil(t, retryable.RetryAfter, "retry carries the dispatcher's backoff")
				require.Equal(t, tt.outcome.RetryAfter, *retryable.RetryAfter, "retry uses the dispatcher's backoff")
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
