package publicationintent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestAutomationPRContentStaging(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		req      RequestPullRequest
		expected func(f *coordinatorFixture, run models.AutomationRun) models.SessionPRIntent
		wantErr  bool
	}{
		{
			name: "reviewers title and body stage together",
			req:  RequestPullRequest{TeamReviewers: []string{"team"}, Title: new("  feature-brief: Plan exports \t"), Body: new("Source plan: docs/plans/exports.md\n")},
			expected: func(f *coordinatorFixture, run models.AutomationRun) models.SessionPRIntent {
				return models.SessionPRIntent{OrgID: f.orgID, SessionID: f.sessionID, AutomationRunID: run.ID, ChangesetID: f.changesetID, TeamReviewers: []string{"acme/team"}, Title: new("feature-brief: Plan exports"), Body: new("Source plan: docs/plans/exports.md\n")}
			},
		},
		{
			name: "title alone leaves reviewers and body unchanged",
			req:  RequestPullRequest{Title: new("feature-brief: Plan exports")},
			expected: func(f *coordinatorFixture, run models.AutomationRun) models.SessionPRIntent {
				return models.SessionPRIntent{OrgID: f.orgID, SessionID: f.sessionID, AutomationRunID: run.ID, ChangesetID: f.changesetID, Title: new("feature-brief: Plan exports")}
			},
		},
		{
			name: "body alone leaves reviewers and title unchanged",
			req:  RequestPullRequest{Body: new("## Summary\n\nSource plan: docs/plans/exports.md")},
			expected: func(f *coordinatorFixture, run models.AutomationRun) models.SessionPRIntent {
				return models.SessionPRIntent{OrgID: f.orgID, SessionID: f.sessionID, AutomationRunID: run.ID, ChangesetID: f.changesetID, Body: new("## Summary\n\nSource plan: docs/plans/exports.md")}
			},
		},
		{name: "multiline title rejected", req: RequestPullRequest{Title: new("first\nsecond")}, wantErr: true},
		{name: "blank title rejected", req: RequestPullRequest{Title: new("  ")}, wantErr: true},
		{name: "oversized title rejected", req: RequestPullRequest{Title: new(strings.Repeat("a", models.MaxStagedPRTitleBytes+1))}, wantErr: true},
		{name: "blank body rejected", req: RequestPullRequest{Body: new("\n\t")}, wantErr: true},
		{name: "oversized body rejected", req: RequestPullRequest{Body: new(strings.Repeat("a", models.MaxStagedPRBodyLength+1))}, wantErr: true},
		{name: "draft override still rejected with text", req: RequestPullRequest{Title: new("docs: Report"), Draft: coordinatorBoolPtr(false)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, intents, run := automationReviewerFixture(t)
			req := tt.req
			req.Source = models.SessionPublicationSourceAgentTool
			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, req)
			if tt.wantErr {
				var intentErr *Error
				require.ErrorAs(t, err, &intentErr, "invalid staged text must be rejected while the agent can still correct it")
				require.Equal(t, ErrorInvalidPublicationOptions, intentErr.Code, "invalid staged text should map to a client error")
				require.Equal(t, 0, intents.stages, "rejected text must not be staged")
			} else {
				require.NoError(t, err, "authorized automation may stage pull request text")
				require.Equal(t, &PublicationIntentResult{Status: ResultAwaitingAutomaticPublication, SessionID: f.sessionID}, result, "staging text must not publish early")
				require.Equal(t, tt.expected(f, run), intents.intent, "staging should normalize the title and leave omitted fields nil for the store to keep")
			}
			require.Equal(t, 0, intents.freezes, "agent staging must never freeze completion early")
			require.Nil(t, f.publications.captured, "staging must never create a requested publication row")
			require.Nil(t, f.jobs.payload, "staging must never enqueue early publication work")
		})
	}
}

func TestAutomationPRContentAfterPublicationReportsFrozenMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  RequestPullRequest
	}{
		{name: "late title", req: RequestPullRequest{Title: new("docs: Late title")}},
		{name: "late body", req: RequestPullRequest{Body: new("Late body")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, intents, _ := automationReviewerFixture(t)
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy})
			require.NoError(t, err, "completion should create the durable publication first")
			req := tt.req
			req.Source = models.SessionPublicationSourceAgentTool
			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, req)
			require.NoError(t, err, "a late metadata call should report the existing publication")
			require.NotNil(t, result.Reason, "late text must be reported as ignored")
			require.Equal(t, "publication metadata is already frozen; the existing publication retains its original team reviewers, title, and body", *result.Reason, "the agent should learn its late text was not applied")
			require.Equal(t, 0, intents.stages, "late text must not be staged after publication exists")
		})
	}
}

func TestPRContentRejectedOutsideAutomationStaging(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  RequestPullRequest
	}{
		{name: "agent title in manual session", req: RequestPullRequest{Source: models.SessionPublicationSourceAgentTool, Title: new("docs: Report")}},
		{name: "agent body in manual session", req: RequestPullRequest{Source: models.SessionPublicationSourceAgentTool, Body: new("Body")}},
		{name: "explicit user request", req: RequestPullRequest{Source: models.SessionPublicationSourceUser, TriggerKind: models.SessionPublicationTriggerExplicitAction, Title: new("docs: Report")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
			req := tt.req
			if req.Source == models.SessionPublicationSourceUser {
				req.RequestedByUserID = &f.userID
				req.RequestedRole = string(models.RoleMember)
			}
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, req)
			var intentErr *Error
			require.ErrorAs(t, err, &intentErr, "text outside automation staging must be rejected rather than silently dropped")
			require.Equal(t, ErrorInvalidPublicationOptions, intentErr.Code, "unsupported text should map to a client error")
			require.Nil(t, f.publications.captured, "rejected text must not create a publication")
			require.Nil(t, f.jobs.payload, "rejected text must not queue publication work")
		})
	}
}

func TestAutomaticPublicationFreezesStagedPRContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		staged   RequestPullRequest
		expected map[string]any
	}{
		{
			name:     "title and body",
			staged:   RequestPullRequest{Title: new("feature-brief: Plan exports"), Body: new("Source plan: docs/plans/exports.md")},
			expected: map[string]any{"title": "feature-brief: Plan exports", "body": "Source plan: docs/plans/exports.md"},
		},
		{
			name:     "title only",
			staged:   RequestPullRequest{Title: new("feature-brief: Plan exports")},
			expected: map[string]any{"title": "feature-brief: Plan exports"},
		},
		{name: "nothing staged", expected: map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, intents, _ := automationReviewerFixture(t)
			if tt.staged.Title != nil || tt.staged.Body != nil {
				staged := tt.staged
				staged.Source = models.SessionPublicationSourceAgentTool
				_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, staged)
				require.NoError(t, err, "agent should stage text before completion")
			}
			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy})
			require.NoError(t, err, "completion should publish with frozen metadata")
			require.Equal(t, ResultPRQueued, result.Status, "completion should queue the configured publication")
			require.Equal(t, 1, intents.freezes, "completion should freeze staged metadata once")
			actual := map[string]any{}
			for _, key := range []string{"title", "body"} {
				if value, ok := f.jobs.payload[key]; ok {
					actual[key] = value
				}
			}
			require.Equal(t, tt.expected, actual, "queued payload should carry exactly the staged text")
			var durable map[string]any
			require.NoError(t, json.Unmarshal(f.publications.captured.RequestPayload, &durable), "durable request payload should decode")
			for _, key := range []string{"title", "body"} {
				require.Equal(t, f.jobs.payload[key], durable[key], "durable publication row must freeze the same text the job carries")
			}
		})
	}
}

func TestAutomaticPublicationReplayRetainsFrozenPRContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state models.SessionPublicationState
	}{
		{name: "completed no-op reopen", state: models.SessionPublicationStateCompletedNoop},
		{name: "terminal failure reopen", state: models.SessionPublicationStateTerminalFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, intents, _ := automationReviewerFixture(t)
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{
				Source: models.SessionPublicationSourceAgentTool, Title: new("feature-brief: Plan exports"), Body: new("Source plan: docs/plans/exports.md"),
			})
			require.NoError(t, err, "agent should stage text before completion")
			request := RequestPullRequest{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy}
			_, err = f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, request)
			require.NoError(t, err, "first completion should freeze staged text")
			f.publications.captured.State = tt.state
			f.jobs.payload = nil
			// A replay must read the durable request, not a mutable staging row.
			intents.intent.Title = new("changed after freeze")
			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, request)
			require.NoError(t, err, "retryable terminal outcome should reopen")
			require.Equal(t, ResultPRQueued, result.Status, "reopened publication should queue again")
			require.NotNil(t, f.jobs.payload, "test must exercise the rebuilt payload")
			require.Equal(t, "feature-brief: Plan exports", f.jobs.payload["title"], "rebuilt payload must keep the frozen title")
			require.Equal(t, "Source plan: docs/plans/exports.md", f.jobs.payload["body"], "rebuilt payload must keep the frozen body")
			require.Equal(t, 1, intents.freezes, "replay must not refreeze staging metadata")
		})
	}
}
