package publicationintent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type reviewIntentRunStore struct{ run models.AutomationRun }

func (s reviewIntentRunStore) GetByRunID(context.Context, uuid.UUID, uuid.UUID) (models.AutomationRun, error) {
	return s.run, nil
}

type reviewIntentStore struct {
	intent          models.SessionPRIntent
	stages, freezes int
}

func (s *reviewIntentStore) Stage(_ context.Context, _ uuid.UUID, intent models.SessionPRIntent) error {
	s.stages++
	s.intent = intent
	return nil
}

func (s *reviewIntentStore) Freeze(_ context.Context, _, _, _, _ uuid.UUID) (models.SessionPRIntent, error) {
	s.freezes++
	return s.intent, nil
}

func automationReviewerFixture(t *testing.T) (*coordinatorFixture, *reviewIntentStore, models.AutomationRun) {
	t.Helper()
	runID := uuid.New()
	f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, func(session *models.Session, _ *models.SessionChangeset) {
		session.Origin = models.SessionOriginAutomation
		session.AutomationRunID = &runID
		session.CapabilitySnapshot = []models.AgentCapabilitySnapshotItem{{ID: models.AgentCapabilityPublishing, AccessLevel: models.AgentCapabilityAccessWrite}}
	}, true, nil)
	run := models.AutomationRun{ID: runID, OrgID: f.orgID, ConfigSnapshot: json.RawMessage(`{"publish_policy":"pull_request","session_continuity":"per_run"}`)}
	intents := &reviewIntentStore{}
	f.coordinator.SetAutomationPRIntentStores(reviewIntentRunStore{run: run}, intents)
	f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{ID: f.repositoryID, FullName: "acme/repo"}})
	return f, intents, run
}

func TestAutomationReviewerMetadataStagesWithoutEarlyPublication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		edit    func(*models.Session, *models.AutomationRun, *RequestPullRequest)
		wantErr bool
	}{
		{name: "stage configured teams"},
		{name: "missing publishing grant", wantErr: true, edit: func(s *models.Session, _ *models.AutomationRun, _ *RequestPullRequest) { s.CapabilitySnapshot = nil }},
		{name: "read capability cannot publish", wantErr: true, edit: func(s *models.Session, _ *models.AutomationRun, _ *RequestPullRequest) {
			s.CapabilitySnapshot[0].AccessLevel = models.AgentCapabilityAccessRead
		}},
		{name: "frozen none policy", wantErr: true, edit: func(_ *models.Session, run *models.AutomationRun, _ *RequestPullRequest) {
			run.ConfigSnapshot = json.RawMessage(`{"publish_policy":"none"}`)
		}},
		{name: "per-target run", wantErr: true, edit: func(_ *models.Session, run *models.AutomationRun, _ *RequestPullRequest) {
			id := uuid.New()
			run.TargetID = &id
		}},
		{name: "per-target frozen policy", wantErr: true, edit: func(_ *models.Session, run *models.AutomationRun, _ *RequestPullRequest) {
			run.ConfigSnapshot = json.RawMessage(`{"publish_policy":"pull_request","session_continuity":"per_target"}`)
		}},
		{name: "draft override rejected", wantErr: true, edit: func(_ *models.Session, _ *models.AutomationRun, req *RequestPullRequest) {
			draft := true
			req.Draft = &draft
		}},
		{name: "publisher override rejected", wantErr: true, edit: func(_ *models.Session, _ *models.AutomationRun, req *RequestPullRequest) { req.AuthorMode = "app" }},
		{name: "cross-organization team", wantErr: true, edit: func(_ *models.Session, _ *models.AutomationRun, req *RequestPullRequest) {
			req.TeamReviewers = []string{"other/team"}
		}},
		{name: "mismatched run tenant", wantErr: true, edit: func(_ *models.Session, run *models.AutomationRun, _ *RequestPullRequest) { run.OrgID = uuid.New() }},
		{name: "mismatched linked run", wantErr: true, edit: func(_ *models.Session, run *models.AutomationRun, _ *RequestPullRequest) { run.ID = uuid.New() }},
		{name: "missing repository", wantErr: true, edit: func(s *models.Session, _ *models.AutomationRun, _ *RequestPullRequest) { s.RepositoryID = nil }},
		{name: "non-primary selector", wantErr: true, edit: func(_ *models.Session, _ *models.AutomationRun, req *RequestPullRequest) {
			id := uuid.New()
			req.ChangesetID = &id
		}},
	}
	origins := []struct {
		name   string
		origin models.SessionOrigin
	}{
		{name: "automation", origin: models.SessionOriginAutomation},
		{name: "linked legacy issue trigger", origin: models.SessionOriginIssueTrigger},
	}
	for _, origin := range origins {
		t.Run(origin.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					f, intents, run := automationReviewerFixture(t)
					f.session.Origin = origin.origin
					req := RequestPullRequest{Source: models.SessionPublicationSourceAgentTool, TeamReviewers: []string{"Acme/TEAM", "team"}}
					if tt.edit != nil {
						tt.edit(&f.session, &run, &req)
					}
					f.coordinator.sessions = coordinatorSessionStore{session: f.session}
					f.coordinator.SetAutomationPRIntentStores(reviewIntentRunStore{run: run}, intents)
					result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, req)
					if tt.wantErr {
						require.Error(t, err, "unavailable or unsupported metadata request must fail closed")
						require.Equal(t, 0, intents.stages, "rejected request must not stage metadata")
					} else {
						require.NoError(t, err, "authorized automation may stage reviewer metadata")
						require.Equal(t, &PublicationIntentResult{Status: ResultAwaitingAutomaticPublication, SessionID: f.sessionID}, result, "response must distinguish staging from queued publication")
						require.Equal(t, models.SessionPRIntent{OrgID: f.orgID, SessionID: f.sessionID, AutomationRunID: run.ID, ChangesetID: f.changesetID, TeamReviewers: []string{"acme/team"}}, intents.intent, "staged metadata must normalize teams and bind the exact run and primary changeset")
					}
					require.Nil(t, f.publications.captured, "staging must never create a requested publication row")
					require.Nil(t, f.jobs.payload, "staging must never enqueue early publication work")
				})
			}
		})
	}
}

func TestAutomationReviewerMetadataRejectsUnlinkedOrUnrelatedSessions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		origin models.SessionOrigin
		linked bool
	}{
		{name: "unlinked legacy issue trigger", origin: models.SessionOriginIssueTrigger},
		{name: "unlinked automation provenance", origin: models.SessionOriginAutomation},
		{name: "linked manual", origin: models.SessionOriginManual, linked: true},
		{name: "linked project", origin: models.SessionOriginProject, linked: true},
		{name: "linked revision", origin: models.SessionOriginRevision, linked: true},
		{name: "linked code review", origin: models.SessionOriginCodeReview, linked: true},
		{name: "linked goal improvement", origin: models.SessionOriginAutomationGoalImprovement, linked: true},
		{name: "missing provenance", linked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, intents, _ := automationReviewerFixture(t)
			f.session.Origin = tt.origin
			if !tt.linked {
				f.session.AutomationRunID = nil
			}

			result, err := f.coordinator.stageAutomationPRIntent(context.Background(), f.orgID, f.session, RequestPullRequest{Source: models.SessionPublicationSourceAgentTool, TeamReviewers: []string{"team"}})

			var intentErr *Error
			require.ErrorAs(t, err, &intentErr, "automation metadata authority must require an eligible origin and durable run link")
			require.Equal(t, ErrorSessionNotEligible, intentErr.Code, "unlinked or unrelated sessions must retain the eligibility fence")
			require.Nil(t, result, "an ineligible session must not claim metadata admission")
			require.Equal(t, 0, intents.stages, "an ineligible session must not stage automatic publication preferences")
			require.Nil(t, f.publications.captured, "an ineligible session must not create a publication")
			require.Nil(t, f.jobs.payload, "an ineligible session must not queue publication work")
		})
	}
}

func TestAutomationReviewerMetadataSelectsOnlyPrimaryBeforeCompletion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		selector   string
		trigger    models.SessionPublicationTriggerKind
		wantErr    bool
		wantStages int
	}{
		{name: "implicit primary", wantStages: 1},
		{name: "explicit primary", selector: "primary", wantStages: 1},
		{name: "explicit action remains metadata only", selector: "primary", trigger: models.SessionPublicationTriggerExplicitAction, wantStages: 1},
		{name: "child changeset rejected", selector: "child", wantErr: true},
		{name: "foreign changeset rejected", selector: "foreign", wantErr: true},
		{name: "zero changeset rejected", selector: "zero", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, intents, run := automationReviewerFixture(t)
			f.session.Status = models.SessionStatusRunning
			f.coordinator.sessions = coordinatorSessionStore{session: f.session}
			req := RequestPullRequest{Source: models.SessionPublicationSourceAgentTool, TriggerKind: tt.trigger, TeamReviewers: []string{"Acme/TEAM", "team"}}
			if tt.selector != "" {
				selectedID := uuid.New()
				if tt.selector == "primary" {
					selectedID = f.changesetID
				} else if tt.selector == "zero" {
					selectedID = uuid.Nil
				}
				req.ChangesetID = &selectedID
			}

			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, req)

			if tt.wantErr {
				var intentErr *Error
				require.ErrorAs(t, err, &intentErr, "non-primary automation selectors must return a typed rejection")
				require.Equal(t, ErrorInvalidPublicationOptions, intentErr.Code, "automation metadata cannot select a child or foreign changeset")
				require.Nil(t, result, "a rejected selector must not claim metadata was staged")
			} else {
				require.NoError(t, err, "a running automation may register metadata for its primary changeset")
				require.Equal(t, &PublicationIntentResult{Status: ResultAwaitingAutomaticPublication, SessionID: f.sessionID}, result, "running-session metadata registration must await the configured completion handoff")
				require.Equal(t, models.SessionPRIntent{OrgID: f.orgID, SessionID: f.sessionID, AutomationRunID: run.ID, ChangesetID: f.changesetID, TeamReviewers: []string{"acme/team"}}, intents.intent, "explicit selection must preserve scoped primary intent and normalized teams")
			}
			require.Equal(t, tt.wantStages, intents.stages, "only primary metadata may reach the durable staging store")
			require.Nil(t, f.changesets.requestedID, "automatic staging must resolve the authoritative primary rather than trust an arbitrary selector")
			require.Nil(t, f.publications.captured, "running-session metadata must not create a publication before quiescence")
			require.Nil(t, f.jobs.payload, "running-session metadata must not enqueue early publication work")
		})
	}
}

func TestInteractiveAgentPublicationRetainsChildChangesetGuards(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		materialized bool
		disabled     bool
		automaticOff bool
		trigger      models.SessionPublicationTriggerKind
		wantErr      bool
		wantStatus   ResultStatus
	}{
		{name: "materialized child can publish", materialized: true, wantStatus: ResultReviewInProgress},
		{name: "unmaterialized child remains blocked", wantErr: true},
		{name: "disabled publication cannot fall back to primary", materialized: true, disabled: true, wantStatus: ResultManualPublicationRequired},
		{name: "explicit child publication survives automatic preference off", materialized: true, automaticOff: true, trigger: models.SessionPublicationTriggerExplicitAction, wantStatus: ResultReviewInProgress},
		{name: "agent-ready child obeys automatic preference off", materialized: true, automaticOff: true, trigger: models.SessionPublicationTriggerAgentReady, wantStatus: ResultManualPublicationRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := models.AutomaticFollowThroughOrgSettings{}
			if tt.automaticOff {
				automatic := false
				policy.CreatePRWhenAgentReady = &automatic
			}
			f := newCoordinatorFixture(t, policy, nil, func(_ *models.Session, changeset *models.SessionChangeset) {
				changeset.IsPrimary = false
				if tt.materialized {
					worktree := "/workspace/child"
					changeset.WorktreePath = &worktree
				}
			}, true, nil)
			f.coordinator.SetPublicationEnabled(!tt.disabled)

			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{Source: models.SessionPublicationSourceAgentTool, TriggerKind: tt.trigger, ChangesetID: &f.changesetID})

			if tt.wantErr {
				var intentErr *Error
				require.ErrorAs(t, err, &intentErr, "interactive child publication must preserve its materialization guard")
				require.Equal(t, ErrorWorkspaceNotReady, intentErr.Code, "unmaterialized children must remain ineligible for publication")
				require.Nil(t, f.publications.captured, "an unmaterialized child must not create durable publication work")
				require.Nil(t, f.jobs.payload, "an unmaterialized child must not enqueue a pull request")
			} else {
				require.NoError(t, err, "ordinary sandbox publication must still support a materialized child")
				require.Equal(t, tt.wantStatus, result.Status, "interactive child publication must retain the configured review and execution gates")
				if tt.wantStatus == ResultManualPublicationRequired {
					require.Nil(t, f.publications.captured, "a blocked agent handoff must not create a fallback primary publication")
					require.Nil(t, f.jobs.payload, "a blocked agent handoff must not queue any changeset")
				} else {
					require.Equal(t, f.changesetID.String(), f.jobs.payload["changeset_id"], "ordinary publication must preserve the selected child rather than switch to primary")
				}
			}
			require.Equal(t, &f.changesetID, f.changesets.requestedID, "interactive selectors must use the tenant- and session-scoped changeset lookup")
		})
	}
}

func TestAutomaticPublicationFreezesReviewerMetadataAcrossRetries(t *testing.T) {
	t.Parallel()
	f, intents, _ := automationReviewerFixture(t)
	_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{TeamReviewers: []string{"team"}})
	require.NoError(t, err, "agent should stage requested team")
	_, err = f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{})
	require.NoError(t, err, "option-less staging retry should retain previously requested metadata")
	require.Equal(t, []string{"acme/team"}, intents.intent.TeamReviewers, "omitting team_reviewers must not clear an explicit staged list")
	require.Equal(t, 1, intents.stages, "option-less retries must not overwrite operational intent")
	_, err = f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy})
	require.NoError(t, err, "completion policy should create the first durable publication")
	require.Equal(t, []any{"acme/team"}, f.jobs.payload["team_reviewers"], "automatic request must include staged normalized team")
	intents.intent.TeamReviewers = []string{"acme/changed"}
	f.publications.captured.State = models.SessionPublicationStateCompletedNoop
	f.jobs.payload = nil
	_, err = f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy})
	require.NoError(t, err, "retryable publication should rejoin its original intent")
	require.NotNil(t, f.jobs.payload, "terminal reopen should rebuild and queue a payload rather than rejoin early")
	require.Equal(t, []any{"acme/team"}, f.jobs.payload["team_reviewers"], "retry must retain first durable reviewer choices even if mutable staging changed")
	require.Equal(t, false, f.jobs.payload["draft"], "terminal reopen must retain the original ready choice with the teams")
	require.Equal(t, 1, intents.freezes, "only first publication may freeze staged metadata")
}

func TestManualPublicationForwardsOnlyExplicitTeamReviewers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		teams    []string
		expected any
	}{
		{name: "explicit requested teams", teams: []string{"Acme/TEAM", "team"}, expected: []any{"acme/team"}},
		{name: "no implicit team reviewers"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
			f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{FullName: "acme/repo"}})
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{TeamReviewers: tt.teams})
			require.NoError(t, err, "ordinary session publication should retain its existing handoff")
			require.Equal(t, tt.expected, f.jobs.payload["team_reviewers"], "payload should request teams only when caller explicitly supplied them")
		})
	}
}

func TestTeamReviewerIntentRequiresReadyPublication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                       string
		orgDraft, explicitDraft, draft, draftFirst bool
		wantErr                                    bool
	}{
		{name: "explicit draft rejected", explicitDraft: true, draft: true, wantErr: true},
		{name: "implicit draft default rejected", orgDraft: true, wantErr: true},
		{name: "explicit ready overrides draft default", orgDraft: true, explicitDraft: true},
		{name: "ready default is frozen"},
		{name: "draft-first gate owns readiness", orgDraft: true, draftFirst: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
			settings, err := json.Marshal(models.OrgSettings{PRDraftDefault: tt.orgDraft})
			require.NoError(t, err, "draft policy fixture should encode")
			f.coordinator.organizations = coordinatorOrganizationStore{organization: models.Organization{ID: f.orgID, Settings: settings}}
			repository := models.Repository{FullName: "acme/repo"}
			if tt.draftFirst {
				repository.Settings = json.RawMessage(`{"pr_handoff_mode":"draft_first"}`)
			}
			f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: repository})
			req := RequestPullRequest{TeamReviewers: []string{"team"}}
			if tt.explicitDraft {
				draft := tt.draft
				req.Draft = &draft
			}
			_, err = f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, req)
			if tt.wantErr {
				var intentErr *Error
				require.ErrorAs(t, err, &intentErr, "draft reviewer request should return an actionable options error")
				require.Equal(t, ErrorInvalidPublicationOptions, intentErr.Code, "unsupported draft request must be rejected before publication")
				require.Nil(t, f.publications.captured, "unsupported draft request must not create a publication")
				require.Nil(t, f.jobs.payload, "unsupported draft request must not queue side effects")
			} else {
				require.NoError(t, err, "ready or gated publication should support team reviewers")
				require.Equal(t, tt.draftFirst, f.jobs.payload["draft"], "first durable payload must freeze ready publication or retain temporary draft-first gating")
			}
		})
	}
}

func TestFrozenTeamReviewersRejectDraftReviewBypass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		explicitDraft bool
	}{
		{name: "explicit draft bypass"},
		{name: "explicit ready action cannot leave existing draft unfinalized", explicitDraft: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
			f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{FullName: "acme/repo", Settings: json.RawMessage(`{"pr_handoff_mode":"draft_first"}`)}})
			publication := models.SessionPublication{ID: uuid.New(), OrgID: f.orgID, SessionID: f.sessionID, ChangesetID: f.changesetID, State: models.SessionPublicationStateReviewPending, ReviewGateState: models.SessionPublicationReviewGateNeedsHuman, HandoffMode: models.PRHandoffModeDraftFirst, RequestPayload: json.RawMessage(`{"team_reviewers":["acme/team"],"draft":true}`)}
			f.publications.captured = &publication
			req := RequestPullRequest{Source: models.SessionPublicationSourceUser, TriggerKind: models.SessionPublicationTriggerExplicitAction, RequestedByUserID: &f.userID, RequestedRole: string(models.RoleMember)}
			if !tt.explicitDraft {
				draft := true
				req.Draft = &draft
			}
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, req)
			var intentErr *Error
			require.ErrorAs(t, err, &intentErr, "draft bypass must not silently strand frozen reviewer intent")
			require.Equal(t, ErrorInvalidPublicationOptions, intentErr.Code, "ready requirement should remain actionable on an option-less retry")
			require.Equal(t, publication, *f.publications.captured, "rejected bypass must preserve the original review gate and frozen teams")
			require.Nil(t, f.jobs.payload, "unsupported bypass must not queue early or incomplete publication")
		})
	}
}
