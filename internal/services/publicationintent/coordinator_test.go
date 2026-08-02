package publicationintent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type coordinatorSessionStore struct {
	session models.Session
}

func (s coordinatorSessionStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	return s.session, nil
}

type coordinatorChangesetStore struct{ changeset models.SessionChangeset }

func (s coordinatorChangesetStore) GetPrimary(context.Context, uuid.UUID, uuid.UUID) (models.SessionChangeset, error) {
	return s.changeset, nil
}

type coordinatorPullRequestStore struct{ pr *models.PullRequest }

func (s coordinatorPullRequestStore) GetPrimaryBySessionID(context.Context, uuid.UUID, uuid.UUID) (models.PullRequest, error) {
	if s.pr == nil {
		return models.PullRequest{}, pgx.ErrNoRows
	}
	return *s.pr, nil
}

type coordinatorOrganizationStore struct{ organization models.Organization }

func (s coordinatorOrganizationStore) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	return s.organization, nil
}

type coordinatorUserStore struct{ user models.UserWithSettings }

func (s coordinatorUserStore) GetByIDGlobalWithSettings(context.Context, uuid.UUID) (models.UserWithSettings, error) {
	return s.user, nil
}

type coordinatorRepositoryStore struct{ repository models.Repository }

func (s coordinatorRepositoryStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	return s.repository, nil
}

type coordinatorPublicationStore struct {
	captured *models.SessionPublication
	// ensureReturnsState models the row EnsureRequested actually wrote back.
	// Setting it to a terminal state reproduces a generation-guarded reopen
	// that did not fire.
	ensureReturnsState models.SessionPublicationState
}

func (s *coordinatorPublicationStore) EnsureRequested(_ context.Context, _ uuid.UUID, publication *models.SessionPublication) error {
	publication.ID = uuid.New()
	if s.ensureReturnsState != "" {
		publication.State = s.ensureReturnsState
	}
	copy := *publication
	s.captured = &copy
	return nil
}

func (s *coordinatorPublicationStore) ApplyReviewBypass(_ context.Context, _ uuid.UUID, publication *models.SessionPublication) error {
	publication.ReviewGateState = models.SessionPublicationReviewGateNotRequired
	publication.ReviewMaxPasses = nil
	publication.ReviewLoopID = nil
	publication.ReviewWorkspaceRevision = nil
	publication.ReviewDesiredHeadSHA = nil
	copy := *publication
	s.captured = &copy
	return nil
}

func (s *coordinatorPublicationStore) GetByChangeset(_ context.Context, _, _, _ uuid.UUID) (models.SessionPublication, error) {
	if s.captured == nil {
		return models.SessionPublication{}, pgx.ErrNoRows
	}
	return *s.captured, nil
}

type coordinatorJobStore struct {
	queued  bool
	payload map[string]any
	err     error
}

func (s *coordinatorJobStore) QueueChangesetPRCreation(_ context.Context, _, _, _ uuid.UUID, _ string, payload any, _ int) (uuid.UUID, bool, error) {
	encoded, _ := json.Marshal(payload)
	_ = json.Unmarshal(encoded, &s.payload)
	return uuid.New(), s.queued, s.err
}

// coordinatorFixture builds the stores for a session that is mid-turn: the
// working branch is durable (the orchestrator persists it before the agent
// starts) but no diff snapshot has been written yet, because UpdateResult only
// runs after the turn ends.
type coordinatorFixture struct {
	orgID, sessionID, repositoryID, userID, changesetID uuid.UUID
	session                                             models.Session
	changeset                                           models.SessionChangeset
	publications                                        *coordinatorPublicationStore
	jobs                                                *coordinatorJobStore
	coordinator                                         *Coordinator
}

func newCoordinatorFixture(
	t *testing.T,
	orgPolicy models.AutomaticFollowThroughOrgSettings,
	personal *models.AutomaticPRFollowThroughSettings,
	edit func(*models.Session, *models.SessionChangeset),
	queued bool,
	queueErr error,
) *coordinatorFixture {
	t.Helper()

	f := &coordinatorFixture{
		orgID: uuid.New(), sessionID: uuid.New(), repositoryID: uuid.New(),
		userID: uuid.New(), changesetID: uuid.New(),
	}
	branch, head := "143/change", "0123456789abcdef"
	f.session = models.Session{
		ID: f.sessionID, OrgID: f.orgID, RepositoryID: &f.repositoryID,
		Origin: models.SessionOriginManual, TriggeredByUserID: &f.userID,
		WorkingBranch: &branch,
	}
	f.changeset = models.SessionChangeset{
		ID: f.changesetID, OrgID: f.orgID, SessionID: f.sessionID,
		Status: models.ChangesetStatusReady, BaseBranch: "main",
		WorkingBranch: &branch, HeadSHA: &head,
	}
	if edit != nil {
		edit(&f.session, &f.changeset)
	}
	orgSettings, err := json.Marshal(models.OrgSettings{
		SessionAutomation: models.SessionAutomationSettings{AutomaticFollowThrough: orgPolicy},
	})
	require.NoError(t, err, "test organization settings should encode")

	f.publications = &coordinatorPublicationStore{}
	f.jobs = &coordinatorJobStore{queued: queued, err: queueErr}
	f.coordinator = NewCoordinator(
		coordinatorSessionStore{session: f.session},
		coordinatorChangesetStore{changeset: f.changeset},
		coordinatorPullRequestStore{},
		coordinatorOrganizationStore{organization: models.Organization{ID: f.orgID, Settings: orgSettings}},
		coordinatorUserStore{user: models.UserWithSettings{
			ID: f.userID, OrgID: f.orgID,
			Settings: models.UserSettings{AutomaticPRFollowThrough: personal},
		}},
		f.publications, f.jobs, zerolog.Nop(),
	)
	return f
}

func TestCoordinatorRequestPullRequest(t *testing.T) {
	t.Parallel()

	enabled, disabled := true, false
	tests := []struct {
		name        string
		edit        func(*models.Session, *models.SessionChangeset)
		orgPolicy   models.AutomaticFollowThroughOrgSettings
		personal    *models.AutomaticPRFollowThroughSettings
		wantStatus  ResultStatus
		wantErrCode ErrorCode
		wantQueued  bool
		queueErr    error
	}{
		{name: "eligible agent intent starts durable review", wantStatus: ResultReviewInProgress, wantQueued: true},
		{name: "queue failure preserves durable blocked intent", wantStatus: ResultBlocked, queueErr: assertiveError("queue unavailable")},
		{
			name: "code review session is ineligible",
			edit: func(session *models.Session, _ *models.SessionChangeset) {
				session.Origin = models.SessionOriginCodeReview
			},
			wantErrCode: ErrorSessionNotEligible,
		},
		{
			name: "unmaterialized workspace is rejected",
			edit: func(session *models.Session, changeset *models.SessionChangeset) {
				session.WorkingBranch = nil
				changeset.WorkingBranch = nil
			},
			wantErrCode: ErrorWorkspaceNotReady,
		},
		{
			name: "changeset with an open pull request is rejected",
			edit: func(_ *models.Session, changeset *models.SessionChangeset) {
				changeset.Status = models.ChangesetStatusPROpen
			},
			wantErrCode: ErrorWorkspaceNotReady,
		},
		{
			name: "changeset awaiting restack is rejected",
			edit: func(_ *models.Session, changeset *models.SessionChangeset) {
				changeset.Status = models.ChangesetStatusNeedsRestack
			},
			wantErrCode: ErrorWorkspaceNotReady,
		},
		{
			name:       "personal opt-out requires manual publication",
			orgPolicy:  models.AutomaticFollowThroughOrgSettings{CreatePRWhenAgentReady: &enabled},
			personal:   &models.AutomaticPRFollowThroughSettings{CreatePRWhenAgentReady: models.AutomaticFollowThroughPreferenceOff},
			wantStatus: ResultManualPublicationRequired,
		},
		{
			name:       "organization false remains false when inherited",
			orgPolicy:  models.AutomaticFollowThroughOrgSettings{CreatePRWhenAgentReady: &disabled},
			personal:   &models.AutomaticPRFollowThroughSettings{CreatePRWhenAgentReady: models.AutomaticFollowThroughPreferenceInherit},
			wantStatus: ResultManualPublicationRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newCoordinatorFixture(t, tt.orgPolicy, tt.personal, tt.edit, tt.wantQueued, tt.queueErr)
			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{})
			if tt.wantErrCode != "" {
				var intentErr *Error
				require.Error(t, err, "ineligible request should return a typed error")
				require.ErrorAs(t, err, &intentErr, "ineligible request should preserve the coordinator error")
				require.Equal(t, tt.wantErrCode, intentErr.Code, "coordinator should return the expected error code")
				require.Nil(t, f.publications.captured, "rejected request should not persist publication state")
				return
			}
			require.NoError(t, err, "eligible or policy-disabled request should return a typed result")
			require.Equal(t, tt.wantStatus, result.Status, "coordinator should return the expected publication state")
			if tt.wantQueued {
				require.NotNil(t, f.publications.captured, "queued request should persist durable publication intent")
				require.Equal(t, models.SessionPublicationTriggerAgentReady, f.publications.captured.TriggerKind, "agent tool readiness should be persisted")
				require.Equal(t, &f.userID, f.publications.captured.InitiatedByUserID, "stable session initiator should be persisted")
				require.Equal(t, string(models.SessionPublicationTriggerAgentReady), f.jobs.payload["publication_trigger_kind"], "queued job should carry durable trigger metadata")
			}
			if tt.queueErr != nil {
				require.NotNil(t, f.publications.captured, "queue failure should retain the durable publication intent for reconciliation")
				require.NotNil(t, result.Reason, "queue failure should explain that recovery is pending")
			}
		})
	}
}

// The agent calls create_pr from inside the sandbox while its turn is still
// running, so sessions.latest_diff_snapshot_id is nil on the first turn and
// session_changesets.materialized_diff is never refreshed for the primary
// changeset. Publishing must not depend on either: the open_pr worker captures
// a fresh diff and terminates an empty changeset as completed_noop.
func TestCoordinatorRequestPullRequestDoesNotRequireCapturedDiffEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*models.Session, *models.SessionChangeset)
	}{
		{
			name: "first turn has no diff snapshot yet",
			edit: func(session *models.Session, changeset *models.SessionChangeset) {
				session.SnapshotKey = nil
				session.LatestDiffSnapshotID = nil
				changeset.MaterializedDiff = nil
			},
		},
		{
			name: "worktree primary carries a stale empty materialized diff",
			edit: func(_ *models.Session, changeset *models.SessionChangeset) {
				worktree, empty := "/workspace/change", ""
				changeset.WorktreePath = &worktree
				changeset.MaterializedDiff = &empty
			},
		},
		{
			name: "planned worktree changeset is still publishable",
			edit: func(_ *models.Session, changeset *models.SessionChangeset) {
				worktree := "/workspace/change"
				changeset.WorktreePath = &worktree
				changeset.Status = models.ChangesetStatusPlanned
				changeset.MaterializedDiff = nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, tt.edit, true, nil)
			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{})

			require.NoError(t, err, "a mid-turn request must not be refused for lacking end-of-turn diff evidence")
			require.Equal(t, ResultReviewInProgress, result.Status, "the worker should capture review evidence after the turn becomes quiescent")
			require.NotNil(t, f.publications.captured, "the intent should be durable before the job is queued")
		})
	}
}

// source records the channel the request arrived through and trigger kind
// records why it was made. They vary independently: an agent relaying an
// explicit user instruction is still an agent_tool publication.
func TestCoordinatorRequestPullRequestKeepsSourceIndependentOfTrigger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     models.SessionPublicationSource
		trigger    models.SessionPublicationTriggerKind
		wantPolicy models.PublicationPolicySource
	}{
		{
			name:       "agent readiness through the agent tool",
			source:     models.SessionPublicationSourceAgentTool,
			trigger:    models.SessionPublicationTriggerAgentReady,
			wantPolicy: models.PublicationPolicySourceProductDefault,
		},
		{
			name:       "agent relaying an explicit user instruction stays an agent tool publication",
			source:     models.SessionPublicationSourceAgentTool,
			trigger:    models.SessionPublicationTriggerExplicitAction,
			wantPolicy: models.PublicationPolicySourceExplicitAction,
		},
		{
			name:       "operator clicking create PR in the UI",
			source:     models.SessionPublicationSourceUser,
			trigger:    models.SessionPublicationTriggerExplicitAction,
			wantPolicy: models.PublicationPolicySourceExplicitAction,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
			_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{
				Source: tt.source, TriggerKind: tt.trigger,
			})

			require.NoError(t, err, "the request should be accepted")
			require.NotNil(t, f.publications.captured, "the request should persist durable intent")
			require.Equal(t, tt.source, f.publications.captured.Source, "publication source should record the channel the caller used")
			require.Equal(t, tt.trigger, f.publications.captured.TriggerKind, "trigger kind should record why the request was made")
			require.Equal(t, string(tt.source), f.jobs.payload["publication_source"], "queued job should carry the same attribution as the durable row")
			require.Equal(t, tt.wantPolicy, f.publications.captured.AutomaticPolicySource, "policy source should record why the publication was allowed")
		})
	}
}

// The DB enforces this pairing too, but rejecting it here keeps a caller from
// laundering an agent decision through a user-attributed row.
func TestCoordinatorRequestPullRequestRejectsAgentReadyFromNonAgentSource(t *testing.T) {
	t.Parallel()

	f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
	_, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{
		Source: models.SessionPublicationSourceUser, TriggerKind: models.SessionPublicationTriggerAgentReady,
	})

	var intentErr *Error
	require.Error(t, err, "agent readiness from a non-agent channel should be refused")
	require.ErrorAs(t, err, &intentErr, "the rejection should be a typed coordinator error")
	require.Equal(t, ErrorPublicationFailed, intentErr.Code, "the rejection should surface as a publication failure")
	require.Nil(t, f.publications.captured, "a refused pairing should not persist durable state")
}

// An explicit user request must still publish when automatic handoff is off —
// that is the escape hatch the handoff prompt points the agent at.
func TestCoordinatorRequestPullRequestExplicitActionBypassesDisabledPolicy(t *testing.T) {
	t.Parallel()

	disabled := false
	orgPolicy := models.AutomaticFollowThroughOrgSettings{CreatePRWhenAgentReady: &disabled}

	f := newCoordinatorFixture(t, orgPolicy, nil, nil, true, nil)
	agentResult, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{
		TriggerKind: models.SessionPublicationTriggerAgentReady,
	})
	require.NoError(t, err, "a policy rejection is a typed result, not an error")
	require.Equal(t, ResultManualPublicationRequired, agentResult.Status, "automatic handoff should respect the opt-out")

	explicit := newCoordinatorFixture(t, orgPolicy, nil, nil, true, nil)
	explicitResult, err := explicit.coordinator.RequestPullRequest(context.Background(), explicit.orgID, explicit.sessionID, RequestPullRequest{
		Source: models.SessionPublicationSourceUser, TriggerKind: models.SessionPublicationTriggerExplicitAction,
	})
	require.NoError(t, err, "an explicit user request should be accepted")
	require.Equal(t, ResultReviewInProgress, explicitResult.Status, "explicit user action should override handoff policy while retaining review")
}

func TestCoordinatorRequestPullRequestRepositoryHandoffAndDraftBypass(t *testing.T) {
	t.Parallel()

	draft := true
	explicitDraftRequest := RequestPullRequest{
		Source: models.SessionPublicationSourceUser, TriggerKind: models.SessionPublicationTriggerExplicitAction,
		Draft: &draft, RequestedByUserID: coordinatorUUIDPtr(uuid.New()), RequestedRole: string(models.RoleMember),
	}
	tests := []struct {
		name                 string
		req                  RequestPullRequest
		existing             *models.SessionPublication
		expectedStatus       ResultStatus
		expectedGate         models.SessionPublicationReviewGateState
		expectedReviewSource models.PublicationPolicySource
		expectedBypass       bool
	}{
		{
			name:           "repository draft-first policy forces a draft while retaining review",
			req:            RequestPullRequest{Source: models.SessionPublicationSourceAgentTool, TriggerKind: models.SessionPublicationTriggerAgentReady},
			expectedStatus: ResultReviewInProgress, expectedGate: models.SessionPublicationReviewGatePending,
			expectedReviewSource: models.PublicationPolicySourceProductDefault,
		},
		{
			name:           "a first draft request is not a review bypass",
			req:            explicitDraftRequest,
			expectedStatus: ResultReviewInProgress, expectedGate: models.SessionPublicationReviewGatePending,
			expectedReviewSource: models.PublicationPolicySourceProductDefault,
		},
		{
			name: "authorized explicit draft action bypasses a blocked review",
			req:  explicitDraftRequest,
			existing: &models.SessionPublication{
				State:           models.SessionPublicationStateReviewPending,
				ReviewGateState: models.SessionPublicationReviewGateNeedsHuman,
			},
			expectedStatus: ResultPRQueued, expectedGate: models.SessionPublicationReviewGateNotRequired,
			expectedReviewSource: models.PublicationPolicySourceExplicitBypass, expectedBypass: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
			f.coordinator.SetRepositoryStore(coordinatorRepositoryStore{repository: models.Repository{
				ID: f.repositoryID, OrgID: f.orgID, Settings: json.RawMessage(`{"pr_handoff_mode":"draft_first"}`),
			}})
			f.publications.captured = tt.existing

			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, tt.req)
			require.NoError(t, err, "repository handoff request should be accepted")
			require.Equal(t, tt.expectedStatus, result.Status, "repository handoff should return the expected durable state")
			require.Equal(t, tt.expectedBypass, result.ReviewBypassed, "result should expose whether review was explicitly bypassed for audit")
			require.Equal(t, models.PRHandoffModeDraftFirst, f.publications.captured.HandoffMode, "repository policy should be authoritative for handoff mode")
			require.Equal(t, tt.expectedGate, f.publications.captured.ReviewGateState, "draft handoff should persist the expected review gate")
			require.Equal(t, tt.expectedReviewSource, f.publications.captured.ReviewPolicySource, "draft handoff should persist the review decision source")
			require.Equal(t, true, f.jobs.payload["draft"], "draft-first should force the durable open_pr request to create a draft")
		})
	}
}

func coordinatorUUIDPtr(value uuid.UUID) *uuid.UUID { return &value }

func TestCoordinatorRequestPullRequestRejoinsOrReopensExistingIntent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		existing         models.SessionPublication
		ensureKeepsState models.SessionPublicationState
		wantStatus       ResultStatus
		wantReopened     bool
		wantNotQueued    bool
	}{
		{
			name: "a live intent is rejoined rather than duplicated",
			existing: models.SessionPublication{
				State: models.SessionPublicationStateReviewPending, ReviewGateState: models.SessionPublicationReviewGatePending,
			},
			wantStatus: ResultReviewInProgress,
		},
		{
			name:       "a completed publication stays published",
			existing:   models.SessionPublication{State: models.SessionPublicationStateCompleted},
			wantStatus: ResultAlreadyPublished,
		},
		{
			name:         "a no-op publication can be retried once there is something to publish",
			existing:     models.SessionPublication{State: models.SessionPublicationStateCompletedNoop},
			wantStatus:   ResultReviewInProgress,
			wantReopened: true,
		},
		{
			name:         "a terminally failed publication can be retried",
			existing:     models.SessionPublication{State: models.SessionPublicationStateTerminalFailed},
			wantStatus:   ResultReviewInProgress,
			wantReopened: true,
		},
		{
			// The reopen is guarded on request generation, and the two clocks
			// involved have different sources. If it does not fire, queueing
			// anyway would hand the caller a success the worker discards as a
			// terminal replay.
			name:             "a reopen that did not take reports the durable state",
			existing:         models.SessionPublication{State: models.SessionPublicationStateTerminalFailed},
			ensureKeepsState: models.SessionPublicationStateTerminalFailed,
			wantStatus:       ResultManualPublicationRequired,
			wantNotQueued:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
			existing := tt.existing
			existing.ID = uuid.New()
			existing.SessionID = f.sessionID
			f.publications.captured = &existing
			f.publications.ensureReturnsState = tt.ensureKeepsState

			result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{
				Source: models.SessionPublicationSourceUser, TriggerKind: models.SessionPublicationTriggerExplicitAction,
			})
			require.NoError(t, err, "a repeated publication request should be accepted")
			require.Equal(t, tt.wantStatus, result.Status, "a repeated request should report the existing intent's durable state")
			if tt.wantNotQueued {
				require.Nil(t, f.jobs.payload, "a publication that is still terminal must not be queued")
				require.NotNil(t, result.Reason, "a settled publication should explain why it cannot advance")
				return
			}
			if tt.wantReopened {
				require.NotEqual(t, existing.ID, *result.PublicationID, "a retryable terminal outcome should be reopened through EnsureRequested")
				require.NotNil(t, f.jobs.payload, "a reopened publication should be queued again")
				return
			}
			require.Equal(t, existing.ID, *result.PublicationID, "a live intent should be rejoined, not replaced")
			require.Nil(t, f.jobs.payload, "rejoining a live intent should not queue a duplicate job")
		})
	}
}

func TestCoordinatorRequestPullRequestReopensTerminalDraftWithExistingPR(t *testing.T) {
	t.Parallel()

	f := newCoordinatorFixture(t, models.AutomaticFollowThroughOrgSettings{}, nil, nil, true, nil)
	publicationID, prNumber := uuid.New(), 42
	f.publications.captured = &models.SessionPublication{
		ID: publicationID, SessionID: f.sessionID,
		State:          models.SessionPublicationStateTerminalFailed,
		HandoffMode:    models.PRHandoffModeDraftFirst,
		GitHubPRNumber: &prNumber,
	}
	f.coordinator.pullRequests = coordinatorPullRequestStore{pr: &models.PullRequest{
		ID: uuid.New(), OrgID: f.orgID, GitHubPRNumber: prNumber,
	}}

	result, err := f.coordinator.RequestPullRequest(context.Background(), f.orgID, f.sessionID, RequestPullRequest{
		Source: models.SessionPublicationSourceUser, TriggerKind: models.SessionPublicationTriggerExplicitAction,
	})

	require.NoError(t, err, "a terminal draft-first publication should reopen around its existing draft")
	require.Equal(t, ResultReviewInProgress, result.Status, "the existing draft should resume review instead of reporting an already-published PR")
	require.NotNil(t, f.jobs.payload, "the reopened draft publication should enqueue its durable worker")
	require.NotNil(t, f.publications.captured, "the reopened draft publication should be persisted")
	require.Equal(t, models.PRHandoffModeDraftFirst, f.publications.captured.HandoffMode, "the reopened publication should retain the existing draft handoff")
	require.Equal(t, true, f.jobs.payload["draft"], "the resumed worker should reuse the existing draft lifecycle")
}

func TestExistingPublicationResult(t *testing.T) {
	t.Parallel()

	publicationID, sessionID, loopID := uuid.New(), uuid.New(), uuid.New()
	prURL := "https://github.com/assembledhq/143/pull/42"
	tests := []struct {
		name        string
		publication models.SessionPublication
		wantStatus  ResultStatus
		wantReason  bool
	}{
		{
			name: "pending review rejoins the active loop",
			publication: models.SessionPublication{
				ID: publicationID, SessionID: sessionID, ReviewLoopID: &loopID,
				ReviewGateState: models.SessionPublicationReviewGatePending,
			},
			wantStatus: ResultReviewInProgress,
		},
		{
			name: "recorded draft awaiting finalization remains queued",
			publication: models.SessionPublication{
				ID: publicationID, SessionID: sessionID, GitHubPRURL: &prURL,
				State: models.SessionPublicationStateRecorded, ReviewGateState: models.SessionPublicationReviewGatePassed,
			},
			wantStatus: ResultPRQueued,
		},
		{
			// ResultBlocked means "durable but nothing enqueued", which callers
			// surface as a retryable 500. A review awaiting a human is a settled
			// state the user must act on, so it must not borrow that status.
			name: "review attention needs a manual decision",
			publication: models.SessionPublication{
				ID: publicationID, SessionID: sessionID, ReviewGateState: models.SessionPublicationReviewGateNeedsHuman,
			},
			wantStatus: ResultManualPublicationRequired, wantReason: true,
		},
		{
			name: "completed publication is already published",
			publication: models.SessionPublication{
				ID: publicationID, SessionID: sessionID, State: models.SessionPublicationStateCompleted,
			},
			wantStatus: ResultAlreadyPublished,
		},
		{
			// Reachable when the generation-guarded reopen does not take: the
			// caller must be told the row is still settled.
			name: "a no-op outcome that could not be reopened needs attention",
			publication: models.SessionPublication{
				ID: publicationID, SessionID: sessionID, State: models.SessionPublicationStateCompletedNoop,
			},
			wantStatus: ResultManualPublicationRequired, wantReason: true,
		},
		{
			name: "a terminal failure that could not be reopened needs attention",
			publication: models.SessionPublication{
				ID: publicationID, SessionID: sessionID, State: models.SessionPublicationStateTerminalFailed,
			},
			wantStatus: ResultManualPublicationRequired, wantReason: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := existingPublicationResult(tt.publication)
			require.Equal(t, tt.wantStatus, result.Status, "existing durable intent should return its current asynchronous state")
			require.Equal(t, tt.wantReason, result.Reason != nil, "blocked existing intent should explain why it cannot advance")
			require.Equal(t, tt.publication.ReviewLoopID, result.ReviewLoopID, "existing intent should preserve its review loop link")
			require.Equal(t, tt.publication.GitHubPRURL, result.PullRequestURL, "existing intent should preserve its draft or pull request URL")
		})
	}
}

type assertiveError string

func (e assertiveError) Error() string { return string(e) }
