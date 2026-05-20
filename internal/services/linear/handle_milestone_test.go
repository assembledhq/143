package linear

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// fakeProviderStateStore is an in-memory implementation of providerStateStore
// keyed by linkID. Tests use it instead of the real DB-backed store so we
// can assert on persisted state without spinning up pgxmock.
type fakeProviderStateStore struct {
	mu      sync.Mutex
	rows    map[uuid.UUID]db.LinearProviderState
	getErr  error
	uperr   error
	mergeFn func(orgID, linkID uuid.UUID, patch db.LinearProviderState)
}

func newFakeProviderStateStore() *fakeProviderStateStore {
	return &fakeProviderStateStore{rows: map[uuid.UUID]db.LinearProviderState{}}
}

func (f *fakeProviderStateStore) Get(_ context.Context, _, linkID uuid.UUID) (db.LinearProviderState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return db.LinearProviderState{}, f.getErr
	}
	return f.rows[linkID], nil
}

func (f *fakeProviderStateStore) Upsert(_ context.Context, _, linkID uuid.UUID, state db.LinearProviderState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uperr != nil {
		return f.uperr
	}
	f.rows[linkID] = state
	return nil
}

func (f *fakeProviderStateStore) Merge(_ context.Context, orgID, linkID uuid.UUID, patch db.LinearProviderState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mergeFn != nil {
		f.mergeFn(orgID, linkID, patch)
	}
	current := f.rows[linkID]
	f.rows[linkID] = db.MergeLinearProviderState(current, patch)
	return nil
}

// fakeStateEventStore captures Insert calls for assertion. The real store
// raises ErrLinearStateEventExists on (session_id, issue_id, event_kind)
// duplicates; the fake mirrors that contract so HandleStateTransition's
// fire-once branch is exercised.
type fakeStateEventStore struct {
	mu      sync.Mutex
	inserts []db.LinearStateEventInput
	seen    map[string]bool
}

func newFakeStateEventStore() *fakeStateEventStore {
	return &fakeStateEventStore{seen: map[string]bool{}}
}

func (f *fakeStateEventStore) Insert(_ context.Context, _ uuid.UUID, in db.LinearStateEventInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := in.SessionID.String() + "|" + in.IssueID.String() + "|" + string(in.EventKind)
	if f.seen[key] {
		return db.ErrLinearStateEventExists
	}
	f.seen[key] = true
	f.inserts = append(f.inserts, in)
	return nil
}

// fakeLinearClient implements the linear.Client interface with counters so
// tests can assert "exactly one CreateComment was called even with a race"
// and similar invariants.
type fakeLinearClient struct {
	mu                     sync.Mutex
	createCommentCalls     int
	updateCommentCalls     int
	createOrUpdateCalls    int
	updateStateCalls       int
	findOrphanCommentCalls int
	hasGitHubCalls         int
	humanEdited            bool
	hasGitHubAttachment    bool
	currentIssue           *FetchedIssue
	commentIDToReturn      string
	attachmentToReturn     AttachmentResult
	target                 *WorkflowState
	updateStateErr         error
	attachmentErr          error
	createCommentErr       error
	humanEditedErr         error
	hasGitHubErr           error
	findOrphanCommentID    string
	findOrphanCommentErr   error
	agentSessionUpdateErr  error
}

func newFakeLinearClient() *fakeLinearClient {
	return &fakeLinearClient{
		commentIDToReturn:  "linear-comment-1",
		attachmentToReturn: AttachmentResult{ID: "linear-attachment-1", URL: "https://linear.app/attachment/1"},
		target:             &WorkflowState{ID: "ws-id", Name: "In Progress", Type: "started"},
		currentIssue:       &FetchedIssue{StateName: "Todo", StateType: "unstarted", TeamID: "team-1"},
	}
}

func (f *fakeLinearClient) FetchIssue(context.Context, string) (*FetchedIssue, error) {
	if f.currentIssue == nil {
		return nil, errors.New("FetchIssue not stubbed")
	}
	return f.currentIssue, nil
}

func (f *fakeLinearClient) ListTeamKeys(context.Context) ([]TeamKeyInfo, error) {
	return nil, errors.New("ListTeamKeys not stubbed")
}

func (f *fakeLinearClient) CreateOrUpdateAttachment(_ context.Context, _ AttachmentWriteInput) (AttachmentResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createOrUpdateCalls++
	if f.attachmentErr != nil {
		return AttachmentResult{}, f.attachmentErr
	}
	return f.attachmentToReturn, nil
}

func (f *fakeLinearClient) CreateComment(_ context.Context, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCommentCalls++
	if f.createCommentErr != nil {
		return "", f.createCommentErr
	}
	return f.commentIDToReturn, nil
}

func (f *fakeLinearClient) UpdateComment(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCommentCalls++
	return nil
}

func (f *fakeLinearClient) FindRecentBotCommentByURL(_ context.Context, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findOrphanCommentCalls++
	return f.findOrphanCommentID, f.findOrphanCommentErr
}

func (f *fakeLinearClient) WorkflowStateForType(_ context.Context, _ string, _ []string, _ string) (*WorkflowState, error) {
	return f.target, nil
}

func (f *fakeLinearClient) UpdateIssueState(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateStateCalls++
	return f.updateStateErr
}

func (f *fakeLinearClient) IssueRecentHumanEdits(context.Context, string, time.Time) (bool, error) {
	return f.humanEdited, f.humanEditedErr
}

func (f *fakeLinearClient) HasGitHubIntegrationAttachment(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hasGitHubCalls++
	return f.hasGitHubAttachment, f.hasGitHubErr
}

// AgentActivityCreate / AgentSessionUpdate / AgentSessionGet are the agent
// surface added in phase 1 of the linear-agent rollout. The milestone tests
// don't exercise the inbound agent path; these stubs satisfy the interface
// without doing useful work.
func (f *fakeLinearClient) AgentActivityCreate(context.Context, AgentActivityInput) (AgentActivityResult, error) {
	return AgentActivityResult{}, nil
}

func (f *fakeLinearClient) AgentSessionUpdate(context.Context, AgentSessionUpdateInput) error {
	return f.agentSessionUpdateErr
}

func (f *fakeLinearClient) AgentSessionGet(context.Context, string) (*FetchedAgentSession, error) {
	return nil, ErrAgentSessionNotFound
}

func (f *fakeLinearClient) FetchComment(context.Context, string) (*FetchedComment, error) {
	return nil, ErrCommentNotFound
}

// fakeIntegrationReader / fakeCredentialReader return a stable "Linear is
// active" combo so HandleMilestone reaches the API call path. Both are
// intentionally minimal — the LinearPrivate short-circuit in HandleMilestone
// fires before either is consulted, which is exactly the property the
// disabled-flag tests pin.
type fakeIntegrationReader struct{}

func (fakeIntegrationReader) GetByOrgAndProvider(context.Context, uuid.UUID, string) (models.Integration, error) {
	return models.Integration{Status: "active"}, nil
}

type fakeCredentialReader struct{}

func (fakeCredentialReader) Get(context.Context, uuid.UUID, models.ProviderName) (*models.DecryptedCredential, error) {
	return &models.DecryptedCredential{Config: models.LinearConfig{AccessToken: "tok"}}, nil
}

// buildTestService stitches together a Service with in-memory fakes. Pool
// is intentionally nil so withProviderStateLocked falls through to the
// non-transactional path — the row-lock semantics live in PostgreSQL and
// can't be exercised in unit tests, but the code-path coverage of the
// guard logic still matters.
func buildTestService(t *testing.T, client Client) (*Service, *fakeProviderStateStore, *fakeStateEventStore) {
	t.Helper()
	provider := newFakeProviderStateStore()
	events := newFakeStateEventStore()
	svc := &Service{
		logger:        zerolog.Nop(),
		integrations:  fakeIntegrationReader{},
		credentials:   fakeCredentialReader{},
		providerState: provider,
		stateEvents:   events,
		clientFactory: func(_ context.Context, _ string) (Client, error) { return client, nil },
		orgSettingsLoader: func(context.Context, uuid.UUID) (models.OrgSettings, error) {
			t := true
			return models.OrgSettings{LinearAutomation: models.LinearAutomationSettings{
				PostSessionLinks:           &t,
				MoveWorkflowStates:         &t,
				ReviewStateNamePreferences: models.DefaultLinearReviewStateNames,
			}}, nil
		},
		appBaseURL: "https://app.test.example",
	}
	return svc, provider, events
}

func newPrimaryLink() models.SessionIssueLink {
	return models.SessionIssueLink{
		ID:      uuid.New(),
		IssueID: uuid.New(),
		Role:    models.SessionIssueLinkRolePrimary,
	}
}

func newSession() *models.Session {
	return &models.Session{ID: uuid.New(), OrgID: uuid.New()}
}

func TestHandleAgentMilestoneLogsExternalURLPinFailures(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	var logs bytes.Buffer
	client := newFakeLinearClient()
	client.agentSessionUpdateErr = errors.New("linear update failed")
	svc, _, _ := buildTestService(t, client)
	svc.logger = zerolog.New(&logs)
	svc.agentSessions = db.NewLinearAgentSessionStore(mock)
	svc.agentActivities = db.NewLinearAgentActivityLogStore(mock)

	session := newSession()
	rowID := uuid.New()
	integrationID := uuid.New()
	now := time.Now().UTC()
	sessionID := session.ID

	mock.ExpectQuery("SELECT id, org_id").
		WithArgs(session.OrgID, session.ID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "integration_id", "linear_agent_session_id",
			"linear_issue_id", "linear_issue_identifier",
			"linear_app_user_id", "linear_creator_user_id",
			"session_id", "state", "last_event_received_at",
			"created_at", "updated_at",
		}).AddRow(
			rowID, session.OrgID, integrationID, "as_1",
			"iss_1", "ACS-1",
			"app_1", "user_1",
			&sessionID, "pending", &now,
			now, now,
		))
	mock.ExpectQuery("INSERT INTO linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "inserted"}).AddRow(uuid.New(), true))
	mock.ExpectExec("UPDATE linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = svc.HandleAgentMilestone(context.Background(), MilestoneInput{
		Event:    MilestonePROpened,
		Session:  session,
		Link:     newPrimaryLink(),
		PRNumber: 42,
	})
	require.NoError(t, err, "agent milestone external URL pin failures should remain best-effort")
	require.Contains(t, logs.String(), "agent milestone: failed to pin external URLs on AgentSession", "best-effort AgentSessionUpdate failures should be logged for operators")
	require.NoError(t, mock.ExpectationsWereMet(), "agent milestone should emit the activity before attempting the external URL pin")
}

// TestPinExternalURLsForEvent pins the set of milestone events on which we
// pin the 143 session deep-link onto the AgentSession header. The set must
// include Started so the link lands as early as possible, and PROpened so a
// failed first pin gets a second chance. Terminal events are excluded
// because re-pinning right before close is overhead with no UX benefit.
func TestPinExternalURLsForEvent(t *testing.T) {
	t.Parallel()

	require.True(t, pinExternalURLsForEvent(MilestoneStarted), "Started must pin so the deep link surfaces before the run finishes")
	require.True(t, pinExternalURLsForEvent(MilestonePROpened), "PROpened must pin as a retry of the Started pin")
	require.False(t, pinExternalURLsForEvent(MilestoneLinked), "Linked is suppressed entirely")
	require.False(t, pinExternalURLsForEvent(MilestonePRMerged), "terminal events do not need to re-pin")
	require.False(t, pinExternalURLsForEvent(MilestoneEndedNoPR), "terminal events do not need to re-pin")
	require.False(t, pinExternalURLsForEvent(MilestoneFailed), "terminal events do not need to re-pin")
}

func TestMilestoneFormattingAndStateHelpers(t *testing.T) {
	t.Parallel()

	title := "Fix checkout"
	session := &models.Session{Title: &title}
	if got := attachmentTitle(session); got != "143: Fix checkout" {
		t.Fatalf("attachmentTitle should include the session title, got %q", got)
	}
	if got := attachmentTitle(nil); got != "143 session" {
		t.Fatalf("attachmentTitle should fall back for nil sessions, got %q", got)
	}

	subtitleCases := []struct {
		event    MilestoneEvent
		prNumber int
		expected string
	}{
		{event: MilestonePROpened, prNumber: 42, expected: "PR #42 open"},
		{event: MilestonePROpened, expected: "PR open"},
		{event: MilestonePRMerged, prNumber: 42, expected: "PR #42 merged"},
		{event: MilestonePRMerged, expected: "PR merged"},
		{event: MilestoneEndedNoPR, expected: "Ended without PR"},
		{event: MilestoneFailed, expected: "Failed"},
		{event: MilestoneLinked, expected: "Running"},
	}
	for _, tt := range subtitleCases {
		if got := subtitleForMilestone(tt.event, tt.prNumber); got != tt.expected {
			t.Fatalf("subtitleForMilestone(%q, %d) = %q, want %q", tt.event, tt.prNumber, got, tt.expected)
		}
	}

	outcomeCases := []struct {
		event    MilestoneEvent
		expected db.LinearAttachmentOutcome
	}{
		{event: MilestonePROpened, expected: db.LinearAttachmentOutcomePROpen},
		{event: MilestonePRMerged, expected: db.LinearAttachmentOutcomeMerged},
		{event: MilestoneEndedNoPR, expected: db.LinearAttachmentOutcomeEndedNoPR},
		{event: MilestoneFailed, expected: db.LinearAttachmentOutcomeFailed},
		{event: MilestoneLinked, expected: db.LinearAttachmentOutcomeRunning},
	}
	for _, tt := range outcomeCases {
		if got := outcomeForMilestone(tt.event); got != tt.expected {
			t.Fatalf("outcomeForMilestone(%q) = %q, want %q", tt.event, got, tt.expected)
		}
	}

	commentCases := []struct {
		event    MilestoneEvent
		prNumber int
		contains string
	}{
		{event: MilestoneLinked, contains: "Started a session"},
		{event: MilestonePROpened, prNumber: 42, contains: "Pull request #42 opened"},
		{event: MilestonePROpened, contains: "Pull request opened"},
		{event: MilestonePRMerged, prNumber: 42, contains: "Pull request #42 merged"},
		{event: MilestonePRMerged, contains: "Pull request merged"},
		{event: MilestoneEndedNoPR, contains: "ended without opening"},
		{event: MilestoneFailed, contains: "failed"},
	}
	for _, tt := range commentCases {
		got := commentBodyForMilestone(tt.event, "ACS-1", "https://app.test/sessions/1", tt.prNumber)
		if !strings.Contains(got, botCommentPrefix) || !strings.Contains(got, tt.contains) {
			t.Fatalf("commentBodyForMilestone(%q, %d) = %q, want prefix and %q", tt.event, tt.prNumber, got, tt.contains)
		}
	}

	if got := teamKeyFromIdentifier("ACS-123"); got != "ACS" {
		t.Fatalf("teamKeyFromIdentifier should extract team key, got %q", got)
	}
	if got := teamKeyFromIdentifier("bad"); got != "" {
		t.Fatalf("teamKeyFromIdentifier should return empty for malformed identifiers, got %q", got)
	}

	eventKindCases := []struct {
		event    MilestoneEvent
		expected db.LinearStateEventKind
	}{
		{event: MilestoneLinked, expected: ""},
		{event: MilestoneStarted, expected: db.LinearStateEventStarted},
		{event: MilestonePROpened, expected: db.LinearStateEventPROpened},
		{event: MilestonePRMerged, expected: db.LinearStateEventPRMerged},
		{event: MilestoneEndedNoPR, expected: ""},
		{event: MilestoneFailed, expected: ""},
		{event: MilestoneEvent("unknown"), expected: ""},
	}
	for _, tt := range eventKindCases {
		if got := stateEventKindFor(tt.event); got != tt.expected {
			t.Fatalf("stateEventKindFor(%q) = %q, want %q", tt.event, got, tt.expected)
		}
	}

	stateTypeCases := []struct {
		event    MilestoneEvent
		expected string
	}{
		{event: MilestoneLinked, expected: ""},
		{event: MilestoneStarted, expected: "started"},
		{event: MilestonePROpened, expected: "started"},
		{event: MilestonePRMerged, expected: "completed"},
		{event: MilestoneFailed, expected: ""},
	}
	for _, tt := range stateTypeCases {
		if got := stateTypeFor(tt.event); got != tt.expected {
			t.Fatalf("stateTypeFor(%q) = %q, want %q", tt.event, got, tt.expected)
		}
	}
}

// TestHandleMilestone_RollingCommentTakesUpdateBranchAfterFirstWrite pins
// the rolling-comment idempotency contract: once provider_state.CommentID
// is set, subsequent milestones must hit UpdateComment, not CreateComment.
// Without this, every PR-opened/PR-merged event would create a fresh
// comment and overwhelm Linear assignees with notifications. This is the
// happy-path coverage of the design 62 §"One live comment, not three"
// invariant.
func TestHandleMilestone_RollingCommentTakesUpdateBranchAfterFirstWrite(t *testing.T) {
	t.Parallel()
	client := newFakeLinearClient()
	svc, _, _ := buildTestService(t, client)
	link := newPrimaryLink()
	session := newSession()

	if err := svc.HandleMilestone(context.Background(), MilestoneInput{
		Event:      MilestoneLinked,
		Session:    session,
		Link:       link,
		IssueID:    "linear-issue-id",
		IssueIdent: "ACS-1",
	}); err != nil {
		t.Fatalf("first HandleMilestone: %v", err)
	}
	if client.createCommentCalls != 1 {
		t.Fatalf("first call should CreateComment exactly once, got %d", client.createCommentCalls)
	}

	if err := svc.HandleMilestone(context.Background(), MilestoneInput{
		Event:      MilestonePROpened,
		Session:    session,
		Link:       link,
		IssueID:    "linear-issue-id",
		PRNumber:   42,
		IssueIdent: "ACS-1",
	}); err != nil {
		t.Fatalf("second HandleMilestone: %v", err)
	}
	if client.createCommentCalls != 1 {
		t.Fatalf("second call must NOT CreateComment again (got %d) — design 62 §\"One live comment\"", client.createCommentCalls)
	}
	if client.updateCommentCalls != 1 {
		t.Fatalf("second call must UpdateComment exactly once, got %d", client.updateCommentCalls)
	}
}

// TestHandleMilestone_RecoversOrphanedCommentFromPriorAttempt pins the
// recovery behavior for the lost-response zone. When a prior attempt
// created a comment server-side but the response was lost, our local
// state has CommentID="". On the next attempt, FindRecentBotCommentByURL
// finds the orphan (matched by session URL) and we take the UpdateComment
// branch instead of creating a duplicate.
func TestHandleMilestone_RecoversOrphanedCommentFromPriorAttempt(t *testing.T) {
	t.Parallel()
	client := newFakeLinearClient()
	client.findOrphanCommentID = "orphan-comment-7"
	svc, provider, _ := buildTestService(t, client)
	link := newPrimaryLink()
	session := newSession()

	if err := svc.HandleMilestone(context.Background(), MilestoneInput{
		Event:      MilestoneLinked,
		Session:    session,
		Link:       link,
		IssueID:    "linear-issue-id",
		IssueIdent: "ACS-1",
	}); err != nil {
		t.Fatalf("HandleMilestone returned error: %v", err)
	}
	if client.findOrphanCommentCalls != 1 {
		t.Fatalf("expected one FindRecentBotCommentByURL scan on first write, got %d", client.findOrphanCommentCalls)
	}
	if client.createCommentCalls != 0 {
		t.Fatalf("recovery must NOT call CreateComment; got %d (would have created a duplicate)", client.createCommentCalls)
	}
	if client.updateCommentCalls != 1 {
		t.Fatalf("recovery should UpdateComment exactly once on the orphan, got %d", client.updateCommentCalls)
	}
	if got := provider.rows[link.ID].CommentID; got != "orphan-comment-7" {
		t.Fatalf("provider state should record the recovered comment id; got %q", got)
	}
}

// TestHandleMilestone_RecoveryScanFailureFallsThroughToCreate verifies
// that a transient FindRecentBotCommentByURL failure is logged but does
// not block the milestone — we still issue commentCreate. Worst-case is
// a duplicate (same as before recovery existed), best-case is a normal
// first write.
func TestHandleMilestone_RecoveryScanFailureFallsThroughToCreate(t *testing.T) {
	t.Parallel()
	client := newFakeLinearClient()
	client.findOrphanCommentErr = errors.New("transient")
	svc, _, _ := buildTestService(t, client)
	link := newPrimaryLink()
	session := newSession()

	if err := svc.HandleMilestone(context.Background(), MilestoneInput{
		Event:      MilestoneLinked,
		Session:    session,
		Link:       link,
		IssueID:    "linear-issue-id",
		IssueIdent: "ACS-1",
	}); err != nil {
		t.Fatalf("HandleMilestone should not surface scan errors: %v", err)
	}
	if client.findOrphanCommentCalls != 1 {
		t.Fatalf("expected one scan attempt, got %d", client.findOrphanCommentCalls)
	}
	if client.createCommentCalls != 1 {
		t.Fatalf("scan failure should fall through to CreateComment, got %d", client.createCommentCalls)
	}
}

// TestHandleMilestone_LinearPrivateShortCircuitsBeforeAnyAPICall pins the
// "private session" guard: when LinearPrivate=true, no Linear API call
// fires and the only side effect is recording LastSkippedReason in
// provider_state. Without this guard, a private session could leak its
// existence to Linear assignees via attachment + comment writes.
func TestHandleMilestone_LinearPrivateShortCircuitsBeforeAnyAPICall(t *testing.T) {
	t.Parallel()
	client := newFakeLinearClient()
	svc, provider, _ := buildTestService(t, client)
	link := newPrimaryLink()
	session := newSession()
	session.LinearPrivate = true

	if err := svc.HandleMilestone(context.Background(), MilestoneInput{
		Event:      MilestoneLinked,
		Session:    session,
		Link:       link,
		IssueID:    "linear-issue-id",
		IssueIdent: "ACS-1",
	}); err != nil {
		t.Fatalf("HandleMilestone returned error: %v", err)
	}
	if client.createOrUpdateCalls != 0 || client.createCommentCalls != 0 || client.updateCommentCalls != 0 {
		t.Fatalf("private session must not hit Linear: attachments=%d createComment=%d updateComment=%d",
			client.createOrUpdateCalls, client.createCommentCalls, client.updateCommentCalls)
	}
	state := provider.rows[link.ID]
	if state.LastSkippedReason != string(db.LinearStateSkipPrivateSession) {
		t.Fatalf("expected LastSkippedReason=%q, got %q",
			db.LinearStateSkipPrivateSession, state.LastSkippedReason)
	}
}

func TestHandleMilestone_GuardsAndErrors(t *testing.T) {
	t.Parallel()

	t.Run("nil session returns error", func(t *testing.T) {
		t.Parallel()
		svc, _, _ := buildTestService(t, newFakeLinearClient())
		if err := svc.HandleMilestone(context.Background(), MilestoneInput{}); err == nil {
			t.Fatal("HandleMilestone must reject nil sessions")
		}
	})

	t.Run("related link skips writes", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		svc, _, _ := buildTestService(t, client)
		link := newPrimaryLink()
		link.Role = models.SessionIssueLinkRoleRelated
		if err := svc.HandleMilestone(context.Background(), MilestoneInput{Session: newSession(), Link: link, IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("related link should skip without error: %v", err)
		}
		if client.createOrUpdateCalls != 0 || client.createCommentCalls != 0 {
			t.Fatalf("related links must not write to Linear, got attachment=%d comment=%d", client.createOrUpdateCalls, client.createCommentCalls)
		}
	})

	t.Run("per-team setting suppresses session links", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		svc, _, _ := buildTestService(t, client)
		f := false
		svc.orgSettingsLoader = func(context.Context, uuid.UUID) (models.OrgSettings, error) {
			return models.OrgSettings{LinearAutomation: models.LinearAutomationSettings{
				PerTeam: map[string]models.LinearTeamAutomationOverride{
					"ACS": {PostSessionLinks: &f},
				},
			}}, nil
		}
		if err := svc.HandleMilestone(context.Background(), MilestoneInput{Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("disabled team setting should skip without error: %v", err)
		}
		if client.createOrUpdateCalls != 0 || client.createCommentCalls != 0 {
			t.Fatalf("disabled team setting must not write to Linear, got attachment=%d comment=%d", client.createOrUpdateCalls, client.createCommentCalls)
		}
	})

	t.Run("per-team true override beats org-level false default", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		svc, _, _ := buildTestService(t, client)
		tt := true
		ff := false
		svc.orgSettingsLoader = func(context.Context, uuid.UUID) (models.OrgSettings, error) {
			return models.OrgSettings{LinearAutomation: models.LinearAutomationSettings{
				PostSessionLinks: &ff,
				PerTeam: map[string]models.LinearTeamAutomationOverride{
					"ACS": {PostSessionLinks: &tt},
				},
			}}, nil
		}
		if err := svc.HandleMilestone(context.Background(), MilestoneInput{Event: MilestoneLinked, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("per-team true override should permit writes: %v", err)
		}
		if client.createOrUpdateCalls != 1 || client.createCommentCalls != 1 {
			t.Fatalf("per-team true override must permit writes despite org false default, got attachment=%d comment=%d", client.createOrUpdateCalls, client.createCommentCalls)
		}
	})

	t.Run("org-level false default suppresses untargeted teams", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		svc, _, _ := buildTestService(t, client)
		ff := false
		svc.orgSettingsLoader = func(context.Context, uuid.UUID) (models.OrgSettings, error) {
			return models.OrgSettings{LinearAutomation: models.LinearAutomationSettings{
				PostSessionLinks: &ff,
				// No per-team overrides — every team inherits the false default.
			}}, nil
		}
		if err := svc.HandleMilestone(context.Background(), MilestoneInput{Event: MilestoneLinked, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "OTHER-1"}); err != nil {
			t.Fatalf("org-level false should skip without error: %v", err)
		}
		if client.createOrUpdateCalls != 0 || client.createCommentCalls != 0 {
			t.Fatalf("org-level false must suppress writes for non-overridden teams, got attachment=%d comment=%d", client.createOrUpdateCalls, client.createCommentCalls)
		}
	})

	t.Run("attachment write errors are wrapped", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.attachmentErr = errors.New("attachment failed")
		svc, _, _ := buildTestService(t, client)
		err := svc.HandleMilestone(context.Background(), MilestoneInput{Event: MilestoneLinked, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"})
		if err == nil || !strings.Contains(err.Error(), "write linear attachment") {
			t.Fatalf("attachment errors should be wrapped, got %v", err)
		}
	})

	t.Run("comment create errors are wrapped", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.createCommentErr = errors.New("comment failed")
		svc, _, _ := buildTestService(t, client)
		err := svc.HandleMilestone(context.Background(), MilestoneInput{Event: MilestoneLinked, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"})
		if err == nil || !strings.Contains(err.Error(), "create linear comment") {
			t.Fatalf("comment errors should be wrapped, got %v", err)
		}
	})
}

// TestHandleStateTransition_LinearStateSyncDisabledRecordsSkip pins the
// "user disabled state sync but kept comments" path: the audit trail
// records `disabled_by_user`, no Linear API call fires, and HandleMilestone
// (the visibility surface) is still allowed to write. This is what makes
// LinearPrivate and LinearStateSyncDisabled distinct controls — see
// design 62 §"Composer controls must express distinct semantics".
func TestHandleStateTransition_LinearStateSyncDisabledRecordsSkip(t *testing.T) {
	t.Parallel()
	client := newFakeLinearClient()
	svc, _, events := buildTestService(t, client)
	link := newPrimaryLink()
	session := newSession()
	session.LinearStateSyncDisabled = true

	if err := svc.HandleStateTransition(context.Background(), MilestoneInput{
		Event:      MilestoneStarted,
		Session:    session,
		Link:       link,
		IssueID:    "linear-issue-id",
		IssueIdent: "ACS-1",
	}); err != nil {
		t.Fatalf("HandleStateTransition returned error: %v", err)
	}
	if client.updateStateCalls != 0 {
		t.Fatalf("state-sync disabled must not call UpdateIssueState (got %d)", client.updateStateCalls)
	}
	if len(events.inserts) != 1 {
		t.Fatalf("expected 1 skip event recorded, got %d", len(events.inserts))
	}
	got := events.inserts[0].SkippedReason
	if got != db.LinearStateSkipDisabledByUser {
		t.Fatalf("expected SkippedReason=%q, got %q", db.LinearStateSkipDisabledByUser, got)
	}
}

func TestHandleStateTransition_GuardsAndSkips(t *testing.T) {
	t.Parallel()

	t.Run("nil session returns error", func(t *testing.T) {
		t.Parallel()
		svc, _, _ := buildTestService(t, newFakeLinearClient())
		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{}); err == nil {
			t.Fatal("HandleStateTransition must reject nil sessions")
		}
	})

	t.Run("non-transition milestones record nothing", func(t *testing.T) {
		t.Parallel()
		tests := []MilestoneEvent{
			MilestoneEvent("noop"),
			MilestoneEndedNoPR,
			MilestoneFailed,
		}
		for _, event := range tests {
			event := event
			t.Run(string(event), func(t *testing.T) {
				t.Parallel()

				client := newFakeLinearClient()
				svc, _, events := buildTestService(t, client)
				err := svc.HandleStateTransition(context.Background(), MilestoneInput{Event: event, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"})
				if err != nil {
					t.Fatalf("terminal/non-transition milestone should not transition: %v", err)
				}
				if len(events.inserts) != 0 || client.updateStateCalls != 0 {
					t.Fatalf("terminal/non-transition milestone should not record or update, events=%d updates=%d", len(events.inserts), client.updateStateCalls)
				}
			})
		}
	})

	t.Run("related link records not-primary skip", func(t *testing.T) {
		t.Parallel()
		svc, _, events := buildTestService(t, newFakeLinearClient())
		link := newPrimaryLink()
		link.Role = models.SessionIssueLinkRoleRelated
		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{Event: MilestoneStarted, Session: newSession(), Link: link, IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("related link skip should not error: %v", err)
		}
		if got := events.inserts[0].SkippedReason; got != db.LinearStateSkipNotPrimary {
			t.Fatalf("expected not_primary skip, got %q", got)
		}
	})

	t.Run("per-team setting records skip", func(t *testing.T) {
		t.Parallel()
		svc, _, events := buildTestService(t, newFakeLinearClient())
		f := false
		svc.orgSettingsLoader = func(context.Context, uuid.UUID) (models.OrgSettings, error) {
			return models.OrgSettings{LinearAutomation: models.LinearAutomationSettings{
				PerTeam: map[string]models.LinearTeamAutomationOverride{
					"ACS": {MoveWorkflowStates: &f},
				},
			}}, nil
		}
		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{Event: MilestoneStarted, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("per-team disabled skip should not error: %v", err)
		}
		if got := events.inserts[0].SkippedReason; got != db.LinearStateSkipPerTeamDisabled {
			t.Fatalf("expected per_team_disabled skip, got %q", got)
		}
	})

	t.Run("recent human edit records skip", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.humanEdited = true
		svc, _, events := buildTestService(t, client)
		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{Event: MilestoneStarted, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("human edit skip should not error: %v", err)
		}
		if got := events.inserts[0].SkippedReason; got != db.LinearStateSkipUserRecentEdit {
			t.Fatalf("expected user_recent_edit skip, got %q", got)
		}
	})

	t.Run("PR merge coexisting GitHub attachment records skip", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.hasGitHubAttachment = true
		svc, _, events := buildTestService(t, client)
		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{Event: MilestonePRMerged, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("coexistence skip should not error: %v", err)
		}
		if got := events.inserts[0].SkippedReason; got != db.LinearStateSkipLinearGitHubIntegration {
			t.Fatalf("expected github integration skip, got %q", got)
		}
		if client.updateStateCalls != 0 {
			t.Fatalf("UpdateIssueState must NOT fire on coexistence skip (got %d)", client.updateStateCalls)
		}
	})

	// PR-open coexistence: Linear's GitHub integration moves issues to
	// "In Review" on PR open (mirror of the PR-merge → "Done" behavior).
	// If we transition on top, the issue takes two state moves and gets
	// double cycle/sprint membership. The guard must apply to BOTH events.
	t.Run("PR open coexisting GitHub attachment records skip", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.hasGitHubAttachment = true
		svc, _, events := buildTestService(t, client)
		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{Event: MilestonePROpened, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("coexistence skip should not error: %v", err)
		}
		if len(events.inserts) != 1 {
			t.Fatalf("expected 1 skip event recorded, got %d", len(events.inserts))
		}
		if got := events.inserts[0].SkippedReason; got != db.LinearStateSkipLinearGitHubIntegration {
			t.Fatalf("expected github integration skip on PR open, got %q", got)
		}
		if client.updateStateCalls != 0 {
			t.Fatalf("UpdateIssueState must NOT fire when Linear's integration already handles PR open (got %d)", client.updateStateCalls)
		}
	})

	// Session-start must NOT trip the coexistence guard — Linear's GitHub
	// integration only runs on PR-lifecycle events, so suppressing our
	// own "started" move on session-start would silently lose the only
	// transition we own.
	t.Run("session started does not trip coexistence guard", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.hasGitHubAttachment = true
		svc, _, events := buildTestService(t, client)
		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{Event: MilestoneStarted, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("started transition should not error: %v", err)
		}
		if client.updateStateCalls != 1 {
			t.Fatalf("started event must fire UpdateIssueState even when GitHub integration is present (got %d)", client.updateStateCalls)
		}
		if len(events.inserts) != 1 {
			t.Fatalf("expected 1 transition event recorded, got %d", len(events.inserts))
		}
		if got := events.inserts[0].SkippedReason; got != "" {
			t.Fatalf("started event must record a real transition, not a skip (got SkippedReason=%q)", got)
		}
	})

	// Coexistence cache TTL: a "true" cached observation suppresses our
	// state moves, so the cache must re-check at the active TTL window
	// (CoexistsCheckTTLActive=1h) so an operator who removes Linear's
	// GitHub integration mid-day starts seeing our transitions again
	// quickly. A "false" observation gets the long TTL (CoexistsCheckTTL=
	// 24h) — re-checking on every milestone would be wasted API calls and
	// the safe default is "transition." Tests below cover all three legs.
	t.Run("coexistence cache: fresh true is reused (no API call)", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		// Default behavior would short-circuit the API; we want to assert
		// the call count, so set a stub error too — if the real client
		// were called, the test would fail with "network hiccup".
		client.hasGitHubErr = errors.New("must not be called for fresh-true cache")
		svc, provider, _ := buildTestService(t, client)
		link := newPrimaryLink()
		// Pre-populate provider state with a fresh "coexists=true" cache.
		now := time.Now()
		_ = provider.Upsert(context.Background(), uuid.Nil, link.ID, db.LinearProviderState{
			CoexistsWithGitHubIntegration: db.BoolPtr(true),
			CoexistsCheckedAt:             db.TimePtr(now.Add(-30 * time.Minute)), // < 1h active TTL
		})

		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{
			Event: MilestonePRMerged, Session: newSession(), Link: link,
			IssueID: "linear-issue-id", IssueIdent: "ACS-1",
		}); err != nil {
			t.Fatalf("fresh-true coexistence skip should not error: %v", err)
		}
		if client.hasGitHubCalls != 0 {
			t.Fatalf("fresh-true cache must NOT re-query Linear, got %d calls", client.hasGitHubCalls)
		}
		if client.updateStateCalls != 0 {
			t.Fatalf("UpdateIssueState must NOT fire when coexistence cache says skip (got %d)", client.updateStateCalls)
		}
	})

	t.Run("coexistence cache: stale true (>1h) re-fetches", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.hasGitHubAttachment = false // operator removed Linear's GitHub integration
		svc, provider, _ := buildTestService(t, client)
		link := newPrimaryLink()
		// Cached "true" observation older than the active TTL must not be
		// trusted — operator may have removed Linear's integration mid-day.
		_ = provider.Upsert(context.Background(), uuid.Nil, link.ID, db.LinearProviderState{
			CoexistsWithGitHubIntegration: db.BoolPtr(true),
			CoexistsCheckedAt:             db.TimePtr(time.Now().Add(-2 * time.Hour)), // > 1h active TTL
		})

		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{
			Event: MilestonePRMerged, Session: newSession(), Link: link,
			IssueID: "linear-issue-id", IssueIdent: "ACS-1",
		}); err != nil {
			t.Fatalf("stale-true re-fetch + transition should not error: %v", err)
		}
		if client.hasGitHubCalls != 1 {
			t.Fatalf("stale-true cache must re-query Linear exactly once, got %d", client.hasGitHubCalls)
		}
		if client.updateStateCalls != 1 {
			t.Fatalf("re-fetched coexists=false must let UpdateIssueState fire, got %d", client.updateStateCalls)
		}
	})

	t.Run("coexistence cache: fresh false is reused (no API call)", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.hasGitHubErr = errors.New("must not be called for fresh-false cache")
		svc, provider, _ := buildTestService(t, client)
		link := newPrimaryLink()
		// Cached "no GitHub integration" within the long TTL must be
		// trusted — re-checking would burn API quota for nothing.
		_ = provider.Upsert(context.Background(), uuid.Nil, link.ID, db.LinearProviderState{
			CoexistsWithGitHubIntegration: db.BoolPtr(false),
			CoexistsCheckedAt:             db.TimePtr(time.Now().Add(-2 * time.Hour)), // < 24h passive TTL
		})

		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{
			Event: MilestonePRMerged, Session: newSession(), Link: link,
			IssueID: "linear-issue-id", IssueIdent: "ACS-1",
		}); err != nil {
			t.Fatalf("fresh-false coexistence transition should not error: %v", err)
		}
		if client.hasGitHubCalls != 0 {
			t.Fatalf("fresh-false cache must NOT re-query Linear, got %d", client.hasGitHubCalls)
		}
		if client.updateStateCalls != 1 {
			t.Fatalf("fresh-false cache must let UpdateIssueState fire, got %d", client.updateStateCalls)
		}
	})

	t.Run("coexistence cache: nil checkedAt always re-fetches", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		svc, provider, _ := buildTestService(t, client)
		link := newPrimaryLink()
		// Legacy row written before CoexistsCheckedAt existed, or any row
		// with a nil timestamp, must always re-query so we don't trust an
		// indeterminate state.
		_ = provider.Upsert(context.Background(), uuid.Nil, link.ID, db.LinearProviderState{
			CoexistsWithGitHubIntegration: db.BoolPtr(false),
		})

		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{
			Event: MilestonePRMerged, Session: newSession(), Link: link,
			IssueID: "linear-issue-id", IssueIdent: "ACS-1",
		}); err != nil {
			t.Fatalf("nil-checkedAt re-fetch should not error: %v", err)
		}
		if client.hasGitHubCalls != 1 {
			t.Fatalf("nil checkedAt must re-query exactly once, got %d", client.hasGitHubCalls)
		}
	})

	t.Run("workspace slug drift records workspace-mismatch skip", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.currentIssue = &FetchedIssue{
			StateName:     "Todo",
			StateType:     "unstarted",
			TeamID:        "team-1",
			WorkspaceSlug: "renamed-workspace",
		}
		svc, provider, events := buildTestService(t, client)
		link := newPrimaryLink()
		// Persisted state remembers a different workspace slug than what
		// FetchIssue now returns — integration was reconnected mid-flight or
		// Linear renamed the workspace. We must skip rather than write under
		// the wrong workspace's branding.
		_ = provider.Upsert(context.Background(), uuid.Nil, link.ID, db.LinearProviderState{
			WorkspaceSlug: "original-workspace",
			TeamID:        "team-1",
		})

		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{
			Event:      MilestoneStarted,
			Session:    newSession(),
			Link:       link,
			IssueID:    "linear-issue-id",
			IssueIdent: "ACS-1",
		}); err != nil {
			t.Fatalf("workspace-mismatch skip should not error: %v", err)
		}
		if client.updateStateCalls != 0 {
			t.Fatalf("UpdateIssueState must NOT fire on workspace mismatch (got %d)", client.updateStateCalls)
		}
		if len(events.inserts) != 1 {
			t.Fatalf("expected 1 skip event recorded, got %d", len(events.inserts))
		}
		if got := events.inserts[0].SkippedReason; got != db.LinearStateSkipWorkspaceMismatch {
			t.Fatalf("expected workspace_mismatch skip, got %q", got)
		}
	})

	t.Run("nil target records no-target-state skip", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.target = nil
		svc, _, events := buildTestService(t, client)
		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{Event: MilestoneStarted, Session: newSession(), Link: newPrimaryLink(), IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("nil target skip should not error: %v", err)
		}
		if got := events.inserts[0].SkippedReason; got != db.LinearStateSkipNoTargetState {
			t.Fatalf("expected no_target_state skip, got %q", got)
		}
	})

	t.Run("current Linear state overrides stale cached provider state", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.currentIssue = &FetchedIssue{StateName: "Done", StateType: "completed", TeamID: "team-1"}
		svc, provider, events := buildTestService(t, client)
		link := newPrimaryLink()
		provider.rows[link.ID] = db.LinearProviderState{
			LastKnownStateName: "Todo",
			LastKnownStateType: "unstarted",
			TeamID:             "team-1",
		}

		if err := svc.HandleStateTransition(context.Background(), MilestoneInput{Event: MilestonePROpened, Session: newSession(), Link: link, IssueID: "linear-issue-id", IssueIdent: "ACS-1"}); err != nil {
			t.Fatalf("completed current state should record a skip, not error: %v", err)
		}
		if client.updateStateCalls != 0 {
			t.Fatalf("UpdateIssueState must NOT fire when current Linear state is already completed (got %d)", client.updateStateCalls)
		}
		if len(events.inserts) != 1 {
			t.Fatalf("expected 1 skip event recorded, got %d", len(events.inserts))
		}
		if got := events.inserts[0].SkippedReason; got != db.LinearStateSkipAlreadyPastTarget {
			t.Fatalf("expected already_past_target from current Linear state, got %q", got)
		}
	})
}

// TestHandleStateTransition_GuardLookupErrorsAreRetried verifies that
// transient lookup failures from IssueRecentHumanEdits and
// HasGitHubIntegrationAttachment surface as errors so the worker retries,
// rather than being silently treated as "no edits / no integration". A
// silent fall-through here would let us clobber a manual move the user
// just made or double-transition an issue Linear's GitHub integration
// already moved.
func TestHandleStateTransition_GuardLookupErrorsAreRetried(t *testing.T) {
	t.Parallel()

	t.Run("IssueRecentHumanEdits failure surfaces error", func(t *testing.T) {
		t.Parallel()
		client := newFakeLinearClient()
		client.humanEditedErr = errors.New("network hiccup")
		svc, _, events := buildTestService(t, client)

		err := svc.HandleStateTransition(context.Background(), MilestoneInput{
			Event:      MilestoneStarted,
			Session:    newSession(),
			Link:       newPrimaryLink(),
			IssueID:    "linear-issue-id",
			IssueIdent: "ACS-1",
		})
		if err == nil {
			t.Fatal("HandleStateTransition must surface IssueRecentHumanEdits errors so the worker retries")
		}
		if client.updateStateCalls != 0 {
			t.Fatalf("UpdateIssueState must NOT be called when the recent-edits guard lookup failed (got %d)", client.updateStateCalls)
		}
		if len(events.inserts) != 0 {
			t.Fatalf("no fire-once claim must be recorded when the guard lookup failed (got %d)", len(events.inserts))
		}
	})

	// Both PR-driven events run through the coexistence guard, so a
	// transient API failure on either must surface for a retry rather
	// than silently treat as "no integration" — see writes.go
	// isPRDrivenTransition. Sub-testing the same expectation across both
	// events guards against a future change that drops one branch.
	for _, ev := range []MilestoneEvent{MilestonePROpened, MilestonePRMerged} {
		ev := ev
		t.Run("HasGitHubIntegrationAttachment failure surfaces error on "+string(ev), func(t *testing.T) {
			t.Parallel()
			client := newFakeLinearClient()
			client.hasGitHubErr = errors.New("network hiccup")
			svc, _, events := buildTestService(t, client)

			err := svc.HandleStateTransition(context.Background(), MilestoneInput{
				Event:      ev,
				Session:    newSession(),
				Link:       newPrimaryLink(),
				IssueID:    "linear-issue-id",
				IssueIdent: "ACS-1",
			})
			if err == nil {
				t.Fatal("HandleStateTransition must surface HasGitHubIntegrationAttachment errors so the worker retries")
			}
			if client.updateStateCalls != 0 {
				t.Fatalf("UpdateIssueState must NOT be called when the coexistence guard lookup failed (got %d)", client.updateStateCalls)
			}
			if len(events.inserts) != 0 {
				t.Fatalf("no fire-once claim must be recorded when the guard lookup failed (got %d)", len(events.inserts))
			}
		})
	}
}

// TestHandleStateTransition_FireOnceCollapseDuplicates locks down the
// retry contract: a second call for the same (session, issue, event)
// must observe ErrLinearStateEventExists and skip the Linear API call.
// This is the post-crash protection — without it, a worker that died
// mid-call would re-fire UpdateIssueState on every retry.
func TestHandleStateTransition_FireOnceCollapseDuplicates(t *testing.T) {
	t.Parallel()
	client := newFakeLinearClient()
	svc, _, events := buildTestService(t, client)
	link := newPrimaryLink()
	session := newSession()
	in := MilestoneInput{
		Event:      MilestoneStarted,
		Session:    session,
		Link:       link,
		IssueID:    "linear-issue-id",
		IssueIdent: "ACS-1",
	}

	if err := svc.HandleStateTransition(context.Background(), in); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if client.updateStateCalls != 1 {
		t.Fatalf("first call must UpdateIssueState exactly once, got %d", client.updateStateCalls)
	}
	if err := svc.HandleStateTransition(context.Background(), in); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if client.updateStateCalls != 1 {
		t.Fatalf("retry must NOT UpdateIssueState again (got %d) — fire-once is broken", client.updateStateCalls)
	}
	if len(events.inserts) != 1 {
		t.Fatalf("expected 1 event recorded across retries, got %d", len(events.inserts))
	}
}

func TestHandleStateTransition_PersistsPriorStateID(t *testing.T) {
	t.Parallel()

	client := newFakeLinearClient()
	client.currentIssue = &FetchedIssue{
		StateID:   "state-todo-id",
		StateName: "Todo",
		StateType: "unstarted",
		TeamID:    "team-1",
		TeamKey:   "ACS",
		TeamName:  "App Core",
	}
	client.target = &WorkflowState{ID: "state-started-id", Name: "In Progress", Type: "started"}
	svc, provider, events := buildTestService(t, client)
	link := newPrimaryLink()
	session := newSession()
	in := MilestoneInput{
		Event:      MilestoneStarted,
		Session:    session,
		Link:       link,
		IssueID:    "linear-issue-id",
		IssueIdent: "ACS-1",
	}

	if err := svc.HandleStateTransition(context.Background(), in); err != nil {
		t.Fatalf("HandleStateTransition: %v", err)
	}
	state, err := provider.Get(context.Background(), session.OrgID, link.ID)
	if err != nil {
		t.Fatalf("provider state get: %v", err)
	}
	if state.PriorStateID != "state-todo-id" {
		t.Fatalf("PriorStateID should store the Linear workflow state ID, got %q", state.PriorStateID)
	}
	if len(events.inserts) != 1 || events.inserts[0].TransitionFrom != "Todo" {
		t.Fatalf("audit transition_from should remain the human-readable state name, got %+v", events.inserts)
	}
}

// TestSessionURL_BuildsAbsoluteFromAppBaseURL verifies the URL helper
// returns an absolute URL when AppBaseURL is set and a relative path as
// the explicit fallback. The relative form is reserved for tests where
// the field is intentionally unset; production wiring must always pass a
// FrontendURL via Config so Linear renders clickable links.
func TestSessionURL_BuildsAbsoluteFromAppBaseURL(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	want := "https://app.test.example/sessions/" + id.String()

	configured := &Service{appBaseURL: "https://app.test.example"}
	if got := configured.SessionURL(id); got != want {
		t.Errorf("absolute URL: got %q, want %q", got, want)
	}
	configuredTrimmed := &Service{appBaseURL: "https://app.test.example/"}
	// The constructor trims trailing slashes; assert only that the result
	// doesn't contain a double slash before /sessions/.
	if got := configuredTrimmed.SessionURL(id); strings.Contains(got, "//sessions/") {
		t.Errorf("trailing slash should be normalized, got %q", got)
	}

	bare := &Service{}
	if got := bare.SessionURL(id); got != "/sessions/"+id.String() {
		t.Errorf("relative fallback: got %q", got)
	}
}

// TestMergeLinearProviderState_PointerFlagsRespectNil pins the pointer-
// typed bool semantics: a partial Merge that doesn't touch
// CoexistsWithGitHubIntegration must NOT silently flip it back to false.
// Without the pointer indirection, every recordSkip call would clobber
// the suppression flag and re-enable double cycle/sprint membership.
func TestMergeLinearProviderState_PointerFlagsRespectNil(t *testing.T) {
	t.Parallel()
	current := db.LinearProviderState{
		CoexistsWithGitHubIntegration: db.BoolPtr(true),
		IssueRepoStale:                db.BoolPtr(true),
	}
	patch := db.LinearProviderState{LastSkippedReason: "debounced"}
	merged := db.MergeLinearProviderState(current, patch)
	if merged.CoexistsWithGitHubIntegration == nil || !*merged.CoexistsWithGitHubIntegration {
		t.Fatal("partial merge must NOT clear CoexistsWithGitHubIntegration — pointer-nil means leave alone")
	}
	if merged.IssueRepoStale == nil || !*merged.IssueRepoStale {
		t.Fatal("partial merge must NOT clear IssueRepoStale — pointer-nil means leave alone")
	}
	if merged.LastSkippedReason != "debounced" {
		t.Fatalf("LastSkippedReason should propagate, got %q", merged.LastSkippedReason)
	}
}

// TestMergeLinearProviderState_PointerFlagsExplicitFalseClears pins the
// other half of the pointer contract: a non-nil patch with *false must
// flip the field off. This is the "repair from stale" path.
func TestMergeLinearProviderState_PointerFlagsExplicitFalseClears(t *testing.T) {
	t.Parallel()
	current := db.LinearProviderState{IssueRepoStale: db.BoolPtr(true)}
	patch := db.LinearProviderState{IssueRepoStale: db.BoolPtr(false)}
	merged := db.MergeLinearProviderState(current, patch)
	if merged.IssueRepoStale == nil || *merged.IssueRepoStale {
		t.Fatal("explicit *false must overwrite — repair-from-stale must work")
	}
}

// TestLinearAutomationSettings_PerTeamOverridesOrgDefault locks down the
// team-override semantics design 62 promises: an explicit per-team *false
// disables writes for that team only, while other teams keep the org
// default. Without this, the per-team UI would silently reset on save.
func TestLinearAutomationSettings_PerTeamOverridesOrgDefault(t *testing.T) {
	t.Parallel()
	tt := true
	ff := false
	settings := models.LinearAutomationSettings{
		PostSessionLinks:   &tt,
		MoveWorkflowStates: &tt,
		PerTeam: map[string]models.LinearTeamAutomationOverride{
			"OFF": {PostSessionLinks: &ff, MoveWorkflowStates: &ff},
		},
	}
	if !settings.PostSessionLinksFor("UNRELATED") {
		t.Error("teams without overrides must inherit the org default (true)")
	}
	if settings.PostSessionLinksFor("OFF") {
		t.Error("explicit per-team false must be respected for PostSessionLinks")
	}
	if settings.MoveWorkflowStatesFor("OFF") {
		t.Error("explicit per-team false must be respected for MoveWorkflowStates")
	}
	if !settings.MoveWorkflowStatesFor("UNRELATED") {
		t.Error("teams without overrides must inherit the org default (true)")
	}
}

// TestTeamKeyAllowlist_CachesAcrossCalls verifies the in-process cache
// suppresses repeated DB hits for the same org. Detection runs on every
// session create; without the cache, hot orgs hammer linear_team_keys.
// We exercise the cache via the public TeamKeyAllowlist API.
func TestTeamKeyAllowlist_CachesAcrossCalls(t *testing.T) {
	t.Parallel()
	svc := &Service{teamKeys: nil, teamKeyCache: NewTeamKeyCache()} // nil-safe: returns empty allow without hitting cache
	got, err := svc.TeamKeyAllowlist(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("nil teamKeys store must return empty allow, got %v", got)
	}

	// Direct cache exercise — bypass the DB by writing through the cache
	// directly so we can assert get-after-put returns the same map.
	id := uuid.New()
	allow := map[string]bool{"ACS": true}
	svc.teamKeyCache.put(id, allow)
	cached, ok := svc.teamKeyCache.get(id)
	if !ok || cached["ACS"] != true {
		t.Fatalf("cache get-after-put failed: ok=%v cached=%v", ok, cached)
	}
	svc.teamKeyCache.invalidate(id)
	if _, ok := svc.teamKeyCache.get(id); ok {
		t.Fatal("invalidate must clear the entry")
	}
}
