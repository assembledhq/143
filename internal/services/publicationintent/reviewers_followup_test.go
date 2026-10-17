package publicationintent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type reviewerUnavailableOrganizationStore struct{}

func (reviewerUnavailableOrganizationStore) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	return models.Organization{}, errors.New("organization lookup unavailable")
}

func setReviewerDraftDefault(t *testing.T, f *coordinatorFixture, draft bool) {
	t.Helper()
	settings, err := json.Marshal(models.OrgSettings{PRDraftDefault: draft})
	require.NoError(t, err, "draft policy fixture should encode")
	f.coordinator.organizations = coordinatorOrganizationStore{organization: models.Organization{ID: f.orgID, Settings: settings}}
}

func TestAutomaticReviewerMetadataRejectsConfiguredDraftBeforeStaging(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                              string
		draftDefault, draftFirst, wantErr bool
	}{
		{name: "configured draft cannot request teams", draftDefault: true, wantErr: true},
		{name: "draft-first finalizes before team requests", draftDefault: true, draftFirst: true},
		{name: "ready publication supports teams"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, intents, _ := automationReviewerFixture(t)
			var logs bytes.Buffer
			f.coordinator.logger = zerolog.New(&logs)
			setReviewerDraftDefault(t, f, tt.draftDefault)
			if tt.draftFirst {
				f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{FullName: "acme/repo", Settings: json.RawMessage(`{"pr_handoff_mode":"draft_first"}`)}})
			}
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{TeamReviewers: []string{"team"}})
			if tt.wantErr {
				var intentErr *Error
				require.ErrorAs(t, err, &intentErr, "unsupported automatic reviewer metadata must be rejected while the agent can still react")
				require.Equal(t, ErrorInvalidPublicationOptions, intentErr.Code, "staging rejection should explain the configured draft policy")
				require.Equal(t, 0, intents.stages, "unsupported teams must not poison staged completion metadata")
			} else {
				require.NoError(t, err, "ready or draft-first automatic policy can stage teams")
				require.Equal(t, 1, intents.stages, "supported metadata should stage once")
			}
			require.Equal(t, 0, intents.freezes, "agent metadata admission must never freeze completion early")
			if tt.wantErr {
				result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy})
				require.NoError(t, err, "rejected optional metadata must leave normal automatic publication available")
				require.Equal(t, ResultPRQueued, result.Status, "configured automatic publication should still queue")
				require.NotContains(t, f.jobs.payload, "team_reviewers", "rejected metadata must not request teams")
				require.NotContains(t, f.jobs.payload, "draft", "completion must preserve the configured draft default")
			}
		})
	}
}

func TestAutomaticPublicationOmitsIncompatibleStagedReviewers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		stageDraftFirst bool
	}{
		{name: "draft default changed after staging"},
		{name: "draft-first changed to pre-publish", stageDraftFirst: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, intents, _ := automationReviewerFixture(t)
			var logs bytes.Buffer
			f.coordinator.logger = zerolog.New(&logs)
			if tt.stageDraftFirst {
				f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{FullName: "acme/repo", Settings: json.RawMessage(`{"pr_handoff_mode":"draft_first"}`)}})
			}
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{TeamReviewers: []string{"team"}})
			require.NoError(t, err, "original ready-capable policy should stage metadata")
			setReviewerDraftDefault(t, f, true)
			f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{FullName: "acme/repo"}})
			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy})
			require.NoError(t, err, "optional team metadata must not prevent normal automatic publication after policy drift")
			require.Equal(t, ResultPRQueued, result.Status, "completion must queue the configured publication")
			require.Equal(t, 1, intents.freezes, "completion should freeze the original staged metadata once")
			require.NotContains(t, f.jobs.payload, "team_reviewers", "incompatible optional teams must not enter the executable publication payload")
			require.NotContains(t, f.jobs.payload, "draft", "omitting incompatible metadata must not override the configured draft policy")
			require.NotNil(t, result.Reason, "completion must report that optional team requests were omitted")
			require.Equal(t, "team reviewers were omitted because automatic publication is configured to remain a draft; configured publication policy is unchanged", *result.Reason, "the typed completion result must explain the metadata tradeoff")
			require.Contains(t, logs.String(), `"level":"warn"`, "omitting previously staged teams must leave an operational warning")
		})
	}
}

func TestAutomaticPublicationPreservesReviewerIntentWhenReadinessUnavailable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		organizations OrganizationStore
	}{
		{name: "policy lookup failed", organizations: reviewerUnavailableOrganizationStore{}},
		{name: "organization store unavailable"},
		{name: "policy settings invalid", organizations: coordinatorOrganizationStore{organization: models.Organization{Settings: json.RawMessage(`{`)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, intents, _ := automationReviewerFixture(t)
			var logs bytes.Buffer
			f.coordinator.logger = zerolog.New(&logs)
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{TeamReviewers: []string{"team"}})
			require.NoError(t, err, "known ready policy should accept metadata before infrastructure becomes unavailable")
			staged := intents.intent
			f.coordinator.organizations = tt.organizations
			request := RequestPullRequest{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy}
			_, err = f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, request)
			var intentErr *Error
			require.ErrorAs(t, err, &intentErr, "uncertain readiness must remain an actionable publication failure")
			require.Equal(t, ErrorPublicationFailed, intentErr.Code, "infrastructure uncertainty must not be treated as an incompatible configured draft")
			require.Equal(t, staged, intents.intent, "failed policy resolution must preserve all staged reviewer intent for retry")
			require.Nil(t, f.publications.captured, "uncertain readiness must not commit a publication with discarded teams")
			require.Nil(t, f.jobs.payload, "uncertain readiness must not queue incomplete publication work")
			require.NotContains(t, logs.String(), "omitting optional team reviewers", "transient failures must not be reported as intentional team omission")
			setReviewerDraftDefault(t, f, false)
			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, request)
			require.NoError(t, err, "retry after policy recovery should reuse durable staged metadata")
			require.Equal(t, ResultPRQueued, result.Status, "recovered publication should queue the original requested teams")
			require.Equal(t, []any{"acme/team"}, f.jobs.payload["team_reviewers"], "retry must retain teams rather than freezing their omission")
			require.Equal(t, false, f.jobs.payload["draft"], "recovered publication must freeze its confirmed ready choice")
			require.Equal(t, 1, intents.stages, "recovery must not require the agent to stage metadata again")
		})
	}
}

func TestDraftFirstReviewerReopenRetainsFinalizationGate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state models.SessionPublicationState
	}{
		{name: "no-op before draft creation", state: models.SessionPublicationStateCompletedNoop},
		{name: "failure before draft creation", state: models.SessionPublicationStateTerminalFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
			setReviewerDraftDefault(t, f, true)
			f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{FullName: "acme/repo", Settings: json.RawMessage(`{"pr_handoff_mode":"draft_first"}`)}})
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{TeamReviewers: []string{"team"}})
			require.NoError(t, err, "temporary draft with finalization gate should accept team intent")
			f.publications.captured.State = tt.state
			f.jobs.payload = nil
			f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{FullName: "acme/repo"}})
			_, err = f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{Source: models.SessionPublicationSourceUser, TriggerKind: models.SessionPublicationTriggerExplicitAction, RequestedByUserID: &f.userID, RequestedRole: string(models.RoleMember)})
			require.NoError(t, err, "reopening must preserve the existing finalization contract despite changed repository defaults")
			require.NotNil(t, f.jobs.payload, "terminal draft-first reopen must rebuild and queue its payload")
			require.Equal(t, true, f.jobs.payload["draft"], "draft-first replay should retain its temporary draft")
			require.Equal(t, string(models.PRHandoffModeDraftFirst), f.jobs.payload["publication_handoff_mode"], "temporary draft must keep the finalization gate needed before requesting teams")
			require.Equal(t, models.PRHandoffModeDraftFirst, f.publications.captured.HandoffMode, "ledger must preserve the same finalization gate as the rebuilt job")
			require.Equal(t, []any{"acme/team"}, f.jobs.payload["team_reviewers"], "reopening must retain teams with their original finalization contract")
		})
	}
}

func TestReviewerReadinessSurvivesRebuiltPublication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		state         models.SessionPublicationState
		changeHandoff bool
	}{
		{name: "needs-human explicit publication", state: models.SessionPublicationStateReviewPending},
		{name: "needs-human after handoff change", state: models.SessionPublicationStateReviewPending, changeHandoff: true},
		{name: "completed-noop reopen", state: models.SessionPublicationStateCompletedNoop},
		{name: "completed-noop after handoff change", state: models.SessionPublicationStateCompletedNoop, changeHandoff: true},
		{name: "terminal-failed reopen", state: models.SessionPublicationStateTerminalFailed},
		{name: "terminal-failed after handoff change", state: models.SessionPublicationStateTerminalFailed, changeHandoff: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
			f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{FullName: "acme/repo"}})
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{TeamReviewers: []string{"team"}})
			require.NoError(t, err, "first ready publication should freeze requested teams")
			require.Equal(t, false, f.jobs.payload["draft"], "fixture must start with an explicit durable ready choice")
			f.publications.captured.State = tt.state
			f.publications.captured.ReviewGateState = models.SessionPublicationReviewGateNeedsHuman
			f.jobs.payload = nil
			setReviewerDraftDefault(t, f, true)
			if tt.changeHandoff {
				f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{FullName: "acme/repo", Settings: json.RawMessage(`{"pr_handoff_mode":"draft_first"}`)}})
			}
			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{Source: models.SessionPublicationSourceUser, TriggerKind: models.SessionPublicationTriggerExplicitAction, RequestedByUserID: &f.userID, RequestedRole: string(models.RoleMember)})
			require.NoError(t, err, "explicit reopen must retain the publication's frozen ready contract")
			require.Equal(t, ResultPRQueued, result.Status, "explicit publication should queue after rebuilding its payload")
			require.NotNil(t, f.jobs.payload, "test must exercise payload rebuild rather than rejoining the existing intent")
			require.Equal(t, []any{"acme/team"}, f.jobs.payload["team_reviewers"], "rebuilt publication must retain the frozen teams")
			require.Equal(t, false, f.jobs.payload["draft"], "changed draft defaults must not overwrite frozen readiness")
			require.Equal(t, string(models.PRHandoffModePrePublish), f.jobs.payload["publication_handoff_mode"], "changed handoff defaults must not reinterpret the frozen ready request")
			require.Equal(t, models.PRHandoffModePrePublish, f.publications.captured.HandoffMode, "ledger and rebuilt payload must agree on the frozen handoff")
		})
	}
}
